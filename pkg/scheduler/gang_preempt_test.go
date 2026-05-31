package scheduler

import (
	"testing"
)

// ---------- gang scheduling tests ----------

// TestGangScheduleAllPlaced verifies that when all N tasks fit, all are placed.
func TestGangScheduleAllPlaced(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})
	s.RegisterNode(&Node{ID: "n2", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	jobs := []*Job{
		{ID: "g-0", Resources: Resources{CPU: 2, Memory: 1024}, Priority: 5},
		{ID: "g-1", Resources: Resources{CPU: 2, Memory: 1024}, Priority: 5},
	}

	res, err := s.ScheduleGang(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Placements) != 2 {
		t.Fatalf("expected 2 placements, got %d", len(res.Placements))
	}
	for _, j := range jobs {
		if j.Status != JobStatusScheduled {
			t.Fatalf("expected job %s scheduled, got %s", j.ID, j.Status)
		}
	}
}

// TestGangScheduleAtomicNoPartial is the mutation-paired test for gang scheduling:
// if ANY task cannot be placed, NONE are placed and node resources are unchanged.
func TestGangScheduleAtomicNoPartial(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	// Only enough capacity for ONE of the two tasks.
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 3, Memory: 4096}})

	jobs := []*Job{
		{ID: "g-0", Resources: Resources{CPU: 2, Memory: 1024}, Priority: 5},
		{ID: "g-1", Resources: Resources{CPU: 2, Memory: 1024}, Priority: 5},
	}

	_, err := s.ScheduleGang(jobs)
	if err == nil {
		t.Fatal("expected gang scheduling to fail when not all tasks fit")
	}

	// Mutation guard: NO partial placement. Node resources must be untouched.
	n, _ := s.GetNode("n1")
	if n.AvailableResources.CPU != 3 {
		t.Fatalf("partial placement detected: CPU should be 3 (untouched), got %f", n.AvailableResources.CPU)
	}
	if n.AvailableResources.Memory != 4096 {
		t.Fatalf("partial placement detected: Memory should be 4096 (untouched), got %d", n.AvailableResources.Memory)
	}
	// No job should be left in scheduled state.
	for _, j := range jobs {
		if j.Status == JobStatusScheduled {
			t.Fatalf("partial placement: job %s should not be scheduled", j.ID)
		}
	}
	// No running placements should be recorded.
	if got := len(s.RunningPlacements()); got != 0 {
		t.Fatalf("partial placement: expected 0 running placements, got %d", got)
	}
}

// TestGangScheduleSpreadAcrossNodes ensures two tasks needing the same node's
// full capacity are spread atomically across distinct nodes when one node
// cannot hold both.
func TestGangScheduleSpreadAcrossNodes(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})
	s.RegisterNode(&Node{ID: "n2", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	// Each task needs all 4 CPU; two tasks => must land on two different nodes.
	jobs := []*Job{
		{ID: "g-0", Resources: Resources{CPU: 4, Memory: 1024}, Priority: 5},
		{ID: "g-1", Resources: Resources{CPU: 4, Memory: 1024}, Priority: 5},
	}

	res, err := s.ScheduleGang(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range res.Placements {
		seen[p.NodeID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("expected tasks spread across 2 nodes, got %v", seen)
	}
}

// TestGangScheduleEmpty rejects an empty gang.
func TestGangScheduleEmpty(t *testing.T) {
	s := NewScheduler()
	if _, err := s.ScheduleGang(nil); err == nil {
		t.Fatal("expected error for empty gang")
	}
}

// TestGangScheduleVersionIncrementsOnce confirms a successful gang bumps the
// version exactly once (atomic transaction), not once per task.
func TestGangScheduleVersionIncrementsOnce(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 8, Memory: 8192}})

	v1 := s.Version()
	jobs := []*Job{
		{ID: "g-0", Resources: Resources{CPU: 1, Memory: 256}, Priority: 5},
		{ID: "g-1", Resources: Resources{CPU: 1, Memory: 256}, Priority: 5},
		{ID: "g-2", Resources: Resources{CPU: 1, Memory: 256}, Priority: 5},
	}
	if _, err := s.ScheduleGang(jobs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.Version(); got != v1+1 {
		t.Fatalf("expected version to increment exactly once, %d -> %d", v1, got)
	}
}

// TestGangScheduleFailureNoVersionChange confirms a failed gang does not bump version.
func TestGangScheduleFailureNoVersionChange(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 1, Memory: 1024}})

	v1 := s.Version()
	jobs := []*Job{
		{ID: "g-0", Resources: Resources{CPU: 1, Memory: 256}, Priority: 5},
		{ID: "g-1", Resources: Resources{CPU: 1, Memory: 256}, Priority: 5},
	}
	if _, err := s.ScheduleGang(jobs); err == nil {
		t.Fatal("expected failure")
	}
	if got := s.Version(); got != v1 {
		t.Fatalf("failed gang must not change version, %d -> %d", v1, got)
	}
}

// ---------- preemption tests ----------

// TestPreemptEvictsLowerPriority verifies a high-priority job evicts a
// lower-priority running placement to make room.
func TestPreemptEvictsLowerPriority(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 2, Memory: 2048}})

	// Place a low-priority job that consumes the whole node.
	low := &Job{ID: "low", Resources: Resources{CPU: 2, Memory: 2048}, Priority: 1}
	if _, err := s.Schedule(low); err != nil {
		t.Fatalf("seed schedule failed: %v", err)
	}

	// A plain schedule for the high-priority job must fail (no room).
	high := &Job{ID: "high", Resources: Resources{CPU: 2, Memory: 2048}, Priority: 10}
	if _, err := s.Schedule(high); err == nil {
		t.Fatal("expected plain schedule to fail (node full)")
	}

	// Preemptive schedule must succeed by evicting the low-priority job.
	res, err := s.SchedulePreempt(high)
	if err != nil {
		t.Fatalf("preemptive schedule failed: %v", err)
	}
	if res.AssignedNode != "n1" {
		t.Fatalf("expected high placed on n1, got %s", res.AssignedNode)
	}
	if len(res.Preempted) != 1 || res.Preempted[0] != "low" {
		t.Fatalf("expected 'low' to be preempted, got %v", res.Preempted)
	}
	// Victim must be marked pending (evicted) and no longer running.
	if low.Status != JobStatusPending {
		t.Fatalf("expected victim status pending, got %s", low.Status)
	}
	if s.IsRunning("low") {
		t.Fatal("victim should no longer be a running placement")
	}
	if !s.IsRunning("high") {
		t.Fatal("high-priority job should now be running")
	}
}

// TestPreemptEvictsLowestPriorityVictimOnly is the mutation-paired test:
// preemption must evict the LOWEST-priority victim(s) needed and NOT touch
// higher-priority running jobs. Here only the lowest of several must go.
func TestPreemptEvictsLowestPriorityVictimOnly(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 3, Memory: 3072}})

	// Three running jobs, each 1 CPU, priorities 1, 2, 3. Node is full.
	j1 := &Job{ID: "p1", Resources: Resources{CPU: 1, Memory: 1024}, Priority: 1}
	j2 := &Job{ID: "p2", Resources: Resources{CPU: 1, Memory: 1024}, Priority: 2}
	j3 := &Job{ID: "p3", Resources: Resources{CPU: 1, Memory: 1024}, Priority: 3}
	for _, j := range []*Job{j1, j2, j3} {
		if _, err := s.Schedule(j); err != nil {
			t.Fatalf("seed schedule %s failed: %v", j.ID, err)
		}
	}

	// Incoming priority-5 job needs 1 CPU; only ONE victim required.
	high := &Job{ID: "high", Resources: Resources{CPU: 1, Memory: 1024}, Priority: 5}
	res, err := s.SchedulePreempt(high)
	if err != nil {
		t.Fatalf("preempt failed: %v", err)
	}

	// Exactly one victim, and it MUST be the lowest priority (p1).
	if len(res.Preempted) != 1 {
		t.Fatalf("expected exactly 1 victim, got %v", res.Preempted)
	}
	if res.Preempted[0] != "p1" {
		t.Fatalf("expected lowest-priority victim 'p1', got %s", res.Preempted[0])
	}
	// p2 and p3 (higher priority) MUST still be running.
	if !s.IsRunning("p2") || !s.IsRunning("p3") {
		t.Fatal("higher-priority jobs p2/p3 must not be evicted")
	}
	if s.IsRunning("p1") {
		t.Fatal("lowest-priority p1 must be evicted")
	}
}

// TestPreemptRefusesEqualOrHigherPriority confirms preemption never evicts a
// job of equal or higher priority than the incoming job.
func TestPreemptRefusesEqualOrHigherPriority(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 2, Memory: 2048}})

	// Running job at SAME priority as incoming; must not be evicted.
	running := &Job{ID: "peer", Resources: Resources{CPU: 2, Memory: 2048}, Priority: 5}
	if _, err := s.Schedule(running); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	incoming := &Job{ID: "incoming", Resources: Resources{CPU: 2, Memory: 2048}, Priority: 5}
	if _, err := s.SchedulePreempt(incoming); err == nil {
		t.Fatal("expected preemption to fail: cannot evict equal-priority job")
	}
	if !s.IsRunning("peer") {
		t.Fatal("equal-priority peer must remain running")
	}
	if s.IsRunning("incoming") {
		t.Fatal("incoming must not be placed when no valid victims exist")
	}
}

// TestPreemptRestoresVictimResources is a mutation guard proving evicted
// resources are actually returned so the incoming job fits.
func TestPreemptRestoresVictimResources(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	low := &Job{ID: "low", Resources: Resources{CPU: 4, Memory: 4096}, Priority: 1}
	if _, err := s.Schedule(low); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	// Node now fully consumed.
	n, _ := s.GetNode("n1")
	if n.AvailableResources.CPU != 0 {
		t.Fatalf("precondition: node should be full, got CPU %f", n.AvailableResources.CPU)
	}

	high := &Job{ID: "high", Resources: Resources{CPU: 3, Memory: 2048}, Priority: 10}
	if _, err := s.SchedulePreempt(high); err != nil {
		t.Fatalf("preempt failed: %v", err)
	}
	// After evicting low (frees 4 CPU) and placing high (3 CPU), 1 CPU remains.
	n2, _ := s.GetNode("n1")
	if n2.AvailableResources.CPU != 1 {
		t.Fatalf("expected 1 CPU remaining after evict+place, got %f", n2.AvailableResources.CPU)
	}
}

// TestPreemptNoEvictionWhenRoomExists confirms preemption places without
// evicting anyone if there is already room (no needless eviction).
func TestPreemptNoEvictionWhenRoomExists(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 8, Memory: 8192}})

	low := &Job{ID: "low", Resources: Resources{CPU: 1, Memory: 1024}, Priority: 1}
	if _, err := s.Schedule(low); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	high := &Job{ID: "high", Resources: Resources{CPU: 1, Memory: 1024}, Priority: 10}
	res, err := s.SchedulePreempt(high)
	if err != nil {
		t.Fatalf("preempt failed: %v", err)
	}
	if len(res.Preempted) != 0 {
		t.Fatalf("expected no eviction when room exists, evicted %v", res.Preempted)
	}
	if !s.IsRunning("low") {
		t.Fatal("low must remain running when no eviction needed")
	}
}

// TestPreemptOptimisticConflict ties preemption into the existing optimistic
// concurrency model: a stale version must conflict.
func TestPreemptOptimisticConflict(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 2, Memory: 2048}})

	low := &Job{ID: "low", Resources: Resources{CPU: 2, Memory: 2048}, Priority: 1}
	v := s.Version()
	if _, err := s.SchedulePreemptOptimistic(low, v); err != nil {
		t.Fatalf("first preempt-optimistic failed: %v", err)
	}

	high := &Job{ID: "high", Resources: Resources{CPU: 2, Memory: 2048}, Priority: 10}
	// Stale version must conflict.
	if _, err := s.SchedulePreemptOptimistic(high, v); err == nil {
		t.Fatal("expected optimistic conflict on stale version")
	}
	// Correct version should succeed (evicting low).
	if _, err := s.SchedulePreemptOptimistic(high, s.Version()); err != nil {
		t.Fatalf("preempt-optimistic with current version failed: %v", err)
	}
}

// TestScheduleRecordsRunningPlacement verifies plain Schedule now records a
// running placement usable by preemption.
func TestScheduleRecordsRunningPlacement(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	job := &Job{ID: "j1", Resources: Resources{CPU: 1, Memory: 1024}, Priority: 3}
	if _, err := s.Schedule(job); err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if !s.IsRunning("j1") {
		t.Fatal("expected j1 to be recorded as running placement")
	}
	if got := len(s.RunningPlacements()); got != 1 {
		t.Fatalf("expected 1 running placement, got %d", got)
	}
}
