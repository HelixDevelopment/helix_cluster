package porcupine

import "testing"

// Adversarial audit of the WGL linearizability checker (package.go).
//
// A linearizability checker is itself a correctness ORACLE: the dominant and
// most dangerous failure is the FALSE NEGATIVE — it returns OK=true (PASS) for a
// history that is NOT linearizable, rubber-stamping a broken system. These tests
// hand-construct histories that are PROVABLY non-linearizable for a simple,
// well-understood model and assert the checker REJECTS each. The accept-side
// baseline (clearly-linearizable histories accepted) guards the inverse error
// and ensures an "always reject" mutation is caught.
//
// Models used (all sequential specs, immutable Step per the Model contract):
//   * register   — single int cell; read legal iff it returns the current value.
//   * counter    — monotonic int; the only legal op is inc returning the
//                   post-increment value; pins that a counter cannot go backwards
//                   and cannot skip/repeat a value.
//
// The register helpers w()/rd() and registerModel() are defined in
// package_test.go (same package).

// ----------------------------------------------------------------------------
// Counter model: state is an int n. The single operation is "increment", whose
// Output is the value AFTER the increment. Legal iff output == n+1; new state is
// output. This makes "counter went backwards", "counter skipped a value", and
// "counter repeated a value" all non-linearizable.
// ----------------------------------------------------------------------------

func counterModel() Model {
	return Model{
		Init: func() interface{} { return 0 },
		Step: func(state, input, output interface{}) (bool, interface{}) {
			n := state.(int)
			got := output.(int)
			if got == n+1 {
				return true, got
			}
			return false, n
		},
		Equal: func(s1, s2 interface{}) bool { return s1.(int) == s2.(int) },
	}
}

func inc(client, got int, call, ret int64) Operation {
	return Operation{ClientID: client, Input: "inc", Output: got, Call: call, Return: ret}
}

// ===========================================================================
// ACCEPT side — baseline that an "always reject" mutation must break.
// ===========================================================================

// A single operation in isolation is trivially linearizable.
func TestAdv_SingleOp_Accepts(t *testing.T) {
	model := registerModel()
	if !CheckOperations(model, []Operation{w(0, 5, 1, 2)}) {
		t.Fatalf("single write must be linearizable")
	}
	if !CheckOperations(model, []Operation{rd(0, 0, 1, 2)}) {
		t.Fatalf("single read of the initial value 0 must be linearizable")
	}
}

// All-reads of the initial value, heavily overlapping, is linearizable.
func TestAdv_AllReadsInitial_Accepts(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		rd(0, 0, 1, 10),
		rd(1, 0, 2, 9),
		rd(2, 0, 3, 8),
	}
	if !CheckOperations(model, hist) {
		t.Fatalf("concurrent reads all returning the initial value must be linearizable")
	}
}

// Concurrent-but-valid: a write overlaps two reads; the reads disagree (one sees
// old, one sees new), which is legal because the write's linearization point can
// fall between them. This is the canonical "has a valid linearization point" case.
func TestAdv_ConcurrentValidLinPoint_Accepts(t *testing.T) {
	model := registerModel()
	// W(1) spans [1..6]. R->0 [2..3] (before W's lin point), R->1 [4..5] (after).
	hist := []Operation{
		w(0, 1, 1, 6),
		rd(1, 0, 2, 3),
		rd(2, 1, 4, 5),
	}
	if !CheckOperations(model, hist) {
		t.Fatalf("write with a valid mid-window lin point must be linearizable")
	}
}

// A correct, strictly increasing counter history (sequential) is linearizable.
func TestAdv_CounterSequential_Accepts(t *testing.T) {
	model := counterModel()
	hist := []Operation{
		inc(0, 1, 1, 2),
		inc(0, 2, 3, 4),
		inc(0, 3, 5, 6),
	}
	if !CheckOperations(model, hist) {
		t.Fatalf("monotonic counter 1,2,3 must be linearizable")
	}
}

// Concurrent counter increments: two overlapping incs returning 1 and 2 in
// either real-time-permitted order are linearizable (the lin points order them).
func TestAdv_CounterConcurrent_Accepts(t *testing.T) {
	model := counterModel()
	hist := []Operation{
		inc(0, 1, 1, 4),
		inc(1, 2, 2, 5),
	}
	if !CheckOperations(model, hist) {
		t.Fatalf("two overlapping increments returning 1 then 2 must be linearizable")
	}
}

// ===========================================================================
// REJECT side — the CENTERPIECE: provably non-linearizable histories that the
// checker MUST reject. A PASS here is a false negative (PASS-bluff) and a
// serious correctness bug in the oracle.
// ===========================================================================

// (a) Read of a never-written value. The system claims a read returned 99 but no
// write of 99 exists. No sequential register run produces 99.
func TestAdv_Reject_ReadNeverWritten(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 1, 1, 2),
		w(0, 2, 3, 4),
		rd(1, 99, 5, 6), // 99 never written
	}
	assertNonLinearizable(t, model, hist)
}

// (b) Stale read sandwiched AFTER a completed write, where the read fully
// follows the write in real time. There is no concurrent write to explain the
// stale value. This is the classic "lost update visible" violation.
//
// W(1) [1..2] completes; R->0 [3..4] starts strictly after. The only writes are
// W(1); after it the value is 1, so a read returning 0 that starts at t=3 (after
// W(1) returned at t=2) cannot be linearized.
func TestAdv_Reject_StaleAfterCompletedWrite(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 1, 1, 2),
		rd(1, 0, 3, 4),
	}
	assertNonLinearizable(t, model, hist)
}

// (c) Stale read across a SECOND write — the dangerous case a naive checker that
// only looks pairwise can miss. W(1) [1..2], W(2) [3..4], then R->1 [5..6].
// After W(2) the value is 2; the read starts strictly after W(2) returned, so it
// must see 2, not 1. Returning 1 is non-linearizable. A checker that lets the
// read "float" back before W(2) without respecting the real-time edge would
// wrongly accept.
func TestAdv_Reject_StaleAcrossSecondWrite(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 1, 1, 2),
		w(0, 2, 3, 4),
		rd(1, 1, 5, 6), // must observe 2 (W(2) fully precedes this read)
	}
	assertNonLinearizable(t, model, hist)
}

// (d) Read-before-write in real time: R->1 [1..2] returns 1, but the only W(1)
// happens at [3..4], strictly AFTER the read returned. A read cannot observe a
// value that no write has produced yet.
func TestAdv_Reject_ReadBeforeWrite(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		rd(0, 1, 1, 2), // observes 1...
		w(1, 1, 3, 4),  // ...but W(1) only starts after the read returned
	}
	assertNonLinearizable(t, model, hist)
}

// (e) Two reads in the same client ordering observing impossible state churn.
// Sole write is W(1) [1..2]. Client reads R->1 [3..4] then R->0 [5..6]. Once the
// value is observed as 1 after the only write, no later read can legally return
// the pre-write 0 — there is no write of 0 to take it back. Non-linearizable.
func TestAdv_Reject_ValueRevertsWithNoWrite(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 1, 1, 2),
		rd(1, 1, 3, 4), // sees 1
		rd(1, 0, 5, 6), // then sees 0 again with no intervening write -> impossible
	}
	assertNonLinearizable(t, model, hist)
}

// (f) Counter goes backwards. Increments return 1 then 2 then 1 — the third inc
// reports a value lower than the second, impossible for a monotonic counter.
func TestAdv_Reject_CounterBackwards(t *testing.T) {
	model := counterModel()
	hist := []Operation{
		inc(0, 1, 1, 2),
		inc(0, 2, 3, 4),
		inc(0, 1, 5, 6), // backwards: a monotonic counter cannot return 1 again
	}
	assertNonLinearizable(t, model, hist)
}

// (g) Counter skips a value. 1 then 3 — value 2 is never returned, so the
// post-increment sequence is broken.
func TestAdv_Reject_CounterSkips(t *testing.T) {
	model := counterModel()
	hist := []Operation{
		inc(0, 1, 1, 2),
		inc(0, 3, 3, 4), // skipped 2
	}
	assertNonLinearizable(t, model, hist)
}

// (h) Counter repeats a value across two NON-overlapping increments. inc->1
// [1..2] then inc->1 [3..4]: two sequential increments cannot both yield 1.
func TestAdv_Reject_CounterRepeats(t *testing.T) {
	model := counterModel()
	hist := []Operation{
		inc(0, 1, 1, 2),
		inc(1, 1, 3, 4),
	}
	assertNonLinearizable(t, model, hist)
}

// (i) Real-time-gate stress: a long chain of completed writes followed by a read
// that returns an EARLY value. W(1)[1..2] W(2)[3..4] W(3)[5..6] then R->1
// [7..8]. The read strictly follows W(3); it must see 3. This specifically
// exercises the minReturn real-time gate across multiple ordered ops — the exact
// branch (package.go:214) whose removal produces false negatives.
func TestAdv_Reject_ReadStaleAfterChain(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 1, 1, 2),
		w(0, 2, 3, 4),
		w(0, 3, 5, 6),
		rd(1, 1, 7, 8),
	}
	assertNonLinearizable(t, model, hist)
}

// ===========================================================================
// Witness sanity: on rejection the offending op is populated (a usable witness).
// ===========================================================================

func TestAdv_RejectReportsWitness(t *testing.T) {
	model := registerModel()
	hist := []Operation{
		w(0, 1, 1, 2),
		rd(1, 0, 3, 4),
	}
	res := CheckOperationsVerbose(model, hist)
	if res.OK {
		t.Fatalf("expected non-linearizable")
	}
	// The witness must be a real op from the history (non-zero Return), so a
	// caller can point at something. We do not over-constrain WHICH op.
	if res.Offending.Return == 0 && res.Offending.Call == 0 {
		t.Fatalf("expected a populated witness operation, got zero value")
	}
}

// ===========================================================================
// Verdict parity for the new cases: memo on vs off must agree (correctness of
// the optimization is independent of these specific histories).
// ===========================================================================

func TestAdv_MemoParity_NewCases(t *testing.T) {
	regWith := registerModel()
	regNo := registerModel()
	regNo.Equal = nil
	cntWith := counterModel()
	cntNo := counterModel()
	cntNo.Equal = nil

	type tc struct {
		name   string
		model  Model
		modelN Model
		hist   []Operation
		wantOK bool
	}
	cases := []tc{
		{"stale-across-second-write", regWith, regNo, []Operation{w(0, 1, 1, 2), w(0, 2, 3, 4), rd(1, 1, 5, 6)}, false},
		{"read-before-write", regWith, regNo, []Operation{rd(0, 1, 1, 2), w(1, 1, 3, 4)}, false},
		{"value-reverts", regWith, regNo, []Operation{w(0, 1, 1, 2), rd(1, 1, 3, 4), rd(1, 0, 5, 6)}, false},
		{"concurrent-valid", regWith, regNo, []Operation{w(0, 1, 1, 6), rd(1, 0, 2, 3), rd(2, 1, 4, 5)}, true},
		{"counter-backwards", cntWith, cntNo, []Operation{inc(0, 1, 1, 2), inc(0, 2, 3, 4), inc(0, 1, 5, 6)}, false},
		{"counter-concurrent-ok", cntWith, cntNo, []Operation{inc(0, 1, 1, 4), inc(1, 2, 2, 5)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rWith := CheckOperationsVerbose(c.model, c.hist)
			rNo := CheckOperationsVerbose(c.modelN, c.hist)
			if rWith.OK != c.wantOK {
				t.Fatalf("with-memo verdict=%v, want %v", rWith.OK, c.wantOK)
			}
			if rNo.OK != c.wantOK {
				t.Fatalf("no-memo verdict=%v, want %v", rNo.OK, c.wantOK)
			}
			if rWith.OK != rNo.OK {
				t.Fatalf("memo changed verdict: with=%v no=%v", rWith.OK, rNo.OK)
			}
		})
	}
}

// assertNonLinearizable fails the test loudly if the checker ACCEPTS a history
// that must be rejected — capturing the exact false-negative for the report.
func assertNonLinearizable(t *testing.T, model Model, hist []Operation) {
	t.Helper()
	res := CheckOperationsVerbose(model, hist)
	if res.OK {
		t.Fatalf("FALSE NEGATIVE: checker ACCEPTED a non-linearizable history (returned OK=true). history=%+v", hist)
	}
	if CheckOperations(model, hist) {
		t.Fatalf("FALSE NEGATIVE: boolean wrapper ACCEPTED a non-linearizable history. history=%+v", hist)
	}
}
