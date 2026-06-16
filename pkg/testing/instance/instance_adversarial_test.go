package instance_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/device"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/instance"
)

// withTimeout fails the test if fn does not return within d. It guards every
// adversarial probe so a deadlock/livelock surfaces as a FAIL, never a hang.
func withTimeout(t *testing.T, d time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("adversarial probe %q exceeded timeout %v (possible deadlock)", name, d)
	}
}

func mustProvisionT3(t *testing.T) (*instance.VirtualClock, *instance.FakeProvisioner, *instance.Instance) {
	t.Helper()
	reg := device.NewRegistry()
	profile := reg.Get("T3")
	if profile == nil {
		t.Fatal("T3 profile missing from default registry")
	}
	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)
	inst, err := fp.Provision(profile)
	if err != nil {
		t.Fatalf("Provision(T3): %v", err)
	}
	return clk, fp, inst
}

// ─── (A) LIFECYCLE STATE CORRECTNESS ────────────────────────────────────────

// TestAdv_DoubleStop_RejectedAndIdempotent verifies a terminated (stopped)
// instance cannot be stopped twice: the second Stop must be rejected with a
// typed error, the sink-side state must remain stopped, and the trace must NOT
// grow a duplicate stopped entry.
func TestAdv_DoubleStop_RejectedAndIdempotent(t *testing.T) {
	clk, fp, inst := mustProvisionT3(t)
	clk.Advance(fp.BootTimeFor("T3") + time.Nanosecond)
	if inst.Status() != instance.StateReady {
		t.Fatalf("precondition: want ready, got %s", inst.Status())
	}
	if err := inst.Stop(); err != nil {
		t.Fatalf("first Stop(): %v", err)
	}
	traceAfterFirstStop := inst.Trace()

	// Second Stop must be rejected.
	err := inst.Stop()
	if err == nil {
		t.Fatalf("double Stop(): want ErrIllegalTransition, got nil")
	}
	if !errors.Is(err, instance.ErrIllegalTransition) {
		t.Errorf("double Stop(): want errors.Is(ErrIllegalTransition), got %v", err)
	}
	if inst.Status() != instance.StateStopped {
		t.Errorf("after double Stop(): want stopped, got %s", inst.Status())
	}
	traceAfterSecondStop := inst.Trace()
	if len(traceAfterSecondStop) != len(traceAfterFirstStop) {
		t.Errorf("trace grew on rejected double Stop(): before=%v after=%v",
			traceAfterFirstStop, traceAfterSecondStop)
	}
}

// TestAdv_StopWhileBooting_RejectedNoLeak verifies that Stop() on an instance
// whose boot-time has NOT elapsed is rejected (no booting->stopped edge) and
// leaves state booting. This pins the documented FSM (no skip edges).
func TestAdv_StopWhileBooting_RejectedNoLeak(t *testing.T) {
	clk, _, inst := mustProvisionT3(t)
	// Clock not advanced — still booting.
	if inst.Status() != instance.StateBooting {
		t.Fatalf("precondition: want booting, got %s", inst.Status())
	}
	err := inst.Stop()
	if err == nil {
		t.Fatalf("Stop() while booting: want ErrIllegalTransition, got nil")
	}
	if !errors.Is(err, instance.ErrIllegalTransition) {
		t.Errorf("Stop() while booting: want errors.Is(ErrIllegalTransition), got %v", err)
	}
	if inst.Status() != instance.StateBooting {
		t.Errorf("after rejected Stop(): want booting, got %s", inst.Status())
	}
	_ = clk
}

// TestAdv_StatusIdempotent_NoDuplicateReadyInTrace verifies that calling
// Status() many times after boot-time elapses promotes to ready EXACTLY ONCE:
// the trace must contain a single ready entry, not one per Status() call.
func TestAdv_StatusIdempotent_NoDuplicateReadyInTrace(t *testing.T) {
	clk, fp, inst := mustProvisionT3(t)
	clk.Advance(fp.BootTimeFor("T3") + time.Nanosecond)

	for i := 0; i < 20; i++ {
		if got := inst.Status(); got != instance.StateReady {
			t.Fatalf("Status() iter %d: want ready, got %s", i, got)
		}
	}
	trace := inst.Trace()
	readyCount := 0
	for _, s := range trace {
		if s == instance.StateReady {
			readyCount++
		}
	}
	if readyCount != 1 {
		t.Errorf("ready appears %d times in trace %v; want exactly 1 (Status not idempotent)", readyCount, trace)
	}
}

// TestAdv_TraceIsExactCanonicalSequence asserts the full happy-path trace is
// exactly [provisioned, booting, ready, stopped] with no extra/missing nodes.
func TestAdv_TraceIsExactCanonicalSequence(t *testing.T) {
	clk, fp, inst := mustProvisionT3(t)
	clk.Advance(fp.BootTimeFor("T3") + time.Nanosecond)
	_ = inst.Status()
	if err := inst.Stop(); err != nil {
		t.Fatalf("Stop(): %v", err)
	}
	want := []instance.State{
		instance.StateProvisioned,
		instance.StateBooting,
		instance.StateReady,
		instance.StateStopped,
	}
	got := inst.Trace()
	if len(got) != len(want) {
		t.Fatalf("trace: want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trace[%d]: want %s, got %s (full=%v)", i, want[i], got[i], got)
		}
	}
}

// ─── (A2) REJECTION ACCOUNTING ──────────────────────────────────────────────

// TestAdv_RejectionRecordsCorrectFromState verifies the rejection sink records
// the ACTUAL current state as `From` (not a stale/wrong value). We reject a
// Boot() from stopped and a Boot() from ready, and assert each From is exact.
func TestAdv_RejectionRecordsCorrectFromState(t *testing.T) {
	clk, fp, inst := mustProvisionT3(t)
	clk.Advance(fp.BootTimeFor("T3") + time.Nanosecond)
	if inst.Status() != instance.StateReady {
		t.Fatalf("precondition: want ready, got %s", inst.Status())
	}

	// Boot() from ready -> illegal. From must be ready.
	if err := inst.Boot(); err == nil {
		t.Fatalf("Boot() from ready: want error, got nil")
	}
	rej := fp.RejectedTransitions()
	if len(rej) == 0 {
		t.Fatalf("want >=1 rejection after Boot() from ready")
	}
	last := rej[len(rej)-1]
	if last.From != instance.StateReady || last.To != instance.StateBooting {
		t.Errorf("rejection from ready: want ready->booting, got %s->%s", last.From, last.To)
	}

	// Now stop, then Boot() from stopped -> illegal. From must be stopped.
	if err := inst.Stop(); err != nil {
		t.Fatalf("Stop(): %v", err)
	}
	if err := inst.Boot(); err == nil {
		t.Fatalf("Boot() from stopped: want error, got nil")
	}
	rej = fp.RejectedTransitions()
	last = rej[len(rej)-1]
	if last.From != instance.StateStopped || last.To != instance.StateBooting {
		t.Errorf("rejection from stopped: want stopped->booting, got %s->%s", last.From, last.To)
	}
}

// ─── (B) CONCURRENCY ────────────────────────────────────────────────────────

// TestAdv_ConcurrentStatusStop_RaceFree drives concurrent Status()/Stop()/Boot()
// against one instance and asserts: no torn state (final state is a legal
// terminal/leaf), Stop() succeeds AT MOST once, and the trace contains at most
// one ready and at most one stopped. Run a single -race pass over this.
func TestAdv_ConcurrentStatusStop_RaceFree(t *testing.T) {
	withTimeout(t, 10*time.Second, "concurrent-status-stop", func() {
		clk, fp, inst := mustProvisionT3(t)
		clk.Advance(fp.BootTimeFor("T3") + time.Nanosecond)

		var wg sync.WaitGroup
		var stopOK int64
		var stopMu sync.Mutex

		const workers = 32
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			go func(k int) {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					switch (k + j) % 3 {
					case 0:
						_ = inst.Status()
					case 1:
						if err := inst.Stop(); err == nil {
							stopMu.Lock()
							stopOK++
							stopMu.Unlock()
						}
					case 2:
						_ = inst.Boot() // always illegal here
					}
				}
			}(i)
		}
		wg.Wait()

		if stopOK != 1 {
			t.Errorf("Stop() succeeded %d times under concurrency; want exactly 1", stopOK)
		}
		if got := inst.Status(); got != instance.StateStopped {
			t.Errorf("final state: want stopped, got %s", got)
		}
		trace := inst.Trace()
		ready, stopped := 0, 0
		for _, s := range trace {
			switch s {
			case instance.StateReady:
				ready++
			case instance.StateStopped:
				stopped++
			}
		}
		if ready > 1 {
			t.Errorf("ready appears %d times in trace under concurrency; want <=1: %v", ready, trace)
		}
		if stopped != 1 {
			t.Errorf("stopped appears %d times in trace; want exactly 1: %v", stopped, trace)
		}
	})
}

// TestAdv_ConcurrentProvision_AccountingReconciles verifies concurrent
// Provision() calls never panic, never error for a valid profile, and produce
// exactly n distinct, independently-booting instances (no lost/torn provisions).
// Independence is checked sink-side: advancing the clock past one instance's
// boot-time promotes ALL of them (they each gate on their own bootAt, recorded
// at provision time) — proving each got its own correctly-initialized state.
func TestAdv_ConcurrentProvision_AccountingReconciles(t *testing.T) {
	withTimeout(t, 10*time.Second, "concurrent-provision", func() {
		reg := device.NewRegistry()
		profile := reg.Get("T3")
		clk := instance.NewVirtualClock(0)
		fp := instance.NewFakeProvisioner(clk)

		const n = 200
		insts := make([]*instance.Instance, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func(idx int) {
				defer wg.Done()
				in, err := fp.Provision(profile)
				if err != nil {
					t.Errorf("Provision: %v", err)
					return
				}
				insts[idx] = in
			}(i)
		}
		wg.Wait()

		seen := make(map[*instance.Instance]bool, n)
		for i, in := range insts {
			if in == nil {
				t.Fatalf("instance %d nil (lost provision)", i)
			}
			if seen[in] {
				t.Errorf("duplicate instance pointer at %d (torn provision)", i)
			}
			seen[in] = true
			if in.Status() != instance.StateBooting {
				t.Errorf("instance %d: want booting right after provision, got %s", i, in.Status())
			}
		}
		if len(seen) != n {
			t.Errorf("distinct provisioned instances: want %d, got %d", n, len(seen))
		}

		// All were provisioned at virtual t=0 (clock never advanced during the
		// concurrent phase here is not guaranteed; but bootAt was captured per
		// instance). Advance well past T3 boot-time and assert ALL are ready.
		clk.Advance(fp.BootTimeFor("T3") + time.Second)
		for i, in := range insts {
			if got := in.Status(); got != instance.StateReady {
				t.Errorf("instance %d after full advance: want ready, got %s", i, got)
			}
		}
	})
}

// TestAdv_RejectedReBoot_DoesNotResetBootTimer verifies that an illegal Boot()
// while already booting does NOT reset bootAt (which would extend/restart the
// boot timer and delay readiness). The instance must still become ready at the
// ORIGINAL bootAt+bootTime, regardless of intervening rejected Boot() calls.
func TestAdv_RejectedReBoot_DoesNotResetBootTimer(t *testing.T) {
	clk, fp, inst := mustProvisionT3(t)
	bt := fp.BootTimeFor("T3")

	// Advance most of the way through boot, then issue an illegal re-Boot.
	clk.Advance(bt - 10*time.Nanosecond)
	if inst.Status() != instance.StateBooting {
		t.Fatalf("precondition: want booting, got %s", inst.Status())
	}
	if err := inst.Boot(); err == nil {
		t.Fatalf("re-Boot() while booting: want ErrIllegalTransition, got nil")
	}

	// Advance the remaining 10ns past the ORIGINAL bootTime. If bootAt had been
	// reset by the rejected Boot(), the instance would still be booting here.
	clk.Advance(10 * time.Nanosecond)
	if got := inst.Status(); got != instance.StateReady {
		t.Errorf("after original bootTime elapsed: want ready, got %s "+
			"(rejected re-Boot may have reset boot timer)", got)
	}
}

// ─── (C) EDGE INPUTS / BOUNDARY ─────────────────────────────────────────────

// TestAdv_UnknownTier_UsesDefaultBootTime_NoPanic verifies an unknown tier
// does not panic and boots on the documented default boot-time.
func TestAdv_UnknownTier_UsesDefaultBootTime_NoPanic(t *testing.T) {
	clk := instance.NewVirtualClock(0)
	fp := instance.NewFakeProvisioner(clk)
	p := &device.Profile{ID: "weird", Tier: "T999"}

	var inst *instance.Instance
	withTimeout(t, 5*time.Second, "unknown-tier", func() {
		var err error
		inst, err = fp.Provision(p)
		if err != nil {
			t.Fatalf("Provision(unknown tier): %v", err)
		}
	})
	if inst.Status() != instance.StateBooting {
		t.Fatalf("want booting, got %s", inst.Status())
	}
	bt := fp.BootTimeFor("T999")
	// One ns before default boot-time: still booting.
	clk.Advance(bt - time.Nanosecond)
	if inst.Status() != instance.StateBooting {
		t.Errorf("at default-bootTime-1ns: want booting, got %s", inst.Status())
	}
	clk.Advance(2 * time.Nanosecond)
	if inst.Status() != instance.StateReady {
		t.Errorf("at default-bootTime+1ns: want ready, got %s", inst.Status())
	}
}

// TestAdv_BootTimeBoundary_ExactTick verifies the gate is "ready at exactly
// bootAt+bootTime" (not strictly after): at exactly bootTime the instance is
// ready, consistent with the !now.Before(...) predicate.
func TestAdv_BootTimeBoundary_ExactTick(t *testing.T) {
	clk, fp, inst := mustProvisionT3(t)
	bt := fp.BootTimeFor("T3")
	// Advance to EXACTLY bootTime.
	clk.Advance(bt)
	if got := inst.Status(); got != instance.StateReady {
		t.Errorf("at exactly bootTime: want ready, got %s", got)
	}
}
