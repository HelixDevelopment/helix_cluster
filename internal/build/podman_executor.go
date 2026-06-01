// Package build provides the build orchestration service for Helix Cluster OS.
package build

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// OrchestratorCommandRunner is the injectable seam for running external commands
// inside the Orchestrator's build executor. Unit tests inject a fakeOrchestratorRunner;
// production code uses realOrchestratorRunner which shells out via os/exec.
type OrchestratorCommandRunner interface {
	// Run executes the named command with the given arguments.
	// It returns combined stdout, stderr bytes and any error.
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// realOrchestratorRunner is the production OrchestratorCommandRunner that shells out
// via os/exec.
type realOrchestratorRunner struct{}

func (realOrchestratorRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// OrchestratorBuilder is the interface through which the Orchestrator delegates
// actual build execution. It mirrors pkg/build.Builder but operates on the
// internal *Job type so the Orchestrator's state machine remains the authority
// over state transitions.
//
// Contract: Execute MUST drive the job to a terminal state (StateSucceeded /
// StateFailed / StateCancelled) before returning, and on success MUST set
// j.ImageTag to a tag that refers to a real image that was built.
type OrchestratorBuilder interface {
	Execute(ctx context.Context, j *Job)
}

// podmanOrchestratorBuilder is the REAL OrchestratorBuilder implementation that
// drives `podman build` for a job and confirms the image exists with
// `podman image inspect`.
//
// It is injected into the Orchestrator via NewOrchestratorWithBuilder (or
// NewOrchestrator which defaults to the real os/exec runner). The runner seam
// allows unit tests to inject a fake runner to assert exact argv and outcomes
// without requiring a real podman daemon.
type podmanOrchestratorBuilder struct {
	runner OrchestratorCommandRunner
}

// NewPodmanOrchestratorBuilder creates a podmanOrchestratorBuilder backed by
// the given runner. Pass nil to use the production os/exec runner.
func NewPodmanOrchestratorBuilder(runner OrchestratorCommandRunner) OrchestratorBuilder {
	if runner == nil {
		runner = realOrchestratorRunner{}
	}
	return &podmanOrchestratorBuilder{runner: runner}
}

// Execute implements OrchestratorBuilder by running a real podman build and
// then confirming image existence via `podman image inspect`.
//
// Tag derivation: "helix-build/<jobID>:<ref>" where ref defaults to "latest".
//
// Steps:
//  1. Build: podman build -t <tag> [-f <dockerfile>] [--build-arg K=V ...] <contextDir>
//  2. Inspect: podman image inspect <tag>  (sink-side: image must exist)
//  3. On success: set j.ImageTag + TransitionTo(StateSucceeded)
//     On runner error or ctx cancel: TransitionTo(StateFailed/StateCancelled)
func (b *podmanOrchestratorBuilder) Execute(ctx context.Context, j *Job) {
	ref := j.Ref
	if ref == "" {
		ref = "latest"
	}
	tag := fmt.Sprintf("helix-build/%s:%s", j.ID, ref)

	// The "context directory" for podman build is carried in RepoURL.  For local
	// builds (integration tests / production local mode) this is a filesystem
	// path; production callers populate it from the checked-out repo root.
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

	j.AppendLog(fmt.Sprintf("[podman] running: podman %s", strings.Join(buildArgs, " ")))

	stdout, stderr, err := b.runner.Run(ctx, "podman", buildArgs...)
	if len(stdout) > 0 {
		j.AppendLog("[podman] stdout: " + string(stdout))
	}
	if len(stderr) > 0 {
		j.AppendLog("[podman] stderr: " + string(stderr))
	}

	if err != nil {
		if ctx.Err() != nil {
			_ = j.TransitionTo(StateCancelled)
			j.AppendLog("[podman] build cancelled by context: " + ctx.Err().Error())
			return
		}
		_ = j.TransitionTo(StateFailed)
		j.AppendLog("[podman] podman build failed: " + err.Error())
		return
	}

	// --- Inspect phase (sink-side verification: image must exist) ---
	inspectArgs := []string{"image", "inspect", tag}
	j.AppendLog(fmt.Sprintf("[podman] running: podman %s", strings.Join(inspectArgs, " ")))

	iOut, iErr, iErr2 := b.runner.Run(ctx, "podman", inspectArgs...)
	if len(iOut) > 0 {
		j.AppendLog("[podman] inspect stdout: " + string(iOut))
	}
	if len(iErr) > 0 {
		j.AppendLog("[podman] inspect stderr: " + string(iErr))
	}

	if iErr2 != nil {
		if ctx.Err() != nil {
			_ = j.TransitionTo(StateCancelled)
			j.AppendLog("[podman] inspect cancelled by context: " + ctx.Err().Error())
			return
		}
		_ = j.TransitionTo(StateFailed)
		j.AppendLog("[podman] podman image inspect failed (image does not exist): " + iErr2.Error())
		return
	}

	// Image exists and is inspectable — set tag and mark success.
	j.mu.Lock()
	j.ImageTag = tag
	j.mu.Unlock()
	_ = j.TransitionTo(StateSucceeded)
	j.AppendLog(fmt.Sprintf("[podman] build succeeded: image %s exists and is inspectable", tag))
}
