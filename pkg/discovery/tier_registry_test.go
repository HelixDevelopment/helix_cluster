package discovery

import (
	"errors"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/tierdef"
)

// loadTestRegistry returns the canonical T1-T15 tierdef.Registry or fatals.
func loadTestRegistry(t *testing.T) *tierdef.Registry {
	t.Helper()
	td, err := tierdef.Default()
	if err != nil {
		t.Fatalf("tierdef.Default: %v", err)
	}
	return td
}

// makeInst is a convenience constructor for test instances.
func makeInst(id, service string) *Instance {
	return &Instance{
		ID:      id,
		Service: service,
		Address: "127.0.0.1",
		Port:    8080,
		Healthy: true,
	}
}

// ---------------------------------------------------------------------------
// Registration guard-rails
// ---------------------------------------------------------------------------

func TestTierRegistry_RegisterNilInstance(t *testing.T) {
	td := loadTestRegistry(t)
	r := NewTierRegistry(td)
	err := r.Register(nil, "T5")
	if err == nil {
		t.Fatal("expected error for nil instance, got nil")
	}
	if !errors.Is(err, ErrNilInstance) {
		t.Fatalf("expected ErrNilInstance, got %v", err)
	}
}

func TestTierRegistry_RegisterInvalidTier(t *testing.T) {
	td := loadTestRegistry(t)
	r := NewTierRegistry(td)
	err := r.Register(makeInst("x", "svc"), "T16")
	if err == nil {
		t.Fatal("expected error for tier T16 (out of range), got nil")
	}
	if !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("expected ErrInvalidTier, got %v", err)
	}
}

func TestTierRegistry_RegisterValidTiers(t *testing.T) {
	td := loadTestRegistry(t)
	r := NewTierRegistry(td)
	for _, tier := range []string{"T1", "T5", "T8", "T12", "T15"} {
		inst := makeInst("inst-"+tier, "svc")
		if err := r.Register(inst, tier); err != nil {
			t.Fatalf("Register(%s) unexpected error: %v", tier, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Core SelectByTier correctness
// ---------------------------------------------------------------------------

// buildMultiTierRegistry registers one instance per tier at T1, T5, T8, T12, T15.
func buildMultiTierRegistry(t *testing.T) *TierRegistry {
	t.Helper()
	td := loadTestRegistry(t)
	r := NewTierRegistry(td)
	for _, pair := range []struct{ id, tier string }{
		{"inst-T1", "T1"},
		{"inst-T5", "T5"},
		{"inst-T8", "T8"},
		{"inst-T12", "T12"},
		{"inst-T15", "T15"},
	} {
		if err := r.Register(makeInst(pair.id, "svc"), pair.tier); err != nil {
			t.Fatalf("Register %s @ %s: %v", pair.id, pair.tier, err)
		}
	}
	return r
}

// TestSelectByTier_T8 is the primary anti-bluff gate:
// SelectByTier("T8") must return EXACTLY {T8, T12, T15} in that tier order.
// A flipped >= -> <= comparison would return {T1, T5, T8} and the test fails.
func TestSelectByTier_T8(t *testing.T) {
	r := buildMultiTierRegistry(t)

	got, err := r.SelectByTier("T8")
	if err != nil {
		t.Fatalf("SelectByTier(T8) unexpected error: %v", err)
	}

	wantIDs := []string{"inst-T8", "inst-T12", "inst-T15"}
	assertExactOrderedIDs(t, "SelectByTier(T8)", got, wantIDs)
}

// TestSelectByTier_T1 must return all five instances ordered by tier.
func TestSelectByTier_T1(t *testing.T) {
	r := buildMultiTierRegistry(t)

	got, err := r.SelectByTier("T1")
	if err != nil {
		t.Fatalf("SelectByTier(T1) unexpected error: %v", err)
	}

	wantIDs := []string{"inst-T1", "inst-T5", "inst-T8", "inst-T12", "inst-T15"}
	assertExactOrderedIDs(t, "SelectByTier(T1)", got, wantIDs)
}

// TestSelectByTier_T15 must return exactly one instance at T15.
func TestSelectByTier_T15(t *testing.T) {
	r := buildMultiTierRegistry(t)

	got, err := r.SelectByTier("T15")
	if err != nil {
		t.Fatalf("SelectByTier(T15) unexpected error: %v", err)
	}

	wantIDs := []string{"inst-T15"}
	assertExactOrderedIDs(t, "SelectByTier(T15)", got, wantIDs)
}

// TestSelectByTier_InvalidTier verifies that an unrecognised tier returns an error.
func TestSelectByTier_InvalidTier(t *testing.T) {
	r := buildMultiTierRegistry(t)

	_, err := r.SelectByTier("T16")
	if err == nil {
		t.Fatal("SelectByTier(T16) expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("expected ErrInvalidTier, got %v", err)
	}
}

// TestSelectByTier_T12 checks the boundary between T12 and T15.
func TestSelectByTier_T12(t *testing.T) {
	r := buildMultiTierRegistry(t)

	got, err := r.SelectByTier("T12")
	if err != nil {
		t.Fatalf("SelectByTier(T12) unexpected error: %v", err)
	}

	wantIDs := []string{"inst-T12", "inst-T15"}
	assertExactOrderedIDs(t, "SelectByTier(T12)", got, wantIDs)
}

// TestSelectByTier_T5 checks the middle of the range.
func TestSelectByTier_T5(t *testing.T) {
	r := buildMultiTierRegistry(t)

	got, err := r.SelectByTier("T5")
	if err != nil {
		t.Fatalf("SelectByTier(T5) unexpected error: %v", err)
	}

	wantIDs := []string{"inst-T5", "inst-T8", "inst-T12", "inst-T15"}
	assertExactOrderedIDs(t, "SelectByTier(T5)", got, wantIDs)
}

// ---------------------------------------------------------------------------
// SelectExact
// ---------------------------------------------------------------------------

func TestSelectExact_T8(t *testing.T) {
	r := buildMultiTierRegistry(t)

	got, err := r.SelectExact("T8")
	if err != nil {
		t.Fatalf("SelectExact(T8) unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "inst-T8" {
		t.Fatalf("SelectExact(T8): want [inst-T8], got %v", ids(got))
	}
}

func TestSelectExact_InvalidTier(t *testing.T) {
	r := buildMultiTierRegistry(t)
	_, err := r.SelectExact("T99")
	if !errors.Is(err, ErrInvalidTier) {
		t.Fatalf("expected ErrInvalidTier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Deregister
// ---------------------------------------------------------------------------

func TestTierRegistry_Deregister(t *testing.T) {
	r := buildMultiTierRegistry(t)

	r.Deregister("inst-T8")

	// T8 must no longer appear in >= T8 results.
	got, err := r.SelectByTier("T8")
	if err != nil {
		t.Fatalf("SelectByTier(T8) after deregister: %v", err)
	}
	for _, inst := range got {
		if inst.ID == "inst-T8" {
			t.Fatal("deregistered inst-T8 still returned by SelectByTier(T8)")
		}
	}

	// Remaining: T12 and T15 only.
	wantIDs := []string{"inst-T12", "inst-T15"}
	assertExactOrderedIDs(t, "SelectByTier(T8) post-deregister", got, wantIDs)
}

// Deregister of an unknown id must be a no-op (no panic).
func TestTierRegistry_DeregisterUnknown(t *testing.T) {
	r := buildMultiTierRegistry(t)
	r.Deregister("does-not-exist") // must not panic
}

// ---------------------------------------------------------------------------
// Ordering with multiple instances at the same tier
// ---------------------------------------------------------------------------

func TestSelectByTier_OrderingWithinTier(t *testing.T) {
	td := loadTestRegistry(t)
	r := NewTierRegistry(td)

	// Register three instances at T5, IDs intentionally out of alpha order.
	for _, id := range []string{"zzz", "aaa", "mmm"} {
		if err := r.Register(makeInst(id, "svc"), "T5"); err != nil {
			t.Fatalf("Register %s: %v", id, err)
		}
	}

	got, err := r.SelectByTier("T5")
	if err != nil {
		t.Fatalf("SelectByTier(T5): %v", err)
	}

	wantIDs := []string{"aaa", "mmm", "zzz"}
	assertExactOrderedIDs(t, "SelectByTier(T5) same-tier ordering", got, wantIDs)
}

// ---------------------------------------------------------------------------
// Empty registry
// ---------------------------------------------------------------------------

func TestSelectByTier_Empty(t *testing.T) {
	td := loadTestRegistry(t)
	r := NewTierRegistry(td)

	got, err := r.SelectByTier("T1")
	if err != nil {
		t.Fatalf("SelectByTier on empty registry: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %d instances", len(got))
	}
}

// ---------------------------------------------------------------------------
// Mutation canary: flip >= to <= in rankOf check yields wrong results
// ---------------------------------------------------------------------------

// TestSelectByTier_MutationCanary verifies that the T8 test WOULD catch a
// flipped rank comparison by checking the excluded set is absent.
// (The primary canary is TestSelectByTier_T8 — this makes the exclusion explicit.)
func TestSelectByTier_MutationCanary(t *testing.T) {
	r := buildMultiTierRegistry(t)

	got, err := r.SelectByTier("T8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// These must NOT be present (lower than T8).
	excluded := map[string]bool{"inst-T1": true, "inst-T5": true}
	for _, inst := range got {
		if excluded[inst.ID] {
			t.Fatalf("instance %s (below T8) incorrectly included in SelectByTier(T8) — rank comparison is likely flipped", inst.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// assertExactOrderedIDs fails if the IDs of got do not exactly match wantIDs in order.
func assertExactOrderedIDs(t *testing.T, label string, got []*Instance, wantIDs []string) {
	t.Helper()
	if len(got) != len(wantIDs) {
		t.Fatalf("%s: got %d instances %v, want %d %v",
			label, len(got), ids(got), len(wantIDs), wantIDs)
	}
	for i, inst := range got {
		if inst.ID != wantIDs[i] {
			t.Fatalf("%s: position %d: got ID %q, want %q (full: got %v, want %v)",
				label, i, inst.ID, wantIDs[i], ids(got), wantIDs)
		}
	}
}

// ids extracts the IDs from a slice of instances for error messages.
func ids(insts []*Instance) []string {
	out := make([]string, len(insts))
	for i, inst := range insts {
		out[i] = inst.ID
	}
	return out
}
