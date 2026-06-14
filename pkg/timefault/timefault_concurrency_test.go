package timefault

// Adversarial -race + contract-discrimination suite for pkg/timefault.
//
// ## Thread-safety contract (verbatim evidence)
//
// The package documentation is SILENT on the word "concurrent": a search of the
// whole package for concurren/thread/goroutine/mutex/sync finds nothing. There
// is therefore NO literal "not safe for concurrent use" disclaimer.
//
// However the read path DOES document a strict, single-sequence ordering
// contract. From Node.Read (timefault.go):
//
//	"Reads must be issued with non-decreasing base values to model a
//	 forward-advancing base clock."
//
// and the package model is documented as "deterministic, driven by explicit
// base ticks, never by the wall clock". The monotonicity machinery
// (Node.hasLast / lastBase / last) is meaningful ONLY under a single ordered
// read sequence per node: it remembers the previous read and flags a backward
// step. That state is mutated on EVERY read (EvaluateAt -> Read), and also by
// Inject/Reset.
//
// This places timefault in CASE 1 (honest single-sequence contract), NOT CASE 2
// (a silent type whose ROLE implies concurrent safety). The deciding evidence:
// a sync.Mutex would make memory access safe but would NOT make concurrent use
// *correct*, because the documented ordering contract requires non-decreasing
// base values within a node's read sequence. Two goroutines reading the same
// node interleave their base values out of order and corrupt the monotonicity
// history even with perfect mutual exclusion. A mutex would silence -race while
// leaving the model semantically broken under concurrency — a misleading fix.
// Therefore the correct deliverable is: (a) serialized correctness with
// mutation-verify, and (b) an opt-in race reproducer that proves concurrent use
// is OUT OF CONTRACT (the disclaimer proof), with NO mutex added.
//
// TestContract_MutexWouldNotFixDeterminism below demonstrates point (b)
// deterministically WITHOUT the race detector: even when every shared access is
// perfectly serialized by an external lock, the SAME node read at the SAME base
// yields a DIFFERENT monotonicity verdict depending on prior-read HISTORY.
// Concurrency randomises that history (whichever goroutine ran last wins the
// last/lastBase slot), so it destroys the documented deterministic guarantee —
// proving the contract is a sequential, history-dependent ordering contract, not
// merely memory-safety, and that no mutex inside the package could repair it.
// (Note: the detector defensively guards monotonicity with `base >= prevBase`,
// so a decreasing base does NOT fabricate a violation; the real failure mode is
// loss of determinism, which is what this test pins down.)

import (
	"os"
	"sync"
	"testing"
)

const concRunUUID = "timefault-conc-run-9b2e41d7-HXC-1245"

// TestContract_OptInRaceReproducer is the CASE 1 disclaimer proof: it shows that
// concurrent EvaluateAt + Inject on the same node is a genuine data race on the
// per-Node monotonicity fields, i.e. concurrent use is out of contract. It is
// OPT-IN (TIMEFAULT_RACE_REPRO=1) so the normal -race suite stays green; running
// it under `go test -race` reproduces the WARNING: DATA RACE deterministically.
//
// This is intentionally NOT a bug to fix: the package's documented read ordering
// contract is single-sequence (see file header). We do not add a mutex; we prove
// the disclaimer instead.
func TestContract_OptInRaceReproducer(t *testing.T) {
	if os.Getenv("TIMEFAULT_RACE_REPRO") != "1" {
		t.Skip("opt-in race reproducer; set TIMEFAULT_RACE_REPRO=1 and run with -race to reproduce the out-of-contract data race")
	}
	c := NewCluster(30, "a")
	n := c.Node("a")
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.Detector.EvaluateAt(n, 100) }()
		go func() { defer wg.Done(); _ = c.Inject("a", SkewFault{Delta: 5}) }()
	}
	wg.Wait()
}

// TestContract_MutexWouldNotFixDeterminism proves — WITHOUT the race detector,
// fully deterministically — that the contract is a sequential, HISTORY-DEPENDENT
// ordering contract, not merely memory safety. We serialize every access through
// an external mutex (the strongest possible in-package lock) and show that the
// SAME node read at the SAME base tick yields a DIFFERENT verdict depending on
// the prior-read history. Concurrency makes that prior-read history
// nondeterministic (whichever goroutine ran last wins the last/lastBase slot),
// so it destroys the package's documented "deterministic, driven by explicit
// base ticks" guarantee even with perfect mutual exclusion. Hence a mutex cannot
// restore correctness: CASE 1 (no mutex), not CASE 2 (add a mutex).
//
// Mutation: if EvaluateAt did not carry per-read history (last/lastBase), the
// monotonicity verdict could not depend on prior reads and the two verdicts
// below would be identical, so this assertion is load-bearing on the
// history-dependent sequential contract.
func TestContract_MutexWouldNotFixDeterminism(t *testing.T) {
	const at, jump, tol = 200, 100, 5

	// History A: the immediately-preceding read was BELOW the jump boundary, so
	// the read at `at` is the first to step backward => violation detected.
	cA := NewCluster(tol, "m")
	mA := cA.Node("m")
	if err := cA.Inject("m", MonotonicViolationFault{At: at, Jump: jump}); err != nil {
		t.Fatalf("inject A: %v", err)
	}
	var mu sync.Mutex
	readA := func(base int64) Verdict { mu.Lock(); defer mu.Unlock(); return cA.Detector.EvaluateAt(mA, base) }
	_ = readA(at - 1) // history just below the boundary
	vWithHistory := readA(at)

	// History B: the SAME fault, SAME node type, SAME base `at`, but the prior
	// read was ABOVE the boundary (already-jumped value), so the read at `at`
	// does NOT observe a backward step => no violation. Same base, opposite
	// verdict, purely because the prior read differed.
	cB := NewCluster(tol, "m")
	mB := cB.Node("m")
	if err := cB.Inject("m", MonotonicViolationFault{At: at, Jump: jump}); err != nil {
		t.Fatalf("inject B: %v", err)
	}
	readB := func(base int64) Verdict { mu.Lock(); defer mu.Unlock(); return cB.Detector.EvaluateAt(mB, base) }
	_ = readB(at + 1) // history already past the jump (reported = at+1-jump)
	vDiffHistory := readB(at)

	if !vWithHistory.Monotonicity {
		t.Fatalf("history-below-boundary read at base=%d should detect the backward step: %+v", at, vWithHistory)
	}
	if vDiffHistory.Monotonicity {
		t.Fatalf("history-above-boundary read at base=%d should NOT see a backward step: %+v", at, vDiffHistory)
	}
	if vWithHistory.Reported != vDiffHistory.Reported {
		t.Fatalf("sanity: both reads at base=%d must report the same value %d/%d", at, vWithHistory.Reported, vDiffHistory.Reported)
	}
	t.Logf("[%s] SAME node, SAME base=%d, SAME reported=%d -> verdict depends on prior-read HISTORY (mono=%v vs %v). Concurrency randomises that history, breaking the deterministic contract; a mutex (used here) cannot fix it => CASE 1, no mutex.",
		concRunUUID, at, vWithHistory.Reported, vWithHistory.Monotonicity, vDiffHistory.Monotonicity)
}

// TestSerialized_SkewReflectedInTimeQuery is the always-required correctness
// proof: an injected skew/offset is reflected in the node's time query, and
// clearing it restores base tracking. Runs clean under -race because it is
// single-sequence (the contract).
//
// Mutation: if Inject ignored the fault (left noFault), Read(base) would return
// base and the want=base+delta assertion would fail; if Reset did not clear the
// fault, the post-reset Read would still carry the offset and fail.
func TestSerialized_SkewReflectedInTimeQuery(t *testing.T) {
	const base = 1000
	const delta = -600
	c := NewCluster(30, "n")
	n := c.Node("n")

	// Healthy: reads base exactly.
	if got := n.Read(base); got != base {
		t.Fatalf("healthy node Read(%d)=%d, want %d", base, got, base)
	}

	// Injected skew is reflected in the time query.
	if err := c.Inject("n", SkewFault{Delta: delta}); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got, want := n.Read(base), int64(base+delta); got != want {
		t.Fatalf("skewed Read(%d)=%d, want base+delta=%d", base, got, want)
	}
	t.Logf("[%s] skew reflected: Read(%d)=%d (base%+d)", concRunUUID, base, base+delta, delta)

	// Clearing the fault restores exact base tracking (re-sync).
	if err := c.Reset("n"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := n.Read(base); got != base {
		t.Fatalf("after reset Read(%d)=%d, want re-synced %d", base, got, base)
	}
	t.Logf("[%s] reset restored base tracking: Read(%d)=%d", concRunUUID, base, base)
}

// TestSerialized_FreezeAndMonotonicModels exercises the freeze and
// monotonic-violation models per their documented behaviour and proves clearing
// restores tracking. Single-sequence by contract; clean under -race.
//
// Mutation: if FreezeFault returned base instead of min(base,At) it would not
// fall behind and the lag assertion fails; if MonotonicViolationFault never
// jumped, the backward-step assertion fails.
func TestSerialized_FreezeAndMonotonicModels(t *testing.T) {
	// Freeze: sticks at At, falls behind as base advances.
	{
		const at = 100
		c := NewCluster(1000, "f")
		n := c.Node("f")
		if err := c.Inject("f", FreezeFault{At: at}); err != nil {
			t.Fatalf("inject freeze: %v", err)
		}
		if got := n.Read(at); got != at { // at the instant, equal
			t.Fatalf("freeze Read(%d)=%d, want %d", at, got, at)
		}
		if got := n.Read(at + 500); got != at { // later, stuck at At => behind
			t.Fatalf("freeze Read(%d)=%d, want stuck at %d", at+500, got, at)
		}
		c.Reset("f")
		if got := n.Read(at + 500); got != at+500 {
			t.Fatalf("after reset freeze should track base: Read(%d)=%d", at+500, got)
		}
	}

	// Monotonic violation: one backward step at At, then advances; clearing
	// removes it.
	{
		const at, jump = 200, 100
		c := NewCluster(5, "m")
		n := c.Node("m")
		if err := c.Inject("m", MonotonicViolationFault{At: at, Jump: jump}); err != nil {
			t.Fatalf("inject mono: %v", err)
		}
		vPre := c.Detector.EvaluateAt(n, at-1)
		vAt := c.Detector.EvaluateAt(n, at)
		if !vAt.Monotonicity {
			t.Fatalf("expected monotonicity violation at base=%d: %+v", at, vAt)
		}
		if vAt.Reported >= vPre.Reported {
			t.Fatalf("expected backward step: %d should be < %d", vAt.Reported, vPre.Reported)
		}
		// After clearing, a fresh read sequence shows no violation (re-synced).
		c.Reset("m")
		_ = c.Detector.EvaluateAt(n, at+1)
		vClear := c.Detector.EvaluateAt(n, at+2)
		if vClear.Monotonicity || !vClear.Consistent {
			t.Fatalf("after reset node must be clean: %+v", vClear)
		}
		t.Logf("[%s] mono model + clear restore verified", concRunUUID)
	}
}

// TestSerialized_ConcurrentReadsDistinctNodesAreSafe proves the LEGITIMATE
// concurrency the design supports: distinct nodes are independent objects, so
// reading DIFFERENT nodes concurrently touches disjoint state and is race-free.
// This both documents the real safe-usage envelope and runs clean under -race
// (-count is irrelevant to the per-node mutation race, which is excluded here by
// construction: one goroutine per node, no shared node).
//
// Mutation: if Cluster.nodes were rebuilt/mutated at read time (it is build-once
// in NewCluster), this would race; the green result confirms the map is
// read-only after construction (CASE 3 for the map), with per-node state the
// only sequential resource.
func TestSerialized_ConcurrentReadsDistinctNodesAreSafe(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	c := NewCluster(30, ids...)
	// Give each node a distinct, stable skew so we can assert independence.
	for i, id := range ids {
		if err := c.Inject(id, SkewFault{Delta: int64(i)}); err != nil {
			t.Fatalf("inject %s: %v", id, err)
		}
	}
	const base = 500
	results := make([]int64, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			// Each goroutine owns exactly one node: disjoint state.
			results[i] = c.Node(id).Read(base)
		}(i, id)
	}
	wg.Wait()
	for i := range ids {
		if want := int64(base + i); results[i] != want {
			t.Fatalf("node %s concurrent Read=%d, want base+%d=%d", ids[i], results[i], i, want)
		}
	}
	t.Logf("[%s] concurrent reads of DISTINCT nodes are race-free and correct: %v", concRunUUID, results)
}
