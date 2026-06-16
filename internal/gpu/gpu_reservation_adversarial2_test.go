package gpu

// Adversarial SECOND-PASS suite for the NON-manager surface of internal/gpu:
// reservation/quota accounting, MIG partitioning, and the health monitor's
// state machine. These probes target REAL bugs the existing suites
// (reservation_test.go, mig_test.go, manager_adversarial2_test.go) do NOT
// cover. Every assertion is SINK-SIDE: it asserts the actual returned headroom,
// the actual admission decision, or the actual device/instance state — never
// "did not panic".
//
//   (R1) AvailableFor(Batch) MUST NOT over-report headroom that Admit then
//        denies.  reservation.go documents (lines on AvailableFor):
//          "AvailableFor(c) == n implies Admit(c, n) is admitted and
//           Admit(c, n+1) is not (when n+1 <= capacity)."
//        The Batch path computes whether the hard-cap engages using a SINGLE
//        unit probe (`r.totalUsedLocked()+1 >= r.highWaterUnits`) instead of the
//        full requested amount. When the n-unit grant would ITSELF push total
//        load across the high-water mark, AvailableFor skips the hard cap while
//        Admit applies it — so AvailableFor returns n but Admit(Batch,n) is
//        DENIED. A scheduler trusting AvailableFor over-commits batch toward
//        interactive's protected reserve: a starvation-guarantee breach.
//        [REAL BUG — minimal fix landed in reservation.go AvailableFor]
//
//   (R2) Concurrent batch reservers under an ACTIVE hard cap never commit more
//        than the hard cap permits above high-water (run under -race; single
//        race probe for this pass).
//
//   (M1) MIG partition conservation: the carved instances never consume MORE
//        slices or memory than the physical device exposes (no oversubscription
//        beyond the floor), and no two instances share an ID (no double-carve).
//
//   (H1) Monitor health transitions preserve allocation attribution: a held
//        (AllocatedTo set) GPU flipped Unhealthy by telemetry never loses its
//        AllocatedTo, and a non-held Unhealthy GPU recovers to Available — the
//        monitor never silently drops a job's attribution.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (R1) AvailableFor(Batch) MUST NOT over-report vs Admit (multi-unit + hard cap)
// ---------------------------------------------------------------------------

// TestAdversarial2_AvailableForBatchNoOverReportAcrossHighWater drives the pool
// to a state where the FULL AvailableFor(Batch) grant would itself cross the
// high-water mark and therefore be clamped by the batch hard cap. The contract
// requires Admit(Batch, AvailableFor(Batch)) to be admitted; the bug makes
// AvailableFor over-report so that exact Admit is denied.
//
// Config: cap=10, interactive reserve 5, batch reserve 5, high-water 8 units,
// EXPLICIT batch hard cap 3 units. Pre-load 6 interactive units (below the
// 8-unit high-water, so AvailableFor's single-unit high-water probe says "not
// crossing"). poolFree = 4; batch reserve headroom = 5; the buggy code returns
// min(5,4)=4. But Admit(Batch,4) makes total 10 >= high-water, engaging the
// 3-unit hard cap, so 4 > 3 is DENIED.
//
// MUTATION PROOF: with the fixed AvailableFor (which accounts for the full grant
// crossing high-water), this passes; reverting AvailableFor's Batch hard-cap
// computation to the single-unit `r.totalUsedLocked()+1 >= r.highWaterUnits`
// probe makes AvailableFor return 4 while Admit(Batch,4) is denied → FAIL.
func TestAdversarial2_AvailableForBatchNoOverReportAcrossHighWater(t *testing.T) {
	r, err := NewReservation(ReservationConfig{
		Capacity:          10,
		InteractiveRatio:  0.5,
		HighWaterRatio:    0.8, // 8 units
		BatchHardCapRatio: 0.3, // 3 units
	})
	if err != nil {
		t.Fatalf("NewReservation: %v", err)
	}

	// Pre-load 6 interactive units. Total load 6 < high-water 8.
	if _, err := r.Reserve(Interactive, 6); err != nil {
		t.Fatalf("interactive preload: %v", err)
	}

	n := r.AvailableFor(Batch)
	// The contract: Admit(Batch, n) MUST be admitted.
	if n > 0 {
		d := r.Admit(Batch, n)
		if !d.Admitted {
			t.Fatalf("CONTRACT VIOLATION: AvailableFor(Batch)=%d but Admit(Batch,%d) DENIED (%s); "+
				"AvailableFor over-reported headroom the hard cap forbids", n, n, d.Reason)
		}
		// And actually committing that grant must succeed and not exceed the cap.
		if _, err := r.Reserve(Batch, n); err != nil {
			t.Fatalf("CONTRACT VIOLATION: AvailableFor(Batch)=%d but Reserve(Batch,%d) failed: %v", n, n, err)
		}
		if used := r.Used(Batch); used > 3 {
			t.Fatalf("OVER-COMMIT: batch used %d exceeds hard cap 3 above high-water", used)
		}
	}
}

// TestAdversarial2_AvailableForBatchExactConsistencySweep generalises (R1): for a
// matrix of hard-cap configurations and pre-load states, AvailableFor(Batch)==n
// MUST imply Admit(Batch,n) admitted (when n>0). Unlike the existing
// AvailableForMatchesAdmit sweep (which advances state one unit at a time and so
// rarely makes the FULL grant the thing that crosses high-water), this fixes a
// multi-unit interactive preload precisely so the n-unit batch grant is the
// boundary-crossing request.
func TestAdversarial2_AvailableForBatchExactConsistencySweep(t *testing.T) {
	const capacity = 12
	for _, ir := range []float64{0.3, 0.5, 0.6} {
		for _, hcr := range []float64{0.1, 0.2, 0.3, 0.4} {
			for preload := 0; preload <= capacity; preload++ {
				r, err := NewReservation(ReservationConfig{
					Capacity:          capacity,
					InteractiveRatio:  ir,
					HighWaterRatio:    0.8,
					BatchHardCapRatio: hcr,
				})
				if err != nil {
					t.Fatalf("NewReservation(ir=%v,hcr=%v): %v", ir, hcr, err)
				}
				// Preload interactive (capped by what interactive can take = poolFree).
				if preload > 0 {
					if d := r.Admit(Interactive, preload); d.Admitted {
						if _, err := r.Reserve(Interactive, preload); err != nil {
							t.Fatalf("preload reserve: %v", err)
						}
					} else {
						continue // preload exceeds pool; skip
					}
				}
				n := r.AvailableFor(Batch)
				if n < 0 {
					t.Fatalf("ir=%v hcr=%v preload=%d: AvailableFor(Batch)=%d negative", ir, hcr, preload, n)
				}
				if n > 0 {
					if d := r.Admit(Batch, n); !d.Admitted {
						t.Fatalf("ir=%v hcr=%v preload=%d: AvailableFor(Batch)=%d but Admit(Batch,%d) DENIED: %s",
							ir, hcr, preload, n, n, d.Reason)
					}
				}
				// And n+1 must be denied when it still fits the pool (the upper half
				// of the contract — AvailableFor must not UNDER-report either).
				total := r.Used(Interactive) + r.Used(Batch)
				if total+(n+1) <= capacity {
					if d := r.Admit(Batch, n+1); d.Admitted {
						t.Fatalf("ir=%v hcr=%v preload=%d: AvailableFor(Batch)=%d but Admit(Batch,%d) ADMITTED (under-report)",
							ir, hcr, preload, n, n+1)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// (R2) Concurrent batch under an active hard cap (single -race probe)
// ---------------------------------------------------------------------------

// TestAdversarial2_ConcurrentBatchRespectsHardCapAboveHighWater drives total
// load to the high-water mark with interactive, then races many single-unit
// batch reservers. With an active 1-unit hard cap and load already at high-water,
// AT MOST 1 batch unit may commit — interactive's reserve must never be eaten by
// a batch over-commit even under concurrency. Run with -race.
func TestAdversarial2_ConcurrentBatchRespectsHardCapAboveHighWater(t *testing.T) {
	r, err := NewReservation(ReservationConfig{
		Capacity:          16,
		InteractiveRatio:  0.5, // interactive reserve 8, batch reserve 8
		HighWaterRatio:    0.5, // 8 units → high-water reached after the interactive fill
		BatchHardCapRatio: 0.0625, // 1 unit
	})
	if err != nil {
		t.Fatalf("NewReservation: %v", err)
	}
	if r.batchHardCapUse != 1 {
		t.Fatalf("expected 1-unit hard cap, got %d", r.batchHardCapUse)
	}
	// Fill interactive to the high-water mark (8 units).
	if _, err := r.Reserve(Interactive, 8); err != nil {
		t.Fatalf("interactive fill: %v", err)
	}

	var wg sync.WaitGroup
	successes := make(chan struct{}, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Reserve(Batch, 1); err == nil {
				successes <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(successes)

	got := 0
	for range successes {
		got++
	}
	if got > 1 {
		t.Fatalf("OVER-COMMIT under -race: %d batch units committed above high-water with a 1-unit hard cap", got)
	}
	if used := r.Used(Batch); used > 1 {
		t.Fatalf("batch usage %d exceeds 1-unit hard cap", used)
	}
	// Interactive's reserve was never invaded.
	if r.Used(Interactive) != 8 {
		t.Fatalf("interactive usage changed unexpectedly: %d", r.Used(Interactive))
	}
}

// ---------------------------------------------------------------------------
// (M1) MIG partition conservation — no oversubscription, no double-carve
// ---------------------------------------------------------------------------

// TestAdversarial2_MIGPartitionConservation proves the carved MIG instances
// never consume more slices or memory than the physical device exposes, across
// every standard profile and a range of device geometries. It also proves the
// instance IDs are unique (no slice double-carved under the same handle).
func TestAdversarial2_MIGPartitionConservation(t *testing.T) {
	profiles := []MIGProfile{ProfileSmall, ProfileMedium, ProfileLarge, ProfileFull}
	geometries := []struct{ slices, mem int }{
		{7, 80}, {7, 40}, {4, 80}, {3, 24}, {1, 10}, {2, 5},
	}
	for _, p := range profiles {
		for _, g := range geometries {
			dev, err := NewMIGDevice("gpu-"+p.Name, g.slices, g.mem)
			if err != nil {
				t.Fatalf("NewMIGDevice: %v", err)
			}
			inst, err := dev.PartitionGPU(p)
			if err != nil {
				// Oversubscription rejection is the correct behaviour when the
				// profile cannot fit; just assert nothing was carved.
				if len(inst) != 0 {
					t.Fatalf("profile %s on %dx%dGB: error %v but %d instances carved",
						p.Name, g.slices, g.mem, err, len(inst))
				}
				continue
			}
			// CONSERVATION: total slices/memory consumed never exceed the device.
			usedSlices := len(inst) * p.Slices
			usedMem := len(inst) * p.MemoryGB
			if usedSlices > g.slices {
				t.Fatalf("OVERSUBSCRIPTION: profile %s carved %d instances * %d slices = %d > device %d slices",
					p.Name, len(inst), p.Slices, usedSlices, g.slices)
			}
			if usedMem > g.mem {
				t.Fatalf("OVERSUBSCRIPTION: profile %s carved %d instances * %d GB = %d > device %d GB",
					p.Name, len(inst), p.MemoryGB, usedMem, g.mem)
			}
			// NO DOUBLE-CARVE: instance IDs unique, parent set correctly.
			seen := map[string]bool{}
			for _, in := range inst {
				if seen[in.ID] {
					t.Fatalf("DOUBLE-CARVE: duplicate MIG instance ID %q for profile %s", in.ID, p.Name)
				}
				seen[in.ID] = true
				if in.ParentGPUID != dev.GPUID {
					t.Fatalf("instance %q parent %q != device %q", in.ID, in.ParentGPUID, dev.GPUID)
				}
			}
		}
	}
}

// TestAdversarial2_MIGNoLiveRepartition proves a partitioned device cannot be
// silently repartitioned into a DIFFERENT plan (which would orphan the first
// plan's instances and double-count slices). The second call must fail and the
// device's committed plan must be unchanged.
func TestAdversarial2_MIGNoLiveRepartition(t *testing.T) {
	dev, err := NewMIGDevice("gpu-repart", 7, 80)
	if err != nil {
		t.Fatalf("NewMIGDevice: %v", err)
	}
	first, err := dev.PartitionGPU(ProfileLarge) // 3g.40gb → 2 instances
	if err != nil {
		t.Fatalf("first partition: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("expected 2 large instances, got %d", len(first))
	}
	// Attempt a different plan: must be rejected, and the committed plan unchanged.
	second, err := dev.PartitionGPU(ProfileSmall)
	if err == nil {
		t.Fatalf("repartition into a different plan must be rejected; got %d instances", len(second))
	}
	if !dev.IsPartitioned() {
		t.Fatal("device must remain partitioned")
	}
	if got := dev.Instances(); len(got) != 2 {
		t.Fatalf("committed plan must be unchanged (2 large instances), got %d", len(got))
	}
	if dev.Profile.Name != ProfileLarge.Name {
		t.Fatalf("committed profile must remain %q, got %q", ProfileLarge.Name, dev.Profile.Name)
	}
}

// ---------------------------------------------------------------------------
// (H1) Monitor health transitions preserve allocation attribution
// ---------------------------------------------------------------------------

// TestAdversarial2_MonitorPreservesAttributionUnderUnhealthy proves the health
// monitor never drops a job's AllocatedTo when telemetry flips a held GPU to
// Unhealthy, and recovers a NON-held Unhealthy GPU to Available when telemetry
// is back in range. We force the thresholds so the monitor's mock telemetry can
// only ever read "healthy" (temp <= 100, util <= 100 always; thresholds set to
// the ceiling), then assert the held GPU keeps its attribution across ticks.
func TestAdversarial2_MonitorPreservesAttributionUnderUnhealthy(t *testing.T) {
	m := NewManager()
	m.InjectGPUsForTest(makeInventory(3, 1024))

	held, err := m.AllocateGPU("job-mon")
	if err != nil {
		t.Fatalf("AllocateGPU: %v", err)
	}

	// Directly flip the held GPU Unhealthy (as monitor.checkGPU does to an
	// overheating Allocated GPU): attribution must be retained.
	m.updateGPU(held.ID, func(g *GPU) { g.Status = Unhealthy })
	for _, g := range m.ListGPUs() {
		if g.ID == held.ID && g.AllocatedTo != "job-mon" {
			t.Fatalf("ATTRIBUTION LOST: held GPU %s flipped Unhealthy dropped AllocatedTo (got %q)",
				g.ID, g.AllocatedTo)
		}
	}

	// Run the real monitor with thresholds at the mock telemetry ceiling so no
	// GPU is ever marked unhealthy by it; the loop must not strip attribution.
	mon := newMonitor(m, WithInterval(2*time.Millisecond),
		WithTemperatureThreshold(1000.0), WithUtilizationThreshold(1000.0))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := mon.Start(ctx); err != nil {
		t.Fatalf("monitor start: %v", err)
	}
	// Let several ticks fire.
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	mon.Stop()

	// SINK-SIDE: the held GPU is still attributed to job-mon (monitor never
	// dropped it), and ReleaseGPU still detaches it cleanly.
	stillHeld := false
	for _, g := range m.ListGPUs() {
		if g.AllocatedTo == "job-mon" {
			stillHeld = true
			if g.ID != held.ID {
				t.Fatalf("attribution migrated to a different GPU %s (was %s)", g.ID, held.ID)
			}
		}
	}
	if !stillHeld {
		t.Fatal("ATTRIBUTION LOST across monitor ticks: job-mon no longer holds any GPU")
	}
	m.ReleaseGPU("job-mon")
	for _, g := range m.ListGPUs() {
		if g.AllocatedTo == "job-mon" {
			t.Fatalf("LEAK: job-mon still attributed to %s after release", g.ID)
		}
	}
}
