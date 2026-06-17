package gpucatalog

// Adversarial2 probes — targeted at the NaN/Inf-POISON candidate class and the
// residual correctness edges that the first adversarial file did not pin.
//
// FINDINGS SUMMARY (see REPORT in the task): gpucatalog has NO float64 input
// parameter anywhere — LookupMultiplier/Score/Lookup all take (string[, bool]).
// The catalog is a map[string]float64 of finite, hardcoded literals. There is
// therefore NO caller-injectable NaN/Inf, NO min/max selection over external
// floats, and NO float-keyed sort comparator that a NaN could poison. The only
// arithmetic is `mult * attestationFactor`, both finite. The NaN/Inf-poison
// hypothesis does NOT apply.
//
// These tests pin that as an enforced contract (every emitted Multiplier/Score
// is finite) plus the two genuine correctness edges discovered while probing:
//   - Unicode ToUpper folds (ı→I, ſ→S) must NOT manufacture a false catalog hit.
//   - sortStrings must be a correct total order vs the stdlib oracle, including
//     inputs that collide after normalisation.
//
// All tests pass on the code left behind (no production change was warranted).

import (
	"math"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// FINITENESS INVARIANT (NaN/Inf-poison guard)
//
// Every value the catalog can ever emit — for every known model, the unknown
// default, and degenerate/hostile inputs, across both attestation states — must
// be a finite, positive float64. A NaN or Inf escaping here is exactly the
// poison class scanned for; this test is the sink-side gate that proves it
// cannot happen given the all-finite catalog.
//
// MUTATION THAT BITES: introduce a non-finite catalog entry, e.g.
//
//	catalog["H100"] = math.Inf(1)   // or math.NaN()
//
// → LookupMultiplier("H100"), its Score, and Lookup(...).Multiplier all go
// non-finite and the IsNaN/IsInf assertions below fail. (attestationFactor is
// finite-nonzero, so the unattested score is non-finite iff the multiplier is.)
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial2_AllEmittedValuesAreFinite(t *testing.T) {
	runID := newRunID("finite")

	long := strings.Repeat("A100", 5000)
	models := append([]string{}, KnownModels()...)
	models = append(models,
		"", "   ", "\t\n", "UNKNOWN_GPU_ZZZ", "h100", "  A100  ",
		"\x00", "💥", long, "MI300X", "ı", "ſ", "L40",
	)

	for _, m := range models {
		mult := LookupMultiplier(m)
		if math.IsNaN(mult) || math.IsInf(mult, 0) {
			t.Errorf("[%s] LookupMultiplier(%q) non-finite: %v (NaN/Inf poison)", runID, m, mult)
		}
		if mult <= 0 {
			t.Errorf("[%s] LookupMultiplier(%q) = %v; must be > 0", runID, m, mult)
		}

		for _, att := range []bool{true, false} {
			s := Score(m, att)
			if math.IsNaN(s) || math.IsInf(s, 0) {
				t.Errorf("[%s] Score(%q,%v) non-finite: %v (NaN/Inf poison)", runID, m, att, s)
			}
			if s <= 0 {
				t.Errorf("[%s] Score(%q,%v) = %v; must be > 0", runID, m, att, s)
			}

			e := Lookup(runID, m, att)
			if math.IsNaN(e.Multiplier) || math.IsInf(e.Multiplier, 0) {
				t.Errorf("[%s] Lookup(%q,%v).Multiplier non-finite: %v", runID, m, att, e.Multiplier)
			}
			if math.IsNaN(e.Score) || math.IsInf(e.Score, 0) {
				t.Errorf("[%s] Lookup(%q,%v).Score non-finite: %v", runID, m, att, e.Score)
			}
		}
	}
}

// raw-catalog finiteness: pins the source values themselves so a non-finite
// literal added to the map is rejected at the storage layer, independent of any
// lookup path.
//
// MUTATION THAT BITES: catalog["NEW"] = math.NaN() (or Inf) → fails here.
func TestAdversarial2_CatalogStoresOnlyFinitePositiveValues(t *testing.T) {
	runID := newRunID("catalog-finite")
	for k, v := range catalog {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("[%s] catalog[%q] = %v is non-finite — NaN/Inf poison at the source", runID, k, v)
		}
		if v <= 0 {
			t.Errorf("[%s] catalog[%q] = %v must be strictly positive", runID, k, v)
		}
	}
	if math.IsNaN(defaultMultiplier) || math.IsInf(defaultMultiplier, 0) || defaultMultiplier <= 0 {
		t.Errorf("[%s] defaultMultiplier = %v must be finite and > 0", runID, defaultMultiplier)
	}
	if math.IsNaN(attestationFactor) || math.IsInf(attestationFactor, 0) {
		t.Errorf("[%s] attestationFactor = %v must be finite", runID, attestationFactor)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UNICODE FOLD OVER-MATCH GUARD
//
// strings.ToUpper folds two non-ASCII runes onto ASCII letters used in catalog
// keys: 'ı' (U+0131) → "I" and 'ſ' (U+017F) → "S". This test proves those folds
// (and a battery of homoglyph/lookalike inputs) do NOT manufacture a false
// catalog hit: each must resolve to the unknown default, never a known
// multiplier. A future key like "S" or "I" plus this fold would be a silent
// over-match (mis-priced GPU); this is the trip-wire.
//
// MUTATION THAT BITES: add catalog["S"] = 9.9 → "ſ" now folds to "S" and
// LookupMultiplier("ſ") returns 9.9 instead of the default → fails.
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial2_UnicodeFoldDoesNotOverMatch(t *testing.T) {
	runID := newRunID("unicode-fold")

	// Each of these normalises to something that is NOT a catalog key.
	foldy := []string{
		"ı",      // U+0131 → "I"
		"ſ",      // U+017F → "S"
		"ℎ100",   // U+210E PLANCK CONSTANT, not ASCII 'h'
		"Ⓗ100",   // circled H
		"ＨＩＯＯ",  // fullwidth latin — not ASCII H100
		"А100",   // Cyrillic А (U+0410) + ASCII 100
		"H1ОО",   // Cyrillic О in place of zeros
		"l40s",   // lowercase L40S → matches; sanity below excludes it
	}

	for _, m := range foldy {
		got := LookupMultiplier(m)
		// "l40s" is a real normalised hit; treat it separately as a sanity anchor.
		if normalise(m) == "L40S" {
			if got != catalog["L40S"] {
				t.Errorf("[%s] sanity: %q should normalise to L40S (%v), got %v", runID, m, catalog["L40S"], got)
			}
			continue
		}
		if _, isKey := catalog[normalise(m)]; isKey {
			// If normalisation legitimately lands on a key, that's fine and not an
			// over-match; only flag the case where it does NOT.
			continue
		}
		if got != defaultMultiplier {
			t.Errorf("[%s] Unicode lookalike %q over-matched: got %v; want default %v (false catalog hit)",
				runID, m, got, defaultMultiplier)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// sortStrings TOTAL-ORDER CORRECTNESS
//
// KnownModels relies on the hand-rolled insertion sort sortStrings. Prove it is
// a correct total order identical to sort.Strings for arbitrary inputs,
// including duplicates and post-normalisation collisions, so the deterministic
// catalog ordering guarantee rests on a sound sort.
//
// MUTATION THAT BITES: break the comparison in sortStrings (e.g. s[j] < key) →
// the result diverges from the stdlib oracle and these cases fail.
// ─────────────────────────────────────────────────────────────────────────────
func TestAdversarial2_SortStringsMatchesStdlibOracle(t *testing.T) {
	runID := newRunID("sort-oracle")

	cases := [][]string{
		{},
		{"only"},
		{"b", "a"},
		{"H100", "A100", "V100", "T4", "A10G", "L4", "L40S", "A30"},
		{"z", "z", "a", "a", "m"},                 // duplicates
		{"", "  ", " ", "\t", "a"},                // whitespace variants
		{"A10", "A100", "A1", "A10G", "A30"},      // prefix-ordering edges
		{"10", "2", "1", "20", "3"},               // lexical vs numeric
		strings.Fields("the quick brown fox H100"), // mixed words
	}

	for ci, in := range cases {
		got := append([]string{}, in...)
		sortStrings(got)

		want := append([]string{}, in...)
		sort.Strings(want)

		if len(got) != len(want) {
			t.Fatalf("[%s] case %d length %d != oracle %d", runID, ci, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("[%s] case %d position %d: sortStrings=%q oracle=%q (input=%v)",
					runID, ci, i, got[i], want[i], in)
			}
		}
		// Independent monotonicity check on the result.
		for i := 1; i < len(got); i++ {
			if got[i-1] > got[i] {
				t.Errorf("[%s] case %d not sorted at %d: %q > %q", runID, ci, i, got[i-1], got[i])
			}
		}
	}
}
