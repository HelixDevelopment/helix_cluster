//go:build integration

package instance_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/device"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/instance"
)

// integrationRunID is a per-run UUID embedded in all sink-side evidence.
// In production the orchestrator injects a real UUID via env; for the
// integration tier we generate a deterministic one from the test binary's
// random source (see generateRunID below).
var integrationRunID string

func TestMain(m *testing.M) {
	integrationRunID = fmt.Sprintf("integ-HXC1231-%d", time.Now().UnixNano())
	m.Run()
}

// TestInteg_ProvisionT3_FullLifecycle_WithRealVirtualClock exercises the
// complete provisioned→booting→ready→stopped lifecycle against a real
// VirtualClock, asserts every sink-side fact demanded by HXC-1231 closure
// criteria, and embeds the per-run UUID in the evidence log.
func TestInteg_ProvisionT3_FullLifecycle_WithRealVirtualClock(t *testing.T) {
	reg := device.NewRegistry()
	profile := reg.Get("T3")
	if profile == nil {
		t.Fatal("T3 profile not found in default registry — integration test requires it")
	}

	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)
	fp.SetRunID(integrationRunID)

	// ── 1. Provision ─────────────────────────────────────────────────────────
	inst, err := fp.Provision(profile)
	if err != nil {
		t.Fatalf("[%s] Provision(T3): %v", integrationRunID, err)
	}

	// ── 2. Immediately after Provision: must be booting ──────────────────────
	if got := inst.Status(); got != instance.StateBooting {
		t.Fatalf("[%s] immediately after Provision: want booting, got %s", integrationRunID, got)
	}

	bootTime := fp.BootTimeFor("T3")
	t.Logf("[%s] T3 boot-time: %v", integrationRunID, bootTime)

	// ── 3. One ns before bootTime: still booting ──────────────────────────────
	clk.Advance(bootTime - time.Nanosecond)
	if got := inst.Status(); got != instance.StateBooting {
		t.Errorf("[%s] at bootTime-1ns: want booting, got %s", integrationRunID, got)
	}

	// ── 4. Advance past bootTime: now ready ───────────────────────────────────
	clk.Advance(2 * time.Nanosecond)
	if got := inst.Status(); got != instance.StateReady {
		t.Fatalf("[%s] at bootTime+1ns: want ready, got %s", integrationRunID, got)
	}

	// ── 5. Ready timestamp == boot start + bootTime ──────────────────────────
	// boot started at virtual 0 (clock was 0 when Provision was called).
	wantReadyAt := time.Unix(0, 0).Add(bootTime)
	if !inst.ReadyAt().Equal(wantReadyAt) {
		t.Errorf("[%s] readyAt: want %v, got %v", integrationRunID, wantReadyAt, inst.ReadyAt())
	}

	// ── 6. Ordered transition trace: provisioned→booting→ready ───────────────
	wantTrace := []instance.State{
		instance.StateProvisioned,
		instance.StateBooting,
		instance.StateReady,
	}
	trace := inst.Trace()
	if len(trace) != len(wantTrace) {
		t.Fatalf("[%s] trace len: want %d, got %d %v", integrationRunID, len(wantTrace), len(trace), trace)
	}
	for i, w := range wantTrace {
		if trace[i] != w {
			t.Errorf("[%s] trace[%d]: want %s, got %s", integrationRunID, i, w, trace[i])
		}
	}

	// ── 7. Stop → stopped ────────────────────────────────────────────────────
	if err := inst.Stop(); err != nil {
		t.Fatalf("[%s] Stop(): %v", integrationRunID, err)
	}
	if got := inst.Status(); got != instance.StateStopped {
		t.Errorf("[%s] after Stop: want stopped, got %s", integrationRunID, got)
	}

	// ── 8. Boot from stopped → rejected, typed error ─────────────────────────
	bootErr := inst.Boot()
	if bootErr == nil {
		t.Fatalf("[%s] Boot() from stopped: want ErrIllegalTransition, got nil", integrationRunID)
	}
	if !errors.Is(bootErr, instance.ErrIllegalTransition) {
		t.Errorf("[%s] Boot() from stopped: want errors.Is(ErrIllegalTransition), got %v", integrationRunID, bootErr)
	}
	if got := inst.Status(); got != instance.StateStopped {
		t.Errorf("[%s] after rejected Boot: want stopped, got %s", integrationRunID, got)
	}

	// ── 9. Rejection captured by FakeProvisioner ─────────────────────────────
	rejected := fp.RejectedTransitions()
	if len(rejected) == 0 {
		t.Fatalf("[%s] RejectedTransitions: want >=1, got 0", integrationRunID)
	}
	last := rejected[len(rejected)-1]
	if last.From != instance.StateStopped || last.To != instance.StateBooting {
		t.Errorf("[%s] last rejection: want stopped->booting, got %s->%s", integrationRunID, last.From, last.To)
	}
	if last.RunID != integrationRunID {
		t.Errorf("[%s] last rejection RunID: want %q, got %q", integrationRunID, integrationRunID, last.RunID)
	}

	t.Logf("[%s] SINK-SIDE EVIDENCE: trace=%v readyAt=%v rejections=%+v",
		integrationRunID, trace, inst.ReadyAt(), rejected)
}

// TestInteg_MultipleProvisions_IndependentBootTimes ensures two instances
// from the same provisioner on the same clock have independent boot-time
// gates — one can be ready while the other is still booting.
func TestInteg_MultipleProvisions_IndependentBootTimes(t *testing.T) {
	reg := device.NewRegistry()
	p3 := reg.Get("T3")
	p1 := reg.Get("T1")
	if p3 == nil || p1 == nil {
		t.Skip("T3 or T1 profile missing")
	}

	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)
	fp.SetRunID(integrationRunID)

	inst3, err := fp.Provision(p3)
	if err != nil {
		t.Fatalf("[%s] Provision(T3): %v", integrationRunID, err)
	}
	inst1, err := fp.Provision(p1)
	if err != nil {
		t.Fatalf("[%s] Provision(T1): %v", integrationRunID, err)
	}

	// Both start booting.
	if inst3.Status() != instance.StateBooting {
		t.Errorf("[%s] T3 should start booting", integrationRunID)
	}
	if inst1.Status() != instance.StateBooting {
		t.Errorf("[%s] T1 should start booting", integrationRunID)
	}

	// Advance past T1's boot time (50ms) but not T3's (100ms).
	bt1 := fp.BootTimeFor("T1")
	bt3 := fp.BootTimeFor("T3")
	clk.Advance(bt1 + time.Nanosecond)

	if inst1.Status() != instance.StateReady {
		t.Errorf("[%s] T1 should be ready after %v, got %s", integrationRunID, bt1, inst1.Status())
	}
	if inst3.Status() != instance.StateBooting {
		t.Errorf("[%s] T3 should still be booting (only %v elapsed, needs %v), got %s",
			integrationRunID, bt1+time.Nanosecond, bt3, inst3.Status())
	}

	// Now advance past T3's boot time.
	clk.Advance(bt3 - bt1)
	if inst3.Status() != instance.StateReady {
		t.Errorf("[%s] T3 should be ready after %v total, got %s", integrationRunID, bt3+time.Nanosecond, inst3.Status())
	}

	t.Logf("[%s] T1 readyAt=%v T3 readyAt=%v", integrationRunID, inst1.ReadyAt(), inst3.ReadyAt())
}
