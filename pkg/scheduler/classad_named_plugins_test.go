package scheduler

import (
	"testing"
)

// HXC-1212: ClassAdFilterPlugin and ClassAdScorePlugin must satisfy Plugin interface.
var _ Plugin = (*ClassAdFilterPlugin)(nil)
var _ Plugin = (*ClassAdScorePlugin)(nil)

// ---------- ClassAdFilterPlugin: identity ----------

func TestClassAdFilterPluginName(t *testing.T) {
	p := &ClassAdFilterPlugin{}
	if got := p.Name(); got != "ClassAdFilterPlugin" {
		t.Fatalf("Name() = %q, want ClassAdFilterPlugin", got)
	}
	// Mutation: any other string -> FAIL.
}

// ---------- ClassAdFilterPlugin: Filter delegates Requirements ----------

// TestClassAdFilterPluginRejectsNonGPUNode is the canonical sink-side test.
// Requirements="GPU >= 1": a node without GPU must be filtered OUT; a GPU node admitted.
func TestClassAdFilterPluginRejectsNonGPUNode(t *testing.T) {
	p := &ClassAdFilterPlugin{}

	job := &Job{ID: "gpu-job"}
	SetRequirements(job, "GPU >= 1")

	noGPU := &Node{ID: "no-gpu", AvailableResources: Resources{GPU: 0, CPU: 4, Memory: 8192}}
	withGPU := &Node{ID: "with-gpu", AvailableResources: Resources{GPU: 2, CPU: 4, Memory: 8192}}

	// Sink-side assertion 1: non-GPU node MUST be rejected.
	if p.Filter(job, noGPU) {
		t.Fatal("ClassAdFilterPlugin must REJECT a node with GPU=0 when Requirements='GPU >= 1'")
	}
	// Sink-side assertion 2: GPU node MUST be admitted.
	if !p.Filter(job, withGPU) {
		t.Fatal("ClassAdFilterPlugin must ADMIT a node with GPU=2 when Requirements='GPU >= 1'")
	}
	// Mutation: flip '>=' to '<' in the Requirements expression (see
	// TestClassAdFilterPlugin_MutationFlipGEToLT) and this FAILS.
}

// TestClassAdFilterPlugin_MutationFlipGEToLT documents the §1.1 mutation.
// When the test uses "GPU < 1" instead of "GPU >= 1", the filter-out assertion
// must FAIL (the non-GPU node would be admitted, not rejected).
// This test directly exercises the mutation to prove it produces a failure:
func TestClassAdFilterPlugin_MutationFlipGEToLT(t *testing.T) {
	p := &ClassAdFilterPlugin{}
	job := &Job{ID: "mutation-job"}
	// MUTATION: flipped >= to < — a non-GPU node now satisfies "GPU < 1" == true
	SetRequirements(job, "GPU < 1")

	noGPU := &Node{ID: "no-gpu", AvailableResources: Resources{GPU: 0}}

	// With the MUTATED expression, a node with GPU=0 satisfies "GPU < 1":
	// Filter returns true (admitted). The canary is that Filter(job, noGPU) is true,
	// which is the OPPOSITE of what we want for "GPU >= 1".
	if !p.Filter(job, noGPU) {
		t.Fatal("mutation sanity: with Requirements='GPU < 1', GPU=0 node MUST be admitted (this proves mutation changes the outcome)")
	}
	// If this test passes, it proves flipping >= to < changes the filter outcome —
	// confirming that the guard in TestClassAdFilterPluginRejectsNonGPUNode
	// would FAIL under mutation.
}

// ---------- ClassAdFilterPlugin: Score returns 0 ----------

func TestClassAdFilterPluginScoreAlwaysZero(t *testing.T) {
	p := &ClassAdFilterPlugin{}
	job := &Job{ID: "j"}
	SetRequirements(job, "GPU >= 1")
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 4}}
	if got := p.Score(job, node); got != 0 {
		t.Fatalf("ClassAdFilterPlugin.Score must always return 0, got %d", got)
	}
	// Mutation: returning non-zero from Score -> FAIL.
}

// ---------- ClassAdFilterPlugin: Bind returns true ----------

func TestClassAdFilterPluginBindReturnsTrue(t *testing.T) {
	p := &ClassAdFilterPlugin{}
	job := &Job{ID: "j"}
	node := &Node{ID: "n"}
	if !p.Bind(job, node) {
		t.Fatal("ClassAdFilterPlugin.Bind must return true")
	}
}

// ---------- ClassAdFilterPlugin: no Requirements admits every node ----------

func TestClassAdFilterPluginNoRequirementsAdmitsAll(t *testing.T) {
	p := &ClassAdFilterPlugin{}
	job := &Job{ID: "plain"}
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 0}}
	if !p.Filter(job, node) {
		t.Fatal("ClassAdFilterPlugin with no Requirements must admit every node")
	}
}

// ---------- ClassAdFilterPlugin: nil node rejected ----------

func TestClassAdFilterPluginNilNodeRejected(t *testing.T) {
	p := &ClassAdFilterPlugin{}
	job := &Job{ID: "j"}
	SetRequirements(job, "GPU >= 1")
	if p.Filter(job, nil) {
		t.Fatal("ClassAdFilterPlugin must reject a nil node")
	}
}

// ---------- ClassAdScorePlugin: identity ----------

func TestClassAdScorePluginName(t *testing.T) {
	p := &ClassAdScorePlugin{}
	if got := p.Name(); got != "ClassAdScorePlugin" {
		t.Fatalf("Name() = %q, want ClassAdScorePlugin", got)
	}
}

// ---------- ClassAdScorePlugin: Filter always true ----------

func TestClassAdScorePluginFilterAlwaysTrue(t *testing.T) {
	p := &ClassAdScorePlugin{}
	job := &Job{ID: "j"}
	// Even a node that would fail ClassAdFilterPlugin's requirements passes ClassAdScorePlugin.
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 0}}
	if !p.Filter(job, node) {
		t.Fatal("ClassAdScorePlugin.Filter must always return true")
	}
}

// ---------- ClassAdScorePlugin: Score orders by Rank expression ----------

// TestClassAdScorePluginOrdersNodesByMemory is the primary sink-side assertion:
// Rank="-Memory" means higher memory => lower (more negative) score;
// Rank="Memory" means higher memory => higher score.
func TestClassAdScorePluginOrdersNodesByMemory(t *testing.T) {
	p := &ClassAdScorePlugin{}

	job := &Job{ID: "mem-rank-job"}
	// Rank="Memory": higher memory => higher rank score (higher is better).
	SetRank(job, "Memory")

	lowMem := &Node{ID: "low-mem", AvailableResources: Resources{Memory: 1024, CPU: 4, GPU: 0}}
	highMem := &Node{ID: "high-mem", AvailableResources: Resources{Memory: 8192, CPU: 4, GPU: 0}}

	sLow := p.Score(job, lowMem)
	sHigh := p.Score(job, highMem)

	// Sink-side: higher-memory node must score strictly higher.
	if !(sHigh > sLow) {
		t.Fatalf("ClassAdScorePlugin: node with Memory=8192 must score higher than Memory=1024 when Rank='Memory': high=%d low=%d", sHigh, sLow)
	}
}

// TestClassAdScorePluginOrdersNodesByNegativeMemory verifies Rank="-Memory"
// inverts the ordering: a node with LESS memory scores higher.
func TestClassAdScorePluginOrdersNodesByNegativeMemory(t *testing.T) {
	p := &ClassAdScorePlugin{}

	job := &Job{ID: "neg-mem-rank-job"}
	SetRank(job, "-Memory")

	lowMem := &Node{ID: "low-mem", AvailableResources: Resources{Memory: 1024}}
	highMem := &Node{ID: "high-mem", AvailableResources: Resources{Memory: 8192}}

	sLow := p.Score(job, lowMem)
	sHigh := p.Score(job, highMem)

	// With Rank="-Memory", lower memory => less-negative rank => higher score.
	if !(sLow > sHigh) {
		t.Fatalf("ClassAdScorePlugin: with Rank='-Memory', lowMem must score higher than highMem: low=%d high=%d", sLow, sHigh)
	}
}

// ---------- ClassAdScorePlugin: Bind returns true ----------

func TestClassAdScorePluginBindReturnsTrue(t *testing.T) {
	p := &ClassAdScorePlugin{}
	job := &Job{ID: "j"}
	node := &Node{ID: "n"}
	if !p.Bind(job, node) {
		t.Fatal("ClassAdScorePlugin.Bind must return true")
	}
}

// ---------- ClassAdScorePlugin: no Rank expression yields 0 ----------

func TestClassAdScorePluginNoRankYieldsZero(t *testing.T) {
	p := &ClassAdScorePlugin{}
	job := &Job{ID: "j"} // no rank label
	node := &Node{ID: "n", AvailableResources: Resources{GPU: 4}}
	if got := p.Score(job, node); got != 0 {
		t.Fatalf("ClassAdScorePlugin with no Rank must score 0, got %d", got)
	}
}

// ---------- End-to-end: composed use of both named plugins ----------

// TestClassAdNamedPluginsEndToEnd verifies that using ClassAdFilterPlugin + ClassAdScorePlugin
// together produces the same semantic result as ClassAdMatch, but as two distinct named plugins.
func TestClassAdNamedPluginsEndToEnd(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&ClassAdFilterPlugin{})
	s.AddPlugin(&ClassAdScorePlugin{})

	// Two nodes satisfy GPU >= 1; the one with higher Memory wins via Rank="Memory".
	noGPU := &Node{ID: "no-gpu", AvailableResources: Resources{GPU: 0, Memory: 4096}}
	lowMem := &Node{ID: "low-mem", AvailableResources: Resources{GPU: 1, Memory: 2048}}
	highMem := &Node{ID: "high-mem", AvailableResources: Resources{GPU: 1, Memory: 16384}}
	s.RegisterNode(noGPU)
	s.RegisterNode(lowMem)
	s.RegisterNode(highMem)

	job := &Job{ID: "e2e-named"}
	SetRequirements(job, "GPU >= 1")
	SetRank(job, "Memory")

	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("Schedule failed: %v", err)
	}
	// Sink-side: must pick "high-mem" — it passes the filter and has the best Rank.
	if res.AssignedNode != "high-mem" {
		t.Fatalf("named plugins end-to-end: expected 'high-mem', got %q", res.AssignedNode)
	}
}
