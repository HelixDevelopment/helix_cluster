package fallbackchain

import (
	"errors"
	"reflect"
	"testing"
)

const advUUID = "fbc-adv-2c19e4a7-6b3d-4f0a-8c5e-1a7d9f0b3e62"

// recorder is an adversarial fake provider. It appends every providerID it is
// asked to attempt to an ordered slice (so EXACT call order and count can be
// asserted), then returns the configured output for that provider. A provider
// with an entry in errs returns that error (with a non-empty sentinel output to
// prove an errored attempt is never mistaken for a win).
type recorder struct {
	calls   []string
	outputs map[string]string
	errs    map[string]error
}

func newRecorder(outputs map[string]string) *recorder {
	return &recorder{outputs: outputs, errs: map[string]error{}}
}

func (r *recorder) fn(providerID string) (string, error) {
	r.calls = append(r.calls, providerID)
	if err, ok := r.errs[providerID]; ok {
		// Non-empty output paired with the error: a correct Run must IGNORE it.
		return "ERRORED-OUTPUT-" + providerID, err
	}
	return r.outputs[providerID], nil
}

// ----------------------------------------------------------------------------
// CENTERPIECE 1 — EXACT ORDER: primary first, then fallbacks in declared order.
// ----------------------------------------------------------------------------

func TestAdvExactOrderAllEmpty(t *testing.T) {
	// All empty so the WHOLE chain is walked; the recorded sequence then proves
	// the exact iteration order end-to-end.
	rec := newRecorder(map[string]string{})
	chain := Chain{Providers: []string{"primary", "fb1", "fb2", "fb3"}}

	t.Logf("run=%s before: providers=%v (all empty)", advUUID, chain.Providers)
	res, err := Run(chain, rec.fn)
	t.Logf("run=%s after: err=%v calls=%v trace=%v", advUUID, err, rec.calls, res.Trace)

	if !errors.Is(err, ErrAllEmpty) {
		t.Fatalf("run=%s expected ErrAllEmpty, got %v", advUUID, err)
	}
	want := []string{"primary", "fb1", "fb2", "fb3"}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("run=%s ORDER VIOLATION: expected exact call sequence %v, got %v", advUUID, want, rec.calls)
	}
	if !reflect.DeepEqual(res.Trace, want) {
		t.Fatalf("run=%s expected trace %v, got %v", advUUID, want, res.Trace)
	}
}

// ----------------------------------------------------------------------------
// CENTERPIECE 2 — STOP-ON-SUCCESS: once a provider wins, NO later provider is
// invoked. The winner sits in the MIDDLE so a missing `return`/`break` would be
// caught: the recorder would capture the trailing providers being called.
// ----------------------------------------------------------------------------

func TestAdvStopOnSuccessMidChain(t *testing.T) {
	rec := newRecorder(map[string]string{
		"primary": "",        // empty -> fall through
		"fb1":     "",        // empty -> fall through
		"WINNER":  "THE-WIN", // wins here
		"fb3":     "LATER-A", // MUST NOT be invoked
		"fb4":     "LATER-B", // MUST NOT be invoked
	})
	chain := Chain{Providers: []string{"primary", "fb1", "WINNER", "fb3", "fb4"}}

	t.Logf("run=%s before: winner is mid-chain; trailing fb3,fb4 must stay untouched", advUUID)
	res, err := Run(chain, rec.fn)
	t.Logf("run=%s after: winner=%q output=%q calls=%v trace=%v", advUUID, res.Provider, res.Output, rec.calls, res.Trace)

	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", advUUID, err)
	}
	// Returned result must be the winner's own value.
	if res.Provider != "WINNER" || res.Output != "THE-WIN" {
		t.Fatalf("run=%s WRONG RESULT: expected WINNER/THE-WIN, got %q/%q", advUUID, res.Provider, res.Output)
	}
	// The call sequence MUST end at the winner — nothing after it.
	wantCalls := []string{"primary", "fb1", "WINNER"}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Fatalf("run=%s STOP-ON-SUCCESS VIOLATION: expected calls to stop at winner %v, got %v", advUUID, wantCalls, rec.calls)
	}
	// Explicitly assert the trailing providers were never touched.
	for _, beyond := range []string{"fb3", "fb4"} {
		if countOccurrences(rec.calls, beyond) != 0 {
			t.Fatalf("run=%s provider %q invoked AFTER a win (calls=%v)", advUUID, beyond, rec.calls)
		}
	}
	// Trace ends at the winner (inclusive), per the documented contract.
	if !reflect.DeepEqual(res.Trace, wantCalls) {
		t.Fatalf("run=%s expected trace to end at winner %v, got %v", advUUID, wantCalls, res.Trace)
	}
}

// First provider wins immediately: only one attempt, ever.
func TestAdvStopOnSuccessFirstProviderWins(t *testing.T) {
	rec := newRecorder(map[string]string{
		"primary": "FIRST-WIN",
		"fb1":     "NOPE",
		"fb2":     "NOPE",
	})
	chain := Chain{Providers: []string{"primary", "fb1", "fb2"}}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s first-wins: winner=%q calls=%v", advUUID, res.Provider, rec.calls)

	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", advUUID, err)
	}
	if res.Provider != "primary" || res.Output != "FIRST-WIN" {
		t.Fatalf("run=%s expected primary/FIRST-WIN, got %q/%q", advUUID, res.Provider, res.Output)
	}
	if !reflect.DeepEqual(rec.calls, []string{"primary"}) {
		t.Fatalf("run=%s STOP-ON-SUCCESS VIOLATION: expected only [primary] invoked, got %v", advUUID, rec.calls)
	}
}

// ----------------------------------------------------------------------------
// DEDUP with ORDER interaction: a provider that recurs LATER must not re-run,
// and the surviving order is the first-seen order. The recurring provider also
// happens to be the eventual winner on its (single) attempt.
// ----------------------------------------------------------------------------

func TestAdvDedupPreservesFirstSeenOrderAndStops(t *testing.T) {
	rec := newRecorder(map[string]string{
		"a": "",      // empty
		"b": "",      // empty
		"c": "WIN-C", // wins
	})
	// "a" recurs at positions 0,2,4; "b" at 1; "c" at 3,5. After dedup the
	// effective order is [a b c]; c wins, so a/b empty then c.
	chain := Chain{Providers: []string{"a", "b", "a", "c", "a", "c"}}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s dedup: calls=%v trace=%v winner=%q", advUUID, rec.calls, res.Trace, res.Provider)

	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", advUUID, err)
	}
	wantCalls := []string{"a", "b", "c"}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Fatalf("run=%s DEDUP/ORDER VIOLATION: expected %v, got %v", advUUID, wantCalls, rec.calls)
	}
	if countOccurrences(rec.calls, "a") != 1 {
		t.Fatalf("run=%s duplicated provider \"a\" invoked more than once: %v", advUUID, rec.calls)
	}
	if res.Provider != "c" || res.Output != "WIN-C" {
		t.Fatalf("run=%s expected c/WIN-C, got %q/%q", advUUID, res.Provider, res.Output)
	}
}

// ----------------------------------------------------------------------------
// CAP with a winner INSIDE the cap window: result is the winner, cap not a
// limiting factor when a win arrives early.
// ----------------------------------------------------------------------------

func TestAdvCapWinnerInsideCap(t *testing.T) {
	rec := newRecorder(map[string]string{
		"p1": "",
		"p2": "WIN-P2",
		"p3": "LATER",
	})
	chain := Chain{Providers: []string{"p1", "p2", "p3", "p4", "p5"}, Cap: 4}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s cap-winner: calls=%v winner=%q", advUUID, rec.calls, res.Provider)

	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", advUUID, err)
	}
	if res.Provider != "p2" || res.Output != "WIN-P2" {
		t.Fatalf("run=%s expected p2/WIN-P2, got %q/%q", advUUID, res.Provider, res.Output)
	}
	if !reflect.DeepEqual(rec.calls, []string{"p1", "p2"}) {
		t.Fatalf("run=%s expected stop at p2, got %v", advUUID, rec.calls)
	}
}

// CAP exactly bounds attempts AND a would-be winner sitting just BEYOND the cap
// must never be reached — proving the cap is enforced before invocation, not
// after. Result must be ErrAllEmpty (the reachable providers were all empty).
func TestAdvCapHidesBeyondCapWinner(t *testing.T) {
	rec := newRecorder(map[string]string{
		"p1": "",          // attempted, empty
		"p2": "",          // attempted, empty
		"p3": "UNREACHED", // a winner, but BEYOND the cap of 2 -> must not run
	})
	chain := Chain{Providers: []string{"p1", "p2", "p3"}, Cap: 2}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s cap-hides-winner: err=%v calls=%v", advUUID, err, rec.calls)

	if !errors.Is(err, ErrAllEmpty) {
		t.Fatalf("run=%s CAP VIOLATION: expected ErrAllEmpty (p3 beyond cap), got err=%v provider=%q", advUUID, err, res.Provider)
	}
	if !reflect.DeepEqual(rec.calls, []string{"p1", "p2"}) {
		t.Fatalf("run=%s CAP VIOLATION: expected exactly [p1 p2], got %v", advUUID, rec.calls)
	}
	if countOccurrences(rec.calls, "p3") != 0 {
		t.Fatalf("run=%s CAP VIOLATION: beyond-cap winner p3 was invoked (calls=%v)", advUUID, rec.calls)
	}
}

// Cap=1 keeps exactly the primary; a non-empty primary still wins.
func TestAdvCapOnePrimaryWins(t *testing.T) {
	rec := newRecorder(map[string]string{"primary": "ONLY", "fb": "IGNORED"})
	chain := Chain{Providers: []string{"primary", "fb"}, Cap: 1}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s cap1: winner=%q calls=%v", advUUID, res.Provider, rec.calls)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", advUUID, err)
	}
	if res.Provider != "primary" || res.Output != "ONLY" {
		t.Fatalf("run=%s expected primary/ONLY, got %q/%q", advUUID, res.Provider, res.Output)
	}
	if !reflect.DeepEqual(rec.calls, []string{"primary"}) {
		t.Fatalf("run=%s expected only primary attempted under Cap=1, got %v", advUUID, rec.calls)
	}
}

// ----------------------------------------------------------------------------
// EMPTY PRIMARY skips to the FIRST non-empty fallback (immediate win on fb1).
// ----------------------------------------------------------------------------

func TestAdvEmptyPrimarySkipsToFirstNonEmptyFallback(t *testing.T) {
	rec := newRecorder(map[string]string{
		"primary": "",         // empty -> skip
		"fb1":     "FROM-FB1", // immediate win
		"fb2":     "LATER",    // must not run
	})
	chain := Chain{Providers: []string{"primary", "fb1", "fb2"}}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s empty-primary: winner=%q calls=%v", advUUID, res.Provider, rec.calls)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", advUUID, err)
	}
	if res.Provider != "fb1" || res.Output != "FROM-FB1" {
		t.Fatalf("run=%s expected fb1/FROM-FB1, got %q/%q", advUUID, res.Provider, res.Output)
	}
	wantCalls := []string{"primary", "fb1"}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Fatalf("run=%s expected primary then fb1 only, got %v", advUUID, rec.calls)
	}
}

// ----------------------------------------------------------------------------
// ERROR SURFACING: a mid-chain error must NOT be surfaced as a win or mask a
// later real success; an all-error chain returns ErrAllEmpty (errors == empty).
// ----------------------------------------------------------------------------

func TestAdvMidChainErrorMaskedByLaterSuccess(t *testing.T) {
	rec := newRecorder(map[string]string{
		"good": "REAL",
	})
	rec.errs["errprov"] = errors.New("provider failed")
	chain := Chain{Providers: []string{"errprov", "good"}}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s err-then-good: winner=%q output=%q calls=%v", advUUID, res.Provider, res.Output, rec.calls)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", advUUID, err)
	}
	if res.Provider != "good" || res.Output != "REAL" {
		t.Fatalf("run=%s ERROR-SURFACING VIOLATION: expected good/REAL, got %q/%q", advUUID, res.Provider, res.Output)
	}
	if !reflect.DeepEqual(rec.calls, []string{"errprov", "good"}) {
		t.Fatalf("run=%s expected [errprov good], got %v", advUUID, rec.calls)
	}
}

func TestAdvAllErroredReturnsErrAllEmpty(t *testing.T) {
	rec := newRecorder(map[string]string{})
	rec.errs["e1"] = errors.New("boom1")
	rec.errs["e2"] = errors.New("boom2")
	chain := Chain{Providers: []string{"e1", "e2"}}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s all-errored: err=%v winner=%q trace=%v", advUUID, err, res.Provider, res.Trace)
	if !errors.Is(err, ErrAllEmpty) {
		t.Fatalf("run=%s expected ErrAllEmpty on all-errored chain, got %v", advUUID, err)
	}
	if res.Provider != "" || res.Output != "" {
		t.Fatalf("run=%s expected empty winner on all-errored, got %q/%q", advUUID, res.Provider, res.Output)
	}
	if !reflect.DeepEqual(res.Trace, []string{"e1", "e2"}) {
		t.Fatalf("run=%s expected full trace [e1 e2], got %v", advUUID, res.Trace)
	}
}

// ----------------------------------------------------------------------------
// EDGES: single provider success; nil attempt fn; all-empty single provider;
// duplicate-collapses-to-one then ErrEmptyChain is NOT triggered (one survives).
// ----------------------------------------------------------------------------

func TestAdvSingleProviderSuccess(t *testing.T) {
	rec := newRecorder(map[string]string{"solo": "SOLO-OUT"})
	res, err := Run(Chain{Providers: []string{"solo"}}, rec.fn)
	t.Logf("run=%s single: winner=%q calls=%v", advUUID, res.Provider, rec.calls)
	if err != nil {
		t.Fatalf("run=%s unexpected error: %v", advUUID, err)
	}
	if res.Provider != "solo" || res.Output != "SOLO-OUT" {
		t.Fatalf("run=%s expected solo/SOLO-OUT, got %q/%q", advUUID, res.Provider, res.Output)
	}
	if !reflect.DeepEqual(rec.calls, []string{"solo"}) {
		t.Fatalf("run=%s expected [solo], got %v", advUUID, rec.calls)
	}
}

func TestAdvNilAttemptFnDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("run=%s nil attemptFn must not panic, got %v", advUUID, r)
		}
	}()
	res, err := Run(Chain{Providers: []string{"x"}}, nil)
	t.Logf("run=%s nil-fn: err=%v", advUUID, err)
	if err == nil {
		t.Fatalf("run=%s expected a non-nil error for nil attemptFn", advUUID)
	}
	if res.Provider != "" || res.Output != "" {
		t.Fatalf("run=%s expected empty result for nil attemptFn, got %q/%q", advUUID, res.Provider, res.Output)
	}
}

// A chain whose providers ALL collapse to a single distinct provider that is
// empty must report ErrAllEmpty (one provider was attempted), NOT ErrEmptyChain.
func TestAdvAllDuplicatesEmptyIsAllEmptyNotEmptyChain(t *testing.T) {
	rec := newRecorder(map[string]string{}) // "z" -> empty
	chain := Chain{Providers: []string{"z", "z", "z"}}

	res, err := Run(chain, rec.fn)
	t.Logf("run=%s dup-empty: err=%v calls=%v trace=%v", advUUID, err, rec.calls, res.Trace)
	if !errors.Is(err, ErrAllEmpty) {
		t.Fatalf("run=%s expected ErrAllEmpty (one distinct provider attempted), got %v", advUUID, err)
	}
	if !reflect.DeepEqual(rec.calls, []string{"z"}) {
		t.Fatalf("run=%s expected single attempt [z], got %v", advUUID, rec.calls)
	}
}
