package build

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// waitForTerminal polls svc.Get until the job reaches a terminal state or the
// deadline elapses. It replaces fragile fixed time.Sleep calls: it asserts on a
// real observed terminal state rather than assuming the simulated 100ms build
// fits inside an arbitrary sleep window.
func waitForTerminal(t *testing.T, svc *Service, id string, timeout time.Duration) *Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := svc.Get(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.IsTerminal() {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := svc.Get(id)
	t.Fatalf("job %s did not reach terminal state within %v (state=%s)", id, timeout, got.State)
	return nil
}

func TestJobStateTransitions(t *testing.T) {
	j := &Job{ID: "test-1", State: StateQueued}

	if err := j.TransitionTo(StateRunning); err != nil {
		t.Fatalf("queued->running: %v", err)
	}
	if j.State != StateRunning {
		t.Errorf("state = %s, want running", j.State)
	}
	if j.StartedAt == nil {
		t.Error("started_at not set")
	}

	if err := j.TransitionTo(StateSucceeded); err != nil {
		t.Fatalf("running->succeeded: %v", err)
	}
	if j.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded", j.State)
	}
	if j.CompletedAt == nil {
		t.Error("completed_at not set")
	}

	if err := j.TransitionTo(StateFailed); err == nil {
		t.Error("terminal transition should fail")
	}
}

func TestJobInvalidTransitions(t *testing.T) {
	cases := []struct {
		from, to State
		valid    bool
	}{
		{StateQueued, StateRunning, true},
		{StateQueued, StateCancelled, true},
		{StateQueued, StateSucceeded, false},
		{StateRunning, StateSucceeded, true},
		{StateRunning, StateFailed, true},
		{StateRunning, StateCancelled, true},
		{StateRunning, StateQueued, false},
	}

	for _, tc := range cases {
		j := &Job{ID: "test", State: tc.from}
		err := j.TransitionTo(tc.to)
		if tc.valid && err != nil {
			t.Errorf("%s->%s should be valid: %v", tc.from, tc.to, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("%s->%s should be invalid", tc.from, tc.to)
		}
	}
}

// TestSimulatedBuilderIsMarkedNonProduction documents and locks in that the
// default builder is the NON-PRODUCTION simulation. If someone makes the
// default a real builder, this assertion must be revisited deliberately.
func TestSimulatedBuilderIsMarkedNonProduction(t *testing.T) {
	if !(simulatedBuilder{}).Simulated() {
		t.Fatal("simulatedBuilder must report Simulated()==true")
	}
}

// TestServiceSubmitAndGet_Simulated exercises the orchestration pipeline (queue,
// worker, state machine) against the SIMULATED builder.
//
// HONESTY NOTE (PCS-6 / CLAUDE-1): This test asserts ONLY what the simulation
// truly guarantees — that a submitted job is picked up by a worker and driven
// to the terminal StateSucceeded of the SIMULATION. It deliberately does NOT
// assert that any real image, layer, digest, or registry artifact exists,
// because the default builder produces none. The fabricated ImageTag is checked
// only as a property of the simulation's output format, NOT as proof of a real
// image. Real-image proof lives in the //go:build integration skeleton.
func TestServiceSubmitAndGet_Simulated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(2)
	svc.Start(ctx)
	defer svc.Stop()

	j := &Job{ID: "build-1", RepoURL: "https://github.com/test/repo", Ref: "main"}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	got := waitForTerminal(t, svc, "build-1", 2*time.Second)

	// The simulation guarantees: worker ran the job to a terminal success.
	if got.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded (simulation)", got.State)
	}
	// Worker-set timestamps prove the Service pipeline (not a direct
	// TransitionTo) drove the lifecycle.
	if got.StartedAt == nil {
		t.Error("started_at not set by worker")
	}
	if got.CompletedAt == nil {
		t.Error("completed_at not set by worker")
	}
	if len(got.GetLogs()) == 0 {
		t.Error("no logs captured by simulation")
	}

	// The fabricated tag is asserted ONLY as the simulation's output shape.
	// This is NOT evidence that an image exists or is pullable.
	wantTag := fmt.Sprintf("helix/%s:%s", j.ID, j.Ref)
	if got.ImageTag != wantTag {
		t.Errorf("simulated image_tag = %q, want %q", got.ImageTag, wantTag)
	}
	// Explicitly document the boundary: do NOT treat the tag as a real image.
	// (No registry/Docker assertion is made here on purpose.)
}

func TestServiceSubmitDuplicate(t *testing.T) {
	svc := NewService(1)
	j := &Job{ID: "dup", RepoURL: "url"}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if err := svc.Submit(j); err == nil {
		t.Error("duplicate submit should fail")
	}
}

func TestServiceCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(1)
	svc.Start(ctx)
	defer svc.Stop()

	j := &Job{ID: "cancel-me", RepoURL: "url"}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if err := svc.Cancel("cancel-me"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	got, _ := svc.Get("cancel-me")
	if got.State != StateCancelled {
		t.Errorf("state = %s, want cancelled", got.State)
	}
}

// TestServiceFailedBuild_Simulated drives the SIMULATED failure path via the
// sentinel RepoURL "fail".
//
// HONESTY NOTE: This proves only that the simulation's failure branch reaches
// StateFailed through the worker pipeline. It does NOT cover real build-failure
// modes (bad Dockerfile, missing ref, compiler error, OOM) — those belong to
// the real Builder and its integration test.
func TestServiceFailedBuild_Simulated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(1)
	svc.Start(ctx)
	defer svc.Stop()

	j := &Job{ID: "fail-build", RepoURL: "fail", Ref: "main"}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	got := waitForTerminal(t, svc, "fail-build", 2*time.Second)
	if got.State != StateFailed {
		t.Errorf("state = %s, want failed (simulation sentinel)", got.State)
	}
	if got.ImageTag != "" {
		t.Errorf("failed build must not set image_tag, got %q", got.ImageTag)
	}
}

// TestServiceList_Simulated actually starts the workers and waits for every job
// to reach a real terminal success state of the simulation before asserting.
// (The previous version never called Start(), so jobs sat queued forever and
// the test only counted map size — it would have passed even if the build path
// were deleted.)
func TestServiceList_Simulated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(2)
	svc.Start(ctx)
	defer svc.Stop()

	const n = 3
	for i := 0; i < n; i++ {
		if err := svc.Submit(&Job{ID: fmt.Sprintf("job-%d", i), RepoURL: "url"}); err != nil {
			t.Fatalf("submit job-%d: %v", i, err)
		}
	}

	for i := 0; i < n; i++ {
		got := waitForTerminal(t, svc, fmt.Sprintf("job-%d", i), 2*time.Second)
		if got.State != StateSucceeded {
			t.Errorf("job-%d state = %s, want succeeded", i, got.State)
		}
	}

	jobs := svc.List()
	if len(jobs) != n {
		t.Errorf("len(jobs) = %d, want %d", len(jobs), n)
	}
}

// TestServiceConcurrentSubmits_Simulated starts the workers, checks every
// Submit return value (instead of discarding it), and asserts that all 10 jobs
// reached the terminal StateSucceeded of the simulation.
func TestServiceConcurrentSubmits_Simulated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(4)
	svc.Start(ctx)
	defer svc.Stop()

	const n = 10
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(k int) {
			j := &Job{ID: fmt.Sprintf("concurrent-%d", k), RepoURL: "url"}
			errs <- svc.Submit(j)
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("submit error: %v", err)
		}
	}

	for i := 0; i < n; i++ {
		got := waitForTerminal(t, svc, fmt.Sprintf("concurrent-%d", i), 3*time.Second)
		if got.State != StateSucceeded {
			t.Errorf("concurrent-%d state = %s, want succeeded", i, got.State)
		}
	}

	jobs := svc.List()
	if len(jobs) != n {
		t.Errorf("len(jobs) = %d, want %d", len(jobs), n)
	}
}

// TestServiceQueueFull asserts the real backpressure path: filling the queue
// beyond its capacity surfaces "build queue is full" rather than silently
// dropping work. Workers are intentionally NOT started so nothing drains the
// queue.
func TestServiceQueueFull(t *testing.T) {
	svc := NewService(1) // not Start()ed; queue never drains

	// Queue capacity is 100.
	for i := 0; i < 100; i++ {
		if err := svc.Submit(&Job{ID: fmt.Sprintf("q-%d", i), RepoURL: "url"}); err != nil {
			t.Fatalf("submit q-%d unexpectedly failed: %v", i, err)
		}
	}
	err := svc.Submit(&Job{ID: "overflow", RepoURL: "url"})
	if err == nil {
		t.Fatal("expected queue-full error on the 101st submit")
	}
}
