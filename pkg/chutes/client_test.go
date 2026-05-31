package chutes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCompletionServer returns an httptest server that answers
// /v1/chat/completions with a canned completion echoing `content`, recording
// how many times it was hit. It captures the last seen Authorization header.
func fakeCompletionServer(t *testing.T, content string, hits *int32, authSeen *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if authSeen != nil {
			*authSeen = r.Header.Get("Authorization")
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp := ChatResponse{
			ID:    "cmpl-test",
			Model: req.Model,
			Choices: []Choice{{
				Index:        0,
				Message:      Message{Role: "assistant", Content: content},
				FinishReason: "stop",
			}},
			Usage: Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// statusServer returns a server that always replies with the given status code.
func statusServer(t *testing.T, code int, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		http.Error(w, "synthetic", code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestChatCompletionRealCompletion proves the client gets a REAL parsed
// completion from a fake /v1/chat/completions: exact content, model, usage, and
// the served endpoint name.
//
// MUTATION: in attempt(), change `json.Unmarshal(raw, &parsed)` to leave parsed
// zero (e.g. skip the unmarshal). resp.Content() would be "" and the
// "hello from primary" assertion fails.
func TestChatCompletionRealCompletion(t *testing.T) {
	t.Parallel()
	var hits int32
	var auth string
	srv := fakeCompletionServer(t, "hello from primary", &hits, &auth)

	c, err := NewClient(Config{Endpoints: []Endpoint{
		{Name: "primary", BaseURL: srv.URL, APIKey: "sk-secret"},
	}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := c.ChatCompletion(context.Background(), ChatRequest{
		Model:    "qwen-2.5",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := resp.Content(); got != "hello from primary" {
		t.Fatalf("content = %q, want %q", got, "hello from primary")
	}
	if resp.Endpoint != "primary" {
		t.Fatalf("served endpoint = %q, want primary", resp.Endpoint)
	}
	if resp.Model != "qwen-2.5" {
		t.Fatalf("model = %q, want qwen-2.5", resp.Model)
	}
	if resp.Usage.TotalTokens != 8 {
		t.Fatalf("total tokens = %d, want 8", resp.Usage.TotalTokens)
	}
	if auth != "Bearer sk-secret" {
		t.Fatalf("Authorization = %q, want Bearer sk-secret", auth)
	}
}

// TestChatCompletionFallsBackOn503 proves that when the preferred endpoint
// returns 503 the client FALLS BACK to the second endpoint and returns ITS
// content, naming the second endpoint as the server.
//
// MUTATION: in ChatCompletion, drop the `if retryableStatus(status) { ...;
// continue }` branch (treat 503 like a terminal response). The client would
// stop at the first endpoint; resp.Endpoint=="secondary" and the
// "served by secondary" content assertion both fail.
func TestChatCompletionFallsBackOn503(t *testing.T) {
	t.Parallel()
	var downHits, upHits int32
	down := statusServer(t, http.StatusServiceUnavailable, &downHits)
	up := fakeCompletionServer(t, "served by secondary", &upHits, nil)

	c, err := NewClient(Config{Endpoints: []Endpoint{
		{Name: "primary", BaseURL: down.URL},
		{Name: "secondary", BaseURL: up.URL},
	}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := c.ChatCompletion(context.Background(), ChatRequest{
		Model:    "llama-3",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Endpoint != "secondary" {
		t.Fatalf("served endpoint = %q, want secondary (no fallback happened)", resp.Endpoint)
	}
	if resp.Content() != "served by secondary" {
		t.Fatalf("content = %q, want served by secondary", resp.Content())
	}
	if atomic.LoadInt32(&downHits) != 1 {
		t.Fatalf("primary hits = %d, want 1 (must be tried first)", downHits)
	}
	if atomic.LoadInt32(&upHits) != 1 {
		t.Fatalf("secondary hits = %d, want 1 (fallback must occur)", upHits)
	}
}

// TestChatCompletionNonRetryable4xxNoFallback proves a 401 is surfaced
// immediately WITHOUT burning the rest of the chain — falling back on a bad key
// would be wrong.
//
// MUTATION: change retryableStatus to `code >= 400` (treat 401 as retryable).
// The client would then fall back to the healthy second server and succeed, so
// the expected error becomes nil and secondHits becomes 1 — both asserts fail.
func TestChatCompletionNonRetryable4xxNoFallback(t *testing.T) {
	t.Parallel()
	var firstHits, secondHits int32
	bad := statusServer(t, http.StatusUnauthorized, &firstHits)
	good := fakeCompletionServer(t, "should-not-be-used", &secondHits, nil)

	c, _ := NewClient(Config{Endpoints: []Endpoint{
		{Name: "primary", BaseURL: bad.URL},
		{Name: "secondary", BaseURL: good.URL},
	}})
	_, err := c.ChatCompletion(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("expected non-retryable 401 error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want it to mention 401", err)
	}
	if atomic.LoadInt32(&secondHits) != 0 {
		t.Fatalf("secondary hits = %d, want 0 (must NOT fall back on 401)", secondHits)
	}
}

// TestChatCompletionAllFail proves that when every endpoint is unreachable the
// client returns ErrAllEndpointsFailed wrapping the last cause.
//
// MUTATION: in ChatCompletion, return `nil` instead of the wrapped
// ErrAllEndpointsFailed after the loop. errors.Is would no longer match and the
// assertion fails.
func TestChatCompletionAllFail(t *testing.T) {
	t.Parallel()
	// Two servers that we close immediately to force transport errors.
	s1 := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s2 := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url1, url2 := s1.URL, s2.URL
	s1.Close()
	s2.Close()

	c, _ := NewClient(Config{Endpoints: []Endpoint{
		{Name: "a", BaseURL: url1},
		{Name: "b", BaseURL: url2},
	}})
	_, err := c.ChatCompletion(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, ErrAllEndpointsFailed) {
		t.Fatalf("error = %v, want ErrAllEndpointsFailed", err)
	}
}

// TestChatCompletionTimeoutFallsBack proves a slow primary that exceeds the
// per-attempt timeout falls back to a fast secondary.
//
// MUTATION: delete the `context.WithTimeout` block in attempt(). The primary's
// slow handler would eventually answer 200 and serve, so resp.Endpoint would be
// "slow" instead of "fast".
func TestChatCompletionTimeoutFallsBack(t *testing.T) {
	t.Parallel()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(slow.Close)
	var fastHits int32
	fast := fakeCompletionServer(t, "fast-content", &fastHits, nil)

	c, _ := NewClient(Config{
		Timeout: 50 * time.Millisecond,
		Endpoints: []Endpoint{
			{Name: "slow", BaseURL: slow.URL},
			{Name: "fast", BaseURL: fast.URL},
		},
	})
	resp, err := c.ChatCompletion(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Endpoint != "fast" {
		t.Fatalf("served endpoint = %q, want fast (timeout fallback failed)", resp.Endpoint)
	}
}

// TestNewClientNoEndpoints proves a misconfigured client is rejected.
//
// MUTATION: make NewClient return (&Client{}, nil) for an empty chain. The
// errors.Is(ErrNoEndpoints) assertion fails.
func TestNewClientNoEndpoints(t *testing.T) {
	t.Parallel()
	_, err := NewClient(Config{})
	if !errors.Is(err, ErrNoEndpoints) {
		t.Fatalf("error = %v, want ErrNoEndpoints", err)
	}
}

// TestChatCompletionValidation proves empty messages / model are rejected before
// any network call.
//
// MUTATION: remove the `len(req.Messages)==0` guard. The request would be sent
// and the ErrEmptyMessages assertion fails.
func TestChatCompletionValidation(t *testing.T) {
	t.Parallel()
	c, _ := NewClient(Config{Endpoints: []Endpoint{{Name: "x", BaseURL: "http://127.0.0.1:1"}}})
	if _, err := c.ChatCompletion(context.Background(), ChatRequest{Model: "m"}); !errors.Is(err, ErrEmptyMessages) {
		t.Fatalf("error = %v, want ErrEmptyMessages", err)
	}
	if _, err := c.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}); !errors.Is(err, ErrNoModel) {
		t.Fatalf("error = %v, want ErrNoModel", err)
	}
}
