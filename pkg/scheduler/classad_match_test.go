package scheduler

import (
	"errors"
	"testing"
)

// ClassAdMatch must satisfy the Plugin interface (backward-compatible additive API).
var _ Plugin = (*ClassAdMatch)(nil)

func TestClassAdMatchName(t *testing.T) {
	p := &ClassAdMatch{}
	if got := p.Name(); got != "ClassAdMatch" {
		t.Fatalf("Name() = %q, want ClassAdMatch", got)
	}
	// Mutation: returning any other string -> FAIL.
}

// ---------- Filter: Requirements satisfaction ----------

// gpuJob builds a job requiring GPU >= 2 via the public side-channel.
func gpuJob() *Job {
	j := &Job{ID: "gpu-job", Resources: Resources{}}
	SetRequirements(j, "GPU >= 2")
	return j
}

func TestClassAdFilterRejectsNodeBelowRequirement(t *testing.T) {
	p := &ClassAdMatch{}
	node := &Node{ID: "n1", AvailableResources: Resources{GPU: 1}}
	if p.Filter(gpuJob(), node) {
		t.Fatal("node with GPU=1 must be filtered OUT for a job requiring GPU>=2")
	}
	// Mutation: a Filter that ignores Requirements (always returns true)
	// -> this assertion FAILS. Also flipping '>=' semantics in classads,
	// or swallowing the Match result, fails here.
}

func TestClassAdFilterKeepsNodeMeetingRequirement(t *testing.T) {
	p := &ClassAdMatch{}
	node := &Node{ID: "n2", AvailableResources: Resources{GPU: 4}}
	if !p.Filter(gpuJob(), node) {
		t.Fatal("node with GPU=4 must satisfy a job requiring GPU>=2")
	}
	// Mutation: a Filter that always returns false (or rejects on any
	// expression) -> FAIL.
}

func TestClassAdFilterUsesExplicitLabelAttribute(t *testing.T) {
	p := &ClassAdMatch{}
	j := &Job{ID: "j"}
	// Requirement references a label attribute, not a synthesised resource.
	SetRequirements(j, `arch == "x86_64" && cuda >= 12`)
	good := &Node{ID: "good", Labels: map[string]string{"arch": "x86_64", "cuda": "12.4"}}
	bad := &Node{ID: "bad", Labels: map[string]string{"arch": "arm64", "cuda": "12.4"}}
	if !p.Filter(j, good) {
		t.Fatal("x86_64/cuda12.4 node must satisfy the requirement")
	}
	if p.Filter(j, bad) {
		t.Fatal("arm64 node must be filtered OUT by arch == \"x86_64\"")
	}
	// Mutation: dropping coerceAttr (so "12.4" stays a string and `cuda >= 12`
	// errors -> reject) would wrongly reject the GOOD node -> FAIL.
}

func TestClassAdFilterNoRequirementsPassesEveryNode(t *testing.T) {
	p := &ClassAdMatch{}
	job := &Job{ID: "plain"}
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 0}}
	if !p.Filter(job, node) {
		t.Fatal("a job without ClassAd Requirements must not be filtered by this plugin")
	}
	// Mutation: removing the empty-expr early return (so an empty expression
	// is parsed and errors -> reject) -> FAIL.
}

func TestClassAdFilterNilNode(t *testing.T) {
	p := &ClassAdMatch{}
	if p.Filter(gpuJob(), nil) {
		t.Fatal("nil node must be filtered out")
	}
	// Mutation: dropping the nil guard -> nil-map deref / wrong admit -> FAIL.
}

// ---------- Filter: malformed Requirements -> deterministic reject + error ----------

func TestClassAdFilterMalformedRequirementsRejects(t *testing.T) {
	p := &ClassAdMatch{}
	j := &Job{ID: "bad"}
	SetRequirements(j, "GPU >=") // syntactically incomplete
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 8}}
	if p.Filter(j, node) {
		t.Fatal("a malformed Requirements expression must deterministically reject the node")
	}
	// Mutation: 'return err == nil' style that admits on parse error -> FAIL.
}

func TestClassAdRequirementsErrorReportsMalformed(t *testing.T) {
	p := &ClassAdMatch{}
	j := &Job{ID: "bad"}
	SetRequirements(j, "GPU >=")
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 8}}
	err := p.RequirementsError(j, node)
	if err == nil {
		t.Fatal("RequirementsError must report a non-nil error for a malformed expression")
	}
	var cae *ClassAdError
	if !errors.As(err, &cae) {
		t.Fatalf("error must be *ClassAdError, got %T", err)
	}
	// Mutation: returning nil unconditionally -> FAIL.
}

func TestClassAdRequirementsErrorNilWhenSatisfiable(t *testing.T) {
	p := &ClassAdMatch{}
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 1}}
	// Valid expression that simply evaluates to false: NOT an error.
	if err := p.RequirementsError(gpuJob(), node); err != nil {
		t.Fatalf("a well-formed but unsatisfied requirement must NOT be an error, got %v", err)
	}
	// Mutation: treating Filter==false as an error -> FAIL.
}

// ---------- Score: Rank ordering ----------

func TestClassAdScoreRanksHigherNodeStrictlyAbove(t *testing.T) {
	p := &ClassAdMatch{}
	j := &Job{ID: "rank-job"}
	SetRequirements(j, "GPU >= 1")
	SetRank(j, "GPU") // more GPUs => higher rank

	low := &Node{ID: "low", AvailableResources: Resources{GPU: 2}}
	high := &Node{ID: "high", AvailableResources: Resources{GPU: 6}}

	sLow := p.Score(j, low)
	sHigh := p.Score(j, high)
	if !(sHigh > sLow) {
		t.Fatalf("higher-rank node must score strictly higher: high=%d low=%d", sHigh, sLow)
	}
	// Mutation: a Score that ignores Rank (returns a constant) -> sHigh==sLow -> FAIL.
}

func TestClassAdScoreCompositeRankExpression(t *testing.T) {
	p := &ClassAdMatch{}
	j := &Job{ID: "j"}
	SetRequirements(j, "true")
	SetRank(j, "GPU * 100 + CPU") // weight GPUs heavily over CPU

	// Node A: more CPU, fewer GPUs. Node B: more GPUs. B must win.
	a := &Node{ID: "a", AvailableResources: Resources{CPU: 50, GPU: 1}}
	b := &Node{ID: "b", AvailableResources: Resources{CPU: 1, GPU: 2}}
	if !(p.Score(j, b) > p.Score(j, a)) {
		t.Fatalf("GPU-weighted rank must prefer the higher-GPU node: a=%d b=%d", p.Score(j, a), p.Score(j, b))
	}
	// Mutation: evaluating Rank against the JOB instead of the node attrs,
	// or ignoring the arithmetic, collapses the ordering -> FAIL.
}

func TestClassAdScoreZeroWhenFilteredOut(t *testing.T) {
	p := &ClassAdMatch{}
	j := &Job{ID: "j"}
	SetRequirements(j, "GPU >= 8")
	SetRank(j, "GPU")
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 1}} // fails Filter
	if got := p.Score(j, node); got != 0 {
		t.Fatalf("a node that fails Filter must score 0, got %d", got)
	}
	// Mutation: scoring before checking Filter -> returns 1000 -> FAIL.
}

func TestClassAdScoreZeroWhenNoRank(t *testing.T) {
	p := &ClassAdMatch{}
	j := &Job{ID: "j"}
	SetRequirements(j, "GPU >= 1")
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 4}}
	if got := p.Score(j, node); got != 0 {
		t.Fatalf("no Rank expression must contribute 0, got %d", got)
	}
	// Mutation: defaulting to a non-zero score when Rank is absent -> FAIL.
}

func TestClassAdScoreMalformedRankIsZeroNotPanic(t *testing.T) {
	p := &ClassAdMatch{}
	j := &Job{ID: "j"}
	SetRequirements(j, "GPU >= 1")
	SetRank(j, "GPU +") // malformed
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 4}}
	if got := p.Score(j, node); got != 0 {
		t.Fatalf("malformed Rank must yield 0 (no panic), got %d", got)
	}
	// Mutation: ignoring the eval error -> panic / nonzero -> FAIL.
}

// ---------- End-to-end: scheduler picks the ClassAd-preferred node ----------

func TestClassAdEndToEndSchedulerPicksPreferredNode(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&ClassAdMatch{})

	// Three nodes, all GPU-capable; only two satisfy GPU>=2; among those the
	// Rank "GPU" must steer the binding onto the richer one.
	tiny := &Node{ID: "tiny", AvailableResources: Resources{GPU: 1}} // filtered out
	mid := &Node{ID: "mid", AvailableResources: Resources{GPU: 2}}   // satisfies, lower rank
	big := &Node{ID: "big", AvailableResources: Resources{GPU: 7}}   // satisfies, higher rank
	s.RegisterNode(tiny)
	s.RegisterNode(mid)
	s.RegisterNode(big)

	job := &Job{ID: "e2e"}
	SetRequirements(job, "GPU >= 2")
	SetRank(job, "GPU")

	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("Schedule returned error: %v", err)
	}
	if res.AssignedNode != "big" {
		t.Fatalf("scheduler must bind to the ClassAd-preferred node 'big', got %q", res.AssignedNode)
	}
	if job.Status != JobStatusScheduled {
		t.Fatalf("job status = %q, want scheduled", job.Status)
	}
	// Mutation: a Filter that ignores Requirements admits 'tiny' but Rank still
	// favors 'big'; instead, a Score that ignores Rank lets 'tiny'/'mid' tie
	// and be picked -> assignment != 'big' -> FAIL.
}

func TestClassAdEndToEndRejectsWhenNoNodeSatisfies(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&ClassAdMatch{})
	s.RegisterNode(&Node{ID: "a", AvailableResources: Resources{GPU: 1}})
	s.RegisterNode(&Node{ID: "b", AvailableResources: Resources{GPU: 0}})

	job := &Job{ID: "impossible"}
	SetRequirements(job, "GPU >= 4")

	if _, err := s.Schedule(job); !errors.Is(err, ErrNoNodeAvailable) {
		t.Fatalf("expected ErrNoNodeAvailable when no node satisfies Requirements, got %v", err)
	}
	// Mutation: a Filter that admits sub-requirement nodes -> a node is bound
	// and err is nil -> FAIL.
}

// ---------- Backward-compatibility: existing plugins still compose ----------

func TestClassAdComposesWithNodeResourcesFit(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&ClassAdMatch{})

	// Both nodes have the raw resources, but only 'east' carries the required
	// region label; ClassAdMatch must veto 'west' even though NodeResourcesFit
	// would accept it.
	east := &Node{ID: "east", AvailableResources: Resources{CPU: 8, GPU: 2}, Labels: map[string]string{"region": "us-east"}}
	west := &Node{ID: "west", AvailableResources: Resources{CPU: 8, GPU: 2}, Labels: map[string]string{"region": "us-west"}}
	s.RegisterNode(east)
	s.RegisterNode(west)

	job := &Job{ID: "regional", Resources: Resources{CPU: 4, GPU: 1}}
	SetRequirements(job, `region == "us-east"`)

	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("Schedule error: %v", err)
	}
	if res.AssignedNode != "east" {
		t.Fatalf("region requirement must pin the job to 'east', got %q", res.AssignedNode)
	}
	// NodeResourcesFit must have actually consumed resources on east.
	got, _ := s.GetNode("east")
	if got.AvailableResources.GPU != 1 {
		t.Fatalf("NodeResourcesFit must still bind/consume: east GPU = %d, want 1", got.AvailableResources.GPU)
	}
	// Mutation: ClassAdMatch.Filter ignoring Requirements would let 'west' tie
	// and possibly win -> assignment flaps off 'east' -> FAIL.
}
