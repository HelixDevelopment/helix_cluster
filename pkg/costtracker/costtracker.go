// Package costtracker computes a deterministic monthly cost report for GPU
// allocations: blended cost-per-hour and the percentage reduction versus an
// AWS on-demand baseline rate.
//
// The model is intentionally simple and pure: over a simulated month a set of
// allocations accrue cost. Each allocation contributes (costPerHour * hours) to
// the total cost and `hours` to the total GPU-hours. The blended cost-per-hour
// is the hours-weighted average (total cost / total hours), NOT a naive average
// of the per-allocation rates. The reduction versus an AWS on-demand baseline is
// measured against baselineTotal = baselineRate * totalHours.
//
// Concurrency: Tracker is safe for concurrent use — Record and Report are
// serialized by an internal mutex. This is load-bearing for cost conservation:
// without it, concurrent Record calls race on the allocs slice (a data race)
// AND lose appends (a lost-update that silently destroys recorded charges, so
// the reported total under-counts the money actually spent).
package costtracker

import "sync"

// allocation is a single recorded cost-accruing GPU allocation over the month.
type allocation struct {
	costPerHour float64
	hours       float64
	spot        bool
}

// Tracker accumulates allocations for a simulated month and produces a Report.
// The zero value is a ready-to-use, empty Tracker.
type Tracker struct {
	mu     sync.Mutex
	allocs []allocation
}

// New returns an empty Tracker.
func New() *Tracker { return &Tracker{} }

// Record adds one allocation that accrues costPerHour * hours over `hours`
// GPU-hours. `spot` flags whether the allocation used a spot (preemptible)
// price; it is retained for reporting/inspection but does not change the
// blended math, which is purely hours-weighted over the actual cost paid.
//
// Negative inputs are clamped to zero so a single bad record cannot invert the
// totals; callers in tests pass only non-negative values.
func (t *Tracker) Record(costPerHour, hours float64, spot bool) {
	if costPerHour < 0 {
		costPerHour = 0
	}
	if hours < 0 {
		hours = 0
	}
	t.mu.Lock()
	t.allocs = append(t.allocs, allocation{costPerHour: costPerHour, hours: hours, spot: spot})
	t.mu.Unlock()
}

// Report is the computed monthly summary.
type Report struct {
	// TotalCost is the sum of costPerHour*hours over all allocations.
	TotalCost float64
	// TotalHours is the sum of hours over all allocations (GPU-hours).
	TotalHours float64
	// BlendedPerHour is TotalCost/TotalHours (0 when there are no hours).
	BlendedPerHour float64
	// PctReductionVsBaseline is (baselineTotal-TotalCost)/baselineTotal where
	// baselineTotal = baselineRate*TotalHours. It is 0 when baselineTotal is 0.
	// A positive value means the actual spend was cheaper than the baseline.
	PctReductionVsBaseline float64
}

// Report computes the monthly summary against an AWS on-demand baseline rate
// (cost per GPU-hour). It is a pure function of the recorded allocations and the
// supplied baselineRatePerHour; it performs no I/O and reads no clock.
func (t *Tracker) Report(baselineRatePerHour float64) Report {
	var totalCost, totalHours float64
	t.mu.Lock()
	for _, a := range t.allocs {
		totalCost += a.costPerHour * a.hours
		totalHours += a.hours
	}
	t.mu.Unlock()

	r := Report{TotalCost: totalCost, TotalHours: totalHours}

	if totalHours > 0 {
		r.BlendedPerHour = totalCost / totalHours
	}

	baselineTotal := baselineRatePerHour * totalHours
	if baselineTotal != 0 {
		r.PctReductionVsBaseline = (baselineTotal - totalCost) / baselineTotal
	}

	return r
}
