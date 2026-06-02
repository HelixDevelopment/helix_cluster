package scheduler

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newLatencyNode(id string, latencyMs string, preemptible string) *Node {
	labels := map[string]string{}
	if latencyMs != "" {
		labels[LabelLatencyMs] = latencyMs
	}
	if preemptible != "" {
		labels[LabelPreemptible] = preemptible
	}
	return &Node{ID: id, Labels: labels}
}

func newLatencyJob(id string, latencySensitive string) *Job {
	labels := map[string]string{}
	if latencySensitive != "" {
		labels[LabelLatencySensitive] = latencySensitive
	}
	return &Job{ID: id, Labels: labels}
}

// ---------------------------------------------------------------------------
// Plugin interface compliance
// ---------------------------------------------------------------------------

func TestLatencySpotPolicy_Name(t *testing.T) {
	p := &LatencySpotPolicy{}
	if got := p.Name(); got != "LatencySpotPolicy" {
		t.Fatalf("Name() = %q, want %q", got, "LatencySpotPolicy")
	}
}

func TestLatencySpotPolicy_Bind(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j1", "")
	node := newLatencyNode("n1", "10", "false")
	if !p.Bind(job, node) {
		t.Fatal("Bind() must always return true")
	}
}

// ---------------------------------------------------------------------------
// ANTI-BLUFF bite 1: lower latency => strictly higher score
//
// Two nodes identical except for latency_ms. The node with smaller latency_ms
// MUST score strictly higher.
//
// Mutation resistance: flipping the latency penalty to addition (score +=
// latency*k) would make the higher-latency node score higher — this test
// catches the regression.
// ---------------------------------------------------------------------------

func TestLatencySpotPolicy_Score_LowerLatencyScoresHigher(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j-batch", "false") // non-sensitive to isolate latency term

	lowLatNode := newLatencyNode("low", "5", "false")   // 5 ms
	highLatNode := newLatencyNode("high", "100", "false") // 100 ms

	scoreLow := p.Score(job, lowLatNode)
	scoreHigh := p.Score(job, highLatNode)

	if scoreLow <= scoreHigh {
		t.Errorf(
			"lower-latency node must score strictly higher: low-lat score=%d, high-lat score=%d",
			scoreLow, scoreHigh,
		)
	}
}

// Same bite with a latency-sensitive job — the latency ordering must hold
// regardless of job sensitivity (the extra preemptible penalty is equal on
// both nodes since both are non-preemptible).
func TestLatencySpotPolicy_Score_LowerLatencyScoresHigher_SensitiveJob(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j-sensitive", "true")

	lowLatNode := newLatencyNode("low", "1", "false")
	highLatNode := newLatencyNode("high", "200", "false")

	if p.Score(job, lowLatNode) <= p.Score(job, highLatNode) {
		t.Error("lower-latency node must score strictly higher for sensitive job")
	}
}

// ---------------------------------------------------------------------------
// ANTI-BLUFF bite 2: latency-sensitive job — preemptible node scores strictly
// LOWER than an identical non-preemptible node.
//
// Mutation resistance: removing the preemptiblePenalty subtraction makes both
// nodes score equally — the strict inequality fails.
// ---------------------------------------------------------------------------

func TestLatencySpotPolicy_Score_SensitiveJob_PreemptibleScoresLower(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j-sensitive", "true")

	// Identical latency, only preemptible differs.
	const latency = "20"
	preemptNode := newLatencyNode("spot", latency, "true")
	onDemandNode := newLatencyNode("demand", latency, "false")

	scorePreempt := p.Score(job, preemptNode)
	scoreOnDemand := p.Score(job, onDemandNode)

	if scorePreempt >= scoreOnDemand {
		t.Errorf(
			"sensitive job: preemptible node must score strictly lower: preempt=%d, on-demand=%d",
			scorePreempt, scoreOnDemand,
		)
	}
}

// Also assert the delta equals exactly preemptiblePenalty (no accidental
// rounding or double-application).
func TestLatencySpotPolicy_Score_SensitiveJob_PenaltyMagnitude(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j-sensitive", "true")

	const latency = "50"
	preemptNode := newLatencyNode("spot", latency, "true")
	onDemandNode := newLatencyNode("demand", latency, "false")

	delta := p.Score(job, onDemandNode) - p.Score(job, preemptNode)
	if delta != preemptiblePenalty {
		t.Errorf("penalty delta = %d, want preemptiblePenalty = %d", delta, preemptiblePenalty)
	}
}

// ---------------------------------------------------------------------------
// ANTI-BLUFF bite 3: non-latency-sensitive (batch) job — preemptible node
// scores >= non-preemptible (spot bonus / neutral).
// ---------------------------------------------------------------------------

func TestLatencySpotPolicy_Score_BatchJob_PreemptibleScoresHigherOrEqual(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j-batch", "false")

	const latency = "20"
	preemptNode := newLatencyNode("spot", latency, "true")
	onDemandNode := newLatencyNode("demand", latency, "false")

	scorePreempt := p.Score(job, preemptNode)
	scoreOnDemand := p.Score(job, onDemandNode)

	if scorePreempt < scoreOnDemand {
		t.Errorf(
			"batch job: preemptible node must score >= non-preemptible: preempt=%d, on-demand=%d",
			scorePreempt, scoreOnDemand,
		)
	}
}

// Batch job: preemptible node scores strictly HIGHER (spot bonus > 0).
func TestLatencySpotPolicy_Score_BatchJob_PreemptibleGetsBonus(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j-batch", "false")

	const latency = "20"
	preemptNode := newLatencyNode("spot", latency, "true")
	onDemandNode := newLatencyNode("demand", latency, "false")

	delta := p.Score(job, preemptNode) - p.Score(job, onDemandNode)
	if delta != spotBonus {
		t.Errorf("spot bonus delta = %d, want spotBonus = %d", delta, spotBonus)
	}
}

// ---------------------------------------------------------------------------
// ANTI-BLUFF bite 4: absent latency_ms label => assume-safe (no latency
// penalty). Node with absent label must NOT score worse than a node with
// latency_ms == 0.
// ---------------------------------------------------------------------------

func TestLatencySpotPolicy_Score_AbsentLatency_AssumedSafe(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j-batch", "false")

	// Node with no latency label at all.
	absentNode := newLatencyNode("absent", "", "false")
	// Node with explicit zero latency (should be tied with absent or absent wins).
	zeroLatNode := newLatencyNode("zero", "0", "false")

	scoreAbsent := p.Score(job, absentNode)
	scoreZero := p.Score(job, zeroLatNode)

	// Absent label should produce the same score as 0-ms latency (no penalty
	// in either case), so absent must NOT be worse.
	if scoreAbsent < scoreZero {
		t.Errorf(
			"absent latency_ms must not penalise the node: absent=%d, zero-latency=%d",
			scoreAbsent, scoreZero,
		)
	}
}

// More precisely: absent latency_ms produces exactly the base score (no
// penalty applied at all), whereas 0 ms produces base − 0*k = base. They
// must be equal.
func TestLatencySpotPolicy_Score_AbsentLatency_ExactBase(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j-batch", "false")

	node := newLatencyNode("n", "", "false")
	got := p.Score(job, node)
	want := latencySpotScoreBase // no latency penalty, no preemptible adjustment

	if got != want {
		t.Errorf("absent latency: score = %d, want base %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// Filter tests
// ---------------------------------------------------------------------------

// Default (no cap): Filter always returns true.
func TestLatencySpotPolicy_Filter_DefaultAlwaysAdmits(t *testing.T) {
	p := &LatencySpotPolicy{} // MaxLatencyMs == 0
	job := newLatencyJob("j", "")

	cases := []*Node{
		newLatencyNode("n1", "1000", "false"),
		newLatencyNode("n2", "", "false"),
		newLatencyNode("n3", "0", "true"),
	}
	for _, n := range cases {
		if !p.Filter(job, n) {
			t.Errorf("Filter(no cap, node %q) = false, want true", n.ID)
		}
	}
}

// Nil node always rejected.
func TestLatencySpotPolicy_Filter_NilNode(t *testing.T) {
	p := &LatencySpotPolicy{}
	if p.Filter(newLatencyJob("j", ""), nil) {
		t.Error("Filter(nil node) must return false")
	}
}

// Cap set: node with latency_ms > cap is rejected.
func TestLatencySpotPolicy_Filter_CapRejectsHighLatency(t *testing.T) {
	p := &LatencySpotPolicy{MaxLatencyMs: 50}
	job := newLatencyJob("j", "")

	slowNode := newLatencyNode("slow", "100", "false")
	if p.Filter(job, slowNode) {
		t.Error("Filter must reject node with latency_ms > MaxLatencyMs")
	}
}

// Cap set: node with latency_ms <= cap is admitted.
func TestLatencySpotPolicy_Filter_CapAdmitsFastNode(t *testing.T) {
	p := &LatencySpotPolicy{MaxLatencyMs: 50}
	job := newLatencyJob("j", "")

	fastNode := newLatencyNode("fast", "20", "false")
	if !p.Filter(job, fastNode) {
		t.Error("Filter must admit node with latency_ms <= MaxLatencyMs")
	}
}

// Cap set: absent latency_ms is assume-safe (admitted).
func TestLatencySpotPolicy_Filter_CapAbsentLatencyAdmitted(t *testing.T) {
	p := &LatencySpotPolicy{MaxLatencyMs: 10}
	job := newLatencyJob("j", "")

	noLatNode := newLatencyNode("noLat", "", "false")
	if !p.Filter(job, noLatNode) {
		t.Error("Filter must admit node with absent latency_ms (assume-safe)")
	}
}

// ---------------------------------------------------------------------------
// Score edge cases
// ---------------------------------------------------------------------------

// Nil node scores 0.
func TestLatencySpotPolicy_Score_NilNode(t *testing.T) {
	p := &LatencySpotPolicy{}
	if got := p.Score(newLatencyJob("j", ""), nil); got != 0 {
		t.Errorf("Score(nil node) = %d, want 0", got)
	}
}

// Nil job — absent job labels treated as non-sensitive (batch path).
func TestLatencySpotPolicy_Score_NilJob_BatchPath(t *testing.T) {
	p := &LatencySpotPolicy{}
	node := newLatencyNode("n", "20", "true")

	// nil job => isLatencySensitive returns false => spot bonus applies
	scoreNilJob := p.Score(nil, node)
	scoreBatchJob := p.Score(newLatencyJob("j", "false"), node)

	if scoreNilJob != scoreBatchJob {
		t.Errorf(
			"nil job should behave as batch job: nil=%d, batch=%d",
			scoreNilJob, scoreBatchJob,
		)
	}
}

// Verify that the ordering across three nodes with increasing latency is
// strictly monotone decreasing (comprehensive ordering check).
func TestLatencySpotPolicy_Score_MonotonicallyDecreasesWithLatency(t *testing.T) {
	p := &LatencySpotPolicy{}
	job := newLatencyJob("j", "false")

	n1 := newLatencyNode("n1", "1", "false")   // 1 ms
	n2 := newLatencyNode("n2", "50", "false")  // 50 ms
	n3 := newLatencyNode("n3", "500", "false") // 500 ms

	s1 := p.Score(job, n1)
	s2 := p.Score(job, n2)
	s3 := p.Score(job, n3)

	if !(s1 > s2 && s2 > s3) {
		t.Errorf("scores must be strictly decreasing with latency: s1=%d s2=%d s3=%d", s1, s2, s3)
	}
}

// Sensitive + preemptible node must score lower than sensitive + non-preemptible
// AND lower than batch + preemptible (the largest penalty case).
func TestLatencySpotPolicy_Score_SensitivePreemptible_LowestScore(t *testing.T) {
	p := &LatencySpotPolicy{}

	const latency = "10"
	sensitivePreempt := p.Score(newLatencyJob("j", "true"), newLatencyNode("n", latency, "true"))
	sensitiveOnDemand := p.Score(newLatencyJob("j", "true"), newLatencyNode("n", latency, "false"))
	batchPreempt := p.Score(newLatencyJob("j", "false"), newLatencyNode("n", latency, "true"))
	batchOnDemand := p.Score(newLatencyJob("j", "false"), newLatencyNode("n", latency, "false"))

	// sensitive+preempt must be the worst of the four configurations.
	if sensitivePreempt >= sensitiveOnDemand {
		t.Errorf("sensitivePreempt(%d) must be < sensitiveOnDemand(%d)", sensitivePreempt, sensitiveOnDemand)
	}
	if sensitivePreempt >= batchOnDemand {
		t.Errorf("sensitivePreempt(%d) must be < batchOnDemand(%d)", sensitivePreempt, batchOnDemand)
	}
	// batchPreempt must be the best (base + spotBonus - latencyPenalty)
	if batchPreempt <= batchOnDemand {
		t.Errorf("batchPreempt(%d) must be > batchOnDemand(%d)", batchPreempt, batchOnDemand)
	}
}

// Implements Plugin interface (compile-time check via assignment to typed var).
func TestLatencySpotPolicy_ImplementsPlugin(t *testing.T) {
	var _ Plugin = &LatencySpotPolicy{}
}
