package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeProbe builds a Prober that reports the given per-address reachability.
// Any address not in the map is treated as unreachable.
func fakeProbe(reachable map[string]bool) Prober {
	return func(_ context.Context, addr string, _ time.Duration) bool {
		return reachable[addr]
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// Mutation: change default 8081 -> 8080 in LoadConfig makes this fail.
	if cfg.Port != 8081 {
		t.Errorf("Port = %d, want 8081", cfg.Port)
	}
	if cfg.ProbeTimeout <= 0 || cfg.ShutdownTimeout <= 0 {
		t.Errorf("timeouts must be > 0, got probe=%s shutdown=%s", cfg.ProbeTimeout, cfg.ShutdownTimeout)
	}
}

func TestLoadConfigPortOverride(t *testing.T) {
	env := map[string]string{"HELIXD_PORT": "9099", "HELIXD_HOST": "127.0.0.1"}
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// Mutation: ignore HELIXD_PORT in LoadConfig makes this fail.
	if cfg.Port != 9099 {
		t.Errorf("Port = %d, want 9099", cfg.Port)
	}
	if cfg.Addr() != "127.0.0.1:9099" {
		t.Errorf("Addr = %s, want 127.0.0.1:9099", cfg.Addr())
	}
}

func TestLoadConfigRejectsNonNumericPort(t *testing.T) {
	env := map[string]string{"HELIXD_PORT": "notaport"}
	_, err := LoadConfig(func(k string) string { return env[k] })
	// Mutation: return cfg without parsing HELIXD_PORT makes this fail (err==nil).
	if err == nil {
		t.Fatal("expected error for non-numeric HELIXD_PORT, got nil")
	}
	if !strings.Contains(err.Error(), "HELIXD_PORT") {
		t.Errorf("error %q should name HELIXD_PORT", err)
	}
}

func TestLoadConfigRejectsOutOfRangePort(t *testing.T) {
	env := map[string]string{"HELIXD_PORT": "70000"}
	_, err := LoadConfig(func(k string) string { return env[k] })
	// Mutation: drop the Port>65535 check in Validate makes this fail.
	if err == nil {
		t.Fatal("expected error for port 70000, got nil")
	}
}

func TestValidateRejectsNonPositiveTimeouts(t *testing.T) {
	bad := Config{Port: 8081, ProbeTimeout: 0, ShutdownTimeout: time.Second}
	// Mutation: drop the ProbeTimeout<=0 check in Validate makes this fail.
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for zero ProbeTimeout, got nil")
	}
}

func TestDefaultDependenciesWiring(t *testing.T) {
	deps := defaultDependencies(func(string) string { return "" })
	got := map[string]string{}
	for _, d := range deps {
		got[d.Name] = d.Addr
	}
	want := map[string]string{
		"etcd":     "localhost:2379",
		"postgres": "localhost:5432",
		"redis":    "localhost:6379",
	}
	// Mutation: drop redis from defaultDependencies, or change etcd's port,
	// makes this fail.
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d: %v", len(got), len(want), got)
	}
	for name, addr := range want {
		if got[name] != addr {
			t.Errorf("dep %s = %q, want %q", name, got[name], addr)
		}
	}
}

func TestDefaultDependenciesEnvOverride(t *testing.T) {
	env := map[string]string{"HELIXD_POSTGRES_ADDR": "db.internal:6543"}
	deps := defaultDependencies(func(k string) string { return env[k] })
	var pgAddr string
	for _, d := range deps {
		if d.Name == "postgres" {
			pgAddr = d.Addr
		}
	}
	// Mutation: ignore HELIXD_POSTGRES_ADDR makes this fail.
	if pgAddr != "db.internal:6543" {
		t.Errorf("postgres addr = %q, want db.internal:6543", pgAddr)
	}
}

func TestCheckServicesMixedReachability(t *testing.T) {
	deps := []Dependency{
		{Name: "etcd", Addr: "a:1"},
		{Name: "postgres", Addr: "b:2"},
		{Name: "redis", Addr: "c:3"},
	}
	probe := fakeProbe(map[string]bool{"a:1": true, "c:3": true}) // b:2 unreachable
	got := checkServices(context.Background(), deps, probe, time.Second)

	// Mutation: hardcode state "reachable" for all in checkServices makes the
	// postgres assertion below fail.
	want := map[string]string{
		"etcd":     stateReachable,
		"postgres": stateUnreachable,
		"redis":    stateReachable,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for name, state := range want {
		if got[name] != state {
			t.Errorf("service %s = %q, want %q", name, got[name], state)
		}
	}
}

// liveStatus dials a real server started by run() and decodes the /status body.
func liveStatus(t *testing.T, deps []Dependency, probe Prober) (status, func()) {
	t.Helper()
	return livePath(t, deps, probe, "/status")
}

// livePath dials a real server started by run() and decodes the JSON body served
// at the given path. It is the shared driver behind /status and its /health and
// /healthz aliases, so each endpoint is exercised exactly as an end user (k8s
// probe, gateway health check, or HelixQA challenge) would hit it.
func livePath(t *testing.T, deps []Dependency, probe Prober, path string) (status, func()) {
	t.Helper()
	cfg := Config{
		Host:            "127.0.0.1",
		Port:            0, // ephemeral, host-safe
		ProbeTimeout:    500 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, cfg, deps, probe, func(addr string) { addrCh <- addr })
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case err := <-runErr:
		cancel()
		t.Fatalf("run exited before ready: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for server ready")
	}

	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET %s status code = %d, want 200", path, resp.StatusCode)
	}
	var body status
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		cancel()
		t.Fatalf("decode %s: %v", path, err)
	}

	stop := func() {
		cancel()
		select {
		case err := <-runErr:
			if err != nil {
				t.Errorf("run returned error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("run did not return after cancel")
		}
	}
	return body, stop
}

func TestRunServesRealStatusPayload(t *testing.T) {
	deps := []Dependency{
		{Name: "etcd", Addr: "e:1"},
		{Name: "postgres", Addr: "p:1"},
		{Name: "redis", Addr: "r:1"},
	}
	// etcd+redis up, postgres down — proves the live payload mirrors wiring.
	probe := fakeProbe(map[string]bool{"e:1": true, "r:1": true})

	body, stop := liveStatus(t, deps, probe)
	defer stop()

	if body.Version != version {
		t.Errorf("version = %q, want %q", body.Version, version)
	}
	if body.Status != "healthy" {
		t.Errorf("status = %q, want healthy", body.Status)
	}
	// Mutation: serve a static services map (ignoring probe) makes these fail.
	if body.Services["etcd"] != stateReachable {
		t.Errorf("etcd = %q, want reachable", body.Services["etcd"])
	}
	if body.Services["postgres"] != stateUnreachable {
		t.Errorf("postgres = %q, want unreachable", body.Services["postgres"])
	}
	if body.Services["redis"] != stateReachable {
		t.Errorf("redis = %q, want reachable", body.Services["redis"])
	}
	if body.Timestamp <= 0 {
		t.Errorf("timestamp = %d, want > 0", body.Timestamp)
	}
}

func TestRunGracefulShutdownStopsServing(t *testing.T) {
	deps := []Dependency{{Name: "etcd", Addr: "e:1"}}
	probe := fakeProbe(map[string]bool{"e:1": true})

	cfg := Config{
		Host:            "127.0.0.1",
		Port:            0,
		ProbeTimeout:    500 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, cfg, deps, probe, func(addr string) { addrCh <- addr })
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for ready")
	}

	// Prove it is really serving first.
	resp, err := http.Get("http://" + addr + "/status")
	if err != nil {
		cancel()
		t.Fatalf("pre-shutdown GET: %v", err)
	}
	resp.Body.Close()

	// Trigger graceful shutdown.
	cancel()
	select {
	case err := <-runErr:
		// Mutation: return srv.Close()'s/Serve's ErrServerClosed as an error
		// (i.e. drop the errors.Is(...ErrServerClosed) guard) makes this fail.
		if err != nil {
			t.Fatalf("run returned error on clean shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after cancel (shutdown not bounded)")
	}

	// After shutdown the listener must be closed: a fresh dial must fail.
	// Mutation: skip srv.Shutdown in run (leave it serving) makes this fail.
	c, derr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if derr == nil {
		c.Close()
		t.Fatalf("server still accepting connections on %s after shutdown", addr)
	}
}

// TestRunServesHealthAliases proves the conventional /health and /healthz probe
// endpoints serve the SAME live payload as /status. Many consumers (k8s/orchestrator
// liveness probes, helix-gateway health checks, HelixQA challenges) hit these paths
// by convention; before this fix they 404'd, forcing workarounds.
func TestRunServesHealthAliases(t *testing.T) {
	deps := []Dependency{
		{Name: "etcd", Addr: "e:1"},
		{Name: "postgres", Addr: "p:1"},
		{Name: "redis", Addr: "r:1"},
	}
	// etcd+redis up, postgres down — same wiring as the /status test, so we can
	// assert the alias payload mirrors live reachability, not a static stub.
	probe := fakeProbe(map[string]bool{"e:1": true, "r:1": true})

	for _, path := range []string{"/health", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			// Mutation: do not register the alias in newMux makes this fail
			// (livePath fatals on the 404 status code).
			body, stop := livePath(t, deps, probe, path)
			defer stop()

			if body.Version != version {
				t.Errorf("%s version = %q, want %q", path, body.Version, version)
			}
			if body.Status != "healthy" {
				t.Errorf("%s status = %q, want healthy", path, body.Status)
			}
			if body.Services["etcd"] != stateReachable {
				t.Errorf("%s etcd = %q, want reachable", path, body.Services["etcd"])
			}
			if body.Services["postgres"] != stateUnreachable {
				t.Errorf("%s postgres = %q, want unreachable", path, body.Services["postgres"])
			}
			if body.Services["redis"] != stateReachable {
				t.Errorf("%s redis = %q, want reachable", path, body.Services["redis"])
			}
			if body.Timestamp <= 0 {
				t.Errorf("%s timestamp = %d, want > 0", path, body.Timestamp)
			}
		})
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	bad := Config{Host: "127.0.0.1", Port: -5, ProbeTimeout: time.Second, ShutdownTimeout: time.Second}
	err := run(context.Background(), bad, nil, fakeProbe(nil), nil)
	// Mutation: drop the cfg.Validate() call at the top of run makes this fail
	// (it would instead try to listen and return a different/nil error path).
	if err == nil {
		t.Fatal("expected config error for negative port, got nil")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Errorf("error %q should be a config error", err)
	}
}
