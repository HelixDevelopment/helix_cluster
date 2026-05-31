package scheduler

import "testing"

// ---------- TierPowerAware: identity & interface ----------

func TestTierPowerName(t *testing.T) {
	p := &TierPowerAware{}
	if got := p.Name(); got != "TierPowerAware" {
		t.Fatalf("Name() = %q, want TierPowerAware", got)
	}
	// Mutation: returning any other string -> FAIL.
}

// It must satisfy the Plugin interface (backward-compatible additive API).
var _ Plugin = (*TierPowerAware)(nil)

// ---------- Filter: tier admission ----------

func TestTierPowerFilterRejectsBelowMinTier(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "5"}}
	t3 := &Node{ID: "t3", Labels: map[string]string{LabelNodeTier: "T3"}}
	if p.Filter(job, t3) {
		t.Fatal("a T3 node must be filtered OUT for a job requiring tier>=T5")
	}
	// Mutation: a Filter that ignores the tier requirement (e.g. dropping the
	// 'nodeTier(node) < jobMinTier(job)' check / always returning true) keeps
	// the T3 node -> this assertion FAILS.
}

func TestTierPowerFilterKeepsAtOrAboveMinTier(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "5"}}
	t6 := &Node{ID: "t6", Labels: map[string]string{LabelNodeTier: "T6"}}
	t5 := &Node{ID: "t5", Labels: map[string]string{LabelNodeTier: "5"}}
	if !p.Filter(job, t6) {
		t.Fatal("a T6 node must satisfy a job requiring tier>=T5")
	}
	if !p.Filter(job, t5) {
		t.Fatal("a T5 node must satisfy (exact match) a job requiring tier>=T5")
	}
	// Mutation: changing '<' to '<=' rejects the exact T5 match -> FAIL.
}

func TestTierPowerFilterNoTierReqAdmitsAnyTier(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j"} // no min_tier label
	t0 := &Node{ID: "t0", Labels: map[string]string{LabelNodeTier: "0"}}
	if !p.Filter(job, t0) {
		t.Fatal("a job with no tier requirement must be admitted on a baseline tier-0 node")
	}
	// Mutation: defaulting jobMinTier to a non-zero value when the label is
	// absent would reject the tier-0 node -> FAIL.
}

func TestTierPowerFilterUnlabeledNodeIsTierZero(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "1"}}
	bare := &Node{ID: "bare"} // no tier label -> tier 0
	if p.Filter(job, bare) {
		t.Fatal("an unlabeled (tier-0) node must be filtered OUT for a tier>=1 job")
	}
	// Mutation: defaulting an unlabeled node's tier to a high value would admit
	// it -> FAIL.
}

// ---------- Filter: arch admission ----------

func TestTierPowerFilterRejectsArchMismatch(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobRequireArch: "arm64"}}
	x86 := &Node{ID: "x86", Labels: map[string]string{LabelNodeArch: "x86_64"}}
	if p.Filter(job, x86) {
		t.Fatal("an x86_64 node must be filtered OUT for an arm64-required job")
	}
	// Mutation: dropping the arch-mismatch rejection (or comparing with == on
	// always-equal) admits the wrong-arch node -> FAIL.
}

func TestTierPowerFilterArchMatchCaseInsensitive(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobRequireArch: "ARM64"}}
	arm := &Node{ID: "arm", Labels: map[string]string{LabelNodeArch: "arm64"}}
	if !p.Filter(job, arm) {
		t.Fatal("arch match must be case-insensitive: arm64 must satisfy ARM64")
	}
	// Mutation: using a case-sensitive '==' compare rejects the match -> FAIL.
}

func TestTierPowerFilterNoArchReqIgnoresNodeArch(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j"} // no require_arch
	any := &Node{ID: "any", Labels: map[string]string{LabelNodeArch: "riscv"}}
	if !p.Filter(job, any) {
		t.Fatal("a job with no arch requirement must accept any node arch")
	}
	// Mutation: requiring an arch match even when the job has no requirement
	// rejects the node -> FAIL.
}

func TestTierPowerFilterNilNode(t *testing.T) {
	p := &TierPowerAware{}
	if p.Filter(&Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "1"}}, nil) {
		t.Fatal("nil node must be filtered out")
	}
	// Mutation: dropping the nil check -> nil-deref panic / FAIL.
}

// ---------- Score: more power-efficient node scores strictly higher ----------

func TestTierPowerEfficientNodeScoresHigher(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "2"}}
	// Same tier (no overshoot), differ only in raw watts.
	frugal := &Node{ID: "frugal", Labels: map[string]string{
		LabelNodeTier: "2", LabelNodeWatts: "100"}}
	thirsty := &Node{ID: "thirsty", Labels: map[string]string{
		LabelNodeTier: "2", LabelNodeWatts: "400"}}

	fs := p.Score(job, frugal)
	ts := p.Score(job, thirsty)
	if !(fs > ts) {
		t.Fatalf("the more power-efficient (lower-watt) node must score strictly higher: frugal=%d thirsty=%d", fs, ts)
	}
	// Concrete arithmetic: score = base - watts*100 (no overshoot, no ppw).
	if fs != tierPowerScoreBase-100*wattScale {
		t.Fatalf("frugal score = %d, want %d", fs, tierPowerScoreBase-100*wattScale)
	}
	if ts != tierPowerScoreBase-400*wattScale {
		t.Fatalf("thirsty score = %d, want %d", ts, tierPowerScoreBase-400*wattScale)
	}
	// Mutation: flipping 'base - watts*wattScale' to 'base + watts*wattScale'
	// inverts the ordering (thirsty would win) -> fs > ts FAIL, and the exact
	// values FAIL on any change to the watt weighting.
}

func TestTierPowerPerfPerWattBreaksTieAtEqualWatts(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "2"}}
	// Equal raw watts; the node advertising higher perf-per-watt must win.
	efficient := &Node{ID: "eff", Labels: map[string]string{
		LabelNodeTier: "2", LabelNodeWatts: "200", LabelNodePerfPerWatt: "9"}}
	plain := &Node{ID: "plain", Labels: map[string]string{
		LabelNodeTier: "2", LabelNodeWatts: "200", LabelNodePerfPerWatt: "1"}}

	es := p.Score(job, efficient)
	ps := p.Score(job, plain)
	if !(es > ps) {
		t.Fatalf("higher perf-per-watt must win at equal watts: eff=%d plain=%d", es, ps)
	}
	// base - 200*100 + ppw*1000.
	if es != tierPowerScoreBase-200*wattScale+9*perfPerWattScale {
		t.Fatalf("efficient score = %d, want %d", es, tierPowerScoreBase-200*wattScale+9*perfPerWattScale)
	}
	if ps != tierPowerScoreBase-200*wattScale+1*perfPerWattScale {
		t.Fatalf("plain score = %d, want %d", ps, tierPowerScoreBase-200*wattScale+1*perfPerWattScale)
	}
	// Mutation: dropping the '+ ppw*perfPerWattScale' efficiency bonus makes
	// es == ps -> es > ps FAIL.
}

// ---------- Score: anti-fragmentation tier-overshoot penalty ----------

func TestTierPowerAntiFragmentationPenalisesOvershoot(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "2"}}
	// Identical power; one node exactly matches the tier, the other overshoots
	// by 4 tiers. The exact match must score higher (top tier not wasted).
	exact := &Node{ID: "exact", Labels: map[string]string{
		LabelNodeTier: "2", LabelNodeWatts: "100"}}
	top := &Node{ID: "top", Labels: map[string]string{
		LabelNodeTier: "6", LabelNodeWatts: "100"}}

	exs := p.Score(job, exact)
	tps := p.Score(job, top)
	if !(exs > tps) {
		t.Fatalf("an exact-tier node must score higher than a top-tier overshoot at equal power: exact=%d top=%d", exs, tps)
	}
	// top overshoots by 4 tiers: penalty = 4 * tierFragmentationPenalty.
	if exs-tps != 4*tierFragmentationPenalty {
		t.Fatalf("overshoot penalty = %d, want %d", exs-tps, 4*tierFragmentationPenalty)
	}
	// Mutation: removing the 'overshoot * tierFragmentationPenalty' subtraction
	// makes exs == tps -> exs > tps FAIL.
}

func TestTierPowerHigherTierWinsWhenJobDemandsIt(t *testing.T) {
	p := &TierPowerAware{}
	// A tier-demanding job: min_tier 6. The T3 node is FILTERED OUT, so even
	// though it is more frugal the scheduler can only pick a satisfying tier.
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "6"}}
	t3 := &Node{ID: "t3", Labels: map[string]string{LabelNodeTier: "3", LabelNodeWatts: "50"}}
	t6 := &Node{ID: "t6", Labels: map[string]string{LabelNodeTier: "6", LabelNodeWatts: "300"}}

	if p.Filter(job, t3) {
		t.Fatal("T3 cannot satisfy a tier>=6 job and must be filtered out")
	}
	// The satisfying T6 node (overshoot 0) scores its honest power cost.
	if got := p.Score(job, t6); got != tierPowerScoreBase-300*wattScale {
		t.Fatalf("T6 score = %d, want %d", got, tierPowerScoreBase-300*wattScale)
	}
	// And the filtered T3 scores 0 (cannot host the job).
	if got := p.Score(job, t3); got != 0 {
		t.Fatalf("filtered T3 must score 0, got %d", got)
	}
	// Mutation: a Score that ignores Filter would give the frugal T3 a high
	// score and mis-route a tier>=6 job onto an inadequate node -> FAIL.
}

// ---------- Score: filtered node scores 0 ----------

func TestTierPowerFilteredNodeScoresZero(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "5"}}
	low := &Node{ID: "low", Labels: map[string]string{LabelNodeTier: "1", LabelNodeWatts: "10"}}
	if got := p.Score(job, low); got != 0 {
		t.Fatalf("a below-tier node must score 0, got %d", got)
	}
	// Mutation: removing the '!Filter -> 0' guard returns a high score for a
	// node that cannot host the job -> FAIL.
}

// ---------- Score: derived power proxy when no watts label ----------

func TestTierPowerDerivedProxyPrefersLeanerNode(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j"} // no tier req -> both admitted, both overshoot 0
	lean := &Node{ID: "lean", AvailableResources: Resources{CPU: 2, Memory: 1024, GPU: 0}}
	beefy := &Node{ID: "beefy", AvailableResources: Resources{CPU: 8, Memory: 8192, GPU: 2}}

	ls := p.Score(job, lean)
	bs := p.Score(job, beefy)
	if !(ls > bs) {
		t.Fatalf("leaner node draws less derived power and must score higher: lean=%d beefy=%d", ls, bs)
	}
	// lean proxy = 2*5 + 1024/1024 + 0*250 = 10 + 1 = 11 watts.
	if ls != tierPowerScoreBase-11*wattScale {
		t.Fatalf("lean derived score = %d, want %d", ls, tierPowerScoreBase-11*wattScale)
	}
	// beefy proxy = 8*5 + 8192/1024 + 2*250 = 40 + 8 + 500 = 548 watts.
	if bs != tierPowerScoreBase-548*wattScale {
		t.Fatalf("beefy derived score = %d, want %d", bs, tierPowerScoreBase-548*wattScale)
	}
	// Mutation: zeroing the GPU*250 power weight changes beefy's exact value
	// (and could flip the ordering) -> FAIL.
}

func TestTierPowerWattsLabelOverridesProxy(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j"}
	// Resource-rich node, but an explicit low watts label must dominate.
	node := &Node{ID: "n",
		AvailableResources: Resources{CPU: 64, Memory: 262144, GPU: 8},
		Labels:             map[string]string{LabelNodeWatts: "120"}}
	if got := p.Score(job, node); got != tierPowerScoreBase-120*wattScale {
		t.Fatalf("explicit watts label must override derived proxy: got %d want %d",
			got, tierPowerScoreBase-120*wattScale)
	}
	// Mutation: ignoring the watts label and always using the resource proxy
	// would yield a far lower score -> FAIL.
}

// ---------- Bad/non-positive labels fall back to derived proxy ----------

func TestTierPowerUnparseableWattsFallsBack(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j"}
	garbage := &Node{ID: "g", AvailableResources: Resources{CPU: 2, Memory: 1024},
		Labels: map[string]string{LabelNodeWatts: "lots"}}
	nonpos := &Node{ID: "z", AvailableResources: Resources{CPU: 2, Memory: 1024},
		Labels: map[string]string{LabelNodeWatts: "0"}}

	want := tierPowerScoreBase - 11*wattScale // proxy: 2*5 + 1 = 11
	if got := p.Score(job, garbage); got != want {
		t.Fatalf("garbage watts must fall back to proxy: got %d want %d", got, want)
	}
	if got := p.Score(job, nonpos); got != want {
		t.Fatalf("non-positive watts must fall back to proxy: got %d want %d", got, want)
	}
	// Mutation: trusting ParseFloat without the err/'<=0' check would accept a
	// 0/garbage watts and produce the wrong (base) score -> FAIL.
}

// ---------- Ties are deterministic ----------

func TestTierPowerTiesDeterministic(t *testing.T) {
	p := &TierPowerAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelJobMinTier: "2"}}
	a := &Node{ID: "a", Labels: map[string]string{LabelNodeTier: "2", LabelNodeWatts: "150"}}
	b := &Node{ID: "b", Labels: map[string]string{LabelNodeTier: "2", LabelNodeWatts: "150"}}
	if p.Score(job, a) != p.Score(job, b) {
		t.Fatal("equal tier+power nodes must score equally")
	}
	first := p.Score(job, a)
	for i := 0; i < 100; i++ {
		if p.Score(job, a) != first {
			t.Fatalf("score not stable across calls at iter %d", i)
		}
	}
	// Mutation: introducing node.ID hashing or rand into the score -> FAIL.
}

// ---------- Setters write reserved labels backward-compatibly ----------

func TestTierPowerSettersPopulateLabels(t *testing.T) {
	node := &Node{ID: "n"} // nil Labels
	SetNodeTier(node, 4)
	SetNodeArch(node, "cuda_hopper")
	SetNodePower(node, 250)
	if node.Labels[LabelNodeTier] != "4" {
		t.Fatalf("SetNodeTier wrote %q, want 4", node.Labels[LabelNodeTier])
	}
	if node.Labels[LabelNodeArch] != "cuda_hopper" {
		t.Fatalf("SetNodeArch wrote %q", node.Labels[LabelNodeArch])
	}
	if nodePowerWatts(node) != 250 {
		t.Fatalf("SetNodePower => watts %v, want 250", nodePowerWatts(node))
	}

	job := &Job{ID: "j"} // nil Labels
	SetJobMinTier(job, 5)
	SetJobRequireArch(job, "arm64")
	if jobMinTier(job) != 5 {
		t.Fatalf("SetJobMinTier => %d, want 5", jobMinTier(job))
	}
	if jobRequireArch(job) != "arm64" {
		t.Fatalf("SetJobRequireArch => %q, want arm64", jobRequireArch(job))
	}
	// Mutation: a setter that fails to allocate the nil map would panic /
	// store nothing -> the read-backs FAIL.
}

// ---------- End-to-end: scheduler picks the tier+power-preferred node ----------

func TestTierPowerSchedulerPicksPreferredNode(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{}) // resource accounting
	s.AddPlugin(&TierPowerAware{})   // tier + power preference

	// Job needs tier>=5 and 1 GPU. Three nodes:
	//  - t3:   tier 3  -> FILTERED OUT (below min tier), even though frugal.
	//  - t6hi: tier 6, 400W -> satisfies, but high power + tier overshoot.
	//  - t5lo: tier 5, 120W -> satisfies, exact-ish tier, frugal -> WINNER.
	s.RegisterNode(&Node{ID: "t3",
		AvailableResources: Resources{CPU: 8, Memory: 8192, GPU: 2},
		Labels:             map[string]string{LabelNodeTier: "3", LabelNodeWatts: "50"}})
	s.RegisterNode(&Node{ID: "t6hi",
		AvailableResources: Resources{CPU: 8, Memory: 8192, GPU: 2},
		Labels:             map[string]string{LabelNodeTier: "6", LabelNodeWatts: "400"}})
	s.RegisterNode(&Node{ID: "t5lo",
		AvailableResources: Resources{CPU: 8, Memory: 8192, GPU: 2},
		Labels:             map[string]string{LabelNodeTier: "5", LabelNodeWatts: "120"}})

	job := &Job{ID: "j",
		Resources: Resources{CPU: 1, Memory: 1024, GPU: 1},
		Labels:    map[string]string{LabelJobMinTier: "5"}}

	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if res.AssignedNode != "t5lo" {
		t.Fatalf("expected job placed on 't5lo' (satisfies tier, lowest power, no overshoot), got %q", res.AssignedNode)
	}
	// Mutation: a Filter ignoring the tier requirement would let the frugal
	// 50W 't3' node win and the assignment would be 't3' -> FAIL. Inverting the
	// power sign would route to the 400W 't6hi' -> FAIL.
}

// End-to-end with arch: the scheduler must filter out the wrong-arch node.
func TestTierPowerSchedulerFiltersArchMismatchEndToEnd(t *testing.T) {
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&TierPowerAware{})

	// Job requires arm64. x86 node is frugal but wrong arch -> filtered out.
	s.RegisterNode(&Node{ID: "x86", AvailableResources: Resources{CPU: 8, Memory: 8192},
		Labels: map[string]string{LabelNodeArch: "x86_64", LabelNodeWatts: "60"}})
	s.RegisterNode(&Node{ID: "arm", AvailableResources: Resources{CPU: 8, Memory: 8192},
		Labels: map[string]string{LabelNodeArch: "arm64", LabelNodeWatts: "200"}})

	job := &Job{ID: "j", Resources: Resources{CPU: 1, Memory: 1024},
		Labels: map[string]string{LabelJobRequireArch: "arm64"}}

	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if res.AssignedNode != "arm" {
		t.Fatalf("arch-required job must land on the arm64 node despite higher power, got %q", res.AssignedNode)
	}
	// Mutation: dropping the arch-mismatch rejection lets the frugal 60W x86
	// node win the score and the assignment becomes 'x86' -> FAIL.
}
