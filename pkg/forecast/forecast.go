// Package forecast implements a deterministic, pure-Go predictive scaling
// forecaster. It pre-warms capacity BEFORE a utilization threshold is crossed
// by projecting a short-window linear trend forward over a lead-time horizon.
//
// Model: a stream of (tick, utilization) samples is fed in via Observe. The
// Forecaster keeps a trailing window of the most recent Window samples and
// fits a least-squares line over (tick, util). Given the latest measured
// utilization and the fitted slope, it PROJECTS forward by Lead ticks:
//
//	projected = current + slope*Lead
//
// If projected >= Threshold AND the trend is rising (slope > 0), it fires a
// PreWarm signal NOW — strictly before the tick at which utilization actually
// reaches Threshold. A flat or declining trend (slope <= 0) never fires.
//
// The package is self-contained, deterministic, and performs no I/O, no
// time.Now(), and no host/network access. All inputs are injected by the
// caller.
package forecast

// slopeEpsilon is a deadband below which a fitted slope is treated as flat.
// Least-squares fitting of a genuinely constant (or near-constant) trace can
// yield a slope on the order of floating-point rounding noise (~1e-15) rather
// than exactly zero. Treating such noise as "rising" would fire a pre-warm on a
// flat trace — a false positive. A slope must exceed this magnitude to count as
// a real upward trend. The value is far below any meaningful utilization trend
// (utilization is in [0,1] over integer ticks) yet far above rounding noise.
const slopeEpsilon = 1e-9

// Forecaster predicts whether utilization will reach Threshold within Lead
// ticks and pre-warms ahead of the actual crossing.
//
// Zero value is not usable; construct with a positive Window, a Threshold, and
// a positive Lead. The zero value of internal bookkeeping fields is valid (no
// pre-warm yet).
type Forecaster struct {
	// Window is the number of trailing samples used to fit the trend line.
	// Must be >= 2 for a slope to be defined.
	Window int
	// Threshold is the utilization level at/above which pre-warming is needed.
	Threshold float64
	// Lead is the look-ahead horizon, in ticks, over which the trend is
	// projected. Must be > 0 for projection to look ahead.
	Lead int

	// samples is the trailing ring of recent observations, most-recent-last.
	samples []sample

	// fired records whether a PreWarm has ever fired and, if so, the tick at
	// which it first fired.
	fired      bool
	firedTick  int
	firedKnown bool
}

type sample struct {
	tick int
	util float64
}

// Observe ingests one (tick, util) sample and returns whether a PreWarm should
// fire NOW together with the projected utilization at horizon Lead.
//
// PreWarm is true on a given Observe call iff the fitted slope over the current
// window is strictly positive (rising) and the projected utilization
// (current + slope*Lead) is >= Threshold. Because projection looks Lead ticks
// ahead along a rising line, this fires strictly before the tick at which the
// measured utilization actually reaches Threshold (for a monotonically rising
// trace), making the signal pre-emptive rather than reactive.
//
// The returned projected value is current+slope*Lead; when the slope is not yet
// defined (fewer than 2 samples) the projection equals the current value and
// PreWarm is false.
func (f *Forecaster) Observe(tick int, util float64) (preWarm bool, projected float64) {
	// Append and trim to the trailing window.
	f.samples = append(f.samples, sample{tick: tick, util: util})
	if f.Window > 0 && len(f.samples) > f.Window {
		// Drop oldest; keep last Window.
		f.samples = append(f.samples[:0], f.samples[len(f.samples)-f.Window:]...)
	}

	slope, ok := f.slope()
	if !ok {
		// Not enough data to define a trend; purely report current value, no fire.
		return false, util
	}

	projected = util + slope*float64(f.Lead)

	// Rising trend required: a flat or declining slope never fires. A slope
	// within +/-slopeEpsilon is treated as flat (see slopeEpsilon) so
	// floating-point rounding on a constant trace cannot trigger a false fire.
	if slope > slopeEpsilon && projected >= f.Threshold {
		preWarm = true
		if !f.fired {
			f.fired = true
			f.firedTick = tick
			f.firedKnown = true
		}
	}
	return preWarm, projected
}

// slope returns the least-squares slope of util vs tick over the current
// window. ok is false when fewer than two samples (or a degenerate spread of
// ticks) prevent a slope from being defined.
func (f *Forecaster) slope() (slopeVal float64, ok bool) {
	n := len(f.samples)
	if n < 2 {
		return 0, false
	}
	var sumX, sumY, sumXY, sumXX float64
	for _, s := range f.samples {
		x := float64(s.tick)
		y := s.util
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	fn := float64(n)
	denom := fn*sumXX - sumX*sumX
	if denom == 0 {
		// All ticks identical: slope undefined.
		return 0, false
	}
	return (fn*sumXY - sumX*sumY) / denom, true
}

// FiredTick reports the tick at which a PreWarm first fired and whether one has
// fired at all. If ok is false, no PreWarm has fired yet.
func (f *Forecaster) FiredTick() (tick int, ok bool) {
	return f.firedTick, f.firedKnown
}
