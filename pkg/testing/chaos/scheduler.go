package chaos

import (
	"math/rand"
)

// Injection records one step of a chaos schedule: which fault was selected,
// what category it belongs to, and the virtual timestamp at which it was
// injected.
type Injection struct {
	// Step is the zero-based index of this injection within the run.
	Step int
	// FaultName is the value returned by the selected fault's Name() method.
	FaultName string
	// Category is the fault's category (from Categorized if implemented, or
	// "uncategorized" for legacy faults that predate the Categorized interface).
	Category Category
	// VirtualTS is a monotonically-increasing virtual nanosecond timestamp
	// that advances by a fixed quantum per step so that logs are comparable
	// and reproducible across runs with the same seed.
	VirtualTS int64
}

// InjectionLog is an ordered sequence of Injection records produced by a
// Schedule run.
type InjectionLog []Injection

// virtualTSQuantum is the number of nanoseconds each step advances the virtual
// clock. A fixed quantum (1 ms) is used rather than a random draw so that the
// virtual timestamps are always strictly monotone regardless of the number of
// faults in the catalog.
const virtualTSQuantum = int64(1_000_000) // 1 ms in ns

// Schedule is a seeded, deterministic fault sequencer. It draws from the
// FaultCatalog using a private *rand.Rand seeded at construction time so that
// any two Schedules created with the same seed produce identical InjectionLogs.
type Schedule struct {
	seed   int64
	rng    *rand.Rand
	faults []Fault
}

// ExtendedCatalog returns FaultCatalog(seed) augmented with the ByzantineFault,
// giving the scheduler access to all 27+ distinct fault types (≥25 required).
// This is the catalog used by NewSchedule so that Byzantine equivocation is
// exercised during scheduled runs.
func ExtendedCatalog(seed int64) []Fault {
	base := FaultCatalog(seed)
	base = append(base, &ByzantineFault{
		NodeID: "n1",
		Values: []string{"leader", "follower", "candidate"},
	})
	return base
}

// NewSchedule constructs a Schedule seeded with seed. The fault catalog is
// drawn from ExtendedCatalog(seed) (which includes ByzantineFault) so the
// catalog composition itself is also a function of the seed, making the entire
// schedule fully reproducible.
func NewSchedule(seed int64) *Schedule {
	return &Schedule{
		seed:   seed,
		rng:    rand.New(rand.NewSource(seed)), //gosec:disable G404 -- deterministic chaos schedule requires a reproducible seeded PRNG, not crypto/rand
		faults: ExtendedCatalog(seed),
	}
}

// Run executes steps injection cycles and returns the resulting InjectionLog.
// Each cycle:
//  1. Picks a fault uniformly at random from the catalog using the seeded rng.
//  2. Calls Apply() on the selected fault.
//  3. Records an Injection (step, name, category, virtual timestamp).
//  4. Calls Restore() to return the fault to a clean state for the next cycle.
//
// Because the rng is fully seeded and the catalog is deterministic, Run with
// the same seed and steps count always returns a byte-identical InjectionLog.
func (s *Schedule) Run(steps int) InjectionLog {
	log := make(InjectionLog, 0, steps)
	if steps == 0 {
		return log
	}
	for step := 0; step < steps; step++ {
		// Pick a fault using the seeded rng.  This is the line whose mutation
		// (e.g. always picking index 0) would cause the divergence assertion in
		// TestScheduleDeterminism to survive but the mutation proof would expose
		// that two different seeds still produce identical sequences.
		idx := s.rng.Intn(len(s.faults))
		f := s.faults[idx]

		// Apply — ignore error (fault may already be in the required state from
		// a prior step; the catalog builds fresh instances so this should not
		// happen in normal operation, but we are defensive).
		_ = f.Apply()

		// Determine category.
		cat := Category("uncategorized")
		if c, ok := f.(Categorized); ok {
			cat = c.Category()
		}

		// Virtual timestamp: strictly monotone, 1 ms per step.
		vts := int64(step+1) * virtualTSQuantum

		log = append(log, Injection{
			Step:      step,
			FaultName: f.Name(),
			Category:  cat,
			VirtualTS: vts,
		})

		// Restore so the fault is ready for the next cycle.
		_ = f.Restore()
	}
	return log
}
