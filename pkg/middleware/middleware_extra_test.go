package middleware

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Request-ID middleware tests ---

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	var captured string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	mw := RequestIDMiddleware(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if captured == "" {
		t.Fatal("expected a request id to be generated and placed in context")
	}
	if got := rr.Header().Get(RequestIDHeader); got == "" {
		t.Fatalf("expected %s response header to be set", RequestIDHeader)
	}
	if rr.Header().Get(RequestIDHeader) != captured {
		t.Errorf("response header %q must match context value %q", rr.Header().Get(RequestIDHeader), captured)
	}
}

func TestRequestIDPreservedWhenPresent(t *testing.T) {
	const incoming = "client-supplied-id-123"
	var captured string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	mw := RequestIDMiddleware(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, incoming)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if captured != incoming {
		t.Errorf("expected incoming id %q to be preserved in context, got %q", incoming, captured)
	}
	if rr.Header().Get(RequestIDHeader) != incoming {
		t.Errorf("expected incoming id %q echoed in response header, got %q", incoming, rr.Header().Get(RequestIDHeader))
	}
}

// Mutation: two requests without an incoming id MUST get distinct generated ids.
func TestRequestIDGeneratesUniqueIDs(t *testing.T) {
	seen := map[string]struct{}{}
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[RequestIDFromContext(r.Context())] = struct{}{}
		mu.Unlock()
	})
	mw := RequestIDMiddleware(handler)

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rr := httptest.NewRecorder()
			mw.ServeHTTP(rr, req)
		}()
	}
	wg.Wait()

	if len(seen) != n {
		t.Errorf("expected %d unique ids, got %d (collision or empty id)", n, len(seen))
	}
	if _, ok := seen[""]; ok {
		t.Error("generated an empty request id")
	}
}

// RequestIDFromContext on an empty context must return empty string, not panic.
func TestRequestIDFromContextEmpty(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("expected empty string for context without id, got %q", got)
	}
}

// --- Timeout middleware tests ---

func TestTimeoutCutsOffSlowHandler(t *testing.T) {
	released := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the request context is cancelled by the timeout.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		close(released)
	})
	mw := TimeoutMiddleware(20 * time.Millisecond)(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	start := time.Now()
	mw.ServeHTTP(rr, req)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("timeout did not cut off slow handler; took %s", elapsed)
	}
	if rr.Code != http.StatusGatewayTimeout {
		t.Errorf("expected 504 on timeout, got %d", rr.Code)
	}
	// Ensure the handler goroutine actually observed cancellation (sink-side).
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Error("handler context was not cancelled on timeout")
	}
}

// Mutation: a fast handler completes normally and is NOT turned into a 504.
func TestTimeoutAllowsFastHandler(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("quick"))
	})
	mw := TimeoutMiddleware(500 * time.Millisecond)(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusTeapot {
		t.Errorf("expected fast handler status 418 to pass through, got %d", rr.Code)
	}
	if rr.Body.String() != "quick" {
		t.Errorf("expected body 'quick', got %q", rr.Body.String())
	}
}

// The request context handed to the handler must carry the deadline.
func TestTimeoutSetsContextDeadline(t *testing.T) {
	var hadDeadline bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadDeadline = r.Context().Deadline()
		w.WriteHeader(http.StatusOK)
	})
	mw := TimeoutMiddleware(time.Second)(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if !hadDeadline {
		t.Error("expected handler request context to carry a deadline")
	}
}

// --- Recover middleware: optional logging hook ---

func TestRecoverWithHookInvokesHookAndReturns500(t *testing.T) {
	var gotVal interface{}
	var called bool
	hook := func(w http.ResponseWriter, r *http.Request, rec interface{}) {
		called = true
		gotVal = rec
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom-hook")
	})
	mw := RecoverMiddlewareWithHook(hook)(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req) // must not crash the test process

	if !called {
		t.Fatal("expected recovery hook to be invoked")
	}
	if gotVal != "boom-hook" {
		t.Errorf("expected recovered value 'boom-hook', got %v", gotVal)
	}
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rr.Code)
	}
}

// Mutation: with a hook present and NO panic, the hook must NOT fire.
func TestRecoverWithHookNotCalledWithoutPanic(t *testing.T) {
	called := false
	hook := func(w http.ResponseWriter, r *http.Request, rec interface{}) { called = true }
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := RecoverMiddlewareWithHook(hook)(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if called {
		t.Error("hook must not be called when handler does not panic")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

// A nil hook must still recover and return 500 (back-compat with RecoverMiddleware).
func TestRecoverWithNilHookStillRecovers(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("nil-hook-panic")
	})
	mw := RecoverMiddlewareWithHook(nil)(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rr.Code)
	}
}

// --- Composition: all three compose cleanly via Chain ---

func TestComposeRecoverTimeoutRequestID(t *testing.T) {
	var idInHandler string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idInHandler = RequestIDFromContext(r.Context())
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("expected deadline inside composed chain")
		}
		panic("composed-boom")
	})
	chained := Chain(handler,
		RecoverMiddleware,
		TimeoutMiddleware(time.Second),
		RequestIDMiddleware,
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "compose-id")
	rr := httptest.NewRecorder()
	mw := LoggingMiddleware(chained)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	mw.ServeHTTP(rr, req) // panic must be recovered into 500, process survives

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected recovered 500 from composed chain, got %d", rr.Code)
	}
	if idInHandler != "compose-id" {
		t.Errorf("expected request id propagated through chain, got %q", idInHandler)
	}
	if rr.Header().Get(RequestIDHeader) != "compose-id" {
		t.Errorf("expected request id header on response, got %q", rr.Header().Get(RequestIDHeader))
	}
	if !strings.Contains(buf.String(), "500") {
		t.Error("expected logging middleware to record 500 status")
	}
}
