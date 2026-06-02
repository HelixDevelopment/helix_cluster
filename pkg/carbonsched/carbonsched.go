// Package carbonsched implements a carbon-aware scheduler with per-job energy
// metering (HXC-1480). Given a job and a set of candidate regions, it selects
// the latency-eligible region with the lowest carbon intensity and meters the
// resulting energy use (kWh) and carbon emission (grams CO2).
package carbonsched

import "errors"

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
		if r.LatencyMS > job.MaxLatencyMS {
			continue // latency-ineligible
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
