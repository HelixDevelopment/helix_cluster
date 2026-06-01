package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMount_CounterValueInScrape is the authoritative anti-bluff scrape test
// for HXC-1110.  It:
//   1. Creates a registry via NewServiceRegistry.
//   2. Registers a counter, increments it twice so the value is 2.
//   3. Mounts the registry on a fresh ServeMux via Mount.
//   4. Starts a real httptest.Server backed by that mux.
//   5. Issues a real HTTP GET /metrics over the loopback TCP connection.
//   6. Asserts status 200 AND that the body contains the counter line with
//      the exact value 2 (not merely the name appearing somewhere).
//
// A stub handler that returns "" or only 200-OK would fail step 6.
// A stub that returns the name but not the value would also fail.
// The assertion targets the exact Prometheus exposition line produced by
// Registry.serialize(), which is: "<name> <value>\n".
func TestMount_CounterValueInScrape(t *testing.T) {
	// Build registry with a known counter incremented exactly twice.
	reg := NewServiceRegistry("test")
	c := reg.RegisterCounter("helix_test_jobs_total", "Total test jobs processed.")
	c.Inc()
	c.Inc()
	// Counter value is now 2.

	mux := http.NewServeMux()
	Mount(mux, reg)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	body := string(bodyBytes)

	// Assert the exact counter value line — not just name presence.
	// The serialiser emits: "helix_test_jobs_total 2\n"
	const wantLine = "helix_test_jobs_total 2"
	if !strings.Contains(body, wantLine) {
		t.Errorf("scrape body must contain %q (value=2); got:\n%s", wantLine, body)
	}

	// Additionally assert the name does NOT appear with the wrong value 0 or 1
	// so a premature-read or off-by-one cannot sneak through.
	for _, wrong := range []string{"helix_test_jobs_total 0", "helix_test_jobs_total 1"} {
		if strings.Contains(body, wrong) {
			t.Errorf("scrape body must not contain %q; got:\n%s", wrong, body)
		}
	}
}

// TestMount_ContentType confirms that /metrics returns the Prometheus text
// content type over a real TCP connection, not just in-process.
func TestMount_ContentType(t *testing.T) {
	reg := NewServiceRegistry("ct")
	mux := http.NewServeMux()
	Mount(mux, reg)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Errorf("unexpected Content-Type %q; want \"text/plain; charset=utf-8\"", ct)
	}
}

// TestMount_BuildInfoGauge proves that NewServiceRegistry pre-registers the
// build-info gauge with value 1 so every service has a non-empty scrape
// output immediately after registry creation.
func TestMount_BuildInfoGauge(t *testing.T) {
	reg := NewServiceRegistry("myservice")
	mux := http.NewServeMux()
	Mount(mux, reg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	const wantLine = "helix_myservice_build_info 1"
	if !strings.Contains(body, wantLine) {
		t.Errorf("expected build-info line %q in body; got:\n%s", wantLine, body)
	}
}

// TestMount_NilMuxPanics ensures Mount panics on nil mux rather than producing
// a silent nil-dereference later.
func TestMount_NilMuxPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Mount(nil, reg) must panic")
		}
	}()
	reg := NewRegistry()
	Mount(nil, reg)
}

// TestMount_NilRegPanics ensures Mount panics on nil registry.
func TestMount_NilRegPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Mount(mux, nil) must panic")
		}
	}()
	Mount(http.NewServeMux(), nil)
}

// TestNewServiceRegistry_EmptyPanics ensures NewServiceRegistry panics on an
// empty service name.
func TestNewServiceRegistry_EmptyPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewServiceRegistry(\"\") must panic")
		}
	}()
	NewServiceRegistry("")
}

// TestMount_OtherRoutesUnaffected checks that mounting /metrics does not
// shadow unrelated routes already registered on the mux.
func TestMount_OtherRoutesUnaffected(t *testing.T) {
	reg := NewServiceRegistry("probe")
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	Mount(mux, reg)

	// /health still responds correctly.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/health: expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("/health: unexpected body %q", rec.Body.String())
	}

	// /metrics also responds correctly.
	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("/metrics: expected 200, got %d", rec2.Code)
	}
}
