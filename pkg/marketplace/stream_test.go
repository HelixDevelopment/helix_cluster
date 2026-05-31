package marketplace

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseBody is a canonical Chutes-style SSE token stream: each token on its own
// `data:` JSON event, interleaved with the noise a real event-stream carries
// (comments, an event: field, blank-line event boundaries), terminated by the
// [DONE] sentinel. The decoder must yield EXACTLY ["The", " quick", " brown",
// " fox"] in this order and stop cleanly at [DONE].
const sseBody = ": keep-alive comment\n" +
	"event: token\n" +
	"data: {\"token\":\"The\"}\n" +
	"\n" +
	"data: {\"token\":\" quick\"}\n" +
	"\n" +
	"data: {\"token\":\" brown\"}\n" +
	"\n" +
	"data: {\"token\":\" fox\"}\n" +
	"\n" +
	"data: [DONE]\n"

func eqTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTokenStream_YieldsExactOrderedTokens proves the SSE decoder yields EXACT
// ordered tokens from a real event-stream body, skipping comments/event lines/
// blank boundaries and stopping at [DONE].
//
// Mutation that this kills (ordering): if Next batched or reversed events — e.g.
// collecting payloads into a map or prepending instead of appending — the slice
// would not equal the pinned ["The"," quick"," brown"," fox"] sequence and this
// assertion FAILS. Mutation that this kills (framing): if the decoder forgot to
// strip the `data:` prefix or the optional leading space, the decoded JSON would
// be malformed and Collect would return an error instead of the four tokens.
func TestTokenStream_YieldsExactOrderedTokens(t *testing.T) {
	s := NewTokenStream(io.NopCloser(strings.NewReader(sseBody)))
	defer s.Close()

	got, err := Collect(s)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := []string{"The", " quick", " brown", " fox"}
	if !eqTokens(got, want) {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
}

// TestTokenStream_Next_Incremental proves the incremental Next() path returns
// tokens one at a time in order and signals ErrStreamClosed exactly once at the
// end, then keeps returning ErrStreamClosed (idempotent stop).
//
// Mutation that this kills: if Next did not set s.done on [DONE]/EOF, the second
// post-close call would block or re-read, and the "fourth Next is ' fox'" /
// "fifth Next is ErrStreamClosed" ordered assertions FAIL.
func TestTokenStream_Next_Incremental(t *testing.T) {
	s := NewTokenStream(io.NopCloser(strings.NewReader(sseBody)))
	defer s.Close()

	want := []string{"The", " quick", " brown", " fox"}
	for i, w := range want {
		tok, err := s.Next()
		if err != nil {
			t.Fatalf("Next #%d: %v", i, err)
		}
		if tok != w {
			t.Fatalf("Next #%d = %q, want %q", i, tok, w)
		}
	}
	// End-of-stream is signalled, and stays signalled.
	if _, err := s.Next(); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Next after last token = %v, want ErrStreamClosed", err)
	}
	if _, err := s.Next(); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("repeat Next after close = %v, want ErrStreamClosed", err)
	}
}

// TestTokenStream_MalformedData_Surfaces proves a malformed data payload is
// surfaced as a decode error rather than silently dropped or yielded as an empty
// token.
//
// Mutation that this kills: if Next ignored json.Unmarshal's error and returned
// chunk.Token anyway, the malformed event would yield "" with no error and this
// "expected decode error" assertion FAILS.
func TestTokenStream_MalformedData_Surfaces(t *testing.T) {
	body := "data: {not valid json}\n"
	s := NewTokenStream(io.NopCloser(strings.NewReader(body)))
	defer s.Close()
	if _, err := s.Next(); err == nil {
		t.Fatalf("malformed data accepted, want decode error")
	}
}

// TestOpenTokenStream_OverHTTP proves the full HTTP+SSE path: a local httptest
// server emits a text/event-stream and OpenTokenStream decodes the exact ordered
// tokens. This is the honest stand-in for the live Chutes streaming endpoint.
//
// Mutation that this kills: if OpenTokenStream dropped the status guard and the
// server returned non-200, Collect would still run against an error body; and if
// it failed to forward ctx or read the body, the four-token ordered assertion
// FAILS.
func TestOpenTokenStream_OverHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseBody)
	}))
	defer srv.Close()

	s, err := OpenTokenStream(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer s.Close()

	got, err := Collect(s)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := []string{"The", " quick", " brown", " fox"}
	if !eqTokens(got, want) {
		t.Fatalf("tokens over HTTP = %q, want %q", got, want)
	}
}

// TestOpenTokenStream_Non200_Surfaces proves the streaming path REALLY surfaces a
// non-200 status (e.g. 503) instead of handing back a TokenStream over an error
// body.
//
// Mutation that this kills: remove the `if resp.StatusCode != http.StatusOK`
// guard in OpenTokenStream. Then it would return a non-nil stream over the error
// body and err==nil, so the "expected non-nil error / nil stream" assertions
// FAIL.
func TestOpenTokenStream_Non200_Surfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s, err := OpenTokenStream(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatalf("OpenTokenStream accepted 503, want error")
	}
	if s != nil {
		t.Fatalf("OpenTokenStream returned a stream alongside the error: %v", s)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error %q does not carry the upstream 503 status", err.Error())
	}
}
