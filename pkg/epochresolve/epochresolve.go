// Package epochresolve implements config-epoch conflict resolution for
// simultaneous failovers.  When multiple Claims arrive for the same Slot
// (e.g. two controllers both attempt to own a partition during a network
// partition), Resolve picks one winner per Slot using a deterministic,
// order-independent rule:
//
//  1. The Claim with the HIGHEST Epoch wins.
//  2. On equal Epoch, the Claim whose Owner is lexicographically SMALLER wins.
//
// The tiebreak in rule 2 is documented so that all observers converge to the
// same result regardless of the order in which they receive Claims.
//
// Every resolution is recorded in a ResolutionRecord that carries the RunID
// supplied by the caller, enabling downstream audit and replay.
package epochresolve

import (
	"errors"
	"sort"
)

// Claim represents a single ownership assertion for a configuration slot.
type Claim struct {
	Slot  string
	Owner string
	Epoch uint64
}

// ResolutionRecord is the output produced by Resolve: one entry per Slot that
// appeared in the input, with the winning Claim and the RunID of the
// resolution run.
type ResolutionRecord struct {
	RunID  string
	Slot   string
	Winner Claim
}

// ErrNoClaims is returned when the claims slice is empty.
var ErrNoClaims = errors.New("epochresolve: no claims provided")

// beats reports whether challenger should replace current as the slot winner.
//
// MUTATION GUARD — epoch comparison.
// Inverting the first branch (challenger.Epoch < current.Epoch) causes
// TestHigherEpochWins to FAIL because a lower-epoch claim would be chosen
// instead of the higher-epoch one.
func beats(challenger, current Claim) bool {
	if challenger.Epoch > current.Epoch { // ← mutation-killable guard (TestHigherEpochWins)
		return true
	}
	if challenger.Epoch < current.Epoch {
		return false
	}
	// Equal epochs: lexicographically smaller Owner wins (tiebreak rule 2).
	return challenger.Owner < current.Owner
}

// Resolve returns one ResolutionRecord per distinct Slot found in claims.
// The result slice is sorted by Slot so callers receive a stable ordering.
//
// runID is embedded in every ResolutionRecord and MUST be non-empty; the
// caller is responsible for generating a unique value per resolution run
// (e.g. a UUID or a monotonic counter rendered as a string).
//
// Returns ErrNoClaims if claims is nil or empty.
func Resolve(runID string, claims []Claim) ([]ResolutionRecord, error) {
	if len(claims) == 0 {
		return nil, ErrNoClaims
	}

	winners := make(map[string]Claim)

	for _, c := range claims {
		current, exists := winners[c.Slot]
		if !exists || beats(c, current) {
			winners[c.Slot] = c
		}
	}

	// Collect and sort by Slot for deterministic output ordering.
	records := make([]ResolutionRecord, 0, len(winners))
	for slot, winner := range winners {
		records = append(records, ResolutionRecord{
			RunID:  runID,
			Slot:   slot,
			Winner: winner,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Slot < records[j].Slot
	})

	return records, nil
}
