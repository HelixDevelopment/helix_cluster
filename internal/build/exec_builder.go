// Package build provides the build orchestration service for Helix Cluster OS.
package build

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	pkgbuild "github.com/HelixDevelopment/helix_cluster/pkg/build"
)

// ExecBuilder is a REAL, NON-SIMULATED build implementation. It satisfies the
// pkgbuild.Builder seam (see pkg/build.NewServiceWithBuilder) and runs an actual
// local build command via os/exec in a per-job temporary working directory.
//
// What it really does (honestly):
//   - Creates a fresh per-job temp workdir on the local filesystem.
//   - Executes the configured command line through "sh -c" inside that workdir,
//     with build context exported via environment variables (HELIX_BUILD_ID,
//     HELIX_REF, HELIX_REPO_URL, HELIX_WORKDIR, HELIX_OUTPUT, plus every entry of
//     Job.BuildArgs).
//   - Streams the command's REAL stdout and stderr, line by line, into the job
//     logs as they are produced.
//   - Captures the command's REAL process exit code.
//   - On a zero exit code, reads the REAL artifact file the command was asked to
//     produce (HELIX_OUTPUT), computes its sha256 digest, and derives the image
//     tag from that digest. The artifact bytes and digest reflect actual command
//     output, not a fabricated constant.
//
// What it is NOT (honest seam boundary, PCS-6 / CLAUDE-1): ExecBuilder is NOT a
// container image builder. It does not invoke podman/docker/buildkit, does not
// produce OCI layers, and does not push to a registry. It runs ordinary local
// shell commands. The "image tag" it emits is a content-addressed reference to
// the locally produced artifact file, suitable for a registry/cache that stores
// such artifacts — it is real with respect to the command's output, but it is a
// local-exec artifact, not a pulled-and-pushed container image. Because the
// result is derived from a real process, Simulated() returns false.
type ExecBuilder struct {
	// Shell is the shell binary used to interpret Command. Defaults to "sh".
	Shell string

	// Command is the shell command line executed for every job. It is run as
	// `Shell -c Command` inside the per-job workdir. If empty, the builder uses
	// the job's DockerfilePath only as a label and fails the job, since there is
	// nothing real to execute — it never fabricates a success.
	Command string

	// OutputName is the basename of the artifact file the command is expected to
	// write inside the workdir. Its absolute path is exported as HELIX_OUTPUT.
	// Defaults to "artifact.out".
	OutputName string

	// ArtifactStore, if set, receives the produced artifact bytes keyed by their
	// content digest, enabling end-to-end retrieval of the REAL output. It mirrors
	// the pkg/build/cache.Cache.Put signature without importing it here.
	ArtifactStore ArtifactStore

	// BaseDir, if set, is the parent directory under which per-job temp workdirs
	// are created. Defaults to the OS temp dir.
	BaseDir string

	mu        sync.Mutex
	artifacts map[string]ExecArtifact // jobID -> last produced artifact
}

// ArtifactStore is the minimal sink ExecBuilder needs to persist a real produced
// artifact. pkg/build/cache.Cache satisfies it.
type ArtifactStore interface {
	Put(digest string, data []byte) error
}

// ExecArtifact records the real result of an exec build for a job.
type ExecArtifact struct {
	Digest   string // sha256 hex of the produced artifact bytes
	Size     int    // byte length of the produced artifact
	ExitCode int    // real process exit code
	ImageTag string // content-addressed tag derived from the digest
}

// compile-time assertion that ExecBuilder satisfies the real Builder seam.
//
// TODO(PCS-6): This assertion only covers Build(ctx, *Job). Simulated() bool is
// NOT part of pkgbuild.Builder, so the seam cannot, by itself, guarantee that a
// real builder is distinguishable from the simulatedBuilder at the interface
// level — the distinction currently relies on the concrete-type check below and
// in tests. If the anti-bluff contract requires that distinction to be enforced
// at the Builder seam, Simulated() bool must be promoted into the pkgbuild.Builder
// interface in pkg/build/build.go. That file is outside internal/build/ and must
// be changed by the package owner; until then this is a documented known gap.
var _ pkgbuild.Builder = (*ExecBuilder)(nil)

// Simulated reports that this builder is NOT a simulation. It runs real
// processes and derives output from their real results.
func (b *ExecBuilder) Simulated() bool { return false }

// ArtifactFor returns the last recorded artifact for a job, if any.
func (b *ExecBuilder) ArtifactFor(jobID string) (ExecArtifact, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.artifacts[jobID]
	return a, ok
}

func (b *ExecBuilder) recordArtifact(jobID string, a ExecArtifact) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.artifacts == nil {
		b.artifacts = make(map[string]ExecArtifact)
	}
	b.artifacts[jobID] = a
}

func (b *ExecBuilder) shell() string {
	if b.Shell != "" {
		return b.Shell
	}
	return "sh"
}

func (b *ExecBuilder) outputName() string {
	if b.OutputName != "" {
		return b.OutputName
	}
	return "artifact.out"
}

// terminate drives the job into the given terminal state and records, in the
// job log, any error returned by Job.TransitionTo. Discarding that error is a
// real defect: if the job was already terminal (e.g. cancelled between enqueue
// and execution) the transition silently fails and the caller would otherwise
// march on as if it had succeeded. Returning the error lets callers stop.
func terminate(j *pkgbuild.Job, to pkgbuild.State) error {
	if err := j.TransitionTo(to); err != nil {
		j.AppendLog(fmt.Sprintf("Build state error: cannot transition to %s: %v", to, err))
		return err
	}
	return nil
}

// Build implements pkgbuild.Builder by executing a real local command. It MUST
// drive the job to a terminal state before returning.
func (b *ExecBuilder) Build(ctx context.Context, j *pkgbuild.Job) {
	if b.Command == "" {
		j.AppendLog("Build failed: ExecBuilder has no Command configured (refusing to fabricate success)")
		_ = terminate(j, pkgbuild.StateFailed)
		return
	}

	workdir, err := os.MkdirTemp(b.BaseDir, "helix-build-"+sanitize(j.ID)+"-")
	if err != nil {
		j.AppendLog(fmt.Sprintf("Build failed: cannot create workdir: %v", err))
		_ = terminate(j, pkgbuild.StateFailed)
		return
	}

	// Workdir cleanup MUST happen-before the terminal transition that callers
	// observe. A deferred os.RemoveAll runs only after Build returns — i.e. AFTER
	// TransitionTo(StateSucceeded/Failed/Cancelled) — so a caller that watches the
	// job reach a terminal state (Service.Get) can race ahead and still see the
	// per-job workdir on disk under BaseDir. With a caller-controlled (possibly
	// hostile/traversal-named) Job.ID, leaving that directory behind is a real
	// cleanup-integrity defect. cleanupWorkdir is idempotent: each terminal path
	// calls it explicitly before terminate(), and the deferred call is a
	// belt-and-braces guard for any unexpected return.
	cleanedUp := false
	cleanupWorkdir := func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		_ = os.RemoveAll(workdir)
	}
	defer cleanupWorkdir()
	// finish removes the per-job workdir and THEN drives the job to its terminal
	// state, guaranteeing the directory is gone before the state any caller can
	// observe becomes terminal.
	finish := func(to pkgbuild.State) error {
		cleanupWorkdir()
		return terminate(j, to)
	}

	outPath := filepath.Join(workdir, b.outputName())
	j.AppendLog(fmt.Sprintf("[%s] exec build started in %s", time.Now().UTC().Format(time.RFC3339), workdir))

	cmd := exec.CommandContext(ctx, b.shell(), "-c", b.Command)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"HELIX_BUILD_ID="+j.ID,
		"HELIX_REF="+j.Ref,
		"HELIX_REPO_URL="+j.RepoURL,
		"HELIX_WORKDIR="+workdir,
		"HELIX_OUTPUT="+outPath,
	)
	// Deterministic ordering of build-arg env for reproducible logs.
	keys := make([]string, 0, len(j.BuildArgs))
	for k := range j.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+j.BuildArgs[k])
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		j.AppendLog(fmt.Sprintf("Build failed: stdout pipe: %v", err))
		_ = finish(pkgbuild.StateFailed)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		j.AppendLog(fmt.Sprintf("Build failed: stderr pipe: %v", err))
		_ = finish(pkgbuild.StateFailed)
		return
	}

	if err := cmd.Start(); err != nil {
		j.AppendLog(fmt.Sprintf("Build failed: cannot start command: %v", err))
		_ = finish(pkgbuild.StateFailed)
		return
	}

	// Stream real stdout/stderr concurrently, prefixing each line so the source
	// stream is identifiable in the captured logs.
	var streamWG sync.WaitGroup
	streamWG.Add(2)
	go streamLines(&streamWG, stdout, "stdout: ", j)
	go streamLines(&streamWG, stderr, "stderr: ", j)
	streamWG.Wait()

	waitErr := cmd.Wait()
	exitCode := exitCodeOf(waitErr)

	// Honor cancellation: if the context was cancelled, the job is cancelled, not
	// failed, so callers can distinguish operator abort from a real build error.
	if ctx.Err() != nil {
		j.AppendLog(fmt.Sprintf("Build cancelled by context (exit %d)", exitCode))
		_ = finish(pkgbuild.StateCancelled)
		return
	}

	if exitCode != 0 {
		b.recordArtifact(j.ID, ExecArtifact{ExitCode: exitCode})
		j.AppendLog(fmt.Sprintf("Build failed: command exited with code %d", exitCode))
		_ = finish(pkgbuild.StateFailed)
		return
	}

	// Real success: read whatever the command actually produced. The bytes are
	// fully read into memory here, BEFORE the workdir is removed below, so the
	// digest/artifact reflect the real output even though cleanup precedes the
	// terminal transition.
	data, readErr := os.ReadFile(outPath)
	if readErr != nil {
		// A zero exit but no artifact is a real failure: the command claimed
		// success without producing the requested output. Do NOT fabricate one.
		b.recordArtifact(j.ID, ExecArtifact{ExitCode: exitCode})
		j.AppendLog(fmt.Sprintf("Build failed: command exited 0 but produced no artifact at %s: %v", outPath, readErr))
		_ = finish(pkgbuild.StateFailed)
		return
	}

	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	imageTag := fmt.Sprintf("helix/%s@sha256:%s", sanitize(j.ID), digest)

	if b.ArtifactStore != nil {
		if perr := b.ArtifactStore.Put(digest, data); perr != nil {
			j.AppendLog(fmt.Sprintf("Build failed: persisting artifact: %v", perr))
			_ = finish(pkgbuild.StateFailed)
			return
		}
	}

	// Commit the success transition BEFORE advertising the image tag or
	// recording a successful artifact. If the job was concurrently driven into a
	// terminal state (e.g. cancelled), TransitionTo returns an error and we must
	// NOT claim a success that did not happen: no image tag, no success artifact.
	// finish() removes the per-job workdir first, so the directory is gone before
	// any caller can observe StateSucceeded — no leftover workdir under BaseDir.
	if err := finish(pkgbuild.StateSucceeded); err != nil {
		b.recordArtifact(j.ID, ExecArtifact{ExitCode: exitCode})
		return
	}

	b.recordArtifact(j.ID, ExecArtifact{
		Digest:   digest,
		Size:     len(data),
		ExitCode: exitCode,
		ImageTag: imageTag,
	})

	j.SetImageTag(imageTag)
	j.AppendLog(fmt.Sprintf("exec build succeeded: %d-byte artifact, sha256:%s", len(data), digest))
}

// streamLines copies r line-by-line into the job log, prefixing each line.
func streamLines(wg *sync.WaitGroup, r io.Reader, prefix string, j *pkgbuild.Job) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		j.AppendLog(prefix + sc.Text())
	}
}

// exitCodeOf extracts the process exit code from a cmd.Wait error. A nil error
// means exit 0; an *exec.ExitError carries the real code; any other error is
// reported as -1 so it is unambiguously non-zero (a real failure).
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// sanitize replaces characters unsuitable for filesystem/tag use with '-'.
func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}
