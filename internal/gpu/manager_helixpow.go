package gpu

import "fmt"

// helixPoWStarvationThreshold is the Helix-load fraction above which
// ChutesCapacity hard-caps the Chutes (batch / external) available count and
// free memory to zero. This is the "Gepetto starvation guard": once Helix PoW
// is consuming more than 80 % of its reserved slice, external schedulers are
// granted ZERO capacity so they cannot starve Helix PoW work.
//
// WHY a named constant: the threshold appears in both ChutesCapacity and in the
// test table so a single source of truth prevents the test silently diverging
// from the runtime guard.
const helixPoWStarvationThreshold = 0.80

// ReserveForHelixPoW records fraction as the portion [0, 1] of the total GPU
// pool reserved for Helix Proof-of-Work. It does NOT allocate or mark any GPU;
// it stores an accounting fraction that ChutesCapacity uses when computing the
// non-reserved remainder available to external schedulers (e.g. Chutes).
//
// Validation:
//   - fraction must be in [0, 1] (inclusive on both ends).
//   - Storing 1.0 means the entire pool is claimed for Helix PoW; 0.0 means no
//     reservation (ChutesCapacity equals the full Stats).
func (m *Manager) ReserveForHelixPoW(fraction float64) error {
	if fraction < 0 || fraction > 1 {
		return fmt.Errorf("gpu: ReserveForHelixPoW: fraction %v out of [0,1]", fraction)
	}
	m.mu.Lock()
	m.helixPoWFraction = fraction
	m.mu.Unlock()
	return nil
}

// SetHelixLoad records fraction as the current Helix PoW load [0, 1]. Callers
// (e.g. the PoW scheduler) report this continuously; ChutesCapacity reads it to
// decide whether the Gepetto starvation guard must engage.
//
// A value > 0.80 triggers the hard-cap: ChutesCapacity returns zero Available
// and zero FreeMemoryMB regardless of the reservation fraction, preventing
// external schedulers from being assigned any GPU while Helix is under heavy
// load.
func (m *Manager) SetHelixLoad(fraction float64) {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	m.mu.Lock()
	m.helixLoad = fraction
	m.mu.Unlock()
}

// ChutesCapacity returns a Stats snapshot describing the GPU capacity available
// to external schedulers ("Chutes") after accounting for:
//
//  1. The Helix PoW reservation fraction set by ReserveForHelixPoW.
//  2. The Gepetto starvation guard: if the current Helix load (set by
//     SetHelixLoad) is strictly above helixPoWStarvationThreshold (0.80),
//     Available and FreeMemoryMB are forced to zero so Chutes receives no
//     resources at all, regardless of reservation arithmetic.
//
// Non-count fields (Total, Allocated, Unhealthy, Offline, TotalMemoryMB)
// reflect the full physical inventory; only Available and FreeMemoryMB are
// adjusted to show the Chutes-visible slice.
func (m *Manager) ChutesCapacity() Stats {
	m.mu.RLock()
	powFraction := m.helixPoWFraction
	load := m.helixLoad
	m.mu.RUnlock()

	base := m.Stats()

	// Gepetto starvation guard: Helix load is hot — Chutes gets nothing.
	// WHY strictly greater than (not >=): the threshold is 0.80; a load of
	// exactly 0.80 is at the boundary and is NOT considered "above threshold",
	// consistent with the spec wording "when current Helix load > 0.80".
	if load > helixPoWStarvationThreshold {
		base.Available = 0
		base.FreeMemoryMB = 0
		return base
	}

	// Normal case: apply the reservation fraction.
	// chutesRatio is the complement of the PoW reservation: the fraction of
	// the pool that Chutes may use.
	chutesRatio := 1.0 - powFraction

	// Scale Available (count) and FreeMemoryMB by the Chutes ratio.
	// Truncation (implicit in int conversion) floors the result so we never
	// over-promise capacity to an external scheduler.
	base.Available = int(float64(base.Available) * chutesRatio)
	base.FreeMemoryMB = int(float64(base.FreeMemoryMB) * chutesRatio)

	return base
}
