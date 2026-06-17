package backfill

import (
	"math"
	"sort"
)

// addClamp returns a+b, saturating at math.MaxInt instead of wrapping to a
// negative value on overflow. Window math here (end = start + duration) feeds
// both the occupancy-fit comparison and the delta-map keys; an unguarded wrap
// produces a negative "end" that (a) makes fits() skip every interior boundary
// (b >= end is trivially true for a negative end) and silently ADMIT a window
// that overlaps a fully-booked region, and (b) writes a delta at a negative key
// that sorts before t=0, driving occupancy negative across the whole timeline.
// Clamping keeps end monotonic (>= start) so neither corruption can occur; for
// in-envelope inputs (small abstract time units) it is a no-op.
func addClamp(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}

// timeline tracks committed resource usage over discrete time. It is the
// authoritative occupancy map used both to place reservations and to test
// candidate backfill windows. Usage is represented as a piecewise-constant
// step function keyed by the instants at which usage changes.
//
// Invariant: at no instant does used(t) exceed capacity. EarliestStart only
// ever returns windows that preserve this invariant, and reserve panics if a
// caller tries to violate it (programmer error, not a runtime condition).
type timeline struct {
	capacity int
	// delta[t] is the change in occupancy applied at instant t: +w when a job
	// starts at t, -w when one ends at t. The running prefix sum over sorted
	// keys yields used(t).
	delta map[int]int
}

func newTimeline(capacity int) *timeline {
	return &timeline{capacity: capacity, delta: map[int]int{}}
}

// boundaries returns the sorted instants at which occupancy can change.
func (tl *timeline) boundaries() []int {
	ts := make([]int, 0, len(tl.delta))
	for t := range tl.delta {
		ts = append(ts, t)
	}
	sort.Ints(ts)
	return ts
}

// usedAt returns the occupancy in force during the half-open instant t, i.e.
// the prefix sum of all deltas at keys <= t. This is the number of units busy
// across [t, next-boundary).
func (tl *timeline) usedAt(t int) int {
	used := 0
	for _, b := range tl.boundaries() {
		if b > t {
			break
		}
		used += tl.delta[b]
	}
	return used
}

// fits reports whether width units are free across the whole half-open
// interval [start, start+duration). It checks occupancy at start and at every
// boundary that falls strictly inside the interval — those are the only points
// where occupancy can rise.
func (tl *timeline) fits(start, duration, width int) bool {
	end := addClamp(start, duration)
	// Occupancy entering the window.
	if tl.usedAt(start)+width > tl.capacity {
		return false
	}
	// Any change-point inside (start, end) could push usage over.
	for _, b := range tl.boundaries() {
		if b <= start {
			continue
		}
		if b >= end {
			break
		}
		if tl.usedAt(b)+width > tl.capacity {
			return false
		}
	}
	return true
}

// earliestStart returns the smallest start time >= notBefore at which width
// units are free continuously for duration. Candidate starts are notBefore
// plus every existing boundary at or after it: occupancy only ever drops at a
// boundary, so a window that does not fit at one candidate can only start
// fitting at a later boundary.
func (tl *timeline) earliestStart(notBefore, duration, width int) int {
	candidates := []int{notBefore}
	for _, b := range tl.boundaries() {
		if b > notBefore {
			candidates = append(candidates, b)
		}
	}
	sort.Ints(candidates)
	for _, c := range candidates {
		if tl.fits(c, duration, width) {
			return c
		}
	}
	// With finite committed work, capacity is fully free after the last
	// boundary, so a fit is guaranteed at or before that point; this return
	// is a defensive fallback.
	bs := tl.boundaries()
	if len(bs) == 0 {
		return notBefore
	}
	last := bs[len(bs)-1]
	if last > notBefore {
		return last
	}
	return notBefore
}

// reserve commits width units across [start, start+duration). It panics if the
// commitment would exceed capacity at any instant — callers must validate with
// fits first. Panic here signals a scheduler bug, never bad external input.
func (tl *timeline) reserve(start, duration, width int) {
	if !tl.fits(start, duration, width) {
		panic("backfill: reserve would exceed capacity invariant")
	}
	end := addClamp(start, duration)
	tl.delta[start] += width
	tl.delta[end] -= width
	// Drop keys that net to zero to keep the boundary set tight.
	if tl.delta[start] == 0 {
		delete(tl.delta, start)
	}
	if tl.delta[end] == 0 {
		delete(tl.delta, end)
	}
}

// peakUsage returns the maximum occupancy reached at any boundary. Used by
// tests to assert the capacity invariant globally.
func (tl *timeline) peakUsage() int {
	peak := 0
	used := 0
	for _, b := range tl.boundaries() {
		used += tl.delta[b]
		if used > peak {
			peak = used
		}
	}
	return peak
}
