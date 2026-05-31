package cloudspot

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder captures, in call order and under a lock, which drain hooks fired.
// It is the sink-side evidence: we assert on the recorded order, not on "no
// error" / "no panic".
type recorder struct {
	mu    sync.Mutex
	order []DrainStep
}

func (r *recorder) record(s DrainStep) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, s)
}

func (r *recorder) snapshot() []DrainStep {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]DrainStep, len(r.order))
	copy(out, r.order)
	return out
}

// recordingHooks builds Hooks whose each step appends to the recorder, plus a
// ForceStop that flips a flag.
func recordingHooks(r *recorder, forced *int32) Hooks {
	return Hooks{
		StopAdmission: func(context.Context) error { r.record(StepStopAdmission); return nil },
		Checkpoint:    func(context.Context) error { r.record(StepCheckpoint); return nil },
		Deregister:    func(context.Context) error { r.record(StepDeregister); return nil },
		ForceStop:     func() { atomic.StoreInt32(forced, 1) },
	}
}

// TestDrain_HooksInvokedInOrder_BeforeDeadline is the core real-behaviour test.
//
// Mutation that this kills: in Handler.drain, change the `steps` slice order
// (e.g. put Checkpoint before StopAdmission), or make drain ignore the notice
// and never call the hooks. Either makes the recorded order differ from
// {StopAdmission, Checkpoint, Deregister} and the reflect.DeepEqual assertion
// FAILS. (Specifically: "a handler that ignores the notice (never drains)"
// yields an empty order and fails here.)
func TestDrain_HooksInvokedInOrder_BeforeDeadline(t *testing.T) {
	src := NewFakeSource()
	var rec recorder
	var forced int32
	h := NewHandler(src, recordingHooks(&rec, &forced))

	if !h.Admit() {
		t.Fatal("Admit must be true before any preemption notice")
	}

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()

	// Comfortable deadline: the trivial hooks finish well within it.
	src.Emit(time.Now().Add(2 * time.Second))

	var res Result
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not complete after notice")
	}

	if res.Outcome != OutcomeDrained {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeDrained)
	}
	want := []DrainStep{StepStopAdmission, StepCheckpoint, StepDeregister}
	if got := rec.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("hook order = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(res.Steps, want) {
		t.Fatalf("Result.Steps = %v, want %v", res.Steps, want)
	}
	if atomic.LoadInt32(&forced) != 0 {
		t.Fatal("ForceStop must NOT run when drain finishes before the deadline")
	}
}

// TestDrain_ClosesAdmissionAfterNotice proves the admission gate flips closed
// once a notice begins draining, so no new work is accepted.
//
// Mutation that this kills: remove `h.draining = true` in drain (or make Admit
// always return true). Then Admit() stays true after the notice and the
// assertion below FAILS.
func TestDrain_ClosesAdmissionAfterNotice(t *testing.T) {
	src := NewFakeSource()
	var rec recorder
	var forced int32

	// Block on the FIRST hook until we have observed Admit()==false, proving the
	// gate closed BEFORE/at drain start rather than only after completion.
	gateChecked := make(chan struct{})
	hooks := recordingHooks(&rec, &forced)
	inner := hooks.StopAdmission
	hooks.StopAdmission = func(ctx context.Context) error {
		<-gateChecked // wait until the test confirms admission is closed
		return inner(ctx)
	}

	h := NewHandler(src, hooks)
	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()

	src.Emit(time.Now().Add(2 * time.Second))

	// Poll for the gate to close (drain sets draining before running hooks).
	deadline := time.Now().Add(time.Second)
	for h.Admit() {
		if time.Now().After(deadline) {
			t.Fatal("Admit still true after preemption notice; admission gate never closed")
		}
		time.Sleep(time.Millisecond)
	}
	close(gateChecked)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not complete")
	}
	if h.Admit() {
		t.Fatal("Admit must remain false after draining")
	}
}

// TestDrain_ForceStopOnDeadlineExceeded proves that if a drain step exceeds the
// deadline, the handler aborts and invokes ForceStop.
//
// Mutation that this kills: remove the `case <-deadlineCtx.Done()` branch in
// drain (so a slow step is awaited forever / never force-stopped). Then
// ForceStop never runs, Outcome is not OutcomeForceStopped, and both
// assertions FAIL.
func TestDrain_ForceStopOnDeadlineExceeded(t *testing.T) {
	src := NewFakeSource()
	var forced int32
	var rec recorder

	release := make(chan struct{})
	hooks := Hooks{
		StopAdmission: func(context.Context) error { rec.record(StepStopAdmission); return nil },
		// Checkpoint hangs past the deadline (until released after assertions).
		Checkpoint: func(ctx context.Context) error {
			rec.record(StepCheckpoint)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return ctx.Err()
		},
		Deregister: func(context.Context) error { rec.record(StepDeregister); return nil },
		ForceStop:  func() { atomic.StoreInt32(&forced, 1) },
	}
	h := NewHandler(src, hooks)

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()

	// Tight deadline; Checkpoint will outlast it.
	src.Emit(time.Now().Add(80 * time.Millisecond))

	var res Result
	select {
	case res = <-done:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("Run did not force-stop after the deadline")
	}
	close(release)

	if res.Outcome != OutcomeForceStopped {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeForceStopped)
	}
	if atomic.LoadInt32(&forced) != 1 {
		t.Fatal("ForceStop must be invoked when drain exceeds the deadline")
	}
	// Deregister must NOT have completed: we never got past the hung Checkpoint.
	for _, s := range res.Steps {
		if s == StepDeregister {
			t.Fatal("Deregister recorded as completed despite missed deadline")
		}
	}
}

// TestDrain_AlreadyPastDeadline_ForceStopsImmediately proves the pre-step
// deadline guard: if the deadline is already in the past when a step is about
// to run, the handler force-stops without running further hooks.
//
// Mutation that this kills: delete the `if !h.now().Before(notice.Deadline)`
// guard at the top of the step loop. Then with an already-expired deadline the
// handler would still attempt steps instead of immediately force-stopping, the
// outcome would not be OutcomeForceStopped, and ForceStop would not be the sole
// effect — failing the assertions.
func TestDrain_AlreadyPastDeadline_ForceStopsImmediately(t *testing.T) {
	src := NewFakeSource()
	var forced int32
	var ran int32
	hooks := Hooks{
		StopAdmission: func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		Checkpoint:    func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		Deregister:    func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		ForceStop:     func() { atomic.StoreInt32(&forced, 1) },
	}
	h := NewHandler(src, hooks)

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()

	// Deadline already in the past at emit time.
	src.Emit(time.Now().Add(-time.Second))

	var res Result
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return for an already-expired deadline")
	}
	if res.Outcome != OutcomeForceStopped {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeForceStopped)
	}
	if atomic.LoadInt32(&forced) != 1 {
		t.Fatal("ForceStop must run for an already-expired deadline")
	}
	if n := atomic.LoadInt32(&ran); n != 0 {
		t.Fatalf("%d drain steps ran for an already-expired deadline; want 0", n)
	}
}

// TestRun_NoNotice_Idle proves that with no preemption notice the handler is
// idle: no hooks fire and admission stays open until the source closes.
//
// Mutation that this kills: make Run call drain() unconditionally (without
// receiving a notice). Then hooks would fire / admission would close and the
// assertions (empty order, Admit()==true, OutcomeIdle) FAIL.
func TestRun_NoNotice_Idle(t *testing.T) {
	src := NewFakeSource()
	var rec recorder
	var forced int32
	h := NewHandler(src, recordingHooks(&rec, &forced))

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()

	// No Emit. Admission must remain open while idle.
	time.Sleep(50 * time.Millisecond)
	if !h.Admit() {
		t.Fatal("Admit must stay true while no notice has fired")
	}

	src.Close() // source exhausted with no notice → idle

	var res Result
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after source close")
	}
	if res.Outcome != OutcomeIdle {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeIdle)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("hooks fired with no notice: %v", got)
	}
	if atomic.LoadInt32(&forced) != 0 {
		t.Fatal("ForceStop fired with no notice")
	}
	if !h.Admit() {
		t.Fatal("Admit must remain true after an idle source close")
	}
}

// TestRun_ContextCancel_Idle proves ctx cancellation before any notice yields
// idle with no side effects.
//
// Mutation that this kills: remove the `case <-ctx.Done()` branch in Run. Then
// a cancelled context would never unblock Run (test times out) — failing here.
func TestRun_ContextCancel_Idle(t *testing.T) {
	src := NewFakeSource()
	var rec recorder
	var forced int32
	h := NewHandler(src, recordingHooks(&rec, &forced))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() { done <- h.Run(ctx) }()

	cancel()
	select {
	case res := <-done:
		if res.Outcome != OutcomeIdle {
			t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeIdle)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run ignored context cancellation")
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("hooks fired on cancel-before-notice: %v", got)
	}
}

// TestDrain_PropagatesHookError proves the first hook error is surfaced in
// Result.Err while the drain still completes (the deadline was not missed).
//
// Mutation that this kills: drop the `if err != nil { res.Err = err }` line in
// drain. Then Result.Err would be nil and the assertion FAILS.
func TestDrain_PropagatesHookError(t *testing.T) {
	src := NewFakeSource()
	wantErr := errors.New("checkpoint flush failed")
	hooks := Hooks{
		StopAdmission: func(context.Context) error { return nil },
		Checkpoint:    func(context.Context) error { return wantErr },
		Deregister:    func(context.Context) error { return nil },
		ForceStop:     func() {},
	}
	h := NewHandler(src, hooks)

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()
	src.Emit(time.Now().Add(2 * time.Second))

	var res Result
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not complete")
	}
	if !errors.Is(res.Err, wantErr) {
		t.Fatalf("Result.Err = %v, want %v", res.Err, wantErr)
	}
	// All three steps still completed (deadline not missed despite the error).
	if len(res.Steps) != 3 {
		t.Fatalf("Result.Steps = %v, want all 3 steps", res.Steps)
	}
}

// TestDrainOrder_IsCanonical pins the exported DrainOrder so callers can rely on
// it. Mutation: reorder DrainOrder → this FAILS.
func TestDrainOrder_IsCanonical(t *testing.T) {
	want := []DrainStep{StepStopAdmission, StepCheckpoint, StepDeregister}
	if !reflect.DeepEqual(DrainOrder, want) {
		t.Fatalf("DrainOrder = %v, want %v", DrainOrder, want)
	}
}

// TestWithClock_GuardUsesInjectedClock proves the deadline guard consults the
// injected clock, exercising the test seam deterministically.
//
// Mutation that this kills: make the guard use time.Now() directly instead of
// h.now(). Then the injected far-past clock would be ignored and the handler
// would run the steps (drained) instead of force-stopping — failing the
// OutcomeForceStopped assertion.
func TestWithClock_GuardUsesInjectedClock(t *testing.T) {
	src := NewFakeSource()
	var forced int32
	var ran int32
	hooks := Hooks{
		StopAdmission: func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		Checkpoint:    func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		Deregister:    func(context.Context) error { atomic.AddInt32(&ran, 1); return nil },
		ForceStop:     func() { atomic.StoreInt32(&forced, 1) },
	}
	// Clock fixed far in the FUTURE so any deadline is already "past".
	fixed := time.Now().Add(time.Hour)
	h := NewHandler(src, hooks, withClock(func() time.Time { return fixed }))

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()
	// Deadline is in real near-future, but BEFORE the injected fixed clock.
	src.Emit(time.Now().Add(500 * time.Millisecond))

	var res Result
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	if res.Outcome != OutcomeForceStopped {
		t.Fatalf("outcome = %q, want %q (injected clock past deadline)", res.Outcome, OutcomeForceStopped)
	}
	if atomic.LoadInt32(&ran) != 0 {
		t.Fatal("steps ran though injected clock was past the deadline")
	}
	if atomic.LoadInt32(&forced) != 1 {
		t.Fatal("ForceStop must run when injected clock is past the deadline")
	}
}
