// Package gpucatalog provides a supported-GPU compute-multiplier catalog,
// a LookupMultiplier function, and an attestation-aware Score function.
//
// Catalog ordering guarantee (mutation guard — see TestCatalogOrdering):
//
//	multiplier(H100) > multiplier(A100) > defaultMultiplier
//
// Swapping the H100 and A100 entries in the catalog map will cause
// TestCatalogOrdering to fail immediately.
package gpucatalog

import (
	"fmt"
	"strings"
	"time"
)

// defaultMultiplier is returned for any model not present in the catalog.
// GUARD: TestCatalogOrdering pins the invariant
//
//	catalogH100 > catalogA100 > defaultMultiplier.
//
// Mutating this value upward past catalogA100 kills that test.
const defaultMultiplier float64 = 1.0

// attestationFactor is the penalty applied to un-attested capacity.
// It MUST be strictly less than 1.0 so that Score(m,true) > Score(m,false).
// GUARD: TestAttestationScoring pins this invariant.
const attestationFactor float64 = 0.80

// catalog maps canonical GPU model names (upper-cased at lookup time) to
// their compute multipliers.
//
// *** MUTATION GUARD ***
// The values for "H100" and "A100" are load-bearing.  Swapping them
// (i.e. setting "H100" = 3.2 and "A100" = 5.0) makes TestCatalogOrdering
// FAIL because the assertion
//
//	h100 > a100 > defaultMultiplier
//
// will no longer hold.
var catalog = map[string]float64{
	"H100": 5.0, // fastest — MUST be strictly > A100 value (guard: TestCatalogOrdering)
	"A100": 3.2, // mid-tier — MUST be strictly > defaultMultiplier (guard: TestCatalogOrdering)
	"V100": 2.1,
	"T4":   1.6,
	"A10G": 1.8,
	"L4":   1.9,
	"L40S": 2.5,
	"A30":  2.0,
}

// CatalogEntry is a single record emitted by the catalog for observability.
// RunID ties every emitted record to one logical query session so tests can
// assert on the RunID they injected.
type CatalogEntry struct {
	RunID      string    // caller-supplied correlation identifier
	Model      string    // canonical (normalised) model name
	Multiplier float64   // value from catalog, or defaultMultiplier
	Known      bool      // true iff the model was found in the catalog
	Attested   bool      // true if this entry represents an attested device
	Score      float64   // effective score after attestation factor
	QueriedAt  time.Time // wall-clock time of the lookup
}

// String renders a CatalogEntry as a single log line.
func (e CatalogEntry) String() string {
	status := "unattested"
	if e.Attested {
		status = "attested"
	}
	known := "unknown"
	if e.Known {
		known = "known"
	}
	return fmt.Sprintf(
		"run=%s model=%s multiplier=%.4f score=%.4f known=%s status=%s ts=%s",
		e.RunID, e.Model, e.Multiplier, e.Score, known, status,
		e.QueriedAt.UTC().Format(time.RFC3339Nano),
	)
}

// normalise upper-cases and trims whitespace so callers may pass "h100",
// "H100 ", or "H100" and receive the same answer.
func normalise(model string) string {
	return strings.TrimSpace(strings.ToUpper(model))
}

// LookupMultiplier returns the compute multiplier for the given GPU model.
// The model name is normalised before lookup.  If the model is not in the
// catalog, defaultMultiplier is returned.
func LookupMultiplier(model string) float64 {
	if v, ok := catalog[normalise(model)]; ok {
		return v
	}
	return defaultMultiplier
}

// Score returns the effective score for the given GPU model accounting for
// attestation status.
//
//   - attested == true  → full catalog multiplier
//   - attested == false → multiplier * attestationFactor  (strictly lower)
//
// This guarantees Score(model, true) > Score(model, false) for any model
// whose multiplier > 0, because attestationFactor < 1.0.
func Score(model string, attested bool) float64 {
	m := LookupMultiplier(model)
	if attested {
		return m
	}
	return m * attestationFactor
}

// Lookup performs a full enriched lookup that returns a CatalogEntry
// containing the RunID, multiplier, score, and wall-clock timestamp.
// Tests MUST assert on the RunID they pass in order to prove sink-side
// observability rather than a structurally-guaranteed predicate.
func Lookup(runID, model string, attested bool) CatalogEntry {
	n := normalise(model)
	mult, known := func() (float64, bool) {
		if v, ok := catalog[n]; ok {
			return v, true
		}
		return defaultMultiplier, false
	}()

	score := mult
	if !attested {
		score = mult * attestationFactor
	}

	return CatalogEntry{
		RunID:      runID,
		Model:      n,
		Multiplier: mult,
		Known:      known,
		Attested:   attested,
		Score:      score,
		QueriedAt:  time.Now().UTC(),
	}
}

// KnownModels returns a sorted slice of all canonical model names in the
// catalog.  Useful for operators querying the catalog at runtime.
func KnownModels() []string {
	out := make([]string, 0, len(catalog))
	for k := range catalog {
		out = append(out, k)
	}
	// deterministic ordering
	sortStrings(out)
	return out
}

// sortStrings is an insertion-sort over a small slice; avoids importing
// "sort" for a single trivial use.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}
