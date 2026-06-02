package failconfirm

import "testing"

// runUUID anchors every test's t.Logf evidence to a fixed forensic identifier.
const runUUID = "failconfirm-HXC-1399-7c1a4e3b-0f2d-4a90-9b6e-2d11a8c4f001"

// logicalClock is a deterministic, test-controlled Clock. No wall clock is ever
// consulted by the package under test.
type logicalClock struct{ t int64 }

func (c *logicalClock) Now() int64 { return c.t }

func members5() []NodeID {
	return []NodeID{"n1", "n2", "n3", "n4", "n5"}
}

// TestQuorumPromotionFiresOnDeadExactlyOnce proves CLOSURE CLAUSE 1:
// N=5 (quorum 3). Two distinct PFAILs keep the target Suspect (not Dead, no
// callback). A third DISTINCT PFAIL promotes to Dead and OnDead fires EXACTLY
// ONCE even though further reports arrive.
//
// Mutation: if OnDead were fired on every report past quorum (not exactly
// once), deadCount would exceed 1 and this test fails. If quorum were computed
// as <=2, promotion would happen after the second report (wrong before-state).
func TestQuorumPromotionFiresOnDeadExactlyOnce(t *testing.T) {
	var deadCount int
	var deadTarget NodeID
	d, err := New(members5(), OnDead(func(n NodeID) {
		deadCount++
		deadTarget = n
	}))
	if err != nil {
		t.Fatalf("[%s] New: %v", runUUID, err)
	}
	if got := d.Quorum(); got != 3 {
		t.Fatalf("[%s] quorum: want 3 for N=5, got %d", runUUID, got)
	}

	const target = NodeID("n5")

	// Two distinct reporters -> Suspect, below quorum, no callback.
	if err := d.ReportPFAIL("n1", target); err != nil {
		t.Fatalf("[%s] report n1: %v", runUUID, err)
	}
	if err := d.ReportPFAIL("n2", target); err != nil {
		t.Fatalf("[%s] report n2: %v", runUUID, err)
	}
	t.Logf("[%s] after 2 distinct PFAILs: state=%s reporters=%d deadCount=%d",
		runUUID, d.State(target), d.ReporterCount(target), deadCount)
	if got := d.State(target); got != Suspect {
		t.Fatalf("[%s] want Suspect below quorum, got %s", runUUID, got)
	}
	if deadCount != 0 {
		t.Fatalf("[%s] OnDead fired before quorum: deadCount=%d", runUUID, deadCount)
	}

	// Third DISTINCT reporter reaches quorum -> Dead, callback once.
	beforeState := d.State(target)
	if err := d.ReportPFAIL("n3", target); err != nil {
		t.Fatalf("[%s] report n3: %v", runUUID, err)
	}
	afterState := d.State(target)
	t.Logf("[%s] third distinct PFAIL: before=%s after=%s reporters=%d deadCount=%d",
		runUUID, beforeState, afterState, d.ReporterCount(target), deadCount)
	if afterState != Dead {
		t.Fatalf("[%s] want Dead at quorum, got %s", runUUID, afterState)
	}
	if deadCount != 1 {
		t.Fatalf("[%s] OnDead must fire exactly once, got %d", runUUID, deadCount)
	}
	if deadTarget != target {
		t.Fatalf("[%s] OnDead target: want %s got %s", runUUID, target, deadTarget)
	}

	// Further reports past quorum must NOT re-fire OnDead and must stay Dead.
	if err := d.ReportPFAIL("n4", target); err != nil {
		t.Fatalf("[%s] report n4: %v", runUUID, err)
	}
	if err := d.ReportPFAIL("n4", target); err != nil { // even a repeat
		t.Fatalf("[%s] report n4 repeat: %v", runUUID, err)
	}
	t.Logf("[%s] after extra reports past quorum: state=%s deadCount=%d",
		runUUID, d.State(target), deadCount)
	if d.State(target) != Dead {
		t.Fatalf("[%s] FAIL must be terminal, got %s", runUUID, d.State(target))
	}
	if deadCount != 1 {
		t.Fatalf("[%s] OnDead re-fired past quorum: deadCount=%d", runUUID, deadCount)
	}
}

// TestRefutationBeforeQuorumKeepsAlive proves CLOSURE CLAUSE 2:
// After two PFAILs (Suspect), a ClearPFAIL drops the count below threshold;
// a subsequent re-resolve keeps the target Suspect/Alive (not Dead).
//
// Mutation: if ClearPFAIL did not actually remove the reporter's vote, a third
// distinct report would push the live count to 3 and promote to Dead — this
// test asserts the post-clear path stays non-Dead.
func TestRefutationBeforeQuorumKeepsAlive(t *testing.T) {
	var deadCount int
	d, err := New(members5(), OnDead(func(NodeID) { deadCount++ }))
	if err != nil {
		t.Fatalf("[%s] New: %v", runUUID, err)
	}
	const target = NodeID("n4")

	mustReport(t, d, "n1", target)
	mustReport(t, d, "n2", target)
	t.Logf("[%s] after 2 PFAILs: state=%s reporters=%d", runUUID, d.State(target), d.ReporterCount(target))
	if d.State(target) != Suspect {
		t.Fatalf("[%s] want Suspect, got %s", runUUID, d.State(target))
	}

	// Refutation: n1's PFAIL is cleared (target answered n1). Count -> 1.
	if err := d.ClearPFAIL("n1", target); err != nil {
		t.Fatalf("[%s] clear n1: %v", runUUID, err)
	}
	t.Logf("[%s] after refutation (clear n1): state=%s reporters=%d",
		runUUID, d.State(target), d.ReporterCount(target))
	if got := d.ReporterCount(target); got != 1 {
		t.Fatalf("[%s] want 1 reporter after clear, got %d", runUUID, got)
	}

	// Re-resolve: a third distinct node reports. Live distinct count is now
	// {n2, n3} = 2, still below quorum 3 -> must NOT be Dead.
	mustReport(t, d, "n3", target)
	t.Logf("[%s] after re-resolve (n3): state=%s reporters=%d deadCount=%d",
		runUUID, d.State(target), d.ReporterCount(target), deadCount)
	if got := d.ReporterCount(target); got != 2 {
		t.Fatalf("[%s] want 2 distinct reporters, got %d", runUUID, got)
	}
	if d.State(target) == Dead {
		t.Fatalf("[%s] refutation must prevent FAIL, got Dead", runUUID)
	}
	if deadCount != 0 {
		t.Fatalf("[%s] OnDead must not fire after refutation, got %d", runUUID, deadCount)
	}
}

// TestDistinctReporterDedup proves CLOSURE CLAUSE 3:
// The SAME node reporting PFAIL twice counts as ONE suspicion; two reports from
// one reporter (plus one from another) do NOT reach quorum 3.
//
// Mutation: if quorum counted TOTAL reports instead of DISTINCT reporters, n1
// reporting three times would reach a count of 3 and promote to Dead — this
// test asserts it stays Suspect with a distinct-reporter count of 1, then 2.
func TestDistinctReporterDedup(t *testing.T) {
	var deadCount int
	d, err := New(members5(), OnDead(func(NodeID) { deadCount++ }))
	if err != nil {
		t.Fatalf("[%s] New: %v", runUUID, err)
	}
	const target = NodeID("n3")

	// n1 reports THREE times. Distinct reporters = 1.
	mustReport(t, d, "n1", target)
	mustReport(t, d, "n1", target)
	mustReport(t, d, "n1", target)
	t.Logf("[%s] n1 reported x3: state=%s distinctReporters=%d deadCount=%d",
		runUUID, d.State(target), d.ReporterCount(target), deadCount)
	if got := d.ReporterCount(target); got != 1 {
		t.Fatalf("[%s] dedup failed: want 1 distinct reporter, got %d", runUUID, got)
	}
	if d.State(target) != Suspect {
		t.Fatalf("[%s] want Suspect after deduped repeats, got %s", runUUID, d.State(target))
	}

	// Add a second DISTINCT reporter, also twice. Distinct reporters = 2 < 3.
	mustReport(t, d, "n2", target)
	mustReport(t, d, "n2", target)
	t.Logf("[%s] +n2 reported x2: state=%s distinctReporters=%d deadCount=%d",
		runUUID, d.State(target), d.ReporterCount(target), deadCount)
	if got := d.ReporterCount(target); got != 2 {
		t.Fatalf("[%s] want 2 distinct reporters, got %d", runUUID, got)
	}
	if d.State(target) == Dead {
		t.Fatalf("[%s] 2 distinct reporters must not reach quorum 3, got Dead", runUUID)
	}
	if deadCount != 0 {
		t.Fatalf("[%s] OnDead must not fire below distinct quorum, got %d", runUUID, deadCount)
	}

	// A genuine THIRD distinct reporter finally reaches quorum — proves the
	// gate is distinct-count (1->2->3 distinct), not an arbitrary block and
	// not total-report counting.
	mustReport(t, d, "n4", target)
	t.Logf("[%s] +n4 (3rd distinct): state=%s distinctReporters=%d deadCount=%d",
		runUUID, d.State(target), d.ReporterCount(target), deadCount)
	if d.State(target) != Dead {
		t.Fatalf("[%s] 3 distinct reporters must reach quorum, got %s", runUUID, d.State(target))
	}
	if deadCount != 1 {
		t.Fatalf("[%s] OnDead must fire exactly once at distinct quorum, got %d", runUUID, deadCount)
	}

	// Membership guard: a non-member reporter is rejected.
	if err := d.ReportPFAIL("ghost", "n1"); err != ErrUnknownNode {
		t.Fatalf("[%s] non-member report: want ErrUnknownNode, got %v", runUUID, err)
	}
}

// mustReport is a helper that fails the test on report error.
func mustReport(t *testing.T, d *Detector, reporter, target NodeID) {
	t.Helper()
	if err := d.ReportPFAIL(reporter, target); err != nil {
		t.Fatalf("[%s] ReportPFAIL(%s,%s): %v", runUUID, reporter, target, err)
	}
}

// TestTTLExpiryDropsStaleVotes proves the injected-clock TTL window: a PFAIL
// that ages past the TTL no longer counts toward quorum, so quorum is only met
// while reports are concurrent within the window.
//
// Mutation: if expired votes were NOT pruned, n1's stale vote would persist and
// {n1,n2,n3} would promote to Dead; this test asserts the stale vote drops so
// only {n2,n3} remain (below quorum 3).
func TestTTLExpiryDropsStaleVotes(t *testing.T) {
	clk := &logicalClock{t: 100}
	var deadCount int
	d, err := New(members5(), WithClock(clk), WithTTL(10), OnDead(func(NodeID) { deadCount++ }))
	if err != nil {
		t.Fatalf("[%s] New: %v", runUUID, err)
	}
	const target = NodeID("n5")

	// n1 reports at t=100.
	mustReport(t, d, "n1", target)
	t.Logf("[%s] t=%d n1 PFAIL: reporters=%d", runUUID, clk.t, d.ReporterCount(target))

	// Advance well past TTL so n1's vote expires.
	clk.t = 120
	// n2, n3 report fresh at t=120.
	mustReport(t, d, "n2", target)
	mustReport(t, d, "n3", target)
	live := d.ReporterCount(target)
	t.Logf("[%s] t=%d after expiry+2 fresh: distinctLiveReporters=%d state=%s deadCount=%d",
		runUUID, clk.t, live, d.State(target), deadCount)
	if live != 2 {
		t.Fatalf("[%s] want 2 live reporters (n1 expired), got %d", runUUID, live)
	}
	if d.State(target) == Dead {
		t.Fatalf("[%s] stale vote must not contribute to quorum, got Dead", runUUID)
	}
	if deadCount != 0 {
		t.Fatalf("[%s] OnDead must not fire on expired-vote quorum, got %d", runUUID, deadCount)
	}

	// A third FRESH distinct reporter at the same tick reaches quorum.
	mustReport(t, d, "n4", target)
	t.Logf("[%s] t=%d +n4 fresh: state=%s deadCount=%d", runUUID, clk.t, d.State(target), deadCount)
	if d.State(target) != Dead {
		t.Fatalf("[%s] want Dead with 3 fresh reporters, got %s", runUUID, d.State(target))
	}
	if deadCount != 1 {
		t.Fatalf("[%s] OnDead must fire once, got %d", runUUID, deadCount)
	}
}
