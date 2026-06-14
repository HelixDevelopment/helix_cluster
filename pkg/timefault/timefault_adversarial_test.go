package timefault

// Adversarial sink-side suite for pkg/timefault.
//
// Probes the numeric edges of the skew detector: int64 overflow in the
// absolute-skew computation, the threshold boundary (`<=` vs `<`), the
// backward-jump (MonotonicViolation) overflow path, and the data race on the
// per-Node monotonicity state. Every assertion checks a SINK — the detector's
// actual Consistent / Monotonicity verdict and the GrantLease decision — never
// merely "no panic".
//
// Run: go test -race -count=2 github.com/HelixDevelopment/helix_cluster/pkg/timefault

import (
	"math"
	"sync"
	"testing"
)

const advRunUUID = "timefault-adv-run-3d71f0c4-HXC-1245"

// TestAdversarial_AbsSkewOverflowMissesDetection is the load-bearing bug probe.
//
// CONTRACT (package doc): a node is "flagged INCONSISTENT when |reported-base|
// exceeds a tolerance" and a clock-faulty node is "denied leadership to avoid
// split-brain". The maximally-skewed node possible — SkewFault{Delta:MinInt64}
// — has |skew| astronomically larger than any tolerance, so it MUST be flagged
// inconsistent and denied a lease.
//
// SINK proven: with base=1000, reported-base lands EXACTLY on math.MinInt64.
// abs64(math.MinInt64) == math.MinInt64 (negation overflow: there is no positive
// representation of -MinInt64 in int64), which is < 0 and therefore <= any
// non-negative tolerance. The detector returns Consistent=true and GrantLease
// GRANTS the lease to a clock that is ~9.2e18 seconds off — the exact
// split-brain outcome the detector exists to prevent.
//
// This test FAILS on unfixed prod (it asserts the CORRECT verdict). It is NOT
// env-gated: it is a real-bug reproducer, single-sequence, race-clean.
func TestAdversarial_AbsSkewOverflowMissesDetection(t *testing.T) {
	const tol = 30
	const base = 1000

	c := NewCluster(tol, "n")
	n := c.Node("n")

	// Public-API reachable: the largest-magnitude negative skew.
	if err := c.Inject("n", SkewFault{Delta: math.MinInt64}); err != nil {
		t.Fatalf("inject: %v", err)
	}

	v := c.Detector.EvaluateAt(n, base)

	// Sink 0: confirm the adversarial skew actually lands on the overflow value,
	// so the test is exercising the real edge and not a near-miss.
	if v.Skew != math.MinInt64 {
		t.Fatalf("setup: expected skew=MinInt64 at base=%d, got %d", base, v.Skew)
	}
	t.Logf("[%s] base=%d Delta=MinInt64 reported=%d skew=%d (|skew| dwarfs tol=%d)",
		advRunUUID, base, v.Reported, v.Skew, tol)

	// Sink 1: a skew of |MinInt64| seconds MUST be inconsistent. On unfixed prod
	// abs64 overflows negative, abs64(skew) <= tol is true, and this is reported
	// Consistent=true => this assertion bites.
	if v.Consistent {
		t.Fatalf("OVERFLOW MISS: node with skew=%d flagged CONSISTENT (abs64 overflow); "+
			"|skew| hugely exceeds tol=%d and MUST be inconsistent", v.Skew, tol)
	}

	// Sink 2: the lease decision — the real-world consequence. A maximally
	// clock-faulty node must be DENIED leadership.
	if d := c.Detector.GrantLease(n, base); d.Granted {
		t.Fatalf("SPLIT-BRAIN: node skewed by %d seconds was GRANTED a lease (reason=%q)",
			v.Skew, d.Reason)
	}
	t.Logf("[%s] correct sink: skew=%d node denied lease (no split-brain)", advRunUUID, v.Skew)
}

// TestAdversarial_ThresholdBoundary pins the documented `<=` semantics: a skew
// exactly AT tolerance is consistent (within), one second OVER is flagged. This
// is a contract-pinning test (the boundary is correct as documented: "exceeds
// the tolerance"), and it also exercises zero skew.
//
// Mutation: flipping `<=` to `<` in withinTol would flag the at-tolerance node
// inconsistent and trip the first assertion; flipping `<` to `<=` (i.e. raising
// the over-tolerance bound) would pass the over-tol node and trip the second.
func TestAdversarial_ThresholdBoundary(t *testing.T) {
	const tol = 30
	const base = 1000
	c := NewCluster(tol, "z", "at", "over")

	// Zero skew: dead-center consistent.
	if v := c.Detector.EvaluateAt(c.Node("z"), base); !v.Consistent || v.Skew != 0 {
		t.Fatalf("zero skew must be consistent: %+v", v)
	}

	// Exactly at tolerance (+tol): within => consistent (doc: only *exceeds* flags).
	if err := c.Inject("at", SkewFault{Delta: tol}); err != nil {
		t.Fatalf("inject at: %v", err)
	}
	vAt := c.Detector.EvaluateAt(c.Node("at"), base)
	if !vAt.Consistent {
		t.Fatalf("skew exactly at tol=%d must be CONSISTENT (<=): %+v", tol, vAt)
	}
	if d := c.Detector.GrantLease(c.Node("at"), base); !d.Granted {
		t.Fatalf("at-tolerance node should be granted a lease: %q", d.Reason)
	}

	// One over tolerance (+tol+1): exceeds => inconsistent + denied.
	if err := c.Inject("over", SkewFault{Delta: tol + 1}); err != nil {
		t.Fatalf("inject over: %v", err)
	}
	vOver := c.Detector.EvaluateAt(c.Node("over"), base)
	if vOver.Consistent {
		t.Fatalf("skew tol+1=%d must be INCONSISTENT: %+v", tol+1, vOver)
	}
	if d := c.Detector.GrantLease(c.Node("over"), base); d.Granted {
		t.Fatalf("over-tolerance node must be denied a lease")
	}
	t.Logf("[%s] boundary OK: skew=0 ok, skew=%d (==tol) ok, skew=%d (>tol) flagged",
		advRunUUID, tol, tol+1)
}

// TestAdversarial_MonotonicJumpOverflowSign probes the backward-jump path with an
// overflowing Jump magnitude. MonotonicViolationFault.ReportedTime returns
// base-Jump; with Jump<0 (or an overflowing positive), the "backward" jump can
// become a FORWARD step, so the monotonicity check (reported < prev) does not
// fire. This is a LATENT-RISK / contract pin: the documented field is
// "positive magnitude of the backward jump", so a negative Jump is out of
// contract. We pin the in-contract behaviour (a real positive jump is detected)
// and document the sign sink for a negative jump.
//
// Mutation: if the backward-step check (reported < prev) were dropped, the
// genuine positive-jump case below would NOT be flagged and the assertion bites.
func TestAdversarial_MonotonicJumpOverflowSign(t *testing.T) {
	const tol = 1 << 40 // large, so only the monotonicity step is under test
	const at = 200

	// In-contract: a real positive backward jump is detected as a violation.
	{
		c := NewCluster(tol, "m")
		m := c.Node("m")
		if err := c.Inject("m", MonotonicViolationFault{At: at, Jump: 100}); err != nil {
			t.Fatalf("inject: %v", err)
		}
		_ = c.Detector.EvaluateAt(m, at-1) // history below boundary
		v := c.Detector.EvaluateAt(m, at)
		if !v.Monotonicity {
			t.Fatalf("positive backward jump at base=%d must be a monotonicity violation: %+v", at, v)
		}
		t.Logf("[%s] in-contract positive jump detected: reported=%d", advRunUUID, v.Reported)
	}

	// Out-of-contract sign documentation: Jump<0 makes base-Jump step FORWARD, so
	// it is (correctly, per the model) NOT a backward-step violation. We assert the
	// SINK so the behaviour is pinned, not assumed: reported steps forward and no
	// monotonicity violation is raised. This is the documented "positive magnitude"
	// precondition being violated by the caller, not a detector bug.
	{
		c := NewCluster(tol, "m")
		m := c.Node("m")
		if err := c.Inject("m", MonotonicViolationFault{At: at, Jump: -100}); err != nil {
			t.Fatalf("inject: %v", err)
		}
		vPre := c.Detector.EvaluateAt(m, at-1)
		vAt := c.Detector.EvaluateAt(m, at)
		if vAt.Reported <= vPre.Reported {
			t.Fatalf("negative Jump should step FORWARD (out-of-contract): %d !> %d", vAt.Reported, vPre.Reported)
		}
		if vAt.Monotonicity {
			t.Fatalf("forward step must NOT be a monotonicity violation: %+v", vAt)
		}
		t.Logf("[%s] LATENT: Jump<0 (out-of-contract) steps forward reported=%d, no false violation",
			advRunUUID, vAt.Reported)
	}
}

// TestAdversarial_SkewSubtractionOverflowAtMaxBase complements the MinInt64 probe
// from the positive side: a large positive Delta at a large base must still be
// flagged. This guards against any "fix" that only special-cases MinInt64 while
// leaving ordinary large skews mis-detected. In-range large skews must flag.
//
// Mutation: a fix that clamps abs to 0 on overflow (wrong direction) would make
// this large in-range skew read consistent and trip the assertion.
func TestAdversarial_SkewSubtractionOverflowAtMaxBase(t *testing.T) {
	const tol = 30
	const base = 1 << 50
	c := NewCluster(tol, "n")
	n := c.Node("n")
	// A large but representable skew (no overflow): must be flagged.
	const delta = 1 << 40
	if err := c.Inject("n", SkewFault{Delta: delta}); err != nil {
		t.Fatalf("inject: %v", err)
	}
	v := c.Detector.EvaluateAt(n, base)
	if v.Skew != delta {
		t.Fatalf("expected skew=%d, got %d", int64(delta), v.Skew)
	}
	if v.Consistent {
		t.Fatalf("large in-range skew=%d must be INCONSISTENT: %+v", v.Skew, v)
	}
	t.Logf("[%s] large in-range skew=%d correctly flagged", advRunUUID, v.Skew)
}

// TestAdversarial_ConcurrentSameNodeRace is the data-race probe on the per-Node
// monotonicity state (hasLast/lastBase/last), mutated on every Read and by
// Inject. Concurrent EvaluateAt + Inject on the SAME node is out of the
// documented single-sequence contract; under -race this surfaces the race.
//
// It is opt-in (mirrors the existing concurrency suite's disclaimer stance) so
// the default -race run stays green: timefault's read-ordering contract is
// single-sequence, so this is a disclaimer proof, not a bug to fix with a mutex.
func TestAdversarial_ConcurrentSameNodeRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping out-of-contract race reproducer in -short")
	}
	t.Skip("opt-in: documented contract is single-sequence per node; concurrent " +
		"same-node EvaluateAt+Inject is out of contract. Remove this Skip and run " +
		"with -race to observe the per-Node monotonicity-state data race.")

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
