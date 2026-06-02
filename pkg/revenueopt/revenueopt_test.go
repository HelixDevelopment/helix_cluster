package revenueopt

import "testing"

// TestOptimize_TEEBiasedToChutes is the load-bearing mutation guard for HXC-1456.
//
// Fixture: a TEE GPU has a Chutes offer ($9.00) that is strictly CHEAPER than a
// competing marketplace Vast ($9.50). Without the TEE->Chutes boost, Vast wins on
// raw price. With the boost (0.10), Chutes effective revenue = 9.00 * 1.10 = 9.90,
// which beats Vast's 9.50, so Chutes must win.
//
// If the boost multiplier is mutated to 0 (i.e. the boost is removed), the
// effective Chutes revenue collapses back to 9.00 < 9.50, the allocation flips to
// Vast, and this test FAILS — exactly the falsifiability the spec requires.
func TestOptimize_TEEBiasedToChutes(t *testing.T) {
	const runUUID = "revenueopt-test-3f1c-TEEBiasedToChutes"

	opt := NewRevenueOptimizer(0.10)
	fleet := []GPU{{ID: "gpu-tee-1", Model: "H100", TEE: true}}
	offers := []Offer{
		{Marketplace: "Chutes", GPUModel: "H100", ExpectedHourlyUSD: 9.00},
		{Marketplace: "Vast", GPUModel: "H100", ExpectedHourlyUSD: 9.50},
	}

	// Hand-calculated effective revenues:
	//   Chutes (boosted): 9.00 * (1 + 0.10) = 9.90
	//   Vast   (plain):   9.50
	// => Chutes wins (9.90 > 9.50).
	got := opt.OptimizeAllocation(fleet, offers)

	want := "Chutes"
	if got["gpu-tee-1"] != want {
		t.Fatalf("[%s] TEE GPU misallocated: got %q, want %q (boost should flip raw-cheaper Chutes 9.00->9.90 past Vast 9.50)",
			runUUID, got["gpu-tee-1"], want)
	}

	// Prove the before->after delta the boost causes: without the boost Vast (9.50)
	// would have won; with the boost Chutes (9.90) wins.
	bare := NewRevenueOptimizer(0.0).OptimizeAllocation(fleet, offers)
	if bare["gpu-tee-1"] != "Vast" {
		t.Fatalf("[%s] sanity: with boost=0 the competitor Vast must win, got %q", runUUID, bare["gpu-tee-1"])
	}
	t.Logf("[%s] delta proven: boost=0 -> %q (9.50), boost=0.10 -> %q (9.90)",
		runUUID, bare["gpu-tee-1"], got["gpu-tee-1"])
}

// TestOptimize_PicksHighestRevenue verifies that a non-TEE GPU (no boost in play)
// is assigned to the plain revenue-maximizing marketplace.
func TestOptimize_PicksHighestRevenue(t *testing.T) {
	const runUUID = "revenueopt-test-9a02-PicksHighestRevenue"

	opt := NewRevenueOptimizer(0.10)
	fleet := []GPU{{ID: "gpu-plain-1", Model: "A100", TEE: false}}
	offers := []Offer{
		{Marketplace: "Chutes", GPUModel: "A100", ExpectedHourlyUSD: 4.00},
		{Marketplace: "RunPod", GPUModel: "A100", ExpectedHourlyUSD: 6.25},
		{Marketplace: "Vast", GPUModel: "A100", ExpectedHourlyUSD: 5.10},
	}

	// Non-TEE => boost never applies, even to Chutes. Raw max is RunPod at 6.25.
	got := opt.OptimizeAllocation(fleet, offers)

	want := "RunPod"
	if got["gpu-plain-1"] != want {
		t.Fatalf("[%s] non-TEE GPU misallocated: got %q, want %q (plain max = 6.25)",
			runUUID, got["gpu-plain-1"], want)
	}
	t.Logf("[%s] delta proven: among {Chutes 4.00, RunPod 6.25, Vast 5.10} -> chose %q (6.25)",
		runUUID, got["gpu-plain-1"])
}

// TestOptimize_NoMatchOmitted verifies that a GPU with no matching offer for its
// model is omitted from the result map entirely.
func TestOptimize_NoMatchOmitted(t *testing.T) {
	const runUUID = "revenueopt-test-c7e4-NoMatchOmitted"

	opt := NewRevenueOptimizer(0.10)
	fleet := []GPU{
		{ID: "gpu-match", Model: "H100", TEE: false},
		{ID: "gpu-orphan", Model: "MI300X", TEE: false},
	}
	offers := []Offer{
		{Marketplace: "Vast", GPUModel: "H100", ExpectedHourlyUSD: 8.00},
	}

	got := opt.OptimizeAllocation(fleet, offers)

	if _, present := got["gpu-orphan"]; present {
		t.Fatalf("[%s] orphan GPU with no matching offer must be omitted, but got %q",
			runUUID, got["gpu-orphan"])
	}
	if got["gpu-match"] != "Vast" {
		t.Fatalf("[%s] matched GPU misallocated: got %q, want %q", runUUID, got["gpu-match"], "Vast")
	}
	if len(got) != 1 {
		t.Fatalf("[%s] result must contain exactly the 1 matched GPU, got %d entries: %v",
			runUUID, len(got), got)
	}
	t.Logf("[%s] delta proven: 2 GPUs in, only matched gpu-match->Vast survives; gpu-orphan(MI300X) omitted (len=1)",
		runUUID)
}
