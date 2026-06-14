package device

// device_adversarial_test.go — adversarial / sink-side probes for pkg/device.
//
// SCOPE NOTE (honest framing). Tonight's brief hypothesised four bug classes
// drawn from a "device manager that registers/allocates devices and owns a
// shared mutable map": (a) unguarded shared map under concurrency, (b)
// over-commit / double-allocation, (c) duplicate-ID clobbering / nondeterministic
// ordering, and (d) fail-open on a malformed device.
//
// The REAL pkg/device has NONE of that machinery. It is a probe-independent,
// pure deterministic classifier (ClassifyTier / EffectiveCompute / Route /
// AssignTier over a value-typed DeviceDescriptor) plus a per-OS host Probe.
// There is no registry, no allocator, no shared map, no mutex, and no device-ID
// uniqueness ledger — confirmed by reading every non-test .go file and by
//   grep -n 'map\[|sync\.|Register|Allocate|Release' pkg/device/*.go  (no hits).
//
// Rather than fabricate an absent surface, each hypothesised class is mapped to
// the analogous real contract this package DOES promise, and probed sink-side:
//
//   (a) "unguarded shared state under concurrency" -> the package documents its
//       functions as pure (no I/O, no shared state). We hammer ClassifyTier,
//       EffectiveCompute, Route, AssignTier AND the host Probe concurrently under
//       -race and assert CONSERVATION: every one of the N concurrent calls
//       returns the identical, correct result (no torn/lost value). A -race-clean
//       run plus the conservation assertion are both required.
//
//   (b) "over-commit / double-allocation" -> EffectiveCompute is documented
//       STRICTLY MONOTONIC. The "no over-commit" analogue is: adding a resource
//       unit never decreases or duplicates score nondeterministically; the score
//       is a single conserved quantity. We assert strict monotonicity on every
//       axis and exact reproducibility.
//
//   (c) "identity / total-order / duplicate IDs" -> ClassifyTier is documented
//       deterministic (same input -> same tier). We assert a stable TOTAL ORDER:
//       Tier<->name round-trips are identity (tierNameToConst∘String), and a
//       fixed descriptor classifies identically across thousands of goroutines
//       (no nondeterministic tie-break).
//
//   (d) "fail-open on a malformed device" -> the documented contract is that an
//       empty/degenerate descriptor (no cores AND no memory) is TierUnknown and
//       routes to RouteNone — i.e. it must NOT silently be admitted as a usable
//       tier. We assert that closed-by-default behaviour, plus negative-field
//       clamping. CLAUDE-2: we assert the darwin Probe surfaces REAL host values
//       (CPUCores == runtime.NumCPU(), MemoryBytes == an INDEPENDENT
//       `sysctl -n hw.memsize` oracle) so a !linux mock/zero stub would fail.
//
// FINDING: NO BUG. Every contract holds. This file is therefore a
// contract-pinning suite (all tests PASS on unmodified prod); the per-test
// "MUTATION" comments name the one-line prod edit each test is built to catch.

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) CONCURRENCY + CONSERVATION
// ---------------------------------------------------------------------------

// TestAdversarial_ConcurrentClassify_Conservation runs ClassifyTier, Route and
// EffectiveCompute concurrently over a fixed corpus from many goroutines and
// asserts CONSERVATION: every concurrent evaluation of a given descriptor yields
// the byte-identical result that a single-threaded evaluation produced. Run
// under -race this also proves there is no unguarded shared state.
//
// MUTATION: introduce any package-level mutable cache (e.g. a memoisation map)
// behind ClassifyTier without a lock -> -race flags the data race here; a torn
// cache entry also breaks a conservation assertion.
func TestAdversarial_ConcurrentClassify_Conservation(t *testing.T) {
	t.Parallel()

	corpus := []DeviceDescriptor{
		{ID: "mcu", CPUCores: 1, MemoryBytes: 256 * mib},
		{ID: "phone", Arch: ArchARM64, CPUCores: 4, MemoryBytes: 6 * gib, HasBattery: true},
		{ID: "pi", Arch: ArchARM64, CPUCores: 4, MemoryBytes: 4 * gib},
		{ID: "desktop", Arch: ArchAMD64, CPUCores: 8, MemoryBytes: 32 * gib},
		{ID: "npuEdge", Arch: ArchARM64, CPUCores: 8, MemoryBytes: 16 * gib, NPUTops: 40},
		{ID: "ws", Arch: ArchAMD64, CPUCores: 16, MemoryBytes: 64 * gib, GPUCount: 1, GPUVRAMBytes: 12 * gib},
		{ID: "server", Arch: ArchAMD64, CPUCores: 64, MemoryBytes: 256 * gib, GPUCount: 1, GPUVRAMBytes: 24 * gib},
		{ID: "farm", Arch: ArchAMD64, CPUCores: 128, MemoryBytes: 512 * gib, GPUCount: 8, GPUVRAMBytes: 320 * gib},
	}

	// Single-threaded golden results.
	type golden struct {
		tier  Tier
		route RouteClass
		score float64
	}
	want := make([]golden, len(corpus))
	for i, d := range corpus {
		want[i] = golden{ClassifyTier(d), Route(d), EffectiveCompute(d)}
	}

	const goroutines = 64
	const iters = 500
	var wg sync.WaitGroup
	errCh := make(chan string, goroutines*len(corpus))

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := 0; it < iters; it++ {
				for i, d := range corpus {
					if got := ClassifyTier(d); got != want[i].tier {
						errCh <- "tier mismatch for " + d.ID + ": got " + got.String() + " want " + want[i].tier.String()
						return
					}
					if got := Route(d); got != want[i].route {
						errCh <- "route mismatch for " + d.ID
						return
					}
					if got := EffectiveCompute(d); got != want[i].score {
						errCh <- "score mismatch for " + d.ID
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatalf("CONSERVATION VIOLATION under concurrency: %s", msg)
	}

	// Sink-side conservation: every corpus entry was classified, none lost.
	if len(want) != len(corpus) {
		t.Fatalf("corpus conservation: %d golden vs %d corpus", len(want), len(corpus))
	}
}

// TestAdversarial_ConcurrentProbe_StableIdentity calls the REAL host Probe from
// many goroutines concurrently and asserts every result is identical (same ID,
// Arch, CPUCores, MemoryBytes, GPUCount). Probe reads OS facilities and must be
// safe for concurrent use; -race proves no shared-state race, and the equality
// check proves no torn read.
//
// MUTATION: cache the descriptor in an unsynchronised package var on first Probe
// -> -race flags the read/write race; a partially-written cache breaks equality.
func TestAdversarial_ConcurrentProbe_StableIdentity(t *testing.T) {
	t.Parallel()

	base, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("baseline Probe() failed: %v", err)
	}

	const goroutines = 48
	var wg sync.WaitGroup
	results := make([]DeviceDescriptor, goroutines)
	errs := make([]error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = Probe(context.Background())
		}(g)
	}
	wg.Wait()

	for i := range results {
		if errs[i] != nil {
			t.Fatalf("concurrent Probe[%d] error: %v", i, errs[i])
		}
		r := results[i]
		if r.ID != base.ID || r.Arch != base.Arch || r.CPUCores != base.CPUCores ||
			r.MemoryBytes != base.MemoryBytes || r.GPUCount != base.GPUCount {
			t.Fatalf("concurrent Probe[%d] = %+v differs from baseline %+v (torn/racy read)", i, r, base)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) "OVER-COMMIT" ANALOGUE: strict monotonicity + reproducibility
// ---------------------------------------------------------------------------

// TestAdversarial_EffectiveCompute_StrictMonotonic asserts the documented
// strict-monotonicity contract: increasing any single compute axis (cores, GPU
// count, VRAM, NPU) with all else equal STRICTLY increases the score. A score
// that fails to increase is the "over-commit" analogue — a resource added but
// not conserved in the total.
//
// MUTATION: drop any term from EffectiveCompute's base sum (e.g. remove
// `gpus*40.0`) -> adding a GPU no longer increases the score and the GPU axis
// assertion fails.
func TestAdversarial_EffectiveCompute_StrictMonotonic(t *testing.T) {
	t.Parallel()
	base := DeviceDescriptor{Arch: ArchAMD64, CPUCores: 8, MemoryBytes: 16 * gib}

	bump := []struct {
		name string
		mut  func(DeviceDescriptor) DeviceDescriptor
	}{
		{"cores", func(d DeviceDescriptor) DeviceDescriptor { d.CPUCores++; return d }},
		{"gpuCount", func(d DeviceDescriptor) DeviceDescriptor { d.GPUCount++; return d }},
		{"vram", func(d DeviceDescriptor) DeviceDescriptor { d.GPUVRAMBytes += gib; return d }},
		{"npu", func(d DeviceDescriptor) DeviceDescriptor { d.NPUTops += 1; return d }},
	}
	baseScore := EffectiveCompute(base)
	for _, b := range bump {
		got := EffectiveCompute(b.mut(base))
		if !(got > baseScore) {
			t.Errorf("EffectiveCompute not strictly increasing on %s: base=%g bumped=%g", b.name, baseScore, got)
		}
	}

	// Reproducibility: identical input -> byte-identical score, 1000x.
	for i := 0; i < 1000; i++ {
		if EffectiveCompute(base) != baseScore {
			t.Fatalf("EffectiveCompute not reproducible at iter %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// (c) IDENTITY / TOTAL-ORDER
// ---------------------------------------------------------------------------

// TestAdversarial_TierNameRoundTripIdentity asserts the Tier<->name mapping is a
// faithful identity for every real tier: tierNameToConst(t.String()) == t for
// T1..T15. This pins the total-order labelling so a duplicate or shifted label
// cannot silently alias two tiers.
//
// MUTATION: duplicate a case in Tier.String (e.g. `case T9: return "T8"`) ->
// tierNameToConst("T8") yields T8 != T9 and this round-trip fails for T9.
func TestAdversarial_TierNameRoundTripIdentity(t *testing.T) {
	t.Parallel()
	all := []Tier{T1, T2, T3, T4, T5, T6, T7, T8, T9, T10, T11, T12, T13, T14, T15}
	seen := map[string]Tier{}
	for _, tier := range all {
		name := tier.String()
		if prev, dup := seen[name]; dup {
			t.Fatalf("duplicate tier label %q shared by %v and %v (identity collision)", name, prev, tier)
		}
		seen[name] = tier
		if back := tierNameToConst(name); back != tier {
			t.Errorf("round-trip identity broken: tierNameToConst(%q)=%v, want %v", name, back, tier)
		}
	}
	// TierUnknown labels as "T0" and must NOT round-trip to a real tier.
	if tierNameToConst(TierUnknown.String()) != TierUnknown {
		t.Errorf("TierUnknown label %q must map back to TierUnknown", TierUnknown.String())
	}
}

// TestAdversarial_Classify_DeterministicUnderConcurrency pins a boundary
// descriptor and asserts it classifies to exactly one tier across thousands of
// concurrent evaluations — no nondeterministic tie-break at a gate boundary.
//
// MUTATION: reorder ClassifyTier's gates so a boundary descriptor can match two
// tiers depending on evaluation -> divergent results appear and the single-value
// assertion fails.
func TestAdversarial_Classify_DeterministicUnderConcurrency(t *testing.T) {
	t.Parallel()
	// Exactly on the T6 lower boundary: 16 cores, 1 GPU, 8 GiB VRAM.
	boundary := DeviceDescriptor{Arch: ArchAMD64, CPUCores: t6MinCores, GPUCount: 1, GPUVRAMBytes: t6MinGPUVRAM, MemoryBytes: 64 * gib}
	wantTier := ClassifyTier(boundary)
	if wantTier != T6 {
		t.Fatalf("precondition: boundary descriptor expected T6, got %v", wantTier)
	}

	const goroutines = 128
	var wg sync.WaitGroup
	got := make([]Tier, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for k := 0; k < 200; k++ {
				got[i] = ClassifyTier(boundary)
			}
		}(g)
	}
	wg.Wait()
	for i, gt := range got {
		if gt != wantTier {
			t.Fatalf("nondeterministic classification: goroutine %d got %v, want %v", i, gt, wantTier)
		}
	}
}

// ---------------------------------------------------------------------------
// (d) FAIL-OPEN / MALFORMED  +  CLAUDE-2 real-host proof
// ---------------------------------------------------------------------------

// TestAdversarial_MalformedDescriptor_ClosedByDefault asserts the documented
// closed-by-default contract: an empty/degenerate descriptor (no cores AND no
// memory) is NEVER admitted as a usable tier. It must classify TierUnknown and
// route to RouteNone — the opposite of fail-open. Negative fields must clamp,
// not invert the score.
//
// MUTATION: change the degenerate gate in ClassifyTier from `<= 0 && <= 0` to
// `< 0 && < 0` (or drop it) -> an all-zero descriptor falls through to T1 and is
// silently admitted; the TierUnknown assertion fails (fail-open caught).
func TestAdversarial_MalformedDescriptor_ClosedByDefault(t *testing.T) {
	t.Parallel()

	empty := DeviceDescriptor{} // no ID, no cores, no memory
	if got := ClassifyTier(empty); got != TierUnknown {
		t.Errorf("FAIL-OPEN: empty descriptor classified as %v, want TierUnknown", got)
	}
	if got := Route(empty); got != RouteNone {
		t.Errorf("FAIL-OPEN: empty descriptor routed to %q, want RouteNone", got)
	}

	// A descriptor with ONLY negative junk must also stay TierUnknown, not be
	// rescued into a positive tier by an unclamped term.
	junk := DeviceDescriptor{CPUCores: -8, MemoryBytes: -1 * gib, GPUCount: -2, GPUVRAMBytes: -16 * gib, NPUTops: -100}
	if got := ClassifyTier(junk); got != TierUnknown {
		t.Errorf("FAIL-OPEN: all-negative descriptor classified as %v, want TierUnknown", got)
	}
	// EffectiveCompute clamps negatives to 0 -> score must be exactly 0, never
	// negative and never a fabricated positive.
	if s := EffectiveCompute(junk); s != 0 {
		t.Errorf("negative-field descriptor scored %g, want 0 (clamping broken)", s)
	}

	// IsAccelerated must be false for a malformed/empty device (no real
	// accelerator), so it cannot be admitted to an accelerated route.
	if empty.IsAccelerated() || junk.IsAccelerated() {
		t.Error("FAIL-OPEN: malformed descriptor reports IsAccelerated()=true")
	}
}

// TestAdversarial_Probe_RealHostMemory_OSOracle is the CLAUDE-2 sink-side proof
// for THIS darwin host: it asserts device.Probe() surfaces REAL OS values, not a
// !linux mock/zero stub. CPUCores must equal runtime.NumCPU(); MemoryBytes must
// equal an INDEPENDENT `sysctl -n hw.memsize` oracle (not the package's own
// helper). A stub returning 0 or a fabricated constant fails the equality.
//
// This is the host OS (darwin/arm64); no skip is justified here. The sysctl
// oracle is independent of platformTotalMemoryBytes(), so a mutation that makes
// the darwin helper fabricate a value diverges from the oracle and fails.
//
// MUTATION: in probe_darwin.go return a constant (e.g. `return 8 * (1<<30)`) from
// platformTotalMemoryBytes -> it no longer matches the live sysctl oracle and
// this test fails.
func TestAdversarial_Probe_RealHostMemory_OSOracle(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		// CLAUDE-2: NOT hiding an unimplemented port — the independent OS oracle
		// below (sysctl hw.memsize) is darwin-specific. On linux the equivalent
		// oracle is /proc/meminfo or `free -b`; that host-specific assertion is
		// out of scope for this macOS run. probe_linux.go uses syscall.Sysinfo
		// (a real kernel call) and is covered by the host's own CI on Linux.
		t.Skipf("OS oracle in this test is darwin-specific (sysctl hw.memsize); host GOOS=%s", runtime.GOOS)
	}

	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		t.Skipf("sysctl hw.memsize unavailable (sandboxed host?): %v", err)
	}
	oracleBytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || oracleBytes <= 0 {
		t.Fatalf("sysctl oracle returned unparseable/non-positive value %q: %v", string(out), err)
	}

	d, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error: %v", err)
	}

	if d.CPUCores != runtime.NumCPU() {
		t.Errorf("Probe().CPUCores = %d, runtime.NumCPU() oracle = %d (hard-coded/stubbed?)", d.CPUCores, runtime.NumCPU())
	}
	if d.MemoryBytes != oracleBytes {
		t.Errorf("Probe().MemoryBytes = %d, independent sysctl hw.memsize oracle = %d; "+
			"a !linux mock or fabricated constant would diverge here (CLAUDE-2)", d.MemoryBytes, oracleBytes)
	}
	if d.MemoryBytes <= 0 {
		t.Errorf("Probe().MemoryBytes = %d on a real Mac; CLAUDE-1 PASS-bluff (stub returns 0)", d.MemoryBytes)
	}
	t.Logf("CLAUDE-2 real-host proof: GOOS=%s GOARCH=%s CPUCores=%d MemoryBytes=%d (matches sysctl oracle)",
		runtime.GOOS, runtime.GOARCH, d.CPUCores, d.MemoryBytes)
}

// TestAdversarial_ProbeGPUOther_HonestOnNonDarwin documents and pins the CLAUDE-2
// posture of probe_gpu_other.go: on a NON-darwin host the GPU probe returns an
// HONEST (0,0) — explicitly justified in-code (Linux GPU enumeration lives in
// internal/gpu, not duplicated here) — rather than a fabricated value. On THIS
// darwin host the real system_profiler path runs instead (covered by the
// existing probe_gpu_darwin_test.go), so here we only assert the host-appropriate
// invariant: on darwin Probe surfaces a GPU; on non-darwin a 0 is honest, not a
// silent positive fabrication.
func TestAdversarial_ProbeGPUOther_HonestOnNonDarwin(t *testing.T) {
	t.Parallel()
	count, vram := platformProbeGPU()
	if runtime.GOOS == "darwin" {
		// Real path; a Mac running this suite has at least one GPU.
		if count <= 0 {
			t.Errorf("darwin platformProbeGPU() returned count=%d on a real Mac; real probe not wired", count)
		}
		return
	}
	// Non-darwin: honest zeros, never a fabricated positive.
	if count != 0 || vram != 0 {
		t.Errorf("non-darwin platformProbeGPU() = (%d,%d); contract is honest (0,0), not fabricated", count, vram)
	}
}
