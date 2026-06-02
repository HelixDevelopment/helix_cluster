package stonith

import (
	"context"
	"errors"
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

// recordingIPMIRunner captures argv and returns scripted output, so we can
// assert the EXACT command without an IPMI BMC.
type recordingIPMIRunner struct {
	present   bool
	calls     [][]string
	statusOut string
	runErr    error
}

func (r *recordingIPMIRunner) lookPath() (string, error) {
	if !r.present {
		return "", ErrIPMIToolAbsent
	}
	return "/usr/bin/ipmitool", nil
}

func (r *recordingIPMIRunner) run(_ context.Context, args []string) (string, error) {
	r.calls = append(r.calls, args)
	if r.runErr != nil {
		return "", r.runErr
	}
	// Return status output only for a status query.
	if len(args) > 0 && args[len(args)-1] == "status" {
		return r.statusOut, nil
	}
	return "", nil
}

func TestIPMIAgentBuildsExactArgv(t *testing.T) {
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is off\n"}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{
		Host: "10.0.0.5", Username: "admin", Password: "secret",
	}, rec)

	conf, err := agent.Fence(context.Background(), "node-f")
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
		"-I", "lanplus", "-H", "10.0.0.5", "-U", "admin", "-P", "secret",
		"chassis", "power", "off",
	}
	if got := rec.calls[0]; !equalArgs(got, wantOff) {
		t.Errorf("power off argv:\n got  %v\n want %v", got, wantOff)
	}
	wantStatus := []string{
		"-I", "lanplus", "-H", "10.0.0.5", "-U", "admin", "-P", "secret",
		"chassis", "power", "status",
	}
	if got := rec.calls[1]; !equalArgs(got, wantStatus) {
		t.Errorf("status argv:\n got  %v\n want %v", got, wantStatus)
	}
}

// TestIPMIAgentNotConfirmedWhenStillOn proves that if the BMC reports power
// still on, Fence returns ErrNotConfirmed (no fake success).
func TestIPMIAgentNotConfirmedWhenStillOn(t *testing.T) {
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is on\n"}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{Host: "h", Username: "u", Password: "p"}, rec)
	_, err := agent.Fence(context.Background(), "node-g")
	if !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("got %v, want ErrNotConfirmed", err)
	}
}

// TestIPMIAgentAbsentBinary proves we surface a typed error, never a fake fence,
// when ipmitool is missing.
func TestIPMIAgentAbsentBinary(t *testing.T) {
	rec := &recordingIPMIRunner{present: false}
	agent := newIPMIAgent("ipmi-1", IPMIConfig{Host: "h", Username: "u", Password: "p"}, rec)
	conf, err := agent.Fence(context.Background(), "node-h")
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
