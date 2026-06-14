// Package ewmarank provides utilization-aware EWMA candidate ranking with
// sticky-client routing. It is pure Go, deterministic, and self-contained
// (own EWMA implementation; it does NOT import pkg/stats or any scheduler pkg).
//
// Model: each backend candidate carries a utilization signal updated over time
// via an exponentially weighted moving average (EWMA):
//
//	ewma = alpha*sample + (1-alpha)*ewma
//
// Rank orders candidates by ASCENDING EWMA utilization (least-loaded first),
// with a deterministic tie-break by backend ID. A client is sticky: RouteSticky
// returns the same backend for a client across calls UNTIL that backend FAILS
// (is marked failed), after which the client is re-routed to the next-best by
// rank.
package ewmarank

import (
	"math"
	"sort"
)

// DefaultAlpha is used when a Ranker is constructed with a non-positive or
// out-of-range alpha. It biases moderately toward recent samples while still
// smoothing.
const DefaultAlpha = 0.3

// backend holds the EWMA state for a single candidate.
type backend struct {
	id string
	// ewma is the current smoothed utilization estimate in [0, +inf).
	ewma float64
	// seeded reports whether at least one sample has been observed. The first
	// sample initializes the EWMA directly (there is no prior to smooth toward).
	seeded bool
	// failed marks a backend as unavailable for sticky routing.
	failed bool
}

// Ranker ranks backend candidates by ascending EWMA utilization and provides
// sticky per-client routing. A Ranker is not safe for concurrent use; callers
// that need concurrency must synchronize externally.
type Ranker struct {
	alpha    float64
	order    []string            // stable insertion order of backend IDs
	backends map[string]*backend // id -> state
	sticky   map[string]string   // clientID -> currently routed backendID
}

// New constructs a Ranker with the given smoothing factor alpha. If alpha is
// not in (0, 1], DefaultAlpha is used instead.
func New(alpha float64) *Ranker {
	if alpha <= 0 || alpha > 1 {
		alpha = DefaultAlpha
	}
	return &Ranker{
		alpha:    alpha,
		backends: make(map[string]*backend),
		sticky:   make(map[string]string),
	}
}

// Alpha returns the smoothing factor in effect.
func (r *Ranker) Alpha() float64 { return r.alpha }

// ensure returns the backend state for id, creating it on first reference.
func (r *Ranker) ensure(id string) *backend {
	b, ok := r.backends[id]
	if !ok {
		b = &backend{id: id}
		r.backends[id] = b
		r.order = append(r.order, id)
	}
	return b
}

// Observe folds a new utilization sample into the EWMA for backendID.
//
// The first sample for a backend initializes its EWMA exactly to that sample
// (no prior exists). Every subsequent sample is smoothed:
//
//	ewma = alpha*sample + (1-alpha)*ewma
//
// This is a partial move toward the sample, never a replace (for alpha < 1).
func (r *Ranker) Observe(backendID string, sample float64) {
	// Drop NaN samples: a NaN would poison the EWMA state permanently (every
	// subsequent alpha*sample+(1-alpha)*ewma stays NaN), and NaN in Rank's
	// comparator breaks its strict-weak-ordering (NaN!=x is true but NaN<x and
	// x<NaN are both false), collapsing the documented least-loaded order to
	// insertion order — steering traffic to an arbitrary/worst backend. A NaN
	// utilization sample is meaningless, so we keep the prior EWMA unchanged.
	// (±Inf is intentionally NOT dropped: it is totally ordered and sorts
	// deterministically, preserving Rank's contract.)
	if math.IsNaN(sample) {
		return
	}
	b := r.ensure(backendID)
	if !b.seeded {
		b.ewma = sample
		b.seeded = true
		return
	}
	b.ewma = r.alpha*sample + (1-r.alpha)*b.ewma
}

// EWMA returns the current smoothed utilization for backendID and whether the
// backend has been observed at least once.
func (r *Ranker) EWMA(backendID string) (float64, bool) {
	b, ok := r.backends[backendID]
	if !ok || !b.seeded {
		return 0, false
	}
	return b.ewma, true
}

// Rank returns all known backend IDs ordered by ASCENDING EWMA utilization
// (least-loaded first). Ties on EWMA are broken deterministically by ascending
// backend ID. Unseeded backends (no sample yet) sort after all seeded ones,
// then by ID, so they are routed to last until measured.
func (r *Ranker) Rank() []string {
	ids := make([]string, len(r.order))
	copy(ids, r.order)
	sort.SliceStable(ids, func(i, j int) bool {
		bi := r.backends[ids[i]]
		bj := r.backends[ids[j]]
		// Seeded backends rank ahead of unseeded ones.
		if bi.seeded != bj.seeded {
			return bi.seeded
		}
		if bi.seeded && bj.seeded && bi.ewma != bj.ewma {
			return bi.ewma < bj.ewma
		}
		// Deterministic tie-break by ID.
		return ids[i] < ids[j]
	})
	return ids
}

// rankAvailable returns the ranked IDs that are not marked failed.
func (r *Ranker) rankAvailable() []string {
	ranked := r.Rank()
	out := ranked[:0:0]
	for _, id := range ranked {
		if !r.backends[id].failed {
			out = append(out, id)
		}
	}
	return out
}

// RouteSticky returns the backend assigned to clientID. The assignment is
// sticky: the same client receives the same backend on repeated calls, even if
// ranking shifts, as long as that backend remains available. If the client has
// no assignment yet, or its current assignment has been marked failed, the
// client is (re)assigned to the best available backend by Rank. It returns the
// empty string when no available backend exists.
func (r *Ranker) RouteSticky(clientID string) string {
	if cur, ok := r.sticky[clientID]; ok {
		if b, exists := r.backends[cur]; exists && !b.failed {
			return cur // honor the sticky assignment
		}
		// Current assignment is gone or failed: drop it and re-route below.
		delete(r.sticky, clientID)
	}
	avail := r.rankAvailable()
	if len(avail) == 0 {
		return ""
	}
	best := avail[0]
	r.sticky[clientID] = best
	return best
}

// MarkFailed marks backendID as failed, removing it from future sticky routing.
// Any client currently pinned to it will be re-routed on its next RouteSticky
// call. Marking an unknown backend records the failure so a later Observe keeps
// it excluded.
func (r *Ranker) MarkFailed(backendID string) {
	b := r.ensure(backendID)
	b.failed = true
}

// Recover clears the failed flag for backendID, making it eligible for routing
// again on subsequent calls. It does not retroactively re-pin clients.
func (r *Ranker) Recover(backendID string) {
	if b, ok := r.backends[backendID]; ok {
		b.failed = false
	}
}

// Failed reports whether backendID is currently marked failed.
func (r *Ranker) Failed(backendID string) bool {
	if b, ok := r.backends[backendID]; ok {
		return b.failed
	}
	return false
}
