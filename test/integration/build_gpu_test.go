package integration

import (
	"context"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/build"
	"github.com/HelixDevelopment/helix_cluster/internal/gpu"
	"github.com/stretchr/testify/suite"
)

type BuildGPUSuite struct {
	IntegrationSuite
}

func (s *BuildGPUSuite) TestSubmitBuildAllocateGPUBuildRunsReleaseGPU() {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()

	// Setup GPU manager and detect (mock) GPUs.
	gpuMgr := gpu.NewManager()
	err := gpuMgr.DetectGPUs()
	s.Require().NoError(err)

	gpus := gpuMgr.ListGPUs()
	s.NotEmpty(gpus)

	// Allocate GPU for a build job.
	jobID := "build-gpu-001"
	allocated, err := gpuMgr.AllocateGPU(jobID)
	s.Require().NoError(err)
	s.NotNil(allocated)
	s.Equal(gpu.Allocated, allocated.Status)
	s.Equal(jobID, allocated.AllocatedTo)

	// Submit build via build orchestrator.
	orch := build.NewOrchestratorWithBuilder(1, nil, build.NewSimulatedBuilder())
	orch.Start(ctx)
	defer orch.Stop()

	buildJob, err := orch.SubmitBuild(ctx, build.BuildSpec{
		ID:      jobID,
		RepoURL: "https://github.com/example/repo",
		Ref:     "main",
	})
	s.Require().NoError(err)
	s.NotNil(buildJob)

	// Wait for build to complete.
	s.Eventually(func() bool {
		j, err := orch.GetBuildStatus(ctx, jobID)
		if err != nil {
			return false
		}
		return j.IsTerminal()
	}, 5*time.Second, 100*time.Millisecond)

	completed, err := orch.GetBuildStatus(ctx, jobID)
	s.Require().NoError(err)
	s.Equal(build.StateSucceeded, completed.State)

	// Release GPU.
	gpuMgr.ReleaseGPU(jobID)
	gpusAfter := gpuMgr.ListGPUs()
	var released *gpu.GPU
	for _, g := range gpusAfter {
		if g.ID == allocated.ID {
			released = g
			break
		}
	}
	s.Require().NotNil(released)
	s.Equal(gpu.Available, released.Status)
	s.Empty(released.AllocatedTo)
}

func (s *BuildGPUSuite) TestBuildFailureGPUReleasedAnyway() {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()

	gpuMgr := gpu.NewManager()
	err := gpuMgr.DetectGPUs()
	s.Require().NoError(err)

	jobID := "build-gpu-fail-002"
	allocated, err := gpuMgr.AllocateGPU(jobID)
	s.Require().NoError(err)
	s.NotNil(allocated)

	// Submit a build that will fail (RepoURL == "fail" triggers simulated failure).
	orch := build.NewOrchestratorWithBuilder(1, nil, build.NewSimulatedBuilder())
	orch.Start(ctx)
	defer orch.Stop()

	buildJob, err := orch.SubmitBuild(ctx, build.BuildSpec{
		ID:      jobID,
		RepoURL: "fail",
		Ref:     "main",
	})
	s.Require().NoError(err)
	s.NotNil(buildJob)

	// Wait for build to reach terminal state.
	s.Eventually(func() bool {
		j, err := orch.GetBuildStatus(ctx, jobID)
		if err != nil {
			return false
		}
		return j.IsTerminal()
	}, 5*time.Second, 100*time.Millisecond)

	completed, err := orch.GetBuildStatus(ctx, jobID)
	s.Require().NoError(err)
	s.Equal(build.StateFailed, completed.State)

	// Release GPU anyway (as the orchestrator would in a defer/cleanup).
	gpuMgr.ReleaseGPU(jobID)
	gpusAfter := gpuMgr.ListGPUs()
	var released *gpu.GPU
	for _, g := range gpusAfter {
		if g.ID == allocated.ID {
			released = g
			break
		}
	}
	s.Require().NotNil(released)
	s.Equal(gpu.Available, released.Status)
	s.Empty(released.AllocatedTo)
}

func TestBuildGPUSuite(t *testing.T) {
	suite.Run(t, new(BuildGPUSuite))
}
