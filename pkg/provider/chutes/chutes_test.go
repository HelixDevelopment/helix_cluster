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
//
// The injected sleep does NOT actually sleep so tests remain fast. For tests
// that need real ctx-interruptible behavior, use newRealSleepClient instead.
func newTestClient(t *testing.T, baseURL, apiKey string, slept *[]time.Duration) *Client {
	t.Helper()
	var mu sync.Mutex
	c, err := New(Config{
		BaseURL:     baseURL,
		APIKey:      apiKey,
		BaseBackoff: 1 * time.Millisecond,
		MaxRetries:  3,
		sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			*slept = append(*slept, d)
			mu.Unlock()
			// Intentionally do NOT actually sleep: keep the test fast.
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	return c
}

// newRealSleepClient builds a Client that uses contextSleep (the real
// production interruptible sleep) so ctx-cancellation tests exercise the
// actual select branch. slept receives durations requested (not necessarily
// waited in full if ctx is cancelled).
func newRealSleepClient(t *testing.T, baseURL, apiKey string, slept *[]time.Duration) *Client {
	t.Helper()
	var mu sync.Mutex
	c, err := New(Config{
		BaseURL:     baseURL,
		APIKey:      apiKey,
		BaseBackoff: 10 * time.Second, // large so cancellation always fires first
		MaxRetries:  3,
		sleep: func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			*slept = append(*slept, d)
			mu.Unlock()
			// Use the real contextSleep so ctx.Done() is actually selected on.
			return contextSleep(ctx, d)
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

// TestCtxCancelDuringBackoff proves CLOSURE CRITERION #1 (ctx-interruptible
// backoff): cancelling the context DURING a backoff window returns promptly
// with a context error, not after the full sleep duration.
//
// MUTATION GUARD: if the ctx.Done() select branch is removed from contextSleep
// (i.e. the function uses time.Sleep unconditionally), the test blocks for the
// full 10 s BaseBackoff and the 500 ms deadline fires — the test then FAILS
// with a timeout or deadline exceeded error. This proves the select is the
// load-bearing guard.
func TestCtxCancelDuringBackoff(t *testing.T) {
	// The server always returns 429 so the client enters the backoff path.
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer srv.Close()

	var slept []time.Duration
	// newRealSleepClient sets BaseBackoff = 10 s, so without ctx interruption
	// the first retry sleep would block for 10 s.
	c := newRealSleepClient(t, srv.URL+"/v1", "cpk_x", &slept)

	// Cancel the context shortly after the first 429 is received but while the
	// backoff sleep is still in progress.
	ctx, cancel := context.WithCancel(context.Background())

	// Fire cancellation 50 ms after the server first responds with 429.
	// The backoff sleep is 10 s, so this should interrupt it ~9.95 s early.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.CreateChatCompletion(ctx, ChatCompletionRequest{Model: "m"})
	elapsed := time.Since(start)

	// The call MUST return an error (context was cancelled).
	if err == nil {
		t.Fatal("expected error after ctx cancel, got nil")
	}
	// The error must wrap a context error.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected error wrapping context.Canceled, got: %v", err)
	}
	// CLAUDE-1 / closure criterion: must return WELL UNDER the full 10 s backoff.
	// We allow up to 500 ms to account for scheduling jitter; 10 s would mean
	// the select is missing and time.Sleep was used instead.
	const maxAllowed = 500 * time.Millisecond
	if elapsed >= maxAllowed {
		t.Fatalf("CreateChatCompletion took %v after ctx cancel; expected < %v (ctx-interruptible backoff broken)", elapsed, maxAllowed)
	}
	t.Logf("returned in %v after ctx cancel (well under %v backoff) — ctx-interruptible backoff confirmed", elapsed, 10*time.Second)
}

// TestRetryAfterHonored proves CLOSURE CRITERION #2: on 429 with a
// Retry-After: N header, the client waits >= N (via max(serverHint, linear))
// instead of ignoring the header.
//
// Strategy: inject a recording sleep that does NOT actually wait but records
// the duration passed to it. Assert that on the retry following a 429+Retry-After
// response the sleep duration equals max(serverHint, linear) == serverHint when
// serverHint > linear.
func TestRetryAfterHonored(t *testing.T) {
	const retryAfterSecs = 5 // server asks client to wait 5 s

	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if n == 1 {
			// First attempt: 429 with a Retry-After hint.
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"rate limited"}`)
			return
		}
		// Second attempt: success.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, okBody)
	}))
	defer srv.Close()

	// Use BaseBackoff = 1 ms so the linear component (1*1ms = 1ms) is tiny
	// compared to the server hint (5 s = 5000 ms). The sleep must be
	// max(5 s, 1 ms) = 5 s.
	var slept []time.Duration
	var mu sync.Mutex
	c, err := New(Config{
		BaseURL:     srv.URL + "/v1",
		APIKey:      "cpk_x",
		BaseBackoff: 1 * time.Millisecond,
		MaxRetries:  3,
		sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
			// Do NOT actually sleep; we only care about the requested duration.
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("CreateChatCompletion: unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "hello from chutes" {
		t.Fatalf("unexpected completion content: %q", resp.Choices[0].Message.Content)
	}

	// Exactly one backoff sleep occurred (between attempt 0 and attempt 1).
	mu.Lock()
	defer mu.Unlock()
	if len(slept) != 1 {
		t.Fatalf("expected 1 sleep, got %d (%v)", len(slept), slept)
	}

	// The sleep duration must be >= retryAfterSecs (the server hint dominates).
	// Linear would be 1 ms; server hint is 5 s. We assert >= 5 s exactly.
	want := time.Duration(retryAfterSecs) * time.Second
	if slept[0] < want {
		t.Fatalf("sleep duration = %v, want >= %v (Retry-After not honored)", slept[0], want)
	}
	t.Logf("sleep duration = %v >= %v (Retry-After honored, max chosen over linear backoff)", slept[0], want)
}

// TestParseRetryAfter unit-tests the Retry-After header parser directly,
// covering delta-seconds, HTTP-date, empty, and invalid inputs.
func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		// wantMin / wantMax bracket the expected duration for date-form tests;
		// wantExact is used for delta-second and degenerate forms.
		wantExact time.Duration
		wantMin   time.Duration
		wantMax   time.Duration
		dateForm  bool
	}{
		{name: "zero", input: "0", wantExact: 0},
		{name: "delta 30", input: "30", wantExact: 30 * time.Second},
		{name: "delta 1", input: "1", wantExact: 1 * time.Second},
		{name: "delta 3600", input: "3600", wantExact: 3600 * time.Second},
		{name: "empty", input: "", wantExact: 0},
		{name: "garbage", input: "not-a-date-or-number", wantExact: 0},
		{name: "negative delta", input: "-5", wantExact: 0},
		// HTTP-date form: a date ~120s in the future must yield a duration
		// within a window that tolerates clock/runtime skew. This exercises the
		// time.RFC1123 parse + time.Until branch (lines 326-336).
		{
			name:     "http-date future",
			input:    time.Now().Add(120 * time.Second).UTC().Format(time.RFC1123),
			dateForm: true,
			wantMin:  90 * time.Second,
			wantMax:  150 * time.Second,
		},
		// HTTP-date form in the PAST: time.Until(t) is negative, so the d<0
		// clamp (lines 333-335) must return exactly 0.
		{
			name:      "http-date past",
			input:     time.Now().Add(-120 * time.Second).UTC().Format(time.RFC1123),
			wantExact: 0,
		},
		// A syntactically date-shaped but unparseable value must fall through to
		// the trailing return 0.
		{name: "malformed date", input: "Mon, 99 Foo 9999 99:99:99 GMT", wantExact: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryAfter(tc.input)
			if tc.dateForm {
				if got < tc.wantMin || got > tc.wantMax {
					t.Fatalf("parseRetryAfter(%q) = %v, want [%v, %v]", tc.input, got, tc.wantMin, tc.wantMax)
				}
			} else {
				if got != tc.wantExact {
					t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.input, got, tc.wantExact)
				}
			}
		})
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
