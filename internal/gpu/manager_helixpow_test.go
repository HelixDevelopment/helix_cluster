package gpu

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newManagerWith4GPUs seeds a Manager with the standard 4-GPU mock inventory
// (two 40960MB + two 81920MB, all Available) and returns it ready for
// reservation tests. Total: 4 GPUs, 245760 MB.
func newManagerWith4GPUs(t *testing.T) *Manager {
	t.Helper()
	m := NewManager()
	m.InjectGPUsForTest(mockInventory())
	return m
}

// ---------------------------------------------------------------------------
// Closure-criteria tests (all three required by the specification)
// ---------------------------------------------------------------------------

// TestChutesCapacityStarvationGuardFires is closure criterion #1.
// Reserve 0.8 of the pool for Helix PoW, then report Helix load at 0.85.
// The Gepetto starvation guard MUST fire: ChutesCapacity().Available == 0 and
// FreeMemoryMB == 0, regardless of reservation math.
//
// Mutation guard (REQUIRED by spec):
// Removing (or inverting) the "> helixPoWStarvationThreshold" branch in
// ChutesCapacity so it no longer zeros Available when load > 0.80 causes
// this test to fail: without the guard, Available would be
// int(4 * (1-0.8)) == 0 only if reservation math coincidentally yields 0,
// but with powFraction=0.8 the chutesRatio == 0.2 so Available would be
// int(4 * 0.2) == 0 ONLY by coincidence. To guarantee the test ACTUALLY
// exercises the starvation guard (and not reservation math), we set
// powFraction=0.0 so the non-guard path would leave Available untouched
// (== 4), while the guard must still zero it.
func TestChutesCapacityStarvationGuardFires(t *testing.T) {
	m := newManagerWith4GPUs(t)

	// Set reservation to 0.0 (no PoW reservation) so reservation math alone
	// would leave Available == 4.  Only the starvation guard can zero it.
	if err := m.ReserveForHelixPoW(0.0); err != nil {
		t.Fatalf("ReserveForHelixPoW(0.0): %v", err)
	}
	// Report Helix load above the 0.80 threshold.
	m.SetHelixLoad(0.85)

	cs := m.ChutesCapacity()
	if cs.Available != 0 {
		t.Fatalf("starvation guard must zero Available (load=0.85 > 0.80), got %d", cs.Available)
	}
	if cs.FreeMemoryMB != 0 {
		t.Fatalf("starvation guard must zero FreeMemoryMB (load=0.85 > 0.80), got %d", cs.FreeMemoryMB)
	}

	// Non-count fields must still reflect real inventory.
	base := m.Stats()
	if cs.Total != base.Total {
		t.Fatalf("Total must not be zeroed by starvation guard: want %d got %d", base.Total, cs.Total)
	}
	if cs.TotalMemoryMB != base.TotalMemoryMB {
		t.Fatalf("TotalMemoryMB must not be zeroed by starvation guard: want %d got %d", base.TotalMemoryMB, cs.TotalMemoryMB)
	}
}

// TestChutesCapacityNonZeroUnderModerateLoad is closure criterion #2.
// Set Helix load to 0.5 (below the 0.80 threshold).
// ChutesCapacity().Available must be NON-ZERO (the guard must NOT fire;
// remaining capacity flows to Chutes).
func TestChutesCapacityNonZeroUnderModerateLoad(t *testing.T) {
	m := newManagerWith4GPUs(t)

	if err := m.ReserveForHelixPoW(0.5); err != nil {
		t.Fatalf("ReserveForHelixPoW(0.5): %v", err)
	}
	m.SetHelixLoad(0.5)

	cs := m.ChutesCapacity()
	if cs.Available == 0 {
		t.Fatalf("ChutesCapacity().Available must be non-zero when load=0.50 (below guard threshold), got 0")
	}
	if cs.FreeMemoryMB == 0 {
		t.Fatalf("ChutesCapacity().FreeMemoryMB must be non-zero when load=0.50, got 0")
	}
}

// TestChutesCapacityDeltaAfterReservation is closure criterion #3.
// Uses InjectGPUsForTest (the specified test seam) to inject 4 known GPUs,
// computes the FULL Stats() before reservation, then applies a 0.8 PoW
// reservation with low load (0.0 so guard does NOT fire), and asserts the
// delta: ChutesCapacity().Available == int(full.Available * 0.2) and
// ChutesCapacity().FreeMemoryMB == int(full.FreeMemoryMB * 0.2).
//
// This ties the assertion to real inventory injected by InjectGPUsForTest (not
// to constants), so any change to mockInventory() automatically propagates.
func TestChutesCapacityDeltaAfterReservation(t *testing.T) {
	m := newManagerWith4GPUs(t)

	before := m.Stats()
	if before.Total == 0 {
		t.Fatal("mockInventory must produce non-empty inventory")
	}
	if before.Available == 0 {
		t.Fatal("all mockInventory GPUs must start Available")
	}

	// powFraction is a variable (not a const) so that the test uses the same
	// float64 runtime arithmetic as ChutesCapacity — avoiding compile-time
	// constant folding that would produce a slightly different (more precise)
	// result than the stored-float64 path.
	var powFraction float64 = 0.8
	if err := m.ReserveForHelixPoW(powFraction); err != nil {
		t.Fatalf("ReserveForHelixPoW(0.8): %v", err)
	}
	// Load is 0.0 — starvation guard must NOT fire.
	m.SetHelixLoad(0.0)

	cs := m.ChutesCapacity()

	// Mirror the exact arithmetic that ChutesCapacity performs so the expected
	// values are bit-for-bit identical to the production computation.
	chutesRatio := 1.0 - powFraction
	wantAvail := int(float64(before.Available) * chutesRatio)
	wantFree := int(float64(before.FreeMemoryMB) * chutesRatio)

	if cs.Available != wantAvail {
		t.Fatalf("after 0.8 reservation: Available want %d (%.0f%% of %d), got %d",
			wantAvail, chutesRatio*100, before.Available, cs.Available)
	}
	if cs.FreeMemoryMB != wantFree {
		t.Fatalf("after 0.8 reservation: FreeMemoryMB want %d (%.0f%% of %d), got %d",
			wantFree, chutesRatio*100, before.FreeMemoryMB, cs.FreeMemoryMB)
	}
	// Total and TotalMemoryMB must not change (non-count fields).
	if cs.Total != before.Total {
		t.Fatalf("Total must not change: want %d got %d", before.Total, cs.Total)
	}
	if cs.TotalMemoryMB != before.TotalMemoryMB {
		t.Fatalf("TotalMemoryMB must not change: want %d got %d", before.TotalMemoryMB, cs.TotalMemoryMB)
	}
}

// ---------------------------------------------------------------------------
// Additional validation and mutation-catching tests
// ---------------------------------------------------------------------------

// TestReserveForHelixPoWValidation proves out-of-range fractions are rejected
// and in-range extremes (0.0, 1.0) are accepted.
//
// Mutation: change the guard to `fraction < -1` (never rejects) -> negative
// input is accepted, and the assertion on error being non-nil fails.
func TestReserveForHelixPoWValidation(t *testing.T) {
	m := newManagerWith4GPUs(t)

	bad := []float64{-0.1, -1.0, 1.1, 2.0}
	for _, f := range bad {
		if err := m.ReserveForHelixPoW(f); err == nil {
			t.Errorf("expected error for ReserveForHelixPoW(%v), got nil", f)
		}
	}
	good := []float64{0.0, 0.5, 1.0}
	for _, f := range good {
		if err := m.ReserveForHelixPoW(f); err != nil {
			t.Errorf("expected no error for ReserveForHelixPoW(%v), got %v", f, err)
		}
	}
}

// TestChutesCapacityAtExactThresholdNotFiring proves the guard is strictly
// greater-than (not >=): load == 0.80 must NOT zero Chutes capacity.
//
// Mutation: change `>` to `>=` in ChutesCapacity -> load 0.80 fires the guard
// and zeros Available, failing this test.
func TestChutesCapacityAtExactThresholdNotFiring(t *testing.T) {
	m := newManagerWith4GPUs(t)

	if err := m.ReserveForHelixPoW(0.0); err != nil {
		t.Fatalf("ReserveForHelixPoW: %v", err)
	}
	// Exactly at the threshold — guard must NOT fire (strictly-greater-than rule).
	m.SetHelixLoad(helixPoWStarvationThreshold) // == 0.80

	cs := m.ChutesCapacity()
	if cs.Available == 0 {
		t.Fatalf("guard must NOT fire at load == %.2f (only fires when strictly >); Available should be non-zero", helixPoWStarvationThreshold)
	}
}

// TestChutesCapacityFullReservation proves reserving 1.0 leaves Chutes with
// zero capacity (all GPUs claimed for PoW) when load is below the threshold.
//
// Mutation: change `1.0 - powFraction` to `powFraction` -> with fraction=1.0,
// chutesRatio becomes 1.0 instead of 0.0 and Available stays at the full count,
// failing this assertion.
func TestChutesCapacityFullReservation(t *testing.T) {
	m := newManagerWith4GPUs(t)

	if err := m.ReserveForHelixPoW(1.0); err != nil {
		t.Fatalf("ReserveForHelixPoW(1.0): %v", err)
	}
	m.SetHelixLoad(0.0)

	cs := m.ChutesCapacity()
	if cs.Available != 0 {
		t.Fatalf("100%% PoW reservation must leave Chutes with Available==0, got %d", cs.Available)
	}
	if cs.FreeMemoryMB != 0 {
		t.Fatalf("100%% PoW reservation must leave Chutes with FreeMemoryMB==0, got %d", cs.FreeMemoryMB)
	}
}

// TestChutesCapacityZeroReservationPassesAll proves 0.0 reservation returns
// the full Stats() values unchanged when load is below the threshold.
//
// Mutation: change `1.0 - powFraction` to `powFraction` -> with fraction=0.0,
// chutesRatio becomes 0.0 and Available is zeroed, failing this test.
func TestChutesCapacityZeroReservationPassesAll(t *testing.T) {
	m := newManagerWith4GPUs(t)

	if err := m.ReserveForHelixPoW(0.0); err != nil {
		t.Fatalf("ReserveForHelixPoW(0.0): %v", err)
	}
	m.SetHelixLoad(0.0)

	base := m.Stats()
	cs := m.ChutesCapacity()
	if cs.Available != base.Available {
		t.Fatalf("zero reservation must pass full Available: want %d got %d", base.Available, cs.Available)
	}
	if cs.FreeMemoryMB != base.FreeMemoryMB {
		t.Fatalf("zero reservation must pass full FreeMemoryMB: want %d got %d", base.FreeMemoryMB, cs.FreeMemoryMB)
	}
}

// TestChutesCapacityTableDriven is a table-driven sweep of the key decision
// space. It is the MUTATION GUARD for the ">0.80" hard-cap branch:
//
//	Removing the "> helixPoWStarvationThreshold" branch (or replacing > with >=)
//	causes the row {load: 0.85, powFraction: 0.0, wantAvailZero: true} to fail:
//	without the guard, reservation math with powFraction=0.0 would yield
//	chutesRatio=1.0 so Available==4 (non-zero), contradicting wantAvailZero.
func TestChutesCapacityTableDriven(t *testing.T) {
	type row struct {
		name           string
		powFraction    float64
		helixLoad      float64
		wantAvailZero  bool // true if Available must be 0
		wantFreeZero   bool // true if FreeMemoryMB must be 0
		checkAvailFrac float64 // if >0: Available must == int(totalAvail * checkAvailFrac)
	}

	tests := []row{
		{
			name:          "starvation guard fires at load 0.85, powFraction 0.0",
			powFraction:   0.0,
			helixLoad:     0.85,
			wantAvailZero: true,
			wantFreeZero:  true,
			// Mutation: removing the guard makes Available == 4 (non-zero), failing wantAvailZero.
		},
		{
			name:          "starvation guard fires at load 1.0",
			powFraction:   0.0,
			helixLoad:     1.0,
			wantAvailZero: true,
			wantFreeZero:  true,
		},
		{
			name:          "guard fires even with 0.8 powFraction when load=0.99",
			powFraction:   0.8,
			helixLoad:     0.99,
			wantAvailZero: true,
			wantFreeZero:  true,
		},
		{
			name:          "guard does NOT fire at load 0.80 (strictly greater-than rule)",
			powFraction:   0.0,
			helixLoad:     0.80,
			wantAvailZero: false,
			wantFreeZero:  false,
		},
		{
			name:           "moderate load, 50% reservation -> 50% chutes",
			powFraction:    0.5,
			helixLoad:      0.5,
			wantAvailZero:  false,
			wantFreeZero:   false,
			checkAvailFrac: 0.5,
		},
		{
			name:           "no load, no reservation -> full stats",
			powFraction:    0.0,
			helixLoad:      0.0,
			wantAvailZero:  false,
			wantFreeZero:   false,
			checkAvailFrac: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newManagerWith4GPUs(t)
			base := m.Stats()

			if err := m.ReserveForHelixPoW(tt.powFraction); err != nil {
				t.Fatalf("ReserveForHelixPoW(%v): %v", tt.powFraction, err)
			}
			m.SetHelixLoad(tt.helixLoad)

			cs := m.ChutesCapacity()

			if tt.wantAvailZero && cs.Available != 0 {
				t.Errorf("Available must be 0 (starvation/full reservation), got %d", cs.Available)
			}
			if !tt.wantAvailZero && cs.Available == 0 {
				t.Errorf("Available must be non-zero, got 0")
			}
			if tt.wantFreeZero && cs.FreeMemoryMB != 0 {
				t.Errorf("FreeMemoryMB must be 0 (starvation/full reservation), got %d", cs.FreeMemoryMB)
			}
			if !tt.wantFreeZero && cs.FreeMemoryMB == 0 {
				t.Errorf("FreeMemoryMB must be non-zero, got 0")
			}
			if tt.checkAvailFrac > 0 {
				want := int(float64(base.Available) * tt.checkAvailFrac)
				if cs.Available != want {
					t.Errorf("Available: want %d (%.0f%% of %d), got %d",
						want, tt.checkAvailFrac*100, base.Available, cs.Available)
				}
			}
		})
	}
}
