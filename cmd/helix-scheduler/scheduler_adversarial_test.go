package main

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file adversarially pins the only real business logic that lives in the
// cmd/helix-scheduler package: configuration parsing/validation (Config.Validate,
// LoadConfig) and address formatting (Config.Addr).
//
// HONEST SCOPE NOTE: the scheduler's node/task SELECTION, SCORING/RANKING
// comparators, bin-packing, priority/preemption and capacity math do NOT live in
// this package. They live in internal/scheduler (Server.ScheduleJob) and
// pkg/scheduler (NodeResourcesFit / CapabilityMatch / LoadAware plugins), both of
// which are out of scope for edits here. cmd/helix-scheduler is thin wiring plus
// the config seam below, so the adversarial probes here target the boundary math
// and total-ordering-of-validity that the config helpers PROMISE.

// TestValidatePortBoundaryExact pins the EXACT capacity boundary of the port
// range: 1 and 65535 are the inclusive extremes and MUST be accepted; 0 and
// 65536 are one step outside and MUST be rejected. This is the off-by-one /
// boundary probe.
//
// Mutation that breaks it: change the guard in Config.Validate from
//
//	if c.Port < 1 || c.Port > 65535
//
// to `c.Port <= 1` (rejects the valid low boundary 1) or `c.Port >= 65535`
// (rejects the valid high boundary 65535), or to `c.Port > 65536` (accepts the
// invalid 65536). Any of these flips an assertion below.
func TestValidatePortBoundaryExact(t *testing.T) {
	valid := []int{1, 2, 1024, 50052, 65534, 65535}
	for _, p := range valid {
		cfg := Config{Port: p, ShutdownTimeout: time.Second}
		if err := cfg.Validate(); err != nil {
			t.Errorf("port %d is inside [1,65535] and MUST be valid, got error: %v", p, err)
		}
	}

	invalid := []int{-1, 0, 65536, 70000, 1 << 20}
	for _, p := range invalid {
		cfg := Config{Port: p, ShutdownTimeout: time.Second}
		if err := cfg.Validate(); err == nil {
			t.Errorf("port %d is outside [1,65535] and MUST be rejected, got nil error", p)
		}
	}
}

// TestValidateRejectsNonPositiveShutdownTimeout pins that a zero or negative
// ShutdownTimeout is rejected. A non-positive timeout would make run()'s
// context.WithTimeout fire immediately, defeating graceful drain. The existing
// suite never exercises this guard directly.
//
// Mutation that breaks it: change the guard from `c.ShutdownTimeout <= 0` to
// `c.ShutdownTimeout < 0` — then a zero timeout is wrongly accepted and the
// zero-case assertion below fails.
func TestValidateRejectsNonPositiveShutdownTimeout(t *testing.T) {
	for _, d := range []time.Duration{0, -1, -time.Hour} {
		cfg := Config{Port: defaultPort, ShutdownTimeout: d}
		if err := cfg.Validate(); err == nil {
			t.Errorf("ShutdownTimeout %v is non-positive and MUST be rejected, got nil", d)
		}
	}
	// And a positive one MUST pass (proves the guard is not over-broad).
	cfg := Config{Port: defaultPort, ShutdownTimeout: time.Nanosecond}
	if err := cfg.Validate(); err != nil {
		t.Errorf("positive ShutdownTimeout MUST be accepted, got: %v", err)
	}
}

// TestLoadConfigPortOverflowRejected pins that an integer that overflows the
// platform int (and therefore can never be a valid port) is rejected by
// LoadConfig rather than silently truncating/saturating into the valid range.
// strconv.Atoi saturates the returned value to MaxInt on overflow but also
// returns an error; LoadConfig MUST honour that error.
//
// Mutation that breaks it: in LoadConfig, ignore the strconv.Atoi error (assign
// p and proceed). The saturated value MaxInt then flows to Validate, which
// rejects it for range — so to truly observe the overflow leak the test asserts
// LoadConfig returns an error AND no partial Config; if the error were dropped,
// the error-presence assertion fails.
func TestLoadConfigPortOverflowRejected(t *testing.T) {
	huge := "99999999999999999999999" // > math.MaxInt64
	// sanity: this genuinely overflows Atoi
	if _, err := strconv.Atoi(huge); err == nil {
		t.Fatalf("test premise broken: %q unexpectedly parsed without overflow", huge)
	}
	env := func(k string) string {
		if k == "HELIX_SCHEDULER_PORT" {
			return huge
		}
		return ""
	}
	cfg, err := LoadConfig(env)
	if err == nil {
		t.Fatalf("overflowing port %q MUST be rejected, got nil err (cfg=%+v)", huge, cfg)
	}
	if cfg != (Config{}) {
		t.Errorf("on error LoadConfig MUST return the zero Config, got %+v", cfg)
	}
}

// TestAddrRoundTrips pins that Config.Addr() produces a host:port string that
// net.SplitHostPort parses back to the SAME port — i.e. Addr is a correct,
// listenable address and not a malformed string. Covers the empty-host
// ("all interfaces") case and a concrete host.
//
// Mutation that breaks it: change Addr to fmt.Sprintf("%s:%d") without
// net.JoinHostPort — an IPv6 host like "::1" then yields ":::1:port" which
// SplitHostPort rejects, failing the round-trip assertion.
func TestAddrRoundTrips(t *testing.T) {
	cases := []struct {
		host string
		port int
	}{
		{"", 50052},
		{"127.0.0.1", 1},
		{"127.0.0.1", 65535},
		{"::1", 8080}, // IPv6: only correct with JoinHostPort bracketing
	}
	for _, tc := range cases {
		cfg := Config{Host: tc.host, Port: tc.port, ShutdownTimeout: time.Second}
		addr := cfg.Addr()
		_, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			t.Errorf("Addr()=%q for host %q is not a valid host:port: %v", addr, tc.host, err)
			continue
		}
		if portStr != strconv.Itoa(tc.port) {
			t.Errorf("Addr()=%q lost the port: got %q want %d", addr, portStr, tc.port)
		}
	}
}

// TestLoadConfigDefaultShutdownTimeoutPositive pins that the Config produced by
// LoadConfig always carries a strictly-positive ShutdownTimeout, so a
// freshly-loaded config can never fail run()'s own Validate on the timeout
// guard. Guards against a regression where the default constant is zeroed.
//
// Mutation that breaks it: set defaultShutdownTimeout to 0 — the assertion and
// the subsequent Validate both fail.
func TestLoadConfigDefaultShutdownTimeoutPositive(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfig clean env: %v", err)
	}
	if cfg.ShutdownTimeout <= 0 {
		t.Fatalf("LoadConfig MUST yield a positive ShutdownTimeout, got %v", cfg.ShutdownTimeout)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a freshly LoadConfig'd config MUST validate, got: %v", err)
	}
	// The default address must also be listenable-shaped.
	if !strings.HasSuffix(cfg.Addr(), ":"+strconv.Itoa(defaultPort)) {
		t.Fatalf("default Addr()=%q must end in :%d", cfg.Addr(), defaultPort)
	}
}
