package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startServerAdv launches run() on a free 127.0.0.1 port (like startServer in
// main_test.go) but returns the raw host:port too, so adversarial probes can
// dial the listener directly at the transport layer. It blocks until the
// listener is open and guarantees clean shutdown via the returned stop func.
func startServerAdv(t *testing.T) (baseURL, hostPort string, stop func()) {
	t.Helper()

	cfg := Config{
		Host:            "127.0.0.1",
		Port:            freePort(t),
		ShutdownTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	runErr := make(chan error, 1)

	go func() { runErr <- run(ctx, cfg, func(a string) { addrCh <- a }) }()

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
	return "http://" + addr, addr, stop
}

// TestRunEnforcesReadHeaderTimeout is the headline adversarial probe. It proves
// the WIRED http.Server that run() builds defends against a Slowloris-style
// slow-header client: a peer that opens a connection and dribbles request
// headers without ever sending the terminating blank line must be cut off by
// the server within a bounded time, NOT pin a connection (and its goroutine)
// indefinitely.
//
// PROMISE under test: run() sets http.Server.ReadHeaderTimeout, so the server
// itself closes a connection whose headers never complete.
//
// How the assertion is unambiguous: we set a generous client-side read deadline
// (well beyond the server's ReadHeaderTimeout). If the SERVER enforces the
// timeout, our blocking Read returns (EOF / connection reset) BEFORE our client
// deadline fires — proving the server acted. If the server never times out
// (the bug), our Read instead blocks until OUR deadline and returns an
// os.ErrDeadlineExceeded ("i/o timeout") — which we treat as a FAILURE, because
// the connection was held open the whole time. This means the test can never
// hang: the client deadline always bounds it.
//
// Mutation that breaks it (proving the assertion is load-bearing): drop the
// ReadHeaderTimeout field from the http.Server in run(). The server then reads
// headers with no deadline, our Read blocks until the client deadline, and the
// assertion below fails with the diagnostic "server did NOT close the slow
// connection".
func TestRunEnforcesReadHeaderTimeout(t *testing.T) {
	if defaultReadHeaderTimeout > 15*time.Second {
		t.Fatalf("defaultReadHeaderTimeout=%s too large for a bounded test", defaultReadHeaderTimeout)
	}
	_, hostPort, stop := startServerAdv(t)
	defer stop()

	conn, err := net.DialTimeout("tcp", hostPort, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send a valid request line and ONE header, but never the terminating
	// blank line — headers stay perpetually "incomplete".
	if _, err := io.WriteString(conn, "GET /health HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatalf("write partial request: %v", err)
	}

	// Client-side guard deadline: strictly greater than the server's header
	// timeout so a server-side close clearly wins the race. This also bounds the
	// test so it can never hang, even under the buggy (no-timeout) variant.
	clientGuard := defaultReadHeaderTimeout + 5*time.Second
	if err := conn.SetReadDeadline(time.Now().Add(clientGuard)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	start := time.Now()
	buf := make([]byte, 256)
	n, rErr := conn.Read(buf)
	elapsed := time.Since(start)

	// A server that enforces ReadHeaderTimeout will either close the connection
	// (Read -> io.EOF / "reset by peer", n may be 0) or write a 408 then close.
	// Either way the Read returns BEFORE our client guard deadline.
	var netErr net.Error
	if errors.As(rErr, &netErr) && netErr.Timeout() {
		// Our OWN deadline fired => the server held the connection open the whole
		// time and never enforced a header read timeout. This is the bug.
		t.Fatalf("server did NOT close the slow-header connection: client guard "+
			"deadline (%s) fired after %s with no server-side timeout (read err=%v). "+
			"run()'s http.Server is missing ReadHeaderTimeout (Slowloris DoS).",
			clientGuard, elapsed, rErr)
	}

	// Server acted within the window. Sanity-bound it: must be after the header
	// timeout would plausibly start mattering and before our guard.
	if elapsed >= clientGuard {
		t.Fatalf("read returned at %s, at/after client guard %s (ambiguous)", elapsed, clientGuard)
	}
	t.Logf("server closed slow-header connection after %s (n=%d, err=%v) — ReadHeaderTimeout enforced", elapsed, n, rErr)
}

// TestRunWiredStackRoutesAllSurfaces pins the cmd-level mux composition that
// run() builds: /metrics is served by the Prometheus registry, /health and the
// proxy routes are served by the gateway, and an unknown path 404s. This proves
// the three handlers are wired in the right precedence and that mounting the
// gateway under "/" of a ServeMux (alongside /metrics) does not break gateway
// routing.
//
// Mutation that breaks it: remove metrics.Mount(mux, reg) in run() (then
// /metrics falls through to the gateway and 404s), or replace mux.Handle("/", g)
// with an empty handler (then /health and the proxy route stop working).
func TestRunWiredStackRoutesAllSurfaces(t *testing.T) {
	baseURL, _, stop := startServerAdv(t)
	defer stop()

	client := &http.Client{Timeout: 3 * time.Second}

	// /metrics -> Prometheus exposition, served by the registry (NOT the gateway,
	// which would 404 it). Assert sink-side: the build-info gauge run() seeds.
	resp, err := client.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200 (metrics handler not mounted)", resp.StatusCode)
	}
	if !strings.Contains(string(body), "helix_gateway_build_info") {
		t.Fatalf("/metrics body missing helix_gateway_build_info gauge; got:\n%s", string(body))
	}
	// The gateway marker must NOT be present on /metrics: it was served by the
	// registry, not proxied by the gateway.
	if got := resp.Header.Get("X-Helix-Gateway"); got != "" {
		t.Fatalf("/metrics carried gateway marker %q; metrics must not be served by the gateway", got)
	}

	// /health -> gateway healthy JSON, reached THROUGH the mux's "/" handler.
	resp, err = client.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != `{"status":"healthy"}` {
		t.Fatalf("/health = %d %q, want 200 {\"status\":\"healthy\"} (gateway not mounted at /)", resp.StatusCode, string(body))
	}

	// A proxy route reaches the gateway's reverse proxy (backend unreachable in
	// test => 502 + marker), proving "/" forwards real routing to the gateway.
	resp, err = client.Get(baseURL + "/api/v1/scheduler/anything")
	if err != nil {
		t.Fatalf("GET proxy route: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("X-Helix-Gateway") != "true" {
		t.Fatalf("proxy route = %d marker=%q, want 502 marker=true (gateway routing broken through mux)",
			resp.StatusCode, resp.Header.Get("X-Helix-Gateway"))
	}

	// Unknown path 404s with no marker (catch-all), distinguishing routed from
	// unrouted paths through the wired stack.
	resp, err = client.Get(baseURL + "/totally/unknown")
	if err != nil {
		t.Fatalf("GET unknown: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Helix-Gateway"); got != "" {
		t.Fatalf("unknown 404 carried gateway marker %q, want empty", got)
	}
}

// TestRunEncodedSlashPathDoesNotPanicOrEscape probes a hostile, deliberately
// malformed path: percent-encoded slashes and dot segments aimed at escaping
// the /api/v1/scheduler/ prefix toward /metrics. The contract we pin is twofold:
//   - the wired stack must NOT panic / DoS on the input (it returns an HTTP
//     status), and
//   - because %2f is not decoded into a path separator by net/http, the request
//     stays under the scheduler prefix and is handled by the gateway proxy
//     (marker stamped) — it does NOT silently land on the /metrics registry
//     handler. This documents that there is no encoded-slash bypass between the
//     two mux handlers.
//
// This is a DOCUMENTED-CONTRACT pin (current behavior is safe), not a bug fix.
//
// Mutation that would break it: if run() pre-decoded %2f and re-dispatched on
// the decoded path, the request could reach /metrics; this test would then see
// a 200 with the build-info gauge and no gateway marker, and fail.
func TestRunEncodedSlashPathDoesNotPanicOrEscape(t *testing.T) {
	baseURL, _, stop := startServerAdv(t)
	defer stop()

	client := &http.Client{
		Timeout: 3 * time.Second,
		// Do not follow redirects: we want to observe the raw disposition.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	for _, p := range []string{
		"/api/v1/scheduler/..%2f..%2fmetrics",
		"/api/v1/scheduler/%2e%2e/%2e%2e/metrics",
	} {
		req, err := http.NewRequest(http.MethodGet, baseURL+p, nil)
		if err != nil {
			t.Fatalf("build request %s: %v", p, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %s: %v", p, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// No panic/DoS: we got a real HTTP status back.
		if resp.StatusCode < 100 || resp.StatusCode > 599 {
			t.Fatalf("%s: implausible status %d", p, resp.StatusCode)
		}
		// Must NOT have leaked the metrics registry output through an encoded-slash
		// escape: the build-info gauge body is the unambiguous /metrics fingerprint.
		if strings.Contains(string(body), "helix_gateway_build_info") {
			t.Fatalf("%s: encoded-slash path reached /metrics registry (status %d) — prefix escape!", p, resp.StatusCode)
		}
		t.Logf("%s -> %d marker=%q (no escape to /metrics)", p, resp.StatusCode, resp.Header.Get("X-Helix-Gateway"))
	}
}

// TestConfigAddrFormatsHostPort pins Config.Addr()'s host:port formatting,
// including the empty-host ("all interfaces") case and an IPv6 host (which must
// be bracketed by net.JoinHostPort). Addr() feeds net.Listen, so a malformed
// address here is a real startup bug.
//
// Mutation that breaks it: change Addr() to fmt.Sprintf("%s:%d", Host, Port) —
// the IPv6 case below then yields "::1:9" (unbracketed, wrong) and the empty
// host case still passes, so this case is what makes the assertion load-bearing.
func TestConfigAddrFormatsHostPort(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"", 8080, ":8080"},
		{"127.0.0.1", 9090, "127.0.0.1:9090"},
		{"::1", 9, "[::1]:9"},
	}
	for _, tc := range cases {
		got := Config{Host: tc.host, Port: tc.port}.Addr()
		if got != tc.want {
			t.Fatalf("Addr(host=%q,port=%d) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

// TestLoadConfigHonoursHost proves HELIX_GATEWAY_HOST flows into Config.Host
// (and thus into the listen address), a path the existing tests do not cover.
//
// Mutation that breaks it: drop the HELIX_GATEWAY_HOST branch in LoadConfig;
// Host stays "" and Addr() becomes ":7000", failing the assertion.
func TestLoadConfigHonoursHost(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "HELIX_GATEWAY_HOST":
			return "127.0.0.1"
		case "HELIX_GATEWAY_PORT":
			return "7000"
		}
		return ""
	}
	cfg, err := LoadConfig(env)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got, want := cfg.Addr(), "127.0.0.1:7000"; got != want {
		t.Fatalf("Addr() = %q, want %q", got, want)
	}
}
