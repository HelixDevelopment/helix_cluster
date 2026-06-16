package sessionfsm

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// This file is an adversarial probe of the sessionfsm FSM. It pins the
// transition contract exhaustively, hammers terminal-state absorption,
// checks unknown/negative states, and verifies concurrent consistency.
//
// CLASSIFICATION of each probe is documented inline:
//   REAL BUG          — a correct test that fails on the production code.
//   DOCUMENTED CONTRACT — pins intended behavior so a regression is caught.
//   LATENT RISK       — notes a sharp edge that is not currently a bug.

// allStatesAdv is the full set of declared states, used to enumerate every
// (from, to) pair the FSM could ever be asked to perform.
var allStatesAdv = []State{
	StateIdle, StateSetup, StateRunning, StateChaosInject,
	StateVerify, StateDone, StateFailed,
}

// driveTo returns a fresh FSM parked in `target` using only legal advances.
// It returns nil if there is no legal path (which there always is for the
// happy-path chain + ->Failed), failing the test otherwise.
func driveTo(t *testing.T, target State) *FSM {
	t.Helper()
	f := NewFSM()
	switch target {
	case StateIdle:
		// already there
	case StateSetup:
		mustAdvance(t, f, StateSetup)
	case StateRunning:
		mustAdvance(t, f, StateSetup)
		mustAdvance(t, f, StateRunning)
	case StateChaosInject:
		mustAdvance(t, f, StateSetup)
		mustAdvance(t, f, StateRunning)
		mustAdvance(t, f, StateChaosInject)
	case StateVerify:
		mustAdvance(t, f, StateSetup)
		mustAdvance(t, f, StateRunning)
		mustAdvance(t, f, StateChaosInject)
		mustAdvance(t, f, StateVerify)
	case StateDone:
		mustAdvance(t, f, StateSetup)
		mustAdvance(t, f, StateRunning)
		mustAdvance(t, f, StateChaosInject)
		mustAdvance(t, f, StateVerify)
		mustAdvance(t, f, StateDone)
	case StateFailed:
		mustAdvance(t, f, StateFailed)
	default:
		t.Fatalf("driveTo: unknown target %s", target)
	}
	if got := f.Current(); got != target {
		t.Fatalf("driveTo(%s): parked in %s instead", target, got)
	}
	return f
}

// expectedLegal is an INDEPENDENT re-derivation of the legal transition set
// from the package doc comment ("Idle->Setup->Running->ChaosInject->Verify->
// Done; any non-terminal -> Failed"). It deliberately does NOT read
// legalTransitions, so a mutation to that table is caught by divergence.
func expectedLegal(from, to State) bool {
	happy := map[State]State{
		StateIdle:        StateSetup,
		StateSetup:       StateRunning,
		StateRunning:     StateChaosInject,
		StateChaosInject: StateVerify,
		StateVerify:      StateDone,
	}
	if next, ok := happy[from]; ok && next == to {
		return true
	}
	// Any non-terminal may escape to Failed.
	nonTerminal := from == StateIdle || from == StateSetup ||
		from == StateRunning || from == StateChaosInject || from == StateVerify
	if nonTerminal && to == StateFailed {
		return true
	}
	return false
}

// ─── Probe 1: exhaustive transition matrix ────────────────────────────────────
//
// CLASSIFICATION: DOCUMENTED CONTRACT (pins the full 7x7 matrix).
//
// For EVERY (from, to) pair, the FSM's Advance must agree with the independent
// oracle expectedLegal: legal pairs succeed and move state; illegal pairs
// return *IllegalTransitionError and leave state + history untouched.
func TestAdv_ExhaustiveTransitionMatrix(t *testing.T) {
	for _, from := range allStatesAdv {
		for _, to := range allStatesAdv {
			from, to := from, to
			f := driveTo(t, from)
			histBefore := f.History()

			err := f.Advance(to)
			want := expectedLegal(from, to)

			if want {
				if err != nil {
					t.Errorf("%s->%s: expected legal, got error %v", from, to, err)
					continue
				}
				if got := f.Current(); got != to {
					t.Errorf("%s->%s: legal advance did not move state, current=%s", from, to, got)
				}
				h := f.History()
				if len(h) != len(histBefore)+1 || h[len(h)-1] != to {
					t.Errorf("%s->%s: history not extended correctly: %v", from, to, h)
				}
			} else {
				if err == nil {
					t.Errorf("%s->%s: expected illegal (rejected), got nil error; current=%s", from, to, f.Current())
					continue
				}
				var ile *IllegalTransitionError
				if !errors.As(err, &ile) {
					t.Errorf("%s->%s: error type %T, want *IllegalTransitionError", from, to, err)
				} else {
					if ile.From != from || ile.To != to {
						t.Errorf("%s->%s: error has From=%s To=%s", from, to, ile.From, ile.To)
					}
				}
				if got := f.Current(); got != from {
					t.Errorf("%s->%s: illegal advance changed state to %s", from, to, got)
				}
				if h := f.History(); len(h) != len(histBefore) {
					t.Errorf("%s->%s: illegal advance grew history: %v", from, to, h)
				}
			}
		}
	}
}

// ─── Probe 2: self-transitions are illegal ────────────────────────────────────
//
// CLASSIFICATION: DOCUMENTED CONTRACT.
//
// Advancing a state to itself is not in the table, so it must be rejected and
// must not duplicate a history entry.
func TestAdv_SelfTransitionRejected(t *testing.T) {
	for _, s := range allStatesAdv {
		f := driveTo(t, s)
		hb := f.History()
		if err := f.Advance(s); err == nil {
			t.Errorf("self-transition %s->%s: expected error, got nil", s, s)
		}
		if got := f.Current(); got != s {
			t.Errorf("self-transition %s: state changed to %s", s, got)
		}
		if h := f.History(); len(h) != len(hb) {
			t.Errorf("self-transition %s: history grew to %v", s, h)
		}
	}
}

// ─── Probe 3: terminal states are absorbing ───────────────────────────────────
//
// CLASSIFICATION: DOCUMENTED CONTRACT.
//
// Done and Failed reject EVERY outgoing target, including ->Failed and ->self.
func TestAdv_TerminalAbsorbing(t *testing.T) {
	for _, term := range []State{StateDone, StateFailed} {
		for _, to := range allStatesAdv {
			f := driveTo(t, term)
			if err := f.Advance(to); err == nil {
				t.Errorf("terminal %s->%s: expected rejection, got nil", term, to)
			}
			if got := f.Current(); got != term {
				t.Errorf("terminal %s->%s: escaped to %s", term, to, got)
			}
		}
	}
}

// ─── Probe 4: unknown / negative states handled cleanly ───────────────────────
//
// CLASSIFICATION: DOCUMENTED CONTRACT (no panic; rejected from any state).
func TestAdv_UnknownStateRejected(t *testing.T) {
	bogus := []State{State(-1), State(999), State(42), State(-100)}
	for _, from := range allStatesAdv {
		for _, to := range bogus {
			f := driveTo(t, from)
			err := f.Advance(to) // must not panic
			if err == nil {
				t.Errorf("%s->%s(bogus): expected rejection, got nil", from, to)
			}
			if got := f.Current(); got != from {
				t.Errorf("%s->%s(bogus): state changed to %s", from, to, got)
			}
		}
	}
	// Advancing FROM a bogus state is not constructible via the public API
	// (NewFSM always starts Idle and only legal advances mutate state), so
	// there is no way for the FSM to be parked in a bogus state. Document that
	// the String() of a bogus state is non-empty and non-panicking.
	if s := State(-7).String(); s == "" {
		t.Error("State(-7).String() returned empty")
	}
}

// ─── Probe 5: same-construction determinism (reproducibility) ─────────────────
//
// CLASSIFICATION: DOCUMENTED CONTRACT.
//
// The FSM advertises no RNG/clock/map-iteration dependence. Two independently
// constructed FSMs driven through the identical advance script must produce
// byte-identical History and identical callback firing order, every run.
// (Sibling pkg/testing/dst leaked nondeterminism via map iteration; this pins
// that sessionfsm does not.)
func TestAdv_DeterministicReplay(t *testing.T) {
	script := []State{StateSetup, StateRunning, StateChaosInject, StateVerify, StateDone}

	run := func() ([]State, []string) {
		f := NewFSM()
		var fired []string
		// Register several callbacks across states; order must be stable.
		f.On(StateSetup, func() { fired = append(fired, "s1") })
		f.On(StateSetup, func() { fired = append(fired, "s2") })
		f.On(StateRunning, func() { fired = append(fired, "r1") })
		f.On(StateVerify, func() { fired = append(fired, "v1") })
		for _, s := range script {
			if err := f.Advance(s); err != nil {
				t.Fatalf("scripted advance %s failed: %v", s, err)
			}
		}
		return f.History(), fired
	}

	baseHist, baseFired := run()
	for i := 0; i < 200; i++ {
		h, fired := run()
		if len(h) != len(baseHist) {
			t.Fatalf("run %d: history length drift %v vs %v", i, h, baseHist)
		}
		for j := range baseHist {
			if h[j] != baseHist[j] {
				t.Fatalf("run %d: history[%d] drift %s vs %s", i, j, h[j], baseHist[j])
			}
		}
		if len(fired) != len(baseFired) {
			t.Fatalf("run %d: callback count drift %v vs %v", i, fired, baseFired)
		}
		for j := range baseFired {
			if fired[j] != baseFired[j] {
				t.Fatalf("run %d: callback order drift at %d: %s vs %s", i, j, fired[j], baseFired[j])
			}
		}
	}
}

// ─── Probe 6: callbacks not fired on rejected advance ─────────────────────────
//
// CLASSIFICATION: DOCUMENTED CONTRACT.
func TestAdv_NoCallbackOnReject(t *testing.T) {
	f := driveTo(t, StateRunning)
	var fired int
	f.On(StateSetup, func() { fired++ }) // Running->Setup is illegal
	if err := f.Advance(StateSetup); err == nil {
		t.Fatal("Running->Setup expected rejection")
	}
	if fired != 0 {
		t.Fatalf("callback fired %d times on rejected advance", fired)
	}
}

// ─── Probe 7: concurrent advances keep state/history consistent ───────────────
//
// CLASSIFICATION: DOCUMENTED CONTRACT (race-detector + invariant checks).
//
// Many goroutines race to drive the same FSM. Regardless of interleaving:
//   - the FSM never panics,
//   - History always begins with Idle,
//   - every adjacent pair in the final History is a LEGAL edge (no torn /
//     interleaved write produced an impossible sequence),
//   - the final Current() equals the last History entry.
// Guarded with a timeout so a deadlock regression fails instead of hanging.
func TestAdv_ConcurrentConsistency(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f := NewFSM()
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = f.Advance(StateSetup)
				_ = f.Advance(StateRunning)
				_ = f.Advance(StateChaosInject)
				_ = f.Advance(StateVerify)
				_ = f.Advance(StateDone)
				_ = f.Advance(StateFailed)
			}()
		}
		wg.Wait()

		h := f.History()
		if len(h) == 0 || h[0] != StateIdle {
			t.Errorf("history must start with Idle: %v", h)
		}
		// Every adjacent pair must be a legal edge.
		for i := 1; i < len(h); i++ {
			if !legalTransitions[h[i-1]][h[i]] {
				t.Errorf("history contains illegal edge %s->%s at %d: %v", h[i-1], h[i], i, h)
			}
		}
		if cur := f.Current(); cur != h[len(h)-1] {
			t.Errorf("Current()=%s disagrees with last history entry %s", cur, h[len(h)-1])
		}
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("concurrent advance timed out (possible deadlock regression)")
	}
}
