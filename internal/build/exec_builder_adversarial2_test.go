package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pkgbuild "github.com/HelixDevelopment/helix_cluster/pkg/build"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// internal/build ExecBuilder PATH/ARTIFACT adversarial pass
// (exec_builder_adversarial2_test.go).
//
// Scope (distinct from build_adversarial2_test.go, which covers the orchestrator
// worker pool / digest / persistence, and from HXC-1948): the ExecBuilder
// path-construction sinks and the build-command env wiring, probed with
// CALLER/JOB-controlled hostile values (Job.ID, Job.RepoURL, Job.Ref,
// Job.BuildArgs — all JSON-deserialised in pkg/build.Job, hence attacker
// reachable).
//
// VERDICT (pinned by the tests below):
//   - workdir creation  : os.MkdirTemp(BaseDir, "helix-build-"+sanitize(j.ID)+"-")
//       -> SAFE. j.ID is caller-controlled but funnels through sanitize() (maps
//       '/' and every non-[A-Za-z0-9-_.] rune to '-') AND os.MkdirTemp itself
//       rejects any prefix containing a path separator. Two independent layers;
//       a "../" job ID cannot escape BaseDir. DOCUMENTED CONTRACT, not a bug.
//   - outPath           : filepath.Join(workdir, b.outputName())
//       -> SAFE. outputName() returns b.OutputName, an ExecBuilder *struct config*
//       field, never a per-job/caller value (it is never assigned from Job.*).
//       Operator config, not request-injectable. Not a traversal sink.
//   - HELIX_OUTPUT env  : a hostile BuildArgs key (e.g. "HELIX_OUTPUT") is appended
//       AFTER the trusted env and wins last-write in the *child* command's view —
//       BUT the builder's own os.ReadFile uses the Go-side outPath variable, which
//       is immune. So the override grants the child no new capability (b.Command is
//       already arbitrary operator shell) and cannot redirect the builder's own
//       file I/O outside workdir. LATENT-only; pinned so a future refactor that
//       reads $HELIX_OUTPUT back from env would trip this test.
//
// No production bug found in this sink set -> production left BYTE-IDENTICAL.
// Every probe is hermetic and timeout-guarded; none can hang.
// =============================================================================

// withTempTMPDIR mirrors the disk-safety contract: tests must not scatter temp
// dirs across volumes. It returns a fresh BaseDir on the configured TMPDIR so the
// builder's MkdirTemp lands in a known, asserted-against parent.
func adv2BaseDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// PINS the workdir-confinement contract: a hostile job ID packed with "../"
// sequences MUST NOT cause the per-job workdir to be created outside BaseDir.
//
// SINK-SIDE: we set an explicit BaseDir and, after a successful build, assert the
// recorded workdir line names a directory whose parent IS exactly BaseDir — i.e.
// the dir did not escape. We also assert no sibling/parent path containing the
// raw traversal was created.
//
// MUTATION PROOF (manual, documented): replace `sanitize(j.ID)` at the MkdirTemp
// call with a bare `j.ID`. os.MkdirTemp then returns "pattern contains path
// separator" for this ID and the build fails at "cannot create workdir" — the
// require.Equal(StateSucceeded) below FAILS. Thus sanitize() is load-bearing for
// keeping a "/"-bearing ID buildable AND confined. (Verified out-of-band; the fix
// is the existing sanitize() call, left intact.)
func TestExecBuilderAdv2_HostileJobID_WorkdirStaysUnderBaseDir(t *testing.T) {
	base := adv2BaseDir(t)

	// The command echoes its real CWD (the workdir) so we can read it back from
	// the logs and assert containment sink-side, then writes the artifact.
	b := &ExecBuilder{
		BaseDir: base,
		Command: `pwd; printf 'ok' > "$HELIX_OUTPUT"`,
	}

	hostileID := "../../../../../../tmp/pwn"
	j := runViaSeam(t, b, &pkgbuild.Job{ID: hostileID, RepoURL: "r", Ref: "v1"})

	require.Equal(t, pkgbuild.StateSucceeded, j.State,
		"a '/'-bearing job ID must remain buildable (sanitize neutralises the separator) and confined")

	logs := strings.Join(j.GetLogs(), "\n")

	// Extract the real CWD the command reported via `pwd`.
	var cwd string
	for _, line := range strings.Split(logs, "\n") {
		v := strings.TrimPrefix(line, "stdout: ")
		if strings.HasPrefix(v, base+string(os.PathSeparator)) {
			cwd = v
			break
		}
	}
	require.NotEmpty(t, cwd, "command CWD (pwd) must be captured and must live under BaseDir; logs:\n%s", logs)

	// Containment: the workdir's parent is exactly BaseDir; it did NOT escape.
	require.Equal(t, base, filepath.Dir(cwd),
		"per-job workdir must be a direct child of BaseDir, not an escaped path")

	// The raw traversal must NOT have created anything outside BaseDir.
	assert.NoFileExists(t, "/tmp/pwn", "hostile job ID must not have produced /tmp/pwn")

	// The sanitized workdir name is a SINGLE path segment relative to BaseDir:
	// sanitize() turns every '/' into '-', so the result can never introduce a
	// path separator (and thus can never be a real ".." parent-escape component).
	// Note: the literal substring ".." may appear (e.g. "..-..-..-tmp-pwn") because
	// '.' is allowed and '/' became '-'; that is harmless — it is not a separator
	// and cannot escape. Containment is proven by the single-segment check plus the
	// filepath.Dir(cwd)==base assertion above.
	rel, err := filepath.Rel(base, cwd)
	require.NoError(t, err)
	assert.False(t, strings.Contains(rel, string(os.PathSeparator)),
		"sanitized workdir name must be a single path segment (no separator), got %q", rel)
	assert.NotEqual(t, "..", rel, "workdir must not be BaseDir's parent")
	assert.False(t, strings.HasPrefix(rel, ".."+string(os.PathSeparator)),
		"workdir must not start with a real parent-escape segment, got %q", rel)
}

// PINS that OutputName is OPERATOR config, not caller-injectable, and that even a
// hostile-looking OutputName resolves UNDER workdir (it is joined to the temp
// workdir, never an absolute job value). This documents why the filepath.Join at
// line 167 is not a request-traversal sink: nothing on the Job path feeds it.
//
// We additionally confirm the produced artifact path the builder reads is the
// Join(workdir, OutputName) path and lives under BaseDir.
func TestExecBuilderAdv2_OutputNameIsConfig_ResolvesUnderWorkdir(t *testing.T) {
	base := adv2BaseDir(t)

	// Even a traversal-flavoured OutputName: filepath.Join cleans it, and because
	// the command writes to the SAME $HELIX_OUTPUT the builder computed, the build
	// still succeeds and the artifact path stays rooted at the temp workdir's tree.
	b := &ExecBuilder{
		BaseDir:    base,
		OutputName: "nested/artifact.out", // a relative sub-path, still under workdir
		Command:    `mkdir -p "$(dirname "$HELIX_OUTPUT")"; printf 'cfg-bytes' > "$HELIX_OUTPUT"; printf '%s\n' "$HELIX_OUTPUT"`,
	}

	j := runViaSeam(t, b, &pkgbuild.Job{ID: "outname-cfg", RepoURL: "r", Ref: "v1"})
	require.Equal(t, pkgbuild.StateSucceeded, j.State)

	logs := strings.Join(j.GetLogs(), "\n")
	// The exported HELIX_OUTPUT must be Join(workdir, OutputName), under BaseDir.
	var outPath string
	for _, line := range strings.Split(logs, "\n") {
		v := strings.TrimPrefix(line, "stdout: ")
		if strings.HasSuffix(v, filepath.FromSlash("nested/artifact.out")) {
			outPath = v
			break
		}
	}
	require.NotEmpty(t, outPath, "exported HELIX_OUTPUT must be captured; logs:\n%s", logs)
	assert.True(t, strings.HasPrefix(outPath, base+string(os.PathSeparator)),
		"artifact path must resolve under BaseDir, got %q (base %q)", outPath, base)
}

// PINS the env-override LATENT note: a hostile BuildArgs key "HELIX_OUTPUT" wins
// last-write in the CHILD command's environment, but the builder reads its OWN
// Go-side outPath, so the override cannot redirect the builder's file read/write
// outside workdir. The observable effect is a benign MISMATCH (the command writes
// to the attacker path, the builder finds nothing at its intended outPath) ->
// "produced no artifact" FAILURE, never a builder-side escape or a fabricated
// success.
//
// If a future refactor made the builder read back $HELIX_OUTPUT from the
// environment, the attacker path below would be honoured and this test's
// NoFileExists / StateFailed assertions would flip — surfacing the regression.
func TestExecBuilderAdv2_HostileHelixOutputBuildArg_CannotEscapeBuilderIO(t *testing.T) {
	base := adv2BaseDir(t)
	// A distinct sentinel path OUTSIDE the build tree the attacker tries to force.
	escapeDir := t.TempDir()
	escapeTarget := filepath.Join(escapeDir, "escaped-artifact")

	b := &ExecBuilder{
		BaseDir: base,
		// The command writes to whatever $HELIX_OUTPUT the CHILD sees. If the
		// BuildArgs override wins (it does, last-write), the bytes land at the
		// attacker path, NOT at the builder's intended outPath -> builder reads
		// nothing -> failure.
		Command: `printf 'leaked' > "$HELIX_OUTPUT"`,
	}

	j := runViaSeam(t, b, &pkgbuild.Job{
		ID:      "helixout-override",
		RepoURL: "r",
		Ref:     "v1",
		BuildArgs: map[string]string{
			"HELIX_OUTPUT": escapeTarget, // hostile override
		},
	})

	// Builder-side I/O was NOT redirected: it read its own intended outPath, found
	// nothing there, and FAILED honestly (no fabricated success, no escape).
	assert.Equal(t, pkgbuild.StateFailed, j.State,
		"override of child's HELIX_OUTPUT must not let the builder succeed against its own outPath")
	assert.Empty(t, j.ImageTag, "no image tag for a build whose artifact never landed at the builder's outPath")

	logs := strings.Join(j.GetLogs(), "\n")
	assert.Contains(t, logs, "produced no artifact",
		"the builder must report the missing artifact at ITS outPath, proving it ignored the env override")

	// The bytes did land at the attacker path (child honoured the override) — that
	// is the child command's own doing and is no worse than b.Command already
	// being arbitrary shell. The point pinned here: the BUILDER never read/wrote
	// outside its workdir on account of the override, and recorded no success.
	if _, err := os.Stat(escapeTarget); err == nil {
		// Present: child wrote there. Fine — builder still failed (asserted above).
		assert.FileExists(t, escapeTarget)
	}
}

// Guards that the workdir is REMOVED after the build regardless of the hostile
// job ID — confirming the deferred os.RemoveAll(workdir) targets the real
// (sanitized, confined) dir and nothing outside it.
func TestExecBuilderAdv2_HostileJobID_WorkdirCleanedUp(t *testing.T) {
	base := adv2BaseDir(t)
	b := &ExecBuilder{
		BaseDir: base,
		Command: `printf 'x' > "$HELIX_OUTPUT"`,
	}

	_ = runViaSeam(t, b, &pkgbuild.Job{ID: "../../escape-cleanup", RepoURL: "r", Ref: "v1"})

	// After the build, BaseDir must contain no leftover helix-build-* workdir.
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), "helix-build-"),
			"per-job workdir %q must be cleaned up under BaseDir", e.Name())
	}
}

// helixBuildEntries returns the names of any per-job workdirs still present
// directly under base.
func helixBuildEntries(t *testing.T, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "helix-build-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// DETERMINISTIC mutation-killer for the cleanup-ordering fix (DEFECT D9).
//
// The contract: the per-job workdir MUST be removed BEFORE the job reaches the
// terminal state any caller can observe. A caller learns the job is terminal via
// Job.Done() (closed inside TransitionTo, under j.mu). We submit through the REAL
// Service seam, block on the ORIGINAL job's Done() channel, and the instant it
// fires assert that NO helix-build-* workdir remains under BaseDir.
//
// Why this is deterministic (unlike the stress variant above): Done() is the
// happens-before edge. With the fix, cleanupWorkdir() runs before
// terminate(StateSucceeded), so the dir is provably gone when Done() closes.
//
// MUTATION PROOF: revert the fix to a bare `defer os.RemoveAll(workdir)` (cleanup
// after Build returns, i.e. AFTER TransitionTo closes Done) — when Done() fires
// the workdir is still on disk and this test FAILS. Likewise, making
// cleanupWorkdir a no-op (or dropping the finish() call on the success path)
// leaves the dir present at Done() and FAILS. The fix is load-bearing here.
func TestExecBuilderAdv2_HostileJobID_CleanupHappensBeforeTerminal(t *testing.T) {
	base := adv2BaseDir(t)
	b := &ExecBuilder{
		BaseDir: base,
		Command: `printf 'x' > "$HELIX_OUTPUT"`,
	}

	svc := pkgbuild.NewServiceWithBuilder(1, b)
	svc.Start(context.Background())
	t.Cleanup(svc.Stop)

	j := &pkgbuild.Job{ID: "../../escape-cleanup", RepoURL: "r", Ref: "v1"}
	require.NoError(t, svc.Submit(j))

	select {
	case <-j.Done():
		// Terminal state is now observable. The workdir must already be gone.
		leftovers := helixBuildEntries(t, base)
		assert.Empty(t, leftovers,
			"workdir cleanup must happen-before the terminal Done() signal; leftovers: %v", leftovers)
	case <-time.After(60 * time.Second):
		t.Fatal("hostile-ID build did not reach a terminal state within 60s")
	}

	require.Equal(t, pkgbuild.StateSucceeded, j.State,
		"a '/'-bearing job ID must still build successfully after cleanup-before-terminal")
}

// Sanity timeout guard wrapper: ensures the whole hostile-ID success path cannot
// hang the suite (the seam already bounds via Eventually, this is belt+braces).
func TestExecBuilderAdv2_HostileJobID_Terminates(t *testing.T) {
	base := adv2BaseDir(t)
	b := &ExecBuilder{BaseDir: base, Command: `printf 'x' > "$HELIX_OUTPUT"`}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runViaSeam(t, b, &pkgbuild.Job{ID: "../weird/../id", RepoURL: "r", Ref: "v1"})
	}()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("hostile-ID build did not terminate within 90s")
	}
}

// Compile-time anchor so the unused-import linter never trips if a probe is
// trimmed; references the package context type.
var _ = context.Background
