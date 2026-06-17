// Package carbonsched implements a carbon-aware scheduler with per-job energy
// metering (HXC-1480). Given a job and a set of candidate regions, it selects
// the latency-eligible region with the lowest carbon intensity and meters the
// resulting energy use (kWh) and carbon emission (grams CO2).
package carbonsched

import (
	"errors"
	"math"
)

// Region describes a candidate placement target.
//
//   - CarbonIntensity is the grid carbon intensity in grams of CO2 per kWh.
//   - LatencyMS is the network round-trip latency to the region in milliseconds.
type Region struct {
	Name            string
	CarbonIntensity float64 // gCO2 per kWh
	LatencyMS       float64 // network latency
}

// Job describes a unit of work to place.
//
//   - PowerWatts is the average power draw of the job while running.
//   - DurationHours is how long the job runs.
//   - MaxLatencyMS is the maximum tolerable network latency for the placement.
type Job struct {
	ID            string
	PowerWatts    float64
	DurationHours float64
	MaxLatencyMS  float64
}

// Placement is the metered result of placing a Job in a chosen Region.
type Placement struct {
	JobID    string
	Region   string
	KWh      float64
	GramsCO2 float64
}

// ErrNoEligibleRegion is returned by Place when no region satisfies the job's
// latency constraint.
var ErrNoEligibleRegion = errors.New("carbonsched: no eligible region")

// Place selects the best region for job and returns the metered Placement.
//
// Only regions whose LatencyMS <= job.MaxLatencyMS are considered eligible.
// Among the eligible regions, the one with the LOWEST CarbonIntensity is chosen;
// ties are broken deterministically by the lexicographically smallest Name. If
// no region is eligible, ErrNoEligibleRegion is returned.
//
// Energy and emissions are metered as:
//
//	KWh      = PowerWatts/1000 * DurationHours
//	GramsCO2 = KWh * chosenRegion.CarbonIntensity
func Place(job Job, regions []Region) (Placement, error) {
	var best *Region
	for i := range regions {
		r := &regions[i]
		// Eligibility is the documented predicate `LatencyMS <= MaxLatencyMS`.
		// It is written affirmatively (rather than `LatencyMS > Max { continue }`)
		// so that a NaN latency is correctly EXCLUDED: a NaN ("unknown"/sensor-
		// error) latency is not <= anything, so `NaN <= Max` is false and the
		// region is skipped. The negated form `NaN > Max` is also false, which
		// would wrongly ADMIT an unknown-latency region as eligible — letting it
		// be selected as "greenest" and meter its intensity into the GramsCO2 sink
		// despite a latency we do not know. This mirrors the NaN-CarbonIntensity
		// guard below.
		if !(r.LatencyMS <= job.MaxLatencyMS) {
			continue // latency-ineligible (NaN latency falls here)
		}
		if math.IsNaN(r.CarbonIntensity) {
			// A NaN carbon intensity is an unknown/sensor-error reading. Every
			// comparison against NaN is false, so without this guard a NaN region
			// scanned first would become `best` and never be displaced — it would
			// be selected as "greenest" and emit a NaN GramsCO2, and selection
			// would become input-order-dependent, violating the documented
			// lowest-intensity total order. Treat it as never selectable.
			continue
		}
		if best == nil ||
			r.CarbonIntensity < best.CarbonIntensity ||
			(r.CarbonIntensity == best.CarbonIntensity && r.Name < best.Name) {
			best = r
		}
	}
	if best == nil {
		return Placement{}, ErrNoEligibleRegion
	}

	kWh := job.PowerWatts / 1000 * job.DurationHours
	return Placement{
		JobID:    job.ID,
		Region:   best.Name,
		KWh:      kWh,
		GramsCO2: kWh * best.CarbonIntensity,
	}, nil
}
