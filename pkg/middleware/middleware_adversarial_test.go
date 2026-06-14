package middleware

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// silenceLog redirects the standard logger to a buffer for the duration of a
// test so adversarial panic/recover probes do not spam test output, and returns
// the buffer for assertions.
func silenceLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// ---------------------------------------------------------------------------
// CENTERPIECE A: RecoverMiddleware actually CATCHES the panic.
//
// If recover() were removed from production, the panic would unwind through
// ServeHTTP into this test goroutine and crash the whole `go test` process
// (not just fail this test). The fact that these assertions are reachable at
// all is itself the load-bearing proof the panic was caught; we additionally
// assert the documented 500 status + body.
// ---------------------------------------------------------------------------

func TestRecoverCatchesPanicVariousValues(t *testing.T) {
	cases := []struct {
		name  string
		panic func()
	}{
		{"string", func() { panic("string boom") }},
		{"error", func() { panic(fmt.Errorf("error boom")) }},
		{"int", func() { panic(42) }},
		{"nil-deref-runtime", func() {
			var p *int
			_ = *p // genuine runtime panic, not an explicit panic() call
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			silenceLog(t)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tc.panic()
			})
			mw := RecoverMiddleware(handler)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rr := httptest.NewRecorder()

			// If the panic escapes (mutation: recover removed), this call
			// propagates the panic and crashes the test binary. Reaching the
			// assertions below proves the panic was caught.
			mw.ServeHTTP(rr, req)

			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("expected 500 after recovering %s panic, got %d", tc.name, rr.Code)
			}
			// http.Error writes the status text + newline as the body.
			if got := strings.TrimSpace(rr.Body.String()); got != http.StatusText(http.StatusInternalServerError) {
				t.Errorf("expected body %q, got %q", http.StatusText(http.StatusInternalServerError), got)
			}
		})
	}
}

// A panic must not leave the client with a 200/0 status: the recover path MUST
// write a non-2xx. This bites a mutation that recovers but writes 200.
func TestRecoverWritesErrorStatusNotSuccess(t *testing.T) {
	silenceLog(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("no success allowed")
	})
	mw := RecoverMiddleware(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code < 500 || rr.Code > 599 {
		t.Fatalf("panic recovery must yield a 5xx status, got %d", rr.Code)
	}
}

// RECOVER WRAPS CHAIN: a panic raised in an INNER middleware (not the final
// handler) must still be caught by an OUTER RecoverMiddleware composed via Chain.
func TestRecoverCatchesPanicFromInnerMiddleware(t *testing.T) {
	silenceLog(t)

	panickingMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("inner middleware exploded before reaching handler")
		})
	}
	handlerReached := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	})

	// Outermost first: Recover wraps panickingMW wraps handler.
	chained := Chain(handler, RecoverMiddleware, panickingMW)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	chained.ServeHTTP(rr, req) // must not crash the process

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected outer Recover to convert inner-mw panic to 500, got %d", rr.Code)
	}
	if handlerReached {
		t.Error("handler should never have been reached after inner middleware panicked")
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE B: Chain composition ORDER (outermost-first), both the request
// (descending) phase and the response (unwinding) phase, using response-header
// markers as an independent oracle in addition to a slice.
// ---------------------------------------------------------------------------

func TestChainOrderOutermostFirstBothPhases(t *testing.T) {
	var order []string
	marker := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name+"-in")
				w.Header().Add("X-Order", name+"-in")
				next.ServeHTTP(w, r)
				order = append(order, name+"-out")
				w.Header().Add("X-Order", name+"-out")
			})
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
		w.Header().Add("X-Order", "handler")
		w.WriteHeader(http.StatusOK)
	})

	// Chain(h, A, B, C) must execute A outermost, C innermost:
	// request phase  A-in, B-in, C-in, handler
	// response phase C-out, B-out, A-out
	chained := Chain(handler, marker("A"), marker("B"), marker("C"))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	chained.ServeHTTP(rr, req)

	want := []string{"A-in", "B-in", "C-in", "handler", "C-out", "B-out", "A-out"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("chain order wrong.\n want %v\n got  %v", want, order)
	}
	// Independent oracle: the response header recorded the same sequence.
	gotHdr := rr.Header().Values("X-Order")
	if !reflect.DeepEqual(gotHdr, want) {
		t.Fatalf("chain order via header wrong.\n want %v\n got  %v", want, gotHdr)
	}
	// Pin the directionality explicitly so a reversed-Chain mutation is unambiguous.
	if order[0] != "A-in" {
		t.Errorf("outermost middleware must be the FIRST arg (A), ran %q first", order[0])
	}
	if order[len(order)-1] != "A-out" {
		t.Errorf("outermost middleware must unwind LAST (A-out), got %q", order[len(order)-1])
	}
}

// Empty chain is the identity: Chain(h) returns a handler that behaves exactly
// like h (and is non-nil).
func TestChainEmptyIsIdentity(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("identity"))
	})
	chained := Chain(handler)
	if chained == nil {
		t.Fatal("Chain with no middlewares returned nil")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	chained.ServeHTTP(rr, req)

	if !called {
		t.Fatal("empty chain did not invoke the wrapped handler")
	}
	if rr.Code != http.StatusTeapot || rr.Body.String() != "identity" {
		t.Errorf("empty chain altered behavior: code=%d body=%q", rr.Code, rr.Body.String())
	}
}

// Single-middleware chain wraps exactly once (no double application).
func TestChainSingleMiddlewareWrapsOnce(t *testing.T) {
	count := 0
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count++
			next.ServeHTTP(w, r)
		})
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chained := Chain(handler, mw)
	chained.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if count != 1 {
		t.Errorf("single middleware must run exactly once, ran %d times", count)
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE C: responseRecorder fidelity — status default + byte accounting.
// The recorder is exercised through LoggingMiddleware, which prints
// "<status> <bytes>"; we parse those from the log line as a sink-side oracle.
// ---------------------------------------------------------------------------

// When the handler never calls WriteHeader, the recorder MUST report 200 (the
// net/http default), NOT 0. Mutation (default statusCode 0) bites here.
func TestResponseRecorderDefaultsTo200WhenHeaderNotWritten(t *testing.T) {
	buf := silenceLog(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("implicit-200")) // Write without prior WriteHeader
	})
	mw := LoggingMiddleware(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	// The underlying recorder's reported status (logged) must be 200.
	if !strings.Contains(buf.String(), " 200 ") {
		t.Fatalf("expected logged status 200 for implicit write, log=%q", buf.String())
	}
	// And the byte count for the 12-byte body must appear.
	if !strings.Contains(buf.String(), " 12 ") {
		t.Fatalf("expected logged byte count 12, log=%q", buf.String())
	}
}

// Multiple Write calls must accumulate the TOTAL byte count, not just the last.
func TestResponseRecorderAccumulatesBytesAcrossWrites(t *testing.T) {
	buf := silenceLog(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("aaaa"))   // 4
		_, _ = w.Write([]byte("bbbbbb")) // 6  -> total 10
	})
	mw := LoggingMiddleware(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Body.String() != "aaaabbbbbb" {
		t.Fatalf("body not fully written: %q", rr.Body.String())
	}
	if !strings.Contains(buf.String(), " 10 ") {
		t.Fatalf("expected accumulated byte count 10, log=%q", buf.String())
	}
}

// An explicit WriteHeader status is captured and the first one wins (the
// recorder ignores a second WriteHeader, matching net/http semantics).
func TestResponseRecorderCapturesExplicitStatusFirstWins(t *testing.T) {
	buf := silenceLog(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.WriteHeader(http.StatusOK) // must be ignored
		_, _ = w.Write([]byte("nf"))
	})
	mw := LoggingMiddleware(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected downstream 404, got %d", rr.Code)
	}
	if !strings.Contains(buf.String(), " 404 ") {
		t.Fatalf("expected logged status 404 (first WriteHeader wins), log=%q", buf.String())
	}
	if strings.Contains(buf.String(), " 200 ") {
		t.Errorf("second WriteHeader(200) must be ignored, but 200 appeared in log=%q", buf.String())
	}
}
