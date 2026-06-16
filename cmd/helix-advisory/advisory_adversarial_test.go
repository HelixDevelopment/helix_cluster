package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This package is a thin gRPC wiring main for the advisory *lock* service plus
// config parsing/validation (Config.Validate / LoadConfig / isHostname). It
// contains NO scoring/ranking/severity-classification/aggregation logic, so the
// classic "advisory engine" bug classes (NaN/Inf comparator, severity boundary
// mis-bucket, drop/double-count aggregation, vacuous all-clear) do not exist
// here. These adversarial tests instead PIN the real config-validation contract
// at the boundaries the existing suite does not cover, so a future regression
// that loosens or breaks validation is caught. Every assertion below is
// verified against the current code.

// TestValidateUpperPortBoundaryAccepted pins that the maximum legal TCP port
// 65535 is ACCEPTED (the existing suite only proves 65536/99999 are rejected,
// leaving the inclusive upper bound unpinned).
//
// Mutation that kills this: in Validate() change `p > 65535` to `p >= 65535`
// (off-by-one) — port 65535 is then wrongly rejected and this fails.
func TestValidateUpperPortBoundaryAccepted(t *testing.T) {
	err := Config{Addr: "127.0.0.1:65535", GracefulTimeout: time.Second}.Validate()
	require.NoError(t, err, "port 65535 is the inclusive max legal TCP port and must validate")
}

// TestValidateEphemeralPortZeroAccepted pins that port 0 (kernel-chosen
// ephemeral) stays valid — this is exactly how the test harness binds
// 127.0.0.1:0, so a regression here would break the whole integration suite.
//
// Mutation that kills this: change the lower-bound check to `p <= 0` — port 0
// is then rejected and this fails.
func TestValidateEphemeralPortZeroAccepted(t *testing.T) {
	require.NoError(t, Config{Addr: "127.0.0.1:0", GracefulTimeout: time.Second}.Validate())
	require.NoError(t, Config{Addr: ":0", GracefulTimeout: time.Second}.Validate(),
		"empty host with ephemeral port must validate")
}

// TestValidateRejectsMissingPort pins that an address with no port at all is
// rejected up front (net.SplitHostPort fails), rather than slipping through to
// a late, opaque net.Listen error.
//
// Mutation that kills this: short-circuit the SplitHostPort error (e.g. treat a
// split error as a bare host with default port) — a portless addr then passes
// and require.Error fails.
func TestValidateRejectsMissingPort(t *testing.T) {
	err := Config{Addr: "127.0.0.1", GracefulTimeout: time.Second}.Validate()
	require.Error(t, err, "an address with no port must be rejected before listen")
	assert.Contains(t, err.Error(), "invalid addr")
}

// TestValidateRejectsEmptyAddr pins the empty-address guard.
//
// Mutation that kills this: delete the `if c.Addr == ""` block — SplitHostPort
// on "" returns a different ("missing port") error but still errors, so to make
// this assertion load-bearing we pin the specific "must not be empty" message
// the dedicated guard produces.
func TestValidateRejectsEmptyAddr(t *testing.T) {
	err := Config{Addr: "", GracefulTimeout: time.Second}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

// TestValidateAcceptsIPv6 pins that a bracketed IPv6 host is accepted (net
// requires the [..] form; a naive host check that rejected ':' would break it).
//
// Mutation that kills this: replace the ParseIP/isHostname host check with a
// stricter one that rejects hosts containing ':' — "::1" then fails validation.
func TestValidateAcceptsIPv6(t *testing.T) {
	require.NoError(t, Config{Addr: "[::1]:80", GracefulTimeout: time.Second}.Validate())
}

// TestValidateHostnameAcceptanceContract is a DOCUMENTED-CONTRACT pin, not a bug
// claim. Validate's host check is deliberately permissive: it accepts any
// dot-separated host with no empty labels (isHostname) OR a parseable IP. It
// does NOT enforce RFC-1123 character rules, so syntactically-loose hosts like
// "under_score" pass here and are rejected later by net.Listen. We pin the
// CURRENT behavior in both directions so a future tightening/loosening is a
// conscious, reviewed change rather than a silent drift.
//
// LATENT RISK (noted, not fixed): the accept side is over-permissive (it does
// not reject illegal hostname characters). It is not load-bearing — net.Listen
// is the real gate and rejects a bogus host — so production is left
// byte-identical.
func TestValidateHostnameAcceptanceContract(t *testing.T) {
	// Empty-label hosts ARE rejected by isHostname.
	require.Error(t, Config{Addr: "a..b:80", GracefulTimeout: time.Second}.Validate(),
		"a host with an empty label must be rejected")
	require.Error(t, Config{Addr: ".leading:80", GracefulTimeout: time.Second}.Validate())

	// Permissive accepts (current, documented behavior).
	require.NoError(t, Config{Addr: "under_score:80", GracefulTimeout: time.Second}.Validate(),
		"underscore host is accepted by the permissive isHostname contract")
	require.NoError(t, Config{Addr: "localhost:80", GracefulTimeout: time.Second}.Validate())
}

// TestValidateRejectsNonNumericAndOutOfRangePorts pins both bad-port arms in one
// place to lock the contract.
//
// Mutation that kills this: delete either the Atoi-error arm or the range check
// in Validate() — the corresponding require.Error fails.
func TestValidateRejectsNonNumericAndOutOfRangePorts(t *testing.T) {
	require.Error(t, Config{Addr: "h:0x50", GracefulTimeout: time.Second}.Validate(),
		"hex/non-decimal port must be rejected")
	require.Error(t, Config{Addr: "h:99999", GracefulTimeout: time.Second}.Validate())
	require.Error(t, Config{Addr: "h:-1", GracefulTimeout: time.Second}.Validate())
}

// TestRunRejectsBadDurationViaTimeoutSink is a sink-side guard: run() must
// reject a non-positive GracefulTimeout BEFORE binding a listener, so the
// failure is fast config-validation and not a leaked socket. It is time-bounded
// so a regression that reached Serve cannot hang the suite.
//
// Mutation that kills this: remove the cfg.Validate() guard at the top of run()
// — a zero timeout would then reach net.Listen (binding a real ephemeral port),
// producing no error and leaving the assertion below to fail.
func TestRunRejectsBadDurationViaTimeoutSink(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), Config{Addr: "127.0.0.1:0", GracefulTimeout: 0}, func(net.Addr) {})
	}()
	select {
	case err := <-done:
		require.Error(t, err, "run must reject a non-positive graceful timeout up front")
		assert.Contains(t, err.Error(), "graceful timeout")
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return quickly on invalid config (possible missing pre-listen guard)")
	}
}

// TestLoadConfigRejectsBadAddrFromEnv pins that an invalid HELIX_ADVISORY_ADDR
// from the environment is surfaced by LoadConfig (env -> Validate path), the
// real production entry point.
//
// Mutation that kills this: drop the `cfg.Validate()` call at the end of
// LoadConfig — a bogus addr then loads "successfully" and require.Error fails.
func TestLoadConfigRejectsBadAddrFromEnv(t *testing.T) {
	t.Setenv("HELIX_ADVISORY_ADDR", "127.0.0.1:99999")
	t.Setenv("HELIX_ADVISORY_GRACEFUL_TIMEOUT", "")
	_, err := LoadConfig()
	require.Error(t, err, "an out-of-range port from the environment must be rejected by LoadConfig")
	assert.Contains(t, err.Error(), "out of range")
}
