package chaos

import (
	"reflect"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// HXC-1247 — Seeded deterministic fault sequencer + Byzantine fault
// ---------------------------------------------------------------------------

// TestScheduleDeterminism proves that the same seed produces a byte-identical
// InjectionLog across two independent NewSchedule/Run calls, and that two
// different seeds produce divergent logs.
//
// MUTATION TARGET: freeze the rng advance inside Schedule.Run so it always
// picks faults[0] — the resulting log will still be identical across the two
// same-seed runs, but the different-seed run will ALSO produce the identical
// sequence, causing the divergence assertion to fail.
func TestScheduleDeterminism(t *testing.T) {
	const seed = int64(123)
	const steps = 50

	a := NewSchedule(seed).Run(steps)
	b := NewSchedule(seed).Run(steps)

	if len(a) != steps {
		t.Fatalf("expected %d injections, got %d", steps, len(a))
	}
	if len(b) != steps {
		t.Fatalf("expected %d injections, got %d", steps, len(b))
	}

	// Same seed → identical logs (determinism).
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("step %d: same-seed logs diverged: %+v vs %+v", i, a[i], b[i])
		}
	}

	// Different seed → divergent logs (coverage > 1 fault type needed).
	c := NewSchedule(seed + 1).Run(steps)
	different := false
	for i := range a {
		if a[i].FaultName != c[i].FaultName {
			different = true
			break
		}
	}
	if !different {
		t.Fatal("different seeds must produce divergent fault sequences")
	}
}

// TestScheduleVirtualTimestamps proves that VirtualTS advances monotonically
// within a single Run call (step N's timestamp strictly less than step N+1's).
func TestScheduleVirtualTimestamps(t *testing.T) {
	log := NewSchedule(42).Run(20)
	for i := 1; i < len(log); i++ {
		if log[i].VirtualTS <= log[i-1].VirtualTS {
			t.Fatalf("step %d: VirtualTS not monotone: %d <= %d",
				i, log[i].VirtualTS, log[i-1].VirtualTS)
		}
	}
}

// TestScheduleStepNumbers proves each Injection.Step records the correct
// zero-based step index.
func TestScheduleStepNumbers(t *testing.T) {
	log := NewSchedule(7).Run(10)
	for i, inj := range log {
		if inj.Step != i {
			t.Fatalf("injection %d has wrong Step %d", i, inj.Step)
		}
	}
}

// TestScheduleCategoryPopulated proves every Injection carries a non-empty
// Category derived from the underlying Fault (either from Categorized or
// from the "uncategorized" fallback).
func TestScheduleCategoryPopulated(t *testing.T) {
	log := NewSchedule(99).Run(30)
	for _, inj := range log {
		if inj.Category == "" {
			t.Fatalf("step %d: empty Category", inj.Step)
		}
	}
}

// TestScheduleRunZeroSteps proves Run(0) returns an empty (non-nil) log.
func TestScheduleRunZeroSteps(t *testing.T) {
	log := NewSchedule(1).Run(0)
	if log == nil {
		t.Fatal("Run(0) must return non-nil InjectionLog")
	}
	if len(log) != 0 {
		t.Fatalf("Run(0) must return empty log, got %d entries", len(log))
	}
}

// ---------------------------------------------------------------------------
// Perturbation proofs (sink-side, CLAUDE-1 §1.1)
// ---------------------------------------------------------------------------

// TestPacketReorderPerturbation proves that PacketReorder.Deliver reorders its
// input: a window-3 delivery of 1,2,3 returns [3,2,1] — not the original order.
//
// MUTATION TARGET: remove the reverse loop in PacketReorder.Deliver (return
// buf in original order) → got == [1,2,3] → fails the reflect.DeepEqual check.
func TestPacketReorderPerturbation(t *testing.T) {
	f := &PacketReorder{From: "a", To: "b", Window: 3}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	// Feed 1,2 — buffered, no output.
	if out := f.Deliver(1); out != nil {
		t.Fatalf("expected nil for step 1, got %v", out)
	}
	if out := f.Deliver(2); out != nil {
		t.Fatalf("expected nil for step 2, got %v", out)
	}
	// Feed 3 — window fills, emit reversed: [3,2,1].
	out := f.Deliver(3)
	want := []int{3, 2, 1}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("PacketReorder: expected %v (reversed), got %v (must differ from input order)", want, out)
	}
}

// TestClockSkewPerturbation proves ClockSkew.Now shifts base by exactly Offset
// while applied.
//
// MUTATION TARGET: return base unconditionally → got == base → fails !=.
func TestClockSkewPerturbation(t *testing.T) {
	base := time.Unix(10000, 0)
	offset := 45 * time.Second
	f := &ClockSkew{NodeID: "n1", Offset: offset}

	// Baseline: unchanged.
	if !f.Now(base).Equal(base) {
		t.Fatal("baseline must pass base through")
	}

	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	got := f.Now(base)
	want := base.Add(offset)
	if !got.Equal(want) {
		t.Fatalf("ClockSkew: expected %v, got %v (must differ from base by exactly %v)", want, got, offset)
	}
	// Explicitly prove it differs from base.
	if got.Equal(base) {
		t.Fatal("ClockSkew applied must perturb base (got == base)")
	}
}

// TestDiskFullPerturbation proves DiskFull.Write rejects writes past CapacityB.
//
// MUTATION TARGET: make Write always return true → the over-capacity write
// succeeds → fails "must be denied".
func TestDiskFullPerturbation(t *testing.T) {
	f := &DiskFull{NodeID: "n1", CapacityB: 100}
	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}
	// Write 60 bytes — fits.
	if !f.Write(60) {
		t.Fatal("write 60 within cap 100 must succeed")
	}
	// Write another 60 bytes — would total 120, exceeds cap.
	if f.Write(60) {
		t.Fatal("DiskFull: write past capacity must be denied (perturbation proof)")
	}
}

// TestByzantinePerturbation proves that ByzantineFault returns conflicting
// (different) values to two distinct peers and that it correctly implements
// Fault + Categorized.
//
// MUTATION TARGET: make Query always return the same value regardless of peer
// → peerA == peerB → fails "must differ".
func TestByzantinePerturbation(t *testing.T) {
	f := &ByzantineFault{
		NodeID: "byzantine-node",
		Values: []string{"leader", "follower"},
	}

	// Verify interface compliance at compile time (static assertion via var).
	var _ Fault = f
	var _ Categorized = f

	if f.Name() != "byzantine" {
		t.Fatalf("expected name 'byzantine', got %q", f.Name())
	}
	if f.Category() != CategoryByzantine {
		t.Fatalf("expected category %q, got %q", CategoryByzantine, f.Category())
	}

	// Baseline: not applied — every peer gets the same (first) canonical value.
	base0 := f.Query("peer-A")
	base1 := f.Query("peer-B")
	if base0 != base1 {
		t.Fatalf("baseline: both peers must get canonical value, got %q vs %q", base0, base1)
	}

	if err := f.Apply(); err != nil {
		t.Fatal(err)
	}

	// While applied: two different peers must receive different values (equivocation).
	valA := f.Query("peer-A")
	valB := f.Query("peer-B")
	if valA == valB {
		t.Fatalf("ByzantineFault: peer-A and peer-B must receive conflicting values, both got %q", valA)
	}
	// Mutation: return Values[0] for all peers → valA==valB → fails above.

	// The same peer must receive a consistent (stable) value within an epoch.
	valA2 := f.Query("peer-A")
	if valA != valA2 {
		t.Fatalf("ByzantineFault: same peer must get consistent value per epoch, got %q then %q", valA, valA2)
	}

	// Restore before round-trip so the apply-state machine is in a clean state.
	if err := f.Restore(); err != nil {
		t.Fatalf("ByzantineFault.Restore before round-trip: %v", err)
	}
	roundTrip(t, f) // re-apply / restore contract.
}

// ---------------------------------------------------------------------------
// Catalog size ≥ 25 including Byzantine
// ---------------------------------------------------------------------------

// TestCatalogContainsByzantine proves the ExtendedCatalog includes ByzantineFault
// and that the total catalog size is ≥ 25 distinct named faults.
//
// MUTATION TARGET: remove ByzantineFault from ExtendedCatalog → seen["byzantine"]
// is never set → t.Fatal fires.
func TestCatalogContainsByzantine(t *testing.T) {
	cat := ExtendedCatalog(1)
	seen := make(map[string]bool)
	for _, f := range cat {
		seen[f.Name()] = true
	}
	if !seen["byzantine"] {
		t.Fatal("ExtendedCatalog must include the byzantine fault")
	}
	if len(seen) < 25 {
		t.Fatalf("catalog must have ≥25 distinct faults, got %d", len(seen))
	}
}

// TestByzantineRoundTrip drives the standard apply-state machine over
// ByzantineFault to prove it is wired up consistently.
func TestByzantineRoundTrip(t *testing.T) {
	f := &ByzantineFault{
		NodeID: "n1",
		Values: []string{"v1", "v2"},
	}
	roundTrip(t, f)
}

// TestScheduleContainsByzantineName proves that across enough steps (200) the
// Byzantine fault appears at least once in the log — confirming it is part of
// the catalog drawn by NewSchedule.
func TestScheduleContainsByzantineName(t *testing.T) {
	log := NewSchedule(555).Run(200)
	for _, inj := range log {
		if inj.FaultName == "byzantine" {
			return
		}
	}
	t.Fatal("expected at least one 'byzantine' injection in 200-step log")
}
