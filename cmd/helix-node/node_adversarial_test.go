package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestConfigValidateBoundaryExactPort pins the EXACT port boundary contract:
// 0 and 65535 are accepted, 65536 and -1 are rejected. The existing suite only
// exercises a far-out-of-range value (70000); this nails the inclusive edges and
// the negative branch.
//
// MUTATION: change the guard to `p < 0 || p >= 65535` (drop the top edge) ->
// the 65535 accept case FAILS. Change to `p <= 0` -> the port-0 accept FAILS.
// Remove the `p < 0` half -> the -1 reject case FAILS.
func TestConfigValidateBoundaryExactPort(t *testing.T) {
	accept := []string{
		"127.0.0.1:0",     // ephemeral
		"127.0.0.1:65535", // top of range
		"127.0.0.1:1",
		":0", // empty host (all interfaces) is valid
	}
	for _, addr := range accept {
		cfg := Config{Addr: addr, ShutdownTimeout: time.Second}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(addr=%q) = %v, want nil (in-range port must pass)", addr, err)
		}
	}

	reject := []string{
		"127.0.0.1:65536", // one past the top edge
		"127.0.0.1:-1",    // negative port (the p<0 branch)
		"127.0.0.1:99999",
	}
	for _, addr := range reject {
		cfg := Config{Addr: addr, ShutdownTimeout: time.Second}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate(addr=%q) = nil, want error (out-of-range port must fail closed)", addr)
		}
	}
}

// TestConfigValidateIPv6 proves bracketed IPv6 host:port forms are accepted (the
// listen path supports them, so validation must not reject them).
//
// MUTATION: replace net.SplitHostPort with a naive strings.Split(":") in
// Validate -> "[::1]:50054" mis-parses and the IPv6 accept case FAILS.
func TestConfigValidateIPv6(t *testing.T) {
	for _, addr := range []string{"[::1]:50054", "[::]:0"} {
		cfg := Config{Addr: addr, ShutdownTimeout: time.Second}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(addr=%q) = %v, want nil (IPv6 host:port is valid)", addr, err)
		}
	}
}

// TestConfigValidateRejectsNamedAndEmptyPort proves validation fails CLOSED on a
// service-name port ("http") and an empty port (":" with nothing after) — both
// are not numeric, so Validate must reject rather than defer to net.Listen.
//
// MUTATION: drop the strconv.Atoi error check in Validate -> "127.0.0.1:http"
// and "127.0.0.1:" stop erroring -> FAILS.
func TestConfigValidateRejectsNamedAndEmptyPort(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:http", "127.0.0.1:", "127.0.0.1: 80"} {
		cfg := Config{Addr: addr, ShutdownTimeout: time.Second}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate(addr=%q) = nil, want error (non-numeric port must fail closed)", addr)
		}
	}
}

// TestConfigValidateNoPanicOnHostileAddr is a DoS guard: Validate must never
// panic on adversarial / malformed addresses, no matter how weird. It returns
// either nil or an error, but never crashes the process.
func TestConfigValidateNoPanicOnHostileAddr(t *testing.T) {
	hostile := []string{
		"",
		":",
		"::",
		"a:b:c:d:e",
		strings.Repeat(":", 1000),
		strings.Repeat("9", 10000) + ":80",
		"127.0.0.1:" + strings.Repeat("9", 10000),
		"\x00:0",
		"[:0",
		"]:0",
		"host:port:extra",
		"127.0.0.1:0x50",
		"127.0.0.1:" + "99999999999999999999999999", // overflow strconv.Atoi
	}
	for _, addr := range hostile {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Validate(addr=%q) panicked: %v", addr, r)
				}
			}()
			cfg := Config{Addr: addr, ShutdownTimeout: time.Second}
			_ = cfg.Validate() // result irrelevant; MUST NOT panic
		}()
	}
}

// TestConfigValidateOverflowPortFailsClosed proves a port string that overflows
// int does NOT wrap into the valid 0-65535 range and slip past validation — it
// must be rejected. This is the numeric-edge / fidelity guard.
//
// MUTATION: replace strconv.Atoi with a parser that ignores the range error ->
// the overflow value could appear valid -> FAILS.
func TestConfigValidateOverflowPortFailsClosed(t *testing.T) {
	// 6553600000... is way past 65535 but also overflows int64; Atoi returns an
	// out-of-range error which Validate must surface as a rejection.
	cfg := Config{Addr: "127.0.0.1:6553600000000000000000", ShutdownTimeout: time.Second}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate(overflow port) = nil, want error (must not wrap into valid range)")
	}
}

// TestConfigValidateShutdownTimeoutBoundary proves the shutdown-timeout guard is
// strictly positive: a negative timeout is rejected, the smallest positive
// duration is accepted.
//
// MUTATION: change `c.ShutdownTimeout <= 0` to `< 0` -> the zero/negative reject
// weakens; the 1ns accept stays. Change to always-positive accept -> negative
// reject FAILS.
func TestConfigValidateShutdownTimeoutBoundary(t *testing.T) {
	if err := (Config{Addr: "127.0.0.1:0", ShutdownTimeout: -time.Second}).Validate(); err == nil {
		t.Error("Validate(negative shutdown) = nil, want error")
	}
	if err := (Config{Addr: "127.0.0.1:0", ShutdownTimeout: 0}).Validate(); err == nil {
		t.Error("Validate(zero shutdown) = nil, want error")
	}
	if err := (Config{Addr: "127.0.0.1:0", ShutdownTimeout: time.Nanosecond}).Validate(); err != nil {
		t.Errorf("Validate(1ns shutdown) = %v, want nil (any positive duration is valid)", err)
	}
}

// TestDefaultConfigEnvOverlay proves DefaultConfig honors HELIX_NODE_ADDR,
// trims surrounding whitespace, and falls back to the baseline when the env var
// is empty/whitespace-only. This is the only place the env overlay is exercised.
//
// MUTATION: drop the strings.TrimSpace in DefaultConfig -> the "  host:port  "
// case keeps the padded value and the trimmed-equality assertion FAILS. Remove
// the env overlay entirely -> the override case FAILS.
func TestDefaultConfigEnvOverlay(t *testing.T) {
	const key = "HELIX_NODE_ADDR"

	// Default when unset.
	t.Setenv(key, "")
	if got := DefaultConfig(); got.Addr != ":50054" {
		t.Errorf("DefaultConfig() Addr with empty env = %q, want %q", got.Addr, ":50054")
	}

	// Whitespace-only env must NOT override (falls back to default).
	t.Setenv(key, "   ")
	if got := DefaultConfig(); got.Addr != ":50054" {
		t.Errorf("DefaultConfig() Addr with whitespace env = %q, want default %q", got.Addr, ":50054")
	}

	// Real override is honored and trimmed.
	t.Setenv(key, "  10.0.0.5:9999  ")
	got := DefaultConfig()
	if got.Addr != "10.0.0.5:9999" {
		t.Errorf("DefaultConfig() Addr with padded env = %q, want trimmed %q", got.Addr, "10.0.0.5:9999")
	}
	// And the overlaid value must itself validate.
	if err := got.Validate(); err != nil {
		t.Errorf("DefaultConfig() overlaid Addr %q failed Validate: %v", got.Addr, err)
	}

	// DefaultConfig always carries a positive shutdown timeout.
	if DefaultConfig().ShutdownTimeout <= 0 {
		t.Error("DefaultConfig() ShutdownTimeout must be positive")
	}
}

// ensure os import is used even if the suite is trimmed.
var _ = os.Getenv
