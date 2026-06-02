package timefault

import "testing"

// runUUID anchors all observable log lines to one deterministic run.
const runUUID = "timefault-run-7f3c9a21-HXC-1245"

// TestSkewInjectionDetectionAndReset covers CLOSURE clause 1:
// before injection all nodes are synced and none flagged; after a -600s SKEW on
// a subset, the skewed nodes are flagged INCONSISTENT and denied a lease while
// the host/base and unskewed nodes remain fine; after RESET the skewed nodes
// re-sync and are granted leases again.
//
// Mutation: if the detector ignores skew (always-consistent), the skewed nodes
// would be granted a lease at base=1000 and Inconsistent() would be empty —
// both assertions below would fail.
func TestSkewInjectionDetectionAndReset(t *testing.T) {
	const tol = 30
	const base = 1000
	c := NewCluster(tol, "n1", "n2", "n3", "n4")
	t.Logf("[%s] BEFORE skew: base=%d tol=%ds nodes=%v", runUUID, base, tol, c.IDs())

	// BEFORE: everyone synced, detector flags none, all leases grantable.
	if got := c.Inconsistent(base); len(got) != 0 {
		t.Fatalf("BEFORE: expected no inconsistent nodes, got %v", got)
	}
	for _, id := range c.IDs() {
		if d := c.Detector.GrantLease(c.Node(id), base); !d.Granted {
			t.Fatalf("BEFORE: node %s should be granted a lease, got %q", id, d.Reason)
		}
	}

	// Inject -600s SKEW on a subset (n2, n4). Host/base clock unaffected.
	const delta = -600
	for _, id := range []string{"n2", "n4"} {
		if err := c.Inject(id, SkewFault{Delta: delta}); err != nil {
			t.Fatalf("inject %s: %v", id, err)
		}
	}
	t.Logf("[%s] injected SKEW delta=%ds on n2,n4", runUUID, delta)

	// Base reference must be unaffected: an unskewed node still reads exactly base.
	if got := c.Node("n1").fault.ReportedTime(base); got != base {
		t.Fatalf("base clock corrupted: unskewed n1 reads %d, want %d", got, base)
	}

	// AFTER: skewed nodes flagged + denied; healthy nodes still fine.
	gotBad := c.Inconsistent(base)
	wantBad := map[string]bool{"n2": true, "n4": true}
	if len(gotBad) != len(wantBad) {
		t.Fatalf("AFTER: inconsistent set = %v, want keys %v", gotBad, wantBad)
	}
	for _, id := range gotBad {
		if !wantBad[id] {
			t.Fatalf("AFTER: unexpected inconsistent node %s (set=%v)", id, gotBad)
		}
	}
	for _, id := range []string{"n2", "n4"} {
		d := c.Detector.GrantLease(c.Node(id), base)
		if d.Granted {
			t.Fatalf("AFTER: skewed node %s must be DENIED a lease (split-brain)", id)
		}
		t.Logf("[%s] AFTER skew: node %s denied lease reason=%q", runUUID, id, d.Reason)
	}
	for _, id := range []string{"n1", "n3"} {
		if d := c.Detector.GrantLease(c.Node(id), base); !d.Granted {
			t.Fatalf("AFTER: healthy node %s should still be granted, got %q", id, d.Reason)
		}
	}

	// RESET: skewed nodes re-sync and become grantable again.
	for _, id := range []string{"n2", "n4"} {
		if err := c.Reset(id); err != nil {
			t.Fatalf("reset %s: %v", id, err)
		}
	}
	if got := c.Inconsistent(base); len(got) != 0 {
		t.Fatalf("AFTER RESET: expected no inconsistent nodes, got %v", got)
	}
	for _, id := range c.IDs() {
		if d := c.Detector.GrantLease(c.Node(id), base); !d.Granted {
			t.Fatalf("AFTER RESET: node %s should be granted, got %q", id, d.Reason)
		}
	}
	t.Logf("[%s] AFTER RESET: inconsistent=%v all leases granted", runUUID, c.Inconsistent(base))
}

// TestFreezeFallsBehindAndIsDetected covers CLOSURE clause 2:
// a frozen node sticks at the freeze instant while the base advances and is
// detected only once its accumulated lag exceeds tolerance.
//
// Mutation: if FreezeFault still advanced with the base (returned base instead
// of capping at At), the reported lag would stay 0 forever and the node would
// never be flagged — the "detected after exceeding tolerance" assertion fails.
func TestFreezeFallsBehindAndIsDetected(t *testing.T) {
	const tol = 10
	const freezeAt = 100
	c := NewCluster(tol, "f1")
	if err := c.Inject("f1", FreezeFault{At: freezeAt}); err != nil {
		t.Fatalf("inject: %v", err)
	}
	t.Logf("[%s] FREEZE injected on f1 at=%d tol=%ds", runUUID, freezeAt, tol)

	n := c.Node("f1")

	// At the freeze instant lag is 0 => consistent.
	if v := c.Detector.EvaluateAt(n, freezeAt); !v.Consistent {
		t.Fatalf("at freeze instant lag should be 0, got skew=%d consistent=%v", v.Skew, v.Consistent)
	}
	t.Logf("[%s] base=%d reported=%d (lag=0) consistent", runUUID, freezeAt, n.last)

	// Within tolerance: base=freezeAt+tol => lag = -tol, still consistent.
	withinBase := int64(freezeAt + tol)
	if v := c.Detector.EvaluateAt(n, withinBase); !v.Consistent {
		t.Fatalf("base=%d lag=%d within tol must be consistent, got %v", withinBase, v.Skew, v.Consistent)
	}
	// Frozen node must NOT have advanced past the freeze instant.
	if n.last != freezeAt {
		t.Fatalf("frozen node advanced: reported=%d, want stuck at %d", n.last, freezeAt)
	}
	t.Logf("[%s] base=%d reported=%d (lag=%d) still consistent", runUUID, withinBase, n.last, n.last-withinBase)

	// Exceed tolerance: base=freezeAt+tol+1 => lag magnitude > tol => flagged.
	overBase := int64(freezeAt + tol + 1)
	v := c.Detector.EvaluateAt(n, overBase)
	if v.Consistent {
		t.Fatalf("base=%d lag=%d exceeds tol=%d, node MUST be flagged", overBase, v.Skew, tol)
	}
	if n.last != freezeAt {
		t.Fatalf("frozen node advanced under load: reported=%d, want %d", n.last, freezeAt)
	}
	if v.Monotonicity {
		t.Fatalf("freeze lag must be skew-class, not a monotonicity violation: %+v", v)
	}
	if d := c.Detector.GrantLease(n, overBase); d.Granted {
		t.Fatalf("frozen+behind node must be denied lease, got granted")
	}
	t.Logf("[%s] base=%d reported=%d (lag=%d) FLAGGED inconsistent", runUUID, overBase, n.last, v.Skew)
}

// TestMonotonicViolationDetectedDistinctly covers CLOSURE clause 3:
// a one-time backward jump is detected as a monotonicity fault, distinctly from
// a steady skew (which has no backward step).
//
// Mutation: if EvaluateAt dropped the backward-step check (Monotonicity always
// false), the post-jump read would be classed as ordinary skew and the
// "Monotonicity == true" assertion below would fail. Conversely a pure skew
// must NOT be reported as a monotonicity violation.
func TestMonotonicViolationDetectedDistinctly(t *testing.T) {
	const tol = 5
	const jumpAt = 200
	const jump = 100
	c := NewCluster(tol, "m1", "s1")

	// m1: monotonic violation. s1: steady skew of +3 (within tol, but used to
	// prove a steady offset is NOT mistaken for a backward jump).
	if err := c.Inject("m1", MonotonicViolationFault{At: jumpAt, Jump: jump}); err != nil {
		t.Fatalf("inject m1: %v", err)
	}
	if err := c.Inject("s1", SkewFault{Delta: 3}); err != nil {
		t.Fatalf("inject s1: %v", err)
	}
	t.Logf("[%s] MONO injected on m1 at=%d jump=%d; steady skew +3 on s1", runUUID, jumpAt, jump)

	m := c.Node("m1")

	// Read just before the jump: tracks base, consistent, no violation.
	pre := int64(jumpAt - 1)
	vPre := c.Detector.EvaluateAt(m, pre)
	if vPre.Monotonicity || !vPre.Consistent {
		t.Fatalf("pre-jump read must be clean: %+v", vPre)
	}
	t.Logf("[%s] m1 base=%d reported=%d pre-jump clean", runUUID, pre, m.last)

	// Read at the jump instant: reported steps backward by `jump`.
	vAt := c.Detector.EvaluateAt(m, jumpAt)
	if !vAt.Monotonicity {
		t.Fatalf("backward jump at base=%d must be flagged as monotonicity violation: %+v", jumpAt, vAt)
	}
	if vAt.Consistent {
		t.Fatalf("monotonicity-violating node must be inconsistent: %+v", vAt)
	}
	if vAt.Reported >= vPre.Reported {
		t.Fatalf("expected backward step: reported %d should be < prior %d", vAt.Reported, vPre.Reported)
	}
	if d := c.Detector.GrantLease(m, jumpAt); d.Granted {
		t.Fatalf("monotonicity-violating node must be denied lease")
	}
	t.Logf("[%s] m1 base=%d reported=%d MONOTONICITY VIOLATION (prev=%d)", runUUID, jumpAt, vAt.Reported, vPre.Reported)

	// The steady-skew node, read across two advancing ticks, must NOT be
	// mistaken for a monotonicity violation — its reported value never steps
	// backward.
	s := c.Node("s1")
	_ = c.Detector.EvaluateAt(s, jumpAt)
	vS := c.Detector.EvaluateAt(s, jumpAt+1)
	if vS.Monotonicity {
		t.Fatalf("steady-skew node wrongly flagged as monotonicity violation: %+v", vS)
	}
	t.Logf("[%s] s1 advancing skew base=%d reported=%d mono=%v (correctly not a violation)", runUUID, jumpAt+1, s.last, vS.Monotonicity)
}

// TestMonotonicViolationIsTransient asserts the backward step is detected once
// at the boundary, then the node advances monotonically from the lowered value
// and is no longer flagged for monotonicity on subsequent reads.
//
// Mutation: a fault that kept reporting a lower-than-previous value every tick
// would keep tripping the violation; this proves the jump is a single step.
func TestMonotonicViolationIsTransient(t *testing.T) {
	// Tolerance large enough that the residual offset never trips skew, so the
	// only thing under test is the monotonicity step.
	const tol = 1000
	const jumpAt = 50
	const jump = 20
	c := NewCluster(tol, "m1")
	if err := c.Inject("m1", MonotonicViolationFault{At: jumpAt, Jump: jump}); err != nil {
		t.Fatalf("inject: %v", err)
	}
	m := c.Node("m1")

	_ = c.Detector.EvaluateAt(m, jumpAt-1) // establish history below the jump
	vJump := c.Detector.EvaluateAt(m, jumpAt)
	if !vJump.Monotonicity {
		t.Fatalf("expected the violation exactly at the jump boundary: %+v", vJump)
	}
	t.Logf("[%s] transient: base=%d reported=%d violation", runUUID, jumpAt, vJump.Reported)

	vAfter := c.Detector.EvaluateAt(m, jumpAt+1)
	if vAfter.Monotonicity {
		t.Fatalf("after the single jump the node advances; no further violation: %+v", vAfter)
	}
	if vAfter.Reported <= vJump.Reported {
		t.Fatalf("post-jump node must advance: %d should be > %d", vAfter.Reported, vJump.Reported)
	}
	t.Logf("[%s] transient: base=%d reported=%d advancing (no violation)", runUUID, jumpAt+1, vAfter.Reported)
}
