package scheduler

import "testing"

// ---------- CostAwareGPUPlacement: identity ----------

func TestCostAwareGPUName(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	if got := p.Name(); got != "CostAwareGPUPlacement" {
		t.Fatalf("Name() = %q, want CostAwareGPUPlacement", got)
	}
	// Mutation: returning any other string -> FAIL.
}

// It must satisfy the Plugin interface (backward-compatible additive API).
var _ Plugin = (*CostAwareGPUPlacement)(nil)

// ---------- Filter: GPU adequacy ----------

func TestCostAwareGPUFilterRejectsInsufficientGPU(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 2}}
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 1}}
	if p.Filter(job, node) {
		t.Fatal("node with 1 GPU must be filtered OUT for a 2-GPU job")
	}
	// Mutation: changing '>=' to '<=' (or always returning true) -> FAIL.
}

func TestCostAwareGPUFilterAcceptsExactGPU(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 2}}
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 2}}
	if !p.Filter(job, node) {
		t.Fatal("node with exactly 2 GPUs must satisfy a 2-GPU job")
	}
	// Mutation: changing '>=' to '>' -> FAIL (exact match rejected).
}

func TestCostAwareGPUFilterNonGPUJobAlwaysPasses(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{CPU: 4}}
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 0}}
	if !p.Filter(job, node) {
		t.Fatal("non-GPU job must not be filtered out by GPU adequacy")
	}
	// Mutation: removing the 'job.Resources.GPU <= 0' early return -> FAIL.
}

func TestCostAwareGPUFilterNilNode(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	if p.Filter(&Job{ID: "j", Resources: Resources{GPU: 1}}, nil) {
		t.Fatal("nil node must be filtered out")
	}
	// Mutation: dropping the nil check -> nil-deref panic / FAIL.
}

// ---------- Score: cheaper adequate node scores strictly higher ----------

func TestCostAwareGPUCheaperExplicitPriceScoresHigher(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 1}}
	cheap := &Node{ID: "cheap", AvailableResources: Resources{GPU: 1},
		Labels: map[string]string{LabelCostPerGPUHour: "1.50"}}
	pricey := &Node{ID: "pricey", AvailableResources: Resources{GPU: 1},
		Labels: map[string]string{LabelCostPerGPUHour: "9.00"}}

	cs := p.Score(job, cheap)
	ps := p.Score(job, pricey)
	if !(cs > ps) {
		t.Fatalf("cheaper node must score strictly higher: cheap=%d pricey=%d", cs, ps)
	}
	// Concrete arithmetic, not just ordering:
	// score = 1_000_000 - cost*1000.
	if cs != costScoreBase-1500 {
		t.Fatalf("cheap score = %d, want %d", cs, costScoreBase-1500)
	}
	if ps != costScoreBase-9000 {
		t.Fatalf("pricey score = %d, want %d", ps, costScoreBase-9000)
	}
	// Mutation: flipping 'costScoreBase - cost' to 'costScoreBase + cost'
	// inverts ordering -> cs > ps assertion FAIL. Also the exact-value
	// assertions FAIL on any change to the cost formula/weights.
}

func TestCostAwareGPUScoreScalesWithGPUCount(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 4},
		Labels: map[string]string{LabelCostPerGPUHour: "2.00"}}

	one := p.Score(&Job{ID: "j1", Resources: Resources{GPU: 1}}, node)
	two := p.Score(&Job{ID: "j2", Resources: Resources{GPU: 2}}, node)

	// Two GPUs cost twice as much, so the 2-GPU placement scores lower.
	if !(one > two) {
		t.Fatalf("placing 2 GPUs must cost more (score lower) than 1: one=%d two=%d", one, two)
	}
	if one != costScoreBase-2000 { // 2.00 * 1 GPU * 1000
		t.Fatalf("1-GPU score = %d, want %d", one, costScoreBase-2000)
	}
	if two != costScoreBase-4000 { // 2.00 * 2 GPU * 1000
		t.Fatalf("2-GPU score = %d, want %d", two, costScoreBase-4000)
	}
	// Mutation: dropping the 'v * float64(gpusNeeded)' multiply (charging a
	// flat per-node price) makes one == two -> FAIL.
}

// ---------- Score: whole-node price amortised across GPUs ----------

func TestCostAwareGPUAmortisesWholeNodePrice(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 1}}

	// Same whole-node price, but more GPUs => cheaper per GPU => higher score.
	dense := &Node{ID: "dense", AvailableResources: Resources{GPU: 8},
		Labels: map[string]string{LabelCostPerHour: "16.00"}} // 2.00/GPU
	sparse := &Node{ID: "sparse", AvailableResources: Resources{GPU: 2},
		Labels: map[string]string{LabelCostPerHour: "16.00"}} // 8.00/GPU

	ds := p.Score(job, dense)
	ss := p.Score(job, sparse)
	if !(ds > ss) {
		t.Fatalf("denser node amortises the node price better and must score higher: dense=%d sparse=%d", ds, ss)
	}
	if ds != costScoreBase-2000 { // 16/8 * 1000
		t.Fatalf("dense score = %d, want %d", ds, costScoreBase-2000)
	}
	if ss != costScoreBase-8000 { // 16/2 * 1000
		t.Fatalf("sparse score = %d, want %d", ss, costScoreBase-8000)
	}
	// Mutation: not dividing by gpusOnNode (using the raw node price) makes
	// ds == ss -> FAIL.
}

func TestCostAwareGPUPerGPULabelBeatsWholeNodeLabel(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 1}}
	// Both labels present: per-GPU label must take precedence.
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 4},
		Labels: map[string]string{
			LabelCostPerGPUHour: "3.00",   // -> used
			LabelCostPerHour:    "100.00", // -> ignored (would amortise to 25.00)
		}}
	got := p.Score(job, node)
	if got != costScoreBase-3000 {
		t.Fatalf("per-GPU label must win: score = %d, want %d", got, costScoreBase-3000)
	}
	// Mutation: checking LabelCostPerHour before LabelCostPerGPUHour -> FAIL.
}

// ---------- Score: derived proxy when no price label exists ----------

func TestCostAwareGPUDerivedProxyPrefersLeanerNode(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 1}}
	// No cost labels: richer node is treated as more expensive.
	lean := &Node{ID: "lean", AvailableResources: Resources{CPU: 2, Memory: 1024, GPU: 1}}
	rich := &Node{ID: "rich", AvailableResources: Resources{CPU: 64, Memory: 262144, GPU: 8}}

	ls := p.Score(job, lean)
	rs := p.Score(job, rich)
	if !(ls > rs) {
		t.Fatalf("leaner adequate node must score higher under derived proxy: lean=%d rich=%d", ls, rs)
	}
	// lean proxy = 2 + 1024/1024 + 1*10 = 13 -> score base-13000
	if ls != costScoreBase-13000 {
		t.Fatalf("lean derived score = %d, want %d", ls, costScoreBase-13000)
	}
	// rich proxy = 64 + 262144/1024 + 8*10 = 64+256+80 = 400 -> base-400000
	if rs != costScoreBase-400000 {
		t.Fatalf("rich derived score = %d, want %d", rs, costScoreBase-400000)
	}
	// Mutation: zeroing any term (e.g. dropping the GPU*10 weight) changes
	// these exact values -> FAIL.
}

// ---------- Score: filtered node scores 0 ----------

func TestCostAwareGPUFilteredNodeScoresZero(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 4}}
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 1},
		Labels: map[string]string{LabelCostPerGPUHour: "0.01"}}
	if got := p.Score(job, node); got != 0 {
		t.Fatalf("inadequate node must score 0, got %d", got)
	}
	// Mutation: removing the '!Filter -> 0' guard would return a high score
	// for a node that cannot host the job -> FAIL.
}

// ---------- Score: monotonic w.r.t. cost proxy ----------

func TestCostAwareGPUScoreMonotonicInCost(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 1}}
	costs := []string{"0.10", "0.50", "1.00", "4.00", "10.00"}
	prev := costScoreBase + 1 // strictly greater than any real score
	for _, c := range costs {
		node := &Node{ID: "n", AvailableResources: Resources{GPU: 1},
			Labels: map[string]string{LabelCostPerGPUHour: c}}
		s := p.Score(job, node)
		if !(s < prev) {
			t.Fatalf("score must strictly decrease as cost rises: cost=%s score=%d prev=%d", c, s, prev)
		}
		prev = s
	}
	// Mutation: making score independent of cost (constant) breaks strict
	// monotonicity -> FAIL.
}

// ---------- Ties are deterministic ----------

func TestCostAwareGPUTiesDeterministic(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 1}}
	a := &Node{ID: "a", AvailableResources: Resources{GPU: 1},
		Labels: map[string]string{LabelCostPerGPUHour: "2.50"}}
	b := &Node{ID: "b", AvailableResources: Resources{GPU: 1},
		Labels: map[string]string{LabelCostPerGPUHour: "2.50"}}
	if p.Score(job, a) != p.Score(job, b) {
		t.Fatal("equal-cost nodes must score equally (deterministic ties)")
	}
	// Repeated evaluation is stable (no randomness/map iteration in score).
	first := p.Score(job, a)
	for i := 0; i < 100; i++ {
		if p.Score(job, a) != first {
			t.Fatalf("score not stable across calls at iter %d", i)
		}
	}
	// Mutation: introducing node.ID hashing or rand into the score -> FAIL.
}

// ---------- Bad/negative labels fall back to derived proxy ----------

func TestCostAwareGPUUnparseableLabelFallsBack(t *testing.T) {
	p := &CostAwareGPUPlacement{}
	job := &Job{ID: "j", Resources: Resources{GPU: 1}}
	// Garbage and negative labels are ignored; derived proxy used instead.
	garbage := &Node{ID: "g", AvailableResources: Resources{CPU: 2, Memory: 1024, GPU: 1},
		Labels: map[string]string{LabelCostPerGPUHour: "not-a-number"}}
	negative := &Node{ID: "neg", AvailableResources: Resources{CPU: 2, Memory: 1024, GPU: 1},
		Labels: map[string]string{LabelCostPerGPUHour: "-5.00"}}

	want := costScoreBase - 13000 // derived proxy: 2 + 1 + 10 = 13
	if got := p.Score(job, garbage); got != want {
		t.Fatalf("garbage label must fall back to derived proxy: got %d want %d", got, want)
	}
	if got := p.Score(job, negative); got != want {
		t.Fatalf("negative label must be rejected and fall back: got %d want %d", got, want)
	}
	// Mutation: trusting strconv.ParseFloat without the err/'<0' check would
	// either panic-free-misbehave or accept a negative price -> FAIL.
}

// ---------- End-to-end: scheduler picks the cheaper adequate node ----------

func TestCostAwareGPUSchedulerPicksCheaperNode(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})      // resource accounting + GPU fit
	s.AddPlugin(&CostAwareGPUPlacement{}) // cost preference

	// Two adequate GPU nodes at different prices, plus one too small.
	s.RegisterNode(&Node{ID: "expensive", AvailableResources: Resources{CPU: 8, Memory: 8192, GPU: 2},
		Labels: map[string]string{LabelCostPerGPUHour: "8.00"}})
	s.RegisterNode(&Node{ID: "cheap", AvailableResources: Resources{CPU: 8, Memory: 8192, GPU: 2},
		Labels: map[string]string{LabelCostPerGPUHour: "2.00"}})
	s.RegisterNode(&Node{ID: "tiny", AvailableResources: Resources{CPU: 8, Memory: 8192, GPU: 1},
		Labels: map[string]string{LabelCostPerGPUHour: "0.01"}})

	job := &Job{ID: "j", Resources: Resources{CPU: 1, Memory: 1024, GPU: 2}}
	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if res.AssignedNode != "cheap" {
		t.Fatalf("expected job placed on 'cheap' node, got %q", res.AssignedNode)
	}
	// Mutation: inverting the cost-to-score sign routes the job to
	// 'expensive' -> FAIL. (The cheap-but-too-small 'tiny' node is Filtered
	// out by both plugins, proving Filter participates in the pipeline.)
}
