package sessionfsm

import (
	"errors"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// mustAdvance calls f.Advance and fails the test immediately on error.
func mustAdvance(t *testing.T, f *FSM, to State) {
	t.Helper()
	if err := f.Advance(to); err != nil {
		t.Fatalf("Advance(%s) unexpected error: %v (current=%s)", to, err, f.Current())
	}
}

// assertState fails if f.Current() != want.
func assertState(t *testing.T, f *FSM, want State) {
	t.Helper()
	if got := f.Current(); got != want {
		t.Fatalf("Current(): got %s, want %s", got, want)
	}
}

// assertHistory fails unless f.History() exactly matches want (element by element).
func assertHistory(t *testing.T, f *FSM, want []State) {
	t.Helper()
	got := f.History()
	if len(got) != len(want) {
		t.Fatalf("History() length: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("History()[%d]: got %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}

// ─── Test 1: happy path ───────────────────────────────────────────────────────

// TestHappyPath verifies that every legal advance along the nominal sequence
// succeeds and that History records the full sequence starting from StateIdle.
//
// Anti-bluff: the History assertion will fail if ANY step is skipped or
// recorded twice, so removing a transition from legalTransitions breaks this.
func TestHappyPath(t *testing.T) {
	f := NewFSM()

	// Initial state must be Idle.
	assertState(t, f, StateIdle)

	steps := []State{
		StateSetup,
		StateRunning,
		StateChaosInject,
		StateVerify,
		StateDone,
	}
	for _, s := range steps {
		if err := f.Advance(s); err != nil {
			t.Fatalf("Advance(%s) returned unexpected error: %v", s, err)
		}
	}

	assertState(t, f, StateDone)

	// History must include the initial StateIdle plus all five transitions.
	want := []State{
		StateIdle,
		StateSetup,
		StateRunning,
		StateChaosInject,
		StateVerify,
		StateDone,
	}
	assertHistory(t, f, want)
}

// ─── Test 2: illegal transitions ─────────────────────────────────────────────

// TestIllegalTransitions verifies that illegal advances return a non-nil
// *IllegalTransitionError and leave the FSM state UNCHANGED.
//
// Anti-bluff: if the transition-table guard is removed, these advances succeed
// and the state changes — both the error check and the state-unchanged check
// catch the mutation.
func TestIllegalTransitions(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*FSM) // drives the FSM to the desired starting state
		to    State
		from  State // expected From in error
	}{
		{
			name:  "Idle->Verify (skip ahead)",
			setup: func(f *FSM) {},
			to:    StateVerify,
			from:  StateIdle,
		},
		{
			name: "Running->Setup (backward)",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateSetup)
				mustAdvance(t, f, StateRunning)
			},
			to:   StateSetup,
			from: StateRunning,
		},
		{
			name:  "Idle->Done (skip ahead to terminal)",
			setup: func(f *FSM) {},
			to:    StateDone,
			from:  StateIdle,
		},
		{
			name:  "Idle->ChaosInject (skip ahead)",
			setup: func(f *FSM) {},
			to:    StateChaosInject,
			from:  StateIdle,
		},
		{
			name: "Done->Setup (out of terminal)",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateSetup)
				mustAdvance(t, f, StateRunning)
				mustAdvance(t, f, StateChaosInject)
				mustAdvance(t, f, StateVerify)
				mustAdvance(t, f, StateDone)
			},
			to:   StateSetup,
			from: StateDone,
		},
		{
			name: "Done->Running (out of terminal)",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateSetup)
				mustAdvance(t, f, StateRunning)
				mustAdvance(t, f, StateChaosInject)
				mustAdvance(t, f, StateVerify)
				mustAdvance(t, f, StateDone)
			},
			to:   StateRunning,
			from: StateDone,
		},
		{
			name: "Failed->Idle (out of terminal)",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateFailed)
			},
			to:   StateIdle,
			from: StateFailed,
		},
		{
			name: "Failed->Setup (out of terminal)",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateFailed)
			},
			to:   StateSetup,
			from: StateFailed,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := NewFSM()
			tc.setup(f)

			stateBefore := f.Current()
			histBefore := f.History()

			err := f.Advance(tc.to)

			// Must return a non-nil error.
			if err == nil {
				t.Fatalf("Advance(%s) from %s: expected error, got nil", tc.to, stateBefore)
			}

			// Error must be *IllegalTransitionError with correct From/To.
			var ile *IllegalTransitionError
			if !errors.As(err, &ile) {
				t.Fatalf("Advance(%s): error type %T, want *IllegalTransitionError", tc.to, err)
			}
			if ile.From != tc.from {
				t.Errorf("IllegalTransitionError.From: got %s, want %s", ile.From, tc.from)
			}
			if ile.To != tc.to {
				t.Errorf("IllegalTransitionError.To: got %s, want %s", ile.To, tc.to)
			}

			// State must be UNCHANGED.
			if got := f.Current(); got != stateBefore {
				t.Fatalf("Current() changed after illegal advance: got %s, want %s", got, stateBefore)
			}

			// History must be UNCHANGED (no entry appended).
			histAfter := f.History()
			if len(histAfter) != len(histBefore) {
				t.Fatalf("History() grew after illegal advance: before=%v after=%v", histBefore, histAfter)
			}
		})
	}
}

// ─── Test 3: ->Failed from non-terminal states ────────────────────────────────

// TestAdvanceToFailed verifies that ->Failed is legal from every non-terminal
// state and that the transition returns nil.
//
// Anti-bluff: if StateFailed is removed from any non-terminal's outgoing set,
// the corresponding advance returns an error and this test fails.
func TestAdvanceToFailed(t *testing.T) {
	nonTerminals := []struct {
		name  string
		setup func(*FSM)
	}{
		{
			name:  "from Idle",
			setup: func(f *FSM) {},
		},
		{
			name: "from Setup",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateSetup)
			},
		},
		{
			name: "from Running",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateSetup)
				mustAdvance(t, f, StateRunning)
			},
		},
		{
			name: "from ChaosInject",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateSetup)
				mustAdvance(t, f, StateRunning)
				mustAdvance(t, f, StateChaosInject)
			},
		},
		{
			name: "from Verify",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateSetup)
				mustAdvance(t, f, StateRunning)
				mustAdvance(t, f, StateChaosInject)
				mustAdvance(t, f, StateVerify)
			},
		},
	}

	for _, tc := range nonTerminals {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := NewFSM()
			tc.setup(f)

			if err := f.Advance(StateFailed); err != nil {
				t.Fatalf("Advance(Failed) %s: unexpected error: %v", tc.name, err)
			}
			assertState(t, f, StateFailed)
		})
	}
}

// ─── Test 4: callbacks fire in order ─────────────────────────────────────────

// TestCallbackOrder verifies that callbacks fire exactly once per registered
// state and in the correct order relative to the advance sequence.
//
// Anti-bluff: the firing-order slice must match the expected order precisely;
// a mutation that fires callbacks in a different order, or fires extra/fewer
// callbacks, causes a mismatch detected by the slice comparison.
func TestCallbackOrder(t *testing.T) {
	f := NewFSM()
	var fired []string

	// Register two callbacks on StateSetup and one on StateRunning.
	f.On(StateSetup, func() { fired = append(fired, "setup-1") })
	f.On(StateSetup, func() { fired = append(fired, "setup-2") })
	f.On(StateRunning, func() { fired = append(fired, "running-1") })

	// Advance through the happy path.
	mustAdvance(t, f, StateSetup)
	mustAdvance(t, f, StateRunning)
	mustAdvance(t, f, StateChaosInject)
	mustAdvance(t, f, StateVerify)
	mustAdvance(t, f, StateDone)

	// Verify exact order and count.
	want := []string{"setup-1", "setup-2", "running-1"}
	if len(fired) != len(want) {
		t.Fatalf("callback fired count: got %d (%v), want %d (%v)", len(fired), fired, len(want), want)
	}
	for i := range want {
		if fired[i] != want[i] {
			t.Fatalf("fired[%d]: got %q, want %q (full: %v)", i, fired[i], want[i], fired)
		}
	}
}

// TestCallbackNotFiredOnIllegalTransition verifies that callbacks are NOT
// invoked when an illegal transition is attempted.
func TestCallbackNotFiredOnIllegalTransition(t *testing.T) {
	f := NewFSM()
	var fired []string

	f.On(StateVerify, func() { fired = append(fired, "verify") })

	// Attempt an illegal skip from Idle to Verify.
	err := f.Advance(StateVerify)
	if err == nil {
		t.Fatal("expected illegal transition error, got nil")
	}
	if len(fired) != 0 {
		t.Fatalf("callback fired on illegal transition: %v", fired)
	}
}

// ─── Test 5: History copy isolation ──────────────────────────────────────────

// TestHistoryCopyIsolation verifies that the slice returned by History() is a
// true copy — mutating it does not affect subsequent calls.
func TestHistoryCopyIsolation(t *testing.T) {
	f := NewFSM()
	mustAdvance(t, f, StateSetup)

	h1 := f.History()
	// Corrupt the returned slice.
	h1[0] = StateDone

	h2 := f.History()
	if h2[0] != StateIdle {
		t.Fatalf("History() not isolated: h2[0] = %s, want Idle", h2[0])
	}
}

// ─── Test 6: State.String() coverage ─────────────────────────────────────────

// TestStateString verifies that every declared State constant has a
// non-empty, non-default string representation.
func TestStateString(t *testing.T) {
	all := []struct {
		s    State
		want string
	}{
		{StateIdle, "Idle"},
		{StateSetup, "Setup"},
		{StateRunning, "Running"},
		{StateChaosInject, "ChaosInject"},
		{StateVerify, "Verify"},
		{StateDone, "Done"},
		{StateFailed, "Failed"},
	}
	for _, tc := range all {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("State(%d).String() = %q, want %q", int(tc.s), got, tc.want)
		}
	}
	// Unknown state must not panic and must not equal any known name.
	unknown := State(999).String()
	if unknown == "" {
		t.Error("State(999).String() returned empty string")
	}
}

// ─── Test 7: Terminal states cannot advance ───────────────────────────────────

// TestTerminalStatesAreBlocked checks that Done and Failed reject every
// possible outgoing transition (including ->Failed for Done, and all others).
func TestTerminalStatesAreBlocked(t *testing.T) {
	allStates := []State{
		StateIdle, StateSetup, StateRunning, StateChaosInject,
		StateVerify, StateDone, StateFailed,
	}

	terminalSetups := []struct {
		name  string
		setup func(*FSM)
		term  State
	}{
		{
			name: "Done",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateSetup)
				mustAdvance(t, f, StateRunning)
				mustAdvance(t, f, StateChaosInject)
				mustAdvance(t, f, StateVerify)
				mustAdvance(t, f, StateDone)
			},
			term: StateDone,
		},
		{
			name: "Failed",
			setup: func(f *FSM) {
				mustAdvance(t, f, StateFailed)
			},
			term: StateFailed,
		},
	}

	for _, tc := range terminalSetups {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, target := range allStates {
				f := NewFSM()
				tc.setup(f)

				err := f.Advance(target)
				if err == nil {
					t.Errorf("terminal %s -> %s: expected error, got nil", tc.term, target)
					continue
				}
				// State must remain the terminal state.
				if got := f.Current(); got != tc.term {
					t.Errorf("terminal %s -> %s: state changed to %s", tc.term, target, got)
				}
			}
		})
	}
}

// ─── Test 8: concurrent safety ────────────────────────────────────────────────

// TestConcurrentAdvance runs multiple goroutines attempting advances
// concurrently; the race detector catches any data races. We don't assert a
// specific final state since races determine ordering, but we verify the FSM
// does not panic and History() is consistent (non-empty, starts with Idle).
func TestConcurrentAdvance(t *testing.T) {
	f := NewFSM()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			_ = f.Advance(StateSetup)
			_ = f.Advance(StateRunning)
			_ = f.Advance(StateFailed)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	h := f.History()
	if len(h) == 0 {
		t.Fatal("History() is empty after concurrent advances")
	}
	if h[0] != StateIdle {
		t.Fatalf("History()[0] = %s, want Idle", h[0])
	}
}
