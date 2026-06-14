package llmfailover

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ===========================================================================
// Adversarial / mutation-verified audit of pkg/llmfailover.
//
// Centerpieces:
//   (1) FALLBACK PATH PER CLASS — exact ordered tier sequence per class, the
//       path terminates (finite, no cycle), primary tier first, distinct paths.
//   (2) HINT HONORING + SPECIAL CHARS — Place reflects a valid hint exactly;
//       hints with spaces / unicode / separators / empty / very-long survive a
//       real in-process HTTP round-trip UNCHANGED; a lookalike (trailing-space)
//       flavor must NOT be accepted as a match; a malformed/missing scheduler
//       must not panic.
//   (3) CLASSIFY — representative errors map to the correct class; boundary /
//       unknown / out-of-range handled per contract.
//   (4) DETERMINISM — same inputs → same path / placement.
//   (5) EDGES — nil scheduler, out-of-range class, zero-value job.
//
// Each centerpiece is paired with a documented mutation (in package.go) that it
// is designed to catch; the mutation experiment is recorded in the audit report.
// ===========================================================================

// ---------------------------------------------------------------------------
// (1) FALLBACK PATH PER CLASS
// ---------------------------------------------------------------------------

// adversarialFallbackSpec is the FROZEN, hand-transcribed contract — NOT read
// from the production map — so a production reorder/drop is detected by diff
// against this independent oracle.
var adversarialFallbackSpec = map[ErrorClass][]FallbackAction{
	RateLimited:     {ActionRetryAfterDelay, ActionQueueForLater, ActionFallbackModel},
	ServerError:     {ActionRetryAfterDelay, ActionSwitchEndpoint, ActionEscalateAlert},
	Timeout:         {ActionReduceTokenCount, ActionRetryAfterDelay, ActionSwitchEndpoint},
	AuthError:       {ActionAbort},
	ContentFiltered: {ActionAbort},
	Unknown:         {ActionQueueForLater, ActionEscalateAlert},
}

func TestAdversarial_FallbackPath_ExactPerClass(t *testing.T) {
	const runID = "adv-fallback-exact"

	for class, want := range adversarialFallbackSpec {
		class, want := class, want
		t.Run(class.String(), func(t *testing.T) {
			got := FallbackPath(runID, class)

			if got.RunID != runID {
				t.Errorf("RunID: got %q, want %q", got.RunID, runID)
			}
			if got.Class != class {
				t.Errorf("Class echo: got %v, want %v", got.Class, class)
			}
			if len(got.Actions) != len(want) {
				t.Fatalf("length: got %d %v, want %d %v",
					len(got.Actions), got.Actions, len(want), want)
			}
			for i := range want {
				if got.Actions[i] != want[i] {
					t.Errorf("Actions[%d]: got %q, want %q (full=%v, oracle=%v)",
						i, got.Actions[i], want[i], got.Actions, want)
				}
			}
		})
	}
}

// The path must TERMINATE: it is a finite slice with no repeated action (a
// repeat would be the static analogue of a retry-cycle). This catches a
// mutation that, e.g., duplicates a tier to "pad" a path.
func TestAdversarial_FallbackPath_TerminatesNoCycle(t *testing.T) {
	const runID = "adv-fallback-terminates"
	classes := []ErrorClass{RateLimited, ServerError, Timeout, AuthError, ContentFiltered, Unknown}
	for _, class := range classes {
		got := FallbackPath(runID, class).Actions
		if len(got) == 0 {
			t.Errorf("%v: empty path — a class must always yield at least one action", class)
		}
		if len(got) > 8 {
			t.Errorf("%v: path length %d is implausibly long — possible cycle/duplication", class, len(got))
		}
		seen := map[FallbackAction]bool{}
		for _, a := range got {
			if seen[a] {
				t.Errorf("%v: action %q repeats in path %v — non-terminating/cyclic tier sequence", class, a, got)
			}
			seen[a] = true
		}
	}
}

// PRIMARY-FIRST invariant: the documented first tier of each primary class is a
// load-bearing assertion. A reorder that moves the first tier later is caught.
func TestAdversarial_FallbackPath_PrimaryTierFirst(t *testing.T) {
	const runID = "adv-fallback-primary-first"
	wantFirst := map[ErrorClass]FallbackAction{
		RateLimited: ActionRetryAfterDelay,
		ServerError: ActionRetryAfterDelay,
		Timeout:     ActionReduceTokenCount, // unique first tier
		AuthError:   ActionAbort,
		Unknown:     ActionQueueForLater,
	}
	for class, first := range wantFirst {
		got := FallbackPath(runID, class).Actions
		if len(got) == 0 || got[0] != first {
			t.Errorf("%v: first tier got %v, want %q", class, got, first)
		}
	}
}

// Terminal classes are abort-only; non-terminal classes must NOT contain Abort.
func TestAdversarial_FallbackPath_TerminalVsRetryable(t *testing.T) {
	const runID = "adv-fallback-terminal"
	terminal := []ErrorClass{AuthError, ContentFiltered}
	for _, class := range terminal {
		got := FallbackPath(runID, class).Actions
		if len(got) != 1 || got[0] != ActionAbort {
			t.Errorf("terminal %v: got %v, want [%s]", class, got, ActionAbort)
		}
	}
	retryable := []ErrorClass{RateLimited, ServerError, Timeout, Unknown}
	for _, class := range retryable {
		for _, a := range FallbackPath(runID, class).Actions {
			if a == ActionAbort {
				t.Errorf("retryable %v must not contain Abort, got %v", class, FallbackPath(runID, class).Actions)
			}
		}
	}
}

// Full-matrix distinctness: EVERY pair of classes (including terminal/Unknown)
// either differs in length or in some position. This is stronger than the
// existing test, which only compared the three non-terminal primaries.
func TestAdversarial_FallbackPath_AllPairsDistinctOrAliased(t *testing.T) {
	const runID = "adv-fallback-distinct"
	classes := []ErrorClass{RateLimited, ServerError, Timeout, AuthError, ContentFiltered, Unknown}
	// Known-aliased intentional pair: AuthError and ContentFiltered are both
	// terminal aborts (documented). Every OTHER pair must be distinct.
	aliased := func(a, b ErrorClass) bool {
		return (a == AuthError && b == ContentFiltered) || (a == ContentFiltered && b == AuthError)
	}
	for i := 0; i < len(classes); i++ {
		for j := i + 1; j < len(classes); j++ {
			a := FallbackPath(runID, classes[i]).Actions
			b := FallbackPath(runID, classes[j]).Actions
			same := advActionsEqual(a, b)
			if same && !aliased(classes[i], classes[j]) {
				t.Errorf("classes %v and %v share path %v — a swap between them would be undetectable",
					classes[i], classes[j], a)
			}
			if !same && aliased(classes[i], classes[j]) {
				t.Errorf("terminal classes %v and %v unexpectedly differ: %v vs %v",
					classes[i], classes[j], a, b)
			}
		}
	}
}

func advActionsEqual(a, b []FallbackAction) bool {
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

// Determinism: repeated lookups of the same class yield byte-identical paths,
// and the returned slice is a defensive copy (mutating it does not corrupt the
// shared table).
func TestAdversarial_FallbackPath_DeterministicAndCopied(t *testing.T) {
	const runID = "adv-fallback-determinism"
	for _, class := range []ErrorClass{RateLimited, ServerError, Timeout, AuthError, ContentFiltered, Unknown} {
		first := FallbackPath(runID, class).Actions
		for i := 0; i < 50; i++ {
			again := FallbackPath(runID, class).Actions
			if !advActionsEqual(first, again) {
				t.Fatalf("%v: nondeterministic path: %v vs %v", class, first, again)
			}
		}
		// Corrupt the returned copy and confirm a fresh lookup is unaffected.
		mutated := FallbackPath(runID, class)
		if len(mutated.Actions) > 0 {
			mutated.Actions[0] = FallbackAction("CORRUPTED")
		}
		clean := FallbackPath(runID, class).Actions
		if !advActionsEqual(first, clean) {
			t.Errorf("%v: caller mutation leaked into shared table: %v", class, clean)
		}
	}
}

// Out-of-range / boundary ErrorClass values fall back to the Unknown path
// (documented default) and must never panic.
func TestAdversarial_FallbackPath_OutOfRangeClass(t *testing.T) {
	const runID = "adv-fallback-oob"
	unknownPath := FallbackPath(runID, Unknown).Actions
	for _, raw := range []int{-1, -999, 6, 7, 100, 1 << 30} {
		got := FallbackPath(runID, ErrorClass(raw)).Actions
		if !advActionsEqual(got, unknownPath) {
			t.Errorf("ErrorClass(%d): got %v, want Unknown path %v", raw, got, unknownPath)
		}
	}
}

// ---------------------------------------------------------------------------
// (2) HINT HONORING + SPECIAL CHARS
// ---------------------------------------------------------------------------

func TestAdversarial_Place_SpecialCharFlavorsRoundTrip(t *testing.T) {
	sched := newDirectScheduler(t)

	cases := []struct {
		name   string
		jobID  string
		flavor string
	}{
		{"spaces", "job with spaces", "gpu large model"},
		{"plus-and-amp", "job+a&b", "gpu+large&fast=yes"},
		{"equals-sep", "k=v", "a=1;b=2"},
		{"unicode", "jоb-кириллица", "gpu-大型-🚀"},
		{"slash-and-question", "a/b?c", "x/y?z#w"},
		{"percent-literal", "100%done", "50%off"},
		{"very-long", strings.Repeat("J", 2048), strings.Repeat("g-pu_", 600)},
		{"newline-tab", "line1\nline2\ttab", "f\nl\ta"},
		{"json-meta", `{"a":1}`, `[{"k":"v"}]`}, // quotes/braces must survive the %q decode
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			job := SandboxJob{ID: tc.jobID, Priority: 5, Preemptable: true, Flavor: tc.flavor}
			p, err := Place("adv-special-"+tc.name, job, sched)
			if err != nil {
				t.Fatalf("Place: unexpected error: %v", err)
			}
			if p.JobID != tc.jobID {
				t.Errorf("JobID corrupted: got %q, want %q", p.JobID, tc.jobID)
			}
			if p.Flavor != tc.flavor {
				t.Errorf("Flavor corrupted: got %q, want %q", p.Flavor, tc.flavor)
			}
			if p.Priority != job.Priority || p.Preemptable != job.Preemptable {
				t.Errorf("scalar hints corrupted: got pri=%d preempt=%v, want pri=%d preempt=%v",
					p.Priority, p.Preemptable, job.Priority, job.Preemptable)
			}
			if p.NodeID == "" {
				t.Error("NodeID empty — no real placement made")
			}
		})
	}
}

// Empty hint: an empty flavor and zero job must place cleanly (no panic, exact echo).
func TestAdversarial_Place_EmptyAndZeroHints(t *testing.T) {
	sched := newDirectScheduler(t)
	job := SandboxJob{ID: "", Priority: 0, Preemptable: false, Flavor: ""}
	p, err := Place("", job, sched)
	if err != nil {
		t.Fatalf("Place zero-job: unexpected error: %v", err)
	}
	if p.Flavor != "" || p.Priority != 0 || p.Preemptable != false {
		t.Errorf("zero hints not echoed: %+v", p)
	}
	if p.NodeID == "" {
		t.Error("NodeID empty for zero job")
	}
}

// LOOKALIKE / EXACT-MATCH guard: the hint-passthrough contract is EXACT equality,
// not substring or trimmed match. A scheduler that returns a flavor that is a
// near-miss (trailing space, different case, substring, unicode-lookalike) MUST
// be rejected as a contract violation — never silently accepted.
func TestAdversarial_Place_LookalikeFlavorRejected(t *testing.T) {
	const runID = "adv-lookalike"
	want := "gpu-large"
	job := SandboxJob{ID: "j", Priority: 1, Preemptable: false, Flavor: want}

	lookalikes := []string{
		"gpu-large ", // trailing space
		" gpu-large", // leading space
		"GPU-LARGE",  // case
		"gpu-larg",   // substring (prefix)
		"gpu-largex", // superstring
		"gpu_large",  // separator swap
		"gpu-lаrge",  // Cyrillic 'а' lookalike
		"",           // empty
	}
	for _, bad := range lookalikes {
		bad := bad
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			sched := &badScheduler{priority: job.Priority, preemptable: job.Preemptable, flavor: bad}
			_, err := Place(runID, job, sched)
			if err == nil {
				t.Errorf("lookalike flavor %q was accepted as a match for %q — exact-match contract broken",
					bad, want)
			}
		})
	}
	// Control: the exact value is accepted.
	good := &badScheduler{priority: job.Priority, preemptable: job.Preemptable, flavor: want}
	if _, err := Place(runID, job, good); err != nil {
		t.Errorf("exact flavor %q rejected: %v", want, err)
	}
}

// Special characters in the hint must not let a near-miss slip through either:
// a scheduler echoing a flavor that differs only by a dropped trailing '&turbo'
// (the kind of corruption an unescaped URL would cause) is rejected.
func TestAdversarial_Place_TruncatedSpecialHintRejected(t *testing.T) {
	const runID = "adv-truncated"
	full := "gpu large+fast&turbo=yes"
	job := SandboxJob{ID: "j", Priority: 2, Flavor: full}
	truncated := "gpu large+fast" // what an unescaped URL would silently produce
	sched := &badScheduler{priority: job.Priority, preemptable: job.Preemptable, flavor: truncated}
	if _, err := Place(runID, job, sched); err == nil {
		t.Errorf("truncated flavor %q accepted for %q — exact-match contract broken", truncated, full)
	}
}

// Determinism: placing the same job many times yields identical placements
// through the real HTTP seam.
func TestAdversarial_Place_Deterministic(t *testing.T) {
	sched := newDirectScheduler(t)
	job := SandboxJob{ID: "det-job", Priority: 9, Preemptable: true, Flavor: "gpu x+y&z"}
	first, err := Place("adv-det", job, sched)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	for i := 0; i < 25; i++ {
		got, err := Place("adv-det", job, sched)
		if err != nil {
			t.Fatalf("Place iter %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("nondeterministic placement: %+v vs %+v", got, first)
		}
	}
}

// Nil / malformed scheduler must not panic.
func TestAdversarial_Place_NilSchedulerNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Place panicked on nil scheduler: %v", r)
		}
	}()
	job := SandboxJob{ID: "j", Priority: 1, Flavor: "weird flavor+with&stuff"}
	_, err := Place("adv-nil", job, nil)
	if !errors.Is(err, ErrNoCapability) {
		t.Errorf("nil scheduler: got %v, want ErrNoCapability", err)
	}
}

// A scheduler that returns its own error must have that error propagated
// verbatim (not swallowed into a contract-violation).
func TestAdversarial_Place_SchedulerErrorPropagates(t *testing.T) {
	sentinel := errors.New("scheduler down")
	sched := schedulerFunc(func(runID string, job SandboxJob) (Placement, error) {
		return Placement{}, sentinel
	})
	_, err := Place("adv-err", SandboxJob{ID: "j"}, sched)
	if !errors.Is(err, sentinel) {
		t.Errorf("scheduler error not propagated: got %v, want %v", err, sentinel)
	}
}

type schedulerFunc func(runID string, job SandboxJob) (Placement, error)

func (f schedulerFunc) Schedule(runID string, job SandboxJob) (Placement, error) {
	return f(runID, job)
}

// ---------------------------------------------------------------------------
// (3) CLASSIFY
// ---------------------------------------------------------------------------

// Representative errors map to the correct class via the sentinel table, and
// wrapping is honored (errors.Is semantics).
func TestAdversarial_Classify_SentinelMappingExact(t *testing.T) {
	clf := &Classifier{Predicates: map[ErrorClass]Predicate{}, Now: func() time.Time {
		return time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	}}
	cases := []struct {
		err  error
		want ErrorClass
	}{
		{ErrRateLimited, RateLimited},
		{ErrServerError, ServerError},
		{ErrTimeout, Timeout},
		{ErrAuthError, AuthError},
		{ErrContentFiltered, ContentFiltered},
		{fmt.Errorf("outer: %w", ErrAuthError), AuthError},
		{fmt.Errorf("a: %w", fmt.Errorf("b: %w", ErrContentFiltered)), ContentFiltered},
		{errors.New("totally novel"), Unknown},
		{nil, Unknown},
	}
	for _, tc := range cases {
		got := clf.Classify("adv-classify", tc.err).Class
		if got != tc.want {
			t.Errorf("Classify(%v): got %v, want %v", tc.err, got, tc.want)
		}
	}
}

// Predicate order is fixed and deterministic: when two predicates both match,
// the one earlier in the canonical class order (RateLimited < ServerError <
// Timeout < AuthError < ContentFiltered) wins — regardless of map insertion.
// This catches a mutation that iterates the predicate map directly (map order
// is randomized in Go).
func TestAdversarial_Classify_PredicateOrderDeterministic(t *testing.T) {
	alwaysTrue := func(error) bool { return true }
	// Both RateLimited and Timeout predicates match everything; RateLimited is
	// earlier in canonical order and must win every single time.
	clf := &Classifier{
		Predicates: map[ErrorClass]Predicate{
			Timeout:     alwaysTrue,
			RateLimited: alwaysTrue,
			ServerError: alwaysTrue,
		},
		Now: time.Now,
	}
	for i := 0; i < 200; i++ {
		got := clf.Classify("adv-order", errors.New("x")).Class
		if got != RateLimited {
			t.Fatalf("iter %d: got %v, want RateLimited (canonical-order precedence broken / map-order leak)", i, got)
		}
	}
}

// Injected predicate overrides the sentinel table.
func TestAdversarial_Classify_PredicateBeatsSentinel(t *testing.T) {
	clf := &Classifier{
		Predicates: map[ErrorClass]Predicate{
			ServerError: func(error) bool { return true },
		},
		Now: time.Now,
	}
	// Error IS ErrRateLimited by sentinel, but ServerError predicate fires first.
	if got := clf.Classify("adv-override", ErrRateLimited).Class; got != ServerError {
		t.Errorf("predicate did not override sentinel: got %v, want ServerError", got)
	}
}

// RunID and injected clock are threaded through unchanged; determinism across
// repeated calls.
func TestAdversarial_Classify_RunIDAndClockDeterministic(t *testing.T) {
	fixed := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	clf := &Classifier{Predicates: map[ErrorClass]Predicate{}, Now: func() time.Time { return fixed }}
	const runID = "adv-classify-runid+special&chars"
	for i := 0; i < 10; i++ {
		r := clf.Classify(runID, ErrTimeout)
		if r.RunID != runID {
			t.Errorf("RunID: got %q, want %q", r.RunID, runID)
		}
		if r.Class != Timeout {
			t.Errorf("Class: got %v, want Timeout", r.Class)
		}
		if !r.Timestamp.Equal(fixed) {
			t.Errorf("Timestamp: got %v, want %v (injected clock not used)", r.Timestamp, fixed)
		}
		if !errors.Is(r.Err, ErrTimeout) {
			t.Errorf("Err not preserved: %v", r.Err)
		}
	}
}

// ---------------------------------------------------------------------------
// (4) Cross-cutting determinism: Classify→FallbackPath end-to-end is stable.
// ---------------------------------------------------------------------------

func TestAdversarial_ClassifyThenFallbackEndToEnd(t *testing.T) {
	clf := &Classifier{Predicates: map[ErrorClass]Predicate{}, Now: time.Now}
	pairs := []struct {
		err  error
		want []FallbackAction
	}{
		{ErrRateLimited, adversarialFallbackSpec[RateLimited]},
		{ErrServerError, adversarialFallbackSpec[ServerError]},
		{ErrTimeout, adversarialFallbackSpec[Timeout]},
		{ErrAuthError, adversarialFallbackSpec[AuthError]},
		{ErrContentFiltered, adversarialFallbackSpec[ContentFiltered]},
		{errors.New("novel"), adversarialFallbackSpec[Unknown]},
	}
	for _, p := range pairs {
		class := clf.Classify("adv-e2e", p.err).Class
		path := FallbackPath("adv-e2e", class).Actions
		if !advActionsEqual(path, p.want) {
			t.Errorf("end-to-end %v: class=%v path=%v, want %v", p.err, class, path, p.want)
		}
	}
}
