package scheduler

// Adversarial probes for pkg/scheduler's CORE placement-decision path.
//
// Target surface: Scheduler.Schedule / Scheduler.ScheduleOptimistic (scheduler.go).
//
// CONTRACT under test (class a — ranking TOTAL ORDER / deterministic tiebreak):
//
//	The scheduler's sibling decision paths bestNodeAmong (gang_preempt.go:179)
//	and scheduleGangLocked DELIBERATELY sort node IDs before ranking, with the
//	in-code comment "Deterministic iteration for stable placement decisions."
//	That establishes the package's own contract: equal-score candidates must
//	resolve to a DETERMINISTIC, well-defined placement (the first by ascending
//	node ID), NOT to whichever node Go's randomized map iteration happens to
//	visit first.
//
//	Scheduler.Schedule and Scheduler.ScheduleOptimistic, however, rank by
//	iterating `s.nodes` (a map) directly with `score > bestScore`. When two or
//	more nodes TIE on score, the winner is "the first one map iteration yields",
//	which Go randomizes per-run. => NONDETERMINISTIC placement, violating the
//	package's own stated stable-placement contract.
//
// SINK-SIDE assertion: we run the SAME tie scenario many times on FRESH
// schedulers and assert the chosen node (ScheduleResult.AssignedNode) is the
// SAME every time AND equals the deterministic oracle (min node ID among the
// tied winners). A nondeterministic implementation produces >1 distinct winner
// across runs and FAILS. This is not a "no panic" check — it asserts the
// exact identity of the bound node.
//
// The other proven classes are already covered and pass on prod:
//   - over-commit / OCC conservation  -> omega_occ_race_test.go (CAS is correct)
//   - so this file targets ONLY the live total-order gap.

import (
	"fmt"
	"sort"
	"testing"
)

// equalScorePlugin makes EVERY node tie: it accepts all nodes and returns a
// constant score, regardless of job or node. This isolates the tiebreak path:
// with all scores equal, a correct implementation must STILL pick a single,
// deterministic node. A map-iteration implementation picks an arbitrary one.
type equalScorePlugin struct{}

func (equalScorePlugin) Name() string            { return "equalScore" }
func (equalScorePlugin) Filter(*Job, *Node) bool { return true }
func (equalScorePlugin) Score(*Job, *Node) int   { return 7 } // constant => universal tie
func (equalScorePlugin) Bind(*Job, *Node) bool   { return true }

// tiedNodeIDs is a spread of node IDs whose min (by string order) is a stable,
// known oracle. We use enough nodes that random map iteration almost surely
// surfaces a different "first" element across runs if the impl is unsorted.
func tiedNodeIDs() []string {
	return []string{
		"node-h", "node-c", "node-a", "node-z", "node-m",
		"node-b", "node-q", "node-d", "node-k", "node-f",
	}
}

func newTiedScheduler() *Scheduler {
	s := NewScheduler()
	s.AddPlugin(equalScorePlugin{})
	for _, id := range tiedNodeIDs() {
		s.RegisterNode(&Node{
			ID:                 id,
			AvailableResources: Resources{CPU: 1000, Memory: 1_000_000, GPU: 8},
		})
	}
	return s
}

func deterministicOracle() string {
	ids := tiedNodeIDs()
	sort.Strings(ids)
	return ids[0] // lowest ID — matches bestNodeAmong's sorted-first-wins rule
}

// TestAdversarial_Schedule_TiebreakDeterministic asserts that Schedule binds the
// SAME node on every run when all candidates tie on score. This BITES on the
// unsorted map-iteration implementation (which yields different winners across
// runs) and PASSES once Schedule ranks over a deterministically ordered node set
// (matching the package's own bestNodeAmong contract).
func TestAdversarial_Schedule_TiebreakDeterministic(t *testing.T) {
	const runs = 400
	oracle := deterministicOracle()
	winners := map[string]int{}

	for i := 0; i < runs; i++ {
		s := newTiedScheduler()
		job := &Job{ID: fmt.Sprintf("job-%d", i), Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}}
		res, err := s.Schedule(job)
		if err != nil {
			t.Fatalf("run %d: unexpected error: %v", i, err)
		}
		winners[res.AssignedNode]++
	}

	if len(winners) != 1 {
		t.Fatalf("Schedule tiebreak is NONDETERMINISTIC: across %d runs the bound node varied: %v\n"+
			"contract (cf. bestNodeAmong: \"Deterministic iteration for stable placement decisions\") "+
			"requires a single stable winner.", runs, winners)
	}
	got := single(winners)
	if got != oracle {
		t.Fatalf("Schedule stable but picked %q; deterministic oracle (min node ID) is %q", got, oracle)
	}
	t.Logf("[tiebreak/Schedule] %d runs -> single deterministic winner %q (oracle %q)", runs, got, oracle)
}

// TestAdversarial_ScheduleOptimistic_TiebreakDeterministic is the same probe on
// the optimistic-concurrency entrypoint, which also iterates s.nodes directly.
func TestAdversarial_ScheduleOptimistic_TiebreakDeterministic(t *testing.T) {
	const runs = 400
	oracle := deterministicOracle()
	winners := map[string]int{}

	for i := 0; i < runs; i++ {
		s := newTiedScheduler()
		job := &Job{ID: fmt.Sprintf("ojob-%d", i), Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}}
		res, err := s.ScheduleOptimistic(job, s.Version())
		if err != nil {
			t.Fatalf("run %d: unexpected error: %v", i, err)
		}
		winners[res.AssignedNode]++
	}

	if len(winners) != 1 {
		t.Fatalf("ScheduleOptimistic tiebreak is NONDETERMINISTIC: across %d runs the bound node varied: %v",
			runs, winners)
	}
	got := single(winners)
	if got != oracle {
		t.Fatalf("ScheduleOptimistic stable but picked %q; oracle (min node ID) is %q", got, oracle)
	}
	t.Logf("[tiebreak/ScheduleOptimistic] %d runs -> single deterministic winner %q (oracle %q)", runs, got, oracle)
}

// TestAdversarial_ScheduleVsGang_SameWinner pins CONSISTENCY across the two
// decision paths: for the identical tied scenario, single-job Schedule and the
// gang path (which is already deterministic) must agree on the chosen node.
// Today they can disagree because only the gang path sorts. After the fix they
// agree. This is a cross-path differential oracle, not a self-referential pin.
func TestAdversarial_ScheduleVsGang_SameWinner(t *testing.T) {
	// Gang path winner for a single tied task = its deterministic choice.
	sg := newTiedScheduler()
	gangJob := &Job{ID: "gang-task", Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}}
	gr, err := sg.ScheduleGang([]*Job{gangJob})
	if err != nil {
		t.Fatalf("gang schedule failed: %v", err)
	}
	gangWinner := gr.Placements[0].NodeID

	// Schedule path: must agree on every run.
	const runs = 200
	for i := 0; i < runs; i++ {
		s := newTiedScheduler()
		job := &Job{ID: fmt.Sprintf("sj-%d", i), Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}}
		res, err := s.Schedule(job)
		if err != nil {
			t.Fatalf("run %d: Schedule failed: %v", i, err)
		}
		if res.AssignedNode != gangWinner {
			t.Fatalf("run %d: Schedule picked %q but the deterministic gang path picked %q; "+
				"the two decision paths must agree on a tied placement", i, res.AssignedNode, gangWinner)
		}
	}
	t.Logf("[cross-path] Schedule agrees with gang path on tied winner %q over %d runs", gangWinner, runs)
}

func single(m map[string]int) string {
	for k := range m {
		return k
	}
	return ""
}
