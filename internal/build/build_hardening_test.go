package build

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// fakeOrchestratorBuilder — unit-test helper
//
// Drives every job to SUCCEEDED (with a fixed image tag) or FAILED without
// touching podman or any real external command. Replaces the old simulated
// runBuild so unit tests that exercise the orchestrator/state-machine remain
// hermetic even though NewOrchestrator now defaults to the real podman builder.
// ---------------------------------------------------------------------------

type fakeOrchestratorBuilder struct {
	// failRepoURL, if non-empty, makes any job with that RepoURL fail.
	failRepoURL string
	// tagFn, if set, overrides how the image tag is derived. Default: "helix/<id>:<ref>".
	tagFn func(j *Job) string
}

func (f *fakeOrchestratorBuilder) Execute(_ context.Context, j *Job) {
	if f.failRepoURL != "" && j.RepoURL == f.failRepoURL {
		j.AppendLog("Build failed: simulated failure")
		_ = j.TransitionTo(StateFailed)
		return
	}
	tag := fmt.Sprintf("helix/%s:%s", j.ID, j.Ref)
	if f.tagFn != nil {
		tag = f.tagFn(j)
	}
	j.mu.Lock()
	j.ImageTag = tag
	j.mu.Unlock()
	j.AppendLog(fmt.Sprintf("Build succeeded: image %s", tag))
	_ = j.TransitionTo(StateSucceeded)
}

// newFakeOrchestrator returns an Orchestrator wired with fakeOrchestratorBuilder.
// All existing tests that need the orchestrator to process jobs to completion
// must use this instead of NewOrchestrator (which now defaults to real podman).
func newFakeOrchestrator(workers int, c cache.Cache) *Orchestrator {
	return NewOrchestratorWithBuilder(workers, c, &fakeOrchestratorBuilder{failRepoURL: "fail"})
}

// --- Job.TransitionTo validation matrix ---

func TestJob_TransitionTo_ValidPaths(t *testing.T) {
	j := &Job{State: StateQueued}
	require.NoError(t, j.TransitionTo(StateRunning))
	assert.Equal(t, StateRunning, j.State)
	require.NotNil(t, j.StartedAt, "running transition must stamp StartedAt")

	require.NoError(t, j.TransitionTo(StateSucceeded))
	assert.Equal(t, StateSucceeded, j.State)
	require.NotNil(t, j.CompletedAt, "terminal transition must stamp CompletedAt")
}

func TestJob_TransitionTo_InvalidQueuedToSucceeded(t *testing.T) {
	j := &Job{State: StateQueued}
	err := j.TransitionTo(StateSucceeded)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid transition")
	// State must be unchanged after a rejected transition.
	assert.Equal(t, StateQueued, j.State)
	assert.Nil(t, j.CompletedAt)
}

func TestJob_TransitionTo_FromTerminalRejected(t *testing.T) {
	j := &Job{State: StateSucceeded}
	err := j.TransitionTo(StateRunning)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal state")
	assert.Equal(t, StateSucceeded, j.State)
}

func TestJob_TransitionTo_QueuedToCancelled(t *testing.T) {
	j := &Job{State: StateQueued}
	require.NoError(t, j.TransitionTo(StateCancelled))
	assert.Equal(t, StateCancelled, j.State)
	require.NotNil(t, j.CompletedAt, "cancel must stamp CompletedAt")
	assert.Nil(t, j.StartedAt, "cancel from queued must not stamp StartedAt")
}

// --- Job.clone isolation (deep copy) ---

func TestJob_Clone_IsolatesLogsAndArgs(t *testing.T) {
	start := time.Now().UTC()
	orig := &Job{
		ID:        "j1",
		State:     StateRunning,
		BuildArgs: map[string]string{"K": "v"},
		StartedAt: &start,
		Logs:      []string{"line-0"},
	}
	c := orig.clone()

	// Mutating the clone's Logs/args must not affect the original.
	c.Logs[0] = "tampered"
	c.BuildArgs["K"] = "tampered"
	assert.Equal(t, "line-0", orig.Logs[0], "clone must not share Logs slice backing array")
	assert.Equal(t, "v", orig.BuildArgs["K"], "clone must not share BuildArgs map")

	// Timestamp pointers must be distinct copies, not aliases.
	require.NotNil(t, c.StartedAt)
	assert.NotSame(t, orig.StartedAt, c.StartedAt, "StartedAt pointer aliased")
	assert.Equal(t, *orig.StartedAt, *c.StartedAt)
}

func TestJob_Clone_NilArgsStayNil(t *testing.T) {
	orig := &Job{ID: "j2", State: StateQueued}
	c := orig.clone()
	assert.Nil(t, c.BuildArgs, "nil args must clone as nil, not empty map")
}

// --- AppendLog / GetLogs concurrency and copy semantics ---

func TestJob_GetLogs_ReturnsCopy(t *testing.T) {
	j := &Job{}
	j.AppendLog("a")
	logs := j.GetLogs()
	logs[0] = "mutated"
	assert.Equal(t, "a", j.GetLogs()[0], "GetLogs must return an independent copy")
}

func TestJob_AppendLog_ConcurrentSafe(t *testing.T) {
	j := &Job{}
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j.AppendLog("x")
		}()
	}
	wg.Wait()
	assert.Len(t, j.GetLogs(), n, "all concurrent appends must be retained")
}

// --- SubmitBuild validation ---

func TestOrchestrator_SubmitBuild_RequiresID(t *testing.T) {
	orch := NewOrchestrator(1, cache.NewMemoryCache())
	_, err := orch.SubmitBuild(context.Background(), BuildSpec{RepoURL: "r"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "build ID is required")
}

func TestOrchestrator_SubmitBuild_RequiresRepoURL(t *testing.T) {
	orch := NewOrchestrator(1, cache.NewMemoryCache())
	_, err := orch.SubmitBuild(context.Background(), BuildSpec{ID: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo URL is required")
}

func TestOrchestrator_SubmitBuild_DuplicateRejected(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache())
	ctx := context.Background()
	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "dup", RepoURL: "r"})
	require.NoError(t, err)
	_, err = orch.SubmitBuild(ctx, BuildSpec{ID: "dup", RepoURL: "r"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// BUG REGRESSION: a submit that fails to enqueue (queue full) must not leave a
// phantom job registered. Before the fix, the job stayed in o.jobs forever,
// blocking resubmission and polluting ListBuilds.
func TestOrchestrator_SubmitBuild_QueueFull_RollsBackRegistration(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache()) // no workers drain the queue
	ctx := context.Background()

	// Fill the 100-slot queue.
	for i := 0; i < 100; i++ {
		_, err := orch.SubmitBuild(ctx, BuildSpec{ID: idForIndex(i), RepoURL: "r"})
		require.NoError(t, err)
	}

	// The 101st submit must fail with "queue is full"...
	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "overflow", RepoURL: "r"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue is full")

	// ...and must NOT have registered the phantom job.
	_, statusErr := orch.GetBuildStatus(ctx, "overflow")
	require.Error(t, statusErr)
	assert.Contains(t, statusErr.Error(), "not found",
		"failed enqueue left a phantom job in the registry")

	assert.Len(t, orch.ListBuilds(ctx, BuildFilters{}), 100,
		"phantom job leaked into listings")
}

func idForIndex(i int) string {
	return "q-" + time.Duration(i).String() + "-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// --- runBuild outcomes (end-user observable state + logs + image tag) ---
//
// These tests exercise the orchestrator's execute delegation path using the
// fakeOrchestratorBuilder so they remain hermetic (no real podman required).
// MUTATION: swap fakeOrchestratorBuilder.Execute to always succeed -> the
// StateFailed assertion fails.  Swap to always fail -> StateSucceeded fails.

func TestOrchestrator_RunBuild_FailurePath(t *testing.T) {
	// Use fakeOrchestratorBuilder so this unit test is hermetic (no podman).
	orch := newFakeOrchestrator(1, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()
	ctx := context.Background()

	// RepoURL "fail" triggers the failure branch in fakeOrchestratorBuilder.
	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "f1", RepoURL: "fail"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		j, err := orch.GetBuildStatus(ctx, "f1")
		return err == nil && j.IsTerminal()
	}, 2*time.Second, 20*time.Millisecond)

	j, err := orch.GetBuildStatus(ctx, "f1")
	require.NoError(t, err)
	assert.Equal(t, StateFailed, j.State)
	assert.Empty(t, j.ImageTag, "failed build must not produce an image tag")
	// The fake builder echoes "simulated failure" on the failure path, preserving
	// the assertion that the failure log line is captured.
	assert.Contains(t, lastLog(j.Logs), "simulated failure")
}

func TestOrchestrator_RunBuild_SuccessTagsImage(t *testing.T) {
	// Use fakeOrchestratorBuilder so this unit test is hermetic (no podman).
	// MUTATION: change fakeOrchestratorBuilder to set tag="wrong" -> this fails.
	orch := newFakeOrchestrator(1, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()
	ctx := context.Background()

	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "ok1", RepoURL: "r", Ref: "v9"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		j, err := orch.GetBuildStatus(ctx, "ok1")
		return err == nil && j.State == StateSucceeded
	}, 2*time.Second, 20*time.Millisecond)

	j, err := orch.GetBuildStatus(ctx, "ok1")
	require.NoError(t, err)
	// The fake builder uses "helix/<id>:<ref>" matching the former simulated tag.
	assert.Equal(t, "helix/ok1:v9", j.ImageTag, "image tag must encode id and ref")
}

func lastLog(logs []string) string {
	if len(logs) == 0 {
		return ""
	}
	return logs[len(logs)-1]
}

// --- PurgeTerminal reaper (completion feature) ---

func TestOrchestrator_PurgeTerminal(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache())
	ctx := context.Background()

	// Two jobs: one cancelled (terminal), one left queued (non-terminal).
	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "term", RepoURL: "r"})
	require.NoError(t, err)
	_, err = orch.SubmitBuild(ctx, BuildSpec{ID: "live", RepoURL: "r"})
	require.NoError(t, err)
	require.NoError(t, orch.CancelBuild(ctx, "term"))
	orch.RegisterArtifact("term", "deadbeef")

	// Cutoff in the future => the terminal job is eligible; live one is not.
	purged := orch.PurgeTerminal(time.Now().UTC().Add(time.Hour))
	assert.Equal(t, 1, purged)

	_, err = orch.GetBuildStatus(ctx, "term")
	assert.Error(t, err, "purged terminal job must be gone")
	_, err = orch.GetBuildStatus(ctx, "live")
	assert.NoError(t, err, "non-terminal job must survive purge")

	_, ok := orch.GetArtifactDigest("term")
	assert.False(t, ok, "purge must also drop the artifact mapping")
}

func TestOrchestrator_PurgeTerminal_RespectsCutoff(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache())
	ctx := context.Background()
	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "term2", RepoURL: "r"})
	require.NoError(t, err)
	require.NoError(t, orch.CancelBuild(ctx, "term2"))

	// Cutoff in the past => job completed AFTER cutoff => not eligible.
	purged := orch.PurgeTerminal(time.Now().UTC().Add(-time.Hour))
	assert.Equal(t, 0, purged)
	_, err = orch.GetBuildStatus(ctx, "term2")
	assert.NoError(t, err, "job newer than cutoff must be retained")
}

// --- WorkerPool allocation policy ---

func TestWorkerPool_AllocatePrefersMostRemainingCapacity(t *testing.T) {
	pool := NewWorkerPool()
	ctx := context.Background()
	require.NoError(t, pool.RegisterWorker(ctx, &Worker{ID: "small", Capacity: 2}))
	require.NoError(t, pool.RegisterWorker(ctx, &Worker{ID: "big", Capacity: 10}))

	w, err := pool.AllocateWorker(ctx)
	require.NoError(t, err)
	assert.Equal(t, "big", w.ID, "allocator must pick the worker with most remaining capacity")
}

func TestWorkerPool_AllocateSkipsUnhealthy(t *testing.T) {
	pool := NewWorkerPool()
	ctx := context.Background()
	require.NoError(t, pool.RegisterWorker(ctx, &Worker{ID: "only", Capacity: 5}))

	// Force unhealthy after registration.
	w, _ := pool.GetWorker("only")
	w.mu.Lock()
	w.Healthy = false
	w.mu.Unlock()

	_, err := pool.AllocateWorker(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no available workers")
}

func TestWorkerPool_RegisterWorker_Validation(t *testing.T) {
	pool := NewWorkerPool()
	ctx := context.Background()
	assert.Error(t, pool.RegisterWorker(ctx, &Worker{Capacity: 1}))             // no ID
	assert.Error(t, pool.RegisterWorker(ctx, &Worker{ID: "z", Capacity: 0}))    // bad capacity
	require.NoError(t, pool.RegisterWorker(ctx, &Worker{ID: "z", Capacity: 1})) // ok
}

// --- Server error propagation & ID generation ---

func TestServer_SubmitBuild_PropagatesValidationError(t *testing.T) {
	orch := NewOrchestrator(1, cache.NewMemoryCache())
	srv := NewServer(orch, NewWorkerPool())
	// Empty RepoUrl => orchestrator rejects => server must surface the error.
	_, err := srv.SubmitBuild(context.Background(), &helixv1.SubmitBuildRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo URL is required")
}

func TestServer_GetBuildStatus_NotFound(t *testing.T) {
	orch := NewOrchestrator(1, cache.NewMemoryCache())
	srv := NewServer(orch, NewWorkerPool())
	_, err := srv.GetBuildStatus(context.Background(), &helixv1.GetBuildStatusRequest{BuildId: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGenerateID_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := generateID()
		_, dup := seen[id]
		require.False(t, dup, "generateID returned a duplicate: %s", id)
		seen[id] = struct{}{}
	}
}

func TestTimestamp_NilIsZero(t *testing.T) {
	assert.Equal(t, int64(0), timestamp(nil))
	now := time.Unix(1234567890, 0)
	assert.Equal(t, int64(1234567890), timestamp(&now))
}
