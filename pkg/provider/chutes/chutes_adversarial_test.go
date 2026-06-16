package chutes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is an ADVERSARIAL probe of pkg/provider/chutes.
//
// SCOPE NOTE (load-bearing): the recorded "chutes attest HMAC no
// domain-separation" latent risk lives in the SIBLING package pkg/chutes
// (attest.go), NOT here. THIS package (pkg/provider/chutes) is a thin
// OpenAI-compatible inference HTTP client: it has NO attestation, NO
// HMAC/signature Verify path, NO pricing/quote/capacity math. There is
// therefore no fail-open verification gate to forge in this package. The
// genuine adversarial surface here is:
//   1. parseRetryAfter() — parses an UNTRUSTED, server-controlled Retry-After
//      header; numeric overflow (the NaN/Inf/overflow class) is the live risk.
//   2. response decoding — a hostile/garbage 2xx body must ERROR, never panic.
//   3. the backoff max(serverHint, linear) guard — must not be bypassable by a
//      crafted header into an unbounded or negative (instantly-elapsing) wait.
//
// Each test below either PINS documented/correct behavior or proves a finding
// is masked (LATENT RISK), with the masking guard mutation-tested in the report.

// ---------------------------------------------------------------------------
// FINDING 1 (LATENT RISK, masked): parseRetryAfter integer overflow.
//
// parseRetryAfter does `time.Duration(secs) * time.Second` where secs comes
// from strconv.Atoi of a server-controlled header. For secs > ~9.2e9 (about
// 292 years) this OVERFLOWS int64 and yields a NEGATIVE time.Duration. The
// function's doc promises an empty/unparseable value returns zero "so the
// caller gracefully falls back to the linear backoff" — but a HUGE valid
// integer returns a negative duration, not zero. This is a real overflow.
//
// It is NOT exploitable through the only consumer (CreateChatCompletion's
// max(serverHint, linear) guard at line ~216): a negative serverHint never
// exceeds the positive linear backoff, so it is silently discarded and the
// linear backoff is used. We PIN both facts: (a) the overflow IS observable at
// the parseRetryAfter boundary, and (b) it is masked end-to-end.
// ---------------------------------------------------------------------------

func TestParseRetryAfterOverflowProducesNegative(t *testing.T) {
	// 1e10 seconds * 1e9 ns/s overflows int64 -> negative duration.
	got := parseRetryAfter("10000000000")
	if got >= 0 {
		t.Fatalf("parseRetryAfter(1e10) = %v; expected a negative (overflowed) duration "+
			"pinning the documented-as-latent overflow. If this is now >= 0 the overflow "+
			"was fixed (clamp added) — update this pin.", got)
	}
	// Max int64 seconds: also overflows.
	got2 := parseRetryAfter("9223372036854775807")
	if got2 >= 0 {
		t.Fatalf("parseRetryAfter(maxint64) = %v; expected negative (overflow)", got2)
	}
	t.Logf("LATENT RISK pinned: parseRetryAfter(1e10) = %v (overflow to negative)", got)
}

// TestRetryAfterOverflowIsMaskedByBackoffGuard proves the overflow above is
// HARMLESS end-to-end: a server returning 429 with a giant Retry-After that
// overflows to negative must NOT collapse the wait. The client must still apply
// its POSITIVE linear backoff (max(serverHint, linear) picks linear), so the
// negative server hint is discarded. This is the load-bearing masking guard.
//
// MUTATION GUARD: if line ~216 `if lastRetryAfter > delay { delay = lastRetryAfter }`
// is inverted to `<` (or the guard removed so delay = lastRetryAfter), the
// negative overflowed hint becomes the delay and slept[0] goes negative — this
// assertion then fails.
func TestRetryAfterOverflowIsMaskedByBackoffGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always send the overflowing Retry-After on the 429.
		w.Header().Set("Retry-After", "10000000000") // 1e10 s -> overflows to negative
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer srv.Close()

	var slept []time.Duration
	var mu sync.Mutex
	c, err := New(Config{
		BaseURL:     srv.URL + "/v1",
		APIKey:      "cpk_x",
		BaseBackoff: 1 * time.Millisecond,
		MaxRetries:  1,
		sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
			return nil // do not actually sleep
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _ = c.CreateChatCompletion(context.Background(), ChatCompletionRequest{Model: "m"})

	mu.Lock()
	defer mu.Unlock()
	if len(slept) == 0 {
		t.Fatalf("expected at least one backoff sleep before retry, got none")
	}
	// The masking guarantee: every applied delay must be POSITIVE (the linear
	// backoff), never the negative overflowed server hint.
	for i, d := range slept {
		if d <= 0 {
			t.Fatalf("slept[%d] = %v; overflowed (negative) Retry-After leaked into the "+
				"actual backoff delay — max(serverHint, linear) guard is broken", i, d)
		}
	}
	t.Logf("masking confirmed: overflowed Retry-After discarded; applied positive backoff %v", slept)
}

// ---------------------------------------------------------------------------
// FINDING 2 probe (DoS via hostile response body): a 2xx with a garbage /
// truncated / wrong-typed JSON body must return a typed decode ERROR, never
// panic and never fabricate a completion.
// ---------------------------------------------------------------------------

func TestGarbage2xxBodyErrorsNeverPanics(t *testing.T) {
	hostileBodies := []string{
		``,                              // empty
		`{`,                             // truncated object
		`not json at all`,              // pure garbage
		`[1,2,3]`,                       // wrong top-level type (array)
		`{"choices": "not-an-array"}`,  // wrong field type
		`{"usage": {"total_tokens": "x"}}`, // wrong nested type
		strings.Repeat("{", 10000),      // deeply nested-ish garbage
		"\x00\x01\x02\xff",             // raw bytes
	}
	for _, hb := range hostileBodies {
		hb := hb
		t.Run("", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, hb)
			}))
			defer srv.Close()

			var slept []time.Duration
			c := newAdvClient(t, srv.URL+"/v1", "cpk_x", &slept)

			// Must not panic. A malformed body yields either an error or, for an
			// empty/JSON-null body, a zero-value response — never a panic.
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("hostile 2xx body %q PANICKED (DoS): %v", hb, rec)
					}
				}()
				resp, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{Model: "m"})
				if err == nil {
					// json.Unmarshal of a structurally-valid-but-empty doc ("" is
					// invalid; "[1,2,3]" is invalid into a struct). For the ones that
					// DO error we require nil resp; for any that decode to zero value
					// we require that no completion content was fabricated.
					if resp != nil && len(resp.Choices) != 0 {
						t.Fatalf("hostile body %q decoded into fabricated choices: %+v", hb, resp.Choices)
					}
				} else if resp != nil {
					t.Fatalf("error returned but resp non-nil for body %q: %+v", hb, resp)
				}
			}()
		})
	}
}

// ---------------------------------------------------------------------------
// FINDING 3 probe (header injection / odd Retry-After forms): malformed,
// negative, fractional, and whitespace Retry-After values must degrade to the
// documented zero (fall back to linear) and never panic.
// ---------------------------------------------------------------------------

func TestParseRetryAfterAdversarialInputs(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration // exact expected for non-date forms
	}{
		{"", 0},
		{"   ", 0},
		{"-1", 0},                       // negative delta -> 0 (documented)
		{"-9999999999", 0},
		{"1.5", 0},                       // fractional not an int -> 0
		{"0x10", 0},                       // hex not Atoi -> 0
		{"  7  ", 7 * time.Second},        // trimmed
		{"+30", 30 * time.Second},         // Atoi accepts leading +
		{"NaN", 0},
		{"Inf", 0},
		{"99999999999999999999999", 0},   // out-of-range Atoi -> 0 (NOT overflow path)
	}
	for _, tc := range cases {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("parseRetryAfter(%q) PANICKED: %v", tc.in, rec)
				}
			}()
			got := parseRetryAfter(tc.in)
			if got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}()
	}
}

// ---------------------------------------------------------------------------
// FINDING 4 probe (no fabricated completion on any non-2xx): a 3xx/4xx/5xx
// non-2xx response must surface a typed *APIError, never a (nil,nil) or a
// fabricated completion. Pins the "never a fabricated completion" promise.
// ---------------------------------------------------------------------------

func TestNon2xxNeverFabricatesCompletion(t *testing.T) {
	for _, code := range []int{http.StatusMovedPermanently, http.StatusUnauthorized,
		http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable} {
		code := code
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				// A body that, if naively decoded, would look like a real completion.
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"FAKE"}}]}`)
			}))
			defer srv.Close()

			var slept []time.Duration
			c := newAdvClient(t, srv.URL+"/v1", "cpk_x", &slept)

			resp, err := c.CreateChatCompletion(context.Background(), ChatCompletionRequest{Model: "m"})
			if err == nil {
				t.Fatalf("status %d returned nil error (fabrication risk): resp=%+v", code, resp)
			}
			if resp != nil {
				t.Fatalf("status %d returned non-nil completion %+v alongside error", code, resp)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("status %d: expected *APIError, got %T: %v", code, err, err)
			}
			if apiErr.StatusCode != code {
				t.Fatalf("APIError.StatusCode = %d, want %d", apiErr.StatusCode, code)
			}
		})
	}
}

// newAdvClient builds a Client with a non-sleeping injected delay so retry/error
// paths run fast and deterministically.
func newAdvClient(t *testing.T, baseURL, apiKey string, slept *[]time.Duration) *Client {
	t.Helper()
	var mu sync.Mutex
	c, err := New(Config{
		BaseURL:     baseURL,
		APIKey:      apiKey,
		BaseBackoff: 1 * time.Millisecond,
		MaxRetries:  2,
		sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			*slept = append(*slept, d)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}
