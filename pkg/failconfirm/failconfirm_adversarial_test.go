package failconfirm

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// advUUID anchors this adversarial suite's t.Logf evidence to a fixed forensic
// identifier, distinct from the existing closure tests.
const advUUID = "failconfirm-ADV-HXC-1399-b71e0d52-9c34-4f6a-a1d8-7e5c2f9043aa"

// ---------------------------------------------------------------------------
// CONTRACT (verbatim from failconfirm.go):
//   - promotion to FAIL (Dead) requires a MAJORITY quorum = floor(N/2)+1 of
//     DISTINCT cluster members (lines 1-9, 114-135).
//   - the gate is `liveReporterCount(target) >= d.quorum` (line 211): at-quorum
//     promotes, below-quorum does not.
//   - DISTINCT-reporter semantics: a reporter present in the map counts ONCE
//     regardless of repeat reports (lines 69-76, 207-209).
//   - FAIL is terminal; OnDead fires EXACTLY once (lines 108-112, 237-247).
//   - Detector is NOT safe for concurrent use; callers serialize (lines 81-82).
//
// This file probes SINK-SIDE: the actual confirmed/not-confirmed verdict, the
// actual distinct vote count — never merely "no panic".
// ---------------------------------------------------------------------------

// ===========================================================================
// (a) THRESHOLD / QUORUM BOUNDARY  —  exactly-at vs one-below, swept over N.
//
// The existing suite only exercises N=5 (quorum 3). The off-by-one that matters
// most (`>=` vs `>` at the quorum line; floor(N/2)+1 vs floor(N/2)) lives at the
// EVEN-N boundary, where strict majority differs from half. This sweep proves
// the SINK verdict at every relevant N: ONE-BELOW quorum must NOT be Dead, and
// EXACTLY-AT quorum MUST be Dead.
// ===========================================================================
func TestAdversarial_QuorumBoundary_SweptOverN(t *testing.T) {
	// Each case: N members, expected strict-majority quorum.
	cases := []struct {
		n          int
		wantQuorum int
	}{
		{1, 1}, // 1/2+1 = 1
		{2, 2}, // 2/2+1 = 2  (both must agree — strict majority of 2)
		{3, 2}, // 3/2+1 = 2
		{4, 3}, // 4/2+1 = 3  (half of 4 is 2; majority is 3 — the key even case)
		{5, 3}, // 5/2+1 = 3
		{6, 4}, // 6/2+1 = 4  (half is 3; majority is 4)
		{7, 4},
		{8, 5}, // half is 4; majority is 5
	}

	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("N%d", tc.n), func(t *testing.T) {
			members := make([]NodeID, tc.n)
			for i := 0; i < tc.n; i++ {
				members[i] = NodeID(fmt.Sprintf("m%d", i))
			}
			// Target is the LAST member. Reporters are drawn from ALL members
			// (a node may report PFAIL, including against itself in this minimal
			// model — see the N=1 self-quorum case). This guarantees exactly N
			// distinct reporter candidates, so quorum=floor(N/2)+1 <= N is always
			// reachable, even when quorum==N (e.g. N=2 needs both members).
			// We order candidates so the target reports LAST, meaning the
			// before-quorum prefix uses non-target reporters wherever possible.
			target := members[tc.n-1]
			reporters := make([]NodeID, 0, tc.n)
			reporters = append(reporters, members[:tc.n-1]...) // non-target first
			reporters = append(reporters, target)             // target reports itself last

			var deadCount int32
			d, err := New(members, OnDead(func(NodeID) { atomic.AddInt32(&deadCount, 1) }))
			if err != nil {
				t.Fatalf("[%s] New(N=%d): %v", advUUID, tc.n, err)
			}
			if got := d.Quorum(); got != tc.wantQuorum {
				t.Fatalf("[%s] N=%d quorum: want %d, got %d", advUUID, tc.n, tc.wantQuorum, got)
			}

			if tc.n == 1 {
				// N=1, quorum 1: covered separately in
				// TestAdversarial_SingleMemberSelfQuorum (self-report sink).
				return
			}

			// --- ONE BELOW QUORUM: must NOT confirm (sink: not Dead) ---
			belowN := tc.wantQuorum - 1
			for i := 0; i < belowN; i++ {
				if err := d.ReportPFAIL(reporters[i], target); err != nil {
					t.Fatalf("[%s] N=%d report %s: %v", advUUID, tc.n, reporters[i], err)
				}
			}
			stateBelow := d.State(target)
			rcBelow := d.ReporterCount(target)
			t.Logf("[%s] N=%d quorum=%d | %d distinct reporters: state=%s reporterCount=%d deadCount=%d",
				advUUID, tc.n, tc.wantQuorum, belowN, stateBelow, rcBelow, atomic.LoadInt32(&deadCount))
			if stateBelow == Dead {
				t.Fatalf("[%s] N=%d FAIL-OPEN: confirmed Dead at %d reporters (one BELOW quorum %d)",
					advUUID, tc.n, belowN, tc.wantQuorum)
			}
			if atomic.LoadInt32(&deadCount) != 0 {
				t.Fatalf("[%s] N=%d OnDead fired below quorum: deadCount=%d", advUUID, tc.n, deadCount)
			}

			// --- EXACTLY AT QUORUM: must confirm (sink: Dead, fired once) ---
			if err := d.ReportPFAIL(reporters[belowN], target); err != nil { // the quorum-th distinct reporter
				t.Fatalf("[%s] N=%d quorum-th report: %v", advUUID, tc.n, err)
			}
			stateAt := d.State(target)
			t.Logf("[%s] N=%d quorum=%d | %d distinct reporters (AT quorum): state=%s deadCount=%d",
				advUUID, tc.n, tc.wantQuorum, tc.wantQuorum, stateAt, atomic.LoadInt32(&deadCount))
			if stateAt != Dead {
				t.Fatalf("[%s] N=%d FAIL-CLOSED: NOT confirmed at exactly quorum %d (state=%s)",
					advUUID, tc.n, tc.wantQuorum, stateAt)
			}
			if got := atomic.LoadInt32(&deadCount); got != 1 {
				t.Fatalf("[%s] N=%d OnDead must fire exactly once at quorum, got %d", advUUID, tc.n, got)
			}
		})
	}
}

// TestAdversarial_SingleMemberSelfQuorum pins the N=1 degenerate boundary:
// quorum is floor(1/2)+1 = 1, so a single distinct PFAIL is, by the stated
// majority rule, already a quorum. We assert the SINK verdict on the smallest
// cluster — proving the quorum formula does not special-case (or off-by-one)
// the N=1 floor.
func TestAdversarial_SingleMemberSelfQuorum(t *testing.T) {
	var deadCount int
	d, err := New([]NodeID{"solo"}, OnDead(func(NodeID) { deadCount++ }))
	if err != nil {
		t.Fatalf("[%s] New(N=1): %v", advUUID, err)
	}
	if got := d.Quorum(); got != 1 {
		t.Fatalf("[%s] N=1 quorum: want 1, got %d", advUUID, got)
	}
	// The only member reports PFAIL against itself (the sole possible target).
	if err := d.ReportPFAIL("solo", "solo"); err != nil {
		t.Fatalf("[%s] report: %v", advUUID, err)
	}
	t.Logf("[%s] N=1 single PFAIL: state=%s deadCount=%d", advUUID, d.State("solo"), deadCount)
	if d.State("solo") != Dead {
		t.Fatalf("[%s] N=1 quorum 1: single PFAIL must confirm Dead, got %s", advUUID, d.State("solo"))
	}
	if deadCount != 1 {
		t.Fatalf("[%s] N=1 OnDead must fire once, got %d", advUUID, deadCount)
	}
}

// ===========================================================================
// (b) DUPLICATE-REPORTER INFLATION — the highest-severity sink check.
//
// A single (malicious or buggy) reporter that re-reports must NEVER, on its own,
// drive a target to FAIL. We pick an EVEN-N cluster (N=4, quorum 3) so the
// "if duplicates counted, one reporter reporting 3x would reach quorum" attack
// is sharply testable: 1 reporter * 3 reports == quorum 3.
//
// SINK ASSERTION: the verdict must NOT flip to Dead on duplicate reports; the
// distinct count must stay 1. Then we prove the gate is genuinely distinct-based
// (not an unconditional block) by adding REAL distinct reporters to reach quorum.
// ===========================================================================
func TestAdversarial_DuplicateReporterCannotReachQuorumAlone(t *testing.T) {
	members := []NodeID{"a", "b", "c", "d"} // N=4 -> quorum 3
	var deadCount int
	d, err := New(members, OnDead(func(NodeID) { deadCount++ }))
	if err != nil {
		t.Fatalf("[%s] New: %v", advUUID, err)
	}
	if d.Quorum() != 3 {
		t.Fatalf("[%s] N=4 quorum: want 3, got %d", advUUID, d.Quorum())
	}
	const target = NodeID("d")

	// One reporter floods the same PFAIL quorum-many times (and then some).
	for i := 0; i < 10; i++ {
		if err := d.ReportPFAIL("a", target); err != nil {
			t.Fatalf("[%s] dup report #%d: %v", advUUID, i, err)
		}
	}
	rc := d.ReporterCount(target)
	state := d.State(target)
	t.Logf("[%s] single reporter 'a' reported 10x (quorum 3): distinctReporters=%d state=%s deadCount=%d",
		advUUID, rc, state, deadCount)
	if rc != 1 {
		t.Fatalf("[%s] DUPLICATE INFLATION: want 1 distinct reporter, got %d", advUUID, rc)
	}
	if state == Dead {
		t.Fatalf("[%s] DUPLICATE INFLATION FAIL-OPEN: a single reporter confirmed FAIL alone (state=%s)",
			advUUID, state)
	}
	if deadCount != 0 {
		t.Fatalf("[%s] OnDead fired from one reporter's duplicates: deadCount=%d", advUUID, deadCount)
	}

	// Prove the gate IS distinct-count: two MORE genuine distinct reporters
	// (a,b,c = 3 distinct) reach quorum, and the verdict flips exactly here.
	if err := d.ReportPFAIL("b", target); err != nil {
		t.Fatalf("[%s] report b: %v", advUUID, err)
	}
	if d.State(target) == Dead {
		t.Fatalf("[%s] premature FAIL at 2 distinct reporters (quorum 3)", advUUID)
	}
	if err := d.ReportPFAIL("c", target); err != nil {
		t.Fatalf("[%s] report c: %v", advUUID, err)
	}
	t.Logf("[%s] +b +c (3 distinct, AT quorum): state=%s deadCount=%d", advUUID, d.State(target), deadCount)
	if d.State(target) != Dead {
		t.Fatalf("[%s] 3 distinct reporters must confirm FAIL, got %s", advUUID, d.State(target))
	}
	if deadCount != 1 {
		t.Fatalf("[%s] OnDead must fire exactly once at distinct quorum, got %d", advUUID, deadCount)
	}
}

// TestAdversarial_DuplicateAtQuorumMinusOne is the tightest duplicate-inflation
// trap: N=3 -> quorum 2. With exactly ONE genuine distinct reporter that
// re-reports many times, the live count must stay 1 and the target must remain
// Suspect (never Dead). If a repeat were miscounted as a second vote, the
// verdict would flip to Dead on the duplicate — this asserts it does not.
func TestAdversarial_DuplicateAtQuorumMinusOne(t *testing.T) {
	d, err := New([]NodeID{"x", "y", "z"}) // N=3 -> quorum 2
	if err != nil {
		t.Fatalf("[%s] New: %v", advUUID, err)
	}
	if d.Quorum() != 2 {
		t.Fatalf("[%s] N=3 quorum: want 2, got %d", advUUID, d.Quorum())
	}
	const target = NodeID("z")
	for i := 0; i < 50; i++ {
		if err := d.ReportPFAIL("x", target); err != nil {
			t.Fatalf("[%s] dup report: %v", advUUID, err)
		}
		if d.State(target) == Dead { // assert verdict never flips mid-flood
			t.Fatalf("[%s] DUPLICATE INFLATION: verdict flipped to Dead on duplicate #%d "+
				"(1 distinct reporter, quorum 2)", advUUID, i)
		}
	}
	t.Logf("[%s] 'x' reported 50x on N=3/quorum2: state=%s distinctReporters=%d",
		advUUID, d.State(target), d.ReporterCount(target))
	if d.State(target) != Suspect {
		t.Fatalf("[%s] want Suspect (1 distinct < quorum 2), got %s", advUUID, d.State(target))
	}
	if d.ReporterCount(target) != 1 {
		t.Fatalf("[%s] want 1 distinct reporter, got %d", advUUID, d.ReporterCount(target))
	}
}

// ===========================================================================
// (c) UNGUARDED SHARED MUTABLE STATE — serialized conservation under load.
//
// The package DOCUMENTS that it is not concurrency-safe and the caller must
// serialize (lines 81-82); the existing concurrent test proves the disclaimer
// is load-bearing. Here we add a CONSERVATION proof under the documented
// serialized contract: many goroutines, each a DISTINCT reporter, hammer a
// single target through one shared lock. The sink invariants:
//   - distinct reporter set never exceeds the number of distinct reporters;
//   - once quorum is reached the target is Dead and STAYS Dead (terminal);
//   - OnDead fires exactly once (no lost/double promotion under interleaving).
// Runs under the package's normal `go test -race`; an internal unsynchronized
// access not covered by the external lock would trip -race here.
// ===========================================================================
func TestAdversarial_SerializedConservationUnderLoad(t *testing.T) {
	var mu sync.Mutex
	var deadCount int
	members := []NodeID{"r1", "r2", "r3", "r4", "r5", "r6", "r7"} // N=7 -> quorum 4
	d, err := New(members, OnDead(func(NodeID) { deadCount++ }))
	if err != nil {
		t.Fatalf("[%s] New: %v", advUUID, err)
	}
	if d.Quorum() != 4 {
		t.Fatalf("[%s] N=7 quorum: want 4, got %d", advUUID, d.Quorum())
	}
	const target = NodeID("r7")
	reporters := []NodeID{"r1", "r2", "r3", "r4", "r5", "r6"} // 6 distinct >= quorum 4

	var wg sync.WaitGroup
	for _, r := range reporters {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 4000; i++ {
				mu.Lock()
				_ = d.ReportPFAIL(r, target)
				// Read-side under the same lock: terminal invariant check.
				if d.State(target) == Dead && d.ReporterCount(target) < d.Quorum() {
					mu.Unlock()
					t.Errorf("[%s] CONSERVATION VIOLATION: Dead with reporterCount<quorum", advUUID)
					return
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	state := d.State(target)
	dc := deadCount
	mu.Unlock()
	t.Logf("[%s] N=7 serialized 6 distinct reporters x4000: state=%s deadCount=%d",
		advUUID, state, dc)
	if state != Dead {
		t.Fatalf("[%s] want Dead (6 distinct >= quorum 4), got %s", advUUID, state)
	}
	if dc != 1 {
		t.Fatalf("[%s] OnDead must fire EXACTLY once under serialized load, got %d", advUUID, dc)
	}
}

// ===========================================================================
// (d) NUMERIC POISON & TOTAL-ORDER / TERMINALITY.
//
// failconfirm uses an integer logical Clock and INTEGER tick math — there is no
// float score/weight surface for NaN/Inf to poison. We PROVE that: a Clock that
// returns a tick derived from a NaN/Inf float (truncated to int64) cannot defeat
// the TTL window, because the quorum gate is an integer distinct-count, not a
// float comparison. This pins the absence of a numeric-poison surface rather
// than manufacturing one.
//
// We also pin DETERMINISTIC TERMINALITY on equal vote-counts: two different
// targets that each independently reach the SAME quorum both confirm
// independently (no shared/ambiguous tiebreak), and a clear/report after FAIL is
// a no-op (terminal), so the verdict is order-independent past quorum.
// ===========================================================================

// poisonClock returns a tick computed from a poisoned float, truncated to int64.
type poisonClock struct{ raw float64 }

func (p *poisonClock) Now() int64 { return int64(p.raw) }

func TestAdversarial_NumericPoisonCannotDefeatTTLQuorum(t *testing.T) {
	// int64(NaN) and int64(+Inf) are implementation-defined-ish but in Go yield
	// 0 / a saturated value; what matters is the SINK: distinct-count quorum is
	// integer and is NOT a float comparison, so no poisoned tick can fake a
	// quorum from a single reporter.
	for _, raw := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		raw := raw
		t.Run(fmt.Sprintf("raw_%v", raw), func(t *testing.T) {
			clk := &poisonClock{raw: raw}
			var deadCount int
			d, err := New(members5(), WithClock(clk), WithTTL(10), OnDead(func(NodeID) { deadCount++ }))
			if err != nil {
				t.Fatalf("[%s] New: %v", advUUID, err)
			}
			const target = NodeID("n5")
			// A SINGLE reporter under a poisoned clock must not reach quorum 3.
			if err := d.ReportPFAIL("n1", target); err != nil {
				t.Fatalf("[%s] report: %v", advUUID, err)
			}
			t.Logf("[%s] poisoned tick=%d, 1 reporter: state=%s reporterCount=%d deadCount=%d",
				advUUID, clk.Now(), d.State(target), d.ReporterCount(target), deadCount)
			if d.State(target) == Dead {
				t.Fatalf("[%s] NUMERIC POISON FAIL-OPEN: 1 reporter confirmed FAIL under poisoned clock", advUUID)
			}
			if deadCount != 0 {
				t.Fatalf("[%s] OnDead fired from single poisoned report: %d", advUUID, deadCount)
			}
		})
	}
}

// TestAdversarial_EqualQuorumTargetsConfirmIndependently proves there is no
// ambiguous tiebreak when two DISTINCT targets reach the SAME quorum with the
// same reporter set: each is promoted independently and OnDead fires once PER
// target (deterministic, total-order-free terminality).
func TestAdversarial_EqualQuorumTargetsConfirmIndependently(t *testing.T) {
	var mu sync.Mutex
	dead := map[NodeID]int{}
	d, err := New(members5(), OnDead(func(n NodeID) { mu.Lock(); dead[n]++; mu.Unlock() }))
	if err != nil {
		t.Fatalf("[%s] New: %v", advUUID, err)
	}
	// Targets n4 and n5 each reached by the same 3 distinct reporters {n1,n2,n3}.
	for _, target := range []NodeID{"n4", "n5"} {
		for _, r := range []NodeID{"n1", "n2", "n3"} {
			if err := d.ReportPFAIL(r, target); err != nil {
				t.Fatalf("[%s] report %s->%s: %v", advUUID, r, target, err)
			}
		}
	}
	t.Logf("[%s] equal-quorum targets: n4=%s(%d) n5=%s(%d)",
		advUUID, d.State("n4"), dead["n4"], d.State("n5"), dead["n5"])
	for _, target := range []NodeID{"n4", "n5"} {
		if d.State(target) != Dead {
			t.Fatalf("[%s] target %s must be Dead at quorum, got %s", advUUID, target, d.State(target))
		}
		if dead[target] != 1 {
			t.Fatalf("[%s] OnDead for %s must fire once, got %d", advUUID, target, dead[target])
		}
	}
	// Terminality: post-FAIL report/clear are no-ops; verdict order-independent.
	if err := d.ClearPFAIL("n1", "n4"); err != nil {
		t.Fatalf("[%s] clear after dead: %v", advUUID, err)
	}
	if err := d.ReportPFAIL("n4", "n4"); err != nil {
		t.Fatalf("[%s] report after dead: %v", advUUID, err)
	}
	if d.State("n4") != Dead {
		t.Fatalf("[%s] FAIL must stay terminal after clear/report, got %s", advUUID, d.State("n4"))
	}
	mu.Lock()
	n4dead := dead["n4"]
	mu.Unlock()
	if n4dead != 1 {
		t.Fatalf("[%s] OnDead re-fired after terminal clear/report: %d", advUUID, n4dead)
	}
}

// TestAdversarial_TTLBoundaryExactAge pins the EXACT TTL boundary semantics as
// the code actually implements them, and flags the doc-vs-code discrepancy.
//
// Doc (WithTTL, lines 101-103): "A PFAIL report OLDER THAN ttl ... no longer
// counts." Strict reading: age > ttl is expired; age == ttl still counts.
// Code (liveReporterCount, lines 176-184): cutoff = now - ttl; prune if
// at <= cutoff, i.e. prune if (now-at) >= ttl. So a vote of age EXACTLY ttl is
// PRUNED (treated as expired), which is "older than OR EQUAL to ttl".
//
// This is a one-tick boundary in the CONSERVATIVE direction (it expires a vote
// one tick sooner than the strict-doc reading), so it can only ever FAIL to
// confirm, never FALSELY confirm — it is NOT a fail-open. We pin the real
// behavior so a future change is caught, and surface the discrepancy as a
// LATENT RISK in the report.
func TestAdversarial_TTLBoundaryExactAge(t *testing.T) {
	clk := &logicalClock{t: 0}
	d, err := New(members5(), WithClock(clk), WithTTL(10))
	if err != nil {
		t.Fatalf("[%s] New: %v", advUUID, err)
	}
	const target = NodeID("n5")

	// Vote asserted at t=0.
	if err := d.ReportPFAIL("n1", target); err != nil {
		t.Fatalf("[%s] report: %v", advUUID, err)
	}

	// Age exactly == ttl: now=10, vote at 0, cutoff = 10-10 = 0, at(0) <= 0 -> PRUNED.
	clk.t = 10
	rcAtTTL := d.ReporterCount(target)
	t.Logf("[%s] age==ttl(10): now=%d reporterCount=%d (code prunes at age>=ttl)",
		advUUID, clk.t, rcAtTTL)
	if rcAtTTL != 0 {
		t.Fatalf("[%s] CODE-CONTRACT PIN broke: expected vote pruned at age==ttl (got count=%d). "+
			"If the package was changed to keep age==ttl votes, update this pin and the report.",
			advUUID, rcAtTTL)
	}

	// Re-assert and check age == ttl-1 (still live) to bracket the boundary.
	clk.t = 100
	if err := d.ReportPFAIL("n2", target); err != nil { // asserted at t=100
		t.Fatalf("[%s] report n2: %v", advUUID, err)
	}
	clk.t = 109 // age 9 < ttl 10 -> live
	rcBelow := d.ReporterCount(target)
	t.Logf("[%s] age==ttl-1(9): now=%d reporterCount=%d (must still be live)",
		advUUID, clk.t, rcBelow)
	if rcBelow != 1 {
		t.Fatalf("[%s] vote of age ttl-1 must remain live, got count=%d", advUUID, rcBelow)
	}
}
