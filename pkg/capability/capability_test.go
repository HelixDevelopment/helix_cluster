package capability

import (
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// goodManifest returns a fully valid CapabilityManifest to be used as the
// baseline in many tests.
func goodManifest() CapabilityManifest {
	return CapabilityManifest{
		NodeID:      "node-001",
		Tier:        "T3",
		Arch:        "amd64",
		CPUCores:    16,
		MemoryMB:    65536,
		GPUCount:    2,
		GPUClass:    "A100",
		NPUTOPS:     100.0,
		FPGAPresent: true,
		Features:    []string{"avx512", "nvlink", "rdma"},
	}
}

// goodRequirement returns a Requirement that the goodManifest satisfies.
func goodRequirement() Requirement {
	return Requirement{
		AcceptedTiers:    []string{"T3", "T4"},
		MinCPUCores:      8,
		MinMemoryMB:      32768,
		MinGPUCount:      1,
		MinNPUTOPS:       50.0,
		RequireFPGA:      true,
		RequiredFeatures: []string{"avx512", "rdma"},
	}
}

// containsAll asserts that got contains every element of want (exact string
// match) and fails the test if any element is absent.
func containsAll(t *testing.T, got []string, want ...string) {
	t.Helper()
	set := make(map[string]struct{}, len(got))
	for _, g := range got {
		set[g] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			t.Errorf("Unmet: want %q in %v but was absent", w, got)
		}
	}
}

// notContains asserts that got does NOT contain s.
func notContains(t *testing.T, got []string, s string) {
	t.Helper()
	for _, g := range got {
		if g == s {
			t.Errorf("Unmet: did not want %q but it appeared in %v", s, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Validate tests
// ---------------------------------------------------------------------------

func TestValidate_ValidManifest(t *testing.T) {
	// Mutation: change m.NodeID = "" → Validate returns non-nil error, test fails.
	m := goodManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("expected nil error for valid manifest, got %v", err)
	}
}

func TestValidate_EmptyNodeID(t *testing.T) {
	// Mutation: remove the NodeID empty-check in Validate → error is nil, test fails.
	m := goodManifest()
	m.NodeID = ""
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for empty NodeID, got nil")
	}
}

func TestValidate_EmptyTier(t *testing.T) {
	// Mutation: remove the Tier empty-check in Validate → error is nil, test fails.
	m := goodManifest()
	m.Tier = ""
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for empty Tier, got nil")
	}
}

func TestValidate_EmptyArch(t *testing.T) {
	// Mutation: remove the Arch empty-check in Validate → error is nil, test fails.
	m := goodManifest()
	m.Arch = ""
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for empty Arch, got nil")
	}
}

func TestValidate_ZeroCPUCores(t *testing.T) {
	// Mutation: change CPUCores check from <= 0 to < 0 → 0 passes, test fails.
	m := goodManifest()
	m.CPUCores = 0
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for CPUCores=0, got nil")
	}
}

func TestValidate_ZeroMemoryMB(t *testing.T) {
	// Mutation: change MemoryMB check from <= 0 to < 0 → 0 passes, test fails.
	m := goodManifest()
	m.MemoryMB = 0
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for MemoryMB=0, got nil")
	}
}

func TestValidate_NegativeCPUCores(t *testing.T) {
	// Mutation: change check to m.CPUCores == 0 → -1 passes, test fails.
	m := goodManifest()
	m.CPUCores = -1
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for CPUCores=-1, got nil")
	}
}

func TestValidate_FeaturesDedupAndSort(t *testing.T) {
	// Mutation: remove deduplicateSorted call → "b" appears twice, len check fails.
	m := goodManifest()
	m.Features = []string{"c", "b", "a", "b"}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(m.Features) != len(want) {
		t.Fatalf("Features: want %v (len %d), got %v (len %d)", want, len(want), m.Features, len(m.Features))
	}
	for i, w := range want {
		if m.Features[i] != w {
			t.Errorf("Features[%d]: want %q, got %q", i, w, m.Features[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Negotiate — all-satisfied case
// ---------------------------------------------------------------------------

// TestNegotiate_AllMet — manifest meeting all requirements → OK=true, Unmet empty.
// Mutation: change `OK: true` to `OK: false` in the return → this test fails.
func TestNegotiate_AllMet(t *testing.T) {
	result := Negotiate(goodManifest(), goodRequirement())
	if !result.OK {
		t.Fatalf("expected OK=true, got OK=false; Unmet=%v", result.Unmet)
	}
	if len(result.Unmet) != 0 {
		t.Fatalf("expected empty Unmet, got %v", result.Unmet)
	}
}

// ---------------------------------------------------------------------------
// Negotiate — single-dimension failures
// ---------------------------------------------------------------------------

// TestNegotiate_BelowMinCPU — below MinCPUCores → OK=false, Unmet contains "cpu".
// Mutation: drop the cpu check in Negotiate (comment out the `if r.MinCPUCores…` block)
// → Unmet does not contain "cpu", containsAll assertion fails.
func TestNegotiate_BelowMinCPU(t *testing.T) {
	m := goodManifest()
	m.CPUCores = 4 // requirement is 8
	r := goodRequirement()
	result := Negotiate(m, r)
	if result.OK {
		t.Fatal("expected OK=false, got OK=true")
	}
	containsAll(t, result.Unmet, "cpu")
	// Ensure no false positives from other dimensions.
	notContains(t, result.Unmet, "memory")
	notContains(t, result.Unmet, "tier-not-accepted")
}

// TestNegotiate_BelowMinMemory — below MinMemoryMB → Unmet names "memory".
// Mutation: drop the memory check in Negotiate → "memory" absent from Unmet, test fails.
func TestNegotiate_BelowMinMemory(t *testing.T) {
	m := goodManifest()
	m.MemoryMB = 1024 // requirement is 32768
	r := goodRequirement()
	result := Negotiate(m, r)
	if result.OK {
		t.Fatal("expected OK=false, got OK=true")
	}
	containsAll(t, result.Unmet, "memory")
	notContains(t, result.Unmet, "cpu")
}

// TestNegotiate_BelowMinGPU — below MinGPUCount → Unmet names "gpu".
// Mutation: drop the gpu check → "gpu" absent, test fails.
func TestNegotiate_BelowMinGPU(t *testing.T) {
	m := goodManifest()
	m.GPUCount = 0 // requirement is 1
	r := goodRequirement()
	result := Negotiate(m, r)
	if result.OK {
		t.Fatal("expected OK=false, got OK=true")
	}
	containsAll(t, result.Unmet, "gpu")
	notContains(t, result.Unmet, "cpu")
	notContains(t, result.Unmet, "memory")
}

// TestNegotiate_BelowMinNPU — below MinNPUTOPS → Unmet names "npu".
// Mutation: drop the npu check → "npu" absent, test fails.
func TestNegotiate_BelowMinNPU(t *testing.T) {
	m := goodManifest()
	m.NPUTOPS = 10.0 // requirement is 50.0
	r := goodRequirement()
	result := Negotiate(m, r)
	if result.OK {
		t.Fatal("expected OK=false, got OK=true")
	}
	containsAll(t, result.Unmet, "npu")
	notContains(t, result.Unmet, "cpu")
}

// TestNegotiate_MissingRequiredFeature — missing a RequiredFeature →
// Unmet names "missing-feature:<name>".
// Mutation: drop the feature-presence check in Negotiate → exact string absent, test fails.
func TestNegotiate_MissingRequiredFeature(t *testing.T) {
	m := goodManifest()
	m.Features = []string{"avx512"} // "rdma" missing; requirement needs both
	r := goodRequirement()
	result := Negotiate(m, r)
	if result.OK {
		t.Fatal("expected OK=false, got OK=true")
	}
	containsAll(t, result.Unmet, "missing-feature:rdma")
	notContains(t, result.Unmet, "missing-feature:avx512")
}

// TestNegotiate_TierNotAccepted — tier not in AcceptedTiers → Unmet names "tier-not-accepted".
// Mutation: drop the tier check → "tier-not-accepted" absent, test fails.
func TestNegotiate_TierNotAccepted(t *testing.T) {
	m := goodManifest()
	m.Tier = "T1" // requirement accepts T3, T4
	r := goodRequirement()
	result := Negotiate(m, r)
	if result.OK {
		t.Fatal("expected OK=false, got OK=true")
	}
	containsAll(t, result.Unmet, "tier-not-accepted")
	notContains(t, result.Unmet, "cpu")
}

// TestNegotiate_FPGARequired — RequireFPGA but FPGAPresent=false → Unmet names "fpga".
// Mutation: drop the fpga check → "fpga" absent, test fails.
func TestNegotiate_FPGARequired(t *testing.T) {
	m := goodManifest()
	m.FPGAPresent = false
	r := goodRequirement()
	r.RequireFPGA = true
	result := Negotiate(m, r)
	if result.OK {
		t.Fatal("expected OK=false, got OK=true")
	}
	containsAll(t, result.Unmet, "fpga")
	notContains(t, result.Unmet, "cpu")
	notContains(t, result.Unmet, "memory")
}

// TestNegotiate_FPGANotRequired — RequireFPGA=false, FPGAPresent=false → still OK.
// Mutation: set RequireFPGA=true unconditionally in Negotiate → OK becomes false, test fails.
func TestNegotiate_FPGANotRequired(t *testing.T) {
	m := goodManifest()
	m.FPGAPresent = false
	r := goodRequirement()
	r.RequireFPGA = false
	result := Negotiate(m, r)
	if !result.OK {
		t.Fatalf("expected OK=true when FPGA not required, got Unmet=%v", result.Unmet)
	}
}

// ---------------------------------------------------------------------------
// Negotiate — multi-gap case (deterministic Unmet ordering)
// ---------------------------------------------------------------------------

// TestNegotiate_MultipleGaps — CPU, memory, and a missing feature all fail →
// Unmet contains all three gaps and preserves the documented order.
// Mutation: always-OK path (return MatchResult{OK:true}) → all sub-assertions fail.
func TestNegotiate_MultipleGaps(t *testing.T) {
	m := goodManifest()
	m.CPUCores = 2          // below 8
	m.MemoryMB = 512        // below 32768
	m.Features = []string{} // missing avx512, rdma

	r := goodRequirement()
	result := Negotiate(m, r)
	if result.OK {
		t.Fatal("expected OK=false for manifest with multiple gaps")
	}
	containsAll(t, result.Unmet, "cpu", "memory", "missing-feature:avx512", "missing-feature:rdma")

	// Verify deterministic ordering: cpu before memory, missing-features sorted.
	cpuIdx, memIdx := -1, -1
	avxIdx, rdmaIdx := -1, -1
	for i, u := range result.Unmet {
		switch u {
		case "cpu":
			cpuIdx = i
		case "memory":
			memIdx = i
		case "missing-feature:avx512":
			avxIdx = i
		case "missing-feature:rdma":
			rdmaIdx = i
		}
	}
	if cpuIdx > memIdx {
		t.Errorf("expected cpu (idx=%d) before memory (idx=%d) in Unmet", cpuIdx, memIdx)
	}
	if avxIdx > rdmaIdx {
		t.Errorf("expected missing-feature:avx512 (idx=%d) before missing-feature:rdma (idx=%d)", avxIdx, rdmaIdx)
	}
	if memIdx > avxIdx {
		t.Errorf("expected memory (idx=%d) before missing-feature:avx512 (idx=%d)", memIdx, avxIdx)
	}
}

// ---------------------------------------------------------------------------
// Negotiate — empty AcceptedTiers means any tier accepted
// ---------------------------------------------------------------------------

// TestNegotiate_EmptyAcceptedTiers — empty AcceptedTiers → tier never blocks.
// Mutation: treat empty AcceptedTiers as "no tier accepted" → OK=false, test fails.
func TestNegotiate_EmptyAcceptedTiers(t *testing.T) {
	m := goodManifest()
	m.Tier = "T8" // exotic tier
	r := goodRequirement()
	r.AcceptedTiers = []string{} // any tier
	result := Negotiate(m, r)
	if !result.OK {
		t.Fatalf("empty AcceptedTiers should accept any tier; got Unmet=%v", result.Unmet)
	}
}

// ---------------------------------------------------------------------------
// Render / ParseManifest round-trip
// ---------------------------------------------------------------------------

// TestRenderParseRoundTrip — Render then ParseManifest preserves all fields exactly.
// Mutation: hardcode Render to return yaml.Marshal(CapabilityManifest{}) → parsed
// fields differ, specific field equality checks fail.
func TestRenderParseRoundTrip(t *testing.T) {
	orig := goodManifest()
	// Normalise to canonical form before comparison.
	if err := orig.Validate(); err != nil {
		t.Fatalf("goodManifest invalid: %v", err)
	}

	b, err := orig.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("Render produced empty bytes")
	}

	got, err := ParseManifest(b)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	// Assert each field individually so a regression names the broken field.
	if got.NodeID != orig.NodeID {
		t.Errorf("NodeID: want %q, got %q", orig.NodeID, got.NodeID)
	}
	if got.Tier != orig.Tier {
		t.Errorf("Tier: want %q, got %q", orig.Tier, got.Tier)
	}
	if got.Arch != orig.Arch {
		t.Errorf("Arch: want %q, got %q", orig.Arch, got.Arch)
	}
	if got.CPUCores != orig.CPUCores {
		t.Errorf("CPUCores: want %d, got %d", orig.CPUCores, got.CPUCores)
	}
	if got.MemoryMB != orig.MemoryMB {
		t.Errorf("MemoryMB: want %d, got %d", orig.MemoryMB, got.MemoryMB)
	}
	if got.GPUCount != orig.GPUCount {
		t.Errorf("GPUCount: want %d, got %d", orig.GPUCount, got.GPUCount)
	}
	if got.GPUClass != orig.GPUClass {
		t.Errorf("GPUClass: want %q, got %q", orig.GPUClass, got.GPUClass)
	}
	if got.NPUTOPS != orig.NPUTOPS {
		t.Errorf("NPUTOPS: want %v, got %v", orig.NPUTOPS, got.NPUTOPS)
	}
	if got.FPGAPresent != orig.FPGAPresent {
		t.Errorf("FPGAPresent: want %v, got %v", orig.FPGAPresent, got.FPGAPresent)
	}
	if len(got.Features) != len(orig.Features) {
		t.Fatalf("Features length: want %d, got %d", len(orig.Features), len(got.Features))
	}
	for i, f := range orig.Features {
		if got.Features[i] != f {
			t.Errorf("Features[%d]: want %q, got %q", i, f, got.Features[i])
		}
	}
}

// TestRenderParseRoundTrip_EmptyFeatures — a manifest with no features survives round-trip.
// Mutation: make ParseManifest return nil Features → len check fails.
func TestRenderParseRoundTrip_EmptyFeatures(t *testing.T) {
	orig := CapabilityManifest{
		NodeID:   "node-zero",
		Tier:     "T1",
		Arch:     "arm64",
		CPUCores: 4,
		MemoryMB: 8192,
		Features: []string{},
	}
	b, err := orig.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := ParseManifest(b)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if got.Features == nil {
		t.Fatal("ParseManifest returned nil Features; want non-nil empty slice")
	}
	if len(got.Features) != 0 {
		t.Fatalf("Features: want empty, got %v", got.Features)
	}
	if got.NodeID != orig.NodeID {
		t.Errorf("NodeID: want %q, got %q", orig.NodeID, got.NodeID)
	}
}

// TestParseManifest_InvalidYAML — malformed YAML returns a non-nil error.
// Mutation: make ParseManifest always return nil error → test fails.
func TestParseManifest_InvalidYAML(t *testing.T) {
	_, err := ParseManifest([]byte(":\tinvalid: yaml: :::"))
	if err == nil {
		t.Fatal("expected error parsing malformed YAML, got nil")
	}
}

// TestParseManifest_UnknownField — a YAML document with an unrecognised field
// must return a non-nil error (strict parsing prevents silent data loss from
// misspelled keys such as "cpu_core" instead of "cpu_cores").
// Mutation: remove dec.KnownFields(true) from ParseManifest → err becomes nil,
// test fails.
func TestParseManifest_UnknownField(t *testing.T) {
	// "cpu_core" is a plausible misspelling of the valid key "cpu_cores".
	yaml := []byte("node_id: n1\ntier: T1\narch: amd64\ncpu_core: 8\nmemory_mb: 1024\n")
	_, err := ParseManifest(yaml)
	if err == nil {
		t.Fatal("expected error for unknown field 'cpu_core', got nil")
	}
}

// ---------------------------------------------------------------------------
// Table-driven compound tests
// ---------------------------------------------------------------------------

// TestNegotiate_Table exercises a broad matrix of Requirement configurations
// against a fixed manifest and verifies the exact Unmet set for each case.
// Mutation: comment out any single dimension check in Negotiate → the
// corresponding row's assertion fails.
func TestNegotiate_Table(t *testing.T) {
	base := goodManifest()
	if err := base.Validate(); err != nil {
		t.Fatalf("base manifest invalid: %v", err)
	}

	// base: NodeID=node-001, Tier=T3, Arch=amd64, CPUCores=16, MemoryMB=65536,
	//       GPUCount=2, NPUTOPS=100, FPGAPresent=true, Features=[avx512,nvlink,rdma]

	type row struct {
		name    string
		req     Requirement
		wantOK  bool
		wantAll []string // every element must appear in Unmet
		wantNot []string // none must appear in Unmet
	}

	rows := []row{
		{
			name:    "no constraints",
			req:     Requirement{},
			wantOK:  true,
			wantAll: nil,
		},
		{
			name: "exact cpu match",
			req:  Requirement{MinCPUCores: 16},
			// m.CPUCores == r.MinCPUCores, should be satisfied
			wantOK:  true,
			wantAll: nil,
		},
		{
			name:    "cpu one over",
			req:     Requirement{MinCPUCores: 17},
			wantOK:  false,
			wantAll: []string{"cpu"},
			wantNot: []string{"memory", "tier-not-accepted"},
		},
		{
			name:    "memory exact match",
			req:     Requirement{MinMemoryMB: 65536},
			wantOK:  true,
			wantAll: nil,
		},
		{
			name:    "memory one over",
			req:     Requirement{MinMemoryMB: 65537},
			wantOK:  false,
			wantAll: []string{"memory"},
			wantNot: []string{"cpu"},
		},
		{
			name:    "tier accepted single match",
			req:     Requirement{AcceptedTiers: []string{"T3"}},
			wantOK:  true,
			wantAll: nil,
		},
		{
			name:    "tier not in list",
			req:     Requirement{AcceptedTiers: []string{"T5", "T6"}},
			wantOK:  false,
			wantAll: []string{"tier-not-accepted"},
			wantNot: []string{"cpu", "memory"},
		},
		{
			name:    "gpu exactly met",
			req:     Requirement{MinGPUCount: 2},
			wantOK:  true,
			wantAll: nil,
		},
		{
			name:    "gpu one over",
			req:     Requirement{MinGPUCount: 3},
			wantOK:  false,
			wantAll: []string{"gpu"},
			wantNot: []string{"cpu", "memory"},
		},
		{
			name:    "npu exactly met",
			req:     Requirement{MinNPUTOPS: 100.0},
			wantOK:  true,
			wantAll: nil,
		},
		{
			name:    "npu slightly over",
			req:     Requirement{MinNPUTOPS: 100.001},
			wantOK:  false,
			wantAll: []string{"npu"},
			wantNot: []string{"cpu"},
		},
		{
			name:    "fpga required and present",
			req:     Requirement{RequireFPGA: true},
			wantOK:  true,
			wantAll: nil,
		},
		{
			name:    "feature present",
			req:     Requirement{RequiredFeatures: []string{"nvlink"}},
			wantOK:  true,
			wantAll: nil,
		},
		{
			name:    "feature absent",
			req:     Requirement{RequiredFeatures: []string{"smartnic"}},
			wantOK:  false,
			wantAll: []string{"missing-feature:smartnic"},
			wantNot: []string{"cpu", "memory", "tier-not-accepted"},
		},
		{
			name:    "two features absent sorted",
			req:     Requirement{RequiredFeatures: []string{"zcopy", "infiniband"}},
			wantOK:  false,
			wantAll: []string{"missing-feature:infiniband", "missing-feature:zcopy"},
			wantNot: []string{"cpu"},
		},
		{
			name:    "cpu+memory+feature combo",
			req:     Requirement{MinCPUCores: 32, MinMemoryMB: 131072, RequiredFeatures: []string{"offload"}},
			wantOK:  false,
			wantAll: []string{"cpu", "memory", "missing-feature:offload"},
		},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			res := Negotiate(base, tc.req)
			if res.OK != tc.wantOK {
				t.Fatalf("OK: want %v, got %v; Unmet=%v", tc.wantOK, res.OK, res.Unmet)
			}
			if tc.wantOK && len(res.Unmet) != 0 {
				t.Fatalf("OK=true but Unmet=%v (want empty)", res.Unmet)
			}
			containsAll(t, res.Unmet, tc.wantAll...)
			for _, nw := range tc.wantNot {
				notContains(t, res.Unmet, nw)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// deduplicateSorted unit tests (package-internal)
// ---------------------------------------------------------------------------

// TestDeduplicateSorted_Basic — "b","a","b" → ["a","b"].
// Mutation: remove sort call → order wrong, assertions fail.
func TestDeduplicateSorted_Basic(t *testing.T) {
	got := deduplicateSorted([]string{"b", "a", "b"})
	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: want %q, got %q", i, want[i], got[i])
		}
	}
}

// TestDeduplicateSorted_NilInput — nil input returns non-nil empty slice.
// Mutation: return nil on empty input → got==nil, test fails.
func TestDeduplicateSorted_NilInput(t *testing.T) {
	got := deduplicateSorted(nil)
	if got == nil {
		t.Fatal("want non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("want len 0, got %d", len(got))
	}
}

// TestDeduplicateSorted_AllDupes — ["x","x","x"] → ["x"].
// Mutation: remove the duplicate-skip logic → ["x","x","x"] length 3, test fails.
func TestDeduplicateSorted_AllDupes(t *testing.T) {
	got := deduplicateSorted([]string{"x", "x", "x"})
	if len(got) != 1 || got[0] != "x" {
		t.Fatalf("want [x], got %v", got)
	}
}
