package build

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/build"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrchestrator_SubmitAndGetStatus(t *testing.T) {
	// Use fake builder: this test exercises orchestrator state-machine and submit
	// plumbing, not the real podman executor.
	orch := newFakeOrchestrator(1, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()

	ctx := context.Background()
	spec := BuildSpec{
		ID:      "build-001",
		RepoURL: "https://github.com/helix/cluster",
		Ref:     "main",
	}

	j, err := orch.SubmitBuild(ctx, spec)
	require.NoError(t, err)
	assert.Equal(t, "build-001", j.ID)
	// State may transition quickly; check via GetBuildStatus for consistent read
	js, err := orch.GetBuildStatus(ctx, "build-001")
	require.NoError(t, err)
	assert.NotNil(t, js)

	// Wait for build to complete.
	require.Eventually(t, func() bool {
		j2, err := orch.GetBuildStatus(ctx, "build-001")
		require.NoError(t, err)
		return j2.IsTerminal()
	}, 2*time.Second, 50*time.Millisecond)

	j2, err := orch.GetBuildStatus(ctx, "build-001")
	require.NoError(t, err)
	assert.Equal(t, StateSucceeded, j2.State)
	assert.NotEmpty(t, j2.ImageTag)
}

func TestOrchestrator_GetBuildStatus_NotFound(t *testing.T) {
	orch := NewOrchestrator(1, cache.NewMemoryCache())
	_, err := orch.GetBuildStatus(context.Background(), "missing")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestOrchestrator_CancelBuild(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache()) // 0 workers so job stays queued
	orch.Start(context.Background())
	defer orch.Stop()

	ctx := context.Background()
	spec := BuildSpec{
		ID:      "build-cancel",
		RepoURL: "https://github.com/helix/cluster",
	}

	_, err := orch.SubmitBuild(ctx, spec)
	require.NoError(t, err)

	err = orch.CancelBuild(ctx, "build-cancel")
	require.NoError(t, err)

	j, err := orch.GetBuildStatus(ctx, "build-cancel")
	require.NoError(t, err)
	assert.Equal(t, StateCancelled, j.State)
}

func TestOrchestrator_CancelBuild_NotFound(t *testing.T) {
	orch := NewOrchestrator(1, cache.NewMemoryCache())
	err := orch.CancelBuild(context.Background(), "missing")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestOrchestrator_ListBuilds_WithFilters(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := orch.SubmitBuild(ctx, BuildSpec{
			ID:      fmt.Sprintf("build-%d", i),
			RepoURL: "https://github.com/helix/cluster",
			Ref:     "main",
		})
		require.NoError(t, err)
	}

	// Cancel one build.
	err := orch.CancelBuild(ctx, "build-1")
	require.NoError(t, err)

	all := orch.ListBuilds(ctx, BuildFilters{})
	assert.Len(t, all, 3)

	cancelled := orch.ListBuilds(ctx, BuildFilters{State: StateCancelled})
	assert.Len(t, cancelled, 1)
	assert.Equal(t, "build-1", cancelled[0].ID)

	byRepo := orch.ListBuilds(ctx, BuildFilters{RepoURL: "https://github.com/helix/cluster"})
	assert.Len(t, byRepo, 3)

	byRepoMiss := orch.ListBuilds(ctx, BuildFilters{RepoURL: "https://other"})
	assert.Len(t, byRepoMiss, 0)
}

func TestOrchestrator_ConcurrentBuilds(t *testing.T) {
	// Use fake builder: exercises concurrent queue/state-machine, not real podman.
	orch := newFakeOrchestrator(4, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()

	ctx := context.Background()
	const n = 10

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			spec := BuildSpec{
				ID:      fmt.Sprintf("build-concurrent-%d", idx),
				RepoURL: "https://github.com/helix/cluster",
				Ref:     "main",
			}
			_, err := orch.SubmitBuild(ctx, spec)
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()

	// Wait for all to finish.
	require.Eventually(t, func() bool {
		done := 0
		for i := 0; i < n; i++ {
			j, err := orch.GetBuildStatus(ctx, fmt.Sprintf("build-concurrent-%d", i))
			if err == nil && j.IsTerminal() {
				done++
			}
		}
		return done == n
	}, 5*time.Second, 50*time.Millisecond)

	all := orch.ListBuilds(ctx, BuildFilters{})
	assert.Len(t, all, n)

	succeeded := 0
	for _, j := range all {
		if j.State == StateSucceeded {
			succeeded++
		}
	}
	assert.Equal(t, n, succeeded)
}

func TestOrchestrator_CacheIntegration(t *testing.T) {
	c := cache.NewMemoryCache()
	orch := NewOrchestrator(1, c)
	orch.Start(context.Background())
	defer orch.Stop()

	data := []byte("artifact-data")
	digest := cache.Digest(data)
	err := c.Put(digest, data)
	require.NoError(t, err)

	orch.RegisterArtifact("build-002", digest)
	d, ok := orch.GetArtifactDigest("build-002")
	assert.True(t, ok)
	assert.Equal(t, digest, d)

	got, err := orch.Cache().Get(digest)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestWorkerPool_RegisterAndAllocate(t *testing.T) {
	pool := NewWorkerPool()
	ctx := context.Background()

	w := &Worker{
		ID:       "worker-1",
		Address:  "10.0.0.1:8080",
		Capacity: 2,
	}
	err := pool.RegisterWorker(ctx, w)
	require.NoError(t, err)

	allocated, err := pool.AllocateWorker(ctx)
	require.NoError(t, err)
	assert.Equal(t, "worker-1", allocated.ID)
	assert.Equal(t, 1, allocated.Load)

	// Allocate again up to capacity.
	allocated2, err := pool.AllocateWorker(ctx)
	require.NoError(t, err)
	assert.Equal(t, "worker-1", allocated2.ID)
	assert.Equal(t, 2, allocated2.Load)

	// No more capacity.
	_, err = pool.AllocateWorker(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no available workers")
}

func TestWorkerPool_ReleaseWorker(t *testing.T) {
	pool := NewWorkerPool()
	ctx := context.Background()

	w := &Worker{ID: "worker-2", Capacity: 2}
	require.NoError(t, pool.RegisterWorker(ctx, w))

	_, err := pool.AllocateWorker(ctx)
	require.NoError(t, err)

	err = pool.ReleaseWorker("worker-2")
	require.NoError(t, err)

	w.mu.RLock()
	load := w.Load
	w.mu.RUnlock()
	assert.Equal(t, 0, load)

	err = pool.ReleaseWorker("missing")
	assert.Error(t, err)
}

func TestWorkerPool_ConcurrentAllocations(t *testing.T) {
	pool := NewWorkerPool()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		w := &Worker{
			ID:       fmt.Sprintf("worker-%d", i),
			Capacity: 10,
		}
		require.NoError(t, pool.RegisterWorker(ctx, w))
	}

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := pool.AllocateWorker(ctx)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond)
			require.NoError(t, pool.ReleaseWorker(w.ID))
		}()
	}
	wg.Wait()

	assert.Equal(t, 50, pool.TotalCapacity())
	assert.Equal(t, 0, pool.TotalLoad())
}

func TestWorkerPool_TotalCapacityAndLoad(t *testing.T) {
	pool := NewWorkerPool()
	ctx := context.Background()

	pool.RegisterWorker(ctx, &Worker{ID: "a", Capacity: 3})
	pool.RegisterWorker(ctx, &Worker{ID: "b", Capacity: 5})

	assert.Equal(t, 8, pool.TotalCapacity())
	assert.Equal(t, 0, pool.TotalLoad())

	_, err := pool.AllocateWorker(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, pool.TotalLoad())
}

func TestWorkerPool_RemoveWorker(t *testing.T) {
	pool := NewWorkerPool()
	ctx := context.Background()

	pool.RegisterWorker(ctx, &Worker{ID: "w1", Capacity: 1})
	pool.RemoveWorker("w1")

	_, ok := pool.GetWorker("w1")
	assert.False(t, ok)
}

func TestServer_SubmitBuild(t *testing.T) {
	// Use fake builder: this test exercises the gRPC server submit path, not podman.
	orch := newFakeOrchestrator(1, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()

	srv := NewServer(orch, NewWorkerPool())
	ctx := context.Background()

	resp, err := srv.SubmitBuild(ctx, &helixv1.SubmitBuildRequest{
		RepoUrl:        "https://github.com/helix/cluster",
		Ref:            "v1.0.0",
		DockerfilePath: "Dockerfile",
		BuildArgs:      map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.BuildId)
	assert.True(t, resp.Queued)
}

func TestServer_GetBuildStatus(t *testing.T) {
	// Use fake builder: this test exercises the gRPC status path, not podman.
	orch := newFakeOrchestrator(1, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()

	srv := NewServer(orch, NewWorkerPool())
	ctx := context.Background()

	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "b1", RepoURL: "https://github.com/helix/cluster"})
	require.NoError(t, err)

	status, err := srv.GetBuildStatus(ctx, &helixv1.GetBuildStatusRequest{BuildId: "b1"})
	require.NoError(t, err)
	assert.Equal(t, "b1", status.BuildId)
}

func TestServer_CancelBuild(t *testing.T) {
	orch := NewOrchestrator(0, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()

	srv := NewServer(orch, NewWorkerPool())
	ctx := context.Background()

	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "b2", RepoURL: "https://github.com/helix/cluster"})
	require.NoError(t, err)

	resp, err := srv.CancelBuild(ctx, &helixv1.CancelBuildRequest{BuildId: "b2"})
	require.NoError(t, err)
	assert.True(t, resp.Cancelled)
}

func TestBuildSpec_Platforms(t *testing.T) {
	spec := BuildSpec{
		ID:      "build-platform",
		RepoURL: "https://github.com/helix/cluster",
		Platforms: []build.Platform{
			{OS: "linux", Arch: "amd64"},
			{OS: "linux", Arch: "arm64", Variant: "v8"},
		},
	}
	assert.Len(t, spec.Platforms, 2)
	assert.Equal(t, "linux/amd64", spec.Platforms[0].String())
	assert.Equal(t, "linux/arm64/v8", spec.Platforms[1].String())
}
