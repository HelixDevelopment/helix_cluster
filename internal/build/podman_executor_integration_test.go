//go:build integration

// Package build provides the build orchestration service for Helix Cluster OS.
package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipIfPodmanAbsent skips the calling test if podman is not found on PATH.
// Per the integration-gate contract: hard-fail when dependency is available;
// t.Skip ONLY when genuinely absent.
func skipIfPodmanAbsent(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not found on PATH (exec.LookPath: %v); skipping integration test", err)
	}
}

// fixtureDockerfile writes a minimal fixture Dockerfile (FROM busybox; RUN true)
// into dir and returns the path. The per-run UUID is embedded as a LABEL so
// each build is distinct and the UUID can be asserted on the sink side.
func fixtureDockerfile(t *testing.T, dir, runUUID string) string {
	t.Helper()
	content := fmt.Sprintf(
		"FROM busybox\nRUN true\nLABEL helix.run.uuid=%s\n",
		runUUID,
	)
	dfPath := filepath.Join(dir, "Dockerfile")
	require.NoError(t, os.WriteFile(dfPath, []byte(content), 0o644))
	return dfPath
}

// TestPodmanOrchestratorBuilder_RealBuild_Integration is the §7.1 integration
// proof for HXC-1099.
//
// It:
//  1. Writes a fixture Dockerfile (FROM busybox; RUN true; LABEL uuid=<run-uuid>)
//     so each run is uniquely identified and cannot be satisfied by a cached
//     fabrication.
//  2. Submits the job through the REAL podmanOrchestratorBuilder backed by the
//     production os/exec runner.
//  3. Asserts the job reaches StateSucceeded with a non-empty ImageTag.
//  4. Asserts `podman image inspect <tag>` exits 0 — SINK-SIDE: image exists.
//  5. Asserts the inspect output contains the per-run UUID label, proving the
//     REAL image built from THIS Dockerfile was inspected (not a stale cache).
//  6. Cleans up with `podman rmi --force <tag>` in a defer.
//
// t.Skip only if podman is absent (exec.LookPath fails); hard-fail otherwise.
func TestPodmanOrchestratorBuilder_RealBuild_Integration(t *testing.T) {
	skipIfPodmanAbsent(t)

	// Per-run UUID: embedded in the Dockerfile label; asserted in inspect output.
	runUUID := uuid.New().String()
	t.Logf("per-run UUID: %s", runUUID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Write fixture Dockerfile to a temp dir.
	tmpDir := t.TempDir()
	dfPath := fixtureDockerfile(t, tmpDir, runUUID)

	// Derive the expected tag (mirrors podmanOrchestratorBuilder.Execute logic).
	jobID := fmt.Sprintf("hxc1099-integ-%d", time.Now().UnixNano())
	jobRef := runUUID // use UUID as ref so tag is globally unique
	wantTag := fmt.Sprintf("helix-build/%s:%s", jobID, jobRef)

	// Register cleanup before building so even a partial build is cleaned up.
	t.Cleanup(func() {
		cmd := exec.Command("podman", "rmi", "--force", wantTag)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	})

	// Build via the REAL podmanOrchestratorBuilder (nil runner = production os/exec).
	b := NewPodmanOrchestratorBuilder(nil)
	orch := NewOrchestratorWithBuilder(1, nil, b)
	orch.Start(ctx)
	defer orch.Stop()

	spec := BuildSpec{
		ID:             jobID,
		RepoURL:        tmpDir,
		Ref:            jobRef,
		DockerfilePath: dfPath,
	}
	_, err := orch.SubmitBuild(ctx, spec)
	require.NoError(t, err, "submit must succeed")

	// Wait for terminal state.
	require.Eventually(t, func() bool {
		j, e := orch.GetBuildStatus(ctx, jobID)
		return e == nil && j.IsTerminal()
	}, 5*time.Minute, 500*time.Millisecond)

	j, err := orch.GetBuildStatus(ctx, jobID)
	require.NoError(t, err)

	// --- State assertion ---
	if j.State != StateSucceeded {
		t.Logf("build logs:\n%s", strings.Join(j.GetLogs(), "\n"))
		t.Fatalf("SINK-SIDE FAILURE: job state = %s, want succeeded", j.State)
	}

	// --- ImageTag assertion ---
	require.NotEmpty(t, j.ImageTag, "ImageTag must be set on success")
	assert.Equal(t, wantTag, j.ImageTag, "ImageTag must match derived tag")

	// --- SINK-SIDE: podman image inspect must exit 0 ---
	// This proves the image actually exists as a result of a REAL podman build,
	// not that the state machine was simply driven to StateSucceeded.
	inspectCmd := exec.CommandContext(ctx, "podman", "image", "inspect", j.ImageTag)
	inspectOut, inspectErr := inspectCmd.CombinedOutput()
	require.NoError(t, inspectErr,
		"SINK-SIDE FAILURE: podman image inspect %q must exit 0 (image must exist)\noutput: %s",
		j.ImageTag, string(inspectOut))
	t.Logf("podman image inspect succeeded (image exists): %s", string(inspectOut))

	// --- Per-run UUID label assertion (sink-side identity) ---
	// The inspect output must contain the UUID we embedded in the Dockerfile
	// LABEL, proving the real image built from THIS run's Dockerfile is what was
	// inspected — not a stale or reused image.
	assert.Contains(t, string(inspectOut), runUUID,
		"SINK-SIDE: inspect output must contain the per-run UUID label, "+
			"proving the REAL image from this run was built and exists")
}

// TestPodmanOrchestratorBuilder_RealBuild_BadDockerfile_Integration proves the
// failure path: a deliberately broken Dockerfile causes the REAL builder to
// drive StateFailed with no ImageTag set.
//
// MUTATION: ignore podman's non-zero exit and transition to StateSucceeded
// -> the StateFailed assertion fails, proving the real exit code is checked.
func TestPodmanOrchestratorBuilder_RealBuild_BadDockerfile_Integration(t *testing.T) {
	skipIfPodmanAbsent(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tmpDir := t.TempDir()
	// Write a deliberately broken Dockerfile.
	badDf := filepath.Join(tmpDir, "Dockerfile")
	require.NoError(t, os.WriteFile(badDf, []byte("THIS IS NOT VALID DOCKERFILE SYNTAX\n"), 0o644))

	b := NewPodmanOrchestratorBuilder(nil)
	orch := NewOrchestratorWithBuilder(1, nil, b)
	orch.Start(ctx)
	defer orch.Stop()

	jobID := fmt.Sprintf("hxc1099-bad-%d", time.Now().UnixNano())
	spec := BuildSpec{
		ID:             jobID,
		RepoURL:        tmpDir,
		Ref:            "main",
		DockerfilePath: badDf,
	}
	_, err := orch.SubmitBuild(ctx, spec)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		j, e := orch.GetBuildStatus(ctx, jobID)
		return e == nil && j.IsTerminal()
	}, 5*time.Minute, 500*time.Millisecond)

	j, err := orch.GetBuildStatus(ctx, jobID)
	require.NoError(t, err)

	assert.Equal(t, StateFailed, j.State,
		"bad Dockerfile must drive StateFailed via a REAL non-zero podman exit code")
	assert.Empty(t, j.ImageTag,
		"a failed build must not produce an image tag")
}
