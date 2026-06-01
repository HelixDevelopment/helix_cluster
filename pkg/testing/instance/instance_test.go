package instance_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/device"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/instance"
)

// runID is a per-test UUID-style tag (deterministic: seed + test name hash).
// We use a simple fixed string per run; the integration test embeds a real UUID.
const runID = "unit-run-HXC1231-A4F7C2B1"

// TestProvisionT3_TransitionTrace verifies that provisioning a T3 profile
// yields the ordered trace [provisioned, booting, ready] and that the
// instance is ready only AFTER boot-time has elapsed on the virtual clock.
func TestProvisionT3_TransitionTrace(t *testing.T) {
	reg := device.NewRegistry()
	profile := reg.Get("T3")
	if profile == nil {
		t.Fatal("T3 profile missing from default registry")
	}

	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)

	inst, err := fp.Provision(profile)
	if err != nil {
		t.Fatalf("Provision(T3) failed: %v", err)
	}

	// ── 1. Immediately after Provision the instance is in 'booting' ──────────
	// Boot() is called internally by Provision; the clock has not yet advanced
	// past bootTime so Status must be Booting (not Ready).
	gotBeforeTick := inst.Status()
	if gotBeforeTick != instance.StateBooting {
		t.Errorf("[%s] before clock advance: want StatusBooting, got %s", runID, gotBeforeTick)
	}

	// ── 2. Record the clock time at which boot started ───────────────────────
	bootStart := clk.Now()

	// ── 3. Advance virtual clock past boot-time → instance becomes ready ─────
	bootTime := fp.BootTimeFor("T3")
	clk.Advance(bootTime + time.Nanosecond)

	gotAfterTick := inst.Status()
	if gotAfterTick != instance.StateReady {
		t.Errorf("[%s] after clock advance: want StateReady, got %s", runID, gotAfterTick)
	}

	// ── 4. Ready timestamp == bootStart + bootTime (within 1 ns) ─────────────
	readyAt := inst.ReadyAt()
	wantReadyAt := bootStart.Add(bootTime)
	if !readyAt.Equal(wantReadyAt) {
		t.Errorf("[%s] readyAt: want %v, got %v", runID, wantReadyAt, readyAt)
	}

	// ── 5. Ordered transition trace: provisioned → booting → ready ───────────
	trace := inst.Trace()
	wantTrace := []instance.State{
		instance.StateProvisioned,
		instance.StateBooting,
		instance.StateReady,
	}
	if len(trace) != len(wantTrace) {
		t.Fatalf("[%s] transition trace len: want %d, got %d (%v)", runID, len(wantTrace), len(trace), trace)
	}
	for i, want := range wantTrace {
		if trace[i] != want {
			t.Errorf("[%s] trace[%d]: want %s, got %s", runID, i, want, trace[i])
		}
	}

	t.Logf("[%s] trace: %v  readyAt: %v  bootTime: %v", runID, trace, readyAt, bootTime)
}

// TestBootFromStopped_RejectedWithTypedError verifies that calling Boot() on
// a stopped instance returns a typed ErrIllegalTransition and leaves state stopped.
func TestBootFromStopped_RejectedWithTypedError(t *testing.T) {
	reg := device.NewRegistry()
	profile := reg.Get("T3")

	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)

	inst, err := fp.Provision(profile)
	if err != nil {
		t.Fatalf("Provision(T3): %v", err)
	}

	// Advance clock so the instance becomes ready.
	clk.Advance(fp.BootTimeFor("T3") + time.Nanosecond)
	if inst.Status() != instance.StateReady {
		t.Fatalf("precondition: instance not ready after clock advance")
	}

	// Stop the instance → transitions to stopped.
	if err := inst.Stop(); err != nil {
		t.Fatalf("Stop(): unexpected error: %v", err)
	}
	if inst.Status() != instance.StateStopped {
		t.Fatalf("after Stop(): want stopped, got %s", inst.Status())
	}

	// ── Attempt Boot() from stopped — MUST be rejected ───────────────────────
	bootErr := inst.Boot()
	if bootErr == nil {
		t.Fatalf("[%s] Boot() from stopped: expected ErrIllegalTransition, got nil", runID)
	}
	if !errors.Is(bootErr, instance.ErrIllegalTransition) {
		t.Errorf("[%s] Boot() from stopped: want errors.Is(ErrIllegalTransition), got %v", runID, bootErr)
	}
	// State must remain stopped (not changed by the rejected transition).
	if inst.Status() != instance.StateStopped {
		t.Errorf("[%s] after rejected Boot(): want stopped, got %s", runID, inst.Status())
	}

	t.Logf("[%s] rejected transition error: %v", runID, bootErr)
}

// TestTransitionTrace_CapturesRejectedIllegalTransition verifies that the
// trace does NOT include the rejected stopped→booting transition and that the
// Provisioner itself records the rejection as an observable event.
func TestTransitionTrace_CapturesRejectedIllegalTransition(t *testing.T) {
	reg := device.NewRegistry()
	profile := reg.Get("T3")

	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)

	inst, err := fp.Provision(profile)
	if err != nil {
		t.Fatalf("Provision(T3): %v", err)
	}
	clk.Advance(fp.BootTimeFor("T3") + time.Nanosecond)
	_ = inst.Stop()

	traceBeforeReject := len(inst.Trace())

	// Attempt the illegal transition.
	_ = inst.Boot()

	traceAfterReject := inst.Trace()
	if len(traceAfterReject) != traceBeforeReject {
		t.Errorf("[%s] trace length changed after rejected transition: before=%d after=%d",
			runID, traceBeforeReject, len(traceAfterReject))
	}

	// The rejection events are captured separately in RejectedTransitions().
	rejected := fp.RejectedTransitions()
	if len(rejected) == 0 {
		t.Fatalf("[%s] RejectedTransitions: want at least 1 rejection, got none", runID)
	}
	last := rejected[len(rejected)-1]
	if last.From != instance.StateStopped || last.To != instance.StateBooting {
		t.Errorf("[%s] last rejection: want stopped->booting, got %s->%s", runID, last.From, last.To)
	}
	// RunID is embedded.
	if last.RunID == "" {
		t.Errorf("[%s] last rejection: RunID must be non-empty", runID)
	}
	t.Logf("[%s] rejection log: %+v", runID, rejected)
}

// TestStatus_BootingBeforeReady is an explicit parametric check of the
// virtual-clock gate: advances time to exactly bootTime-1ns (still booting),
// then to bootTime+1ns (now ready).
func TestStatus_BootingBeforeReady(t *testing.T) {
	reg := device.NewRegistry()
	profile := reg.Get("T3")

	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)

	inst, err := fp.Provision(profile)
	if err != nil {
		t.Fatalf("Provision(T3): %v", err)
	}
	bootTime := fp.BootTimeFor("T3")

	// One nanosecond before bootTime: still booting.
	clk.Advance(bootTime - time.Nanosecond)
	if inst.Status() != instance.StateBooting {
		t.Errorf("at bootTime-1ns: want booting, got %s", inst.Status())
	}

	// One nanosecond past bootTime: now ready.
	clk.Advance(2 * time.Nanosecond)
	if inst.Status() != instance.StateReady {
		t.Errorf("at bootTime+1ns: want ready, got %s", inst.Status())
	}
}

// TestProvisionNilProfile returns a clear error — not a panic.
func TestProvisionNilProfile(t *testing.T) {
	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)
	_, err := fp.Provision(nil)
	if err == nil {
		t.Fatal("Provision(nil): want error, got nil")
	}
}

// TestReadyAtTimestamp_ExactBootTime checks the exact arithmetic:
// ready timestamp = virtual-clock epoch + bootTime.
func TestReadyAtTimestamp_ExactBootTime(t *testing.T) {
	reg := device.NewRegistry()
	profile := reg.Get("T3")

	// Start virtual clock at an arbitrary epoch (1000 ns).
	clk := instance.NewVirtualClock(1_000)
	fp := instance.NewFakeProvisioner(clk)
	bootTime := fp.BootTimeFor("T3")

	inst, err := fp.Provision(profile)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Epoch recorded at provisioning moment.
	provisionedAt := clk.Now()

	// Advance exactly bootTime → should flip to ready at provisioned+bootTime.
	clk.Advance(bootTime)
	if inst.Status() != instance.StateReady {
		t.Fatalf("want ready, got %s", inst.Status())
	}
	want := provisionedAt.Add(bootTime)
	if !inst.ReadyAt().Equal(want) {
		t.Errorf("readyAt: want %v, got %v (diff=%v)", want, inst.ReadyAt(), inst.ReadyAt().Sub(want))
	}
	fmt.Printf("[%s] exact-boot-time check: provisionedAt=%v bootTime=%v readyAt=%v\n",
		runID, provisionedAt.UnixNano(), bootTime, inst.ReadyAt().UnixNano())
}
