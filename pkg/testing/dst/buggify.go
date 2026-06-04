package dst

import (
	"math/rand"
	"time"
)

// Buggify is a FoundationDB-style deterministic fault-injection framework.
// It fires rarely but reproducibly from a seeded PRNG, enabling simulation
// of rare code paths without altering production logic.
//
// Usage pattern:
//
//	b := dst.NewBuggify(seed, true)
//	if b.FireQuarter() {
//	    // inject fault: e.g. return early, corrupt data, compress timeout
//	}
type Buggify struct {
	rng     *rand.Rand
	enabled bool
}

// NewBuggify creates a Buggify instance with its own seeded PRNG.
// When enabled is false every Fire* call returns false unconditionally.
func NewBuggify(seed int64, enabled bool) *Buggify {
	return &Buggify{
		rng:     rand.New(rand.NewSource(seed)), //gosec:disable G404 -- deterministic simulation testing requires a reproducible seeded PRNG, not crypto/rand
		enabled: enabled,
	}
}

// Enabled reports whether fault injection is active.
func (b *Buggify) Enabled() bool {
	return b.enabled
}

// SetEnabled turns fault injection on or off.
func (b *Buggify) SetEnabled(on bool) {
	b.enabled = on
}

// Fire returns false when Buggify is disabled.
// When enabled it returns true with probability p (0.0 ≤ p ≤ 1.0),
// driven entirely by the seeded PRNG so the sequence is reproducible.
func (b *Buggify) Fire(p float64) bool {
	if !b.enabled {
		return false
	}
	return b.rng.Float64() < p
}

// FireQuarter is the canonical ~25% gate: returns true roughly one time in
// four when enabled (equivalent to b.rng.Intn(4) == 0).
func (b *Buggify) FireQuarter() bool {
	if !b.enabled {
		return false
	}
	return b.rng.Intn(4) == 0
}

// BuggifyDuration returns rare when b.FireQuarter() fires and normal otherwise.
// This models FoundationDB-style timeout compression: e.g. a 60 s timeout is
// squashed to 100 ms under simulation so the test reaches the code path quickly.
func BuggifyDuration(b *Buggify, normal, rare time.Duration) time.Duration {
	if b.FireQuarter() {
		return rare
	}
	return normal
}

// Buggify is a convenience method on Engine that fires with probability p
// using the engine's own seeded PRNG (eng.rng).  It adds NO new struct field
// to Engine; it merely reads the existing unexported field.
func (eng *Engine) Buggify(p float64) bool {
	return eng.rng.Float64() < p
}
