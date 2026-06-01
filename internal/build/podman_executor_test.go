// Package build provides the build orchestration service for Helix Cluster OS.
package build

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// fakeOrchestratorCommandRunner captures every call so tests can assert exact
// argv, and returns pre-configured (stdout, stderr, error) per call index.
// ---------------------------------------------------------------------------

type fakeRunCall struct {
	name   string
	args   []string
	stdout []byte
	stderr []byte
	err    error
}

type fakeOrchestratorCommandRunner struct {
	calls   []fakeRunCall // recorded calls (populated by Run)
	returns []struct {    // pre-loaded per-call return values (FIFO)
		stdout []byte
		stderr []byte
		err    error
	}
}

// Enqueue pre-loads the next Run() return value.
func (f *fakeOrchestratorCommandRunner) enqueue(stdout, stderr []byte, err error) {
	f.returns = append(f.returns, struct {
		stdout []byte
		stderr []byte
		err    error
	}{stdout, stderr, err})
}

func (f *fakeOrchestratorCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	call := fakeRunCall{name: name, args: args}
	idx := len(f.calls)
	f.calls = append(f.calls, call)

	if idx < len(f.returns) {
		r := f.returns[idx]
		f.calls[idx].stdout = r.stdout
		f.calls[idx].stderr = r.stderr
		f.calls[idx].err = r.err
		return r.stdout, r.stderr, r.err
	}
	// If no return pre-loaded, return success with empty output.
	return nil, nil, nil
}

// ---------------------------------------------------------------------------
// Helper: run a single job through the orchestrator with the given builder and
// wait for it to reach a terminal state, returning the final job snapshot.
// ---------------------------------------------------------------------------

func runOrchestratorJob(t *testing.T, builder OrchestratorBuilder, spec BuildSpec) *Job {
	t.Helper()
	orch := NewOrchestratorWithBuilder(1, cache.NewMemoryCache(), builder)
	orch.Start(context.Background())
	t.Cleanup(orch.Stop)

	ctx := context.Background()
	_, err := orch.SubmitBuild(ctx, spec)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		j, err := orch.GetBuildStatus(ctx, spec.ID)
		return err == nil && j.IsTerminal()
	}, 5*time.Second, 10*time.Millisecond)

	j, err := orch.GetBuildStatus(ctx, spec.ID)
	require.NoError(t, err)
	return j
}

// ---------------------------------------------------------------------------
// Unit tests for podmanOrchestratorBuilder via fake runner
// ---------------------------------------------------------------------------

// TestPodmanOrchestratorBuilder_SuccessPath asserts:
//   - exact podman build argv (including -t <tag>, <contextDir>)
//   - exact podman image inspect argv
//   - job reaches StateSucceeded with correct ImageTag
//   - stdout/stderr from the fake runner are captured in job logs
//
// MUTATION: remove the inspect call from Execute -> the second call (inspect)
// is absent, so assert on len(runner.calls)==2 fails.
func TestPodmanOrchestratorBuilder_SuccessPath(t *testing.T) {
	runner := &fakeOrchestratorCommandRunner{}
	// call 0: podman build => success
	runner.enqueue([]byte("Step 1/1 : RUN true\n"), nil, nil)
	// call 1: podman image inspect => success (image exists)
	runner.enqueue([]byte(`[{"Id":"abc123"}]`), nil, nil)

	b := NewPodmanOrchestratorBuilder(runner)
	spec := BuildSpec{
		ID:      "job-ok",
		RepoURL: "/tmp/ctx",
		Ref:     "v1",
	}
	j := runOrchestratorJob(t, b, spec)

	// State: COMPLETED (StateSucceeded).
	assert.Equal(t, StateSucceeded, j.State,
		"fake-runner success must drive job to StateSucceeded")

	// ImageTag: "helix-build/<id>:<ref>"
	wantTag := fmt.Sprintf("helix-build/%s:%s", spec.ID, spec.Ref)
	assert.Equal(t, wantTag, j.ImageTag,
		"image tag must be derived from job ID and ref")

	// Exact argv — two runner calls must have been made.
	require.Len(t, runner.calls, 2,
		"exactly two runner calls: podman build + podman image inspect")

	// Call 0: podman build
	buildCall := runner.calls[0]
	assert.Equal(t, "podman", buildCall.name)
	// Must contain: build, -t <tag>, and context dir as last arg.
	assert.Equal(t, "build", buildCall.args[0])
	assert.Contains(t, buildCall.args, "-t",
		"build call must include -t flag")
	tagIdx := indexOf(buildCall.args, "-t")
	require.True(t, tagIdx >= 0 && tagIdx+1 < len(buildCall.args))
	assert.Equal(t, wantTag, buildCall.args[tagIdx+1],
		"build -t arg must be the derived tag")
	assert.Equal(t, spec.RepoURL, buildCall.args[len(buildCall.args)-1],
		"last build arg must be the context directory (RepoURL)")

	// Call 1: podman image inspect
	inspectCall := runner.calls[1]
	assert.Equal(t, "podman", inspectCall.name)
	assert.Equal(t, []string{"image", "inspect", wantTag}, inspectCall.args,
		"inspect call must target the exact same tag that was built")

	// Logs: stdout from build and inspect must be captured.
	logs := strings.Join(j.GetLogs(), "\n")
	assert.Contains(t, logs, "Step 1/1 : RUN true",
		"build stdout must be streamed into job logs")
	assert.Contains(t, logs, `"Id":"abc123"`,
		"inspect stdout must be streamed into job logs")
}

// TestPodmanOrchestratorBuilder_BuildFails asserts that a non-nil error from
// the runner on the build call drives the job to StateFailed and leaves
// ImageTag empty, with no inspect call attempted.
//
// MUTATION: remove the `if err != nil` guard in Execute after the build call
// -> the inspect call is attempted on a non-existent image, the inspect also
// fails, and the job still goes to FAILED — but the assertion that exactly ONE
// call was made fails (two would be made). Use the call-count assertion.
func TestPodmanOrchestratorBuilder_BuildFails(t *testing.T) {
	runner := &fakeOrchestratorCommandRunner{}
	// call 0: podman build => failure
	runner.enqueue(nil, []byte("error: FROM busybox: not found\n"), errors.New("exit status 1"))

	b := NewPodmanOrchestratorBuilder(runner)
	spec := BuildSpec{
		ID:      "job-buildfail",
		RepoURL: "/tmp/ctx",
		Ref:     "v2",
	}
	j := runOrchestratorJob(t, b, spec)

	assert.Equal(t, StateFailed, j.State,
		"build runner error must drive job to StateFailed")
	assert.Empty(t, j.ImageTag,
		"a failed build must not set an image tag")

	// Only one runner call: no inspect on build failure.
	assert.Len(t, runner.calls, 1,
		"build failure must not attempt a podman image inspect")

	// The stderr from the build must be captured in logs.
	logs := strings.Join(j.GetLogs(), "\n")
	assert.Contains(t, logs, "not found",
		"build stderr must be streamed into job logs")
}

// TestPodmanOrchestratorBuilder_InspectFails asserts that a non-nil error from
// the runner on the inspect call (after a successful build) drives the job to
// StateFailed, proving that the sink-side check is load-bearing.
//
// MUTATION: remove the `if iErr2 != nil` guard after the inspect call
// -> the job reaches StateSucceeded even though inspect failed; the
// StateFailed assertion below fails. This is the key anti-bluff guard.
func TestPodmanOrchestratorBuilder_InspectFails(t *testing.T) {
	runner := &fakeOrchestratorCommandRunner{}
	// call 0: podman build => success (image was "built")
	runner.enqueue([]byte("Successfully tagged helix-build/job-inspectfail:v3\n"), nil, nil)
	// call 1: podman image inspect => FAILURE (image does not actually exist)
	runner.enqueue(nil, []byte("Error: image not known\n"), errors.New("exit status 125"))

	b := NewPodmanOrchestratorBuilder(runner)
	spec := BuildSpec{
		ID:      "job-inspectfail",
		RepoURL: "/tmp/ctx",
		Ref:     "v3",
	}
	j := runOrchestratorJob(t, b, spec)

	// The inspect failure must drive StateFailed even though the build "succeeded".
	// This is the sink-side guard: build claiming success but image not existing
	// is a real failure.
	assert.Equal(t, StateFailed, j.State,
		"inspect failure must drive StateFailed (image does not exist on sink)")
	assert.Empty(t, j.ImageTag,
		"a build where inspect fails must not set an image tag")

	require.Len(t, runner.calls, 2,
		"both build and inspect must have been called")

	logs := strings.Join(j.GetLogs(), "\n")
	assert.Contains(t, logs, "image not known",
		"inspect stderr must be captured in job logs")
}

// TestPodmanOrchestratorBuilder_DockerfilePath asserts that a non-empty
// DockerfilePath is forwarded to podman build as -f <path>.
//
// MUTATION: remove the DockerfilePath → -f propagation in Execute
// -> the "-f" flag is absent from the build call args; the Contains assert fails.
func TestPodmanOrchestratorBuilder_DockerfilePath(t *testing.T) {
	runner := &fakeOrchestratorCommandRunner{}
	runner.enqueue(nil, nil, nil) // build succeeds
	runner.enqueue(nil, nil, nil) // inspect succeeds

	b := NewPodmanOrchestratorBuilder(runner)
	spec := BuildSpec{
		ID:             "job-df",
		RepoURL:        "/tmp/ctx",
		Ref:            "v4",
		DockerfilePath: "/tmp/ctx/custom.Dockerfile",
	}
	j := runOrchestratorJob(t, b, spec)

	assert.Equal(t, StateSucceeded, j.State)

	buildArgs := runner.calls[0].args
	fIdx := indexOf(buildArgs, "-f")
	require.True(t, fIdx >= 0 && fIdx+1 < len(buildArgs),
		"-f flag must be present in build call when DockerfilePath is set")
	assert.Equal(t, spec.DockerfilePath, buildArgs[fIdx+1],
		"-f must be followed by the DockerfilePath")
}

// TestPodmanOrchestratorBuilder_BuildArgs asserts that job.BuildArgs entries
// are forwarded as --build-arg K=V to podman build.
//
// MUTATION: skip the BuildArgs loop in Execute -> "--build-arg" is absent
// from the argv; the Contains assert fails.
func TestPodmanOrchestratorBuilder_BuildArgs(t *testing.T) {
	runner := &fakeOrchestratorCommandRunner{}
	runner.enqueue(nil, nil, nil) // build
	runner.enqueue(nil, nil, nil) // inspect

	b := NewPodmanOrchestratorBuilder(runner)
	spec := BuildSpec{
		ID:        "job-bargs",
		RepoURL:   "/tmp/ctx",
		Ref:       "v5",
		BuildArgs: map[string]string{"GO_VERSION": "1.24"},
	}
	j := runOrchestratorJob(t, b, spec)

	assert.Equal(t, StateSucceeded, j.State)

	buildArgs := runner.calls[0].args
	joined := strings.Join(buildArgs, " ")
	assert.Contains(t, joined, "--build-arg",
		"build call must include --build-arg when BuildArgs is set")
	assert.Contains(t, joined, "GO_VERSION=1.24",
		"build arg value must be forwarded verbatim")
}

// TestPodmanOrchestratorBuilder_DefaultRefIsLatest asserts that an empty Ref
// is normalised to "latest" in the derived image tag.
//
// MUTATION: remove the `if ref == "" { ref = "latest" }` guard in Execute
// -> the tag would be "helix-build/<id>:" (empty ref); the assert on the
// "latest" suffix fails.
func TestPodmanOrchestratorBuilder_DefaultRefIsLatest(t *testing.T) {
	runner := &fakeOrchestratorCommandRunner{}
	runner.enqueue(nil, nil, nil)
	runner.enqueue(nil, nil, nil)

	b := NewPodmanOrchestratorBuilder(runner)
	spec := BuildSpec{
		ID:      "job-noref",
		RepoURL: "/tmp/ctx",
		Ref:     "", // empty
	}
	j := runOrchestratorJob(t, b, spec)

	assert.Equal(t, StateSucceeded, j.State)
	assert.Equal(t, "helix-build/job-noref:latest", j.ImageTag,
		"empty Ref must default to 'latest' in the image tag")

	// Also assert the -t arg in the build call.
	tagIdx := indexOf(runner.calls[0].args, "-t")
	require.True(t, tagIdx >= 0 && tagIdx+1 < len(runner.calls[0].args))
	assert.Equal(t, "helix-build/job-noref:latest", runner.calls[0].args[tagIdx+1])
}

// TestPodmanOrchestratorBuilder_CacheShortCircuit asserts that submitting a
// job for which the cache already holds the artifact digest short-circuits the
// build: the orchestrator registers the artifact and the job completes without
// invoking the real builder's Execute (no runner calls).
//
// This tests the Orchestrator's cache-hit path, which wraps the builder. The
// orchestrator checks whether a prior artifact exists for the job; if so it
// marks the job succeeded immediately without calling Execute.
//
// NOTE: The current Orchestrator delegates entirely to the OrchestratorBuilder
// and does not itself implement a pre-build cache check.  The cache is used
// post-hoc (RegisterArtifact / GetArtifactDigest). The test below therefore
// demonstrates the RegisterArtifact/GetArtifactDigest round-trip (cache is
// wired correctly) rather than a "skip Execute on cache hit" path — that
// optimisation is not yet in the Orchestrator. The mutation guard is: removing
// RegisterArtifact or GetArtifactDigest breaks the round-trip assertion.
//
// MUTATION: remove orch.RegisterArtifact or GetArtifactDigest -> the ok/digest
// assertions below fail, proving the cache wiring is real.
func TestPodmanOrchestratorBuilder_CacheShortCircuit(t *testing.T) {
	c := cache.NewMemoryCache()

	// Pre-populate the cache with a known artifact.
	data := []byte("cached-artifact-bytes")
	digest := cache.Digest(data)
	require.NoError(t, c.Put(digest, data))

	runner := &fakeOrchestratorCommandRunner{}
	runner.enqueue(nil, nil, nil) // build (still runs — no short-circuit in orchestrator yet)
	runner.enqueue(nil, nil, nil) // inspect

	b := NewPodmanOrchestratorBuilder(runner)
	orch := NewOrchestratorWithBuilder(1, c, b)
	orch.Start(context.Background())
	defer orch.Stop()

	ctx := context.Background()
	spec := BuildSpec{ID: "job-cache", RepoURL: "/tmp/ctx", Ref: "v6"}
	_, err := orch.SubmitBuild(ctx, spec)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		j, err := orch.GetBuildStatus(ctx, spec.ID)
		return err == nil && j.IsTerminal()
	}, 5*time.Second, 10*time.Millisecond)

	// Associate the cached artifact with the completed build.
	orch.RegisterArtifact(spec.ID, digest)

	// CACHE ROUND-TRIP: the artifact digest must be retrievable and match the
	// bytes stored in the cache.
	gotDigest, ok := orch.GetArtifactDigest(spec.ID)
	require.True(t, ok, "registered artifact digest must be retrievable")
	assert.Equal(t, digest, gotDigest)

	gotBytes, err := orch.Cache().Get(gotDigest)
	require.NoError(t, err)
	assert.Equal(t, data, gotBytes,
		"cache round-trip: bytes retrieved by digest must match what was stored")
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

// indexOf returns the first index of needle in haystack, or -1 if absent.
func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}
