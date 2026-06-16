package build

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// internal/build adversarial probes.
//
// All probes are HERMETIC: no real docker/podman/network. The orchestrator is
// wired with a fake OrchestratorBuilder (blockingBuilder / fakeOrchestratorBuilder
// from build_hardening_test.go) so `go test -race -timeout 90s` COMPLETES.
//
// Concurrency is capped (<= 8 goroutines). Each probe asserts SINK-SIDE state
// (the observable Job returned by GetBuildStatus / ListBuilds), never merely
// "no panic".
// =============================================================================

// blockingBuilder holds a job in StateRunning until released, so a test can
// observe a job WHILE a worker is concurrently executing it. It never touches
// any real external command.
type blockingBuilder struct {
	started  chan struct{} // closed-per-job: signals Execute reached the body
	release  chan struct{} // close to let all in-flight Execute calls finish
	startOne sync.Once
}

func (b *blockingBuilder) Execute(_ context.Context, j *Job) {
	b.startOne.Do(func() { close(b.started) })
	<-b.release
	// Read BuildArgs here, concurrently with any caller-side mutation, to make a
	// shared-pointer race observable to -race. We sort to force a real read of
	// every key/value of the aliased map.
	keys := make([]string, 0, len(j.BuildArgs))
	for k := range j.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	_ = keys
	j.mu.Lock()
	j.ImageTag = "helix/" + j.ID + ":done"
	j.mu.Unlock()
	_ = j.TransitionTo(StateSucceeded)
}

// ---------------------------------------------------------------------------
// (B) UNGUARDED SHARED STATE / SHARED-POINTER MUTATION RACE  (internal/node class)
//
// SubmitBuild stores the caller's BuildArgs map BY REFERENCE into the Job
// (orchestrator.go: `BuildArgs: spec.BuildArgs`). The very same *Job pointer is
// then handed to a worker, whose builder reads j.BuildArgs. The caller still
// holds the identical map. If the caller mutates the map after SubmitBuild
// returns (a completely ordinary thing to do with a map you own), that write
// races with the worker's read of the *aliased* map.
//
// CONTRACT TENSION: SubmitBuild advertises no ownership transfer of the spec; a
// caller reasonably retains and reuses its own map. clone() in this same file
// deep-copies BuildArgs precisely BECAUSE the stored map must be isolated from
// readers — yet ingestion stores the un-cloned alias. A clean -race run requires
// SubmitBuild to snapshot BuildArgs at ingestion.
//
// SINK-SIDE: with the fix, the stored job's args equal the value AT SUBMIT TIME
// regardless of later caller mutation; -race is clean.
//
// This test FAILS (race) on UNFIXED prod. It is NOT env-gated.
// ---------------------------------------------------------------------------

func TestAdversarial_SubmitBuild_SharedBuildArgsAlias_Race(t *testing.T) {
	bb := &blockingBuilder{started: make(chan struct{}), release: make(chan struct{})}
	orch := NewOrchestratorWithBuilder(2, cache.NewMemoryCache(), bb)
	orch.Start(context.Background())
	defer orch.Stop()
	ctx := context.Background()

	// Caller-owned map. We will keep mutating it after handing it to SubmitBuild.
	args := map[string]string{"VERSION": "1", "ARCH": "amd64"}

	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "alias", RepoURL: "r", BuildArgs: args})
	require.NoError(t, err)

	// Wait until a worker is actually inside Execute (reading j.BuildArgs).
	select {
	case <-bb.started:
	case <-time.After(5 * time.Second):
		t.Fatal("builder never started")
	}

	// Concurrently mutate the caller-owned map while the worker reads the alias.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			args[fmt.Sprintf("K%d", i)] = "v" // write to the map the job aliases
		}
	}()

	close(bb.release) // let the worker proceed to read j.BuildArgs
	wg.Wait()
	orch.Stop()

	// SINK-SIDE conservation: the job's recorded args must reflect the SUBMITTED
	// inputs, not whatever the caller later scribbled. A correct (snapshotting)
	// implementation isolates the stored map at ingestion.
	j, err := orch.GetBuildStatus(ctx, "alias")
	require.NoError(t, err)
	assert.Equal(t, "1", j.BuildArgs["VERSION"], "submitted arg corrupted by caller mutation")
	assert.Equal(t, "amd64", j.BuildArgs["ARCH"], "submitted arg corrupted by caller mutation")
	assert.NotContains(t, j.BuildArgs, "K0",
		"caller's post-submit mutation leaked into the stored build inputs (shared-pointer alias)")
}

// ---------------------------------------------------------------------------
// (B2) UNGUARDED SHARED STATE under concurrent start/status/list.
//
// Hammer SubmitBuild + GetBuildStatus + ListBuilds concurrently (capped at 8
// goroutines) while workers drain the queue. Pure -race detector probe on the
// orchestrator's jobs/artifacts maps. SINK-SIDE: every submitted ID is later
// observable exactly once.
// ---------------------------------------------------------------------------

func TestAdversarial_Orchestrator_ConcurrentStateMap_Race(t *testing.T) {
	orch := newFakeOrchestrator(4, cache.NewMemoryCache()) // hermetic fake builder
	orch.Start(context.Background())
	defer orch.Stop()
	ctx := context.Background()

	const writers = 4
	const perWriter = 25
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("c-%d-%d", w, i)
				_, _ = orch.SubmitBuild(ctx, BuildSpec{ID: id, RepoURL: "r"})
				_, _ = orch.GetBuildStatus(ctx, id)
				_ = orch.ListBuilds(ctx, BuildFilters{})
				orch.RegisterArtifact(id, "d")
				_, _ = orch.GetArtifactDigest(id)
			}
		}(w)
	}
	wg.Wait()

	// SINK-SIDE conservation: all writers*perWriter IDs are present, none lost.
	all := orch.ListBuilds(ctx, BuildFilters{})
	seen := make(map[string]int, len(all))
	for _, j := range all {
		seen[j.ID]++
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			id := fmt.Sprintf("c-%d-%d", w, i)
			assert.Equalf(t, 1, seen[id], "build %s lost or duplicated in state map", id)
		}
	}
}

// ---------------------------------------------------------------------------
// (C) TOTAL-ORDER / DETERMINISM — ListBuilds.
//
// ListBuilds iterates o.jobs (a Go map) and appends in map-iteration order,
// which Go randomizes per range. There is NO documented ordering guarantee, so
// this is a CONTRACT/LATENT note, NOT a bug: we PIN the current contract (the
// SET of returned IDs is complete and stable) and explicitly document that ORDER
// is not guaranteed. Callers needing a stable listing must sort.
// ---------------------------------------------------------------------------

func TestAdversarial_ListBuilds_SetIsStable_OrderIsNot(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache()) // no workers; jobs stay queued
	ctx := context.Background()

	want := make([]string, 0, 16)
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("L%02d", i)
		want = append(want, id)
		_, err := orch.SubmitBuild(ctx, BuildSpec{ID: id, RepoURL: "r"})
		require.NoError(t, err)
	}
	sort.Strings(want)

	collect := func() []string {
		got := make([]string, 0, 16)
		for _, j := range orch.ListBuilds(ctx, BuildFilters{}) {
			got = append(got, j.ID)
		}
		return got
	}

	// The SET (sorted) is complete and identical across calls — this is the real,
	// dependable contract and the assertion that bites if a job is dropped.
	a := collect()
	b := collect()
	sortedA := append([]string(nil), a...)
	sortedB := append([]string(nil), b...)
	sort.Strings(sortedA)
	sort.Strings(sortedB)
	assert.Equal(t, want, sortedA, "ListBuilds dropped or duplicated a job (set incomplete)")
	assert.Equal(t, sortedA, sortedB, "ListBuilds set must be stable across calls")
}

// ---------------------------------------------------------------------------
// (C2) IDENTITY — duplicate build ID must NOT clobber the first job.
//
// SubmitBuild rejects a duplicate ID. Pin it: the second submit fails AND the
// FIRST job's inputs survive intact (no silent overwrite of state).
// ---------------------------------------------------------------------------

func TestAdversarial_SubmitBuild_DuplicateDoesNotClobber(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache()) // no workers; stays queued
	ctx := context.Background()

	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "dup", RepoURL: "first", Ref: "r1"})
	require.NoError(t, err)

	_, err = orch.SubmitBuild(ctx, BuildSpec{ID: "dup", RepoURL: "second", Ref: "r2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// SINK-SIDE: the original identity is preserved, not overwritten by attempt 2.
	j, err := orch.GetBuildStatus(ctx, "dup")
	require.NoError(t, err)
	assert.Equal(t, "first", j.RepoURL, "duplicate submit clobbered the original job")
	assert.Equal(t, "r1", j.Ref, "duplicate submit clobbered the original job's ref")
}

// ---------------------------------------------------------------------------
// (D) IDEMPOTENCY / DOUBLE-START — concurrent identical submits create ONE job.
//
// Two goroutines race to submit the SAME ID. Exactly one must win; the registry
// must hold exactly one job for that ID (no two parallel builds for one ID).
// ---------------------------------------------------------------------------

func TestAdversarial_SubmitBuild_ConcurrentSameID_ExactlyOne(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache()) // no workers; stays queued
	ctx := context.Background()

	const racers = 8
	var wg sync.WaitGroup
	var okCount int
	var mu sync.Mutex
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := orch.SubmitBuild(ctx, BuildSpec{ID: "race1", RepoURL: "r"}); err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, 1, okCount, "more than one concurrent submit of the same ID succeeded")

	// SINK-SIDE: exactly one job exists for that ID.
	count := 0
	for _, j := range orch.ListBuilds(ctx, BuildFilters{}) {
		if j.ID == "race1" {
			count++
		}
	}
	assert.Equal(t, 1, count, "double-start created more than one job for one ID")
}

// ---------------------------------------------------------------------------
// (A) ATOMICITY — failed enqueue leaves NO orphan job (under concurrency).
//
// Pins the documented all-or-nothing rollback in SubmitBuild: when the queue is
// full, registration is rolled back so no half-written/orphan job remains. We
// drive it concurrently to ensure the rollback is itself race-clean.
// ---------------------------------------------------------------------------

func TestAdversarial_SubmitBuild_QueueFull_NoOrphan_Concurrent(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache()) // no workers drain the 100-slot queue
	ctx := context.Background()

	// Saturate the queue.
	for i := 0; i < 100; i++ {
		_, err := orch.SubmitBuild(ctx, BuildSpec{ID: fmt.Sprintf("fill-%03d", i), RepoURL: "r"})
		require.NoError(t, err)
	}

	// 8 concurrent overflow submits, all distinct IDs — every one must fail AND
	// leave no trace.
	const overflow = 8
	var wg sync.WaitGroup
	for i := 0; i < overflow; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := orch.SubmitBuild(ctx, BuildSpec{ID: fmt.Sprintf("over-%d", i), RepoURL: "r"})
			assert.Error(t, err)
		}(i)
	}
	wg.Wait()

	// SINK-SIDE conservation: exactly the 100 filled jobs remain; zero orphans.
	all := orch.ListBuilds(ctx, BuildFilters{})
	assert.Len(t, all, 100, "queue-full overflow left orphan job(s) in the registry")
	for i := 0; i < overflow; i++ {
		_, err := orch.GetBuildStatus(ctx, fmt.Sprintf("over-%d", i))
		assert.Error(t, err, "orphan job survived a failed enqueue")
	}
}
