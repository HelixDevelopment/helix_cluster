// Package latencysched implements a deterministic, latency-aware scheduler for
// inference request routing.
//
// Routing policy:
//   - If ANY available LOCAL provider exists, prefer LOCAL (lowest cross-host
//     latency, no network hop). Ties between locals are broken stably by ID.
//   - Otherwise, choose the available REMOTE provider with the LOWEST reported
//     LatencyMs. Ties between remotes are broken stably by ID.
//
// The scheduler is pure Go and fully deterministic: identical inputs always
// yield identical decisions, with no reliance on map iteration order, wall
// clock, or randomness in its selection logic.
package latencysched

import (
	"errors"
	"sort"
)

// Locality classifies where a provider runs relative to the requesting node.
type Locality int

const (
	// Local indicates the provider is co-located (no remote network hop).
	Local Locality = iota
	// Remote indicates the provider is reachable only across the network.
	Remote
)

func (l Locality) String() string {
	switch l {
	case Local:
		return "local"
	case Remote:
		return "remote"
	default:
		return "unknown"
	}
}

// Provider describes a candidate inference backend.
type Provider struct {
	// ID is a stable, unique identifier used for deterministic tie-breaking.
	ID string
	// Locality is Local or Remote.
	Locality Locality
	// LatencyMs is the most recently measured/reported round-trip latency in
	// milliseconds for this provider. Must be >= 0.
	LatencyMs int
	// Available reports whether the provider can currently accept work.
	Available bool
}

// Decision is the outcome of a Select call.
type Decision struct {
	// ChosenID is the ID of the selected provider.
	ChosenID string
	// Locality is the locality of the selected provider.
	Locality Locality
	// LatencyMs is the recorded latency of the chosen provider, copied from the
	// selected Provider's reported LatencyMs.
	LatencyMs int
}

// ErrNoProvider is returned when no available provider can be selected.
var ErrNoProvider = errors.New("latencysched: no available provider")

// Scheduler selects providers according to the latency-aware policy and records
// the latency of the most recent decision.
type Scheduler struct {
	last    Decision
	hasLast bool
}

// New constructs a Scheduler.
func New() *Scheduler { return &Scheduler{} }

// Select applies the routing policy to providers and returns the Decision.
//
// It prefers any available LOCAL provider (lowest-latency local on ties, then
// stable by ID). If no local is available, it returns the available REMOTE with
// the minimum LatencyMs (stable by ID on ties). If nothing is available, it
// returns ErrNoProvider.
//
// The chosen provider's reported LatencyMs is recorded and returned in the
// Decision, and retained as the scheduler's Last decision.
func (s *Scheduler) Select(providers []Provider) (Decision, error) {
	// Partition into available locals and remotes. We copy into local slices so
	// the caller's ordering cannot leak nondeterminism into the result; we sort
	// our own copies explicitly.
	var locals, remotes []Provider
	for _, p := range providers {
		if !p.Available {
			continue
		}
		switch p.Locality {
		case Local:
			locals = append(locals, p)
		case Remote:
			remotes = append(remotes, p)
		}
	}

	// Local-preference branch: any available local wins over every remote,
	// regardless of remote latency.
	if len(locals) > 0 {
		best := minByLatencyThenID(locals)
		return s.record(best), nil
	}

	if len(remotes) > 0 {
		best := minByLatencyThenID(remotes)
		return s.record(best), nil
	}

	return Decision{}, ErrNoProvider
}

// minByLatencyThenID returns the provider with the smallest LatencyMs, breaking
// ties deterministically by ascending ID. The input slice is sorted in place on
// a private copy held by the caller, so mutation is acceptable.
func minByLatencyThenID(ps []Provider) Provider {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].LatencyMs != ps[j].LatencyMs {
			return ps[i].LatencyMs < ps[j].LatencyMs
		}
		return ps[i].ID < ps[j].ID
	})
	return ps[0]
}

func (s *Scheduler) record(p Provider) Decision {
	d := Decision{
		ChosenID:  p.ID,
		Locality:  p.Locality,
		LatencyMs: p.LatencyMs,
	}
	s.last = d
	s.hasLast = true
	return d
}

// Last returns the most recent Decision and true, or a zero Decision and false
// if Select has not yet produced a successful decision.
func (s *Scheduler) Last() (Decision, bool) {
	return s.last, s.hasLast
}
