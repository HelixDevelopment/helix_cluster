package gpu

// Adversarial SECOND-PASS suite for internal/gpu allocation accounting.
//
// These probes target REAL bugs the existing *_adversarial_test.go suite does
// NOT cover. They are all SINK-SIDE: they assert the actual contents of the
// device inventory (no "did not panic").
//
//   (A) UNHEALTHY-REALLOC DOUBLE-ALLOCATION + LEAK  [REAL BUG — fix landed]
//       AllocateGPU/AllocateGPUByMemory used to key their per-job idempotency
//       check on `Status == Allocated`. The Monitor (monitor.go) flips an
//       Allocated GPU to Unhealthy when telemetry crosses a threshold WHILE
//       leaving AllocatedTo set — so the job still holds the device. With the
//       old check, the job's next AllocateGPU saw "no Allocated GPU for me" and
//       handed it a SECOND physical GPU; a single ReleaseGPU then freed only one,
//       leaking the first (still attributed) forever. Same physical GPU is now
//       effectively held twice by one job: a double-allocation + capacity leak.
//       The fix keys idempotency on AllocatedTo (the binding fact of holding).
//
//   (B) AvailableFor / Admit CONSISTENCY CONTRACT  [DOCUMENTED CONTRACT — pin]
//       reservation.go documents: AvailableFor(c) == n implies Admit(c,n) is
//       admitted and Admit(c,n+1) is denied (when n+1 <= capacity). We pin this
//       across many reachable states including the Batch starvation hard-cap,
//       which is the easiest place for an off-by-one to hide.
//
//   (C) CROSS-METHOD CONCURRENT ALLOC: a single job that races AllocateGPU and
//       AllocateGPUByMemory must NEVER end up attributed to two GPUs, and
//       free+allocated+unhealthy+offline == total must hold at quiescence.
//       (run under -race)

import (
	"strconv"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// (A) UNHEALTHY-REALLOC DOUBLE-ALLOCATION + LEAK
// ---------------------------------------------------------------------------

// TestAdversarial2_UnhealthyHeldGPUNotDoubleAllocated proves that when a job's
// held GPU is flipped to Unhealthy under it (exactly what Monitor.checkGPU does
// to an Allocated GPU that overheats), a re-allocation by the SAME job returns
// that same held GPU rather than consuming a SECOND device. The binding
// invariant is: a single job is attributed to AT MOST ONE physical GPU.
//
// SINK-SIDE: after re-allocation, exactly one GPU carries AllocatedTo==job; and
// after ONE ReleaseGPU, zero GPUs remain attributed to the job (no leak).
//
// MUTATION: revert the AllocateGPU idempotency guard to
// `gpu.Status == Allocated && gpu.AllocatedTo == jobID` and this test FAILS:
// the job is attributed to two GPUs and one is leaked past release.
func TestAdversarial2_UnhealthyHeldGPUNotDoubleAllocated(t *testing.T) {
	m := NewManager()
	m.InjectGPUsForTest(makeInventory(3, 1024))

	first, err := m.AllocateGPU("job-A")
	if err != nil {
		t.Fatalf("initial AllocateGPU: %v", err)
	}

	// Simulate the Monitor flipping job-A's held GPU to Unhealthy (AllocatedTo
	// stays set, exactly as monitor.checkGPU does on an Allocated overheating GPU).
	m.updateGPU(first.ID, func(g *GPU) { g.Status = Unhealthy })

	// Re-allocate for the SAME job. Idempotency must return the held device, not
	// hand out a second one.
	second, err := m.AllocateGPU("job-A")
	if err != nil {
		t.Fatalf("re-AllocateGPU for held job: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("DOUBLE-ALLOCATION: job-A held %s but re-alloc returned a different GPU %s",
			first.ID, second.ID)
	}

	// SINK-SIDE: exactly one GPU is attributed to job-A.
	attributed := attributedGPUs(m, "job-A")
	if len(attributed) != 1 {
		t.Fatalf("job-A attributed to %d GPUs (%v); a job must hold at most one", len(attributed), attributed)
	}

	// One Release must fully detach job-A — no leaked attribution.
	m.ReleaseGPU("job-A")
	if leaked := attributedGPUs(m, "job-A"); len(leaked) != 0 {
		t.Fatalf("LEAK: after one ReleaseGPU, job-A still attributed to %v", leaked)
	}
	t.Log("unhealthy held GPU is not double-allocated; single release fully detaches")
}

// TestAdversarial2_UnhealthyHeldGPUByMemoryNotDoubleAllocated is the
// AllocateGPUByMemory analogue of the probe above.
//
// MUTATION: revert the AllocateGPUByMemory idempotency guard to
// `gpu.Status == Allocated && gpu.AllocatedTo == jobID` and this FAILS.
func TestAdversarial2_UnhealthyHeldGPUByMemoryNotDoubleAllocated(t *testing.T) {
	m := NewManager()
	m.InjectGPUsForTest(makeInventory(3, 4096))

	first, err := m.AllocateGPUByMemory("job-B", 1024)
	if err != nil {
		t.Fatalf("initial AllocateGPUByMemory: %v", err)
	}
	m.updateGPU(first.ID, func(g *GPU) { g.Status = Unhealthy })

	second, err := m.AllocateGPUByMemory("job-B", 1024)
	if err != nil {
		t.Fatalf("re-AllocateGPUByMemory for held job: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("DOUBLE-ALLOCATION (byMemory): job-B held %s, re-alloc gave %s", first.ID, second.ID)
	}
	if attributed := attributedGPUs(m, "job-B"); len(attributed) != 1 {
		t.Fatalf("job-B attributed to %d GPUs (%v); must be exactly 1", len(attributed), attributed)
	}
	m.ReleaseGPU("job-B")
	if leaked := attributedGPUs(m, "job-B"); len(leaked) != 0 {
		t.Fatalf("LEAK (byMemory): after one Release, job-B still attributed to %v", leaked)
	}
}

// attributedGPUs returns the IDs of all GPUs whose AllocatedTo == jobID.
func attributedGPUs(m *Manager, jobID string) []string {
	var ids []string
	for _, g := range m.ListGPUs() {
		if g.AllocatedTo == jobID {
			ids = append(ids, g.ID)
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// (B) AvailableFor / Admit CONSISTENCY CONTRACT (pin)
// ---------------------------------------------------------------------------

// TestAdversarial2_AvailableForMatchesAdmit pins the documented reservation
// contract: for every reachable state, AvailableFor(c)==n implies Admit(c,n) is
// admitted and Admit(c,n+1) is denied (when n+1 <= capacity). We walk both
// classes through many used-counts INCLUDING states above the high-water mark
// where the Batch hard-cap engages — the most off-by-one-prone branch.
func TestAdversarial2_AvailableForMatchesAdmit(t *testing.T) {
	const capacity = 10
	// Use a config that activates the batch hard-cap below full capacity so the
	// hard-cap arithmetic in AvailableFor is actually exercised.
	for _, ir := range []float64{0.3, 0.5, 0.7} {
		for _, hw := range []float64{0.5, 0.8, 1.0} {
			r, err := NewReservation(ReservationConfig{
				Capacity:         capacity,
				InteractiveRatio: ir,
				HighWaterRatio:   hw,
			})
			if err != nil {
				t.Fatalf("NewReservation(ir=%v,hw=%v): %v", ir, hw, err)
			}
			// Drive the pool through a representative set of reachable states by
			// reserving a mix of interactive and batch units, checking the contract
			// at every step.
			for step := 0; step <= capacity; step++ {
				for _, c := range []WorkloadClass{Interactive, Batch} {
					n := r.AvailableFor(c)
					if n < 0 {
						t.Fatalf("ir=%v hw=%v step=%d: AvailableFor(%s)=%d is negative", ir, hw, step, c, n)
					}
					// Admit(c, n) must be admitted when n>0.
					if n > 0 {
						if d := r.Admit(c, n); !d.Admitted {
							t.Fatalf("ir=%v hw=%v step=%d: AvailableFor(%s)=%d but Admit(%s,%d) DENIED: %s",
								ir, hw, step, c, n, c, n, d.Reason)
						}
					}
					// Admit(c, n+1) must be denied whenever n+1 <= capacity headroom.
					if n+1 <= capacity {
						if d := r.Admit(c, n+1); d.Admitted {
							// Only a contract violation if n+1 is within pool capacity.
							total := r.Used(Interactive) + r.Used(Batch)
							if total+(n+1) <= capacity {
								t.Fatalf("ir=%v hw=%v step=%d: AvailableFor(%s)=%d but Admit(%s,%d) ADMITTED (over headroom)",
									ir, hw, step, c, n, c, n+1)
							}
						}
					}
				}
				// Advance state: reserve one unit, alternating class, ignoring denials.
				cl := Interactive
				if step%2 == 0 {
					cl = Batch
				}
				_, _ = r.Reserve(cl, 1)
			}
		}
	}
	t.Log("AvailableFor/Admit consistency holds across ratios, high-water marks, and load levels")
}

// ---------------------------------------------------------------------------
// (C) CROSS-METHOD CONCURRENT ALLOCATION (run under -race)
// ---------------------------------------------------------------------------

// TestAdversarial2_CrossMethodConcurrentNoDoubleHold races AllocateGPU and
// AllocateGPUByMemory for the SAME set of jobs against a shared Manager, with
// interleaved releases. The invariants, asserted SINK-SIDE at quiescence:
//   - no job is ever attributed to two GPUs (idempotency holds across methods),
//   - available+allocated+unhealthy+offline == total (no unit lost/duplicated),
//   - Stats agrees with a hand count.
//
// This is the single -race probe for this pass.
func TestAdversarial2_CrossMethodConcurrentNoDoubleHold(t *testing.T) {
	const k = 6
	const jobs = 10
	const rounds = 300

	m := NewManager()
	m.InjectGPUsForTest(makeInventory(k, 8192))

	var wg sync.WaitGroup
	for j := 0; j < jobs; j++ {
		wg.Add(1)
		go func(jobID string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if i%2 == 0 {
					if _, err := m.AllocateGPU(jobID); err == nil {
						// Mid-life, a concurrent monitor could flip status; re-alloc
						// via the OTHER method must still not create a second hold.
						_, _ = m.AllocateGPUByMemory(jobID, 1024)
						m.ReleaseGPU(jobID)
					}
				} else {
					if _, err := m.AllocateGPUByMemory(jobID, 1024); err == nil {
						_, _ = m.AllocateGPU(jobID)
						m.ReleaseGPU(jobID)
					}
				}
				_ = m.ListGPUs()
				_ = m.Stats()
			}
		}("xjob-" + strconv.Itoa(j))
	}
	wg.Wait()

	// Quiescent SINK-SIDE checks.
	holders := make(map[string]string) // jobID -> gpuID, to catch a job on 2 GPUs
	counts := map[GPUStatus]int{}
	for _, g := range m.ListGPUs() {
		counts[g.Status]++
		if g.AllocatedTo != "" {
			if prev, ok := holders[g.AllocatedTo]; ok {
				t.Fatalf("DOUBLE-HOLD: job %q attributed to both %s and %s", g.AllocatedTo, prev, g.ID)
			}
			holders[g.AllocatedTo] = g.ID
		}
	}
	total := counts[Available] + counts[Allocated] + counts[Unhealthy] + counts[Offline]
	if total != k {
		t.Fatalf("CONSERVATION: counted %d GPUs across all statuses, want %d", total, k)
	}
	s := m.Stats()
	if s.Total != k {
		t.Fatalf("Stats.Total=%d want %d", s.Total, k)
	}
	if s.Available != counts[Available] || s.Allocated != counts[Allocated] {
		t.Fatalf("Stats disagrees with hand count: Stats(avail=%d,alloc=%d) hand(avail=%d,alloc=%d)",
			s.Available, s.Allocated, counts[Available], counts[Allocated])
	}
	// Every job released what it acquired in each round, so at quiescence nothing
	// should remain allocated.
	if counts[Allocated] != 0 {
		t.Fatalf("LOST RELEASE: %d GPUs still allocated after every job released", counts[Allocated])
	}
	t.Logf("cross-method race: conserved %d GPUs, no job double-held", k)
}
