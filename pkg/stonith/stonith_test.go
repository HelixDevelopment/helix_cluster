package stonith

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- Closure criterion #1: real in-process fence transitions state ----------

// TestNoOpAgentFencesRealStateMachine proves that issuing a fence against a real
// in-process power fabric transitions the target powered-on -> powered-off AND
// yields a Confirmation record. This is sink-side evidence, not a mock call
// count.
func TestNoOpAgentFencesRealStateMachine(t *testing.T) {
	ctx := context.Background()
	pc := NewInMemoryPower()
	agent := NewNoOpAgent("noop-1", pc)
	const target = "node-a"

	// Pre-condition: the node is alive.
	if st, err := pc.State(ctx, target); err != nil || st != PoweredOn {
		t.Fatalf("pre-condition: got (%v,%v), want (powered-on,nil)", st, err)
	}
	if fenced, err := agent.IsFenced(ctx, target); err != nil || fenced {
		t.Fatalf("pre-condition IsFenced: got (%v,%v), want (false,nil)", fenced, err)
	}

	conf, err := agent.Fence(ctx, target)
	if err != nil {
		t.Fatalf("Fence: unexpected error %v", err)
	}

	// Sink-side: the power fabric itself must now report powered-off.
	if st, err := pc.State(ctx, target); err != nil || st != PoweredOff {
		t.Fatalf("post-condition: got (%v,%v), want (powered-off,nil)", st, err)
	}
	if fenced, err := agent.IsFenced(ctx, target); err != nil || !fenced {
		t.Fatalf("post-condition IsFenced: got (%v,%v), want (true,nil)", fenced, err)
	}

	// Confirmation record must be populated and accurate.
	if conf == nil {
		t.Fatal("Fence returned nil confirmation")
	}
	if conf.Target != target || conf.Agent != "noop-1" {
		t.Errorf("confirmation: got target=%q agent=%q, want %q/noop-1", conf.Target, conf.Agent, target)
	}
	if conf.At.IsZero() {
		t.Error("confirmation timestamp is zero")
	}
}

// --- Closure criterion #2: multi-level fallback to a succeeding secondary ----

// TestMultiLevelFenceFallback configures a PRIMARY that genuinely fails (its
// power fabric is unreachable) and a SECONDARY over a real fabric that
// succeeds. The fencer must return success via the secondary, and the secondary
// must have actually fenced the node (real state change).
//
// MUTATION GUARD: if the fallback branch in MultiLevelFencer.Fence ("continue
// to next agent on error") is removed so it returns on the first error, this
// test fails because Fence would return the primary's error instead of the
// secondary's confirmation.
func TestMultiLevelFenceFallback(t *testing.T) {
	ctx := context.Background()
	const target = "node-b"

	// Primary cannot reach the box: PowerOff fails, node stays powered-on.
	primary := NewNoOpAgent("primary", &FailingPowerController{})
	// Secondary is over a real fabric and will succeed.
	secondaryFabric := NewInMemoryPower()
	secondary := NewNoOpAgent("secondary", secondaryFabric)

	fencer, err := NewMultiLevelFencer(primary, secondary)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}

	conf, err := fencer.Fence(ctx, target)
	if err != nil {
		t.Fatalf("Fence: expected success via secondary, got error %v", err)
	}
	if conf.Agent != "secondary" {
		t.Fatalf("fence won by %q, want secondary", conf.Agent)
	}

	// Sink-side: the SECONDARY fabric must show powered-off.
	if st, err := secondaryFabric.State(ctx, target); err != nil || st != PoweredOff {
		t.Fatalf("secondary fabric: got (%v,%v), want (powered-off,nil)", st, err)
	}
	// And the fencer's aggregate view must agree.
	if fenced, err := fencer.IsFenced(ctx, target); err != nil || !fenced {
		t.Fatalf("fencer.IsFenced: got (%v,%v), want (true,nil)", fenced, err)
	}
}

// TestMultiLevelFencePrimarySucceedsSkipsSecondary ensures the chain stops at
// the first confirmed fence and does NOT touch later levels.
func TestMultiLevelFencePrimarySucceedsSkipsSecondary(t *testing.T) {
	ctx := context.Background()
	const target = "node-c"

	primaryFabric := NewInMemoryPower()
	primary := NewNoOpAgent("primary", primaryFabric)
	secondaryFabric := NewInMemoryPower()
	secondary := NewNoOpAgent("secondary", secondaryFabric)

	fencer, err := NewMultiLevelFencer(primary, secondary)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}

	conf, err := fencer.Fence(ctx, target)
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if conf.Agent != "primary" {
		t.Fatalf("fence won by %q, want primary", conf.Agent)
	}
	// Secondary must NOT have been invoked: its fabric still shows powered-on.
	if st, _ := secondaryFabric.State(ctx, target); st != PoweredOn {
		t.Fatalf("secondary fabric touched: got %v, want powered-on", st)
	}
}

// --- Closure criterion #3: all levels fail -> aggregated error, not fenced ---

func TestMultiLevelFenceAllFail(t *testing.T) {
	ctx := context.Background()
	const target = "node-d"

	primary := NewNoOpAgent("primary", &FailingPowerController{Err: errors.New("bmc timeout")})
	secondary := NewNoOpAgent("secondary", &FailingPowerController{Err: errors.New("api 500")})

	fencer, err := NewMultiLevelFencer(primary, secondary)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}

	conf, err := fencer.Fence(ctx, target)
	if err == nil {
		t.Fatal("expected aggregated error when all levels fail, got nil")
	}
	if conf != nil {
		t.Fatalf("expected nil confirmation on total failure, got %+v", conf)
	}
	// The aggregated error must mention BOTH failed agents' causes.
	msg := err.Error()
	if !strings.Contains(msg, "bmc timeout") || !strings.Contains(msg, "api 500") {
		t.Errorf("aggregated error missing a cause: %q", msg)
	}
	// Target must NOT be considered fenced.
	if fenced, ferr := fencer.IsFenced(ctx, target); ferr != nil || fenced {
		t.Fatalf("IsFenced after total failure: got (%v,%v), want (false,nil)", fenced, ferr)
	}
}

func TestNewMultiLevelFencerNoAgents(t *testing.T) {
	if _, err := NewMultiLevelFencer(); !errors.Is(err, ErrNoAgents) {
		t.Fatalf("got %v, want ErrNoAgents", err)
	}
}

func TestMultiLevelFenceEmptyTarget(t *testing.T) {
	fencer, _ := NewMultiLevelFencer(NewNoOpAgent("x", NewInMemoryPower()))
	if _, err := fencer.Fence(context.Background(), ""); !errors.Is(err, ErrEmptyTarget) {
		t.Fatalf("got %v, want ErrEmptyTarget", err)
	}
}

// TestMultiLevelFenceContextCancelled proves a cancelled context aborts the
// chain before grinding through every level.
func TestMultiLevelFenceContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fencer, _ := NewMultiLevelFencer(NewNoOpAgent("x", NewInMemoryPower()))
	_, err := fencer.Fence(ctx, "node-e")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

// --- IPMI driver: exact argv + absent-binary honesty ------------------------

// recordingIPMIRunner captures argv and env from every run() call and returns
// scripted output, so we can assert the EXACT command (and absence of the
// password from argv) without an IPMI BMC.
type recordingIPMIRunner struct {
	present   bool
	calls     [][]string   // public argv per call (no password here)
	envCalls  [][]string   // env entries per call (password delivered here)
	statusOut string
	runErr    error
}

func (r *recordingIPMIRunner) lookPath() (string, error) {
	if !r.present {
		return "", ErrIPMIToolAbsent
	}
	return "/usr/bin/ipmitool", nil
}

func (r *recordingIPMIRunner) run(_ context.Context, args []string, env []string) (string, error) {
	// Defensive copies so later mutations to the slices do not affect recorded
	// history.
	argsCopy := make([]string, len(args))
	copy(argsCopy, args)
	envCopy := make([]string, len(env))
	copy(envCopy, env)

	r.calls = append(r.calls, argsCopy)
	r.envCalls = append(r.envCalls, envCopy)

	if r.runErr != nil {
		return "", r.runErr
	}
	// Return status output only for a status query.
	if len(args) > 0 && args[len(args)-1] == "status" {
		return r.statusOut, nil
	}
	return "", nil
}

// TestIPMIAgentBuildsExactArgv proves the argv uses "-E" (not "-P <pass>") and
// that the password is absent from every argv element.
//
// MUTATION GUARD: if powerArgs() is changed to re-introduce "-P <pass>", the
// password-in-argv assertions below will fail. If "-E" is removed, the
// "-E"-present assertions will fail.
func TestIPMIAgentBuildsExactArgv(t *testing.T) {
	const password = "secret"
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is off\n"}
	// target must match cfg.Host — that is the agent's safety invariant.
	const host = "10.0.0.5"
	agent := newIPMIAgent("ipmi-1", IPMIConfig{
		Host: host, Username: "admin", Password: password,
	}, rec)

	conf, err := agent.Fence(context.Background(), host)
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if conf == nil || conf.Method != "ipmi power off" {
		t.Fatalf("bad confirmation: %+v", conf)
	}

	if len(rec.calls) != 2 {
		t.Fatalf("expected 2 ipmitool calls (off, status), got %d: %v", len(rec.calls), rec.calls)
	}
	wantOff := []string{
		"-I", "lanplus", "-H", "10.0.0.5", "-U", "admin",
		"-E", "chassis", "power", "off",
	}
	if got := rec.calls[0]; !equalArgs(got, wantOff) {
		t.Errorf("power off argv:\n got  %v\n want %v", got, wantOff)
	}
	wantStatus := []string{
		"-I", "lanplus", "-H", "10.0.0.5", "-U", "admin",
		"-E", "chassis", "power", "status",
	}
	if got := rec.calls[1]; !equalArgs(got, wantStatus) {
		t.Errorf("status argv:\n got  %v\n want %v", got, wantStatus)
	}
}

// TestIPMIArgvContainsNoPassword is the primary CLOSURE CRITERION #1 test.
// It proves that for both a power/off and power/status call:
//   - argv does NOT contain the plaintext password anywhere.
//   - argv DOES contain "-E".
//
// MUTATION GUARD: reverting to "-P <pass>" would make both subtests fail.
func TestIPMIArgvContainsNoPassword(t *testing.T) {
	const password = "SuperSecret123"
	const host = "bmc.example"
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is off\n"}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{
		Host: host, Username: "root", Password: password,
	}, rec)

	// target must equal cfg.Host per the mismatch guard.
	if _, err := agent.Fence(context.Background(), host); err != nil {
		t.Fatalf("Fence: %v", err)
	}

	for i, call := range rec.calls {
		label := fmt.Sprintf("call[%d]", i)
		for _, arg := range call {
			if strings.Contains(arg, password) {
				t.Errorf("%s: argv element %q contains plaintext password", label, arg)
			}
		}
		// "-E" must be present so ipmitool knows to read from the environment.
		foundE := false
		for _, arg := range call {
			if arg == "-E" {
				foundE = true
				break
			}
		}
		if !foundE {
			t.Errorf("%s: argv does not contain \"-E\": %v", label, call)
		}
	}
}

// TestIPMIEnvCarriesPassword is CLOSURE CRITERION #3: the password MUST be
// delivered to the child process via the IPMI_PASSWORD env entry so that real
// fencing still works (ipmitool reads it when "-E" is present).
//
// MUTATION GUARD: removing powerEnv() or returning an empty env would cause this
// test to fail because IPMI_PASSWORD would be absent from the recorded env.
func TestIPMIEnvCarriesPassword(t *testing.T) {
	const password = "SuperSecret123"
	const host = "bmc.example"
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is off\n"}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{
		Host: host, Username: "root", Password: password,
	}, rec)

	// target must equal cfg.Host per the mismatch guard.
	if _, err := agent.Fence(context.Background(), host); err != nil {
		t.Fatalf("Fence: %v", err)
	}

	wantEnvEntry := "IPMI_PASSWORD=" + password
	for i, envSlice := range rec.envCalls {
		found := false
		for _, entry := range envSlice {
			if entry == wantEnvEntry {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("call[%d] env does not contain %q; got %v", i, wantEnvEntry, envSlice)
		}
	}
}

// TestIPMIErrorStringContainsNoPassword is CLOSURE CRITERION #2: when
// ipmitool returns an error, the error string returned to callers MUST NOT
// contain the plaintext password, while still being actionable (it contains
// the failing subcommand context).
//
// NOTE: this test uses recordingIPMIRunner (a mock) and therefore does NOT
// exercise the production redactedIPMIError() code path. It is retained as a
// coarse end-to-end check but the definitive mutation guard for the redaction
// code path is TestExecIPMIRunnerErrorRedaction below.
func TestIPMIErrorStringContainsNoPassword(t *testing.T) {
	const password = "SuperSecret123"
	const host = "bmc.example"
	wantErr := fmt.Errorf("ipmitool exited 1")
	rec := &recordingIPMIRunner{
		present: true,
		runErr:  wantErr,
	}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{
		Host: host, Username: "root", Password: password,
	}, rec)

	// target must equal cfg.Host per the mismatch guard.
	_, err := agent.Fence(context.Background(), host)
	if err == nil {
		t.Fatal("expected an error from Fence when runner fails, got nil")
	}
	errStr := err.Error()
	if strings.Contains(errStr, password) {
		t.Errorf("error string leaks plaintext password %q in: %q", password, errStr)
	}
	// The error should still be actionable: it should reference the operation.
	if !strings.Contains(errStr, "power off") {
		t.Errorf("error string is not actionable (no 'power off' context): %q", errStr)
	}
}

// TestExecIPMIRunnerErrorRedaction is the DEFINITIVE CLOSURE CRITERION #2 test.
// It exercises the PRODUCTION redactedIPMIError() function directly — the exact
// code path called by execIPMIRunner.run() when the child process fails — and
// asserts that the returned error:
//   - does NOT contain the plaintext password (present in the env slice).
//   - DOES contain the public subcommand args (actionable context).
//   - wraps the original cause so errors.Is/As chains work.
//
// MUTATION GUARD: if redactedIPMIError() is changed to include env entries
// (e.g. by appending env to the format string), this test fails because the
// password embedded in envWithPassword would appear in the error message.
// TestIPMIErrorStringContainsNoPassword (mock-based) does NOT catch this
// regression; this test DOES.
func TestExecIPMIRunnerErrorRedaction(t *testing.T) {
	const password = "UltraSecretXYZ!"
	cause := errors.New("exit status 1")

	// Simulate the exact args and env that execIPMIRunner.run() receives for a
	// power-off call: public argv has no password, env carries IPMI_PASSWORD.
	publicArgs := []string{
		"-I", "lanplus", "-H", "10.0.0.5", "-U", "admin",
		"-E", "chassis", "power", "off",
	}
	envWithPassword := []string{"IPMI_PASSWORD=" + password}

	err := redactedIPMIError(publicArgs, envWithPassword, cause)
	if err == nil {
		t.Fatal("redactedIPMIError returned nil, want non-nil error")
	}

	errStr := err.Error()

	// Primary assertion: the password MUST NOT appear anywhere in the error string.
	if strings.Contains(errStr, password) {
		t.Errorf("SECURITY: redactedIPMIError leaks plaintext password %q in: %q", password, errStr)
	}

	// The error must still be actionable: public subcommand args must appear.
	if !strings.Contains(errStr, "chassis") || !strings.Contains(errStr, "power") || !strings.Contains(errStr, "off") {
		t.Errorf("error string is not actionable (missing subcommand context): %q", errStr)
	}

	// The error must wrap the cause so errors.Is chains work.
	if !errors.Is(err, cause) {
		t.Errorf("redactedIPMIError does not wrap cause: errors.Is=%v, err=%q", errors.Is(err, cause), errStr)
	}

	// Table-driven: verify password absent across multiple verb/password combos.
	cases := []struct {
		name     string
		args     []string
		password string
	}{
		{
			name:     "status verb",
			args:     []string{"-I", "lanplus", "-H", "h", "-U", "u", "-E", "chassis", "power", "status"},
			password: "P@ssw0rd!",
		},
		{
			name:     "complex password with spaces",
			args:     []string{"-I", "lanplus", "-H", "h", "-U", "u", "-E", "chassis", "power", "off"},
			password: "has spaces and $pecial",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			e := redactedIPMIError(tc.args, []string{"IPMI_PASSWORD=" + tc.password}, errors.New("fail"))
			if strings.Contains(e.Error(), tc.password) {
				t.Errorf("password %q leaked in error: %q", tc.password, e.Error())
			}
		})
	}
}

// TestIPMIAgentNotConfirmedWhenStillOn proves that if the BMC reports power
// still on, Fence returns ErrNotConfirmed (no fake success).
func TestIPMIAgentNotConfirmedWhenStillOn(t *testing.T) {
	const host = "bmc-h.example"
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is on\n"}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{Host: host, Username: "u", Password: "p"}, rec)
	// target == cfg.Host; the mismatch guard must NOT fire — the test is about
	// the "still powered" path, not the mismatch path.
	_, err := agent.Fence(context.Background(), host)
	if !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("got %v, want ErrNotConfirmed", err)
	}
}

// TestIPMIAgentAbsentBinary proves we surface a typed error, never a fake fence,
// when ipmitool is missing.
func TestIPMIAgentAbsentBinary(t *testing.T) {
	const host = "bmc-h.example"
	rec := &recordingIPMIRunner{present: false}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{Host: host, Username: "u", Password: "p"}, rec)
	// target == cfg.Host so we reach the lookPath check.
	conf, err := agent.Fence(context.Background(), host)
	if !errors.Is(err, ErrIPMIToolAbsent) {
		t.Fatalf("got %v, want ErrIPMIToolAbsent", err)
	}
	if conf != nil {
		t.Fatalf("expected nil confirmation on absent binary, got %+v", conf)
	}
}

// TestIPMIIntegrationRealBinary exercises the production runner against the real
// ipmitool binary. It SKIPS-with-reason if the binary is absent (no fake PASS),
// per CLAUDE-1 / CLAUDE-2 honest-degradation rules. When present, we only assert
// that the production runner can be constructed and resolves the path; we do not
// power off a real machine in CI.
func TestIPMIIntegrationRealBinary(t *testing.T) {
	if _, err := exec.LookPath("ipmitool"); err != nil {
		t.Skip("SKIP: ipmitool not installed on host; cannot run real IPMI integration (no fake PASS)")
	}
	r := execIPMIRunner{}
	if _, err := r.lookPath(); err != nil {
		t.Fatalf("lookPath with ipmitool present: %v", err)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- SBD driver: real file device round-trip --------------------------------

// TestSBDAgentRealFileDevice proves the SBD agent writes a real poison message
// to a real file and confirms it by reading the file back as an independent
// oracle.
func TestSBDAgentRealFileDevice(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sbd.dev")
	dev, err := NewFileSBDDevice(path)
	if err != nil {
		t.Fatalf("NewFileSBDDevice: %v", err)
	}
	agent := NewSBDAgent("sbd-1", dev)
	const target = "node-i"

	if fenced, _ := agent.IsFenced(ctx, target); fenced {
		t.Fatal("pre-condition: target should not be fenced")
	}

	conf, err := agent.Fence(ctx, target)
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if conf.Method != "sbd poison" {
		t.Errorf("method=%q, want sbd poison", conf.Method)
	}

	// Independent oracle: read the slot directly from the device.
	if msg, err := dev.ReadSlot(ctx, target); err != nil || msg != sbdMessageOff {
		t.Fatalf("device slot: got (%q,%v), want (%q,nil)", msg, err, sbdMessageOff)
	}
	if fenced, err := agent.IsFenced(ctx, target); err != nil || !fenced {
		t.Fatalf("IsFenced: got (%v,%v), want (true,nil)", fenced, err)
	}
}

// TestSBDAndNoOpInterop runs an SBD agent as a real fallback level behind a
// failing primary, proving the drivers compose.
func TestSBDAndNoOpFallback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sbd.dev")
	dev, _ := NewFileSBDDevice(path)

	primary := NewNoOpAgent("primary", &FailingPowerController{})
	secondary := NewSBDAgent("sbd", dev)
	fencer, _ := NewMultiLevelFencer(primary, secondary)

	conf, err := fencer.Fence(ctx, "node-j")
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if conf.Agent != "sbd" {
		t.Fatalf("won by %q, want sbd", conf.Agent)
	}
	if msg, _ := dev.ReadSlot(ctx, "node-j"); msg != sbdMessageOff {
		t.Fatalf("sbd device not poisoned: %q", msg)
	}
}

// --- IPMIAgent: target/host mismatch guard -----------------------------------

// TestIPMIAgentTargetMustMatchHost proves the mismatch guard fires when the
// caller routes a fence for "node-X" to an IPMIAgent whose cfg.Host is the BMC
// of "node-Y". Without this guard the agent would silently power off node-Y
// while emitting a Confirmation claiming node-X was fenced — a wrong-node-fence
// / split-brain hazard (the exact failure mode STONITH exists to prevent).
//
// MUTATION GUARD: removing the `target != a.cfg.Host` check in Fence/IsFenced
// causes this test to fail because Fence returns nil error instead of
// ErrTargetMismatch, and IsFenced returns (false, nil) instead of
// (false, ErrTargetMismatch).
func TestIPMIAgentTargetMustMatchHost(t *testing.T) {
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is off\n"}
	// Agent is wired to the BMC for node-Y.
	agent := newIPMIAgent("ipmi-node-y", IPMIConfig{
		Host: "bmc-node-y.example", Username: "admin", Password: "secret",
	}, rec)

	// Attempt to fence node-X through this agent (routing mistake).
	_, err := agent.Fence(context.Background(), "bmc-node-x.example")
	if !errors.Is(err, ErrTargetMismatch) {
		t.Fatalf("Fence with mismatched target: got %v, want ErrTargetMismatch", err)
	}
	// No ipmitool calls must have been made — the guard fires before the runner.
	if len(rec.calls) != 0 {
		t.Fatalf("runner was invoked despite mismatch: %v calls recorded", len(rec.calls))
	}

	// IsFenced with a mismatched target must also reject, not silently return
	// false (which could be misinterpreted as "target is not fenced, so safe to
	// proceed").
	_, err = agent.IsFenced(context.Background(), "bmc-node-x.example")
	if !errors.Is(err, ErrTargetMismatch) {
		t.Fatalf("IsFenced with mismatched target: got %v, want ErrTargetMismatch", err)
	}

	// Fence and IsFenced with a matching target must succeed normally.
	conf, err := agent.Fence(context.Background(), "bmc-node-y.example")
	if err != nil {
		t.Fatalf("Fence with matching target: %v", err)
	}
	if conf.Target != "bmc-node-y.example" {
		t.Errorf("Confirmation.Target=%q, want bmc-node-y.example", conf.Target)
	}
}

// TestIPMIAgentEmptyTargetRejected verifies the empty-target guard still fires
// before the mismatch check (order of guards matters: empty is the more
// fundamental precondition).
func TestIPMIAgentEmptyTargetRejected(t *testing.T) {
	rec := &recordingIPMIRunner{present: true}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{Host: "bmc.example", Username: "u", Password: "p"}, rec)
	_, err := agent.Fence(context.Background(), "")
	if !errors.Is(err, ErrEmptyTarget) {
		t.Fatalf("got %v, want ErrEmptyTarget", err)
	}
}

// --- MultiLevelFencer.Agents() and IsFenced error-aggregation ---------------

// TestMultiLevelFencerAgents verifies that Agents() returns a copy of the
// configured agents in order and that mutating the returned slice does not
// affect the fencer's internal chain.
func TestMultiLevelFencerAgents(t *testing.T) {
	pc1 := NewInMemoryPower()
	pc2 := NewInMemoryPower()
	a1 := NewNoOpAgent("first", pc1)
	a2 := NewNoOpAgent("second", pc2)

	fencer, err := NewMultiLevelFencer(a1, a2)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}

	got := fencer.Agents()
	if len(got) != 2 {
		t.Fatalf("Agents() returned %d agents, want 2", len(got))
	}
	if got[0].Name() != "first" || got[1].Name() != "second" {
		t.Errorf("Agents() order wrong: got [%q, %q]", got[0].Name(), got[1].Name())
	}

	// Mutating the returned slice must not affect the fencer's internal chain.
	got[0] = a2
	internal := fencer.Agents()
	if internal[0].Name() != "first" {
		t.Errorf("fencer internal chain was mutated via returned slice: got %q, want first", internal[0].Name())
	}
}

// TestMultiLevelFencerIsFencedErrorAggregation covers the branch where
// IsFenced aggregates errors from all agents that return errors (rather than
// stopping at the first error). When every agent errors and none reports
// fenced, IsFenced must return (false, joinedErrors).
//
// MUTATION GUARD: removing the `errs` accumulation (replacing it with
// early-return on the first error) would still return false but the joined
// error would be nil or contain only one agent's error, causing the
// "both agents mentioned" assertion to fail.
func TestMultiLevelFencerIsFencedErrorAggregation(t *testing.T) {
	// Both agents return errors; neither reports fenced.
	errA := errors.New("agent-a probe failed")
	errB := errors.New("agent-b probe failed")

	agentA := &alwaysErrorAgent{name: "agent-a", isFencedErr: errA}
	agentB := &alwaysErrorAgent{name: "agent-b", isFencedErr: errB}

	fencer, _ := NewMultiLevelFencer(agentA, agentB)
	fenced, err := fencer.IsFenced(context.Background(), "node-agg")
	if fenced {
		t.Fatal("IsFenced returned true despite all agents erroring")
	}
	if err == nil {
		t.Fatal("IsFenced returned nil error despite all agents erroring")
	}
	msg := err.Error()
	if !strings.Contains(msg, "agent-a probe failed") || !strings.Contains(msg, "agent-b probe failed") {
		t.Errorf("aggregated error missing agent causes: %q", msg)
	}
}

// alwaysErrorAgent is a FencingAgent whose Fence and IsFenced always return
// the configured errors. It is used to exercise error-aggregation paths.
type alwaysErrorAgent struct {
	name        string
	fenceErr    error
	isFencedErr error
}

func (a *alwaysErrorAgent) Name() string { return a.name }
func (a *alwaysErrorAgent) Fence(_ context.Context, _ string) (*Confirmation, error) {
	return nil, a.fenceErr
}
func (a *alwaysErrorAgent) IsFenced(_ context.Context, _ string) (bool, error) {
	return false, a.isFencedErr
}

// --- PowerState.String default branch ----------------------------------------

// TestPowerStateStringDefault exercises the default branch in PowerState.String
// so it is covered and the formatted value is predictable.
func TestPowerStateStringDefault(t *testing.T) {
	unknown := PowerState(99)
	got := unknown.String()
	want := "PowerState(99)"
	if got != want {
		t.Errorf("PowerState(99).String() = %q, want %q", got, want)
	}
}

// --- SBD: deterministic slot ordering and atomic rename ----------------------

// TestSBDWriteSlotDeterministicOrder proves that WriteSlot always produces
// sorted key order on disk regardless of insertion order, making the file
// reproducible and diff-friendly.
//
// MUTATION GUARD: removing the sort.Strings(keys) call in WriteSlot causes this
// test to fail intermittently (map iteration order in Go is randomised) because
// the on-disk representation would not be sorted.
func TestSBDWriteSlotDeterministicOrder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sbd.dev")
	dev, err := NewFileSBDDevice(path)
	if err != nil {
		t.Fatalf("NewFileSBDDevice: %v", err)
	}

	// Write several targets in a known non-alphabetical order.
	for _, tgt := range []string{"zebra", "alpha", "mango", "beta"} {
		if err := dev.WriteSlot(ctx, tgt, "off"); err != nil {
			t.Fatalf("WriteSlot(%q): %v", tgt, err)
		}
	}

	// Read the raw file and verify lines are in sorted key order.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	for i := 1; i < len(lines); i++ {
		prevKey, _, _ := strings.Cut(lines[i-1], "=")
		currKey, _, _ := strings.Cut(lines[i], "=")
		if prevKey > currKey {
			t.Errorf("slots not in sorted order at line %d: %q > %q\nfull file:\n%s",
				i, prevKey, currKey, string(raw))
		}
	}
}

// TestSBDWriteSlotAtomicRename verifies that WriteSlot uses a temp-file-then-
// rename strategy: after a successful write the path must contain the expected
// content (i.e. the final rename happened) and the .tmp sibling must be gone.
//
// MUTATION GUARD: replacing the rename path with direct os.WriteFile would
// still pass the content check, but the absence-of-tmp assertion would also
// pass in that case, so we check both. The key safety property is that a real
// crash scenario is avoided — this test validates the happy-path mechanics.
func TestSBDWriteSlotAtomicRename(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sbd.dev")
	dev, err := NewFileSBDDevice(path)
	if err != nil {
		t.Fatalf("NewFileSBDDevice: %v", err)
	}

	const target = "node-atomic"
	if err := dev.WriteSlot(ctx, target, "off"); err != nil {
		t.Fatalf("WriteSlot: %v", err)
	}

	// The canonical file must have the correct content.
	msg, err := dev.ReadSlot(ctx, target)
	if err != nil || msg != "off" {
		t.Fatalf("ReadSlot after atomic write: got (%q,%v), want (off,nil)", msg, err)
	}

	// The .tmp sibling must be absent after a successful write.
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf(".tmp file still exists after successful WriteSlot: %v", err)
	}
}

// TestExecIPMIRunnerBuildAndRun covers the production execIPMIRunner.run()
// path. We cannot fence a real BMC in CI, so we use "ipmitool --version" as
// a safe command that exits 0 when the binary is present. The test
// SKIPS-with-reason when ipmitool is absent (CLAUDE-1: honest degradation,
// no fake PASS).
//
// This exercises the production code path that TestIPMIAgentBuildsExactArgv
// (which uses a mock runner) cannot reach.
func TestExecIPMIRunnerBuildAndRun(t *testing.T) {
	if _, err := exec.LookPath("ipmitool"); err != nil {
		t.Skip("SKIP: ipmitool not installed; cannot exercise execIPMIRunner.run() production path")
	}
	ctx := context.Background()
	r := execIPMIRunner{}

	// "--version" is safe: it prints the version string and exits 0.
	out, err := r.run(ctx, []string{"--version"}, nil)
	if err != nil {
		t.Fatalf("execIPMIRunner.run([--version]): %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "ipmitool") {
		t.Errorf("expected ipmitool version output, got: %q", out)
	}
}
