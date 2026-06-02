package helixtask

import (
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
)

// runUUID returns a random hex id for before/after run tracing.
func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return fmt.Sprintf("%x", b[:])
}

// recordingPool captures the Spec it was asked to submit and returns a
// programmable result/error. It stands in for the out-of-scope TEE/remote pool.
type recordingPool struct {
	got       Spec
	called    bool
	retResult any
	retErr    error
}

func (p *recordingPool) Submit(spec Spec) (any, error) {
	p.called = true
	p.got = spec
	return p.retResult, p.retErr
}

// Clause 1: TEE=true task submits to the pool with the TEE flag set, hooks fire
// in order OnSubmit -> OnStart -> OnComplete, and the function result returns.
//
// Mutation: if Task fails to propagate opts.TEE onto the Spec, the
// `submitted with TEE` assertion fails. If OnComplete is dropped or reordered,
// the order assertion fails.
func TestDecorate_TEE_SubmitsAndCompletesInOrder(t *testing.T) {
	id := runUUID(t)
	pool := &recordingPool{retResult: "OUT-42"}

	var order []string
	opts := Options{
		TEE:        true,
		Retries:    3,
		OnSubmit:   func(Spec) { order = append(order, "submit") },
		OnStart:    func(Spec) { order = append(order, "start") },
		OnComplete: func(Spec) { order = append(order, "complete") },
		OnError:    func(Spec, error) { order = append(order, "error") },
	}
	fnResult := "OUT-42"
	task := Task(func() (any, error) { return fnResult, nil }, opts, pool)

	// before: nothing submitted, no hooks fired.
	t.Logf("run=%s before: pool.called=%v order=%v", id, pool.called, order)
	if pool.called {
		t.Fatalf("run=%s pool submitted before invocation", id)
	}
	if len(order) != 0 {
		t.Fatalf("run=%s hooks fired before invocation: %v", id, order)
	}

	got, err := task()

	// after: submitted with TEE, hooks in order, result surfaced.
	t.Logf("run=%s after: pool.called=%v spec.TEE=%v order=%v result=%v err=%v",
		id, pool.called, pool.got.TEE, order, got, err)

	if err != nil {
		t.Fatalf("run=%s unexpected err: %v", id, err)
	}
	if !pool.called {
		t.Fatalf("run=%s task was not submitted to the pool", id)
	}
	if !pool.got.TEE {
		t.Fatalf("run=%s TEE flag not propagated to submission: %+v", id, pool.got)
	}
	if got != fnResult {
		t.Fatalf("run=%s result not surfaced: got %v want %v", id, got, fnResult)
	}
	want := []string{"submit", "start", "complete"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("run=%s hook order: got %v want %v", id, order, want)
	}
	// OnError must not have run on the success path.
	for _, o := range order {
		if o == "error" {
			t.Fatalf("run=%s OnError fired on success path: %v", id, order)
		}
	}
}

// Clause 2: on function error, OnError fires (not OnComplete) and the error is
// surfaced.
//
// Mutation: if the implementation fires OnComplete on the error path, the
// `complete must not fire` assertion fails; the surfaced-error check fails if
// the error is swallowed.
func TestDecorate_Error_FiresOnErrorNotComplete(t *testing.T) {
	id := runUUID(t)
	boom := errors.New("work-failed")
	pool := &recordingPool{retErr: boom}

	var order []string
	var sawErr error
	opts := Options{
		TEE:        true,
		OnSubmit:   func(Spec) { order = append(order, "submit") },
		OnStart:    func(Spec) { order = append(order, "start") },
		OnComplete: func(Spec) { order = append(order, "complete") },
		OnError:    func(_ Spec, e error) { order = append(order, "error"); sawErr = e },
	}
	task := Task(func() (any, error) { return nil, boom }, opts, pool)

	t.Logf("run=%s before: order=%v", id, order)

	_, err := task()

	t.Logf("run=%s after: order=%v surfacedErr=%v hookErr=%v", id, order, err, sawErr)

	if !errors.Is(err, boom) {
		t.Fatalf("run=%s error not surfaced: got %v want %v", id, err, boom)
	}
	for _, o := range order {
		if o == "complete" {
			t.Fatalf("run=%s OnComplete fired on error path: %v", id, order)
		}
	}
	want := []string{"submit", "start", "error"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("run=%s hook order on error: got %v want %v", id, order, want)
	}
	if !errors.Is(sawErr, boom) {
		t.Fatalf("run=%s OnError received wrong error: got %v want %v", id, sawErr, boom)
	}
}

// Clause 3: options (TEE, Retries) propagate to the submission exactly, and the
// same Spec is echoed to every hook.
//
// Mutation: if Task hardcodes or drops a field (e.g. always TEE=false, or
// ignores Retries), the exact-equality assertions fail.
func TestOptions_PropagatedExactly(t *testing.T) {
	id := runUUID(t)

	cases := []struct {
		name    string
		tee     bool
		retries int
	}{
		{"tee_true_r5", true, 5},
		{"tee_false_r0", false, 0},
		{"tee_true_r0", true, 0},
		{"tee_false_r7", false, 7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := &recordingPool{retResult: 1}
			var hookSpecs []Spec
			opts := Options{
				TEE:        tc.tee,
				Retries:    tc.retries,
				OnSubmit:   func(s Spec) { hookSpecs = append(hookSpecs, s) },
				OnStart:    func(s Spec) { hookSpecs = append(hookSpecs, s) },
				OnComplete: func(s Spec) { hookSpecs = append(hookSpecs, s) },
			}
			task := Task(func() (any, error) { return 1, nil }, opts, pool)

			t.Logf("run=%s/%s before: pool.called=%v", id, tc.name, pool.called)
			if _, err := task(); err != nil {
				t.Fatalf("run=%s/%s err: %v", id, tc.name, err)
			}
			t.Logf("run=%s/%s after: spec.TEE=%v spec.Retries=%v hookSpecs=%d",
				id, tc.name, pool.got.TEE, pool.got.Retries, len(hookSpecs))

			// Exact propagation onto the submission.
			if pool.got.TEE != tc.tee {
				t.Fatalf("run=%s/%s TEE: got %v want %v", id, tc.name, pool.got.TEE, tc.tee)
			}
			if pool.got.Retries != tc.retries {
				t.Fatalf("run=%s/%s Retries: got %v want %v", id, tc.name, pool.got.Retries, tc.retries)
			}
			// Every hook saw the same propagated options.
			if len(hookSpecs) != 3 {
				t.Fatalf("run=%s/%s expected 3 hook specs, got %d", id, tc.name, len(hookSpecs))
			}
			for i, s := range hookSpecs {
				if s.TEE != tc.tee || s.Retries != tc.retries {
					t.Fatalf("run=%s/%s hook[%d] spec mismatch: got {TEE:%v Retries:%v} want {TEE:%v Retries:%v}",
						id, tc.name, i, s.TEE, s.Retries, tc.tee, tc.retries)
				}
			}
		})
	}
}

// Guard: a nil pool surfaces ErrNilSubmitter without firing success hooks.
// Mutation: removing the nil-guard would panic instead of returning the error.
func TestNilSubmitter(t *testing.T) {
	id := runUUID(t)
	fired := false
	opts := Options{OnComplete: func(Spec) { fired = true }}
	task := Task(func() (any, error) { return nil, nil }, opts, nil)

	t.Logf("run=%s before: fired=%v", id, fired)
	_, err := task()
	t.Logf("run=%s after: err=%v fired=%v", id, err, fired)

	if !errors.Is(err, ErrNilSubmitter) {
		t.Fatalf("run=%s want ErrNilSubmitter, got %v", id, err)
	}
	if fired {
		t.Fatalf("run=%s OnComplete fired despite nil submitter", id)
	}
}

// SubmitterFunc adapter sanity: it forwards the Spec to the wrapped func.
func TestSubmitterFuncAdapter(t *testing.T) {
	id := runUUID(t)
	var seen Spec
	var sub Submitter = SubmitterFunc(func(s Spec) (any, error) { seen = s; return "ok", nil })
	task := Task(func() (any, error) { return "ok", nil }, Options{TEE: true, Retries: 2}, sub)

	got, err := task()
	t.Logf("run=%s after: got=%v err=%v seen={TEE:%v Retries:%v}", id, got, err, seen.TEE, seen.Retries)
	if err != nil || got != "ok" {
		t.Fatalf("run=%s adapter result: got %v err %v", id, got, err)
	}
	if !seen.TEE || seen.Retries != 2 {
		t.Fatalf("run=%s adapter did not receive propagated spec: %+v", id, seen)
	}
}
