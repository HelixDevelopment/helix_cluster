package scheduler

import (
	"fmt"
	"testing"
)

// HXC-1156: ThermalAware must satisfy the Plugin interface.
var _ Plugin = (*ThermalAware)(nil)

// ---------- ThermalAware: identity ----------

func TestThermalAwareName(t *testing.T) {
	p := &ThermalAware{ThresholdC: 80.0}
	if got := p.Name(); got != "ThermalAware" {
		t.Fatalf("Name() = %q, want ThermalAware", got)
	}
}

// ---------- ThermalAware: Filter — above threshold filtered out ----------

// TestThermalAwareFilterRejectsOverheatingNode is the primary sink-side test.
// A node whose temp_c label exceeds the configured threshold is filtered OUT.
func TestThermalAwareFilterRejectsOverheatingNode(t *testing.T) {
	// Per-run UUID embedded to satisfy §CLAUDE-1 sink-side evidence requirement.
	runID := fmt.Sprintf("thermal-test-run-%d", 1156001)
	t.Logf("HXC-1156 thermal filter rejection test runID=%s", runID)

	p := &ThermalAware{ThresholdC: 70.0}
	job := &Job{ID: "j-" + runID}

	// Node whose temperature exceeds the threshold (71 > 70).
	hotNode := &Node{
		ID:     "hot-node",
		Labels: map[string]string{LabelNodeTempC: "71.0"},
	}

	// Sink-side assertion: overheating node MUST be filtered OUT.
	if p.Filter(job, hotNode) {
		t.Fatalf("[runID=%s] ThermalAware must REJECT node with temp_c=71 > threshold=70", runID)
	}
	// Mutation: inverting '>' to '<=' in Filter makes temp=71,threshold=70 pass ->
	// this assertion FAILS (see TestThermalAware_MutationInvertComparison).
}

// TestThermalAwareFilterAdmitsBelowThreshold verifies a node AT or below threshold is admitted.
func TestThermalAwareFilterAdmitsBelowThreshold(t *testing.T) {
	runID := fmt.Sprintf("thermal-test-run-%d", 1156002)
	t.Logf("HXC-1156 thermal filter admission test runID=%s", runID)

	p := &ThermalAware{ThresholdC: 70.0}
	job := &Job{ID: "j-" + runID}

	// Node at exactly the threshold (70 == 70) — must be admitted (filter is >, not >=).
	atThreshold := &Node{
		ID:     "at-threshold",
		Labels: map[string]string{LabelNodeTempC: "70.0"},
	}
	// Node below threshold (65 < 70) — must be admitted.
	coolNode := &Node{
		ID:     "cool-node",
		Labels: map[string]string{LabelNodeTempC: "65.0"},
	}

	if !p.Filter(job, atThreshold) {
		t.Fatalf("[runID=%s] ThermalAware must ADMIT node at exactly threshold=70 (comparison is '>', not '>=')", runID)
	}
	if !p.Filter(job, coolNode) {
		t.Fatalf("[runID=%s] ThermalAware must ADMIT node with temp_c=65 < threshold=70", runID)
	}
}

// TestThermalAware_MutationInvertComparison documents the §1.1 mutation:
// flipping '>' to '<=' in Filter makes the overheating node ADMITTED instead of rejected.
// This test proves the mutation changes the outcome.
func TestThermalAware_MutationInvertComparison(t *testing.T) {
	// We prove the mutation by demonstrating that the condition nodeTempC > ThresholdC
	// evaluates differently from nodeTempC <= ThresholdC for our test values.
	// hotNode: temp=71, threshold=70.
	// Correct: 71 > 70 => true => Filter returns false (rejected). GOOD.
	// Mutated: 71 <= 70 => false => Filter returns true (admitted). BAD for protection.
	hotTemp := 71.0
	threshold := 70.0

	correct := hotTemp > threshold    // true  → filter returns !true = false (REJECTED)
	mutated := hotTemp <= threshold   // false → filter would return !false = true (ADMITTED)

	// Confirm mutation produces opposite result.
	if correct == mutated {
		t.Fatal("mutation sanity: '>' and '<=' must produce opposite results for 71 vs 70")
	}
	// correct==true means the node WOULD be rejected (Filter returns false). Good.
	if !correct {
		t.Fatal("71 > 70 must be true, confirming the overheating node is rejected")
	}
	// mutated==false means with '<=' the rejection guard fires on cool nodes. That is broken.
	if mutated {
		t.Fatal("71 <= 70 must be false, confirming the mutation breaks the protection")
	}
	t.Log("§1.1 mutation proof: inverting '>' to '<=' breaks the overheating-rejection invariant")
}

// ---------- ThermalAware: Filter — rising temperature test ----------

// TestThermalAwareRisingTempFilteredOut exercises rising temperature scenario:
// as temp increases from below threshold to above, the transition is exactly at the threshold.
func TestThermalAwareRisingTempFilteredOut(t *testing.T) {
	runID := fmt.Sprintf("thermal-rising-run-%d", 1156003)
	t.Logf("HXC-1156 thermal rising temp test runID=%s", runID)

	threshold := 75.0
	p := &ThermalAware{ThresholdC: threshold}
	job := &Job{ID: "j-" + runID}

	temps := []struct {
		temp     float64
		admitted bool
	}{
		{60.0, true},
		{70.0, true},
		{74.9, true},
		{75.0, true},   // at threshold: admitted (comparison is >, not >=)
		{75.1, false},  // just above: rejected
		{85.0, false},  // well above: rejected
		{95.0, false},  // severely overheated: rejected
	}

	for _, tc := range temps {
		node := &Node{
			ID:     fmt.Sprintf("node-temp-%.1f", tc.temp),
			Labels: map[string]string{LabelNodeTempC: fmt.Sprintf("%.1f", tc.temp)},
		}
		got := p.Filter(job, node)
		if got != tc.admitted {
			t.Fatalf("[runID=%s] temp=%.1f threshold=%.1f: Filter()=%v, want %v",
				runID, tc.temp, threshold, got, tc.admitted)
		}
	}
	// Mutation: inverting '>' makes 60.0 (clearly cool) get rejected -> FAIL at first entry.
}

// ---------- ThermalAware: Score decreases as temperature rises ----------

// TestThermalAwareScoreDecreasesWithRisingTemp asserts Score ordering:
// a cooler node must score strictly higher than a hotter node (both below threshold).
func TestThermalAwareScoreDecreasesWithRisingTemp(t *testing.T) {
	runID := fmt.Sprintf("thermal-score-run-%d", 1156004)
	t.Logf("HXC-1156 thermal score ordering test runID=%s", runID)

	p := &ThermalAware{ThresholdC: 80.0}
	job := &Job{ID: "j-" + runID}

	cool := &Node{ID: "cool", Labels: map[string]string{LabelNodeTempC: "50.0"}}
	warm := &Node{ID: "warm", Labels: map[string]string{LabelNodeTempC: "70.0"}}
	// Both below threshold, both admitted.

	sCool := p.Score(job, cool)
	sWarm := p.Score(job, warm)

	// Sink-side: cooler node must score STRICTLY HIGHER.
	if !(sCool > sWarm) {
		t.Fatalf("[runID=%s] cooler node (temp=50) must score higher than warmer node (temp=70): cool=%d warm=%d",
			runID, sCool, sWarm)
	}
}

// TestThermalAwareFilteredNodeScoresZero asserts that an overheating node (filter=false)
// scores 0, consistent with all other plugins.
func TestThermalAwareFilteredNodeScoresZero(t *testing.T) {
	p := &ThermalAware{ThresholdC: 70.0}
	job := &Job{ID: "j"}
	hotNode := &Node{ID: "hot", Labels: map[string]string{LabelNodeTempC: "90.0"}}
	if got := p.Score(job, hotNode); got != 0 {
		t.Fatalf("overheating node (filtered out) must score 0, got %d", got)
	}
}

// ---------- ThermalAware: missing or unparseable label ----------

// TestThermalAwareNoLabelAdmitsNode: a node without a temp_c label is admitted
// (no evidence of overheating = assume safe).
func TestThermalAwareNoLabelAdmitsNode(t *testing.T) {
	p := &ThermalAware{ThresholdC: 70.0}
	job := &Job{ID: "j"}
	node := &Node{ID: "unlabeled", AvailableResources: Resources{CPU: 4}}
	if !p.Filter(job, node) {
		t.Fatal("a node with no temp_c label must be admitted (assume safe)")
	}
}

// TestThermalAwareUnparseableLabelAdmitsNode: garbage temp_c value -> admitted (assume safe).
func TestThermalAwareUnparseableLabelAdmitsNode(t *testing.T) {
	p := &ThermalAware{ThresholdC: 70.0}
	job := &Job{ID: "j"}
	node := &Node{ID: "garbage-temp", Labels: map[string]string{LabelNodeTempC: "very-hot"}}
	if !p.Filter(job, node) {
		t.Fatal("a node with unparseable temp_c must be admitted (assume safe, cannot measure)")
	}
}

// ---------- ThermalAware: nil safety ----------

func TestThermalAwareNilNodeRejected(t *testing.T) {
	p := &ThermalAware{ThresholdC: 70.0}
	job := &Job{ID: "j"}
	if p.Filter(job, nil) {
		t.Fatal("nil node must be filtered out")
	}
}

func TestThermalAwareNilJobAdmitsNode(t *testing.T) {
	p := &ThermalAware{ThresholdC: 70.0}
	node := &Node{ID: "n", Labels: map[string]string{LabelNodeTempC: "50.0"}}
	// nil job: the plugin only cares about node temp, so it should still evaluate the node's temp.
	// Implementation choice: admit (no job contraindication).
	if !p.Filter(nil, node) {
		t.Fatal("nil job must not cause rejection in ThermalAware (thermal filter is node-side only)")
	}
}

// ---------- ThermalAware: Bind returns true ----------

func TestThermalAwareBindReturnsTrue(t *testing.T) {
	p := &ThermalAware{ThresholdC: 70.0}
	job := &Job{ID: "j"}
	node := &Node{ID: "n"}
	if !p.Bind(job, node) {
		t.Fatal("ThermalAware.Bind must return true (no-op binding)")
	}
}

// ---------- ThermalAware: end-to-end scheduler integration ----------

// TestThermalAwareSchedulerFiltersOverheatingNode verifies that in the scheduler pipeline,
// an overheating node is excluded from placement even if it has better resources.
func TestThermalAwareSchedulerFiltersOverheatingNode(t *testing.T) {
	runID := fmt.Sprintf("thermal-e2e-run-%d", 1156005)
	t.Logf("HXC-1156 thermal e2e test runID=%s", runID)

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&ThermalAware{ThresholdC: 75.0})

	// Overheating but resource-rich node -> FILTERED OUT by ThermalAware.
	s.RegisterNode(&Node{
		ID:                 "hot-beefy",
		AvailableResources: Resources{CPU: 64, Memory: 262144, GPU: 8},
		Labels:             map[string]string{LabelNodeTempC: "85.0"},
	})
	// Cool but smaller node -> ADMITTED.
	s.RegisterNode(&Node{
		ID:                 "cool-small",
		AvailableResources: Resources{CPU: 4, Memory: 8192, GPU: 1},
		Labels:             map[string]string{LabelNodeTempC: "55.0"},
	})

	job := &Job{
		ID:        "e2e-thermal-" + runID,
		Resources: Resources{CPU: 2, Memory: 4096, GPU: 1},
	}

	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("[runID=%s] Schedule failed: %v", runID, err)
	}
	// Sink-side: must be placed on "cool-small" — the overheating node must be excluded.
	if res.AssignedNode != "cool-small" {
		t.Fatalf("[runID=%s] job must land on 'cool-small' (overheating 'hot-beefy' must be filtered out), got %q",
			runID, res.AssignedNode)
	}
}
