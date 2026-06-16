package main

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRun_BindFailureIsReportedAsError is the load-bearing CLAUDE-1 probe: a
// build service that CANNOT bind its port must surface a non-nil error so that
// main() exits non-zero. A failed start silently treated as success (run
// returning nil) is a PASS-bluff — the operator would believe the build service
// is up when nothing is listening.
//
// We occupy a port first, then ask run() to bind the SAME fixed port. The bind
// must fail and run() must return that failure (not nil).
//
// Mutation proving this bites: in run(), change
//     return fmt.Errorf("listen %s: %w", cfg.Addr(), err)
// to `return nil` — run() then swallows the bind failure, this require.Error
// fails, and the CLAUDE-1 bluff is exposed.
func TestRun_BindFailureIsReportedAsError(t *testing.T) {
	// Grab a concrete port and hold it so the subsequent bind is guaranteed to
	// collide. Listen on 127.0.0.1:0, read the chosen port, keep the socket open.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = occupied.Close() }()

	_, portStr, err := net.SplitHostPort(occupied.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	cfg := Config{
		Host:            "127.0.0.1",
		Port:            port, // already taken => bind must fail
		Workers:         1,
		ShutdownTimeout: time.Second,
		SimulatedBuilds: true,
	}

	// run() must NOT block here: a bind failure returns promptly. Guard with a
	// deadline so a regression that hangs surfaces as a failure, never a hang.
	done := make(chan error, 1)
	go func() { done <- run(context.Background(), cfg, nil) }()

	select {
	case rerr := <-done:
		require.Error(t, rerr, "run() must report bind failure as a non-nil error, not silently succeed")
	case <-time.After(5 * time.Second):
		t.Fatal("run() neither returned nor errored on a bind collision (hang)")
	}
}

// TestRun_BindFailureDoesNotLeakReady proves the ready callback is only invoked
// once the listener is genuinely open. On a bind failure run() must return
// BEFORE signalling readiness — otherwise a caller (and the startServer helper)
// would treat a dead service as live.
//
// Mutation proving this bites: in run(), move the ready(...) call ABOVE the
// net.Listen error check — ready then fires even when the bind failed and this
// assertion fails.
func TestRun_BindFailureDoesNotLeakReady(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = occupied.Close() }()

	_, portStr, err := net.SplitHostPort(occupied.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	cfg := Config{
		Host:            "127.0.0.1",
		Port:            port,
		Workers:         1,
		ShutdownTimeout: time.Second,
		SimulatedBuilds: true,
	}

	readyFired := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), cfg, func(a string) { readyFired <- a })
	}()

	select {
	case <-done:
		// run returned (with the bind error). Readiness must NOT have fired.
		select {
		case a := <-readyFired:
			t.Fatalf("ready() fired with %q despite bind failure", a)
		default:
		}
	case a := <-readyFired:
		t.Fatalf("ready() fired with %q despite bind failure", a)
	case <-time.After(5 * time.Second):
		t.Fatal("run() hung on bind failure")
	}
}

// TestConfig_AddrRoundTrips proves the listen address that run() hands to
// net.Listen is always well-formed: it parses back via net.SplitHostPort to the
// exact host and port. This guards against a malformed-address regression (e.g.
// dropping the colon or the host) that would make net.Listen fail or bind the
// wrong interface.
//
// Mutation proving this bites: change Addr() to `return c.Host + ":" + ...`
// (naive concatenation) — an IPv6 host like "::1" then yields ":::1:50051",
// SplitHostPort errors, and this test fails. net.JoinHostPort handles it.
func TestConfig_AddrRoundTrips(t *testing.T) {
	cases := []struct {
		host string
		port int
	}{
		{"127.0.0.1", 50051},
		{"", 50051},      // all-interfaces form
		{"::1", 50051},   // IPv6 — bracketing required
		{"localhost", 0}, // ephemeral
	}
	for _, tc := range cases {
		c := Config{Host: tc.host, Port: tc.port}
		addr := c.Addr()
		gotHost, gotPortStr, err := net.SplitHostPort(addr)
		require.NoError(t, err, "Addr() %q must be a valid host:port", addr)
		assert.Equal(t, tc.host, gotHost)
		gotPort, err := strconv.Atoi(gotPortStr)
		require.NoError(t, err)
		assert.Equal(t, tc.port, gotPort)
	}
}

// TestValidate_PortBoundaries pins the exact accepted/rejected port boundary so
// a future off-by-one (>= vs >) regression in Validate is caught. 65535 is the
// max valid TCP port and must be accepted; 65536 must be rejected; 0 is the
// documented ephemeral sentinel and must be accepted; negative must be rejected.
//
// Mutation proving this bites: change the check to `c.Port > 65534` — port 65535
// then wrongly errors and the accept case fails; change it to `c.Port > 65536`
// and the 65536 reject case fails.
func TestValidate_PortBoundaries(t *testing.T) {
	base := func(p int) Config {
		return Config{Port: p, Workers: 1, ShutdownTimeout: time.Second}
	}
	assert.NoError(t, base(0).Validate(), "port 0 (ephemeral) must be accepted")
	assert.NoError(t, base(1).Validate())
	assert.NoError(t, base(65535).Validate(), "max valid port must be accepted")
	assert.Error(t, base(65536).Validate(), "port 65536 is out of range")
	assert.Error(t, base(-1).Validate())
}

// TestLoadConfig_RejectsNonCanonicalInts proves the env integer parser rejects
// non-canonical numeric forms instead of silently coercing them — a misconfig
// must fail fast, not start a service on a surprising port/worker count.
//
// Mutation proving this bites: replace strconv.Atoi with a parser that trims or
// tolerates these forms; the corresponding require.Error stops firing.
func TestLoadConfig_RejectsNonCanonicalInts(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"port with whitespace", map[string]string{"HELIX_BUILD_PORT": " 50051 "}},
		{"port hex", map[string]string{"HELIX_BUILD_PORT": "0x50"}},
		{"port float", map[string]string{"HELIX_BUILD_PORT": "50051.0"}},
		{"port plus-sign garbage", map[string]string{"HELIX_BUILD_PORT": "50051abc"}},
		{"workers with whitespace", map[string]string{"HELIX_BUILD_WORKERS": " 4 "}},
		{"workers float", map[string]string{"HELIX_BUILD_WORKERS": "4.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(func(k string) string { return tc.env[k] })
			require.Error(t, err, "non-canonical integer %v must be rejected", tc.env)
		})
	}
}

// TestLoadConfig_HostDoesNotPanicOrCorruptAddr proves a hostile/odd
// HELIX_BUILD_HOST value cannot panic LoadConfig or produce an address that
// fails to parse. There is no shell here (net.Listen does not exec), so this is
// not command injection — but the address must remain structurally sound so the
// service binds the host it was told to, never a smuggled extra field.
//
// This documents the current contract: LoadConfig accepts any non-empty host
// string verbatim and Addr() keeps it well-formed via JoinHostPort.
func TestLoadConfig_HostDoesNotPanicOrCorruptAddr(t *testing.T) {
	hostiles := []string{
		"0.0.0.0",
		"example.com",
		"weird host with spaces",
		"$(whoami)",
		"a;b|c&d",
		"::1",
	}
	for _, h := range hostiles {
		env := map[string]string{"HELIX_BUILD_HOST": h}
		cfg, err := LoadConfig(func(k string) string { return env[k] })
		require.NoError(t, err, "host %q must not error config load", h)
		require.Equal(t, h, cfg.Host)
		// The constructed listen address must round-trip the host exactly: no
		// extra colon-delimited field can be smuggled in.
		gotHost, _, splitErr := net.SplitHostPort(cfg.Addr())
		require.NoError(t, splitErr, "Addr() for host %q must be well-formed", h)
		assert.Equal(t, h, gotHost, "host must not be corrupted/augmented in Addr()")
	}
}

// TestLoadConfig_WorkersUpperBound documents a LATENT RISK, not a passing
// contract we endorse: LoadConfig + Validate accept an arbitrarily large worker
// count, which run() would hand to the orchestrator to spawn that many
// goroutines (resource-exhaustion DoS from a single env var). We assert the
// CURRENT behaviour (accepted) so that if/when an upper bound is added, this
// test is updated deliberately rather than silently.
func TestLoadConfig_WorkersUpperBound(t *testing.T) {
	env := map[string]string{"HELIX_BUILD_WORKERS": "1000000000"}
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	// CURRENT behaviour: no upper bound. If this ever starts erroring, an upper
	// bound was added — update the assertion intentionally.
	require.NoError(t, err)
	assert.Equal(t, 1000000000, cfg.Workers)
}
