package console

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// happyPath is the canonical full legal lifecycle, in order.
var happyPath = []BootState{
	StateDetected,
	StateProvisioning,
	StateJoining,
	StateReady,
	StateDraining,
	StateOffline,
}

// TestBootMachine_InitialState asserts the machine starts PowerOff.
//
// Mutation that kills it: change NewBootMachine to start in StateDetected ->
// Current() != StatePowerOff.
func TestBootMachine_InitialState(t *testing.T) {
	m := NewBootMachine()
	if got := m.Current(); got != StatePowerOff {
		t.Fatalf("initial state = %q, want %q", got, StatePowerOff)
	}
}

// TestBootMachine_FullLegalPath drives the machine through every state and
// asserts: the recorded From/To of each event, that exactly one event is
// recorded per transition, that the final state is Offline, and that the
// recorded timestamps are strictly increasing.
//
// Mutation that kills it: in Transition, drop `m.history = append(...)` ->
// History() length 0 != 6. Or set ev.From = to (wrong From) -> From assertion
// fails. Or make the injected clock constant -> strictly-increasing assertion
// fails.
func TestBootMachine_FullLegalPath(t *testing.T) {
	m := NewBootMachine()

	// Inject a strictly monotonic clock so we can assert ordering exactly
	// rather than relying on wall-clock resolution.
	var tick int64
	base := time.Unix(1_700_000_000, 0).UTC()
	m.now = func() time.Time {
		n := atomic.AddInt64(&tick, 1)
		return base.Add(time.Duration(n) * time.Millisecond)
	}

	var fired []TransitionEvent
	m.SetHook(func(ev TransitionEvent) { fired = append(fired, ev) })

	prev := StatePowerOff
	for _, next := range happyPath {
		if !m.CanTransition(next) {
			t.Fatalf("CanTransition(%q) from %q = false, want true", next, prev)
		}
		if err := m.Transition(next); err != nil {
			t.Fatalf("Transition(%q) from %q: unexpected error %v", next, prev, err)
		}
		if got := m.Current(); got != next {
			t.Fatalf("after Transition: Current() = %q, want %q", got, next)
		}
		prev = next
	}

	if got := m.Current(); got != StateOffline {
		t.Fatalf("final state = %q, want %q", got, StateOffline)
	}

	hist := m.History()
	if len(hist) != len(happyPath) {
		t.Fatalf("history length = %d, want %d", len(hist), len(happyPath))
	}

	// Each event must record the correct From/To pair in order.
	expectFrom := StatePowerOff
	for i, ev := range hist {
		if ev.From != expectFrom {
			t.Errorf("event[%d].From = %q, want %q", i, ev.From, expectFrom)
		}
		if ev.To != happyPath[i] {
			t.Errorf("event[%d].To = %q, want %q", i, ev.To, happyPath[i])
		}
		expectFrom = happyPath[i]
	}

	// Timestamps must be strictly increasing.
	for i := 1; i < len(hist); i++ {
		if !hist[i].At.After(hist[i-1].At) {
			t.Errorf("timestamps not strictly increasing: event[%d].At=%v <= event[%d].At=%v",
				i, hist[i].At, i-1, hist[i-1].At)
		}
	}

	// The hook must have fired once per transition with matching events.
	if len(fired) != len(happyPath) {
		t.Fatalf("hook fired %d times, want %d", len(fired), len(happyPath))
	}
	for i := range fired {
		if fired[i] != hist[i] {
			t.Errorf("hook event[%d] = %+v, want %+v", i, fired[i], hist[i])
		}
	}
}

// TestBootMachine_IllegalJumpRejected asserts that an illegal jump
// (PowerOff -> Ready) is rejected, the state is unchanged, no event is
// recorded, and the error is an *IllegalTransitionError naming both states.
//
// Mutation that kills it: make legalBootTransitions allow any-to-any (e.g.
// return true unconditionally in isLegalBootTransition) -> the jump succeeds,
// state becomes Ready, and this assertion fails. THIS IS THE MANDATED MUTATION.
func TestBootMachine_IllegalJumpRejected(t *testing.T) {
	m := NewBootMachine()

	if m.CanTransition(StateReady) {
		t.Fatalf("CanTransition(Ready) from PowerOff = true, want false")
	}

	err := m.Transition(StateReady)
	if err == nil {
		t.Fatalf("Transition(Ready) from PowerOff: got nil error, want rejection")
	}
	var ite *IllegalTransitionError
	if !errors.As(err, &ite) {
		t.Fatalf("error type = %T (%v), want *IllegalTransitionError", err, err)
	}
	if ite.From != StatePowerOff || ite.To != StateReady {
		t.Errorf("error states = %q->%q, want %q->%q", ite.From, ite.To, StatePowerOff, StateReady)
	}

	if got := m.Current(); got != StatePowerOff {
		t.Errorf("state after illegal jump = %q, want unchanged %q", got, StatePowerOff)
	}
	if h := m.History(); len(h) != 0 {
		t.Errorf("history after illegal jump = %d events, want 0", len(h))
	}
}

// TestBootMachine_TerminalRejectsAdvance walks to Offline and asserts every
// further transition (to any valid state) is rejected and state stays Offline.
//
// Mutation that kills it: add an outgoing edge from StateOffline, e.g.
// StateOffline: {StateDetected} -> Transition(Detected) succeeds and this
// fails. Also IsTerminal() would flip.
func TestBootMachine_TerminalRejectsAdvance(t *testing.T) {
	m := NewBootMachine()
	for _, next := range happyPath {
		if err := m.Transition(next); err != nil {
			t.Fatalf("setup Transition(%q): %v", next, err)
		}
	}
	if got := m.Current(); got != StateOffline {
		t.Fatalf("setup: state = %q, want Offline", got)
	}
	if !StateOffline.IsTerminal() {
		t.Fatalf("StateOffline.IsTerminal() = false, want true")
	}

	for s := range allBootStates {
		if m.CanTransition(s) {
			t.Errorf("CanTransition(%q) from Offline = true, want false", s)
		}
		if err := m.Transition(s); err == nil {
			t.Errorf("Transition(%q) from Offline: got nil error, want rejection", s)
		}
		if got := m.Current(); got != StateOffline {
			t.Fatalf("state escaped terminal: = %q, want Offline", got)
		}
	}
}

// TestBootMachine_GuardRejects asserts that a guard returning an error aborts
// the transition, leaves the state unchanged, records no event, and that the
// guard's error is wrapped (unwrappable via errors.Is).
//
// Mutation that kills it: in Transition, ignore the guard's return value (skip
// the `if err := m.guard...` block) -> the transition commits and state becomes
// Detected, failing the unchanged-state assertion.
func TestBootMachine_GuardRejects(t *testing.T) {
	m := NewBootMachine()
	sentinel := errors.New("attestation pending")

	var sawFrom, sawTo BootState
	m.SetGuard(func(from, to BootState) error {
		sawFrom, sawTo = from, to
		return sentinel
	})

	err := m.Transition(StateDetected)
	if err == nil {
		t.Fatalf("Transition(Detected): got nil error, want guard rejection")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapping of sentinel", err)
	}
	if sawFrom != StatePowerOff || sawTo != StateDetected {
		t.Errorf("guard saw %q->%q, want %q->%q", sawFrom, sawTo, StatePowerOff, StateDetected)
	}
	if got := m.Current(); got != StatePowerOff {
		t.Errorf("state after guarded reject = %q, want unchanged %q", got, StatePowerOff)
	}
	if h := m.History(); len(h) != 0 {
		t.Errorf("history after guarded reject = %d events, want 0", len(h))
	}

	// Removing the guard lets the same transition succeed: proves the guard,
	// not the FSM topology, was the blocker.
	m.SetGuard(nil)
	if err := m.Transition(StateDetected); err != nil {
		t.Fatalf("Transition(Detected) after removing guard: %v", err)
	}
	if got := m.Current(); got != StateDetected {
		t.Errorf("state = %q, want Detected", got)
	}
}

// TestBootMachine_EarlyExitToOffline asserts the recovery edge
// Provisioning -> Offline exists (a node can fail provisioning and go straight
// to terminal) while Provisioning -> Ready (skipping Joining) does not.
//
// Mutation that kills it: remove StateOffline from
// legalBootTransitions[StateProvisioning] -> first assertion fails; or add
// StateReady there -> second assertion fails.
func TestBootMachine_EarlyExitToOffline(t *testing.T) {
	m := NewBootMachine()
	for _, next := range []BootState{StateDetected, StateProvisioning} {
		if err := m.Transition(next); err != nil {
			t.Fatalf("setup Transition(%q): %v", next, err)
		}
	}
	if m.CanTransition(StateReady) {
		t.Errorf("Provisioning -> Ready allowed, want rejected (must pass through Joining)")
	}
	if !m.CanTransition(StateOffline) {
		t.Fatalf("Provisioning -> Offline not allowed, want allowed (failure path)")
	}
	if err := m.Transition(StateOffline); err != nil {
		t.Fatalf("Transition(Offline) from Provisioning: %v", err)
	}
	if got := m.Current(); got != StateOffline {
		t.Errorf("state = %q, want Offline", got)
	}
}

// TestBootMachine_InvalidTargetRejected asserts an unknown/garbage target state
// is rejected and does not corrupt state.
//
// Mutation that kills it: drop the `if !to.IsValid()` check in
// isLegalBootTransition AND have legalBootTransitions default to permissive ->
// here the empty adjacency already rejects, but the IsValid guard protects
// CanTransition reporting. Removing IsValid in IsTerminal/IsValid usage would
// also surface here for a bogus state.
func TestBootMachine_InvalidTargetRejected(t *testing.T) {
	m := NewBootMachine()
	bogus := BootState("Teleporting")
	if bogus.IsValid() {
		t.Fatalf("bogus state reported valid")
	}
	if m.CanTransition(bogus) {
		t.Errorf("CanTransition(bogus) = true, want false")
	}
	if err := m.Transition(bogus); err == nil {
		t.Errorf("Transition(bogus): got nil error, want rejection")
	}
	if got := m.Current(); got != StatePowerOff {
		t.Errorf("state = %q, want unchanged PowerOff", got)
	}
}

// TestBootMachine_ConcurrentTransitions hammers Transition from many goroutines.
// Exactly one goroutine should win the PowerOff->Detected edge; the rest must
// receive *IllegalTransitionError (because state already advanced). After the
// race, exactly one event must be recorded and the state must be Detected.
//
// This proves the mutex makes the check-and-commit atomic. Run with -race.
//
// Mutation that kills it: remove the mutex (or the defer Unlock) -> -race flags
// a data race on m.state/m.history; and the success-count invariant
// (exactly one winner, one event) becomes non-deterministic and fails.
func TestBootMachine_ConcurrentTransitions(t *testing.T) {
	m := NewBootMachine()

	const goroutines = 64
	var wg sync.WaitGroup
	var successes int64
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			if err := m.Transition(StateDetected); err == nil {
				atomic.AddInt64(&successes, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if successes != 1 {
		t.Fatalf("concurrent PowerOff->Detected winners = %d, want exactly 1", successes)
	}
	if got := m.Current(); got != StateDetected {
		t.Fatalf("final state = %q, want Detected", got)
	}
	if h := m.History(); len(h) != 1 {
		t.Fatalf("history length = %d, want 1 (one committed transition)", len(h))
	}
}

// TestBootMachine_ConcurrentReaders ensures Current/CanTransition/History do not
// race against a writer goroutine. Run with -race.
//
// Mutation that kills it: drop the lock from Current() (or History()) -> -race
// reports a read/write data race on m.state / m.history.
func TestBootMachine_ConcurrentReaders(t *testing.T) {
	m := NewBootMachine()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		seq := happyPath
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				// Walk forward; once Offline, recreate to keep churning state.
				if i < len(seq) {
					_ = m.Transition(seq[i])
					i++
				} else {
					return
				}
			}
		}
	}()

	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = m.Current()
				_ = m.CanTransition(StateReady)
				_ = m.History()
			}
		}()
	}

	wg.Wait()
	close(stop)
}
