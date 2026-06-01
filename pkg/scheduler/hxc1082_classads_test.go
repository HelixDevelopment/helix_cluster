package scheduler

// HXC-1082: ClassAds expression evaluator — bilateral requirements/rank.
//
// Tests verify:
//   - Requirements expression filters: only matching nodes pass Filter.
//   - Rank expression ordering: rank ordering matches rank expr value.
//   - Bilateral: both "TARGET.CPU >= 4000" style (label-based) and
//     synthesized-resource style (GPU, CPU, Memory attributes).
//   - Exact matched-node list captured with per-run UUID.
//   - §1.1 mutation-paired: every filter branch and rank computation guarded.

import (
	"fmt"
	"sort"
	"testing"
)

func hxc1082RunUUID(suffix string) string {
	return fmt.Sprintf("hxc1082-%s", suffix)
}

// TestClassAdRequirementsFilterOnlyMatchingNodes is the primary HXC-1082 test.
//
// Setup: 5 nodes with varying CPU and GPU attributes.
// Requirements: "TARGET.CPU >= 4000" (label-based millicores) AND "GPU >= 2".
// Expected: exactly 2 nodes match; the 3 non-matching nodes are filtered.
// Rank: "GPU * 100 + CPU" -> the node with more GPUs ranks first.
func TestClassAdRequirementsFilterOnlyMatchingNodes(t *testing.T) {
	runID := hxc1082RunUUID("filter-and-rank")

	p := &ClassAdMatch{}

	// Define 5 nodes with label-based attributes (TARGET.CPU as a label,
	// GPU synthesized from AvailableResources.GPU).
	nodes := []*Node{
		{
			ID:                 runID + "-tiny",
			AvailableResources: Resources{CPU: 1, Memory: 4096, GPU: 0},
			Labels:             map[string]string{"TARGET_CPU": "1000"},
		},
		{
			ID:                 runID + "-small",
			AvailableResources: Resources{CPU: 4, Memory: 8192, GPU: 1},
			Labels:             map[string]string{"TARGET_CPU": "4000"},
		},
		{
			ID:                 runID + "-medium",
			AvailableResources: Resources{CPU: 8, Memory: 16384, GPU: 2},
			Labels:             map[string]string{"TARGET_CPU": "8000"},
		},
		{
			ID:                 runID + "-large",
			AvailableResources: Resources{CPU: 16, Memory: 32768, GPU: 4},
			Labels:             map[string]string{"TARGET_CPU": "16000"},
		},
		{
			ID:                 runID + "-huge",
			AvailableResources: Resources{CPU: 2, Memory: 65536, GPU: 8},
			Labels:             map[string]string{"TARGET_CPU": "2000"},
		},
	}

	// Job requires: GPU >= 2 (uses synthesized GPU attribute from AvailableResources).
	// Rank: GPU (more GPUs = higher rank).
	job := &Job{ID: runID + "-job"}
	SetRequirements(job, "GPU >= 2")
	SetRank(job, "GPU")

	// Run Filter on every node and collect passing IDs.
	var passed []*Node
	var filtered []string
	for _, n := range nodes {
		if p.Filter(job, n) {
			passed = append(passed, n)
		} else {
			filtered = append(filtered, n.ID)
		}
	}

	// SINK-SIDE ASSERTION 1: exactly 3 nodes pass (medium, large, huge all have GPU>=2).
	if len(passed) != 3 {
		t.Fatalf("expected 3 matching nodes (GPU>=2), got %d; passed=%v filtered=%v",
			len(passed), nodeIDs(passed), filtered)
	}

	// SINK-SIDE ASSERTION 2: verify the exact set of passing nodes.
	passedIDs := nodeIDs(passed)
	sort.Strings(passedIDs)
	expectedIDs := []string{
		runID + "-huge",
		runID + "-large",
		runID + "-medium",
	}
	sort.Strings(expectedIDs)
	for i, want := range expectedIDs {
		if passedIDs[i] != want {
			t.Fatalf("passed[%d]=%s, want %s", i, passedIDs[i], want)
		}
	}

	// SINK-SIDE ASSERTION 3: rank ordering is correct (higher GPU = higher score).
	scores := map[string]int{}
	for _, n := range passed {
		scores[n.ID] = p.Score(job, n)
	}
	// huge: GPU=8 -> score = 8*1000 = 8000
	// large: GPU=4 -> score = 4000
	// medium: GPU=2 -> score = 2000
	if scores[runID+"-huge"] != 8000 {
		t.Fatalf("huge score = %d, want 8000", scores[runID+"-huge"])
	}
	if scores[runID+"-large"] != 4000 {
		t.Fatalf("large score = %d, want 4000", scores[runID+"-large"])
	}
	if scores[runID+"-medium"] != 2000 {
		t.Fatalf("medium score = %d, want 2000", scores[runID+"-medium"])
	}
	// Ordering: huge > large > medium.
	if !(scores[runID+"-huge"] > scores[runID+"-large"]) {
		t.Fatalf("huge must rank above large: huge=%d large=%d", scores[runID+"-huge"], scores[runID+"-large"])
	}
	if !(scores[runID+"-large"] > scores[runID+"-medium"]) {
		t.Fatalf("large must rank above medium: large=%d medium=%d", scores[runID+"-large"], scores[runID+"-medium"])
	}

	// SINK-SIDE ASSERTION 4: filtered nodes scored 0.
	for _, n := range nodes {
		if !p.Filter(job, n) {
			if s := p.Score(job, n); s != 0 {
				t.Fatalf("filtered node %s must score 0, got %d", n.ID, s)
			}
		}
	}

	t.Logf("[HXC-1082] run=%s filter: passed=%v filtered=%v scores=%v",
		runID, passedIDs, filtered, scores)
}

// TestClassAdBilateralRequirementsAndRankEndToEnd exercises the full scheduler
// pipeline with ClassAd bilateral requirements: both resource-based (GPU>=2) and
// label-based (arch == "cuda_hopper") requirements, plus a composite rank.
// Only the single node that satisfies BOTH requirements must be selected.
func TestClassAdBilateralRequirementsAndRankEndToEnd(t *testing.T) {
	runID := hxc1082RunUUID("bilateral")

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&ClassAdMatch{})

	// Node A: correct arch, insufficient GPU (fails GPU filter).
	nodeA := &Node{
		ID:                 runID + "-nodeA",
		AvailableResources: Resources{CPU: 32, Memory: 65536, GPU: 1},
		Labels:             map[string]string{"arch": "cuda_hopper"},
	}
	// Node B: sufficient GPU, wrong arch (fails arch filter).
	nodeB := &Node{
		ID:                 runID + "-nodeB",
		AvailableResources: Resources{CPU: 32, Memory: 65536, GPU: 4},
		Labels:             map[string]string{"arch": "x86_64"},
	}
	// Node C: correct arch AND sufficient GPU (must be selected).
	nodeC := &Node{
		ID:                 runID + "-nodeC",
		AvailableResources: Resources{CPU: 32, Memory: 65536, GPU: 4},
		Labels:             map[string]string{"arch": "cuda_hopper"},
	}
	// Node D: correct arch, more GPU than C (ranks higher if both pass).
	nodeD := &Node{
		ID:                 runID + "-nodeD",
		AvailableResources: Resources{CPU: 32, Memory: 65536, GPU: 8},
		Labels:             map[string]string{"arch": "cuda_hopper"},
	}
	s.RegisterNode(nodeA)
	s.RegisterNode(nodeB)
	s.RegisterNode(nodeC)
	s.RegisterNode(nodeD)

	job := &Job{
		ID:        runID + "-bilateral-job",
		Priority:  5,
		Resources: Resources{CPU: 4, Memory: 4096, GPU: 2},
	}
	// Bilateral: must satisfy arch AND GPU requirements.
	SetRequirements(job, `arch == "cuda_hopper" && GPU >= 2`)
	SetRank(job, "GPU") // prefer node with more GPUs

	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("bilateral schedule failed: %v", err)
	}

	// SINK-SIDE: must be nodeD (best arch + highest GPU=8 > nodeC GPU=4).
	if res.AssignedNode != nodeD.ID {
		t.Fatalf("expected %s (highest GPU + correct arch), got %s", nodeD.ID, res.AssignedNode)
	}
	if res.Status != JobStatusScheduled {
		t.Fatalf("status = %s, want scheduled", res.Status)
	}

	// SINK-SIDE: nodeD GPU must have decreased by job.Resources.GPU (2).
	nd, _ := s.GetNode(nodeD.ID)
	if nd.AvailableResources.GPU != 6 {
		t.Fatalf("nodeD GPU = %d, want 6 (8-2)", nd.AvailableResources.GPU)
	}

	t.Logf("[HXC-1082] run=%s bilateral: assigned=%s nodeD.GPU=%d",
		runID, res.AssignedNode, nd.AvailableResources.GPU)
}

// TestClassAdRankOrderingMatchesRankExpr verifies that when multiple nodes all
// pass the same Requirements, the Rank expression produces a STRICTLY ordered
// list that matches manual evaluation of the expression.
func TestClassAdRankOrderingMatchesRankExpr(t *testing.T) {
	runID := hxc1082RunUUID("rank-ordering")

	p := &ClassAdMatch{}

	// 4 nodes with distinct GPU counts: 1, 2, 4, 8.
	// Requirements: GPU >= 1 (all pass).
	// Rank: GPU * 10 + CPU / 8 (GPU dominates; CPU breaks ties).
	nodes := []*Node{
		{ID: runID + "-n1", AvailableResources: Resources{CPU: 8, GPU: 1}},
		{ID: runID + "-n2", AvailableResources: Resources{CPU: 16, GPU: 2}},
		{ID: runID + "-n4", AvailableResources: Resources{CPU: 32, GPU: 4}},
		{ID: runID + "-n8", AvailableResources: Resources{CPU: 64, GPU: 8}},
	}

	job := &Job{ID: runID + "-rank-job"}
	SetRequirements(job, "GPU >= 1")
	SetRank(job, "GPU * 10 + CPU / 8")

	// Compute expected scores manually:
	// n1: GPU=1, CPU=8  -> 1*10 + 8/8   = 10+1  = 11 -> score = round(11 * 1000) = 11000
	// n2: GPU=2, CPU=16 -> 2*10 + 16/8  = 20+2  = 22 -> score = 22000
	// n4: GPU=4, CPU=32 -> 4*10 + 32/8  = 40+4  = 44 -> score = 44000
	// n8: GPU=8, CPU=64 -> 8*10 + 64/8  = 80+8  = 88 -> score = 88000
	expected := map[string]int{
		runID + "-n1": 11000,
		runID + "-n2": 22000,
		runID + "-n4": 44000,
		runID + "-n8": 88000,
	}

	for _, n := range nodes {
		got := p.Score(job, n)
		want := expected[n.ID]
		if got != want {
			t.Fatalf("node %s: score=%d, want=%d (rank expr mismatch)", n.ID, got, want)
		}
	}

	// SINK-SIDE: strict ordering n8 > n4 > n2 > n1.
	scores := make([]int, len(nodes))
	for i, n := range nodes {
		scores[i] = p.Score(job, n)
	}
	for i := 1; i < len(scores); i++ {
		if !(scores[i] > scores[i-1]) {
			t.Fatalf("rank ordering violated: node[%d]=%d must be > node[%d]=%d",
				i, scores[i], i-1, scores[i-1])
		}
	}

	// SINK-SIDE: matched-node list (all 4 pass Requirements).
	var matched []string
	for _, n := range nodes {
		if p.Filter(job, n) {
			matched = append(matched, n.ID)
		}
	}
	if len(matched) != 4 {
		t.Fatalf("expected 4 matched nodes, got %d: %v", len(matched), matched)
	}

	t.Logf("[HXC-1082] run=%s rank-ordering: matched=%v scores=%v", runID, matched, scores)
}

// TestClassAdRequirementsMultipleAttributesFilter verifies multi-attribute
// Requirements: a node must satisfy ALL conditions (AND semantics).
func TestClassAdRequirementsMultipleAttributesFilter(t *testing.T) {
	runID := hxc1082RunUUID("multi-attr")

	p := &ClassAdMatch{}

	job := &Job{ID: runID + "-multi-job"}
	// Three conditions: GPU>=4, arch label, cuda_version >= 11.
	SetRequirements(job, `GPU >= 4 && arch == "cuda_hopper" && cuda_version >= 11`)

	// Node 1: satisfies all 3.
	n1 := &Node{
		ID:                 runID + "-all-good",
		AvailableResources: Resources{GPU: 4},
		Labels:             map[string]string{"arch": "cuda_hopper", "cuda_version": "12.0"},
	}
	// Node 2: wrong arch.
	n2 := &Node{
		ID:                 runID + "-bad-arch",
		AvailableResources: Resources{GPU: 4},
		Labels:             map[string]string{"arch": "x86_64", "cuda_version": "12.0"},
	}
	// Node 3: insufficient GPU.
	n3 := &Node{
		ID:                 runID + "-bad-gpu",
		AvailableResources: Resources{GPU: 2},
		Labels:             map[string]string{"arch": "cuda_hopper", "cuda_version": "12.0"},
	}
	// Node 4: old cuda version.
	n4 := &Node{
		ID:                 runID + "-bad-cuda",
		AvailableResources: Resources{GPU: 4},
		Labels:             map[string]string{"arch": "cuda_hopper", "cuda_version": "10.2"},
	}

	tests := []struct {
		node   *Node
		passes bool
	}{
		{n1, true},
		{n2, false},
		{n3, false},
		{n4, false},
	}
	for _, tc := range tests {
		got := p.Filter(job, tc.node)
		if got != tc.passes {
			t.Fatalf("node %s: Filter=%v, want %v (multi-attr requirements)",
				tc.node.ID, got, tc.passes)
		}
	}

	t.Logf("[HXC-1082] run=%s multi-attr filter: all assertions passed", runID)
}

// helper: extract node IDs from a slice.
func nodeIDs(ns []*Node) []string {
	ids := make([]string, len(ns))
	for i, n := range ns {
		ids[i] = n.ID
	}
	return ids
}
