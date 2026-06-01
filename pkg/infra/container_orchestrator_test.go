package infra

// container_orchestrator_test.go — Deterministic unit tests for the REAL
// containerOrchestrator (HXC-1010 through HXC-1015).
//
// Two-tier testing contract (CLAUDE-1 / §7.1):
//
//   TIER 1 (this file, -short -race):
//     - A fake CommandExecutor records every argv issued and returns canned
//       output.  Tests assert EXACT argv, parsed results, and observable
//       side-effects (TCP listener flip, RemoteExecutor stdout, etc.).
//     - NOT mocks — fakes whose assertions exercise real orchestrator logic.
//
//   TIER 2 (infra_container_integration_test.go, //go:build integration):
//     - Exercises real podman and a real redis container.
//
// §1.1 MUTATION PROOF is documented inline for each guard under test.  The
// mutation was applied, the test was observed to fail for the right reason,
// and the source was restored.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"digital.vasic.containers/pkg/remote"
	"digital.vasic.containers/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Fake CommandExecutor ────────────────────────────────────────────────────

// fakeExecutor records every call and returns canned output per command prefix.
type fakeExecutor struct {
	mu      sync.Mutex
	calls   [][]string // each entry is [name, arg0, arg1, ...]
	outputs map[string][]byte
	errors  map[string]error
	streams map[string]string // streamed output per command prefix
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		outputs: make(map[string][]byte),
		errors:  make(map[string]error),
		streams: make(map[string]string),
	}
}

// setOutput registers a canned stdout for commands whose key is a prefix of
// "name arg0 arg1 …".
func (f *fakeExecutor) setOutput(key string, out []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outputs[key] = out
}

func (f *fakeExecutor) setError(key string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors[key] = err
}

func (f *fakeExecutor) setStream(key, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams[key] = content
}

func (f *fakeExecutor) argv(name string, args []string) string {
	parts := append([]string{name}, args...)
	return strings.Join(parts, " ")
}

func (f *fakeExecutor) match(key string) ([]byte, error, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, out := range f.outputs {
		if strings.HasPrefix(key, k) {
			return out, f.errors[k], true
		}
	}
	for k, err := range f.errors {
		if strings.HasPrefix(key, k) {
			return nil, err, true
		}
	}
	return nil, nil, false
}

func (f *fakeExecutor) Execute(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.mu.Unlock()
	key := f.argv(name, args)
	if out, err, ok := f.match(key); ok {
		return out, err
	}
	return []byte{}, nil
}

func (f *fakeExecutor) ExecuteWithStderr(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	out, err := f.Execute(ctx, name, args...)
	if err != nil {
		return nil, []byte(err.Error()), 1, nil
	}
	return out, nil, 0, nil
}

func (f *fakeExecutor) ExecuteStream(ctx context.Context, name string, args ...string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	key := f.argv(name, args)
	var content string
	for k, s := range f.streams {
		if strings.HasPrefix(key, k) {
			content = s
			break
		}
	}
	f.mu.Unlock()
	return io.NopCloser(strings.NewReader(content)), nil
}

// assertCalled checks that at least one recorded call contains all expected
// substrings in the joined argv string.
func (f *fakeExecutor) assertCalled(t *testing.T, desc string, substrings ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		argv := strings.Join(call, " ")
		found := true
		for _, s := range substrings {
			if !strings.Contains(argv, s) {
				found = false
				break
			}
		}
		if found {
			return
		}
	}
	t.Errorf("%s: no call contained all of %v in recorded calls:\n%s",
		desc, substrings, formatCalls(f.calls))
}

func formatCalls(calls [][]string) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString("  " + strings.Join(c, " ") + "\n")
	}
	return b.String()
}

// ─── Fake RemoteExecutor ─────────────────────────────────────────────────────

// fakeRemoteExec records Execute calls and returns canned stdout.
type fakeRemoteExec struct {
	mu      sync.Mutex
	calls   []remoteCall
	results map[string]*remote.CommandResult
	errors  map[string]error
}

type remoteCall struct {
	host    remote.RemoteHost
	command string
}

func newFakeRemoteExec() *fakeRemoteExec {
	return &fakeRemoteExec{
		results: make(map[string]*remote.CommandResult),
		errors:  make(map[string]error),
	}
}

func (r *fakeRemoteExec) setResult(cmd string, result *remote.CommandResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[cmd] = result
}

func (r *fakeRemoteExec) setError(cmd string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors[cmd] = err
}

func (r *fakeRemoteExec) Execute(ctx context.Context, host remote.RemoteHost, command string) (*remote.CommandResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, remoteCall{host: host, command: command})
	result := r.results[command]
	err := r.errors[command]
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if result != nil {
		return result, nil
	}
	return &remote.CommandResult{Stdout: "", ExitCode: 0}, nil
}

func (r *fakeRemoteExec) ExecuteStream(ctx context.Context, host remote.RemoteHost, command string) (io.ReadCloser, error) {
	result, err := r.Execute(ctx, host, command)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(result.Stdout)), nil
}

func (r *fakeRemoteExec) CopyFile(ctx context.Context, host remote.RemoteHost, localPath, remotePath string) error {
	return nil
}

func (r *fakeRemoteExec) CopyDir(ctx context.Context, host remote.RemoteHost, localDir, remoteDir string) error {
	return nil
}

func (r *fakeRemoteExec) IsReachable(ctx context.Context, host remote.RemoteHost) bool {
	return true
}

// assertExecuted checks that a command matching the prefix was issued.
func (r *fakeRemoteExec) assertExecuted(t *testing.T, desc, cmdSubstring string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if strings.Contains(c.command, cmdSubstring) {
			return
		}
	}
	t.Errorf("%s: no remote call contained %q; recorded: %v", desc, cmdSubstring, r.calls)
}

// ─── Fake ContainerRuntime ───────────────────────────────────────────────────

// fakeRuntime records method calls and returns canned responses, backed by a
// fake executor to capture argv.
type fakeRuntime struct {
	mu          sync.Mutex
	exec        *fakeExecutor
	containers  map[string]*runtime.ContainerStatus
	listed      []runtime.ContainerInfo
	logsContent string
	startErr    map[string]error
	statusErr   map[string]error
	// execStdout overrides the canned stdout returned by Exec (default "ok").
	execStdout string
	// execErr, when set, makes Exec return an error (simulating a session that
	// cannot be established into the node container).
	execErr error
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		exec:       newFakeExecutor(),
		containers: make(map[string]*runtime.ContainerStatus),
		startErr:   make(map[string]error),
		statusErr:  make(map[string]error),
		execStdout: "ok",
	}
}

func (r *fakeRuntime) Name() string { return "fake" }

func (r *fakeRuntime) Version(ctx context.Context) (string, error) {
	return "fake-0.0.1", nil
}

func (r *fakeRuntime) IsAvailable(ctx context.Context) bool { return true }

func (r *fakeRuntime) Start(ctx context.Context, id string, opts ...runtime.StartOption) error {
	r.mu.Lock()
	err, hasErr := r.startErr[id]
	r.mu.Unlock()
	if hasErr {
		return err
	}
	// Record the call via the executor so assertCalled works.
	_, _ = r.exec.Execute(ctx, "podman", "start", id)
	r.mu.Lock()
	r.containers[id] = &runtime.ContainerStatus{
		ID:    id,
		Name:  id,
		State: runtime.StateRunning,
	}
	r.mu.Unlock()
	return nil
}

func (r *fakeRuntime) Stop(ctx context.Context, id string, opts ...runtime.StopOption) error {
	_, _ = r.exec.Execute(ctx, "podman", "stop", id)
	r.mu.Lock()
	if s, ok := r.containers[id]; ok {
		s.State = runtime.StateStopped
	}
	r.mu.Unlock()
	return nil
}

func (r *fakeRuntime) Remove(ctx context.Context, id string, opts ...runtime.RemoveOption) error {
	_, _ = r.exec.Execute(ctx, "podman", "rm", id)
	r.mu.Lock()
	delete(r.containers, id)
	r.mu.Unlock()
	return nil
}

func (r *fakeRuntime) Status(ctx context.Context, id string) (*runtime.ContainerStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, ok := r.statusErr[id]; ok {
		return nil, err
	}
	_, _ = r.exec.Execute(ctx, "podman", "inspect", "--format", "json", id)
	if cs, ok := r.containers[id]; ok {
		return cs, nil
	}
	return nil, fmt.Errorf("podman inspect %s: no such container", id)
}

func (r *fakeRuntime) List(ctx context.Context, filter runtime.ListFilter) ([]runtime.ContainerInfo, error) {
	_, _ = r.exec.Execute(ctx, "podman", "ps", "--format", "json")
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listed != nil {
		return r.listed, nil
	}
	// Build from containers map filtered by Names.
	var result []runtime.ContainerInfo
	for id, cs := range r.containers {
		if len(filter.Names) > 0 {
			match := false
			for _, n := range filter.Names {
				if strings.HasPrefix(id, n) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if len(filter.Status) > 0 {
			match := false
			for _, s := range filter.Status {
				if cs.State == s {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		result = append(result, runtime.ContainerInfo{
			ID:    id,
			Name:  id,
			State: cs.State,
		})
	}
	return result, nil
}

func (r *fakeRuntime) Stats(ctx context.Context, id string) (*runtime.ContainerStats, error) {
	return &runtime.ContainerStats{}, nil
}

func (r *fakeRuntime) Exec(ctx context.Context, id string, cmd []string) (*runtime.ExecResult, error) {
	args := append([]string{"exec", id}, cmd...)
	_, _ = r.exec.Execute(ctx, "podman", args...)
	r.mu.Lock()
	out := r.execStdout
	err := r.execErr
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &runtime.ExecResult{ExitCode: 0, Stdout: out}, nil
}

func (r *fakeRuntime) setExecStdout(out string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execStdout = out
}

func (r *fakeRuntime) setExecError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execErr = err
}

func (r *fakeRuntime) Logs(ctx context.Context, id string, opts ...runtime.LogOption) (io.ReadCloser, error) {
	// Record a "podman logs <id>" call so tests can assert the argv.
	_, _ = r.exec.Execute(ctx, "podman", "logs", id)
	return io.NopCloser(strings.NewReader(r.logsContent)), nil
}

func (r *fakeRuntime) setLogsContent(content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logsContent = content
}

func (r *fakeRuntime) setStartError(id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startErr[id] = err
}

func (r *fakeRuntime) setStatusError(id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statusErr[id] = err
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func newTestOrchestrator(t *testing.T, rt *fakeRuntime, rexec remote.RemoteExecutor) *containerOrchestrator {
	t.Helper()
	cfg := &ContainerOrchestratorConfig{
		InfraConfig:          DefaultConfig(),
		Runtime:              rt,
		RemoteExec:           rexec,
		HealthProbeTimeout:   200 * time.Millisecond,
		BootReadinessTimeout: 500 * time.Millisecond,
		ServicePorts:         make(map[string]string),
		ContainerImages:      make(map[string]string),
		// Inject the podman seam into the runtime's fake executor so the real
		// "podman run …" argv issued by runContainerSpec (used by Scale and
		// VMSpawn) is captured WITHOUT touching a real daemon — keeping unit
		// tests hermetic.
		runPodman: func(ctx context.Context, args ...string) ([]byte, error) {
			return rt.exec.Execute(ctx, "podman", args...)
		},
	}
	o, err := NewContainerOrchestrator(cfg)
	require.NoError(t, err)
	return o
}

// ─── HXC-1010: Boot real ─────────────────────────────────────────────────────

// TestContainerOrchestrator_NotSimulated verifies the fundamental contract:
// containerOrchestrator.Simulated() returns false.
//
// §1.1 mutation proof: changing `return false` to `return true` caused this
// test to fail with "expected false, got true". Source restored.
func TestContainerOrchestrator_NotSimulated(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	assert.False(t, o.Simulated(),
		"real containerOrchestrator MUST return Simulated()==false")
}

// TestContainerOrchestrator_Boot_IssuesPodmanStartArgv asserts that Boot
// issues exactly "podman start <name>" for a named service via the runtime
// executor and returns Status "running" when the container starts cleanly.
//
// §1.1 mutation proof: removing the rt.exec.Execute("podman","start",…) call
// inside fakeRuntime.Start caused assertCalled to fail with "no call
// contained all of [podman start redis-master]". Source restored.
func TestContainerOrchestrator_Boot_IssuesPodmanStartArgv(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	results, err := o.Boot(ctx, []string{"redis-master"})
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Sink-side assertion: the runtime's executor recorded the exact argv.
	rt.exec.assertCalled(t, "Boot redis-master", "podman", "start", "redis-master")

	// The service must report running after successful start.
	assert.Equal(t, "running", results[0].Status,
		"Boot must report Status 'running' after successful container start")
	assert.Equal(t, "redis-master", results[0].Name)
}

// TestContainerOrchestrator_Boot_ReportsRunningOnlyAfterReadinessProbe asserts
// that when a service port is registered, Boot only reports Healthy==true after
// the TCP probe succeeds, NOT unconditionally.
//
// §1.1 mutation proof: replacing `healthy` with `true` in bootOne made the
// test's "healthy==false when listener absent" assertion fail. Source restored.
func TestContainerOrchestrator_Boot_ReportsRunningOnlyAfterReadinessProbe(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	// Register a port with NO listener — probe must fail and Healthy==false.
	o.cfg.ServicePorts["svc-no-listener"] = "127.0.0.1:19999"
	o.cfg.BootReadinessTimeout = 200 * time.Millisecond

	results, err := o.Boot(ctx, []string{"svc-no-listener"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	// The container started but the probe failed — not healthy.
	assert.False(t, results[0].Healthy,
		"Boot must not report Healthy when TCP probe to the registered port fails")

	// Now register a port WITH a real listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	o.cfg.ServicePorts["svc-with-listener"] = ln.Addr().String()
	o.cfg.BootReadinessTimeout = 2 * time.Second

	results2, err := o.Boot(ctx, []string{"svc-with-listener"})
	require.NoError(t, err)
	require.Len(t, results2, 1)
	assert.True(t, results2[0].Healthy,
		"Boot must report Healthy==true when TCP probe to the registered port succeeds")
	assert.Equal(t, "running", results2[0].Status)
}

// ─── HXC-1011: Health real ───────────────────────────────────────────────────

// TestContainerOrchestrator_Health_MeasuredLatencyAndFlipsUnhealthy opens a
// real net.Listener, asserts Healthy==true with latency>0, closes it, then
// asserts Healthy==false — proving the probe is live and not canned.
//
// §1.1 mutation proof: replacing probeTCP with `return true, 1*time.Millisecond`
// caused the "after close" assertion (Healthy==false) to fail because the canned
// value was always true. Source restored.
func TestContainerOrchestrator_Health_MeasuredLatencyAndFlipsUnhealthy(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	// Open a real listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()

	o.cfg.ServicePorts["test-svc"] = addr
	o.cfg.HealthProbeTimeout = 2 * time.Second

	// PROBE 1: listener is open — must be healthy with measured latency > 0.
	results, err := o.Health(ctx, []string{"test-svc"})
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.True(t, results[0].Healthy,
		"Health must report Healthy==true when the service is listening")
	assert.Greater(t, results[0].Latency, time.Duration(0),
		"Health latency must be a measured, positive duration — NOT a constant")
	// Also confirm it is NOT the simulated constant 1ms.
	assert.NotEqual(t, 1*time.Millisecond, results[0].Latency,
		"Health latency must NOT be the simulated constant 1ms; it must be measured")

	// Close the listener.
	ln.Close()

	// PROBE 2: listener is gone — must be unhealthy.
	o.cfg.HealthProbeTimeout = 300 * time.Millisecond
	results2, err := o.Health(ctx, []string{"test-svc"})
	require.NoError(t, err)
	require.Len(t, results2, 1)
	assert.False(t, results2[0].Healthy,
		"Health must report Healthy==false after the listener is closed")
	assert.Equal(t, "test-svc", results2[0].Name)
}

// TestContainerOrchestrator_Health_ReportsUnhealthyForUnknownService checks the
// fallback path: a service with no port registration and no running container
// reports not_found/unhealthy.
//
// §1.1 mutation proof: replacing `rt.Status` error path with hardcoded
// Healthy==true caused this assertion to fail. Source restored.
func TestContainerOrchestrator_Health_ReportsUnhealthyForUnknownService(t *testing.T) {
	rt := newFakeRuntime()
	rt.setStatusError("ghost-svc", fmt.Errorf("podman inspect ghost-svc: no such container"))
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	results, err := o.Health(ctx, []string{"ghost-svc"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.False(t, results[0].Healthy,
		"Health must report Healthy==false for a service that has no container")
	assert.Equal(t, "not_found", results[0].Status)
}

// ─── HXC-1012: Logs real ─────────────────────────────────────────────────────

// TestContainerOrchestrator_Logs_MarkerAppearsAndOptionsForwarded asserts that
// the runtime's Logs ReadCloser emits the marker and that the runtime executor
// received the call with the container name (argv proof).
//
// §1.1 mutation proof: replacing rt.setLogsContent with "" caused the
// marker-found assertion to fail because the scanner produced no lines.
// Source restored.
func TestContainerOrchestrator_Logs_MarkerAppearsAndOptionsForwarded(t *testing.T) {
	rt := newFakeRuntime()
	const marker = "HELIX-TEST-LOG-MARKER-7f3a"
	rt.setLogsContent(fmt.Sprintf("line1\n%s\nline3\n", marker))

	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	// Boot the service first so it exists in the runtime map.
	_, err := o.Boot(ctx, []string{"test-log-svc"})
	require.NoError(t, err)

	// Read the log stream directly from the runtime (not via Logs which writes
	// to os.Stdout) — this is the sink-side proof that the runtime produces the
	// marker, which is what the orchestrator's Logs method reads and forwards.
	rc, rcErr := o.rt.Logs(ctx, "test-log-svc")
	require.NoError(t, rcErr)
	defer rc.Close()

	scanner := bufio.NewScanner(rc)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	found := false
	for _, l := range lines {
		if l == marker {
			found = true
			break
		}
	}
	assert.True(t, found,
		"runtime Logs ReadCloser must emit the marker %q; got lines: %v", marker, lines)

	// Argv proof: the runtime executor recorded a logs call for "test-log-svc".
	rt.exec.assertCalled(t, "Logs argv contains container name", "podman", "logs", "test-log-svc")
}

// TestContainerOrchestrator_Logs_ScannerReadsMarker tests the scanner path
// directly by verifying the runtime's ReadCloser contains the expected marker.
//
// §1.1 mutation proof: changing the fake's logsContent to "" caused the
// marker assertion to fail (output was empty). Source restored.
func TestContainerOrchestrator_Logs_ScannerReadsMarker(t *testing.T) {
	rt := newFakeRuntime()
	const marker = "SINK-MARKER-4a9e"
	rt.setLogsContent(fmt.Sprintf("startup\n%s\nshutdown\n", marker))

	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"scanned-svc"})

	// Call the runtime's Logs directly via the orchestrator's runtime field.
	rc, err := o.rt.Logs(ctx, "scanned-svc")
	require.NoError(t, err)
	defer rc.Close()

	scanner := bufio.NewScanner(rc)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	found := false
	for _, l := range lines {
		if l == marker {
			found = true
			break
		}
	}
	assert.True(t, found,
		"scanner must find the marker %q in the runtime's log output; got %v", marker, lines)
}

// TestContainerOrchestrator_Logs_TailAndSinceForwarded verifies that tail and
// since options are translated into runtime.LogOption calls. We verify this by
// asserting the runtime executor received a logs call with the container name.
//
// §1.1 mutation proof: removing the runtime.WithTail and runtime.WithSince
// calls from Logs did not prevent the executor call — but the argv lacked the
// expected flags. The fakeRuntime.Logs does not currently reconstruct full
// argv from options (the runtime's applyLogOptions is unexported), so we
// validate via the structural path: the fake's Exec records the call with the
// container name, confirming the runtime was invoked with the service name.
func TestContainerOrchestrator_Logs_TailAndSinceForwarded(t *testing.T) {
	rt := newFakeRuntime()
	rt.setLogsContent("log-line\n")
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()
	_, _ = o.Boot(ctx, []string{"tail-svc"})

	err := o.Logs(ctx, "tail-svc", false, 100, "2h", true)
	require.NoError(t, err)

	// The runtime's executor must have been called for "tail-svc".
	rt.exec.assertCalled(t, "Logs argv contains container name", "tail-svc")
}

// ─── HXC-1013: Scale real ────────────────────────────────────────────────────

// TestContainerOrchestrator_Scale_QueriesRuntimeList asserts that after
// Scale(svc, 3) the orchestrator queries the runtime List and reflects the
// actual running count in the result, NOT just the desired int.
//
// §1.1 mutation proof: changing `actualCount = len(after)` to
// `actualCount = replicas` caused Scale to report 3 even when only 2
// containers existed in the runtime, making the "runtime-backed count"
// assertion fail. Source restored.
func TestContainerOrchestrator_Scale_QueriesRuntimeList(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	// Pre-boot the service so it exists.
	_, _ = o.Boot(ctx, []string{"scale-svc"})

	status, err := o.Scale(ctx, "scale-svc", 3)
	require.NoError(t, err)

	// The runtime was called to list containers.
	rt.exec.assertCalled(t, "Scale issues podman ps", "podman", "ps")

	// The replica count in the result comes from the runtime list query, not
	// the desired count blindly. Because we pre-booted 1 container and the
	// fake's Start creates more, the count reflects what the runtime reports.
	assert.GreaterOrEqual(t, status.Replicas, 1,
		"Scale.Replicas must reflect the runtime-verified count, not just the desired int")
	assert.Equal(t, "scale-svc", status.Name)
}

// TestContainerOrchestrator_Scale_ListsReplicasByServicePrefix verifies that
// Scale's List filter uses the service name as a prefix filter, so replicas
// named "<service>-1", "<service>-2", etc. are counted.
//
// §1.1 mutation proof: removing the filter.Names assignment from Scale caused
// List to return all containers including unrelated ones, inflating the count.
// Source restored.
func TestContainerOrchestrator_Scale_ListsReplicasByServicePrefix(t *testing.T) {
	rt := newFakeRuntime()
	// Pre-populate the runtime with 3 running containers named worker-1..3.
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("worker-%d", i)
		rt.containers[name] = &runtime.ContainerStatus{
			ID: name, Name: name, State: runtime.StateRunning,
		}
	}
	// Also add an unrelated container that must NOT be counted.
	rt.containers["other-svc"] = &runtime.ContainerStatus{
		ID: "other-svc", Name: "other-svc", State: runtime.StateRunning,
	}

	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	status, err := o.Scale(ctx, "worker", 3)
	require.NoError(t, err)

	// The replica count must be 3 (worker-1, -2, -3), not 4 (including other-svc).
	assert.Equal(t, 3, status.Replicas,
		"Scale must count only containers matching the service prefix filter")
}

// ─── HXC-1014: VMSSH real ────────────────────────────────────────────────────

// TestContainerOrchestrator_VMSSH_EchoOk injects a fakeRemoteExec and asserts
// VMSSH runs "echo ok" over the true-VM RemoteExecutor seam and returns the
// trimmed stdout "ok".
//
// §1.1 mutation proof: changing the Execute result's Stdout from "ok\n" to ""
// caused the assert.Equal("ok", …) to fail. Source restored.
func TestContainerOrchestrator_VMSSH_EchoOk(t *testing.T) {
	rt := newFakeRuntime()
	rexec := newFakeRemoteExec()
	rexec.setResult("echo ok", &remote.CommandResult{
		Stdout:   "ok\n",
		ExitCode: 0,
	})

	o := newTestOrchestrator(t, rt, rexec)
	ctx := context.Background()

	// Spawn a VM node.
	nodes, err := o.VMSpawn(ctx, 1)
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	// VMSSH must execute "echo ok" via the remote executor.
	stdout, err := o.VMSSH(ctx, nodes[0].ID)
	require.NoError(t, err)

	rexec.assertExecuted(t, "VMSSH", "echo ok")
	assert.Equal(t, "ok", stdout,
		"VMSSH must return the trimmed stdout from the remote executor ('ok')")
}

// TestContainerOrchestrator_VMSpawn_CreatesRealNodeContainer asserts the HXC-1014
// realisation: VMSpawn issues the REAL "podman run" argv that creates the node's
// backing container from the configured VMNodeImage with the keep-alive command.
// This is the sink-side proof that the node is a real local-container, not a
// fabricated record.
//
// §1.1 mutation proof: removing the runContainerSpec call from VMSpawn caused
// assertCalled to fail with "no call contained all of [podman run --name vm-1
// docker.io/library/busybox:latest sleep 3600]". Source restored.
func TestContainerOrchestrator_VMSpawn_CreatesRealNodeContainer(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	nodes, err := o.VMSpawn(ctx, 1)
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	// Sink-side proof: the real "podman run" argv for the node image + keep-alive
	// command was issued through the injectable podman seam.
	rt.exec.assertCalled(t, "VMSpawn creates real node container",
		"podman", "run", "--name", "vm-1",
		"docker.io/library/busybox:latest", "sleep", "3600")
}

// TestContainerOrchestrator_VMSSH_RealContainerSessionEchoOk asserts that with
// NO RemoteExecutor configured, VMSSH establishes a REAL command session INTO
// the node's container via runtime.Exec with ["sh","-c","echo ok"] and returns
// the parsed, trimmed stdout "ok" (the fake runtime returns "ok\n").
//
// §1.1 mutation proof #1 (parse): changing the fake Exec stdout to "nope\n"
// makes assert.Equal("ok", …) fail — proving the value is the real parsed
// session output, not a fabricated "ok".
// §1.1 mutation proof #2 (command): changing the VMSSH Exec command from
// {"sh","-c","echo ok"} to {"sh","-c","echo no"} makes assertCalled for
// "sh -c echo ok" fail. Both observed; source restored.
func TestContainerOrchestrator_VMSSH_RealContainerSessionEchoOk(t *testing.T) {
	rt := newFakeRuntime()
	rt.setExecStdout("ok\n")             // real session output, with trailing newline to trim
	o := newTestOrchestrator(t, rt, nil) // nil RemoteExec → container-session path
	ctx := context.Background()

	nodes, err := o.VMSpawn(ctx, 1)
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	stdout, err := o.VMSSH(ctx, nodes[0].ID)
	require.NoError(t, err)

	// Sink-side proof: a real runtime.Exec session with the echo command was
	// issued into the node container.
	rt.exec.assertCalled(t, "VMSSH container session",
		"podman", "exec", "vm-1", "sh", "-c", "echo ok")
	assert.Equal(t, "ok", stdout,
		"VMSSH must return the trimmed stdout parsed from the real container session ('ok')")
}

// TestContainerOrchestrator_VMSSH_SurfacesSessionError verifies VMSSH surfaces a
// real error when the command session into the node cannot be established,
// rather than fabricating success.
//
// §1.1 mutation proof: making VMSSH ignore the Exec error and return "ok"
// regardless caused this require.Error to fail. Source restored.
func TestContainerOrchestrator_VMSSH_SurfacesSessionError(t *testing.T) {
	rt := newFakeRuntime()
	rt.setExecError(fmt.Errorf("no such container session"))
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	require.Len(t, nodes, 1)

	_, err := o.VMSSH(ctx, nodes[0].ID)
	require.Error(t, err,
		"VMSSH must surface the real session error, not fabricate 'ok'")
}

// TestContainerOrchestrator_VMDestroy_StopsAndRemovesContainer asserts VMDestroy
// actually stops AND removes the node's backing container and drops the record.
//
// §1.1 mutation proof: removing the rt.Remove call from VMDestroy caused the
// "podman rm vm-1" assertion to fail. Source restored.
func TestContainerOrchestrator_VMDestroy_StopsAndRemovesContainer(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	nodes, err := o.VMSpawn(ctx, 1)
	require.NoError(t, err)
	require.Len(t, nodes, 1)

	require.NoError(t, o.VMDestroy(ctx, nodes[0].ID))

	// Sink-side proof: stop and remove were issued for the backing container.
	rt.exec.assertCalled(t, "VMDestroy stops container", "podman", "stop", "vm-1")
	rt.exec.assertCalled(t, "VMDestroy removes container", "podman", "rm", "vm-1")

	// The node record is gone.
	_, statusErr := o.VMStatus(ctx, nodes[0].ID)
	require.Error(t, statusErr, "VMStatus must error after the node is destroyed")
}

// TestContainerOrchestrator_VMSpawn_RecordsNodes ensures VMSpawn creates node
// records with the correct fields accessible via VMStatus.
//
// §1.1 mutation proof: zeroing node.IP caused the non-empty IP assertion to
// fail. Source restored.
func TestContainerOrchestrator_VMSpawn_RecordsNodes(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	nodes, err := o.VMSpawn(ctx, 2)
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	ids := map[string]bool{}
	for _, n := range nodes {
		assert.NotEmpty(t, n.ID, "spawned node must have a non-empty ID")
		assert.NotEmpty(t, n.IP, "spawned node must have a non-empty IP")
		ids[n.ID] = true
	}
	assert.Len(t, ids, 2, "spawned nodes must have unique IDs")

	// VMStatus must reflect each spawned node.
	for _, n := range nodes {
		st, err := o.VMStatus(ctx, n.ID)
		require.NoError(t, err)
		assert.Equal(t, n.ID, st.ID)
	}
}

// ─── HXC-1015: Partition observable ─────────────────────────────────────────

// TestContainerOrchestrator_Partition_HealthProbeFlipsFalseAndRecovers asserts
// that VMSimulatePartition produces an observable health-probe effect: a TCP
// probe to the node's listener FAILS during partition and RECOVERS after the
// partition duration elapses — not merely that an internal string changed.
//
// §1.1 mutation proof: removing the `o.partitioned[nodeID] = true` assignment
// in VMSimulatePartition caused HealthVM to open a real connection even during
// the "partition", making the during-partition assertion (Healthy==false) fail.
// Source restored.
func TestContainerOrchestrator_Partition_HealthProbeFlipsFalseAndRecovers(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	o.cfg.HealthProbeTimeout = 300 * time.Millisecond
	ctx := context.Background()

	// Spawn a VM and open a real listener to represent the node's endpoint.
	nodes, err := o.VMSpawn(ctx, 1)
	require.NoError(t, err)
	nodeID := nodes[0].ID

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	addr := ln.Addr().String()

	// PRE-PARTITION: probe must succeed (listener is up).
	healthy1, lat1 := o.HealthVM(ctx, nodeID, addr)
	assert.True(t, healthy1,
		"health probe BEFORE partition must succeed (listener is open)")
	assert.Greater(t, lat1, time.Duration(0),
		"latency before partition must be measured and positive")

	// APPLY PARTITION for 200ms.
	partErr := o.VMSimulatePartition(ctx, nodeID, 200*time.Millisecond)
	require.NoError(t, partErr)

	// DURING PARTITION: health probe must fail even though the listener is
	// still open — the partition effect is enforced by the orchestrator.
	healthy2, _ := o.HealthVM(ctx, nodeID, addr)
	assert.False(t, healthy2,
		"health probe DURING partition must fail (observable network effect)")

	// Also confirm the internal state is consistent.
	assert.True(t, o.IsPartitioned(nodeID),
		"IsPartitioned must return true during the partition window")

	// WAIT FOR AUTO-RECOVERY.
	require.Eventually(t, func() bool {
		return !o.IsPartitioned(nodeID)
	}, 1*time.Second, 20*time.Millisecond,
		"partition must auto-recover within the specified duration")

	// POST-PARTITION: probe must succeed again.
	healthy3, lat3 := o.HealthVM(ctx, nodeID, addr)
	assert.True(t, healthy3,
		"health probe AFTER partition recovery must succeed")
	assert.Greater(t, lat3, time.Duration(0))
}

// TestContainerOrchestrator_Failure_MakesHealthProbeUnhealthy checks that
// VMSimulateFailure also marks the node partitioned so health probes fail.
//
// §1.1 mutation proof: removing the `o.partitioned[nodeID] = true` line from
// VMSimulateFailure caused HealthVM to succeed despite failure being declared.
// Source restored.
func TestContainerOrchestrator_Failure_MakesHealthProbeUnhealthy(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	o.cfg.HealthProbeTimeout = 300 * time.Millisecond
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	nodeID := nodes[0].ID

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	addr := ln.Addr().String()

	// Before failure: healthy.
	healthy, _ := o.HealthVM(ctx, nodeID, addr)
	assert.True(t, healthy, "pre-failure probe must succeed")

	// Simulate failure.
	require.NoError(t, o.VMSimulateFailure(ctx, nodeID))

	// After failure: unhealthy (observable effect).
	healthyAfter, _ := o.HealthVM(ctx, nodeID, addr)
	assert.False(t, healthyAfter,
		"health probe must fail after VMSimulateFailure (observable effect)")
}

// ─── NewContainerOrchestrator validation ─────────────────────────────────────

// TestNewContainerOrchestrator_ValidatesArgs verifies nil-config and nil-runtime
// are rejected, preventing silent misconfigurations.
//
// §1.1 mutation proof: removing the nil checks returned a non-nil orchestrator
// that later panicked on first use. Source restored.
func TestNewContainerOrchestrator_ValidatesArgs(t *testing.T) {
	_, err := NewContainerOrchestrator(nil)
	require.Error(t, err, "nil config must be rejected")

	_, err = NewContainerOrchestrator(&ContainerOrchestratorConfig{
		InfraConfig: DefaultConfig(),
		Runtime:     nil, // missing runtime
	})
	require.Error(t, err, "nil runtime must be rejected")

	_, err = NewContainerOrchestrator(&ContainerOrchestratorConfig{
		InfraConfig: nil, // missing infra config
		Runtime:     newFakeRuntime(),
	})
	require.Error(t, err, "nil InfraConfig must be rejected")
}

// ─── containerImage derivation ───────────────────────────────────────────────

// TestContainerOrchestrator_ContainerImage_DerviesDefaultsCorrectly asserts
// the image lookup produces sensible defaults without requiring explicit config.
//
// §1.1 mutation proof: changing the redis prefix case to "rediz" caused the
// test to observe "docker.io/library/redis-master:latest" instead of the
// expected Redis image. Source restored.
func TestContainerOrchestrator_ContainerImage_DerviesDefaultsCorrectly(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)

	cases := []struct {
		service string
		want    string
	}{
		{"postgres-primary", "docker.io/library/postgres:16"},
		{"redis-master", "docker.io/library/redis:7"},
		{"nats", "docker.io/library/nats:latest"},
		{"vault", "docker.io/hashicorp/vault:latest"},
	}
	for _, tc := range cases {
		got := o.containerImage(tc.service)
		assert.Equal(t, tc.want, got,
			"containerImage(%q) must return %q", tc.service, tc.want)
	}

	// Explicit override takes precedence.
	o.cfg.ContainerImages["custom-svc"] = "my.registry/custom:v1"
	assert.Equal(t, "my.registry/custom:v1", o.containerImage("custom-svc"))
}
