// Package rescorer implements a periodic provider re-scoring loop with a
// price-spike guard. It is pure-Go and deterministic: all time is supplied by
// an injected logical clock and advanced via Tick. No wall-clock time, network,
// or host access is used.
//
// # Model
//
// Each provider carries a price. A score is derived from that price (lower
// price => better score). A re-scoring CYCLE happens on a cycle boundary
// (every CycleTicks logical ticks). On a cycle boundary the ReScorer recomputes
// every provider's score from its CURRENT price and re-ranks (best first).
//
// Between cycle boundaries the published Ranking is STABLE: SetPrice updates the
// stored price but does NOT change the ranking until the next boundary. This is
// what lets traffic stay pinned to a chosen provider within a cycle.
//
// A PRICE SPIKE on the current top provider (a large upward jump in price)
// applied between cycles therefore takes effect on the NEXT cycle: the spiked
// provider's score worsens and it drops in the ranking, moving traffic off it.
//
// A ReScorer is safe for concurrent use: prices/scores may be updated
// (SetPrice, Tick) from many request goroutines while selection reads the
// ranking (Ranking, Top) concurrently. An internal RWMutex serializes the
// writers and lets readers run in parallel.
//
// A price-spike GUARD reacts faster than a full cycle: if an abrupt jump above a
// configured factor is detected for the current top provider, the guard forces
// an immediate re-rank on the very next Tick (within one cycle) so the spiked
// provider is demoted without waiting for the cycle boundary.
package rescorer

import (
	"sort"
	"sync"
)

// provider holds the mutable per-provider state.
type provider struct {
	id    string
	price float64 // current (possibly just-updated) price
	// scored is the price that was in effect at the last re-rank. The published
	// ranking reflects scored prices, not the live price, so the ranking is
	// stable between boundaries.
	scored float64
}

// score derives a score from a price. Lower price => higher (better) score.
// The ranking sorts by score descending, so cheapest is top. Making the score
// depend on price is the whole point of clause 1: a mutation that returns a
// constant here (ignoring price) makes the ranking static and must fail tests.
func score(price float64) float64 {
	return -price
}

// ReScorer is a deterministic periodic re-scoring loop.
//
// Construct with zero value is not valid; set CycleTicks (>=1) and Clock.
// Use New for a configured instance.
type ReScorer struct {
	// CycleTicks is the number of logical ticks between cycle boundaries. A
	// re-rank happens when (now % CycleTicks) crosses a multiple of CycleTicks.
	CycleTicks int64

	// Clock is the last observed logical time (set by Tick). It is exported to
	// satisfy the requested API shape; callers normally drive it via Tick.
	Clock int64

	// SpikeFactor is the multiplicative threshold for the price-spike guard.
	// If the current top provider's price jumps to >= SpikeFactor * its scored
	// price, the guard triggers an immediate re-rank on the next Tick. A value
	// <= 1 disables the guard.
	SpikeFactor float64

	// mu guards all mutable state below. SetPrice/Tick are writers (they mutate
	// the providers map, order, ranking and clock/boundary bookkeeping);
	// Ranking/Top are readers. The scorer fills a routing/load-balancer role
	// where scores are updated from many request goroutines while selection
	// reads the ranking, so this lock makes that documented concurrent role
	// race-free.
	mu sync.RWMutex

	providers map[string]*provider
	order     []string // insertion order, for deterministic tie-breaking
	ranking   []string // published ranking, best first; updated only at re-rank

	lastBoundary int64 // logical time of the most recent re-rank boundary
	initialized  bool  // whether an initial ranking has been computed
	guardArmed   bool  // a spike was detected; force re-rank on next Tick
}

// New returns a ReScorer with the given cycle length and starting clock value.
// spikeFactor configures the guard (<=1 disables it).
func New(cycleTicks int64, startClock int64, spikeFactor float64) *ReScorer {
	if cycleTicks < 1 {
		cycleTicks = 1
	}
	return &ReScorer{
		CycleTicks:  cycleTicks,
		Clock:       startClock,
		SpikeFactor: spikeFactor,
		providers:   make(map[string]*provider),
	}
}

// SetPrice records a new price for providerID, creating the provider if needed.
// It updates the live price immediately but does NOT re-rank: the published
// ranking changes only at the next cycle boundary (or via the spike guard).
func (r *ReScorer) SetPrice(providerID string, price float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]*provider)
	}
	p, ok := r.providers[providerID]
	if !ok {
		p = &provider{id: providerID}
		r.providers[providerID] = p
		r.order = append(r.order, providerID)
		// New providers get their scored price seeded so they participate in the
		// next re-rank; they do not appear in the current published ranking until
		// then.
		p.scored = price
	}
	p.price = price

	// Spike guard: if this update is an abrupt jump on the CURRENT top provider,
	// arm an immediate re-rank for the next Tick.
	if r.SpikeFactor > 1 && r.initialized && len(r.ranking) > 0 && providerID == r.ranking[0] {
		base := p.scored
		if base > 0 && price >= base*r.SpikeFactor {
			r.guardArmed = true
		}
	}
}

// rerank recomputes scores from current live prices and rebuilds the published
// ranking (best first). Ties break by insertion order for determinism.
func (r *ReScorer) rerank() {
	for _, p := range r.providers {
		p.scored = p.price
	}
	ids := make([]string, len(r.order))
	copy(ids, r.order)
	// Precompute insertion index for stable tie-break.
	idx := make(map[string]int, len(r.order))
	for i, id := range r.order {
		idx[id] = i
	}
	sort.SliceStable(ids, func(a, b int) bool {
		sa := score(r.providers[ids[a]].scored)
		sb := score(r.providers[ids[b]].scored)
		if sa != sb {
			return sa > sb // higher score first => cheaper first
		}
		return idx[ids[a]] < idx[ids[b]]
	})
	r.ranking = ids
	r.initialized = true
	r.guardArmed = false
}

// Tick advances the logical clock to now and re-ranks if a cycle boundary has
// been crossed since the last re-rank, or if the spike guard is armed.
//
// The first Tick establishes the initial ranking. Subsequent re-ranks happen
// only at boundaries (now - lastBoundary >= CycleTicks) unless the guard fires.
func (r *ReScorer) Tick(now int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Clock = now

	if !r.initialized {
		r.lastBoundary = now
		r.rerank()
		return
	}

	if r.guardArmed {
		// Guard fires within the cycle: re-rank now and reset the cycle phase so
		// the next scheduled boundary is measured from here.
		r.lastBoundary = now
		r.rerank()
		return
	}

	if now-r.lastBoundary >= r.CycleTicks {
		// Snap the boundary to the most recent multiple of CycleTicks that has
		// been reached, so phase is preserved across large jumps.
		elapsed := now - r.lastBoundary
		steps := elapsed / r.CycleTicks
		r.lastBoundary += steps * r.CycleTicks
		r.rerank()
	}
}

// Ranking returns the current published ranking, best (cheapest) first. The
// returned slice is a copy and safe for the caller to retain.
func (r *ReScorer) Ranking() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.ranking))
	copy(out, r.ranking)
	return out
}

// Top returns the current best provider id, or "" if none.
func (r *ReScorer) Top() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.ranking) == 0 {
		return ""
	}
	return r.ranking[0]
}
