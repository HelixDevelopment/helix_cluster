package helixtask

// Adversarial probes for pkg/helixtask.
//
// NOTE ON SCOPE: the briefing assumed a stateful task manager (shared task map,
// stored state machine, task IDs, priority queue, idempotency counter). The
// REAL package is a pure, stateless function decorator: Task() returns a closure
// over immutable inputs and each invocation builds a fresh function-local Spec.
// There is NO package-owned shared map / counter / ID registry to race.
//
// The adversarial classes are therefore re-aimed at the contract the package
// ACTUALLY documents and the sinks it actually owns:
//
//	(a) UNGUARDED SHARED STATE — does the decorator CORE introduce any hidden
//	    shared mutable state when one Task closure is driven by many goroutines,
//	    or many tasks share one Submitter? Sink: -race clean + conservation
//	    (every concurrent invocation is submitted exactly once; no Spec clobber).
//	(b) STATE TRANSITION (hook dispatch is the state machine) — success and
//	    failure paths are MUTUALLY EXCLUSIVE: OnComplete XOR OnError, never both
//	    ("torn"), never neither. Sink: the actual fired-hook set per invocation,
//	    incl. under concurrency.
//	(c) IDENTITY / propagation integrity — concurrent tasks with DIFFERENT opts
//	    sharing one Submitter must each carry their OWN opts onto the Spec their
//	    hooks observe; no cross-invocation clobber. Sink: per-call observed Spec.
//	(d) IDEMPOTENCY — re-invoking one callable submits again (a decorator is not
//	    memoized, by contract) and within a SINGLE invocation a lifecycle hook
//	    fires exactly once (no double-complete). Sink: submit count + hook count.
//
// All assertions are SINK-SIDE (observed Spec / fired-hook sets / conservation
// counts), never merely "did not panic".

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) UNGUARDED SHARED STATE: one Task closure, many goroutines.
//
// A single decorated callable is shared across N goroutines and invoked
// concurrently. The decorator core must hold no shared mutable state of its
// own. Sink-side conservation: the injected Submitter (made internally
// thread-safe HERE, in the test, so the test is not measuring the user's pool)
// must observe EXACTLY N submissions, and every observed Spec must carry the
// (identical) propagated opts. Run under -race.
// ---------------------------------------------------------------------------
func TestAdversarial_ConcurrentSharedClosure_Conservation(t *testing.T) {
	const N = 2000

	var submits int64
	var teeMismatch int64
	var retMismatch int64

	// Thread-safe submitter: the test owns the synchronization so that any
	// race the -race detector reports is the DECORATOR's, not the pool's.
	pool := SubmitterFunc(func(s Spec) (any, error) {
		atomic.AddInt64(&submits, 1)
		if !s.TEE {
			atomic.AddInt64(&teeMismatch, 1)
		}
		if s.Retries != 7 {
			atomic.AddInt64(&retMismatch, 1)
		}
		return "ok", nil
	})

	var completes int64
	opts := Options{
		TEE:        true,
		Retries:    7,
		OnComplete: func(Spec) { atomic.AddInt64(&completes, 1) },
	}

	// ONE shared callable, invoked from N goroutines.
	task := Task(func() (any, error) { return "ok", nil }, opts, pool)

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := task(); err != nil {
				t.Errorf("unexpected err: %v", err)
			}
		}()
	}
	wg.Wait()

	gotSubmits := atomic.LoadInt64(&submits)
	gotCompletes := atomic.LoadInt64(&completes)
	t.Logf("after: submits=%d completes=%d teeMismatch=%d retMismatch=%d (want submits=completes=%d, mismatches=0)",
		gotSubmits, gotCompletes, atomic.LoadInt64(&teeMismatch), atomic.LoadInt64(&retMismatch), N)

	// Conservation: every invocation submitted exactly once.
	if gotSubmits != N {
		t.Fatalf("lost/duplicated submission: got %d submits, want %d", gotSubmits, N)
	}
	// Every success invocation fired OnComplete exactly once.
	if gotCompletes != N {
		t.Fatalf("lost/duplicated OnComplete: got %d, want %d", gotCompletes, N)
	}
	// No Spec carried the wrong propagated options under concurrency.
	if m := atomic.LoadInt64(&teeMismatch); m != 0 {
		t.Fatalf("TEE not propagated on %d concurrent submissions", m)
	}
	if m := atomic.LoadInt64(&retMismatch); m != 0 {
		t.Fatalf("Retries not propagated on %d concurrent submissions", m)
	}
}

// ---------------------------------------------------------------------------
// (b) STATE TRANSITION: success/failure dispatch is mutually exclusive,
//     even under concurrency. OnComplete XOR OnError per invocation — never a
//     torn double-fire, never neither.
//
// Half the concurrent invocations succeed, half fail. Sink-side: total
// OnComplete == #successes, total OnError == #failures, and complete+error ==
// total invocations (no hook is dropped, none double-fires). Run under -race.
// ---------------------------------------------------------------------------
func TestAdversarial_HookExclusivity_UnderConcurrency(t *testing.T) {
	const N = 2000
	boom := errors.New("boom")

	var completes int64
	var errs int64
	var bothFired int64 // per-invocation guard: never both within one call

	var wg sync.WaitGroup
	wg.Add(N)
	wantComplete := 0
	wantError := 0
	for i := 0; i < N; i++ {
		fail := i%2 == 0
		if fail {
			wantError++
		} else {
			wantComplete++
		}
		go func(fail bool) {
			defer wg.Done()

			// Per-invocation observation of WHICH terminal hook fired.
			var localComplete, localError bool
			o := Options{
				OnComplete: func(Spec) { localComplete = true; atomic.AddInt64(&completes, 1) },
				OnError:    func(Spec, error) { localError = true; atomic.AddInt64(&errs, 1) },
			}

			fn := func() (any, error) {
				if fail {
					return nil, boom
				}
				return "ok", nil
			}
			pool := SubmitterFunc(func(s Spec) (any, error) { return fn() })

			task := Task(fn, o, pool)
			_, _ = task()

			// Torn-transition sink: a single invocation must fire EXACTLY one
			// terminal hook.
			if localComplete && localError {
				atomic.AddInt64(&bothFired, 1)
			}
		}(fail)
	}
	wg.Wait()

	gotC := atomic.LoadInt64(&completes)
	gotE := atomic.LoadInt64(&errs)
	t.Logf("after: completes=%d (want %d) errors=%d (want %d) both-fired-in-one-call=%d (want 0) sum=%d (want %d)",
		gotC, wantComplete, gotE, wantError, atomic.LoadInt64(&bothFired), gotC+gotE, N)

	if b := atomic.LoadInt64(&bothFired); b != 0 {
		t.Fatalf("torn transition: %d invocations fired BOTH OnComplete and OnError", b)
	}
	if gotC != int64(wantComplete) {
		t.Fatalf("OnComplete count: got %d want %d", gotC, wantComplete)
	}
	if gotE != int64(wantError) {
		t.Fatalf("OnError count: got %d want %d", gotE, wantError)
	}
	if gotC+gotE != N {
		t.Fatalf("dropped terminal hook: complete+error=%d want %d", gotC+gotE, N)
	}
}

// ---------------------------------------------------------------------------
// (c) IDENTITY / propagation integrity: concurrent tasks with DIFFERENT opts
//     sharing ONE Submitter must each carry their own opts onto the Spec their
//     hooks observe. No cross-invocation clobber.
//
// Sink-side: for every invocation i, the Spec observed by ITS OnSubmit hook
// must equal {TEE: i is odd, Retries: i}. Any clobber (a Spec carrying another
// invocation's opts) is recorded. Run under -race.
// ---------------------------------------------------------------------------
func TestAdversarial_NoCrossInvocationClobber(t *testing.T) {
	const N = 1500

	// One shared submitter for all invocations.
	pool := SubmitterFunc(func(s Spec) (any, error) { return nil, nil })

	var clobbers int64

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			wantTEE := i%2 == 1
			wantRet := i

			var observed Spec
			var seen bool
			opts := Options{
				TEE:      wantTEE,
				Retries:  wantRet,
				OnSubmit: func(s Spec) { observed = s; seen = true },
			}
			task := Task(func() (any, error) { return nil, nil }, opts, pool)
			if _, err := task(); err != nil {
				t.Errorf("i=%d err: %v", i, err)
			}

			// Sink: the Spec this invocation's hook saw must carry THIS
			// invocation's opts, not a neighbour's.
			if !seen || observed.TEE != wantTEE || observed.Retries != wantRet {
				atomic.AddInt64(&clobbers, 1)
			}
		}(i)
	}
	wg.Wait()

	got := atomic.LoadInt64(&clobbers)
	t.Logf("after: cross-invocation clobbers=%d (want 0) over N=%d", got, N)
	if got != 0 {
		t.Fatalf("identity violated: %d invocations observed a clobbered/foreign Spec", got)
	}
}

// ---------------------------------------------------------------------------
// (d) IDEMPOTENCY / determinism of a single callable.
//
//  d1: re-invoking ONE callable submits AGAIN each time (the decorator is not
//      memoized — contract is "invoking the callable submits the work"). A
//      memoizing regression would under-count submissions.
//  d2: within a SINGLE invocation the terminal hook fires EXACTLY once — no
//      double-complete double-count.
// ---------------------------------------------------------------------------
func TestAdversarial_ReinvokeSubmitsEachTime_NoDoubleComplete(t *testing.T) {
	const K = 50

	var submits int
	pool := SubmitterFunc(func(s Spec) (any, error) { submits++; return "r", nil })

	var completes int
	opts := Options{OnComplete: func(Spec) { completes++ }}
	task := Task(func() (any, error) { return "r", nil }, opts, pool)

	t.Logf("before: submits=%d completes=%d", submits, completes)
	for i := 0; i < K; i++ {
		if _, err := task(); err != nil {
			t.Fatalf("invoke %d: %v", i, err)
		}
	}
	t.Logf("after: submits=%d completes=%d (want both=%d)", submits, completes, K)

	// d1: each invocation submitted (not memoized).
	if submits != K {
		t.Fatalf("re-submit conservation: got %d submits over %d invocations, want %d", submits, K, K)
	}
	// d2: exactly one OnComplete per invocation — no double-complete.
	if completes != K {
		t.Fatalf("double/lost complete: got %d completes over %d invocations, want %d", completes, K, K)
	}
}

// ---------------------------------------------------------------------------
// (b'/sink, single-threaded determinism): explicit per-invocation proof that
// the failure path fires OnError and NOT OnComplete, and surfaces the error.
// Pins the exclusivity contract deterministically (complements the concurrent
// (b) probe). A regression that fires OnComplete on the error path is caught.
// ---------------------------------------------------------------------------
func TestAdversarial_ErrorPath_NoComplete_Sink(t *testing.T) {
	boom := errors.New("sink-boom")
	pool := SubmitterFunc(func(s Spec) (any, error) { return nil, boom })

	var fired []string
	opts := Options{
		OnSubmit:   func(Spec) { fired = append(fired, "submit") },
		OnStart:    func(Spec) { fired = append(fired, "start") },
		OnComplete: func(Spec) { fired = append(fired, "complete") },
		OnError:    func(Spec, error) { fired = append(fired, "error") },
	}
	task := Task(func() (any, error) { return nil, boom }, opts, pool)

	_, err := task()
	t.Logf("after: fired=%v err=%v", fired, err)

	if !errors.Is(err, boom) {
		t.Fatalf("error not surfaced: %v", err)
	}
	want := []string{"submit", "start", "error"}
	if fmt.Sprint(fired) != fmt.Sprint(want) {
		t.Fatalf("error-path hook set: got %v want %v", fired, want)
	}
}
