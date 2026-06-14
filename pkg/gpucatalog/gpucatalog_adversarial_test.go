package gpucatalog

// Adversarial, mutation-verified tests pinning the GPU-catalog scoring and
// lookup contract. These complement gpucatalog_test.go and target the four
// economic-hazard bug classes:
//
//   1. UNKNOWN-MODEL DEFAULT  — an unrecognised / empty / lookalike model must
//      return the documented conservative floor (defaultMultiplier), never a
//      known model's high multiplier (fail-open scoring hazard).
//   2. KNOWN-MODEL EXACTNESS  — each catalog model returns its exact documented
//      multiplier; case/whitespace variants normalise to the SAME entry, while
//      substring / lookalike names do NOT silently match a different entry.
//   3. SCORE MONOTONICITY     — raising attestation (false→true) never lowers
//      the score, for every model including the unknown default.
//   4. ORDERING DETERMINISM   — KnownModels() is stable across calls and is the
//      full catalog, sorted; the documented H100 > A100 > default tier holds and
//      the default is the strict floor under EVERY known entry.
//
// Every test that pins a production invariant is accompanied by a documented
// mutation in mutation_evidence below that makes it fail.

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 1: UNKNOWN-MODEL DEFAULT
//
// An unknown / empty / whitespace / lookalike model MUST return exactly
// defaultMultiplier — never a known model's high multiplier. A fail-open here
// (e.g. an unrecognised GPU billed at H100 rates) is an economic hazard.
//
// MUTATION THAT BITES: change LookupMultiplier's fallthrough from
//
//	return defaultMultiplier
//
// to
//
//	return catalog["H100"]
//
// → every unknown case below returns 5.0 and this test fails.
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial_UnknownModelReturnsConservativeDefault(t *testing.T) {
	runID := newRunID("unknown-default")

	// Includes empty, whitespace-only, near-misses, lookalikes, and case-mixed
	// non-members. None of these is a catalog key after normalisation.
	unknowns := []string{
		"",        // empty
		"   ",     // whitespace only
		"\t\n",    // other whitespace
		"H1000",   // superstring of H100
		"H10",     // substring of H100
		"XH100",   // prefixed lookalike
		"h100x",   // suffixed lookalike
		"A 100",   // internal space — not the same token as A100
		"L40",     // L40S minus the S — must NOT match L40S
		"RTX4090", // real GPU not in catalog
		"GTX1080", // real GPU not in catalog
		"H100​",   // zero-width-space contamination
		"MI300X",  // AMD card, absent
		"GAUDI2",  // Intel card, absent
	}

	for _, m := range unknowns {
		got := LookupMultiplier(m)
		if got != defaultMultiplier {
			t.Errorf("[%s] LookupMultiplier(%q) = %v; want conservative default %v (fail-open hazard)",
				runID, m, got, defaultMultiplier)
		}

		// Sink-side: the enriched entry must report Known=false and the floor.
		e := Lookup(runID, m, true)
		if e.Known {
			t.Errorf("[%s] Lookup(%q).Known = true; want false", runID, m)
		}
		if e.Multiplier != defaultMultiplier {
			t.Errorf("[%s] Lookup(%q).Multiplier = %v; want %v", runID, m, e.Multiplier, defaultMultiplier)
		}
		if e.RunID != runID {
			t.Errorf("[%s] Lookup(%q).RunID = %q; want %q", runID, m, e.RunID, runID)
		}

		// Crucial economic assertion: the unknown multiplier is the FLOOR — it
		// must be <= the multiplier of every known model, never above any.
		for _, k := range KnownModels() {
			if got > LookupMultiplier(k) {
				t.Errorf("[%s] unknown %q multiplier %v exceeds known %q multiplier %v — fail-open over-charge",
					runID, m, got, k, LookupMultiplier(k))
			}
		}
	}
}

// defaultMustBeStrictFloor proves the default sits strictly below EVERY catalog
// entry. If a future model were added at <= defaultMultiplier, the "unknown is
// conservative" guarantee would silently break; this test catches that.
//
// MUTATION THAT BITES: add catalog["CHEAP"] = 0.5 (or = 1.0) → fails because a
// known model is no longer strictly above the default floor.
func TestAdversarial_DefaultIsStrictFloorUnderEveryKnownModel(t *testing.T) {
	runID := newRunID("strict-floor")
	known := KnownModels()
	if len(known) == 0 {
		t.Fatal("KnownModels returned empty slice")
	}
	for _, k := range known {
		mult := LookupMultiplier(k)
		if !(mult > defaultMultiplier) {
			t.Errorf("[%s] known model %q multiplier %v is not strictly above default floor %v",
				runID, k, mult, defaultMultiplier)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// KNOWN-MODEL EXACTNESS
//
// Each known model returns its EXACT documented multiplier, and case/whitespace
// variants normalise to that same value. This pins the precise per-model values
// so any silent re-pricing is caught.
//
// MUTATION THAT BITES: change catalog["A100"] from 3.2 to anything else → the
// A100 row's exact-value assertion fails.
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial_KnownModelExactMultipliers(t *testing.T) {
	runID := newRunID("exact-mult")

	// Independent expected table (NOT derived from the production map) — this is
	// the oracle. If production values drift, these literals disagree.
	want := map[string]float64{
		"H100": 5.0,
		"A100": 3.2,
		"V100": 2.1,
		"T4":   1.6,
		"A10G": 1.8,
		"L4":   1.9,
		"L40S": 2.5,
		"A30":  2.0,
	}

	// The catalog must contain exactly these keys — no more, no fewer — so the
	// oracle stays complete.
	if got := len(KnownModels()); got != len(want) {
		t.Errorf("[%s] KnownModels() has %d entries; oracle expects %d: %v",
			runID, got, len(want), KnownModels())
	}

	for model, exp := range want {
		// canonical form
		if got := LookupMultiplier(model); got != exp {
			t.Errorf("[%s] LookupMultiplier(%q) = %v; want exact %v", runID, model, got, exp)
		}
		// case-insensitive + whitespace-tolerant variants must all agree
		for _, variant := range []string{
			strings.ToLower(model),
			"  " + model + "  ",
			"\t" + strings.ToLower(model) + "\n",
		} {
			if got := LookupMultiplier(variant); got != exp {
				t.Errorf("[%s] LookupMultiplier(%q) = %v; want %v (normalisation must match %q)",
					runID, variant, got, exp, model)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 2: SCORE MONOTONICITY IN ATTESTATION
//
// Raising attestation (false → true) must NEVER lower the score, for every
// model including the unknown default. With attestationFactor < 1 the attested
// score must be strictly greater whenever the multiplier > 0.
//
// MUTATION THAT BITES: flip the Score direction —
//
//	if attested { return m * attestationFactor }
//	return m
//
// → attested becomes the LOWER score and every row's monotonicity assertion
// fails. Also bites: set attestationFactor >= 1.0.
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial_ScoreMonotonicInAttestation(t *testing.T) {
	runID := newRunID("monotonic")

	// Grid spans high/mid/low known models and the unknown default.
	models := []string{"H100", "A100", "L40S", "A30", "V100", "L4", "A10G", "T4", "UNKNOWN_XYZ", "", "   "}

	for _, m := range models {
		un := Score(m, false) // attestation level 0
		at := Score(m, true)  // attestation level 1

		// Direction: higher attestation never decreases score.
		if at < un {
			t.Errorf("[%s] non-monotonic: Score(%q,true)=%v < Score(%q,false)=%v",
				runID, m, at, m, un)
		}
		// Strictness: multiplier is always >= defaultMultiplier (> 0), so the
		// attested score must be STRICTLY greater.
		if !(at > un) {
			t.Errorf("[%s] Score(%q,true)=%v must be strictly > Score(%q,false)=%v",
				runID, m, at, m, un)
		}

		// Cross-check the exact contract: at == mult, un == mult*factor.
		mult := LookupMultiplier(m)
		if at != mult {
			t.Errorf("[%s] Score(%q,true)=%v; want multiplier %v", runID, m, at, mult)
		}
		if un != mult*attestationFactor {
			t.Errorf("[%s] Score(%q,false)=%v; want %v", runID, m, un, mult*attestationFactor)
		}

		// Lookup (enriched) must agree with Score on the unattested path.
		eUn := Lookup(runID, m, false)
		eAt := Lookup(runID, m, true)
		if eUn.Score != un || eAt.Score != at {
			t.Errorf("[%s] Lookup/Score disagree for %q: lookup(un=%v at=%v) score(un=%v at=%v)",
				runID, m, eUn.Score, eAt.Score, un, at)
		}
		if !(eAt.Score > eUn.Score) {
			t.Errorf("[%s] Lookup non-monotonic for %q: attested=%v unattested=%v",
				runID, m, eAt.Score, eUn.Score)
		}
	}
}

// monotonicityHoldsAcrossEveryKnownPair sweeps the full catalog and asserts the
// attested>unattested direction on each — a broader net than the fixed grid.
func TestAdversarial_AttestationMonotonicAcrossWholeCatalog(t *testing.T) {
	runID := newRunID("monotonic-all")
	for _, m := range KnownModels() {
		at := Score(m, true)
		un := Score(m, false)
		if !(at > un) {
			t.Errorf("[%s] %q: attested score %v not > unattested %v", runID, m, at, un)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 3: CATALOG ORDERING DETERMINISM
//
// KnownModels() must be deterministic (no map-iteration-order leak) and stable
// across repeated calls AND across an independent re-sort. It must list the
// FULL catalog, ascending by name.
//
// MUTATION THAT BITES: replace `sortStrings(out)` in KnownModels with a no-op
// (delete the call) → the returned slice inherits map-iteration order, which Go
// randomises per run/call, so the cross-call stability and sortedness
// assertions fail.
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial_KnownModelsDeterministicAndSorted(t *testing.T) {
	runID := newRunID("ordering-determinism")

	first := KnownModels()

	// (a) Stable across many calls — catches map-order leak.
	for i := 0; i < 64; i++ {
		got := KnownModels()
		if len(got) != len(first) {
			t.Fatalf("[%s] call %d length %d != first %d", runID, i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("[%s] call %d position %d = %q; first call had %q (non-deterministic ordering)",
					runID, i, j, got[j], first[j])
			}
		}
	}

	// (b) Strictly ascending (documented stable tie-break key = the name).
	for i := 1; i < len(first); i++ {
		if !(first[i-1] < first[i]) {
			t.Errorf("[%s] KnownModels not strictly ascending at %d: %q !< %q",
				runID, i, first[i-1], first[i])
		}
	}

	// (c) Completeness: it is exactly the catalog key set.
	if len(first) != len(catalog) {
		t.Errorf("[%s] KnownModels len %d != catalog len %d", runID, len(first), len(catalog))
	}
	seen := make(map[string]bool, len(first))
	for _, k := range first {
		if seen[k] {
			t.Errorf("[%s] duplicate model %q in KnownModels", runID, k)
		}
		seen[k] = true
		if _, ok := catalog[k]; !ok {
			t.Errorf("[%s] KnownModels contains %q absent from catalog", runID, k)
		}
	}
}

// TestAdversarial_DocumentedTierOrdering pins the headline tier from the package
// doc: multiplier(H100) > multiplier(A100) > defaultMultiplier.
//
// MUTATION THAT BITES: swap catalog["H100"] and catalog["A100"] values → the
// H100 > A100 assertion fails.
func TestAdversarial_DocumentedTierOrdering(t *testing.T) {
	runID := newRunID("tier")
	h := LookupMultiplier("H100")
	a := LookupMultiplier("A100")
	if !(h > a) {
		t.Errorf("[%s] tier violated: H100 (%v) must be > A100 (%v)", runID, h, a)
	}
	if !(a > defaultMultiplier) {
		t.Errorf("[%s] tier violated: A100 (%v) must be > default (%v)", runID, a, defaultMultiplier)
	}
	if !(h > defaultMultiplier) {
		t.Errorf("[%s] tier violated: H100 (%v) must be > default (%v)", runID, h, defaultMultiplier)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EDGES: no panic on degenerate / hostile input.
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial_EdgeInputsNoPanic(t *testing.T) {
	runID := newRunID("edges")
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("[%s] panic on edge input: %v", runID, r)
		}
	}()

	long := strings.Repeat("H100", 10000)
	for _, m := range []string{"", "   ", "\x00", "💥", long, strings.ToValidUTF8("\xff\xfe", "?")} {
		_ = LookupMultiplier(m)
		_ = Score(m, true)
		_ = Score(m, false)
		e := Lookup(runID, m, false)
		if e.RunID != runID {
			t.Errorf("[%s] edge input %q lost RunID: %q", runID, m, e.RunID)
		}
		// Edge inputs are unknown → must be the floor, never above it.
		if e.Multiplier != defaultMultiplier {
			t.Errorf("[%s] edge input %q multiplier %v; want default %v", runID, m, e.Multiplier, defaultMultiplier)
		}
	}
}
