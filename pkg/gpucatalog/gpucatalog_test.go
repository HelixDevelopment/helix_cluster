package gpucatalog

// Tests in this file satisfy CLAUDE-1: every assertion is on real,
// observable sink-side data (CatalogEntry fields including the caller-supplied
// RunID) rather than on structurally-guaranteed or always-true predicates.
//
// MUTATION GUARD SUMMARY
// ─────────────────────────────────────────────────────────────────────────────
// Guard location : catalog["H100"] = 5.0  and  catalog["A100"] = 3.2
// Pinned by      : TestCatalogOrdering
// Kill condition : Swapping those two values (H100 ↔ A100) makes
//                  TestCatalogOrdering fail because
//                  h100Mult (3.2) > a100Mult (5.0) becomes false.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// newRunID generates a unique test run identifier so tests can assert on it.
func newRunID(tag string) string {
	return fmt.Sprintf("test-%s-%d", tag, time.Now().UnixNano())
}

// ─────────────────────────────────────────────────────────────────────────────
// TestCatalogOrdering
//
// MUTATION GUARD: swapping catalog["H100"] and catalog["A100"] MUST make this
// test fail at the "h100Mult > a100Mult" assertion.
// ─────────────────────────────────────────────────────────────────────────────
func TestCatalogOrdering(t *testing.T) {
	runID := newRunID("ordering")

	h100Entry := Lookup(runID, "H100", true)
	a100Entry := Lookup(runID, "A100", true)
	unknownEntry := Lookup(runID, "NONEXISTENT_GPU_XYZ", true)

	// ── Sink-side: RunID must be echoed back verbatim ──────────────────────
	for _, e := range []CatalogEntry{h100Entry, a100Entry, unknownEntry} {
		if e.RunID != runID {
			t.Errorf("entry RunID = %q; want %q (model=%s)", e.RunID, runID, e.Model)
		}
	}

	// ── Known / unknown flags ──────────────────────────────────────────────
	if !h100Entry.Known {
		t.Errorf("H100: Known = false; want true")
	}
	if !a100Entry.Known {
		t.Errorf("A100: Known = false; want true")
	}
	if unknownEntry.Known {
		t.Errorf("NONEXISTENT_GPU_XYZ: Known = true; want false")
	}
	if unknownEntry.Multiplier != defaultMultiplier {
		t.Errorf("unknown model multiplier = %v; want defaultMultiplier (%v)",
			unknownEntry.Multiplier, defaultMultiplier)
	}

	// ── ORDERING INVARIANT (mutation guard) ───────────────────────────────
	// Swapping H100 ↔ A100 in the catalog makes h100Entry.Multiplier < a100Entry.Multiplier,
	// causing the assertion below to fail.
	h100Mult := h100Entry.Multiplier
	a100Mult := a100Entry.Multiplier
	defMult := unknownEntry.Multiplier

	if !(h100Mult > a100Mult) {
		t.Errorf("ordering violated: H100 multiplier (%v) must be > A100 multiplier (%v)",
			h100Mult, a100Mult)
	}
	if !(a100Mult > defMult) {
		t.Errorf("ordering violated: A100 multiplier (%v) must be > default multiplier (%v)",
			a100Mult, defMult)
	}

	// Transitivity: H100 > default
	if !(h100Mult > defMult) {
		t.Errorf("ordering violated: H100 multiplier (%v) must be > default multiplier (%v)",
			h100Mult, defMult)
	}

	// ── Timestamp must be recent (sink-side liveness check) ───────────────
	for _, e := range []CatalogEntry{h100Entry, a100Entry, unknownEntry} {
		age := time.Since(e.QueriedAt)
		if age < 0 || age > 5*time.Second {
			t.Errorf("entry QueriedAt is stale or future: age=%v (model=%s)", age, e.Model)
		}
	}

	t.Logf("RunID=%s  H100=%.4f  A100=%.4f  default=%.4f",
		runID, h100Mult, a100Mult, defMult)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAttestationScoring
//
// An un-attested GPU MUST score strictly below the same attested GPU.
// MUTATION GUARD: increasing attestationFactor to ≥ 1.0 kills this test.
// ─────────────────────────────────────────────────────────────────────────────
func TestAttestationScoring(t *testing.T) {
	runID := newRunID("attestation")

	models := []string{"H100", "A100", "V100", "T4", "NONEXISTENT_GPU"}

	for _, model := range models {
		attested := Lookup(runID, model, true)
		unattested := Lookup(runID, model, false)

		// ── RunID echoed ────────────────────────────────────────────────
		if attested.RunID != runID {
			t.Errorf("[%s] attested RunID = %q; want %q", model, attested.RunID, runID)
		}
		if unattested.RunID != runID {
			t.Errorf("[%s] unattested RunID = %q; want %q", model, unattested.RunID, runID)
		}

		// ── Attestation flag ────────────────────────────────────────────
		if !attested.Attested {
			t.Errorf("[%s] attested entry has Attested=false", model)
		}
		if unattested.Attested {
			t.Errorf("[%s] unattested entry has Attested=true", model)
		}

		// ── Score ordering: attested > unattested ───────────────────────
		// MUTATION GUARD: set attestationFactor = 1.0 → scores become equal → FAIL.
		if !(attested.Score > unattested.Score) {
			t.Errorf("[%s] attested score (%v) must be > unattested score (%v)",
				model, attested.Score, unattested.Score)
		}

		// ── Same multiplier regardless of attestation ───────────────────
		if attested.Multiplier != unattested.Multiplier {
			t.Errorf("[%s] multiplier should not differ by attestation: attested=%v unattested=%v",
				model, attested.Multiplier, unattested.Multiplier)
		}

		t.Logf("RunID=%s model=%-20s attested-score=%.4f unattested-score=%.4f",
			runID, model, attested.Score, unattested.Score)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLookupMultiplierKnownModels
//
// Verifies that every model returned by KnownModels() has a multiplier
// strictly above defaultMultiplier, and that the function works case-insensitively.
// ─────────────────────────────────────────────────────────────────────────────
func TestLookupMultiplierKnownModels(t *testing.T) {
	runID := newRunID("known-models")

	known := KnownModels()
	if len(known) == 0 {
		t.Fatal("KnownModels returned empty slice")
	}

	for _, m := range known {
		mult := LookupMultiplier(m)
		if mult <= defaultMultiplier {
			t.Errorf("[RunID=%s] model %s: multiplier %v <= defaultMultiplier %v",
				runID, m, mult, defaultMultiplier)
		}
		// Case-insensitive: lower-case lookup should match
		multLower := LookupMultiplier(strings.ToLower(m))
		if mult != multLower {
			t.Errorf("[RunID=%s] model %s: upper=%v lower=%v — case normalisation broken",
				runID, m, mult, multLower)
		}
		t.Logf("RunID=%s model=%-6s multiplier=%.4f", runID, m, mult)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLookupMultiplierUnknown
//
// An unknown model must return exactly defaultMultiplier via both LookupMultiplier
// and via the Lookup helper.
// ─────────────────────────────────────────────────────────────────────────────
func TestLookupMultiplierUnknown(t *testing.T) {
	runID := newRunID("unknown")

	cases := []string{"RTX3090", "GTX1080", "", "   ", "NONEXISTENT"}
	for _, m := range cases {
		got := LookupMultiplier(m)
		if got != defaultMultiplier {
			t.Errorf("[RunID=%s] LookupMultiplier(%q) = %v; want defaultMultiplier %v",
				runID, m, got, defaultMultiplier)
		}
		entry := Lookup(runID, m, true)
		if entry.RunID != runID {
			t.Errorf("[RunID=%s] Lookup RunID mismatch: got %q", runID, entry.RunID)
		}
		if entry.Known {
			t.Errorf("[RunID=%s] Lookup(%q) has Known=true; want false", runID, m)
		}
		if entry.Multiplier != defaultMultiplier {
			t.Errorf("[RunID=%s] Lookup(%q).Multiplier = %v; want %v",
				runID, m, entry.Multiplier, defaultMultiplier)
		}
		t.Logf("RunID=%s model=%q multiplier=%.4f known=%v", runID, m, entry.Multiplier, entry.Known)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestScoreFunctionDirectly
//
// Tests the exported Score() convenience function to verify it delegates
// correctly to the same logic as Lookup().
// ─────────────────────────────────────────────────────────────────────────────
func TestScoreFunctionDirectly(t *testing.T) {
	runID := newRunID("score-direct")
	models := []string{"H100", "A100", "UNKNOWN_GPU_777"}

	for _, m := range models {
		attestedScore := Score(m, true)
		unattestedScore := Score(m, false)

		if !(attestedScore > unattestedScore) {
			t.Errorf("[RunID=%s] Score(%q,true)=%v must be > Score(%q,false)=%v",
				runID, m, attestedScore, m, unattestedScore)
		}

		// Score(m,true) must equal LookupMultiplier(m)
		mult := LookupMultiplier(m)
		if attestedScore != mult {
			t.Errorf("[RunID=%s] Score(%q,true)=%v; want LookupMultiplier=%v",
				runID, m, attestedScore, mult)
		}

		// Score(m,false) must equal mult * attestationFactor
		want := mult * attestationFactor
		if unattestedScore != want {
			t.Errorf("[RunID=%s] Score(%q,false)=%v; want %.4f",
				runID, m, unattestedScore, want)
		}

		t.Logf("RunID=%s model=%-20s attested=%.4f unattested=%.4f", runID, m, attestedScore, unattestedScore)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestCatalogEntryString
//
// The String() method must include the RunID and model name so log lines
// are unambiguously attributable to a specific query.
// ─────────────────────────────────────────────────────────────────────────────
func TestCatalogEntryString(t *testing.T) {
	runID := newRunID("string-repr")

	entry := Lookup(runID, "H100", true)
	s := entry.String()

	if !strings.Contains(s, runID) {
		t.Errorf("CatalogEntry.String() does not contain RunID %q: %s", runID, s)
	}
	if !strings.Contains(s, "H100") {
		t.Errorf("CatalogEntry.String() does not contain model name: %s", s)
	}
	if !strings.Contains(s, "attested") {
		t.Errorf("CatalogEntry.String() does not contain attestation status: %s", s)
	}

	t.Logf("RunID=%s entry=%s", runID, s)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAttestationFactorLessThanOne
//
// The package-level constant attestationFactor must be < 1.0; if it is ever
// changed to ≥ 1.0 this test fails before TestAttestationScoring can catch it.
// ─────────────────────────────────────────────────────────────────────────────
func TestAttestationFactorLessThanOne(t *testing.T) {
	if attestationFactor >= 1.0 {
		t.Errorf("attestationFactor = %v; must be < 1.0 to guarantee attested > unattested scores",
			attestationFactor)
	}
	if attestationFactor <= 0.0 {
		t.Errorf("attestationFactor = %v; must be > 0.0 to produce a positive score",
			attestationFactor)
	}
	t.Logf("attestationFactor=%.4f (valid range: 0 < x < 1)", attestationFactor)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestDefaultMultiplierIsPositive
//
// The default multiplier must be positive; a zero or negative default would
// make the catalog ordering invariant trivially meaningless.
// ─────────────────────────────────────────────────────────────────────────────
func TestDefaultMultiplierIsPositive(t *testing.T) {
	if defaultMultiplier <= 0 {
		t.Errorf("defaultMultiplier = %v; must be > 0", defaultMultiplier)
	}
	t.Logf("defaultMultiplier=%.4f", defaultMultiplier)
}
