package main

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
)

// This file is an ADVERSARIAL probe of the cmd/helix-security config seam and
// the hostile-input behaviour of the wired security service. The package is a
// thin CLI wrapper around internal/security; the only business logic that lives
// HERE is config parsing/validation (LoadConfig, Config.Validate, Config.Addr)
// plus the run() wiring. These tests attack the fail-OPEN directions that the
// existing main_test.go does not cover:
//
//   - the ACCEPTED boundaries of the port range (off-by-one in `< 1 || > 65535`)
//   - the ShutdownTimeout guard (a 0/negative timeout would turn a bounded
//     graceful-stop into an unbounded hang — a denial-of-service footgun)
//   - the HELIX_SECURITY_HOST env path + Config.Addr() composition
//   - hostile token bytes over the wire must DENY / report invalid, never panic
//     or fail open to Allowed=true.
//
// All assertions reflect the CURRENT (correct) behaviour: the config layer is
// fail-CLOSED and the authz layer is deny-by-default. Verdict from this run:
// NO production bug at the cmd seam; the gaps were missing coverage, now pinned.

// --- Config.Validate boundary probes -----------------------------------------

// TestValidateAcceptsPortBoundaries pins that the inclusive port range really is
// inclusive: 1 and 65535 are valid. An off-by-one mutation (`<= 1` or `>= 65535`)
// would wrongly reject a legitimate well-known/ephemeral boundary port.
func TestValidateAcceptsPortBoundaries(t *testing.T) {
	for _, p := range []int{1, 65535} {
		cfg := Config{Port: p, ShutdownTimeout: time.Second}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("port %d should be accepted, got %v", p, err)
		}
	}
}

// TestValidateRejectsPortBoundaryOffenders pins the just-outside-range values are
// rejected. Together with the accepted-boundary test this nails the comparison
// operators against off-by-one mutation in BOTH directions.
func TestValidateRejectsPortBoundaryOffenders(t *testing.T) {
	for _, p := range []int{0, -1, 65536, 70000} {
		cfg := Config{Port: p, ShutdownTimeout: time.Second}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("port %d should be rejected, got nil error", p)
		}
	}
}

// TestValidateRejectsNonPositiveShutdownTimeout is the key adversarial guard the
// existing suite omits. A ShutdownTimeout of 0 or negative is a fail-open into a
// HANG: run()'s graceful-stop path arms `context.WithTimeout(..., cfg.ShutdownTimeout)`;
// a non-positive duration fires the deadline immediately (0) or is treated as
// already-expired, but more importantly Validate() promises to reject it up front
// so the broken posture never reaches the listener. We assert it is rejected.
func TestValidateRejectsNonPositiveShutdownTimeout(t *testing.T) {
	for _, d := range []time.Duration{0, -1, -time.Hour} {
		cfg := Config{Port: defaultPort, ShutdownTimeout: d}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("shutdown timeout %s should be rejected, got nil error", d)
		}
	}
}

// TestRunRejectsNonPositiveShutdownTimeout proves the guard is enforced at the
// run() entry point too (defense in depth): a valid port with a zero timeout must
// NOT bind. This complements the existing TestRunRejectsInvalidConfig (which only
// exercises a bad port) and pins that removing the timeout branch of Validate, or
// the Validate() call in run(), is caught.
func TestRunRejectsNonPositiveShutdownTimeout(t *testing.T) {
	err := run(context.Background(), Config{Port: freePort(t), ShutdownTimeout: 0}, nil)
	if err == nil {
		t.Fatal("expected run to reject a zero shutdown timeout, got nil error")
	}
}

// --- Config.Addr composition --------------------------------------------------

// TestAddrComposition pins host:port joining for the empty-host (all-interfaces)
// and explicit-host cases. A regression that dropped the host or mis-joined would
// either bind the wrong interface or fail net.Listen.
func TestAddrComposition(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"", defaultPort, ":" + strconv.Itoa(defaultPort)},
		{"127.0.0.1", 51234, "127.0.0.1:51234"},
		{"::1", 9000, "[::1]:9000"}, // JoinHostPort must bracket IPv6 literals.
	}
	for _, tc := range cases {
		got := Config{Host: tc.host, Port: tc.port}.Addr()
		if got != tc.want {
			t.Fatalf("Addr(host=%q,port=%d) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

// --- LoadConfig env path ------------------------------------------------------

// TestLoadConfigHonoursHost pins the (otherwise untested) HELIX_SECURITY_HOST
// path: a supplied host flows into Config.Host and thus into Addr(). A regression
// that ignored the env var would silently bind all interfaces — a wider exposure
// surface than the operator asked for.
func TestLoadConfigHonoursHost(t *testing.T) {
	env := func(k string) string {
		if k == "HELIX_SECURITY_HOST" {
			return "127.0.0.1"
		}
		return ""
	}
	cfg, err := LoadConfig(env)
	if err != nil {
		t.Fatalf("LoadConfig with host override: %v", err)
	}
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("expected Host=127.0.0.1, got %q", cfg.Host)
	}
	if cfg.Addr() != "127.0.0.1:"+strconv.Itoa(defaultPort) {
		t.Fatalf("unexpected Addr() %q", cfg.Addr())
	}
}

// TestLoadConfigPortIsRangeCheckedNotJustParsed is the central anti-fail-open
// assertion for config parsing: strconv.Atoi alone is NOT a sufficient gate
// because it accepts a leading '+' and arbitrarily large magnitudes. We prove
// that a syntactically-parseable but OUT-OF-RANGE port ("+99999") is still
// rejected — i.e. LoadConfig runs Validate(), it does not stop at Atoi. A
// mutation that returned cfg right after the Atoi (dropping the Validate call)
// would let "+99999" through and fail this test.
func TestLoadConfigPortIsRangeCheckedNotJustParsed(t *testing.T) {
	// Sanity: Atoi itself accepts these, so only the range check rejects them.
	for _, raw := range []string{"+99999", "99999", "0", "+0"} {
		if _, err := strconv.Atoi(raw); err != nil {
			t.Fatalf("precondition: Atoi(%q) should parse, got %v", raw, err)
		}
		env := func(k string) string {
			if k == "HELIX_SECURITY_PORT" {
				return raw
			}
			return ""
		}
		if _, err := LoadConfig(env); err == nil {
			t.Fatalf("HELIX_SECURITY_PORT=%q parses via Atoi but is out of range; LoadConfig must reject it, got nil", raw)
		}
	}
}

// TestLoadConfigAcceptsLeadingPlusInRange documents a real Atoi quirk as a pinned
// CONTRACT, not a bug: "+50056" parses to 50056, which is in range, so LoadConfig
// accepts it. This is harmless (the value is range-validated) but surprising, so
// we pin it to prevent an accidental "fix" that would also reject valid ports, and
// to make the behaviour explicit for operators.
func TestLoadConfigAcceptsLeadingPlusInRange(t *testing.T) {
	env := func(k string) string {
		if k == "HELIX_SECURITY_PORT" {
			return "+50056"
		}
		return ""
	}
	cfg, err := LoadConfig(env)
	if err != nil {
		t.Fatalf("LoadConfig(+50056): unexpected error %v", err)
	}
	if cfg.Port != 50056 {
		t.Fatalf("expected port 50056, got %d", cfg.Port)
	}
}

// --- Hostile input over the wire: must DENY, never fail open / panic ----------

// TestAuthorizeHostileTokensDenied is the security sink-side probe (CLAUDE-1): no
// matter what bytes a caller presents as a token, Authorize on a fresh server
// (no roles loaded) must return Allowed=false and must not crash the service.
// This attacks the classic fail-open bug where a malformed/empty/oversized token
// is mishandled into an ACCEPT. We assert every hostile token is denied and the
// server remains responsive afterwards (it answers a follow-up RPC).
func TestAuthorizeHostileTokensDenied(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	hostile := []string{
		"",                       // empty
		"stub-token-not-real",    // the old PASS-bluff prefix
		"stub-token-admin",       // privilege-escalation attempt via prefix
		"../../etc/passwd",       // path-ish junk
		"' OR '1'='1",            // injection-ish junk
		string(make([]byte, 4096)), // 4KiB of NUL bytes
		"\x00\xff\xfe\x01",       // raw control/non-UTF8 bytes
	}

	for _, tok := range hostile {
		ctx, cancel := rpcCtx(t)
		resp, err := client.Authorize(ctx, &helixv1.AuthorizeRequest{
			Token:    tok,
			Resource: "cluster",
			Action:   "admin",
		})
		cancel()
		// An error (e.g. InvalidArgument for empty token) is acceptable fail-closed
		// behaviour. What is NOT acceptable is err==nil with Allowed==true.
		if err == nil && resp != nil && resp.Allowed {
			t.Fatalf("FAIL-OPEN: hostile token %q was AUTHORIZED (Allowed=true) on a server with no roles loaded", tok)
		}
	}

	// The service must still be alive after the hostile barrage (no panic/DoS):
	// a normal Authenticate must still succeed.
	ctx, cancel := rpcCtx(t)
	defer cancel()
	auth, err := client.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "user1"})
	if err != nil || auth == nil || !auth.Success {
		t.Fatalf("service unresponsive after hostile token barrage: err=%v resp=%+v", err, auth)
	}
}

// TestValidateTokenHostileBytes proves ValidateToken treats hostile/unissued
// token bytes as INVALID (Valid=false) rather than rubber-stamping them, and that
// it does not panic on non-UTF8 / oversized input.
func TestValidateTokenHostileBytes(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	for _, tok := range []string{"stub-token-x", "\x00\x00\x00", string(make([]byte, 8192))} {
		ctx, cancel := rpcCtx(t)
		resp, err := client.ValidateToken(ctx, &helixv1.ValidateTokenRequest{Token: tok})
		cancel()
		if err == nil && resp != nil && resp.Valid {
			t.Fatalf("FAIL-OPEN: hostile/unissued token %q reported Valid=true", tok)
		}
	}
}

// guard against an unused import if net is ever dropped from helpers.
var _ = net.JoinHostPort
