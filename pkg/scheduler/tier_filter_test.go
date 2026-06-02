package scheduler

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/device"
)

// GPUTierFilter must satisfy the Plugin interface (additive, backward-compatible).
var _ Plugin = (*GPUTierFilter)(nil)

func TestGPUTierFilterName(t *testing.T) {
	if got := (&GPUTierFilter{}).Name(); got != "GPUTierFilter" {
		t.Fatalf("Name() = %q, want GPUTierFilter", got)
	}
	// Mutation: returning any other string -> FAIL.
}

// ---------- Closure #1: disallowed tier hard-excluded; allowed tier passes ----------

func TestGPUTierFilterAdmission(t *testing.T) {
	p := &GPUTierFilter{}
	tests := []struct {
		name       string
		nodeTier   device.Tier
		disallowed []device.Tier
		wantAdmit  bool
	}{
		{
			name:       "disallowed tier is hard-excluded",
			nodeTier:   device.T3,
			disallowed: []device.Tier{device.T3},
			wantAdmit:  false,
		},
		{
			name:       "allowed tier passes",
			nodeTier:   device.T7,
			disallowed: []device.Tier{device.T3},
			wantAdmit:  true,
		},
		{
			name:       "node tier in a multi-element disallowed set is excluded",
			nodeTier:   device.T1,
			disallowed: []device.Tier{device.T1, device.T2, device.T3},
			wantAdmit:  false,
		},
		{
			name:       "node tier outside a multi-element disallowed set passes",
			nodeTier:   device.T8,
			disallowed: []device.Tier{device.T1, device.T2, device.T3},
			wantAdmit:  true,
		},
		{
			name:       "empty disallowed set admits every tier",
			nodeTier:   device.T1,
			disallowed: nil,
			wantAdmit:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node := &Node{ID: "n"}
			SetNodeGPUTier(node, tc.nodeTier)
			job := &Job{ID: "j"}
			if len(tc.disallowed) > 0 {
				SetJobDisallowedTiers(job, tc.disallowed...)
			}
			if got := p.Filter(job, node); got != tc.wantAdmit {
				t.Fatalf("Filter(node tier=%s, disallowed=%v) = %v, want %v",
					tc.nodeTier, tc.disallowed, got, tc.wantAdmit)
			}
		})
	}
	// MUTATION GUARD: removing the `if disallowed[nodeGPUTier(node)] { return false }`
	// branch in Filter makes the "disallowed tier is hard-excluded" and the
	// multi-element exclusion cases return true -> FAIL (barred node wrongly admitted).
}

func TestGPUTierFilterNilGuards(t *testing.T) {
	p := &GPUTierFilter{}
	if p.Filter(&Job{ID: "j"}, nil) {
		t.Fatal("nil node must be filtered out")
	}
	if p.Filter(nil, &Node{ID: "n"}) {
		t.Fatal("nil job must be filtered out")
	}
	// Mutation: dropping a nil check -> nil-deref panic / FAIL.
}

// ---------- Closure #2: preferred tier scores strictly higher / ordered first ----------

func TestGPUTierFilterPreferenceScoring(t *testing.T) {
	p := &GPUTierFilter{}
	job := &Job{ID: "j"}
	SetJobPreferredTiers(job, device.T8)

	preferredNode := &Node{ID: "preferred"}
	SetNodeGPUTier(preferredNode, device.T8)
	otherNode := &Node{ID: "other"}
	SetNodeGPUTier(otherNode, device.T5)

	preferredScore := p.Score(job, preferredNode)
	otherScore := p.Score(job, otherNode)

	if preferredScore <= otherScore {
		t.Fatalf("preferred-tier node must score strictly higher: preferred=%d other=%d",
			preferredScore, otherScore)
	}
	if otherScore == 0 {
		t.Fatalf("non-preferred but admitted node must still score > 0 (got %d) — preference is soft, not exclusion", otherScore)
	}
	// MUTATION GUARD: removing the `score += gpuTierPreferenceBonus` branch makes
	// preferredScore == otherScore -> FAIL (preferred no longer ordered first).
}

func TestGPUTierFilterDisallowedScoresZero(t *testing.T) {
	p := &GPUTierFilter{}
	job := &Job{ID: "j"}
	SetJobDisallowedTiers(job, device.T3)
	SetJobPreferredTiers(job, device.T3) // even if also "preferred", disallow wins.
	node := &Node{ID: "n"}
	SetNodeGPUTier(node, device.T3)

	if got := p.Score(job, node); got != 0 {
		t.Fatalf("disallowed node must score 0, got %d", got)
	}
	// Mutation: removing the `if !g.Filter(...) { return 0 }` gate would let a
	// barred node score > 0 -> FAIL.
}

func TestGPUTierFilterNoPreferenceEqualScores(t *testing.T) {
	p := &GPUTierFilter{}
	job := &Job{ID: "j"} // no preferred set at all.
	a := &Node{ID: "a"}
	SetNodeGPUTier(a, device.T8)
	b := &Node{ID: "b"}
	SetNodeGPUTier(b, device.T2)
	if p.Score(job, a) != p.Score(job, b) {
		t.Fatalf("with no preference set, all admitted nodes must score equally: a=%d b=%d",
			p.Score(job, a), p.Score(job, b))
	}
}

// ---------- Closure #3: composition with the existing GPU-count filter ----------

func TestComposeTierAndCountFilter(t *testing.T) {
	tier := &GPUTierFilter{}
	count := &CostAwareGPUPlacement{} // its Filter is the GPU-COUNT predicate.

	// Job needs 2 GPUs and must NOT run on a T3 GPU tier.
	job := &Job{ID: "j", Resources: Resources{GPU: 2}}
	SetJobDisallowedTiers(job, device.T3)

	tests := []struct {
		name      string
		gpuAvail  int
		nodeTier  device.Tier
		wantAdmit bool
	}{
		{name: "passes both: enough GPUs and allowed tier", gpuAvail: 4, nodeTier: device.T8, wantAdmit: true},
		{name: "fails count only: too few GPUs", gpuAvail: 1, nodeTier: device.T8, wantAdmit: false},
		{name: "fails tier only: disallowed tier", gpuAvail: 4, nodeTier: device.T3, wantAdmit: false},
		{name: "fails both: too few GPUs AND disallowed tier", gpuAvail: 0, nodeTier: device.T3, wantAdmit: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node := &Node{ID: "n", AvailableResources: Resources{GPU: tc.gpuAvail}}
			SetNodeGPUTier(node, tc.nodeTier)
			got := ComposeFilter(job, node, count.Filter, tier.Filter)
			if got != tc.wantAdmit {
				t.Fatalf("ComposeFilter(count, tier)(gpus=%d, tier=%s) = %v, want %v",
					tc.gpuAvail, tc.nodeTier, got, tc.wantAdmit)
			}
		})
	}
	// MUTATION GUARD: the "fails tier only" case fails iff the tier filter's
	// disallow branch is intact AND the composition ANDs both predicates; the
	// "fails count only" case fails iff the existing count filter is still wired
	// in. Either being broken flips a case -> FAIL.
}

func TestComposeFilterFailsClosedOnNilPredicate(t *testing.T) {
	job := &Job{ID: "j"}
	node := &Node{ID: "n"}
	if ComposeFilter(job, node, nil) {
		t.Fatal("a nil predicate must make ComposeFilter fail closed (return false)")
	}
	// Mutation: treating nil as pass -> a mis-wired chain silently admits -> FAIL.
}

func TestComposeFilterVacuousTrue(t *testing.T) {
	if !ComposeFilter(&Job{}, &Node{}) {
		t.Fatal("with no predicates ComposeFilter must admit (vacuous truth)")
	}
}

// ---------- parsing / round-trip ----------

func TestParseTierToken(t *testing.T) {
	tests := []struct {
		in   string
		want device.Tier
	}{
		{"T7", device.T7},
		{"t7", device.T7},
		{"7", device.T7},
		{" T15 ", device.T15},
		{"T0", device.TierUnknown},
		{"", device.TierUnknown},
		{"garbage", device.TierUnknown},
		{"T99", device.TierUnknown}, // out of range -> Unknown.
		{"-1", device.TierUnknown},
	}
	for _, tc := range tests {
		if got := parseTierToken(tc.in); got != tc.want {
			t.Errorf("parseTierToken(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestTierSetRoundTrip(t *testing.T) {
	job := &Job{ID: "j"}
	SetJobDisallowedTiers(job, device.T1, device.T15)
	set := parseTierSet(job.Labels, LabelJobDisallowedTiers)
	if !set[device.T1] || !set[device.T15] {
		t.Fatalf("round-trip lost a tier: %v", set)
	}
	if set[device.T7] {
		t.Fatalf("round-trip invented tier T7: %v", set)
	}
}
