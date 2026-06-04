package scheduler

import (
	"testing"
)

// ---------- placement-release (job completion) tests ----------
//
// These tests cover the completion hook that prevents the unbounded growth of
// s.placements: when a job finishes, its placement MUST be removed and the
// node's resources restored, mirroring the preemption-eviction restore path.

// TestCompleteReleasesPlacementAndRestoresResources schedules N jobs (so the
// placement map grows to N), then completes them all and asserts the placement
// map returns to baseline AND each node's resources are restored to pre-schedule
// levels.
func TestCompleteReleasesPlacementAndRestoresResources(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})

	const (
		baselineCPU = 16.0
		baselineMem = uint64(16384)
		baselineGPU = 8
	)
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: baselineCPU, Memory: baselineMem, GPU: baselineGPU}})

	const n = 5
	jobs := make([]*Job, 0, n)
	for i := 0; i < n; i++ {
		j := &Job{
			ID:        nodeJobID(i),
			Resources: Resources{CPU: 2, Memory: 1024, GPU: 1},
			Priority:  5,
		}
		if _, err := s.Schedule(j); err != nil {
			t.Fatalf("schedule job %s: %v", j.ID, err)
		}
		jobs = append(jobs, j)
	}

	// Placement map grew to N.
	if got := len(s.RunningPlacements()); got != n {
		t.Fatalf("expected %d running placements after scheduling, got %d", n, got)
	}

	// Resources were consumed.
	node, _ := s.GetNode("n1")
	if node.AvailableResources.CPU != baselineCPU-(2*n) {
		t.Fatalf("expected CPU %f after scheduling, got %f", baselineCPU-(2*n), node.AvailableResources.CPU)
	}

	// Complete every job.
	for _, j := range jobs {
		s.Complete(j.ID)
	}

	// Placement map returned to baseline (empty).
	if got := len(s.RunningPlacements()); got != 0 {
		t.Fatalf("expected 0 running placements after completion, got %d", got)
	}

	// Node resources fully restored to pre-schedule levels.
	node, _ = s.GetNode("n1")
	if node.AvailableResources.CPU != baselineCPU {
		t.Fatalf("CPU not restored: expected %f, got %f", baselineCPU, node.AvailableResources.CPU)
	}
	if node.AvailableResources.Memory != baselineMem {
		t.Fatalf("Memory not restored: expected %d, got %d", baselineMem, node.AvailableResources.Memory)
	}
	if node.AvailableResources.GPU != baselineGPU {
		t.Fatalf("GPU not restored: expected %d, got %d", baselineGPU, node.AvailableResources.GPU)
	}

	// Completed jobs are no longer reported as running.
	for _, j := range jobs {
		if s.IsRunning(j.ID) {
			t.Fatalf("job %s should not be running after completion", j.ID)
		}
	}
}

// TestReleaseIsAliasOfComplete verifies Release behaves identically to Complete.
func TestReleaseIsAliasOfComplete(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	j := &Job{ID: "j-rel", Resources: Resources{CPU: 2, Memory: 1024}, Priority: 5}
	if _, err := s.Schedule(j); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if !s.IsRunning(j.ID) {
		t.Fatalf("job should be running after schedule")
	}

	released := s.Release(j.ID)
	if !released {
		t.Fatalf("Release should report true for a known running placement")
	}
	if s.IsRunning(j.ID) {
		t.Fatalf("job should not be running after Release")
	}
	node, _ := s.GetNode("n1")
	if node.AvailableResources.CPU != 4 || node.AvailableResources.Memory != 4096 {
		t.Fatalf("resources not restored after Release: %+v", node.AvailableResources)
	}
}

// TestReleaseUnknownIsNoOp verifies releasing an unknown / already-released id is
// a safe no-op (idempotent) and leaves node resources untouched.
func TestReleaseUnknownIsNoOp(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 4, Memory: 4096}})

	// Unknown id: no panic, returns false, resources untouched.
	if s.Release("does-not-exist") {
		t.Fatalf("Release of unknown id should return false")
	}
	if s.Complete("does-not-exist") {
		t.Fatalf("Complete of unknown id should return false")
	}
	node, _ := s.GetNode("n1")
	if node.AvailableResources.CPU != 4 || node.AvailableResources.Memory != 4096 {
		t.Fatalf("resources changed by no-op release: %+v", node.AvailableResources)
	}

	// Double-release: second release of an already-released job is a no-op.
	j := &Job{ID: "j-dbl", Resources: Resources{CPU: 2, Memory: 1024}, Priority: 5}
	if _, err := s.Schedule(j); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if !s.Release(j.ID) {
		t.Fatalf("first Release should return true")
	}
	if s.Release(j.ID) {
		t.Fatalf("second Release should be a no-op returning false")
	}
	node, _ = s.GetNode("n1")
	if node.AvailableResources.CPU != 4 || node.AvailableResources.Memory != 4096 {
		t.Fatalf("double-release double-restored resources: %+v", node.AvailableResources)
	}
}

// TestCompleteDoesNotAffectOtherActiveJobs ensures completing one job leaves
// other active placements and their resource accounting intact.
func TestCompleteDoesNotAffectOtherActiveJobs(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{ID: "n1", AvailableResources: Resources{CPU: 10, Memory: 8192}})

	a := &Job{ID: "a", Resources: Resources{CPU: 2, Memory: 1024}, Priority: 5}
	b := &Job{ID: "b", Resources: Resources{CPU: 3, Memory: 2048}, Priority: 5}
	if _, err := s.Schedule(a); err != nil {
		t.Fatalf("schedule a: %v", err)
	}
	if _, err := s.Schedule(b); err != nil {
		t.Fatalf("schedule b: %v", err)
	}

	s.Complete(a.ID)

	if s.IsRunning(a.ID) {
		t.Fatalf("a should be completed")
	}
	if !s.IsRunning(b.ID) {
		t.Fatalf("b should remain running")
	}
	// Node should reflect: only b's resources still consumed.
	node, _ := s.GetNode("n1")
	if node.AvailableResources.CPU != 10-3 {
		t.Fatalf("expected CPU %f (only b consumed), got %f", 10.0-3.0, node.AvailableResources.CPU)
	}
	if node.AvailableResources.Memory != 8192-2048 {
		t.Fatalf("expected Memory %d (only b consumed), got %d", uint64(8192-2048), node.AvailableResources.Memory)
	}
}

func nodeJobID(i int) string {
	return "c-" + string(rune('0'+i))
}
