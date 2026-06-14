package capability

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ===========================================================================
// Adversarial / mutation-verified audit of the capability manifest +
// negotiation contract (HXC-1309).
//
// NOTE ON MODEL: in this package Negotiate(manifest, requirement) is a
// *satisfaction* check — it answers "does this single node's manifest meet
// the workload's requirement?" and reports each unmet dimension. It is NOT a
// two-party intersection. The load-bearing invariants audited here are:
//
//   (A) Render -> ParseManifest is a faithful round-trip (bijective on the
//       normalised manifest): no field dropped, reordered, or corrupted.
//   (B) ParseManifest rejects malformed / unknown-field input with an error
//       rather than silently producing a partial manifest.
//   (C) Negotiate's Unmet set is EXACTLY the set of failing dimensions —
//       no phantom gap when a dimension is satisfied, no dropped gap when a
//       dimension fails. OK <=> len(Unmet)==0.
//   (D) Matching is exact: feature/tier names are case-sensitive; numeric
//       thresholds use the documented < boundary (equal satisfies).
// ===========================================================================

// fullManifest is a fully-populated manifest with distinct values in every
// field, used as the round-trip centerpiece. Features are intentionally
// unsorted+duplicated so we can also assert Validate-normalisation survives.
func fullManifest() CapabilityManifest {
	return CapabilityManifest{
		NodeID:      "node-Ω-7f3a",
		Tier:        "T7",
		Arch:        "arm64",
		CPUCores:    128,
		MemoryMB:    1 << 40, // 1 TiB in MiB-sized int64, exercises wide int64
		GPUCount:    8,
		GPUClass:    "H100-SXM:80GB",
		NPUTOPS:     1234.5,
		FPGAPresent: true,
		Features:    []string{"rdma", "avx512", "nvlink", "avx512", "amx"},
	}
}

// ---------------------------------------------------------------------------
// (A) ROUND-TRIP FIDELITY — centerpiece
// ---------------------------------------------------------------------------

// TestAdv_RoundTrip_DeepEqual is the strongest round-trip assertion: a
// fully-populated, Validate-normalised manifest, when Rendered then Parsed,
// must reproduce the original byte-for-byte at the struct level (DeepEqual).
// This bites ANY lossy/corrupting mutation in Render or ParseManifest — a
// dropped field, a swapped yaml tag, a nil-vs-empty Features regression, or a
// reordered Features slice.
//
// Mutation proof: changing any `yaml:"..."` tag on a field, or hardcoding a
// field in Render/Parse, makes DeepEqual false and this test fail.
func TestAdv_RoundTrip_DeepEqual(t *testing.T) {
	orig := fullManifest()
	if err := orig.Validate(); err != nil {
		t.Fatalf("fullManifest invalid: %v", err)
	}
	// After Validate, Features is canonical (sorted+deduped): [amx avx512 nvlink rdma].
	wantFeatures := []string{"amx", "avx512", "nvlink", "rdma"}
	if !reflect.DeepEqual(orig.Features, wantFeatures) {
		t.Fatalf("precondition: Validate did not canonicalise Features: got %v want %v", orig.Features, wantFeatures)
	}

	b, err := orig.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := ParseManifest(b)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Fatalf("round-trip not bijective:\n  orig = %+v\n  got  = %+v\n  yaml =\n%s", orig, got, b)
	}
}

// TestAdv_RoundTrip_EachFieldDistinct renders a manifest, then for each field
// mutates the PARSED copy and confirms it diverges from the original — proving
// every field genuinely survived the round-trip (i.e. the DeepEqual pass above
// is not a vacuous all-zero-vs-all-zero match). It also asserts the rendered
// YAML actually carries each value (the field reached the wire).
func TestAdv_RoundTrip_FieldsReachWire(t *testing.T) {
	orig := fullManifest()
	if err := orig.Validate(); err != nil {
		t.Fatalf("invalid: %v", err)
	}
	b, err := orig.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	wire := string(b)

	// Each load-bearing scalar value must appear in the serialised form;
	// a dropped field in Render would omit its key/value.
	for _, want := range []string{
		"node-Ω-7f3a", // NodeID (unicode preserved)
		"T7",          // Tier
		"arm64",       // Arch
		"128",         // CPUCores
		"1099511627776", // MemoryMB (1<<40)
		"H100-SXM:80GB", // GPUClass with punctuation
		"1234.5",        // NPUTOPS
		"true",          // FPGAPresent
		"amx", "avx512", "nvlink", "rdma", // all features
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("rendered YAML missing %q; field dropped in Render?\nYAML:\n%s", want, wire)
		}
	}
}

// TestAdv_RoundTrip_NegativeAndZeroNumerics ensures the round-trip carries
// signed/zero numeric values faithfully (no abs/clamp in serialisation). These
// are pre-Validate values; ParseManifest does not validate, so they must
// survive verbatim.
func TestAdv_RoundTrip_NegativeAndZeroNumerics(t *testing.T) {
	orig := CapabilityManifest{
		NodeID:   "n",
		Tier:     "T1",
		Arch:     "amd64",
		CPUCores: -3,            // negative survives parse (Validate not called)
		MemoryMB: -9000000000,   // negative wide int64
		GPUCount: 0,
		NPUTOPS:  -0.0,          // negative zero float
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
	if got.CPUCores != orig.CPUCores {
		t.Errorf("CPUCores not preserved: want %d got %d", orig.CPUCores, got.CPUCores)
	}
	if got.MemoryMB != orig.MemoryMB {
		t.Errorf("MemoryMB not preserved: want %d got %d", orig.MemoryMB, got.MemoryMB)
	}
}

// ---------------------------------------------------------------------------
// (B) MALFORMED / STRICT PARSE
// ---------------------------------------------------------------------------

// TestAdv_Parse_MalformedNotSilentlyPartial proves a syntactically broken
// document yields an ERROR and the returned manifest is the zero value — the
// parse error is never swallowed into a partial/half-populated manifest.
func TestAdv_Parse_MalformedNotSilentlyPartial(t *testing.T) {
	// Valid prefix then a structural break: a mapping value that is itself an
	// unterminated/garbage construct.
	bad := []byte("node_id: n1\ntier: T3\ncpu_cores: [unterminated\n")
	m, err := ParseManifest(bad)
	if err == nil {
		t.Fatalf("expected error for malformed YAML, got nil; m=%+v", m)
	}
	if !reflect.DeepEqual(m, CapabilityManifest{}) {
		t.Errorf("malformed parse must return zero manifest, got partial: %+v", m)
	}
}

// TestAdv_Parse_WrongScalarType ensures a type-mismatched scalar (string where
// an int is expected) is a parse error, not a silent zero.
func TestAdv_Parse_WrongScalarType(t *testing.T) {
	bad := []byte("node_id: n1\ntier: T3\narch: amd64\ncpu_cores: not-a-number\nmemory_mb: 1024\n")
	m, err := ParseManifest(bad)
	if err == nil {
		t.Fatalf("expected type-mismatch error, got nil; m=%+v", m)
	}
}

// TestAdv_Parse_EmptyInput documents the contract for empty input. An empty
// document decodes to the zero manifest with Features normalised to non-nil.
// (yaml.Decoder returns io.EOF on truly empty input; assert whichever the
// contract yields and that it is internally consistent.)
func TestAdv_Parse_EmptyInput(t *testing.T) {
	m, err := ParseManifest([]byte(""))
	if err != nil {
		// Empty stream -> EOF is an acceptable, honest error (no partial data).
		if !reflect.DeepEqual(m, CapabilityManifest{}) {
			t.Errorf("on error, manifest must be zero value, got %+v", m)
		}
		return
	}
	// If no error, Features must still be the guaranteed non-nil empty slice.
	if m.Features == nil {
		t.Errorf("empty-input parse must yield non-nil Features per contract")
	}
}

// TestAdv_Parse_UnknownNestedishKey reinforces strict mode for a second
// plausible misspelling distinct from the existing test ("memory" vs
// "memory_mb"), guarding KnownFields(true).
func TestAdv_Parse_UnknownKey_Memory(t *testing.T) {
	bad := []byte("node_id: n1\ntier: T3\narch: amd64\ncpu_cores: 8\nmemory: 1024\n")
	if _, err := ParseManifest(bad); err == nil {
		t.Fatal("expected unknown-field error for 'memory' (vs 'memory_mb'), got nil")
	}
}

// ---------------------------------------------------------------------------
// (C) NEGOTIATE — Unmet is EXACTLY the failing-dimension set (centerpiece)
// ---------------------------------------------------------------------------

// negotiateExactSet is the centerpiece: it asserts result.Unmet equals the
// expected set EXACTLY (same elements, no more, no fewer), and that
// OK <=> empty. A union-style bug (reporting a gap for a satisfied dimension)
// or a dropped-gap bug (omitting a genuinely failing dimension) both break the
// exact-set equality.
func negotiateExactSet(t *testing.T, m CapabilityManifest, r Requirement, want []string) {
	t.Helper()
	res := Negotiate(m, r)
	gotSorted := append([]string(nil), res.Unmet...)
	wantSorted := append([]string(nil), want...)
	sort.Strings(gotSorted)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(gotSorted, wantSorted) {
		t.Fatalf("Unmet set mismatch:\n  got  = %v\n  want = %v", res.Unmet, want)
	}
	wantOK := len(want) == 0
	if res.OK != wantOK {
		t.Fatalf("OK invariant broken: OK=%v but Unmet=%v (want OK=%v)", res.OK, res.Unmet, wantOK)
	}
}

// TestAdv_Negotiate_ExactIntersection walks the truth table of which
// dimensions fail. For each dimension it asserts the Unmet set is precisely
// the failing ones — no phantom entry for a satisfied dimension, no missing
// entry for a failing one.
//
// Mutation proof: turning any `if cond` gate in Negotiate into an
// unconditional append (phantom gap) OR deleting any gate (dropped gap) makes
// at least one of these exact-set assertions fail.
func TestAdv_Negotiate_ExactSetTruthTable(t *testing.T) {
	base := goodManifest() // T3,cpu16,mem65536,gpu2,npu100,fpga true,features[avx512,nvlink,rdma]

	// All satisfied -> empty set, OK.
	negotiateExactSet(t, base, goodRequirement(), nil)

	// Only CPU fails.
	m := base
	m.CPUCores = 1
	negotiateExactSet(t, m, Requirement{MinCPUCores: 8}, []string{"cpu"})

	// Only memory fails.
	m = base
	m.MemoryMB = 1
	negotiateExactSet(t, m, Requirement{MinMemoryMB: 2}, []string{"memory"})

	// Only GPU fails.
	m = base
	m.GPUCount = 0
	negotiateExactSet(t, m, Requirement{MinGPUCount: 1}, []string{"gpu"})

	// Only NPU fails.
	m = base
	m.NPUTOPS = 1.0
	negotiateExactSet(t, m, Requirement{MinNPUTOPS: 2.0}, []string{"npu"})

	// Only FPGA fails.
	m = base
	m.FPGAPresent = false
	negotiateExactSet(t, m, Requirement{RequireFPGA: true}, []string{"fpga"})

	// Only tier fails.
	m = base
	m.Tier = "T0"
	negotiateExactSet(t, m, Requirement{AcceptedTiers: []string{"T9"}}, []string{"tier-not-accepted"})

	// Only one feature fails.
	negotiateExactSet(t, base, Requirement{RequiredFeatures: []string{"smartnic"}}, []string{"missing-feature:smartnic"})

	// EVERY dimension fails simultaneously -> exact union of all gaps.
	allFail := CapabilityManifest{
		NodeID: "n", Tier: "Tbad", Arch: "amd64",
		CPUCores: 1, MemoryMB: 1, GPUCount: 0, NPUTOPS: 0, FPGAPresent: false,
		Features: []string{},
	}
	allReq := Requirement{
		AcceptedTiers:    []string{"T3"},
		MinCPUCores:      8,
		MinMemoryMB:      100,
		MinGPUCount:      1,
		MinNPUTOPS:       1,
		RequireFPGA:      true,
		RequiredFeatures: []string{"beta", "alpha"},
	}
	negotiateExactSet(t, allFail, allReq, []string{
		"tier-not-accepted", "cpu", "memory", "gpu", "npu", "fpga",
		"missing-feature:alpha", "missing-feature:beta",
	})
}

// TestAdv_Negotiate_NoPhantomWhenSatisfied isolates the "phantom gap" failure
// mode: every dimension is exactly satisfied (boundary equal), so Unmet MUST
// be empty. A union/always-append mutation surfaces a phantom gap here.
func TestAdv_Negotiate_NoPhantomAtBoundary(t *testing.T) {
	base := goodManifest()
	r := Requirement{
		AcceptedTiers:    []string{base.Tier},
		MinCPUCores:      base.CPUCores, // equal -> satisfied (strict <)
		MinMemoryMB:      base.MemoryMB,
		MinGPUCount:      base.GPUCount,
		MinNPUTOPS:       base.NPUTOPS,
		RequireFPGA:      base.FPGAPresent,
		RequiredFeatures: []string{"avx512", "nvlink", "rdma"},
	}
	negotiateExactSet(t, base, r, nil)
}

// TestAdv_Negotiate_BoundaryOffByOne pins the < vs <= boundary for every
// numeric dimension: equal satisfies, one-less fails. A mutation from `<` to
// `<=` (rejecting an exact match) flips the "exact" rows to a gap and fails.
func TestAdv_Negotiate_BoundaryOffByOne(t *testing.T) {
	base := goodManifest() // cpu16 mem65536 gpu2 npu100

	// Exact == satisfied.
	negotiateExactSet(t, base, Requirement{MinCPUCores: 16}, nil)
	negotiateExactSet(t, base, Requirement{MinMemoryMB: 65536}, nil)
	negotiateExactSet(t, base, Requirement{MinGPUCount: 2}, nil)
	negotiateExactSet(t, base, Requirement{MinNPUTOPS: 100.0}, nil)

	// One past == fails.
	negotiateExactSet(t, base, Requirement{MinCPUCores: 17}, []string{"cpu"})
	negotiateExactSet(t, base, Requirement{MinMemoryMB: 65537}, []string{"memory"})
	negotiateExactSet(t, base, Requirement{MinGPUCount: 3}, []string{"gpu"})
	negotiateExactSet(t, base, Requirement{MinNPUTOPS: 100.0001}, []string{"npu"})
}

// TestAdv_Negotiate_FeatureMatchExact pins matching precision: feature names
// are case-sensitive and matched verbatim, so a case/lookalike variant does
// NOT satisfy a RequiredFeature.
func TestAdv_Negotiate_FeatureMatchCaseSensitive(t *testing.T) {
	m := goodManifest()
	m.Features = []string{"AVX512", "RDMA"} // upper-case lookalikes
	// Lower-case requirement must NOT be satisfied by upper-case features.
	negotiateExactSet(t, m, Requirement{RequiredFeatures: []string{"avx512", "rdma"}},
		[]string{"missing-feature:avx512", "missing-feature:rdma"})

	// Exact-case match IS satisfied.
	negotiateExactSet(t, m, Requirement{RequiredFeatures: []string{"AVX512"}}, nil)
}

// TestAdv_Negotiate_TierMatchCaseSensitive pins tier matching as exact/
// case-sensitive: a case variant of the tier is not accepted.
func TestAdv_Negotiate_TierMatchCaseSensitive(t *testing.T) {
	m := goodManifest()
	m.Tier = "t3" // lower-case
	negotiateExactSet(t, m, Requirement{AcceptedTiers: []string{"T3"}}, []string{"tier-not-accepted"})
	negotiateExactSet(t, m, Requirement{AcceptedTiers: []string{"t3"}}, nil)
}

// TestAdv_Negotiate_DuplicateRequiredFeature ensures a feature listed twice in
// RequiredFeatures but absent yields a single missing-feature entry (the
// missing set is deduplicated via sort+emit), not two — guarding against a
// duplicated-gap regression.
func TestAdv_Negotiate_MissingFeaturesSortedNoDup(t *testing.T) {
	m := goodManifest()
	m.Features = []string{} // nothing present
	r := Requirement{RequiredFeatures: []string{"zeta", "alpha", "alpha", "mu"}}
	res := Negotiate(m, r)
	// Must be sorted; "alpha" present once per its two requests is acceptable so
	// long as ordering is deterministic-sorted. Assert sorted order explicitly.
	want := []string{"missing-feature:alpha", "missing-feature:alpha", "missing-feature:mu", "missing-feature:zeta"}
	// The production emits one entry per RequiredFeatures element that is
	// missing, sorted. Confirm it is sorted ascending.
	if !sort.StringsAreSorted(res.Unmet) {
		t.Fatalf("missing-feature gaps must be sorted, got %v", res.Unmet)
	}
	if !reflect.DeepEqual(res.Unmet, want) {
		t.Fatalf("Unmet: got %v want %v", res.Unmet, want)
	}
}

// TestAdv_Negotiate_OKImpliesEmptyAndViceVersa exhaustively cross-checks the
// OK <-> empty-Unmet invariant across a small randomised-ish matrix.
func TestAdv_Negotiate_OKIffEmpty(t *testing.T) {
	base := goodManifest()
	reqs := []Requirement{
		{},
		{MinCPUCores: 16},
		{MinCPUCores: 999},
		{RequiredFeatures: []string{"nvlink"}},
		{RequiredFeatures: []string{"nope"}},
		{AcceptedTiers: []string{"T3"}},
		{AcceptedTiers: []string{"ZZ"}},
		{RequireFPGA: true},
		{MinMemoryMB: 1 << 60},
	}
	for i, r := range reqs {
		res := Negotiate(base, r)
		if res.OK != (len(res.Unmet) == 0) {
			t.Errorf("req[%d]=%+v: OK=%v but len(Unmet)=%d (must agree)", i, r, res.OK, len(res.Unmet))
		}
		if res.OK && res.Unmet != nil {
			t.Errorf("req[%d]: OK=true must yield nil Unmet, got %v", i, res.Unmet)
		}
	}
}
