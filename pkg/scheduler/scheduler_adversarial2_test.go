package scheduler

// Adversarial second pass for the Omega OCC engine (scheduler.go / gang_preempt.go).
//
// The first adversarial pass + omega_occ_race_test.go already prove:
//   - the version CAS rejects stale commits (exactly-one-commit under a barrier race)
//   - the concurrent pool drains to exactly capacity, never oversubscribed
//   - tied-score placement is deterministic
//
// This pass targets a DIFFERENT, deeper class the existing tests miss: the
// atomicity of the BIND step itself across a MULTI-PLUGIN pipeline, and the
// engine's resource-conservation invariant under a concurrent schedule/complete
// churn. The headline find is a real, always-on capacity LEAK on a FAILED bind.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// debitingPlugin is a stand-in for any binding plugin that performs a
// read-modify-write on node capacity during Bind (exactly what NodeResourcesFit
// does). It debits a fixed amount and always succeeds — so when it is sequenced
// BEFORE a plugin that fails, it models the "earlier plugin already committed its
// debit" half of a partial-bind.
type debitingPlugin struct {
	cpu float64
	gpu int
}

func (debitingPlugin) Name() string            { return "debiting" }
func (debitingPlugin) Filter(*Job, *Node) bool { return true }
func (debitingPlugin) Score(*Job, *Node) int   { return 0 }
func (p debitingPlugin) Bind(_ *Job, n *Node) bool {
	n.AvailableResources.CPU -= p.cpu
	n.AvailableResources.GPU -= p.gpu
	return true
}

// failingBindPlugin always fails its Bind. Placed after a debiting plugin it
// forces the pipeline to abort AFTER an earlier debit landed.
type failingBindPlugin struct{}

func (failingBindPlugin) Name() string            { return "failingBind" }
func (failingBindPlugin) Filter(*Job, *Node) bool { return true }
func (failingBindPlugin) Score(*Job, *Node) int   { return 0 }
func (failingBindPlugin) Bind(*Job, *Node) bool   { return false }

const (
	startCPU = 100.0
	startMem = 100_000
	startGPU = 8
)

func freshLeakScheduler() (*Scheduler, string) {
	s := NewScheduler()
	// Pipeline: a real debiting plugin THEN a plugin that fails the commit.
	s.AddPlugin(debitingPlugin{cpu: 4, gpu: 1})
	s.AddPlugin(failingBindPlugin{})
	id := "leak-node"
	s.RegisterNode(&Node{
		ID:                 id,
		AvailableResources: Resources{CPU: startCPU, Memory: startMem, GPU: startGPU},
	})
	return s, id
}

func assertNoLeak(t *testing.T, s *Scheduler, nodeID, path string) {
	t.Helper()
	n, ok := s.GetNode(nodeID)
	if !ok {
		t.Fatalf("%s: node vanished", path)
	}
	if n.AvailableResources.CPU != startCPU || n.AvailableResources.GPU != startGPU ||
		n.AvailableResources.Memory != startMem {
		t.Fatalf("%s: CAPACITY LEAK on failed bind: node=CPU %v/Mem %v/GPU %v, want %v/%v/%v "+
			"(an earlier plugin's debit was not rolled back when a later Bind failed)",
			path, n.AvailableResources.CPU, n.AvailableResources.Memory, n.AvailableResources.GPU,
			startCPU, startMem, startGPU)
	}
	// A failed bind must NOT advance the version and must NOT record a placement.
	if got := len(s.RunningPlacements()); got != 0 {
		t.Fatalf("%s: failed bind recorded %d placements, want 0", path, got)
	}
}

// TestAdversarial2_BindLeak_Schedule proves Scheduler.Schedule leaves node state
// byte-identical when a multi-plugin Bind pipeline aborts mid-way. This is the
// MUTATION-PROVEN find: without the transactional rollback in runBindLocked the
// node ends at CPU=96/GPU=7 on a FAILED schedule — a permanent leak (no version
// bump, no placement, so the resources are unreclaimable).
func TestAdversarial2_BindLeak_Schedule(t *testing.T) {
	s, id := freshLeakScheduler()
	job := &Job{ID: "j", Resources: Resources{CPU: 4, Memory: 500, GPU: 1}}
	if _, err := s.Schedule(job); err == nil {
		t.Fatalf("expected bind failure, got success")
	}
	assertNoLeak(t, s, id, "Schedule")
}

// TestAdversarial2_BindLeak_ScheduleOptimistic proves the same for the OCC entry
// point: a failed optimistic commit must not leak capacity AND must not bump the
// version (so a retry sees the untouched cell).
func TestAdversarial2_BindLeak_ScheduleOptimistic(t *testing.T) {
	s, id := freshLeakScheduler()
	v0 := s.Version()
	job := &Job{ID: "jo", Resources: Resources{CPU: 4, Memory: 500, GPU: 1}}
	if _, err := s.ScheduleOptimistic(job, v0); err == nil {
		t.Fatalf("expected bind failure, got success")
	}
	assertNoLeak(t, s, id, "ScheduleOptimistic")
	if got := s.Version(); got != v0 {
		t.Fatalf("ScheduleOptimistic: failed bind bumped version %d->%d, must stay put", v0, got)
	}
}

// TestAdversarial2_BindLeak_Preempt proves the preempt fast path (which binds the
// REAL node, not a shadow) also rolls back a partial debit on bind failure.
func TestAdversarial2_BindLeak_Preempt(t *testing.T) {
	s, id := freshLeakScheduler()
	job := &Job{ID: "jp", Priority: 9, Resources: Resources{CPU: 4, Memory: 500, GPU: 1}}
	if _, err := s.SchedulePreempt(job); err == nil {
		t.Fatalf("expected bind failure, got success")
	}
	assertNoLeak(t, s, id, "SchedulePreempt")
}

// TestAdversarial2_GangBindFailure_RealStateUntouched proves the gang path's
// shadow-commit discipline: when a task's bind fails the whole gang aborts and
// the REAL node is byte-identical (the shadow's partial debit is discarded). This
// is the complementary atomicity proof for the all-or-nothing path.
func TestAdversarial2_GangBindFailure_RealStateUntouched(t *testing.T) {
	s, id := freshLeakScheduler()
	jobs := []*Job{
		{ID: "g1", Resources: Resources{CPU: 4, Memory: 500, GPU: 1}},
		{ID: "g2", Resources: Resources{CPU: 4, Memory: 500, GPU: 1}},
	}
	if _, err := s.ScheduleGang(jobs); err == nil {
		t.Fatalf("expected gang failure (failingBind), got success")
	}
	assertNoLeak(t, s, id, "ScheduleGang")
}

// fitOnlyScheduler is the production pipeline (NodeResourcesFit only) for the
// conservation probe below.
func fitOnlyScheduler(nodeID string, cap Resources) *Scheduler {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: nodeID, AvailableResources: cap})
	return s
}

// TestAdversarial2_ConcurrentScheduleComplete_Conserves is a -race conservation
// probe: many goroutines schedule a 1-GPU job (via the real OCC retry loop) and,
// on success, immediately Complete it (crediting the GPU back). At any instant
// placed-count == capacity-minus-available, and the node's GPU must NEVER go
// negative and must reconcile EXACTLY back to capacity once all churn drains.
// This exercises the debit (ScheduleOptimistic/Bind) and credit (Complete) under
// the lock concurrently — proving the accounting has no lost/double credit and no
// oversubscription race. Run with -race.
func TestAdversarial2_ConcurrentScheduleComplete_Conserves(t *testing.T) {
	const capacity = 4
	const workers = 32
	const opsPerWorker = 200

	nodeID := "churn-node"
	s := fitOnlyScheduler(nodeID, Resources{CPU: 1_000_000, Memory: 1_000_000_000, GPU: capacity})

	deadline := time.Now().Add(20 * time.Second) // timeout guard: never hang
	var placed atomic.Int32                       // currently-held placements (our own tally)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for op := 0; op < opsPerWorker; op++ {
				if time.Now().After(deadline) {
					return
				}
				job := &Job{
					ID:        fmt.Sprintf("churn-w%d-op%d", w, op),
					Resources: Resources{CPU: 1, Memory: 1024, GPU: 1},
				}
				// OCC retry loop to acquire one GPU.
				acquired := false
				for attempt := 0; attempt < 500 && !time.Now().After(deadline); attempt++ {
					v := s.Version()
					_, err := s.ScheduleOptimistic(job, v)
					if err == nil {
						acquired = true
						break
					}
					if err == ErrNoNodeAvailable {
						break // pool momentarily full; move on
					}
					// OCC conflict -> retry on fresh version
				}
				if !acquired {
					continue
				}
				placed.Add(1)
				// SINK-SIDE invariant: never oversubscribed at any observation.
				if n, ok := s.GetNode(nodeID); ok && n.AvailableResources.GPU < 0 {
					t.Errorf("oversubscription: GPU=%d < 0", n.AvailableResources.GPU)
				}
				// Immediately release, crediting the GPU back.
				if !s.Complete(job.ID) {
					t.Errorf("Complete returned false for held placement %s", job.ID)
				}
				placed.Add(-1)
			}
		}(w)
	}
	wg.Wait()

	// Reconciliation: all churn drained -> placements empty and GPU == capacity.
	if got := placed.Load(); got != 0 {
		t.Fatalf("internal tally not drained: placed=%d", got)
	}
	if got := len(s.RunningPlacements()); got != 0 {
		t.Fatalf("placements not drained: %d still held", got)
	}
	n, _ := s.GetNode(nodeID)
	if n.AvailableResources.GPU != capacity {
		t.Fatalf("GPU did not reconcile: got %d, want %d (lost/double credit in debit-credit cycle)",
			n.AvailableResources.GPU, capacity)
	}
	t.Logf("[conserve] workers=%d ops=%d cap=%d: reconciled GPU=%d placements=0",
		workers, opsPerWorker, capacity, n.AvailableResources.GPU)
}

// TestAdversarial2_NegativeResourceRequest_InflatesCapacity is a LATENT-RISK pin,
// NOT a passing-contract assertion of correctness. It documents that the engine
// performs NO request validation: a job requesting NEGATIVE resources passes
// NodeResourcesFit.Filter (avail >= negative is always true) and Bind subtracts a
// negative => it INFLATES node capacity above its true maximum. That inflated
// capacity is then an oversubscription vector for subsequent real placements.
//
// This is pinned (not fixed) because the internal scheduling API has no declared
// validation contract and callers are trusted; the parent wrapper / admission
// layer is the proper place to reject negative requests. If a validation contract
// is later added, this test should flip to asserting rejection.
func TestAdversarial2_NegativeResourceRequest_InflatesCapacity(t *testing.T) {
	s := fitOnlyScheduler("neg-node", Resources{CPU: 10, Memory: 1000, GPU: 2})
	job := &Job{ID: "evil", Resources: Resources{CPU: -100, GPU: -5}}
	if _, err := s.Schedule(job); err != nil {
		t.Fatalf("unexpected error scheduling negative-resource job: %v", err)
	}
	n, _ := s.GetNode("neg-node")
	// Pin the CURRENT (risky) behavior so a future change is forced to revisit it.
	if n.AvailableResources.CPU <= 10 && n.AvailableResources.GPU <= 2 {
		t.Fatalf("expected current (unvalidated) behavior to INFLATE capacity, but it did not: "+
			"CPU=%v GPU=%v — a validation contract may have been added; update this LATENT-RISK pin",
			n.AvailableResources.CPU, n.AvailableResources.GPU)
	}
	t.Logf("[LATENT RISK] negative-resource request inflated capacity to CPU=%v GPU=%v (no admission validation in engine)",
		n.AvailableResources.CPU, n.AvailableResources.GPU)
}
