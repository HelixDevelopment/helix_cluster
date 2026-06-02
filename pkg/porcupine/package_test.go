package porcupine

import (
	"sync"
	"testing"
)

// --- A concrete sequential specification: a single-value register. ---
//
// Input is a regOp; for a write the Output is ignored, for a read the Output is
// the value the real system claimed to return. The register's state is the int
// it currently holds.

type regKind int

const (
	regWrite regKind = iota
	regRead
)

type regOp struct {
	kind regKind
	val  int // for write: value written; for read: unused in input.
}

// registerModel returns a Model for an integer register initialized to 0.
func registerModel() Model {
	return Model{
		Init: func() interface{} { return 0 },
		Step: func(state, input, output interface{}) (bool, interface{}) {
			st := state.(int)
			in := input.(regOp)
			switch in.kind {
			case regWrite:
				// A write always succeeds and sets the value.
				return true, in.val
			case regRead:
				// A read is legal only if the value it returned matches state.
				got := output.(int)
				return got == st, st
			}
			return false, st
		},
		Equal: func(s1, s2 interface{}) bool { return s1.(int) == s2.(int) },
	}
}

func w(client, val int, call, ret int64) Operation {
	return Operation{ClientID: client, Input: regOp{kind: regWrite, val: val}, Output: nil, Call: call, Return: ret}
}
func rd(client, got int, call, ret int64) Operation {
	return Operation{ClientID: client, Input: regOp{kind: regRead}, Output: got, Call: call, Return: ret}
}

// Closure #1: a KNOWN-LINEARIZABLE concurrent history must PASS.
func TestCheckOperations_Linearizable(t *testing.T) {
	model := registerModel()

	// W(1) [1..4] overlaps R->1 [2..3]; then R->1 [5..6]. There is a valid
	// linearization: W(1) then both reads observe 1.
	hist := []Operation{
		w(0, 1, 1, 4),
		rd(1, 1, 2, 3),
		rd(1, 1, 5, 6),
	}
	res := CheckOperationsVerbose(model, hist)
	if !res.OK {
		t.Fatalf("expected linearizable, got NOT linearizable (offending=%+v)", res.Offending)
	}
	if !CheckOperations(model, hist) {
		t.Fatalf("boolean wrapper disagreed with verbose result")
	}
}

// A non-overlapping (sequential) correct history is the simplest PASS — and it
// also pins the real-time ordering: the late write must be applied after the
// early read.
func TestCheckOperations_SequentialOrdering(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 7, 1, 2),  // write 7
		rd(0, 7, 3, 4), // read 7
		w(0, 9, 5, 6),  // write 9
		rd(0, 9, 7, 8), // read 9
	}
	if !CheckOperations(model, hist) {
		t.Fatalf("expected sequential history to be linearizable")
	}
}

// Closure #2 + mutation guard: a DELIBERATELY NON-LINEARIZABLE history must
// FAIL and name a violating operation.
//
// W(1) completes in [1..2]. R returns 0 in [3..4], i.e. AFTER the write fully
// returned. No interleaving lets a read that starts after W(1) returned observe
// the pre-write value 0. So this is non-linearizable.
//
// MUTATION GUARD: if the real-time gate `if ops[i].Call > minReturn { continue }`
// were removed (always-true / no backtracking pruning), the checker could place
// the read before the write and wrongly accept => this test would fail. Thus the
// branch is load-bearing.
func TestCheckOperations_StaleReadFails(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 1, 1, 2),  // write 1, fully returned at t=2
		rd(1, 0, 3, 4), // read observes stale 0, started at t=3 > 2
	}
	res := CheckOperationsVerbose(model, hist)
	if res.OK {
		t.Fatalf("expected NON-linearizable, but checker returned linearizable")
	}
	// The witness must be the stale read.
	in, ok := res.Offending.Input.(regOp)
	if !ok || in.kind != regRead {
		t.Fatalf("expected offending op to be the stale read, got %+v", res.Offending)
	}
	if res.Offending.Output.(int) != 0 {
		t.Fatalf("expected offending read output 0, got %v", res.Offending.Output)
	}
}

// A second, independent non-linearizable case: a read returns a value that was
// never written at all. Guards against the model accepting arbitrary outputs.
func TestCheckOperations_ImpossibleValueFails(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 1, 1, 2),
		rd(1, 42, 1, 6), // 42 was never written
	}
	if CheckOperations(model, hist) {
		t.Fatalf("expected NON-linearizable (read of never-written value 42)")
	}
}

// Empty history is vacuously linearizable.
func TestCheckOperations_Empty(t *testing.T) {
	if !CheckOperations(registerModel(), nil) {
		t.Fatalf("empty history should be linearizable")
	}
}

// Closure #3: the recorder shim, driven by concurrent goroutines under -race,
// produces a well-formed history that the checker accepts for a correct
// register. The "real system" here is an in-process mutex-guarded register —
// genuinely concurrent goroutines, real shared state, real locking — not a mock.
func TestRecorder_ConcurrentRealRegister(t *testing.T) {
	rec := NewRecorder()

	// The real, correct, linearizable register implementation under test.
	var mu sync.Mutex
	value := 0

	const writers = 4
	const readsPerWriter = 25

	var wg sync.WaitGroup
	for c := 0; c < writers; c++ {
		wg.Add(1)
		go func(client int) {
			defer wg.Done()
			for i := 0; i < readsPerWriter; i++ {
				// A write op recorded end-to-end.
				wv := client*1000 + i
				tok := rec.Begin(client, regOp{kind: regWrite, val: wv})
				mu.Lock()
				value = wv
				mu.Unlock()
				rec.End(tok, nil)

				// A read op recorded end-to-end, observing the real value.
				rtok := rec.Begin(client, regOp{kind: regRead})
				mu.Lock()
				got := value
				mu.Unlock()
				rec.End(rtok, got)
			}
		}(c)
	}
	wg.Wait()

	hist := rec.History()
	wantOps := writers * readsPerWriter * 2
	if len(hist) != wantOps {
		t.Fatalf("recorder produced %d ops, want %d", len(hist), wantOps)
	}
	// Well-formedness: every op has Call < Return and a finalized Output slot.
	for _, op := range hist {
		if op.Call >= op.Return {
			t.Fatalf("ill-formed op (Call >= Return): %+v", op)
		}
	}
	// A linearizable real register must pass the checker on the recorded history.
	res := CheckOperationsVerbose(registerModel(), hist)
	if !res.OK {
		t.Fatalf("recorded concurrent history of a correct register is NOT linearizable (offending=%+v)", res.Offending)
	}
}

// End with an unknown token is a safe no-op (defensive; recorder robustness).
func TestRecorder_EndUnknownToken(t *testing.T) {
	rec := NewRecorder()
	rec.End(99999, nil) // must not panic
	if len(rec.History()) != 0 {
		t.Fatalf("unknown-token End should not add an op")
	}
}

// Tokens never collide and the logical clock is strictly monotonic per op so
// concurrently-begun ops still get Call < Return.
func TestRecorder_MonotonicClock(t *testing.T) {
	rec := NewRecorder()
	t1 := rec.Begin(0, regOp{kind: regWrite, val: 1})
	t2 := rec.Begin(1, regOp{kind: regWrite, val: 2})
	if t1 == t2 {
		t.Fatalf("tokens collided: %d", t1)
	}
	rec.End(t1, nil)
	rec.End(t2, nil)
	for _, op := range rec.History() {
		if op.Call >= op.Return {
			t.Fatalf("clock not monotonic for op %+v", op)
		}
	}
}
