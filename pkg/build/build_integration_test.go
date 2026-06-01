//go:build integration

package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// skipIfNoPodman skips the test when podman is not available on PATH.
// This allows the test binary to compile and run without podman; the
// orchestrator ensures podman is present in the integration gate.
func skipIfNoPodman(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not found on PATH; skipping integration test")
	}
}

// fixtureDir returns the absolute path to testdata/fixture.
func fixtureDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file path")
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata", "fixture")
}

// TestRealBuild_ImageExistsAfterBuild is the §7.1 integration proof for
// HXC-1020.  It:
//  1. Builds a fixture image (FROM busybox; RUN true) via the REAL podmanBuilder
//     backed by os/exec → real podman daemon.
//  2. Asserts the job reaches StateSucceeded and ImageTag is set.
//  3. Asserts `podman image inspect <tag>` exits 0 (SINK-SIDE: image exists).
//  4. Cleans up with `podman rmi <tag>` in a defer.
//
// This is NOT a mock assertion — it proves a real image layer was produced.
func TestRealBuild_ImageExistsAfterBuild(t *testing.T) {
	skipIfNoPodman(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tag := fmt.Sprintf("helix-build/hxc1020-integ:%d", time.Now().UnixNano())

	// Defer cleanup before any build so even a partial build is cleaned up.
	defer func() {
		cmd := exec.Command("podman", "rmi", "--force", tag)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}()

	fixDir := fixtureDir(t)
	dockerfilePath := filepath.Join(fixDir, "Dockerfile")

	svc := NewServiceWithBuilder(1, NewPodmanBuilder(nil /* uses real execRunner */))
	svc.Start(ctx)
	defer svc.Stop()

	j := &Job{
		ID:             "integ-hxc1020",
		RepoURL:        fixDir,
		Ref:            fmt.Sprintf("%d", time.Now().UnixNano()),
		DockerfilePath: dockerfilePath,
	}
	// Override ImageTag derivation: the builder derives tag from j.ID + j.Ref.
	// We want the cleanup defer to have the same tag, so derive it here.
	derivedTag := fmt.Sprintf("helix-build/%s:%s", j.ID, j.Ref)
	// Update the defer to use the derived tag (reassign outer tag variable).
	tag = derivedTag

	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	got, err := svc.WaitDone(ctx, j.ID)
	if err != nil {
		t.Fatalf("WaitDone: %v", err)
	}

	// --- State assertion ---
	if got.State != StateSucceeded {
		t.Logf("build logs:\n%s", strings.Join(got.GetLogs(), "\n"))
		t.Fatalf("job state = %s, want succeeded", got.State)
	}

	// --- ImageTag assertion ---
	if got.ImageTag == "" {
		t.Fatal("ImageTag must be set on success (real image was built)")
	}
	if got.ImageTag != derivedTag {
		t.Errorf("ImageTag = %q, want %q", got.ImageTag, derivedTag)
	}

	// --- Sink-side assertion: podman image inspect must exit 0 ---
	// This proves the image actually exists and is pullable, not just that the
	// job state machine was driven to StateSucceeded.
	inspectCmd := exec.CommandContext(ctx, "podman", "image", "inspect", got.ImageTag)
	inspectOut, inspectErr := inspectCmd.CombinedOutput()
	if inspectErr != nil {
		t.Fatalf("SINK-SIDE FAILURE: podman image inspect %q exited non-zero: %v\noutput: %s",
			got.ImageTag, inspectErr, inspectOut)
	}
	t.Logf("podman image inspect succeeded: %s", string(inspectOut))
}

// TestRealBuild_BadDockerfile_DriveStateFailed verifies that a deliberately
// broken Dockerfile causes the real builder to drive StateFailed and not set
// an ImageTag — proving the error path is wired to real podman exit codes.
func TestRealBuild_BadDockerfile_DriveStateFailed(t *testing.T) {
	skipIfNoPodman(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Write a bad Dockerfile to a temp dir.
	tmpDir, err := os.MkdirTemp("", "helix-build-bad-df-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	badDf := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(badDf, []byte("THIS IS NOT VALID DOCKERFILE SYNTAX\n"), 0644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	svc := NewServiceWithBuilder(1, NewPodmanBuilder(nil))
	svc.Start(ctx)
	defer svc.Stop()

	j := &Job{
		ID:             "integ-hxc1020-bad",
		RepoURL:        tmpDir,
		Ref:            "main",
		DockerfilePath: badDf,
	}

	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	got, err := svc.WaitDone(ctx, j.ID)
	if err != nil {
		t.Fatalf("WaitDone: %v", err)
	}

	if got.State != StateFailed {
		t.Errorf("bad Dockerfile: state = %s, want failed", got.State)
	}
	if got.ImageTag != "" {
		t.Errorf("bad Dockerfile: ImageTag = %q, want empty (failed build must not set tag)", got.ImageTag)
	}
}
