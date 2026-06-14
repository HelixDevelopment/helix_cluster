package watchtower

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// oracleDigest is an INDEPENDENT integrity-digest oracle used only by the
// adversarial suite. It deliberately uses a different hash (SHA-512) and a
// different domain-separation prefix from the production code and from the
// existing watchtower_test.go helper, so that a mutation which corrupts the
// production comparison (e.g. always-equal) cannot also corrupt the expected
// values these tests assert against. The expected digest is always DERIVED
// from the command bytes here — never hardcoded.
func oracleDigest(cmd string) string {
	sum := sha512.Sum512([]byte("watchtower-oracle:" + cmd))
	return hex.EncodeToString(sum[:])
}

// mustRegister registers and fails the test on error.
func mustRegister(t *testing.T, p *Prober, id, digest string) {
	t.Helper()
	if err := p.Register(id, digest); err != nil {
		t.Fatalf("Register(%q): unexpected error %v", id, err)
	}
}

// ----------------------------------------------------------------------------
// CENTERPIECE 1: tamper detection across every byte-level mutation class.
//
// A registered instance is genuine; we then present four distinct tampered
// observations — same length / changed bytes, truncated, extended, and totally
// different — plus a control genuine observation. Each tampered observation MUST
// fail integrity AND raise the flag; the genuine control MUST pass and not flag.
//
// Why it bites: an integrity comparison that is fail-open (always IntegrityOK),
// length-only (would pass same-length tamper), substring/prefix based (would
// pass truncation or extension), or inverted (would fail the genuine control)
// is caught by at least one row here. The expected baseline comes from the
// independent SHA-512 oracle, so an always-true production compare cannot hide
// behind a co-mutated expectation.
// ----------------------------------------------------------------------------
func TestAdversarialTamperByteClassesAllFailIntegrityAndFlag(t *testing.T) {
	const genuineCmd = "/usr/bin/helixd --role=worker --port=8080 --attest=on"
	genuine := oracleDigest(genuineCmd)

	// Build tampered digests that exercise distinct comparison-bypass classes.
	sameLen := []byte(genuine)
	// Flip one hex nibble in the middle without changing length.
	mid := len(sameLen) / 2
	if sameLen[mid] == 'a' {
		sameLen[mid] = 'b'
	} else {
		sameLen[mid] = 'a'
	}
	sameLenTamper := string(sameLen)
	if len(sameLenTamper) != len(genuine) {
		t.Fatalf("setup: same-length tamper changed length")
	}
	if sameLenTamper == genuine {
		t.Fatalf("setup: same-length tamper did not differ")
	}

	truncated := genuine[:len(genuine)-1]             // observed is a prefix of expected
	extended := genuine + "00"                        // expected is a prefix of observed
	totallyDifferent := oracleDigest("rm -rf / #pwn") // unrelated command

	type probe struct {
		name        string
		observed    string
		wantInteg   bool
		wantFlagged bool
	}
	probes := []probe{
		{"genuine_control", genuine, true, false},
		{"same_length_changed_byte", sameLenTamper, false, true},
		{"truncated_prefix", truncated, false, true},
		{"extended_suffix", extended, false, true},
		{"totally_different", totallyDifferent, false, true},
	}

	for _, pr := range probes {
		t.Run(pr.name, func(t *testing.T) {
			// Fresh prober per row so sticky flagging cannot leak across rows.
			p := NewProber()
			mustRegister(t, p, "inst", genuine)

			res, err := p.Check("inst", true, pr.observed)
			if err != nil {
				t.Fatalf("Check: unexpected error %v", err)
			}
			t.Logf("observed=%q.. integrityOK=%v flagged=%v",
				truncForLog(pr.observed), res.IntegrityOK, res.Flagged)

			if res.IntegrityOK != pr.wantInteg {
				t.Fatalf("IntegrityOK=%v want %v (tamper class %q bypassed the digest compare)",
					res.IntegrityOK, pr.wantInteg, pr.name)
			}
			if res.Flagged != pr.wantFlagged {
				t.Fatalf("Flagged=%v want %v for %q", res.Flagged, pr.wantFlagged, pr.name)
			}
			if p.IsFlagged("inst") != pr.wantFlagged {
				t.Fatalf("IsFlagged=%v want %v for %q", p.IsFlagged("inst"), pr.wantFlagged, pr.name)
			}
		})
	}
}

func truncForLog(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// A tampered observation that happens to be a SUPERSTRING of the genuine digest
// (genuine fully contained inside observed) must still fail. Pins that the
// compare is full-equality, not strings.Contains / HasPrefix.
func TestAdversarialTamperSubstringContainmentStillFails(t *testing.T) {
	genuine := oracleDigest("/usr/bin/helixd")
	observed := "PRE" + genuine + "POST"
	if !strings.Contains(observed, genuine) {
		t.Fatalf("setup: observed must contain genuine")
	}

	p := NewProber()
	mustRegister(t, p, "inst", genuine)

	res, err := p.Check("inst", true, observed)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.IntegrityOK {
		t.Fatalf("containment of genuine inside observed MUST NOT pass integrity (substring bypass)")
	}
	if !res.Flagged {
		t.Fatalf("substring-containment tamper MUST be flagged")
	}
}

// ----------------------------------------------------------------------------
// CENTERPIECE 2: flag stickiness is durable, not single-shot.
//
// Once a tamper raises the flag, an arbitrary number of subsequent CLEAN probes
// (and even a clean+live recovery) must NOT self-clear it. Only documented
// re-Register clears it. A non-sticky implementation that recomputes Flagged
// from the current probe alone is caught after the very first clean probe; this
// test additionally hammers many clean probes to catch a "clear after N" bug.
// ----------------------------------------------------------------------------
func TestAdversarialFlagStaysStickyAcrossManyCleanProbes(t *testing.T) {
	cmd := "/usr/bin/helixd --role=worker"
	genuine := oracleDigest(cmd)

	p := NewProber()
	mustRegister(t, p, "inst", genuine)

	// Raise the flag via a same-length byte tamper.
	tamper := []byte(genuine)
	tamper[0] = nextHex(tamper[0])
	if _, err := p.Check("inst", true, string(tamper)); err != nil {
		t.Fatalf("tamper Check: %v", err)
	}
	if !p.IsFlagged("inst") {
		t.Fatalf("expected flagged after tamper")
	}

	// Now hammer many clean, live probes. The flag must persist on every one.
	for i := 0; i < 50; i++ {
		res, err := p.Check("inst", true, genuine)
		if err != nil {
			t.Fatalf("clean Check #%d: %v", i, err)
		}
		if !res.IntegrityOK {
			t.Fatalf("clean Check #%d: IntegrityOK must be true on matching digest", i)
		}
		if !res.Flagged {
			t.Fatalf("clean Check #%d: flag self-cleared (stickiness violated)", i)
		}
		if !p.IsFlagged("inst") {
			t.Fatalf("clean Check #%d: IsFlagged self-cleared", i)
		}
	}

	// Documented escape hatch: re-Register clears it.
	mustRegister(t, p, "inst", genuine)
	if p.IsFlagged("inst") {
		t.Fatalf("re-Register MUST clear the sticky flag")
	}
	// And a clean probe after re-attest stays clean.
	res, err := p.Check("inst", true, genuine)
	if err != nil {
		t.Fatalf("post re-register Check: %v", err)
	}
	if res.Flagged {
		t.Fatalf("clean instance after re-Register must not be flagged")
	}
}

func nextHex(b byte) byte {
	if b == 'a' {
		return 'b'
	}
	if b >= '0' && b <= '8' {
		return b + 1
	}
	if b == '9' {
		return 'a'
	}
	return 'a'
}

// Stickiness must also survive a liveness recovery: dead -> flagged, then a
// fully healthy (live + clean) probe must keep the flag set.
func TestAdversarialStickyAfterLivenessRecovery(t *testing.T) {
	genuine := oracleDigest("/usr/bin/helixd")
	p := NewProber()
	mustRegister(t, p, "inst", genuine)

	// Dead but clean -> flagged on liveness.
	if _, err := p.Check("inst", false, genuine); err != nil {
		t.Fatalf("dead Check: %v", err)
	}
	if !p.IsFlagged("inst") {
		t.Fatalf("dead instance must be flagged")
	}

	// Recovered: live + clean. Flag must remain sticky.
	res, err := p.Check("inst", true, genuine)
	if err != nil {
		t.Fatalf("recovery Check: %v", err)
	}
	if !res.Live || !res.IntegrityOK {
		t.Fatalf("recovery probe should report live + integrityOK, got live=%v integ=%v", res.Live, res.IntegrityOK)
	}
	if !res.Flagged {
		t.Fatalf("flag must stay sticky after liveness recovery")
	}
}

// ----------------------------------------------------------------------------
// INDEPENDENCE: the full liveness x integrity truth table, including the
// dead+tampered corner the existing suite omits. Integrity must NOT be coupled
// to liveness and vice versa: flag = !live OR !integrityOK.
// ----------------------------------------------------------------------------
func TestAdversarialLivenessIntegrityTruthTable(t *testing.T) {
	genuine := oracleDigest("/usr/bin/helixd --role=coordinator")
	tampered := oracleDigest("/usr/bin/helixd --role=coordinator --backdoor")

	type row struct {
		name        string
		live        bool
		observed    string
		wantLive    bool
		wantInteg   bool
		wantFlagged bool
	}
	rows := []row{
		{"live_clean", true, genuine, true, true, false},
		{"live_tampered", true, tampered, true, false, true},
		{"dead_clean", false, genuine, false, true, true},
		{"dead_tampered", false, tampered, false, false, true},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			p := NewProber()
			mustRegister(t, p, "n", genuine)
			res, err := p.Check("n", r.live, r.observed)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			t.Logf("live=%v integ=%v flagged=%v", res.Live, res.IntegrityOK, res.Flagged)
			if res.Live != r.wantLive {
				t.Fatalf("Live=%v want %v", res.Live, r.wantLive)
			}
			if res.IntegrityOK != r.wantInteg {
				t.Fatalf("IntegrityOK=%v want %v (liveness wrongly coupled into integrity?)",
					res.IntegrityOK, r.wantInteg)
			}
			if res.Flagged != r.wantFlagged {
				t.Fatalf("Flagged=%v want %v", res.Flagged, r.wantFlagged)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// EDGES: empty/unknown/re-register-with-new-baseline, fail-closed, no panic.
// ----------------------------------------------------------------------------

// An unknown (never-registered) instance must return ErrUnknownInstance and a
// zero Result — never panic, never a silent pass.
func TestAdversarialUnknownInstanceFailsClosed(t *testing.T) {
	p := NewProber()
	res, err := p.Check("ghost", true, "whatever")
	if !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("err=%v want ErrUnknownInstance", err)
	}
	if res.IntegrityOK || res.Flagged || res.Live {
		t.Fatalf("unknown instance must yield zero Result, got %+v", res)
	}
	if p.IsFlagged("ghost") {
		t.Fatalf("unknown instance must not appear flagged")
	}
}

// Empty instance ID is rejected by Register; nothing is stored.
func TestAdversarialEmptyInstanceIDRejected(t *testing.T) {
	p := NewProber()
	if err := p.Register("", oracleDigest("x")); !errors.Is(err, ErrEmptyInstanceID) {
		t.Fatalf("Register(\"\") err=%v want ErrEmptyInstanceID", err)
	}
	if len(p.Flagged()) != 0 {
		t.Fatalf("rejected register must not create state")
	}
	// And a Check on that empty ID is unknown (fail-closed), not a silent pass.
	if _, err := p.Check("", true, "x"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("Check(\"\") err=%v want ErrUnknownInstance", err)
	}
}

// Empty-digest baseline: an instance pinned to an empty expected digest passes
// integrity ONLY for an empty observed digest, and fails (flags) for any
// non-empty observation. This pins that empty is a real, exact value — not a
// wildcard that matches anything (a fail-open hazard).
func TestAdversarialEmptyDigestBaselineIsExactNotWildcard(t *testing.T) {
	p := NewProber()
	mustRegister(t, p, "e", "") // expected digest is the empty string

	// Empty observed matches empty expected -> passes.
	res, err := p.Check("e", true, "")
	if err != nil {
		t.Fatalf("Check empty/empty: %v", err)
	}
	if !res.IntegrityOK || res.Flagged {
		t.Fatalf("empty==empty must pass integrity unflagged, got integ=%v flagged=%v",
			res.IntegrityOK, res.Flagged)
	}

	// Non-empty observed against empty baseline -> tamper, flagged.
	p2 := NewProber()
	mustRegister(t, p2, "e", "")
	res2, err := p2.Check("e", true, oracleDigest("anything"))
	if err != nil {
		t.Fatalf("Check empty/nonempty: %v", err)
	}
	if res2.IntegrityOK {
		t.Fatalf("empty baseline must NOT match a non-empty observation (wildcard fail-open)")
	}
	if !res2.Flagged {
		t.Fatalf("mismatch against empty baseline must flag")
	}
}

// Re-Register with a DIFFERENT baseline both clears the old flag AND re-pins the
// expectation: an observation matching the OLD digest must now read as tampered,
// and one matching the NEW digest passes.
func TestAdversarialReRegisterRepinsBaseline(t *testing.T) {
	oldCmd := "/usr/bin/helixd --v1"
	newCmd := "/usr/bin/helixd --v2"
	oldDigest := oracleDigest(oldCmd)
	newDigest := oracleDigest(newCmd)
	if oldDigest == newDigest {
		t.Fatalf("setup: digests must differ")
	}

	p := NewProber()
	mustRegister(t, p, "inst", oldDigest)

	// Flag it via tamper.
	if _, err := p.Check("inst", true, newDigest); err != nil {
		t.Fatalf("tamper Check: %v", err)
	}
	if !p.IsFlagged("inst") {
		t.Fatalf("expected flagged before re-register")
	}

	// Re-attest to the NEW baseline.
	mustRegister(t, p, "inst", newDigest)
	if p.IsFlagged("inst") {
		t.Fatalf("re-Register must clear flag")
	}

	// Old digest is now the tampered one.
	resOld, err := p.Check("inst", true, oldDigest)
	if err != nil {
		t.Fatalf("Check old after repin: %v", err)
	}
	if resOld.IntegrityOK {
		t.Fatalf("old digest must read as tamper against the new baseline")
	}
	if !resOld.Flagged {
		t.Fatalf("old digest against new baseline must flag")
	}

	// New digest passes (re-register again to clear the flag we just set).
	mustRegister(t, p, "inst", newDigest)
	resNew, err := p.Check("inst", true, newDigest)
	if err != nil {
		t.Fatalf("Check new after repin: %v", err)
	}
	if !resNew.IntegrityOK || resNew.Flagged {
		t.Fatalf("new digest must pass clean against new baseline, got integ=%v flagged=%v",
			resNew.IntegrityOK, resNew.Flagged)
	}
}

// Flagged() returns a sorted, de-duplicated snapshot covering exactly the
// flagged instances — used by quarantine logic, so it must be accurate.
func TestAdversarialFlaggedSetSnapshotAccurate(t *testing.T) {
	p := NewProber()
	good := oracleDigest("good")
	for _, id := range []string{"a", "b", "c"} {
		mustRegister(t, p, id, good)
	}
	// Tamper b only.
	if _, err := p.Check("b", true, oracleDigest("evil")); err != nil {
		t.Fatalf("Check b: %v", err)
	}
	// a and c stay clean.
	if _, err := p.Check("a", true, good); err != nil {
		t.Fatalf("Check a: %v", err)
	}
	if _, err := p.Check("c", true, good); err != nil {
		t.Fatalf("Check c: %v", err)
	}

	got := p.Flagged()
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("Flagged()=%v want [b]", got)
	}
}
