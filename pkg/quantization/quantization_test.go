package quantization

import (
	"errors"
	"testing"
)

const runUUID = "qz-7f3c1a9e-4d62-4b80-9c11-2a6e5d0f8b34"

// commonCatalog is a catalog of three quantized builds of one model. The FP16
// build is large; the INT4 build is small. A small tier can only fit INT4.
func commonCatalog() []ModelVariant {
	return []ModelVariant{
		{Name: "m-fp16", Quant: QuantFP16, FootprintMiB: 14000, Backend: "cuda"},
		{Name: "m-int8", Quant: QuantINT8, FootprintMiB: 7000, Backend: "cuda"},
		{Name: "m-int4", Quant: QuantINT4, FootprintMiB: 3500, Backend: "cuda"},
	}
}

// TestPerTierSizing proves clause 1: a large tier gets the FP16 build while a
// small tier — which CANNOT fit FP16 — gets the INT4 build that does fit.
//
// Mutation: if the memory-fit check (`c.FootprintMiB > tier.MemoryMiB`) is
// removed, the small tier would wrongly select the 14000 MiB FP16 build
// instead of the 3500 MiB INT4 build, failing the small-tier assertion below.
func TestPerTierSizing(t *testing.T) {
	cat := commonCatalog()

	large := DeviceTier{ID: "T1", MemoryMiB: 24000, Backends: []string{"cuda"}}
	small := DeviceTier{ID: "T6", MemoryMiB: 4000, Backends: []string{"cuda"}}

	// Sanity: a single one-size FP16 policy would NOT fit the small tier.
	t.Logf("run %s before: small tier mem=%d MiB, fp16 footprint=%d MiB (fp16 fits small? %v)",
		runUUID, small.MemoryMiB, cat[0].FootprintMiB, cat[0].FootprintMiB <= small.MemoryMiB)
	if cat[0].FootprintMiB <= small.MemoryMiB {
		t.Fatalf("test premise broken: FP16 should NOT fit the small tier")
	}

	gotLarge, err := Select(large, cat)
	if err != nil {
		t.Fatalf("large tier: unexpected error: %v", err)
	}
	gotSmall, err := Select(small, cat)
	if err != nil {
		t.Fatalf("small tier: unexpected error: %v", err)
	}

	t.Logf("run %s after: large->%s(%s,%dMiB) small->%s(%s,%dMiB)",
		runUUID,
		gotLarge.Name, gotLarge.Quant, gotLarge.FootprintMiB,
		gotSmall.Name, gotSmall.Quant, gotSmall.FootprintMiB)

	if gotLarge.Quant != QuantFP16 {
		t.Errorf("large tier: want FP16, got %s (%s)", gotLarge.Quant, gotLarge.Name)
	}
	if gotSmall.Quant != QuantINT4 {
		t.Errorf("small tier: want INT4 (only fitting build), got %s (%s, %d MiB)",
			gotSmall.Quant, gotSmall.Name, gotSmall.FootprintMiB)
	}
	if gotSmall.FootprintMiB > small.MemoryMiB {
		t.Errorf("small tier picked a build that does NOT fit: %d > %d",
			gotSmall.FootprintMiB, small.MemoryMiB)
	}
}

// TestBackendCompatExcludesFittingVariant proves clause 2: a variant whose
// backend the tier does NOT support is excluded even when it fits memory, so a
// smaller but compatible variant is chosen instead.
//
// Mutation: if the backend-support gate (`!tier.supports(c.Backend)`) is
// removed, the higher-quality FP16 "metal"-only build would be selected even
// though the tier has no Metal backend, failing the assertions below.
func TestBackendCompatExcludesFittingVariant(t *testing.T) {
	// Both builds fit the tier's 24000 MiB budget. FP16 requires "metal";
	// the tier only has "cuda", so the INT8 cuda build must win.
	cat := []ModelVariant{
		{Name: "g-fp16-metal", Quant: QuantFP16, FootprintMiB: 14000, Backend: "metal"},
		{Name: "g-int8-cuda", Quant: QuantINT8, FootprintMiB: 7000, Backend: "cuda"},
	}
	tier := DeviceTier{ID: "T2", MemoryMiB: 24000, Backends: []string{"cuda"}}

	t.Logf("run %s before: tier backends=%v; fp16 backend=%q fits-mem=%v",
		runUUID, tier.Backends, cat[0].Backend, cat[0].FootprintMiB <= tier.MemoryMiB)

	got, err := Select(tier, cat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("run %s after: selected %s (%s, backend=%s)", runUUID, got.Name, got.Quant, got.Backend)

	if got.Backend != "cuda" {
		t.Errorf("selected an unsupported backend %q; tier only supports %v", got.Backend, tier.Backends)
	}
	if got.Quant != QuantINT8 {
		t.Errorf("want INT8 cuda build (FP16 is metal-only and must be excluded), got %s (%s)",
			got.Quant, got.Name)
	}
	if got.Name == "g-fp16-metal" {
		t.Errorf("backend-incompatible FP16 build was wrongly selected")
	}
}

// TestNoFitReturnsTypedError proves clause 3: a tier too small for EVERY
// variant returns the typed ErrNoFit — not a crash, and not a too-big pick.
//
// Mutation: if the memory-fit check is removed, a too-big variant would be
// returned with a nil error instead of ErrNoFit, failing both assertions.
func TestNoFitReturnsTypedError(t *testing.T) {
	cat := commonCatalog() // smallest build is 3500 MiB
	tiny := DeviceTier{ID: "T8", MemoryMiB: 512, Backends: []string{"cuda"}}

	smallest := cat[len(cat)-1].FootprintMiB
	t.Logf("run %s before: tiny tier mem=%d MiB, smallest build=%d MiB (any fits? %v)",
		runUUID, tiny.MemoryMiB, smallest, smallest <= tiny.MemoryMiB)

	got, err := Select(tiny, cat)

	t.Logf("run %s after: got=%+v err=%v", runUUID, got, err)

	if !errors.Is(err, ErrNoFit) {
		t.Fatalf("want ErrNoFit for a tier too small for every variant, got err=%v (variant=%+v)", err, got)
	}
	if got != (ModelVariant{}) {
		t.Errorf("on no-fit a zero ModelVariant must be returned, got a non-zero pick: %+v", got)
	}
}

// TestBackendOnlyNoFit proves the backend gate also drives ErrNoFit: a variant
// fits memory but its backend is unsupported, leaving zero eligible variants.
//
// Mutation: removing the backend-support gate would make this variant eligible
// and Select would return it with a nil error instead of ErrNoFit.
func TestBackendOnlyNoFit(t *testing.T) {
	cat := []ModelVariant{
		{Name: "x-int4-rocm", Quant: QuantINT4, FootprintMiB: 1000, Backend: "rocm"},
	}
	tier := DeviceTier{ID: "T5", MemoryMiB: 8000, Backends: []string{"cuda", "metal"}}

	t.Logf("run %s before: only build fits-mem=%v backend=%q tier-backends=%v",
		runUUID, cat[0].FootprintMiB <= tier.MemoryMiB, cat[0].Backend, tier.Backends)

	got, err := Select(tier, cat)
	t.Logf("run %s after: got=%+v err=%v", runUUID, got, err)

	if !errors.Is(err, ErrNoFit) {
		t.Fatalf("want ErrNoFit when the only fitting build uses an unsupported backend, got %v", err)
	}
}

// TestTieBreakDeterministic proves the deterministic tie-break: among two
// eligible builds of equal quality, the larger footprint wins; among equal
// footprint, the lexicographically smaller name wins.
//
// Mutation: if the footprint tie-break flips to ascending (a<b), the 6000 MiB
// build would lose to the 5000 MiB build, failing the footprint assertion.
func TestTieBreakDeterministic(t *testing.T) {
	cat := []ModelVariant{
		{Name: "tie-b", Quant: QuantINT8, FootprintMiB: 5000, Backend: "cuda"},
		{Name: "tie-a", Quant: QuantINT8, FootprintMiB: 6000, Backend: "cuda"},
		{Name: "tie-c", Quant: QuantINT8, FootprintMiB: 6000, Backend: "cuda"},
	}
	tier := DeviceTier{ID: "T3", MemoryMiB: 8000, Backends: []string{"cuda"}}

	t.Logf("run %s before: equal-quality builds, footprints 5000/6000/6000", runUUID)

	// Run twice to assert determinism.
	first, err := Select(tier, cat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := Select(tier, cat)
	if err != nil {
		t.Fatalf("unexpected error on rerun: %v", err)
	}

	t.Logf("run %s after: first=%s(%dMiB) second=%s", runUUID, first.Name, first.FootprintMiB, second.Name)

	if first.FootprintMiB != 6000 {
		t.Errorf("tie-break: want larger footprint (6000) to win, got %d (%s)", first.FootprintMiB, first.Name)
	}
	if first.Name != "tie-a" {
		t.Errorf("tie-break: among equal footprint want smaller name 'tie-a', got %s", first.Name)
	}
	if first != second {
		t.Errorf("selection not deterministic: %+v vs %+v", first, second)
	}
}

// TestQuantQualityUnknown proves that Quant.Quality() returns 0 for an unknown
// quantization level. The default: branch in Quality() is the target here; it
// ensures unknown levels never win a tie-break against any known level.
//
// Coverage fix: the default: arm of Quality was previously untested. Adding this
// test closes the gap noted in the code-quality review (§[Minor] coverage gap).
//
// Mutation guard: if the default: arm were removed entirely the compiler would
// warn about a missing return; if it returned a non-zero value (e.g. 1) this
// test would fail on the got != 0 assertion.
func TestQuantQualityUnknown(t *testing.T) {
	unknown := Quant("bf16-hypothetical")
	got := unknown.Quality()
	t.Logf("run %s: Quality(%q) = %d", runUUID, unknown, got)
	if got != 0 {
		t.Errorf("Quality(%q): want 0 for unknown Quant, got %d", unknown, got)
	}

	// Also assert that the empty string is treated as unknown.
	empty := Quant("")
	gotEmpty := empty.Quality()
	if gotEmpty != 0 {
		t.Errorf("Quality(%q): want 0 for empty Quant, got %d", empty, gotEmpty)
	}

	// Sanity: a known level must score higher so unknown never wins a tie-break.
	if Quant(QuantINT4).Quality() <= 0 {
		t.Errorf("INT4 Quality must be > 0 (> unknown level 0), got %d", Quant(QuantINT4).Quality())
	}
}

// TestEmptyCandidates proves the empty-input contract.
//
// Mutation: removing the empty-slice guard would index an empty eligible slice
// or return a different error, failing this typed-error assertion.
func TestEmptyCandidates(t *testing.T) {
	tier := DeviceTier{ID: "T1", MemoryMiB: 24000, Backends: []string{"cuda"}}
	t.Logf("run %s before: empty candidate slice", runUUID)
	got, err := Select(tier, nil)
	t.Logf("run %s after: got=%+v err=%v", runUUID, got, err)
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("want ErrNoCandidates for empty input, got %v", err)
	}
}
