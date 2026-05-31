package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// freePort asks the kernel for an available TCP port on 127.0.0.1 and returns
// it. Binding 127.0.0.1:0 then closing is host-safe and race-tolerant: the
// window before run() re-binds is tiny and local-only.
func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	return port
}

// startServer launches run() on a free 127.0.0.1 port and returns the bound
// base URL plus a stop func. It blocks (via the ready callback) until the
// listener is actually open, so there is no bind race for the dialing client.
func startServer(t *testing.T) (baseURL string, stop func()) {
	t.Helper()

	cfg := Config{
		Host:            "127.0.0.1",
		Port:            freePort(t),
		ShutdownTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	runErr := make(chan error, 1)

	go func() {
		runErr <- run(ctx, cfg, func(a string) { addrCh <- a })
	}()

	var addr string
	select {
	case a := <-addrCh:
		addr = a
	case err := <-runErr:
		t.Fatalf("run exited before binding: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to bind")
	}

	stop = func() {
		cancel()
		select {
		case err := <-runErr:
			if err != nil {
				t.Errorf("run returned error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("timed out waiting for run to shut down")
		}
	}
	return "http://" + addr, stop
}

// TestRunServesHealth proves the service actually starts, binds, and serves a
// REAL HTTP request: a real client hits /health on the bound port and gets the
// concrete healthy JSON body back from internal/gateway.
//
// Mutation that breaks it: in run(), drop the `srv.Serve(lis)` call (or never
// call ready / never bind). With nothing serving the bound port, client.Get
// errors with connection refused and this test fails.
func TestRunServesHealth(t *testing.T) {
	baseURL, stop := startServer(t)
	defer stop()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got, want := string(body), `{"status":"healthy"}`; got != want {
		t.Fatalf("health body = %q, want %q", got, want)
	}
}

// TestRunProxiesToUnreachableBackendStampsMarker proves REAL request routing
// through the running server into internal/gateway's reverse-proxy path. The
// default scheduler backend (localhost:50052) is unreachable in test, so the
// gateway's own reverse-proxy ErrorHandler fires and stamps the
// X-Helix-Gateway marker on a 502. Both the 502 status and the marker header
// are sink-side proof that the client request was routed INTO the gateway's
// proxy for /api/v1/scheduler/* rather than being 404'd by the catch-all or
// short-circuited by /health.
//
// This exercises the gateway the real binary uses (built by run() via
// gateway.NewGateway), served over a real ephemeral TCP listener, hit by a
// real net/http client.
//
// Mutation that breaks it: in run(), serve an empty http.NewServeMux instead of
// the gateway g. Then /api/v1/scheduler/anything returns 404 with no marker,
// failing both assertions. (Gateway-internal mutation: removing the
// ErrorHandler's marker stamp drops the header — out of our dir but the
// behavior we are proving.)
func TestRunProxiesToUnreachableBackendStampsMarker(t *testing.T) {
	baseURL, stop := startServer(t)
	defer stop()

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/api/v1/scheduler/anything")
	if err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d (request not routed into proxy)", resp.StatusCode, http.StatusBadGateway)
	}
	if got := resp.Header.Get("X-Helix-Gateway"); got != "true" {
		t.Fatalf("X-Helix-Gateway = %q, want %q (proxy path not taken)", got, "true")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got, want := string(body), `{"error":"backend unavailable","status":502}`; got != want {
		t.Fatalf("error body = %q, want %q", got, want)
	}
}

// TestRunRoutesUnknownPathTo404 proves the running server distinguishes a
// proxied prefix from an unknown path: a request that matches no route falls
// through to the catch-all 404 WITHOUT the gateway marker. Paired with
// TestRunProxiesToUnreachableBackendStampsMarker, this pins down that routing
// is real (different paths take different code paths), not a constant response.
//
// Mutation that breaks it: make ServeHTTP proxy everything (remove the prefix
// check) — then /nope would hit a proxy and return 502+marker, failing the
// status assertion. At cmd level, serving an all-proxy handler in run() breaks
// it the same way.
func TestRunRoutesUnknownPathTo404(t *testing.T) {
	baseURL, stop := startServer(t)
	defer stop()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/no/such/route")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for unknown path", resp.StatusCode, http.StatusNotFound)
	}
	if got := resp.Header.Get("X-Helix-Gateway"); got != "" {
		t.Fatalf("X-Helix-Gateway = %q, want empty for non-proxied 404", got)
	}
}

// TestRunGracefulShutdownStopsServing proves ctx cancellation actually stops
// the service: after stop() returns, the bound port no longer accepts new
// connections.
//
// Mutation that breaks it: in run(), replace the `case <-ctx.Done():` shutdown
// path with a `select{}` (block forever) — or remove the srv.Shutdown() call.
// The listener would keep serving and the post-shutdown dial below would
// succeed, failing this test (and stop() would also time out).
func TestRunGracefulShutdownStopsServing(t *testing.T) {
	baseURL, stop := startServer(t)

	// Sanity: it serves before shutdown.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("pre-shutdown health failed: %v", err)
	}
	resp.Body.Close()

	stop() // trigger ctx cancel + wait for run() to fully return

	// The bound TCP port must no longer accept connections. We assert at the
	// transport layer because that is the unambiguous sink: a stopped listener
	// refuses connections.
	addr := baseURL[len("http://"):]
	conn, dErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if dErr == nil {
		conn.Close()
		t.Fatalf("expected port %s to stop accepting after shutdown, but dial succeeded", addr)
	}
}

// TestLoadConfigRejectsBadPort proves config validation rejects clearly-bad
// input with a real error instead of starting a broken server.
//
// Mutation that breaks it: in Config.Validate(), change the guard to
// `if c.Port < 0` (or delete the port check). Then port 70000 is accepted and
// LoadConfig returns nil error, failing this test.
func TestLoadConfigRejectsBadPort(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"out-of-range-high", "70000"},
		{"out-of-range-zero", "0"},
		{"negative", "-1"},
		{"non-integer", "not-a-port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := func(k string) string {
				if k == "HELIX_GATEWAY_PORT" {
					return tc.val
				}
				return ""
			}
			if _, err := LoadConfig(env); err == nil {
				t.Fatalf("expected error for HELIX_GATEWAY_PORT=%q, got nil", tc.val)
			}
		})
	}
}

// TestLoadConfigDefaults proves a clean environment yields the documented
// default port, and that a valid override is honoured.
//
// Mutation that breaks it: change defaultPort to a different value, or make
// LoadConfig ignore HELIX_GATEWAY_PORT — either assertion below then fails.
func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfig with empty env: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("expected default port %d, got %d", defaultPort, cfg.Port)
	}

	cfg2, err := LoadConfig(func(k string) string {
		if k == "HELIX_GATEWAY_PORT" {
			return "18080"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadConfig with valid override: %v", err)
	}
	if cfg2.Port != 18080 {
		t.Fatalf("expected overridden port 18080, got %d", cfg2.Port)
	}
}

// TestRunRejectsInvalidConfig proves run() refuses to bind on invalid config
// rather than silently doing something undefined.
//
// Mutation that breaks it: remove the `cfg.Validate()` call at the top of
// run(). The function would then attempt net.Listen on an invalid port and the
// error path / message would differ, failing this assertion.
func TestRunRejectsInvalidConfig(t *testing.T) {
	err := run(context.Background(), Config{Port: 0, ShutdownTimeout: time.Second}, nil)
	if err == nil {
		t.Fatal("expected run to reject invalid config, got nil error")
	}
}
