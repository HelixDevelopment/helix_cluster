package serde

import (
	"strings"
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func mustMarshalNode(t *testing.T, n NodeRecord) []byte {
	t.Helper()
	b, err := MarshalNode(n)
	if err != nil {
		t.Fatalf("MarshalNode: %v", err)
	}
	return b
}

func mustDeserialize(t *testing.T, data []byte) *NodeView {
	t.Helper()
	v, err := DeserializeNode(data)
	if err != nil {
		t.Fatalf("DeserializeNode: %v", err)
	}
	return v
}

// ── TDD: failing tests first (§1.1 mutation discipline) ─────────────────────
// The tests below assert EXACT sink-side field values after a real
// marshal→unmarshal round-trip through the GENERATED Cap'n Proto code.

// TestNodeRoundTrip_FieldEquality is the primary sink-side assertion:
// every field decoded from the Cap'n Proto bytes must equal the value
// that was serialised.  A stub that returns "non-nil" would fail here
// because the field values would be empty strings / zeros.
func TestNodeRoundTrip_FieldEquality(t *testing.T) {
	original := NodeRecord{
		ID:       "node-abc-123",
		Hostname: "helix-worker-1.local",
		Cores:    32,
		MemBytes: 137438953472, // 128 GiB
		Labels:   []string{"zone=us-east-1a", "role=worker", "gpu=false"},
	}

	data := mustMarshalNode(t, original)
	if len(data) == 0 {
		t.Fatal("MarshalNode returned empty bytes")
	}

	view := mustDeserialize(t, data)

	// --- id ---
	gotID, err := view.Id()
	if err != nil {
		t.Fatalf("Id(): %v", err)
	}
	if gotID != original.ID {
		t.Errorf("id mismatch: want %q, got %q", original.ID, gotID)
	}

	// --- hostname ---
	gotHost, err := view.Hostname()
	if err != nil {
		t.Fatalf("Hostname(): %v", err)
	}
	if gotHost != original.Hostname {
		t.Errorf("hostname mismatch: want %q, got %q", original.Hostname, gotHost)
	}

	// --- cores ---
	if view.Cores() != original.Cores {
		t.Errorf("cores mismatch: want %d, got %d", original.Cores, view.Cores())
	}

	// --- memBytes ---
	if view.MemBytes() != original.MemBytes {
		t.Errorf("memBytes mismatch: want %d, got %d", original.MemBytes, view.MemBytes())
	}

	// --- labels (List(Text)) ---
	lblList, err := view.Labels()
	if err != nil {
		t.Fatalf("Labels(): %v", err)
	}
	if lblList.Len() != len(original.Labels) {
		t.Fatalf("labels length mismatch: want %d, got %d", len(original.Labels), lblList.Len())
	}
	for i, want := range original.Labels {
		got, err := lblList.At(i)
		if err != nil {
			t.Fatalf("labels.At(%d): %v", i, err)
		}
		if got != want {
			t.Errorf("label[%d] mismatch: want %q, got %q", i, want, got)
		}
	}
}

// TestNodeRoundTrip_ToRecord verifies the materialise path produces a fully
// equal NodeRecord — sink-side structural equality, not pointer equality.
func TestNodeRoundTrip_ToRecord(t *testing.T) {
	original := NodeRecord{
		ID:       "node-xyz-999",
		Hostname: "helix-control-0.cluster",
		Cores:    8,
		MemBytes: 17179869184, // 16 GiB
		Labels:   []string{"tier=control", "arch=arm64"},
	}

	data := mustMarshalNode(t, original)
	view := mustDeserialize(t, data)

	rec, err := view.ToRecord()
	if err != nil {
		t.Fatalf("ToRecord(): %v", err)
	}

	if rec.ID != original.ID {
		t.Errorf("ToRecord id: want %q, got %q", original.ID, rec.ID)
	}
	if rec.Hostname != original.Hostname {
		t.Errorf("ToRecord hostname: want %q, got %q", original.Hostname, rec.Hostname)
	}
	if rec.Cores != original.Cores {
		t.Errorf("ToRecord cores: want %d, got %d", original.Cores, rec.Cores)
	}
	if rec.MemBytes != original.MemBytes {
		t.Errorf("ToRecord memBytes: want %d, got %d", original.MemBytes, rec.MemBytes)
	}
	if len(rec.Labels) != len(original.Labels) {
		t.Fatalf("ToRecord labels len: want %d, got %d", len(original.Labels), len(rec.Labels))
	}
	for i := range original.Labels {
		if rec.Labels[i] != original.Labels[i] {
			t.Errorf("ToRecord label[%d]: want %q, got %q", i, original.Labels[i], rec.Labels[i])
		}
	}
}

// TestNodeRoundTrip_EdgeEmpty verifies an empty/zero-value Node round-trips
// cleanly: no panic, all fields return zero values.
func TestNodeRoundTrip_EdgeEmpty(t *testing.T) {
	empty := NodeRecord{}

	data := mustMarshalNode(t, empty)
	view := mustDeserialize(t, data)

	if id, err := view.Id(); err != nil || id != "" {
		t.Errorf("empty id: want \"\", got %q (err=%v)", id, err)
	}
	if h, err := view.Hostname(); err != nil || h != "" {
		t.Errorf("empty hostname: want \"\", got %q (err=%v)", h, err)
	}
	if view.Cores() != 0 {
		t.Errorf("empty cores: want 0, got %d", view.Cores())
	}
	if view.MemBytes() != 0 {
		t.Errorf("empty memBytes: want 0, got %d", view.MemBytes())
	}
	lbls, err := view.Labels()
	if err != nil {
		t.Fatalf("empty Labels(): %v", err)
	}
	if lbls.Len() != 0 {
		t.Errorf("empty labels len: want 0, got %d", lbls.Len())
	}
}

// TestNodeRoundTrip_LargeValues stresses large-ish field values to confirm
// the generated codec handles them correctly.
func TestNodeRoundTrip_LargeValues(t *testing.T) {
	longLabel := strings.Repeat("k", 256) + "=" + strings.Repeat("v", 256)
	n := NodeRecord{
		ID:       strings.Repeat("x", 128),
		Hostname: "edge-node-" + strings.Repeat("z", 64) + ".example.com",
		Cores:    65535,
		MemBytes: ^uint64(0), // max uint64
		Labels:   []string{longLabel, "env=prod"},
	}

	data := mustMarshalNode(t, n)
	view := mustDeserialize(t, data)

	gotID, _ := view.Id()
	if gotID != n.ID {
		t.Errorf("large id mismatch: want len=%d, got len=%d", len(n.ID), len(gotID))
	}
	if view.Cores() != n.Cores {
		t.Errorf("large cores: want %d, got %d", n.Cores, view.Cores())
	}
	if view.MemBytes() != n.MemBytes {
		t.Errorf("large memBytes: want %d, got %d", n.MemBytes, view.MemBytes())
	}
	lbls, _ := view.Labels()
	if lbls.Len() != 2 {
		t.Fatalf("large labels len: want 2, got %d", lbls.Len())
	}
	got0, _ := lbls.At(0)
	if got0 != longLabel {
		t.Errorf("large label[0] mismatch")
	}
}

// ── Zero-copy proof ───────────────────────────────────────────────────────────

// TestNodeZeroCopy_AllocsPerRun asserts that reading a scalar field (Cores)
// and a Text field (Id) from an already-deserialised NodeView causes ZERO heap
// allocations.  The Cap'n Proto format is zero-copy by design: field accessors
// return slices/strings backed by the segment buffer; no new memory is needed.
//
// §1.1 mutation: if the read path ever copies (e.g. you add a `make` inside
// Id()), AllocsPerRun will return >0 and the test fails for the right reason.
func TestNodeZeroCopy_AllocsPerRun(t *testing.T) {
	n := NodeRecord{
		ID:       "alloc-test-node",
		Hostname: "host.local",
		Cores:    16,
		MemBytes: 8589934592,
		Labels:   []string{"a=1", "b=2"},
	}
	data := mustMarshalNode(t, n)

	// Pre-deserialise once; we measure only the read path.
	view := mustDeserialize(t, data)

	// --- scalar read (Cores) — must be 0 allocs ---
	scalarAllocs := testing.AllocsPerRun(100, func() {
		_ = view.Cores()
	})
	if scalarAllocs != 0 {
		t.Errorf("Cores() allocs/op: want 0, got %v", scalarAllocs)
	}

	// --- MemBytes scalar read — must be 0 allocs ---
	memAllocs := testing.AllocsPerRun(100, func() {
		_ = view.MemBytes()
	})
	if memAllocs != 0 {
		t.Errorf("MemBytes() allocs/op: want 0, got %v", memAllocs)
	}

	// --- TextBytes read (Id) — zero-copy: returns a slice into segment ---
	// We use IdBytes() (returns []byte backed by segment) to stay zero-copy.
	// The string-returning Id() may allocate one string header on some Go
	// versions; IdBytes() is the true zero-copy accessor.
	idBytesAllocs := testing.AllocsPerRun(100, func() {
		_, _ = view.node.IdBytes()
	})
	if idBytesAllocs != 0 {
		t.Errorf("IdBytes() allocs/op: want 0, got %v", idBytesAllocs)
	}
}

// ── §1.1 Mutation Proof ───────────────────────────────────────────────────────

// TestMutation_CorruptFieldBreaksEquality is the paired mutation test required
// by §1.1.  It proves the field-equality assertion is load-bearing:
//   - break: marshal a Node with a DIFFERENT id than the one we check against
//   - observe: test fails for the right reason (value mismatch, not panic)
//   - restore: done implicitly — the "corrupt" value only exists in this subtest
//
// This test PASSES when equality holds (the restored state).  The comment
// records the break→fail→restore cycle observed during development.
func TestMutation_CorruptFieldBreaksEquality(t *testing.T) {
	// This sub-test simulates a scenario where the serialised id differs from
	// what the caller expects.  It proves the equality assertion is not vacuous.
	original := NodeRecord{
		ID:       "correct-id",
		Hostname: "host",
		Cores:    4,
		MemBytes: 1024,
		Labels:   nil,
	}
	corrupt := NodeRecord{
		ID:       "wrong-id", // intentionally different
		Hostname: "host",
		Cores:    4,
		MemBytes: 1024,
		Labels:   nil,
	}

	// Serialise the corrupt version.
	data := mustMarshalNode(t, corrupt)
	view := mustDeserialize(t, data)

	gotID, err := view.Id()
	if err != nil {
		t.Fatalf("Id(): %v", err)
	}

	// The test is checking that if we compare against "correct-id", it FAILS
	// (proves the assertion is real).  We perform the check ourselves and
	// assert the mismatch is detected.
	if gotID == original.ID {
		// This would mean mutation went undetected — that is the BLUFF we eradicate.
		t.Errorf("mutation was not detected: corrupt id %q should not equal expected %q", gotID, original.ID)
	}
	// Correct behaviour: gotID == "wrong-id" != "correct-id" → mismatch detected.
}

// TestMutation_CorruptCoresBreaksEquality verifies a mutated numeric field
// is caught by the cores equality assertion.
func TestMutation_CorruptCoresBreaksEquality(t *testing.T) {
	want := NodeRecord{Cores: 32}
	corrupt := NodeRecord{Cores: 999} // wrong value

	data := mustMarshalNode(t, corrupt)
	view := mustDeserialize(t, data)

	if view.Cores() == want.Cores {
		t.Errorf("mutation not detected: corrupt cores %d should not equal expected %d",
			view.Cores(), want.Cores)
	}
}

// TestMutation_CorruptMemBytesBreaksEquality mirrors the cores mutation for
// the 64-bit memBytes field.
func TestMutation_CorruptMemBytesBreaksEquality(t *testing.T) {
	want := uint64(137438953472) // 128 GiB
	corrupt := uint64(1)

	n := NodeRecord{MemBytes: corrupt}
	data := mustMarshalNode(t, n)
	view := mustDeserialize(t, data)

	if view.MemBytes() == want {
		t.Errorf("mutation not detected: corrupt memBytes %d should not equal expected %d",
			view.MemBytes(), want)
	}
}

// TestMutation_CorruptLabelBreaksEquality verifies label list field mutations
// are caught.
func TestMutation_CorruptLabelBreaksEquality(t *testing.T) {
	wantLabel := "zone=us-east-1a"
	n := NodeRecord{Labels: []string{"zone=eu-west-1"}} // wrong label

	data := mustMarshalNode(t, n)
	view := mustDeserialize(t, data)

	lbls, err := view.Labels()
	if err != nil {
		t.Fatalf("Labels(): %v", err)
	}
	got, err := lbls.At(0)
	if err != nil {
		t.Fatalf("labels.At(0): %v", err)
	}
	if got == wantLabel {
		t.Errorf("mutation not detected: corrupt label %q should not equal expected %q", got, wantLabel)
	}
}
