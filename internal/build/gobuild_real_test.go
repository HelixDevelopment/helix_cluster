package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pkgbuild "github.com/HelixDevelopment/helix_cluster/pkg/build"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/evidence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gobuild_real_test.go proves the §7.1 anti-bluff claim that the build path
// produces a REAL, runnable artifact NOW (no container runtime required), using
// the real Go toolchain via the real ExecBuilder driven through the real
// pkg/build.Service worker-pool seam.
//
// The original bluff (AUDIT_REPORT §4): pkg/build's simulatedBuilder produced a
// fabricated ImageTag via time.After + a magic "fail" string; no compiler ran,
// no artifact ever existed, yet tests asserted StateSucceeded. These tests close
// that gap with end-user-visible, sink-side evidence: a compiler is actually
// invoked, an executable file is actually written, and that file is actually
// EXECUTED and observed to print a unique per-run token. A fabricated success
// cannot pass, because no fabricated path can make a freshly-compiled binary
// print a token the test minted at runtime.
//
// Evidence (Constitution §7.1 substrate, pkg/testing/evidence):
//   - e.MustDelta proves a real before/after state change (no artifact -> real
//     artifact bytes; binary absent -> binary present).
//   - e.MustPositive proves the run-time token appears in (a) the artifact the
//     builder produced and (b) the actual program output, i.e. positive evidence
//     of end-user-visible behavior, not mere absence of error.

// goToolchainAvailable reports whether a `go` binary we can invoke is on PATH.
// Used to SKIP (never FAIL) on a host with no Go toolchain.
func goToolchainAvailable(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("SKIP-OK: no `go` toolchain on PATH; cannot run a real go build: %v", err)
	}
	return path
}

// writeGoModule writes a minimal, self-contained Go main package into dir whose
// program prints the given token to stdout. This is a REAL source tree the
// builder will compile — not a fixture string.
func writeGoModule(t *testing.T, dir, token string) {
	t.Helper()
	mod := "module helixbuildfixture\n\ngo 1.21\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644))

	// The program embeds the token via a build-time -ldflags injection target so
	// the SAME source compiles to a DIFFERENT, token-bearing binary each run —
	// defeating any caching/fabrication that ignores the real inputs.
	main := "package main\n\n" +
		"import \"fmt\"\n\n" +
		"var Token = \"UNSET\"\n\n" +
		"func main() { fmt.Println(\"helix-fixture-output:\" + Token) }\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0o644))
}

// TestExecBuilder_RealGoBuild_ProducesRunnableBinary is the load-bearing proof
// that the build path is genuinely real and runnable NOW.
//
// It compiles a real Go module with the real toolchain (injecting a unique
// per-run token via -ldflags), captures the produced executable as the build
// artifact, then EXECUTES that artifact and asserts it prints the token. The
// produced ImageTag is content-addressed over the REAL compiled bytes.
//
// MUTATION GUARDS:
//   - Re-flag ExecBuilder.Build to fabricate success + a constant tag: the
//     "binary did not exist" -> "binary exists" delta fails (no file is written),
//     and executing the (absent/garbage) artifact fails to print the token.
//   - Make the build ignore exit codes / skip the real compile: `go build` never
//     runs, no ELF/Mach-O is produced, MustDelta on the artifact bytes fails.
//   - Hardcode the token into the artifact instead of compiling it: the token is
//     minted at test runtime and unknown to any source-checked-in fabrication,
//     so MustPositive on the EXECUTED program's stdout cannot pass without a
//     genuine compile+run.
func TestExecBuilder_RealGoBuild_ProducesRunnableBinary(t *testing.T) {
	goToolchainAvailable(t)
	e := evidence.NewT(t, "real-go-build-runnable-artifact", t.TempDir())
	token := "helixtok-" + e.Token()

	// A real Go source tree the builder will compile.
	srcDir := t.TempDir()
	writeGoModule(t, srcDir, token)

	store := cache.NewMemoryCache()

	// The build command invokes the REAL Go toolchain. It:
	//   - disables network/module fetches (GOFLAGS=-mod=mod, GOPROXY=off) so the
	//     compile is hermetic and fast,
	//   - injects the unique per-run token into main.Token via -ldflags,
	//   - writes the resulting executable to $HELIX_OUTPUT (the artifact path the
	//     ExecBuilder reads, digests, and stores).
	// Every byte of the artifact is the real compiler's output.
	cmd := "cd " + shellQuote(srcDir) + " && " +
		"GOFLAGS=-mod=mod GOPROXY=off CGO_ENABLED=0 " +
		"go build -ldflags " + shellQuote("-X main.Token="+token) +
		" -o \"$HELIX_OUTPUT\" ."

	b := &ExecBuilder{
		Command:       cmd,
		OutputName:    "helixfixture.bin",
		ArtifactStore: store,
	}

	j := runViaSeam(t, b, &pkgbuild.Job{ID: "go-build-1", RepoURL: srcDir, Ref: "v1"})

	// REAL outcome: the build succeeded through the real worker-pool seam.
	require.Equal(t, pkgbuild.StateSucceeded, j.State,
		"a real `go build` of a valid module must succeed; logs:\n%s",
		strings.Join(j.GetLogs(), "\n"))

	art, ok := b.ArtifactFor("go-build-1")
	require.True(t, ok, "a real build must record an artifact")
	require.Equal(t, 0, art.ExitCode)

	// SINK-SIDE EVIDENCE 1: a non-empty binary really exists in the store, keyed
	// by a digest over the compiler's real output bytes.
	require.NotEmpty(t, art.Digest)
	binBytes, err := store.Get(art.Digest)
	require.NoError(t, err)
	require.Equal(t, art.Size, len(binBytes))

	// DELTA: before the build there was no artifact (0 bytes); after, a real,
	// non-empty compiled binary exists. A fabricated/no-op builder cannot produce
	// this delta because no file is ever written.
	e.MustDelta(t, "compiled binary materialized from real go build",
		0, len(binBytes))
	assert.Greater(t, len(binBytes), 1024,
		"a real compiled Go binary is non-trivial in size; got %d bytes", len(binBytes))

	// DELTA: the content-addressed image tag is derived from the REAL bytes.
	wantDigest := cache.Digest(binBytes)
	require.Equal(t, wantDigest, art.Digest, "digest must hash the REAL compiled bytes")
	assert.Contains(t, j.ImageTag, "sha256:"+wantDigest,
		"image tag must be content-addressed to the real artifact, not fabricated")

	// SINK-SIDE EVIDENCE 2 (the strongest): write the stored binary back to disk,
	// mark it executable, RUN it, and observe it print the unique token. This is
	// end-user-visible behavior — the artifact is not just present, it WORKS.
	runDir := t.TempDir()
	binPath := filepath.Join(runDir, "helixfixture")
	require.NoError(t, os.WriteFile(binPath, binBytes, 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, runErr := exec.CommandContext(ctx, binPath).CombinedOutput()
	require.NoError(t, runErr, "the produced artifact must be a runnable executable; output:\n%s", string(out))

	// POSITIVE evidence: the EXECUTED program emitted the unique per-run token,
	// proving the artifact is the real, freshly-compiled program (not a stale or
	// fabricated stand-in). A bluff cannot make an absent/garbage binary print a
	// token minted at this test's runtime.
	e.MustPositive(t, "executed compiled artifact printed the unique run token",
		string(out), token)
	assert.Contains(t, string(out), "helix-fixture-output:"+token)

	if mpath, merr := e.Manifest(); merr == nil {
		t.Logf("evidence manifest written: %s", mpath)
	}
}

// TestExecBuilder_RealGoBuild_CompileErrorFails proves the genuine failure path:
// a Go source tree that does NOT compile must drive the job to StateFailed via a
// REAL non-zero compiler exit — never the old magic-string "fail" sentinel and
// never a fabricated success.
//
// MUTATION GUARD: ignore the real exit code / treat compile failure as success
// -> the StateFailed assertion fails, and no artifact/tag is produced.
func TestExecBuilder_RealGoBuild_CompileErrorFails(t *testing.T) {
	goToolchainAvailable(t)
	e := evidence.NewT(t, "real-go-build-compile-error", t.TempDir())

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "go.mod"),
		[]byte("module helixbroken\n\ngo 1.21\n"), 0o644))
	// Deliberately broken: references an undefined symbol -> real compiler error.
	broken := "package main\n\nfunc main() { thisSymbolDoesNotExist() }\n"
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(broken), 0o644))

	cmd := "cd " + shellQuote(srcDir) + " && GOFLAGS=-mod=mod GOPROXY=off CGO_ENABLED=0 " +
		"go build -o \"$HELIX_OUTPUT\" ."

	b := &ExecBuilder{Command: cmd, OutputName: "broken.bin"}
	j := runViaSeam(t, b, &pkgbuild.Job{ID: "go-build-broken", RepoURL: srcDir, Ref: "v1"})

	require.Equal(t, pkgbuild.StateFailed, j.State,
		"a real compile error must FAIL the job (real non-zero exit), not fabricate success")
	assert.Empty(t, j.ImageTag, "a failed compile must not produce an image tag")

	logs := strings.Join(j.GetLogs(), "\n")
	// The REAL compiler diagnostic must be streamed into the job logs (proves a
	// real compiler ran and really failed, not a canned message).
	assert.Contains(t, logs, "thisSymbolDoesNotExist",
		"the real compiler's diagnostic must be captured in the job logs")

	// DELTA: queued/running -> failed terminal state from a real compiler exit.
	e.MustDelta(t, "broken source drives job to real compiler-failure terminal state",
		string(pkgbuild.StateRunning), string(j.State))

	art, ok := b.ArtifactFor("go-build-broken")
	require.True(t, ok)
	assert.NotEqual(t, 0, art.ExitCode, "the recorded exit code must be the REAL non-zero compiler exit")
	assert.Empty(t, art.Digest, "a failed build must not record an artifact digest")
}

// shellQuote wraps s in single quotes for safe interpolation into the `sh -c`
// command line, escaping any embedded single quotes. Test paths come from
// t.TempDir() and contain no quotes, but this keeps the command construction
// honest and injection-safe.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
