package build

import (
	"context"
	"fmt"
	"testing"
	"time"
)

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

func TestServiceSubmitAndGet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(2)
	svc.Start(ctx)
	defer svc.Stop()

	j := &Job{ID: "build-1", RepoURL: "https://github.com/test/repo", Ref: "main"}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	got, err := svc.Get("build-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded", got.State)
	}
	if got.ImageTag == "" {
		t.Error("image_tag not set")
	}
	if len(got.GetLogs()) == 0 {
		t.Error("no logs captured")
	}
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

func TestServiceFailedBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(1)
	svc.Start(ctx)
	defer svc.Stop()

	j := &Job{ID: "fail-build", RepoURL: "fail", Ref: "main"}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	got, _ := svc.Get("fail-build")
	if got.State != StateFailed {
		t.Errorf("state = %s, want failed", got.State)
	}
}

func TestServiceList(t *testing.T) {
	svc := NewService(1)
	for i := 0; i < 3; i++ {
		svc.Submit(&Job{ID: fmt.Sprintf("job-%d", i), RepoURL: "url"})
	}
	jobs := svc.List()
	if len(jobs) != 3 {
		t.Errorf("len(jobs) = %d, want 3", len(jobs))
	}
}

func TestServiceConcurrentSubmits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(4)
	svc.Start(ctx)
	defer svc.Stop()

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			j := &Job{ID: fmt.Sprintf("concurrent-%d", n), RepoURL: "url"}
			svc.Submit(j)
			done <- true
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	time.Sleep(500 * time.Millisecond)
	jobs := svc.List()
	if len(jobs) != 10 {
		t.Errorf("len(jobs) = %d, want 10", len(jobs))
	}
}
