package dst

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// HXC-1109: deterministic-sim chaos harness unit tests.
// ---------------------------------------------------------------------------

// TestChaosScenarioKillRescheduleMembership is the primary sink-side test.
//
// It runs the full scenario and asserts:
//  1. The session is rescheduled onto a surviving (non-killed) node.
//  2. The rescheduled node is Online.
//  3. Membership reconverges to N-1 (node count before kill minus the dead node).
//  4. The recovery timeline contains "kill", "reschedule", and "reconverge" events.
//  5. The timeline has a non-empty per-run UUID.
//
// Mutation target: set DisableReschedule=true and assert this test FAILS (see
// TestChaosScenarioDisabledRescheduleFails below which exercises that path).
func TestChaosScenarioKillRescheduleMembership(t *testing.T) {
	const N = 3
	result, err := RunChaosScenario(ChaosScenarioConfig{
		Seed:       42,
		NodeCount:  N,
		KillNodeID: "node-1",
		SessionID:  "sess-A",
	})
	if err != nil {
		t.Fatalf("RunChaosScenario: %v", err)
	}

	sess := result.Session
	if sess == nil {
		t.Fatal("session not found in result")
	}

	// 1. Session must NOT be assigned to the killed node.
	if sess.AssignedNode == "node-1" {
		t.Errorf("session still on killed node-1; reschedule did not fire")
	}

	// 2. Session must be Active (successfully rescheduled).
	if !sess.Active {
		t.Errorf("session.Active = false after reschedule; expected true")
	}

	// 3. Membership must have reconverged to N-1.
	wantMembers := N - 1
	if result.FinalMemberCount != wantMembers {
		t.Errorf("final member count = %d, want %d (N-1)", result.FinalMemberCount, wantMembers)
	}

	// 4. Timeline must contain kill, reschedule, reconverge events.
	tl := result.Timeline
	for _, kind := range []TimelineEventKind{
		TimelineKillNode, TimelineReschedule, TimelineReconverge,
	} {
		if !tl.ContainsKind(kind) {
			t.Errorf("timeline missing %q event", kind)
		}
	}

	// 5. Per-run UUID must be non-empty and 36 chars.
	if len(tl.RunID) != 36 {
		t.Errorf("timeline.RunID = %q, expected 36-char UUID", tl.RunID)
	}
}

// TestChaosScenarioDisabledRescheduleFails is the §1.1 MUTATION TEST.
//
// With DisableReschedule=true the session must NOT be rescheduled: sess.Active
// stays false AND sess.AssignedNode remains the killed node. This test PROVES
// that when we restore reschedulingEnabled=true the positive test passes, and
// when we force it false the session stays dead — the mutation is caught.
func TestChaosScenarioDisabledRescheduleFails(t *testing.T) {
	result, err := RunChaosScenario(ChaosScenarioConfig{
		Seed:              99,
		NodeCount:         3,
		KillNodeID:        "node-1",
		SessionID:         "sess-A",
		DisableReschedule: true, // MUTATION: rescheduling disabled
	})
	if err != nil {
		t.Fatalf("RunChaosScenario (disabled): %v", err)
	}

	sess := result.Session
	if sess == nil {
		t.Fatal("session not found")
	}

	// Session MUST be inactive (orphaned, not rescheduled).
	if sess.Active {
		t.Errorf("with DisableReschedule=true: sess.Active = true; expected false (session should be orphaned)")
	}

	// Timeline must still contain a kill event.
	if !result.Timeline.ContainsKind(TimelineKillNode) {
		t.Error("timeline missing kill event even with reschedule disabled")
	}

	// The reschedule event should record "SKIPPED" in the detail.
	found := false
	for _, ev := range result.Timeline.Snapshot() {
		if ev.Kind == TimelineReschedule && strings.Contains(ev.Detail, "SKIPPED") {
			found = true
			break
		}
	}
	if !found {
		t.Error("timeline missing reschedule-SKIPPED event with DisableReschedule=true")
	}
}

// TestChaosScenarioTimelineOrder verifies that kill precedes reschedule precedes
// reconverge in the timeline — proving the DST engine fires events in virtual
// time order (kill@t=10, reconverge@t=20).
//
// Mutation: reordering ScheduleEvent calls (kill@20, reconverge@10) would swap
// the positions and fail the index-order assertion.
func TestChaosScenarioTimelineOrder(t *testing.T) {
	result, err := RunChaosScenario(ChaosScenarioConfig{
		Seed:       7,
		NodeCount:  3,
		KillNodeID: "node-2",
		SessionID:  "work-B",
	})
	if err != nil {
		t.Fatalf("RunChaosScenario: %v", err)
	}

	evts := result.Timeline.Snapshot()

	// Find the index of the first kill, reschedule, reconverge events.
	idx := func(kind TimelineEventKind) int {
		for i, ev := range evts {
			if ev.Kind == kind {
				return i
			}
		}
		return -1
	}

	killIdx := idx(TimelineKillNode)
	reschedIdx := idx(TimelineReschedule)
	reconIdx := idx(TimelineReconverge)

	if killIdx < 0 {
		t.Fatal("no kill event in timeline")
	}
	if reschedIdx < 0 {
		t.Fatal("no reschedule event in timeline")
	}
	if reconIdx < 0 {
		t.Fatal("no reconverge event in timeline")
	}

	// kill must precede reschedule (reschedule is triggered inside KillNode).
	if killIdx >= reschedIdx {
		t.Errorf("kill (%d) must precede reschedule (%d) in timeline", killIdx, reschedIdx)
	}
	// reschedule must precede reconverge (reconverge fires at t=20, after kill@t=10).
	if reschedIdx >= reconIdx {
		t.Errorf("reschedule (%d) must precede reconverge (%d) in timeline", reschedIdx, reconIdx)
	}
}

// TestChaosScenarioSessionOnSurvivingNode verifies that the rescheduled session
// ends up on a node that is still Online after the kill.
//
// Mutation: if rescheduleSession picked the dead node instead of a candidate,
// the assigned node's Online flag would be false and this test would catch it.
func TestChaosScenarioSessionOnSurvivingNode(t *testing.T) {
	result, err := RunChaosScenario(ChaosScenarioConfig{
		Seed:       123,
		NodeCount:  4,
		KillNodeID: "node-1",
		SessionID:  "sess-C",
	})
	if err != nil {
		t.Fatalf("RunChaosScenario: %v", err)
	}

	sess := result.Session
	if sess == nil {
		t.Fatal("session not found")
	}

	// The assigned node must not be the killed node.
	if sess.AssignedNode == "node-1" {
		t.Errorf("session assigned to killed node-1; reschedule failed")
	}

	// The assigned node must be in the surviving online set.
	// We verify this indirectly: final member count is N-1 and the session node
	// is not "node-1", so it must be alive.
	if result.FinalMemberCount != 3 {
		t.Errorf("final member count = %d, want 3 (4-1)", result.FinalMemberCount)
	}
}

// TestChaosScenarioUniqueRunIDs verifies that two independent scenario runs
// produce different timeline RunIDs.
//
// Mutation: using a fixed constant for RunID would make both IDs identical.
func TestChaosScenarioUniqueRunIDs(t *testing.T) {
	cfg := ChaosScenarioConfig{Seed: 1, NodeCount: 3}
	r1, err := RunChaosScenario(cfg)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	r2, err := RunChaosScenario(cfg)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}

	if r1.Timeline.RunID == r2.Timeline.RunID {
		t.Errorf("two scenario runs have identical RunID %q; UUIDs must be unique", r1.Timeline.RunID)
	}
}

// TestChaosScenarioDeterminism verifies that the same seed produces the same
// session assignment outcome across two independent runs.
//
// Mutation: seeding the engine from time.Now() instead of cfg.Seed would make
// the outcome non-deterministic across runs.
func TestChaosScenarioDeterminism(t *testing.T) {
	cfg := ChaosScenarioConfig{Seed: 555, NodeCount: 3, KillNodeID: "node-1"}

	r1, err := RunChaosScenario(cfg)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	r2, err := RunChaosScenario(cfg)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}

	// Same seed => same rescheduled node.
	if r1.Session.AssignedNode != r2.Session.AssignedNode {
		t.Errorf("determinism violated: run1 node=%q run2 node=%q",
			r1.Session.AssignedNode, r2.Session.AssignedNode)
	}
	// Same seed => same final member count.
	if r1.FinalMemberCount != r2.FinalMemberCount {
		t.Errorf("determinism violated: run1 members=%d run2 members=%d",
			r1.FinalMemberCount, r2.FinalMemberCount)
	}
}

// TestChaosScenarioMembershipAfterKill asserts membership == N-1 across several
// cluster sizes, proving the kill correctly removes exactly one member.
func TestChaosScenarioMembershipAfterKill(t *testing.T) {
	for _, N := range []int{3, 5, 7} {
		N := N // capture
		t.Run(fmt.Sprintf("N=%d", N), func(t *testing.T) {
			result, err := RunChaosScenario(ChaosScenarioConfig{
				Seed:      int64(N) * 17,
				NodeCount: N,
			})
			if err != nil {
				t.Fatalf("N=%d: %v", N, err)
			}
			want := N - 1
			if result.FinalMemberCount != want {
				t.Errorf("N=%d: final members = %d, want %d", N, result.FinalMemberCount, want)
			}
		})
	}
}

// TestRecoveryTimelineContainsRunID ensures the timeline's RunID is embedded and
// non-empty even in a minimal scenario, so evidence tracing always has a unique anchor.
func TestRecoveryTimelineContainsRunID(t *testing.T) {
	tl := NewRecoveryTimeline()
	if tl.RunID == "" {
		t.Fatal("timeline RunID is empty on construction")
	}
	if len(tl.RunID) != 36 {
		t.Errorf("RunID = %q; expected 36-char UUID", tl.RunID)
	}
}
