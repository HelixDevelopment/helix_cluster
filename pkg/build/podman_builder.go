package build

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CommandRunner is the injectable seam for running external commands.
// Unit tests inject a fakeRunner; production code uses execRunner.
type CommandRunner interface {
	// Run executes the named command with the given arguments.
	// It returns combined stdout, stderr bytes and any error.
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// execRunner is the production CommandRunner that shells out via os/exec.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// podmanBuilder is the REAL Builder implementation that drives `podman build`
// for a job and confirms the image exists with `podman image inspect`.
//
// It implements the Builder interface.  Inject it via NewServiceWithBuilder or
// NewPodmanBuilder (for testing with a fake runner).
type podmanBuilder struct {
	runner CommandRunner
}

// NewPodmanBuilder creates a podmanBuilder backed by the given runner.
// Pass nil to use the real os/exec runner (production use).
func NewPodmanBuilder(runner CommandRunner) Builder {
	if runner == nil {
		runner = execRunner{}
	}
	return &podmanBuilder{runner: runner}
}

// Build implements Builder using real podman commands.
//
// Steps:
//  1. TransitionTo(StateRunning) — the worker already did this, but this is
//     a no-op if already running; we don't call it again.
//  2. Build the image: podman build -t <tag> [-f <dockerfile>] [--build-arg K=V...] <contextDir>
//  3. Inspect the image: podman image inspect <tag> — proves it exists/is pullable.
//  4. On success: SetImageTag + TransitionTo(StateSucceeded).
//     On runner error or ctx cancellation: TransitionTo(StateFailed/StateCancelled).
//
// Tag derivation: "helix-build/<jobID>:<ref>" where ref defaults to "latest".
func (b *podmanBuilder) Build(ctx context.Context, j *Job) {
	ref := j.Ref
	if ref == "" {
		ref = "latest"
	}
	tag := fmt.Sprintf("helix-build/%s:%s", j.ID, ref)

	// Derive context dir and dockerfile from job fields.
	// DockerfilePath may be empty (uses the default Dockerfile in context dir)
	// or a path relative to or within contextDir.
	// For the real builder the "context directory" is stored in the RepoURL
	// field when it is a local path (integration test uses a testdata dir).
	// In production the caller populates DockerfilePath and RepoURL accordingly.
	contextDir := j.RepoURL
	if contextDir == "" {
		contextDir = "."
	}

	// --- Build phase ---
	buildArgs := []string{"build", "-t", tag}
	if j.DockerfilePath != "" {
		buildArgs = append(buildArgs, "-f", j.DockerfilePath)
	}
	for k, v := range j.BuildArgs {
		buildArgs = append(buildArgs, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}
	buildArgs = append(buildArgs, contextDir)

	j.AppendLog(fmt.Sprintf("[podmanBuilder] running: podman %s", strings.Join(buildArgs, " ")))

	stdout, stderr, err := b.runner.Run(ctx, "podman", buildArgs...)
	if stdout != nil && len(stdout) > 0 {
		j.AppendLog("[podmanBuilder] stdout: " + string(stdout))
	}
	if stderr != nil && len(stderr) > 0 {
		j.AppendLog("[podmanBuilder] stderr: " + string(stderr))
	}

	if err != nil {
		if ctx.Err() != nil {
			_ = j.TransitionTo(StateCancelled)
			j.AppendLog("[podmanBuilder] build cancelled by context: " + ctx.Err().Error())
			return
		}
		_ = j.TransitionTo(StateFailed)
		j.AppendLog("[podmanBuilder] podman build failed: " + err.Error())
		return
	}

	// --- Inspect phase (sink-side verification: image must exist) ---
	inspectArgs := []string{"image", "inspect", tag}
	j.AppendLog(fmt.Sprintf("[podmanBuilder] running: podman %s", strings.Join(inspectArgs, " ")))

	iOut, iErr, iErr2 := b.runner.Run(ctx, "podman", inspectArgs...)
	if iOut != nil && len(iOut) > 0 {
		j.AppendLog("[podmanBuilder] inspect stdout: " + string(iOut))
	}
	if iErr != nil && len(iErr) > 0 {
		j.AppendLog("[podmanBuilder] inspect stderr: " + string(iErr))
	}

	if iErr2 != nil {
		if ctx.Err() != nil {
			_ = j.TransitionTo(StateCancelled)
			j.AppendLog("[podmanBuilder] inspect cancelled by context: " + ctx.Err().Error())
			return
		}
		_ = j.TransitionTo(StateFailed)
		j.AppendLog("[podmanBuilder] podman image inspect failed (image does not exist): " + iErr2.Error())
		return
	}

	// Image exists and is pullable — set the tag and mark success.
	j.SetImageTag(tag)
	_ = j.TransitionTo(StateSucceeded)
	j.AppendLog(fmt.Sprintf("[podmanBuilder] build succeeded: image %s exists and is inspectable", tag))
}
