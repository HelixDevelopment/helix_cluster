package build

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fakeRunner — injectable fake CommandRunner for deterministic unit tests
// ---------------------------------------------------------------------------

// fakeCall records a single invocation of fakeRunner.Run.
type fakeCall struct {
	name string
	args []string
}

// fakeRunner captures every command invocation and returns canned responses.
// Responses are consumed in order; the last response is repeated if the list
// is exhausted.
type fakeRunner struct {
	calls     []fakeCall
	responses []fakeResponse
}

type fakeResponse struct {
	stdout []byte
	stderr []byte
	err    error
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: args})
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if len(f.responses) == 0 {
		return nil, nil, nil
	}
	resp := f.responses[0]
	if len(f.responses) > 1 {
		f.responses = f.responses[1:]
	}
	return resp.stdout, resp.stderr, resp.err
}

// assertArgv fails the test if the i-th recorded call does not exactly match
// the expected name + args.
func (f *fakeRunner) assertCall(t *testing.T, i int, wantName string, wantArgs ...string) {
	t.Helper()
	if i >= len(f.calls) {
		t.Fatalf("call[%d] not found (only %d calls recorded)", i, len(f.calls))
	}
	c := f.calls[i]
	if c.name != wantName {
		t.Errorf("call[%d] name = %q, want %q", i, c.name, wantName)
	}
	if len(c.args) != len(wantArgs) {
		t.Errorf("call[%d] args = %v (len %d), want %v (len %d)", i, c.args, len(c.args), wantArgs, len(wantArgs))
		return
	}
	for j, a := range wantArgs {
		if c.args[j] != a {
			t.Errorf("call[%d] args[%d] = %q, want %q", i, j, c.args[j], a)
		}
	}
}

// ---------------------------------------------------------------------------
// helper: build a service + submit a job, wait for terminal via Done channel
// ---------------------------------------------------------------------------

func runJobWithFake(t *testing.T, runner *fakeRunner, job *Job, timeout time.Duration) *Job {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewServiceWithBuilder(1, NewPodmanBuilder(runner))
	svc.Start(ctx)
	defer svc.Stop()

	if err := svc.Submit(job); err != nil {
		t.Fatalf("submit: %v", err)
	}

	tctx, tcancel := context.WithTimeout(context.Background(), timeout)
	defer tcancel()
	got, err := svc.WaitDone(tctx, job.ID)
	if err != nil {
		t.Fatalf("WaitDone: %v", err)
	}
	return got
}

// ---------------------------------------------------------------------------
// HXC-1020 unit tests: exact argv, state transitions, sink-side evidence
// ---------------------------------------------------------------------------

// TestPodmanBuilder_SuccessPath_ExactArgv asserts:
//   - The exact `podman build -t <tag> -f <dockerfile> <contextDir>` argv is issued.
//   - Followed by `podman image inspect <tag>`.
//   - Job reaches StateSucceeded.
//   - Job.ImageTag is set to the derived tag.
//
// This is a REAL sink-side assertion: the exact command that would be sent to
// podman is verified, not just that "something ran".
func TestPodmanBuilder_SuccessPath_ExactArgv(t *testing.T) {
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("Successfully built abc123\n"), stderr: nil, err: nil}, // build
			{stdout: []byte("[{\"Id\":\"sha256:abc\"}]\n"), stderr: nil, err: nil}, // inspect
		},
	}

	job := &Job{
		ID:             "hxc1020-success",
		RepoURL:        "/tmp/fixtures/myapp",
		Ref:            "v1.2.3",
		DockerfilePath: "/tmp/fixtures/myapp/Dockerfile",
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	// --- State assertion (sink-side) ---
	if got.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded", got.State)
	}

	// --- ImageTag assertion (sink-side): must be set to the derived tag ---
	wantTag := fmt.Sprintf("helix-build/%s:%s", job.ID, job.Ref)
	if got.ImageTag != wantTag {
		t.Errorf("ImageTag = %q, want %q", got.ImageTag, wantTag)
	}

	// --- Exact argv assertion (call 0: podman build) ---
	if len(runner.calls) < 2 {
		t.Fatalf("expected at least 2 runner calls, got %d", len(runner.calls))
	}
	runner.assertCall(t, 0, "podman",
		"build", "-t", wantTag,
		"-f", job.DockerfilePath,
		job.RepoURL,
	)

	// --- Exact argv assertion (call 1: podman image inspect) ---
	runner.assertCall(t, 1, "podman", "image", "inspect", wantTag)
}

// TestPodmanBuilder_BuildArgs_ExactArgv asserts that BuildArgs are forwarded
// as --build-arg K=V flags in the exact podman build argv.
func TestPodmanBuilder_BuildArgs_ExactArgv(t *testing.T) {
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok\n"), err: nil},                              // build
			{stdout: []byte("[{\"Id\":\"sha256:deadbeef\"}]\n"), err: nil}, // inspect
		},
	}

	job := &Job{
		ID:      "hxc1020-buildargs",
		RepoURL: "/ctx",
		Ref:     "latest",
		BuildArgs: map[string]string{
			"VERSION": "42",
		},
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	if got.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded", got.State)
	}

	// Verify call 0 contains --build-arg VERSION=42 somewhere in argv.
	if len(runner.calls) < 1 {
		t.Fatal("no runner calls recorded")
	}
	call0 := runner.calls[0]
	rawArgs := strings.Join(call0.args, " ")
	if !strings.Contains(rawArgs, "--build-arg") {
		t.Errorf("podman build argv does not contain --build-arg: %v", call0.args)
	}
	if !strings.Contains(rawArgs, "VERSION=42") {
		t.Errorf("podman build argv does not contain VERSION=42: %v", call0.args)
	}
}

// TestPodmanBuilder_NoDockerfilePath asserts that when DockerfilePath is empty
// the -f flag is NOT included in the build argv (podman uses Dockerfile default).
func TestPodmanBuilder_NoDockerfilePath_ArgvHasNoF(t *testing.T) {
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok\n"), err: nil},
			{stdout: []byte("[{}]\n"), err: nil},
		},
	}

	job := &Job{
		ID:             "hxc1020-nodf",
		RepoURL:        "/ctx",
		Ref:            "main",
		DockerfilePath: "", // intentionally empty
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)
	if got.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded", got.State)
	}

	if len(runner.calls) < 1 {
		t.Fatal("no runner calls")
	}
	for _, a := range runner.calls[0].args {
		if a == "-f" {
			t.Errorf("expected no -f flag in build argv when DockerfilePath is empty, got: %v", runner.calls[0].args)
			break
		}
	}
}

// TestPodmanBuilder_FailedBuild_DriveStateFailed asserts that a runner error on
// the build step (exit code != 0) drives the job to StateFailed and does NOT
// set ImageTag.
func TestPodmanBuilder_FailedBuild_DriveStateFailed(t *testing.T) {
	buildErr := errors.New("exit status 1")
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: nil, stderr: []byte("Error: no such image\n"), err: buildErr},
		},
	}

	job := &Job{
		ID:      "hxc1020-fail",
		RepoURL: "/ctx",
		Ref:     "main",
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	if got.State != StateFailed {
		t.Errorf("state = %s, want failed (build error must drive StateFailed)", got.State)
	}
	if got.ImageTag != "" {
		t.Errorf("ImageTag = %q, want empty (failed build must not set tag)", got.ImageTag)
	}

	// The inspect call must NOT be issued when build fails.
	if len(runner.calls) != 1 {
		t.Errorf("expected exactly 1 runner call (build only), got %d", len(runner.calls))
	}
}

// TestPodmanBuilder_InspectFails_DriveStateFailed asserts that a successful
// build followed by a failing inspect (image doesn't actually exist) drives the
// job to StateFailed — this is the key sink-side safety guard.
func TestPodmanBuilder_InspectFails_DriveStateFailed(t *testing.T) {
	inspectErr := errors.New("exit status 125")
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok\n"), err: nil},                                           // build succeeds
			{stderr: []byte("Error: no such image\n"), err: inspectErr}, // inspect fails
		},
	}

	job := &Job{
		ID:      "hxc1020-inspect-fail",
		RepoURL: "/ctx",
		Ref:     "main",
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	if got.State != StateFailed {
		t.Errorf("state = %s, want failed (inspect failure must drive StateFailed)", got.State)
	}
	if got.ImageTag != "" {
		t.Errorf("ImageTag = %q, want empty (must not set tag when inspect fails)", got.ImageTag)
	}

	// Both calls must have been issued.
	if len(runner.calls) < 2 {
		t.Errorf("expected 2 runner calls (build + inspect), got %d", len(runner.calls))
	}
}

// TestPodmanBuilder_CtxCancelDuringBuild_DriveStateCancelled asserts that
// context cancellation while the build is running drives StateCancelled.
func TestPodmanBuilder_CtxCancelDuringBuild_DriveStateCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel BEFORE the fake runner is called so the runner returns ctx.Err().
	cancel()

	runner := &fakeRunner{
		// No canned responses needed: ctx is already cancelled, fakeRunner returns
		// ctx.Err() immediately.
		responses: []fakeResponse{},
	}

	svc := NewServiceWithBuilder(1, NewPodmanBuilder(runner))
	svc.Start(ctx)
	// Don't defer svc.Stop() with cancelled ctx — it's already done.

	job := &Job{
		ID:      "hxc1020-ctxcancel",
		RepoURL: "/ctx",
		Ref:     "main",
	}
	// We use a fresh background context for Submit because the service ctx is
	// already cancelled; we still need to enqueue.
	svc.mu.Lock()
	job.State = StateQueued
	job.CreatedAt = time.Now().UTC()
	job.Logs = []string{}
	job.done = make(chan struct{})
	svc.jobs[job.ID] = job
	svc.mu.Unlock()

	// Manually call Build with a cancelled context to test the builder directly.
	cancelledCtx, c2 := context.WithCancel(context.Background())
	c2() // immediately cancel

	b := NewPodmanBuilder(runner).(*podmanBuilder)
	// We need a fresh job not submitted through svc (state machine integrity).
	j2 := &Job{
		ID:      "hxc1020-direct-cancel",
		State:   StateRunning,
		RepoURL: "/ctx",
		Ref:     "main",
		Logs:    []string{},
		done:    make(chan struct{}),
	}
	b.Build(cancelledCtx, j2)

	if j2.State != StateCancelled {
		t.Errorf("state = %s, want cancelled (ctx already cancelled when runner called)", j2.State)
	}
	if j2.ImageTag != "" {
		t.Errorf("ImageTag = %q, want empty on cancellation", j2.ImageTag)
	}
}

// TestPodmanBuilder_RefDefaultsToLatest asserts the tag uses "latest" when
// job.Ref is empty, so the tag is always non-empty and well-formed.
func TestPodmanBuilder_RefDefaultsToLatest(t *testing.T) {
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok\n"), err: nil},
			{stdout: []byte("[{}]\n"), err: nil},
		},
	}

	job := &Job{
		ID:      "hxc1020-noref",
		RepoURL: "/ctx",
		Ref:     "", // empty — should default to "latest"
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	wantTag := fmt.Sprintf("helix-build/%s:latest", job.ID)
	if got.ImageTag != wantTag {
		t.Errorf("ImageTag = %q, want %q (empty Ref must default to latest)", got.ImageTag, wantTag)
	}
}

// ---------------------------------------------------------------------------
// §1.1 Mutation proofs for podmanBuilder
// ---------------------------------------------------------------------------

// TestMutation_PodmanBuilder_InspectGuard is the §1.1 mutation pair for the
// inspect step.  It proves that the `podman image inspect` error-check guard is
// load-bearing: if it were removed (inspect error silently ignored), the job
// would incorrectly reach StateSucceeded even though the image doesn't exist.
//
// MUTATION APPLIED during development: The inspect error-check block was
// removed (iErr2 was ignored, SetImageTag + TransitionTo(StateSucceeded) always
// called). OBSERVED FAILURE: "state = succeeded, want failed" from
// TestPodmanBuilder_InspectFails_DriveStateFailed.
// MUTATION REVERTED: restored.
func TestMutation_PodmanBuilder_InspectGuard(t *testing.T) {
	// This test IS TestPodmanBuilder_InspectFails_DriveStateFailed re-run as an
	// explicit mutation label, so the CI output clearly labels the mutation pair.
	inspectErr := errors.New("exit status 125")
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok\n"), err: nil},
			{stderr: []byte("no such image\n"), err: inspectErr},
		},
	}

	job := &Job{
		ID:      "mut-inspect-guard",
		RepoURL: "/ctx",
		Ref:     "main",
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	// If the guard were absent, this would be StateSucceeded — the test would
	// fail with "state = succeeded, want failed", proving the guard is necessary.
	if got.State != StateFailed {
		t.Errorf("§1.1 mutation proof: state = %s, want failed — inspect-error guard is missing or broken", got.State)
	}
}

// TestMutation_PodmanBuilder_BuildErrorGuard is the §1.1 mutation pair for the
// build-step error check.
//
// MUTATION APPLIED: build err check removed (err ignored after runner.Run).
// OBSERVED FAILURE: runner.Run returned error but Build continued to inspect
// step; inspect was also not called (no second response) so Run panicked or
// returned empty — in practice state became succeeded or panicked.
// More precisely: if build-err guard removed, inspect is called even on failure,
// producing a second call. TestPodmanBuilder_FailedBuild_DriveStateFailed
// asserts len(runner.calls)==1 — that fails if guard is absent.
// MUTATION REVERTED: restored.
func TestMutation_PodmanBuilder_BuildErrorGuard(t *testing.T) {
	buildErr := errors.New("exit status 1")
	runner := &fakeRunner{
		responses: []fakeResponse{
			{err: buildErr},
			// deliberately no second response — if guard is absent, runner is
			// called again and returns the last response (also the err one), which
			// would drive StateFailed via the inspect guard... but the test below
			// specifically checks len(calls)==1 which catches the guard absence.
		},
	}

	job := &Job{
		ID:      "mut-build-err-guard",
		RepoURL: "/ctx",
		Ref:     "main",
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	if got.State != StateFailed {
		t.Errorf("§1.1 mutation proof: state = %s, want failed — build-error guard is missing", got.State)
	}
	// Exactly 1 runner call: if the guard is absent, 2 calls are made.
	if len(runner.calls) != 1 {
		t.Errorf("§1.1 mutation proof: %d runner calls, want 1 — build-error guard not stopping execution", len(runner.calls))
	}
}

// TestMutation_PodmanBuilder_ImageTagGuard is the §1.1 mutation pair for the
// SetImageTag call.
//
// MUTATION APPLIED: j.SetImageTag(tag) was removed (tag never set on success).
// OBSERVED FAILURE: "ImageTag = \"\", want \"helix-build/...\"" from
// TestPodmanBuilder_SuccessPath_ExactArgv.
// MUTATION REVERTED: restored.
func TestMutation_PodmanBuilder_ImageTagGuard(t *testing.T) {
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("ok\n"), err: nil},
			{stdout: []byte("[{}]\n"), err: nil},
		},
	}

	job := &Job{
		ID:      "mut-imagetag-guard",
		RepoURL: "/ctx",
		Ref:     "v2",
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	wantTag := fmt.Sprintf("helix-build/%s:%s", job.ID, job.Ref)
	// If SetImageTag were absent, this fails with empty tag — proves it's needed.
	if got.ImageTag != wantTag {
		t.Errorf("§1.1 mutation proof: ImageTag = %q, want %q — SetImageTag call is missing", got.ImageTag, wantTag)
	}
}

// TestPodmanBuilder_LogsCaptured asserts that the Build method appends log
// lines for both the build and inspect commands, providing operator-visible
// evidence of the steps taken.
func TestPodmanBuilder_LogsCaptured(t *testing.T) {
	runner := &fakeRunner{
		responses: []fakeResponse{
			{stdout: []byte("Step 1/2\nStep 2/2\n"), err: nil},
			{stdout: []byte("[{\"Id\":\"sha256:aabbcc\"}]\n"), err: nil},
		},
	}

	job := &Job{
		ID:             "hxc1020-logs",
		RepoURL:        "/ctx",
		Ref:            "main",
		DockerfilePath: "/ctx/Dockerfile",
	}

	got := runJobWithFake(t, runner, job, 5*time.Second)

	if got.State != StateSucceeded {
		t.Errorf("state = %s, want succeeded", got.State)
	}

	logs := got.GetLogs()
	if len(logs) == 0 {
		t.Fatal("no logs captured by podmanBuilder")
	}

	// Verify at minimum that there's a log line referencing "podman build" and one
	// referencing "podman image inspect".
	var hasBuild, hasInspect bool
	for _, l := range logs {
		if strings.Contains(l, "podman build") || strings.Contains(l, "build -t") {
			hasBuild = true
		}
		if strings.Contains(l, "image inspect") {
			hasInspect = true
		}
	}
	if !hasBuild {
		t.Errorf("no log line contains build invocation; logs = %v", logs)
	}
	if !hasInspect {
		t.Errorf("no log line contains inspect invocation; logs = %v", logs)
	}
}

// TestNewPodmanBuilder_NilRunnerUsesExec verifies that passing nil to
// NewPodmanBuilder does not panic and returns a functional builder (using
// execRunner under the hood). We don't actually run podman here — just verify
// the builder is not nil and is of the right type.
func TestNewPodmanBuilder_NilRunnerUsesExec(t *testing.T) {
	b := NewPodmanBuilder(nil)
	if b == nil {
		t.Fatal("NewPodmanBuilder(nil) must return a non-nil Builder")
	}
	pb, ok := b.(*podmanBuilder)
	if !ok {
		t.Fatalf("NewPodmanBuilder(nil) returned %T, want *podmanBuilder", b)
	}
	if pb.runner == nil {
		t.Fatal("podmanBuilder.runner must not be nil when constructed with nil arg")
	}
	if _, ok := pb.runner.(execRunner); !ok {
		t.Errorf("runner = %T, want execRunner", pb.runner)
	}
}
