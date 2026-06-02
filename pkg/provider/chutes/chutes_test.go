package chutes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient builds a Client pointed at srv with a near-zero injected
// backoff so retry tests run fast and deterministically. It records how long
// the sleep function was asked to wait so backoff can be asserted.
func newTestClient(t *testing.T, baseURL, apiKey string, slept *[]time.Duration) *Client {
	t.Helper()
	var mu sync.Mutex
	c, err := New(Config{
		BaseURL:     baseURL,
		APIKey:      apiKey,
		BaseBackoff: 1 * time.Millisecond,
		MaxRetries:  3,
		sleep: func(d time.Duration) {
			mu.Lock()
			*slept = append(*slept, d)
			mu.Unlock()
			// Intentionally do NOT actually sleep: keep the test fast.
		},
	})
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	return c
}

const okBody = `{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "test-model",
  "choices": [
    {"index": 0, "message": {"role": "assistant", "content": "hello from chutes"}, "finish_reason": "stop"}
  ],
  "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
}`

// TestRetriesOn429ThenSucceeds proves CLOSURE CRITERION #1 and the auth header
// (#2): the server returns 429 once, then 200. The client MUST retry (server
// sees 2 requests), apply backoff, send the bearer key, and return the parsed
// completion content read off the wire.
//
// MUTATION GUARD: if the 429-retry branch is removed (i.e. 429 falls through to
// the typed-error default), the client returns after the FIRST request — the
// server sees only 1 request and CreateChatCompletion returns an error instead
// of the completion. Both assertions below then fail.
func TestRetriesOn429ThenSucceeds(t *testing.T) {
	var requests int32
	var gotAuth string
	var gotBody []byte
	var muAuth sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		muAuth.Lock()
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		muAuth.Unlock()

		if n == 1 {
			// First attempt: backpressure.
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, okBody)
	}))
	defer srv.Close()

	var slept []time.Duration
	c := newTestClient(t, srv.URL+"/v1", "cpk_secrettoken", &slept)

	resp, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletion: unexpected error: %v", err)
	}

	// Closure #1: the client retried — the server saw exactly 2 requests.
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected server to see 2 requests (1 x 429 + 1 x 200), got %d", got)
	}

	// Closure #1: backoff was applied before the retry.
	if len(slept) != 1 {
		t.Fatalf("expected exactly 1 backoff sleep before retry, got %d (%v)", len(slept), slept)
	}
	if slept[0] <= 0 {
		t.Fatalf("expected a positive backoff duration, got %v", slept[0])
	}

	// Closure #1: parsed completion content came off the wire.
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if got := resp.Choices[0].Message.Content; got != "hello from chutes" {
		t.Fatalf("completion content = %q, want %q", got, "hello from chutes")
	}
	if resp.Usage.TotalTokens != 8 {
		t.Fatalf("usage.total_tokens = %d, want 8", resp.Usage.TotalTokens)
	}
	if resp.ID != "chatcmpl-abc123" {
		t.Fatalf("id = %q, want chatcmpl-abc123", resp.ID)
	}

	// Closure #2: the bearer key was actually sent.
	muAuth.Lock()
	defer muAuth.Unlock()
	if gotAuth != "Bearer cpk_secrettoken" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer cpk_secrettoken")
	}

	// Sanity: the request body was real JSON carrying our model.
	var sent ChatCompletionRequest
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("server received non-JSON request body: %v (%s)", err, gotBody)
	}
	if sent.Model != "test-model" || len(sent.Messages) != 1 || sent.Messages[0].Content != "hi" {
		t.Fatalf("server received unexpected request: %+v", sent)
	}
}

// TestPersistentNon2xxReturnsTypedError proves CLOSURE CRITERION #3: a
// persistent non-2xx (here 400, which is NOT retryable) returns a typed
// *APIError, never a fabricated response.
func TestPersistentNon2xxReturnsTypedError(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad model"}`)
	}))
	defer srv.Close()

	var slept []time.Duration
	c := newTestClient(t, srv.URL+"/v1", "cpk_x", &slept)

	resp, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{Model: "nope"})
	if resp != nil {
		t.Fatalf("expected nil response on error, got %+v", resp)
	}
	if err == nil {
		t.Fatal("expected an error for HTTP 400, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("APIError.StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "bad model") {
		t.Fatalf("APIError.Body = %q, want it to contain server body", apiErr.Body)
	}
	// 400 is not retryable: exactly one request, no backoff.
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected exactly 1 request for non-retryable 400, got %d", got)
	}
	if len(slept) != 0 {
		t.Fatalf("expected no backoff for non-retryable status, got %v", slept)
	}
}

// TestPersistent429ExhaustsRetries proves that a server that ALWAYS returns 429
// eventually surfaces a typed *APIError after the bounded retry budget — and
// that the total attempts == 1 + MaxRetries (no infinite loop, no fake PASS).
func TestPersistent429ExhaustsRetries(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"still rate limited"}`)
	}))
	defer srv.Close()

	var slept []time.Duration
	c := newTestClient(t, srv.URL+"/v1", "cpk_x", &slept) // MaxRetries=3

	resp, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{Model: "m"})
	if resp != nil {
		t.Fatalf("expected nil response, got %+v", resp)
	}
	if err == nil {
		t.Fatal("expected an error after exhausting retries, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected wrapped *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("APIError.StatusCode = %d, want 429", apiErr.StatusCode)
	}
	// 1 initial + 3 retries == 4 attempts.
	if got := atomic.LoadInt32(&requests); got != 4 {
		t.Fatalf("expected 4 total attempts (1 + 3 retries), got %d", got)
	}
	// One backoff per retry; durations grow linearly.
	if len(slept) != 3 {
		t.Fatalf("expected 3 backoff sleeps, got %d (%v)", len(slept), slept)
	}
	if !(slept[0] < slept[1] && slept[1] < slept[2]) {
		t.Fatalf("expected strictly increasing linear backoff, got %v", slept)
	}
}

// TestNewRejectsEmptyAPIKey proves the constructor refuses an unauthenticated
// client rather than silently issuing keyless requests.
func TestNewRejectsEmptyAPIKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{APIKey: tc.key})
			if !errors.Is(err, ErrEmptyAPIKey) {
				t.Fatalf("New(apiKey=%q): err = %v, want ErrEmptyAPIKey", tc.key, err)
			}
		})
	}
}

// TestNewDefaultsBaseURL proves an empty BaseURL falls back to DefaultBaseURL
// (the Chutes /v1 root) rather than producing a client that posts to nowhere.
func TestNewDefaultsBaseURL(t *testing.T) {
	c, err := New(Config{APIKey: "cpk_x"})
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if c.base != DefaultBaseURL {
		t.Fatalf("default base = %q, want %q", c.base, DefaultBaseURL)
	}
}

// TestSuccessFirstTrySendsAuthAndNoBackoff proves the happy path: a 200 on the
// first attempt sends the bearer key, makes exactly one request, and applies no
// backoff.
func TestSuccessFirstTrySendsAuthAndNoBackoff(t *testing.T) {
	var requests int32
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, okBody)
	}))
	defer srv.Close()

	var slept []time.Duration
	c := newTestClient(t, srv.URL+"/v1", "cpk_live", &slept)

	resp, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("CreateChatCompletion: %v", err)
	}
	if resp.Choices[0].Message.Content != "hello from chutes" {
		t.Fatalf("unexpected content: %q", resp.Choices[0].Message.Content)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected 1 request on happy path, got %d", got)
	}
	if len(slept) != 0 {
		t.Fatalf("expected no backoff on happy path, got %v", slept)
	}
	if gotAuth != "Bearer cpk_live" {
		t.Fatalf("Authorization = %q, want Bearer cpk_live", gotAuth)
	}
}
