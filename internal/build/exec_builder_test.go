package build

import (
	"context"
	"strings"
	"testing"
	"time"

	pkgbuild "github.com/HelixDevelopment/helix_cluster/pkg/build"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ExecBuilder is REAL, not a simulation. This guards the honesty marker so that
// nobody can quietly re-flag it as simulated (which would let a fabricated
// success masquerade as a real one).
//
// MUTATION: change Simulated() to `return true` -> this assert fails.
func TestExecBuilder_NotSimulated(t *testing.T) {
	var b ExecBuilder
	assert.False(t, b.Simulated(), "ExecBuilder must report itself as a real (non-simulated) builder")
	// And it must satisfy the real Builder seam from pkg/build.
	var _ pkgbuild.Builder = &b
}

// runViaSeam drives the given job to a terminal state through the REAL
// pkg/build.Service seam (worker pool -> Builder.Build) and returns a CLONED
// snapshot of the terminal job obtained via Service.Get.
//
// Why the seam instead of calling b.Build directly on a caller-owned *Job:
// Job.State is written under Job.mu inside TransitionTo, but Job.State has no
// exported lock-protected reader (GetState lives in pkg/build, outside this
// dir). Service.Get returns j.clone(), which reads every field under Job.mu —
// so reading clone.State here is fully synchronised against the worker
// goroutine's TransitionTo writes. Asserting on the clone is therefore
// race-free even though the build runs on a separate goroutine. It also
// exercises the genuine end-user path (submit -> worker -> terminal), which is
// what CLAUDE-1 requires us to prove.
func runViaSeam(t *testing.T, b *ExecBuilder, j *pkgbuild.Job) *pkgbuild.Job {
	t.Helper()
	svc := pkgbuild.NewServiceWithBuilder(1, b)
	svc.Start(context.Background())
	t.Cleanup(svc.Stop)

	require.NoError(t, svc.Submit(j))
	require.Eventually(t, func() bool {
		got, err := svc.Get(j.ID)
		// got is a clone; got.IsTerminal reads clone.State with no contention.
		return err == nil && got.IsTerminal()
	}, 5*time.Second, 10*time.Millisecond)

	got, err := svc.Get(j.ID)
	require.NoError(t, err)
	return got
}

// A job whose command writes a KNOWN string to stdout AND creates a KNOWN
// artifact file must: succeed, have that exact stdout string captured in the
// REAL job logs, and produce an image tag / digest derived from the REAL bytes
// the command wrote (verified by re-reading them from the artifact store).
//
// MUTATION: hardcode Build to always AppendLog a constant + TransitionTo
// StateSucceeded with a fixed tag (the classic "simulated success") -> the
// real-stdout substring assert fails (no "HELLO-FROM-EXEC-7f3a" line) AND the
// digest/round-trip assert fails (artifact bytes won't match the command's
// real output).
func TestExecBuilder_RealStdoutAndArtifactCaptured(t *testing.T) {
	store := cache.NewMemoryCache()
	b := &ExecBuilder{
		// Echo a known marker to stdout, then write distinct known bytes to the
		// artifact path the builder exports. Both are REAL command effects.
		Command:       `echo HELLO-FROM-EXEC-7f3a; printf 'ARTIFACT-BODY-9c2e' > "$HELIX_OUTPUT"`,
		ArtifactStore: store,
	}

	j := runViaSeam(t, b, &pkgbuild.Job{ID: "exec-ok", RepoURL: "r", Ref: "v1"})

	// REAL outcome: succeeded (read from the locked clone).
	assert.Equal(t, pkgbuild.StateSucceeded, j.State)

	// REAL stdout captured verbatim (line-by-line streaming).
	logs := strings.Join(j.GetLogs(), "\n")
	assert.Contains(t, logs, "stdout: HELLO-FROM-EXEC-7f3a",
		"the command's real stdout must be streamed into the job logs")

	// REAL artifact: digest is content-addressed over the bytes the command
	// actually wrote, and those exact bytes are retrievable from the store.
	art, ok := b.ArtifactFor("exec-ok")
	require.True(t, ok, "an artifact must be recorded for a successful exec build")
	assert.Equal(t, 0, art.ExitCode)
	assert.Equal(t, len("ARTIFACT-BODY-9c2e"), art.Size)

	wantDigest := cache.Digest([]byte("ARTIFACT-BODY-9c2e"))
	assert.Equal(t, wantDigest, art.Digest, "digest must hash the REAL artifact bytes")
	assert.Equal(t, j.ImageTag, art.ImageTag)
	assert.Contains(t, j.ImageTag, "sha256:"+wantDigest,
		"image tag must be content-addressed to the real output, not fabricated")

	got, err := store.Get(art.Digest)
	require.NoError(t, err)
	assert.Equal(t, []byte("ARTIFACT-BODY-9c2e"), got,
		"the stored artifact must be the exact bytes the command produced")
}

// A non-zero command must drive the job to FAILED, must NOT produce an image
// tag, and must capture the command's REAL stderr.
//
// MUTATION: hardcode Build to ignore the exit code and always TransitionTo
// StateSucceeded -> the StateFailed assert fails; and a "simulated success"
// that emits no real stderr fails the stderr-capture assert.
func TestExecBuilder_NonZeroExitFailsWithRealStderr(t *testing.T) {
	b := &ExecBuilder{
		// Emit a known marker to stderr, then exit non-zero. Both are REAL.
		Command: `echo BOOM-STDERR-d41c 1>&2; exit 7`,
	}

	j := runViaSeam(t, b, &pkgbuild.Job{ID: "exec-fail", RepoURL: "r", Ref: "v1"})

	assert.Equal(t, pkgbuild.StateFailed, j.State, "non-zero exit must FAIL the job")
	assert.Empty(t, j.ImageTag, "a failed build must not produce an image tag")

	logs := strings.Join(j.GetLogs(), "\n")
	assert.Contains(t, logs, "stderr: BOOM-STDERR-d41c",
		"the command's real stderr must be streamed into the job logs")
	assert.Contains(t, logs, "exited with code 7",
		"the REAL exit code (7) must be reported, not a constant")

	art, ok := b.ArtifactFor("exec-fail")
	require.True(t, ok)
	assert.Equal(t, 7, art.ExitCode, "the recorded exit code must be the REAL process exit code")
	assert.Empty(t, art.Digest, "a failed build must not record an artifact digest")
}

// A command that exits 0 but produces NO artifact at HELIX_OUTPUT is a real
// failure: the builder must NOT fabricate an artifact or a success.
//
// MUTATION: make Build treat a missing artifact file as success (e.g. default
// data to nil and proceed) -> the StateFailed assert fails.
func TestExecBuilder_ZeroExitButNoArtifactFails(t *testing.T) {
	b := &ExecBuilder{Command: `echo no-output-written`} // never touches $HELIX_OUTPUT

	j := runViaSeam(t, b, &pkgbuild.Job{ID: "exec-noart", RepoURL: "r", Ref: "v1"})

	assert.Equal(t, pkgbuild.StateFailed, j.State,
		"exit 0 with no produced artifact must fail, never fabricate success")
	assert.Empty(t, j.ImageTag)
	logs := strings.Join(j.GetLogs(), "\n")
	assert.Contains(t, logs, "produced no artifact")
}

// An empty Command must fail rather than fabricate a success.
//
// MUTATION: remove the empty-Command guard and let it fall through to a
// hardcoded success -> the StateFailed assert fails.
func TestExecBuilder_EmptyCommandFails(t *testing.T) {
	b := &ExecBuilder{} // zero value: no Command
	j := runViaSeam(t, b, &pkgbuild.Job{ID: "exec-empty", RepoURL: "r"})
	assert.Equal(t, pkgbuild.StateFailed, j.State)
	assert.Empty(t, j.ImageTag)
}

// Cancelling the context mid-build must drive the job to CANCELLED (operator
// abort), distinct from a real build failure.
//
// This test calls b.Build directly (not via the service seam) because it needs
// to control the context passed to a single in-flight build. The build runs on
// THIS goroutine and returns before any assertion, so all of TransitionTo's
// writes to Job.State happen-before the reads below on the same goroutine —
// there is no concurrent reader, hence no race. The cancellation goroutine only
// calls cancel(); it never touches the job.
//
// MUTATION: drop the `if ctx.Err() != nil` cancellation branch -> the job ends
// FAILED (killed process => non-zero exit), so the StateCancelled assert fails.
func TestExecBuilder_ContextCancelYieldsCancelled(t *testing.T) {
	b := &ExecBuilder{Command: `sleep 5; printf x > "$HELIX_OUTPUT"`}

	ctx, cancel := context.WithCancel(context.Background())
	j := &pkgbuild.Job{ID: "exec-cancel", RepoURL: "r", State: pkgbuild.StateRunning}

	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	b.Build(ctx, j) // synchronous: returns after the job is terminal

	// Safe to read j.State directly: Build ran and returned on this goroutine, so
	// its TransitionTo writes happen-before this read; no other goroutine touches j.
	require.True(t, j.IsTerminal(), "Build must leave the job in a terminal state")
	assert.Equal(t, pkgbuild.StateCancelled, j.State,
		"context cancellation must yield CANCELLED, not FAILED")
	assert.Empty(t, j.ImageTag)
}

// End-to-end through the real Builder seam: a pkg/build.Service wired with an
// ExecBuilder (NOT the simulatedBuilder) runs a real job and exposes the real
// result via the service's own Get(). This proves an end user submitting a job
// to the service gets REAL exec behavior.
//
// MUTATION: have ExecBuilder.Build skip TransitionTo(StateSucceeded) -> the
// Eventually(StateSucceeded) wait times out and the test fails.
func TestExecBuilder_ThroughServiceSeam(t *testing.T) {
	store := cache.NewMemoryCache()
	b := &ExecBuilder{
		Command:       `echo SEAM-OK-11ab; printf 'seam-bytes' > "$HELIX_OUTPUT"`,
		ArtifactStore: store,
	}
	svc := pkgbuild.NewServiceWithBuilder(1, b)
	svc.Start(context.Background())
	defer svc.Stop()

	require.NoError(t, svc.Submit(&pkgbuild.Job{ID: "seam-1", RepoURL: "r", Ref: "v2"}))

	require.Eventually(t, func() bool {
		j, err := svc.Get("seam-1")
		return err == nil && j.State == pkgbuild.StateSucceeded
	}, 5*time.Second, 20*time.Millisecond)

	j, err := svc.Get("seam-1")
	require.NoError(t, err)
	assert.Contains(t, strings.Join(j.GetLogs(), "\n"), "stdout: SEAM-OK-11ab")
	assert.Contains(t, j.ImageTag, "sha256:"+cache.Digest([]byte("seam-bytes")))

	got, err := store.Get(cache.Digest([]byte("seam-bytes")))
	require.NoError(t, err)
	assert.Equal(t, []byte("seam-bytes"), got)
}
