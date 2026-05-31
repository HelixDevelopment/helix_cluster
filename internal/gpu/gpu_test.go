package gpu

import (
	"encoding/json"
	"testing"
)

// TestGPUStatusStringKnownAndUnknown proves String() maps known statuses to
// their names and renders unknown values explicitly rather than returning "".
// Mutation: change the fallback to `return ""` -> the unknown-status assertion
// fails because an empty string is produced.
func TestGPUStatusStringKnownAndUnknown(t *testing.T) {
	cases := map[GPUStatus]string{
		Available: "available",
		Allocated: "allocated",
		Unhealthy: "unhealthy",
		Offline:   "offline",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("String(%d)=%q, want %q", int(s), got, want)
		}
	}

	unknown := GPUStatus(99)
	if got := unknown.String(); got != "GPUStatus(99)" {
		t.Errorf("unknown String()=%q, want GPUStatus(99)", got)
	}
}

// TestGPUStatusMarshalRoundTrip proves the wire form is the lowercase name and
// that it round-trips back to the same enum value.
// Mutation: make MarshalJSON emit the numeric value (json.Marshal(int(s))) ->
// the decoded JSON string assertion fails.
func TestGPUStatusMarshalRoundTrip(t *testing.T) {
	data, err := json.Marshal(Allocated)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if string(data) != `"allocated"` {
		t.Fatalf("expected \"allocated\", got %s", data)
	}

	var back GPUStatus
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if back != Allocated {
		t.Fatalf("round-trip mismatch: got %v", back)
	}
}

// TestGPUStatusUnmarshalInvalid proves an unrecognized status string is a hard
// error and does not silently default to Available (status 0).
// Mutation: change the final `return fmt.Errorf(...)` to `return nil` ->
// the invalid string is accepted with no error, failing this test.
func TestGPUStatusUnmarshalInvalid(t *testing.T) {
	var s GPUStatus = Allocated
	err := s.UnmarshalJSON([]byte(`"bogus"`))
	if err == nil {
		t.Fatal("expected error unmarshaling invalid status")
	}
	// Must not have clobbered the receiver to a zero/default value.
	if s != Allocated {
		t.Fatalf("receiver mutated on invalid unmarshal: got %v", s)
	}
}

// TestGPUCloneIsDeepCopy proves Clone returns an independent copy whose
// mutation does not leak back to the original (the Manager relies on this to
// hand out snapshots safely).
// Mutation: make Clone `return g` instead of copying -> mutating the clone
// changes the original, failing the assertion.
func TestGPUCloneIsDeepCopy(t *testing.T) {
	orig := &GPU{ID: "gpu-0", Status: Available, AllocatedTo: ""}
	clone := orig.Clone()
	clone.Status = Allocated
	clone.AllocatedTo = "job-1"

	if orig.Status != Available || orig.AllocatedTo != "" {
		t.Fatalf("Clone is not independent: orig=%+v", orig)
	}
	if clone == orig {
		t.Fatal("Clone returned the same pointer")
	}
}

// TestGPUCloneNil proves Clone tolerates a nil receiver.
// Mutation: remove the `if g == nil` guard -> this panics with a nil deref.
func TestGPUCloneNil(t *testing.T) {
	var g *GPU
	if g.Clone() != nil {
		t.Fatal("expected nil clone of nil GPU")
	}
}
