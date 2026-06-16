package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"testing"
	"time"

	pkgbuild "github.com/HelixDevelopment/helix_cluster/pkg/build"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// internal/build adversarial SECOND PASS (build_adversarial2_test.go).
//
// These probes target bugs the first pass missed: an UNBOUNDED worker-pool size
// (resource-exhaustion DoS), goroutine-leak on Stop after cancel, digest
// content-coverage, and byte-exact artifact persistence. Every probe is
// hermetic (no real podman/docker/network) and timeout-guarded so the file can
// never hang. Concurrency is capped well under 8 real goroutines for the -race
// probe.
// =============================================================================

// ---------------------------------------------------------------------------
// (E) REAL BUG — UNBOUNDED WORKER POOL (resource-exhaustion DoS).
//
// NewOrchestratorWithBuilder floors workers<1 to 1 but applied NO upper clamp.
// Start() spawns EXACTLY o.workers goroutines (orchestrator.go: the
// `for i := 0; i < o.workers; i++ { go o.worker(...) }` loop). The worker count
// flows straight from the PUBLIC constructor NewOrchestrator(workers, c) with no
// validation anywhere in the package — a caller wiring it from an operator knob
// (e.g. HELIX_BUILD_WORKERS) with a hostile/typo'd value such as 1_000_000_000
// would have Start() attempt to launch a billion goroutines and exhaust the
// process. The 100-slot queue means anything beyond a small multiple of that is
// pure waste regardless, so a defensive cap is strictly correct.
//
// FIX: clamp workers to maxWorkers (256) in NewOrchestratorWithBuilder.
//
// MUTATION PROOF: delete the `if workers > maxWorkers { workers = maxWorkers }`
// clause and this test FAILS (orch.workers == 1e9). With the clause it PASSES.
//
// SINK-SIDE: we both (a) read the stored o.workers (the value Start will loop
// over) and (b) Start the pool and confirm the live goroutine delta is bounded,
// proving the clamp actually limits the spawned goroutines, not just a field.
// ---------------------------------------------------------------------------

func TestAdversarial2_Orchestrator_WorkerCount_IsClamped(t *testing.T) {
	const hostile = 1_000_000_000

	orch := NewOrchestratorWithBuilder(hostile, cache.NewMemoryCache(), &fakeOrchestratorBuilder{})

	// (a) The stored worker count — the exact bound Start() loops over — must be
	// clamped. Without the fix this is 1e9.
	require.LessOrEqualf(t, orch.workers, maxWorkers,
		"worker pool size not clamped: %d workers would be spawned (resource-exhaustion DoS)", orch.workers)
	require.GreaterOrEqual(t, orch.workers, 1, "clamp must still floor to at least one worker")

	// (b) Sink-side: starting the pool must not spawn an unbounded number of
	// goroutines. Bound the live delta generously (clamp + slack) so this proves
	// the cap took effect end-to-end, not merely that a field was set.
	before := runtime.NumGoroutine()
	orch.Start(context.Background())
	defer orch.Stop()

	// Give the runtime a moment to actually schedule the spawned workers.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() >= before
	}, time.Second, 5*time.Millisecond)

	delta := runtime.NumGoroutine() - before
	assert.LessOrEqualf(t, delta, maxWorkers+16,
		"Start spawned ~%d goroutines for a hostile worker count; clamp did not take effect", delta)
}

// Floor still works (sanity guard that the new clamp didn't break the lower bound).
func TestAdversarial2_Orchestrator_WorkerCount_FloorPreserved(t *testing.T) {
	orch := NewOrchestratorWithBuilder(0, cache.NewMemoryCache(), &fakeOrchestratorBuilder{})
	assert.Equal(t, 1, orch.workers, "workers<1 must floor to exactly 1")
	orch2 := NewOrchestratorWithBuilder(-5, cache.NewMemoryCache(), &fakeOrchestratorBuilder{})
	assert.Equal(t, 1, orch2.workers, "negative workers must floor to exactly 1")
}

// ---------------------------------------------------------------------------
// (F) GOROUTINE-LEAK ON CANCEL/STOP  (-race).
//
// After submitting jobs and Stop()ing the orchestrator (which cancels the
// worker context and wg.Wait()s), no worker goroutine may survive. A leaked
// worker would (i) keep draining the queue after shutdown and (ii) accumulate
// across a process's many build epochs. We measure the goroutine count before
// the orchestrator is created and after Stop returns: it must return to the
// baseline (no residual workers). This is the ONE -race run for this file.
//
// SINK-SIDE: post-Stop goroutine count <= baseline+slack, AND every submitted
// job reached a terminal state (no job abandoned mid-flight by a vanished
// worker).
// ---------------------------------------------------------------------------

func TestAdversarial2_Orchestrator_NoGoroutineLeakOnStop(t *testing.T) {
	baseline := runtime.NumGoroutine()

	orch := newFakeOrchestrator(4, cache.NewMemoryCache())
	orch.Start(context.Background())
	ctx := context.Background()

	const n = 20
	for i := 0; i < n; i++ {
		_, err := orch.SubmitBuild(ctx, BuildSpec{ID: fmt.Sprintf("g-%02d", i), RepoURL: "r"})
		require.NoError(t, err)
	}

	// Let workers drain to terminal states.
	require.Eventually(t, func() bool {
		for i := 0; i < n; i++ {
			j, err := orch.GetBuildStatus(ctx, fmt.Sprintf("g-%02d", i))
			if err != nil || !j.IsTerminal() {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond)

	orch.Stop()

	// After Stop, the 4 worker goroutines must be gone. Poll briefly to let the
	// scheduler reclaim them, then assert we are back near baseline.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline+2
	}, 3*time.Second, 20*time.Millisecond,
		"worker goroutines leaked after Stop (count=%d, baseline=%d)",
		runtime.NumGoroutine(), baseline)

	// SINK-SIDE: every job is terminal — none abandoned by a vanished worker.
	for i := 0; i < n; i++ {
		j, err := orch.GetBuildStatus(ctx, fmt.Sprintf("g-%02d", i))
		require.NoError(t, err)
		assert.True(t, j.IsTerminal(), "job %s left non-terminal", j.ID)
	}
}

// ---------------------------------------------------------------------------
// (G) STOP IS IDEMPOTENT / SURVIVES NEVER-STARTED.
//
// Stop reads o.cancel (nil if Start was never called) and wg.Wait()s. Calling
// Stop on a never-started orchestrator, and calling Stop twice, must neither
// panic nor block. A double-cancel or nil deref here would wedge shutdown.
// Timeout-guarded so a regression that blocks is reported as a FAIL, not a hang.
// ---------------------------------------------------------------------------

func TestAdversarial2_Orchestrator_Stop_IdempotentAndNeverStarted(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Never started.
		NewOrchestrator(2, cache.NewMemoryCache()).Stop()

		// Started, then stopped twice.
		orch := newFakeOrchestrator(2, cache.NewMemoryCache())
		orch.Start(context.Background())
		orch.Stop()
		orch.Stop()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked (never-started or double-Stop wedged shutdown)")
	}
}

// ---------------------------------------------------------------------------
// (H) DIGEST COVERS CONTENT  (ExecBuilder content-addressing integrity).
//
// The exec builder content-addresses its artifact: digest = sha256(bytes),
// imageTag embeds that digest. A correct content-address MUST change when the
// produced bytes change, and MUST equal an independently computed sha256 of the
// exact bytes. If the digest were computed over anything other than the full
// content (e.g. a prefix, or a constant), differing outputs would collide.
//
// We run two builds whose commands emit DIFFERENT artifact bytes and assert:
//   - each recorded digest == independent sha256 of that build's bytes
//   - the two digests DIFFER (content actually covered)
//   - the imageTag embeds the digest
// Hermetic: just `sh -c` writing to $HELIX_OUTPUT. Timeout via runViaSeam.
// ---------------------------------------------------------------------------

func TestAdversarial2_ExecBuilder_DigestCoversFullContent(t *testing.T) {
	run := func(id, payload string) (digest string, size int, tag string) {
		b := &ExecBuilder{
			Command: "printf '%s' \"" + payload + "\" > \"$HELIX_OUTPUT\"",
		}
		j := runViaSeam(t, b, &pkgbuild.Job{ID: id, RepoURL: "r", Ref: "v1"})
		require.Equal(t, pkgbuild.StateSucceeded, j.State, "build %s must succeed", id)
		art, ok := b.ArtifactFor(id)
		require.True(t, ok, "artifact not recorded for %s", id)
		return art.Digest, art.Size, art.ImageTag
	}

	payloadA := "alpha-CONTENT-1234567890"
	payloadB := "alpha-CONTENT-1234567890-DELTA" // shares a long prefix with A

	dA, sizeA, tagA := run("dig-a", payloadA)
	dB, _, _ := run("dig-b", payloadB)

	// Independent oracle: digest must equal sha256 of the EXACT produced bytes.
	wantA := sha256.Sum256([]byte(payloadA))
	assert.Equal(t, hex.EncodeToString(wantA[:]), dA,
		"recorded digest does not match sha256 of the produced artifact bytes")
	assert.Equal(t, len(payloadA), sizeA, "recorded size does not match produced byte length")

	// Content coverage: a longer payload sharing A's full prefix must NOT collide.
	// If the digest covered only a prefix/constant, these would be equal.
	assert.NotEqual(t, dA, dB,
		"differing artifact contents produced the SAME digest (digest does not cover full content)")

	// The advertised image tag must embed the real digest.
	assert.Contains(t, tagA, dA, "image tag does not embed the content digest")
}

// ---------------------------------------------------------------------------
// (I) ARTIFACT BYTES ARE PERSISTED BYTE-EXACT (no truncation/corruption).
//
// On success ExecBuilder.Put(digest, data)s the REAL produced bytes into the
// ArtifactStore. Retrieve them and assert byte-for-byte identity with what the
// command emitted, keyed by the SAME digest the builder advertised. This proves
// the artifact-handling path neither loses nor corrupts bytes between command
// output and the store, and that the store key is the genuine content digest.
// ---------------------------------------------------------------------------

func TestAdversarial2_ExecBuilder_ArtifactPersistedByteExact(t *testing.T) {
	store := cache.NewMemoryCache()
	// Emit bytes that include a NUL and non-ASCII to catch naive string handling.
	// printf %b interprets \xNN escapes; produce 3 bytes: 0x00 0xFF 'Z'.
	b := &ExecBuilder{
		Command:       `printf '%b' '\000\377Z' > "$HELIX_OUTPUT"`,
		ArtifactStore: store,
	}
	j := runViaSeam(t, b, &pkgbuild.Job{ID: "bytes-1", RepoURL: "r", Ref: "v1"})
	require.Equal(t, pkgbuild.StateSucceeded, j.State)

	art, ok := b.ArtifactFor("bytes-1")
	require.True(t, ok)

	got, err := store.Get(art.Digest)
	require.NoError(t, err, "artifact bytes not found in store under advertised digest")

	want := []byte{0x00, 0xFF, 'Z'}
	assert.Equal(t, want, got, "persisted artifact bytes differ from command output (corruption/truncation)")

	// Cross-check: stored bytes hash to the advertised digest.
	sum := sha256.Sum256(got)
	assert.Equal(t, art.Digest, hex.EncodeToString(sum[:]),
		"store key is not the content digest of the stored bytes")
	assert.Equal(t, len(want), art.Size, "recorded size mismatches persisted bytes")
}

// ---------------------------------------------------------------------------
// (J) FAILED STEP -> FAILED RESULT, NEVER SUCCESS (CLAUDE-1 anti-PASS-bluff).
//
// A command that exits non-zero must drive the job to StateFailed with NO image
// tag and NO persisted success artifact — the exact failure CLAUDE-1 forbids
// masking. Mutation: if the builder ever reported such a build as succeeded,
// the State assertion bites. We run via the real seam (worker path).
// ---------------------------------------------------------------------------

func TestAdversarial2_ExecBuilder_NonZeroExit_IsFailureNotSuccess(t *testing.T) {
	store := cache.NewMemoryCache()
	b := &ExecBuilder{
		// Write a plausible artifact THEN fail: a naive builder that reads the
		// output regardless of exit code would wrongly succeed.
		Command:       `printf 'looks-built' > "$HELIX_OUTPUT"; exit 7`,
		ArtifactStore: store,
	}
	j := runViaSeam(t, b, &pkgbuild.Job{ID: "fail-7", RepoURL: "r", Ref: "v1"})

	assert.Equal(t, pkgbuild.StateFailed, j.State,
		"non-zero exit must yield StateFailed, not success")
	assert.Empty(t, j.ImageTag, "failed build must not advertise an image tag")

	art, ok := b.ArtifactFor("fail-7")
	require.True(t, ok)
	assert.Equal(t, 7, art.ExitCode, "real process exit code must be recorded")
	assert.Empty(t, art.Digest, "failed build must not record a success digest")
	assert.Empty(t, art.ImageTag, "failed build must not record a success image tag")
}

