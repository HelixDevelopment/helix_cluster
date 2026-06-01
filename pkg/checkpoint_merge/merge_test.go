package checkpointmerge_test

import (
	"errors"
	"testing"

	checkpointmerge "github.com/HelixDevelopment/helix_cluster/pkg/checkpoint_merge"
	"github.com/HelixDevelopment/helix_cluster/pkg/session"
)

// makeWindow is a test helper that builds a CRDTWindow with the given ID,
// name, layout and explicit version.
func makeWindow(id, name, layout string, version uint64) *session.CRDTWindow {
	return &session.CRDTWindow{
		ID:      id,
		Name:    name,
		Layout:  layout,
		Panes:   make(map[string]*session.CRDTPane),
		Version: version,
	}
}

// TestApply_Divergence_LWWWinner verifies that Apply returns the EXACT
// window content that the LWW tiebreak selects, not just "some non-zero
// value". The test is constructed so that a mutation flipping the version
// comparison (> vs >=) or swapping the content-key direction makes it fail.
//
// Setup:
//
//	State A: window "w1" with Name="alice", Layout="even-horizontal", Version=10
//	State B: window "w1" with Name="bob",   Layout="main-vertical",   Version=5
//
// LWW rule (from convergence.go): higher Version wins for scalar fields.
// A.Version=10 > B.Version=5, therefore A's scalars must dominate.
// Expected merged Name="alice", Layout="even-horizontal".
func TestApply_Divergence_LWWWinner(t *testing.T) {
	stateA := session.NewCRDTSessionState()
	stateA.UpdateWindow(makeWindow("w1", "alice", "even-horizontal", 10))

	stateB := session.NewCRDTSessionState()
	stateB.UpdateWindow(makeWindow("w1", "bob", "main-vertical", 5))

	cpA, err := session.NewCRDTCheckpoint(stateA)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint: %v", err)
	}

	merged, err := checkpointmerge.Apply(stateB, cpA)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	w, ok := merged.GetWindow("w1")
	if !ok {
		t.Fatal("expected window w1 in merged state")
	}

	// DETERMINISTIC ASSERTION: the EXACT winner values, not just "non-zero".
	// If the LWW rule is broken (e.g. B's Version=5 wrongly wins) this fails.
	if w.Name != "alice" {
		t.Errorf("LWW winner: got Name=%q, want %q (stateA version 10 must dominate version 5)", w.Name, "alice")
	}
	if w.Layout != "even-horizontal" {
		t.Errorf("LWW winner: got Layout=%q, want %q", w.Layout, "even-horizontal")
	}
	// Version in the merged window must be the higher of the two.
	if w.Version != 10 {
		t.Errorf("LWW winner: got Version=%d, want 10", w.Version)
	}
}

// TestApply_Divergence_LWWWinner_TieBreak verifies the deterministic
// content-key tiebreak (lexicographic Name+"\x00"+Layout) when both replicas
// carry the SAME version. This is the commutativity-under-collision path.
//
// Setup:
//
//	State A: window "w1" with Name="zebra", Layout="z-layout", Version=7
//	State B: window "w1" with Name="alpha", Layout="a-layout", Version=7
//
// Content key rule (windowContentKey):  Name + "\x00" + Layout
//
//	A key = "zebra\x00z-layout"
//	B key = "alpha\x00a-layout"
//
// "zebra\x00z-layout" > "alpha\x00a-layout" lexicographically → A wins.
// Expected merged Name="zebra", Layout="z-layout".
func TestApply_Divergence_LWWWinner_TieBreak(t *testing.T) {
	stateA := session.NewCRDTSessionState()
	stateA.UpdateWindow(makeWindow("w1", "zebra", "z-layout", 7))

	stateB := session.NewCRDTSessionState()
	stateB.UpdateWindow(makeWindow("w1", "alpha", "a-layout", 7))

	cpA, err := session.NewCRDTCheckpoint(stateA)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint: %v", err)
	}

	merged, err := checkpointmerge.Apply(stateB, cpA)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	w, ok := merged.GetWindow("w1")
	if !ok {
		t.Fatal("expected window w1 in merged state")
	}

	// Deterministic content-key tiebreak: "zebra\x00z-layout" > "alpha\x00a-layout"
	if w.Name != "zebra" {
		t.Errorf("content-key tiebreak: got Name=%q, want %q", w.Name, "zebra")
	}
	if w.Layout != "z-layout" {
		t.Errorf("content-key tiebreak: got Layout=%q, want %q", w.Layout, "z-layout")
	}
}

// TestApply_Commutativity verifies that Apply(B, cpA) and Apply(A, cpB)
// produce states with identical Fingerprints (observable convergence).
// This is the CvRDT convergence property proven at the merge-orchestration
// level, not just the Merge level.
func TestApply_Commutativity(t *testing.T) {
	stateA := session.NewCRDTSessionState()
	stateA.UpdateWindow(makeWindow("w1", "alice", "even-horizontal", 10))
	stateA.UpdateWindow(makeWindow("w2", "shared-a", "tiled", 3))

	stateB := session.NewCRDTSessionState()
	stateB.UpdateWindow(makeWindow("w1", "bob", "main-vertical", 5))
	stateB.UpdateWindow(makeWindow("w3", "only-b", "main-horizontal", 1))

	cpA, err := session.NewCRDTCheckpoint(stateA)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint(A): %v", err)
	}
	cpB, err := session.NewCRDTCheckpoint(stateB)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint(B): %v", err)
	}

	// Apply A's checkpoint into B's local state.
	mergedBA, err := checkpointmerge.Apply(stateB, cpA)
	if err != nil {
		t.Fatalf("Apply(B, cpA): %v", err)
	}
	// Apply B's checkpoint into A's local state.
	mergedAB, err := checkpointmerge.Apply(stateA, cpB)
	if err != nil {
		t.Fatalf("Apply(A, cpB): %v", err)
	}

	fpBA := mergedBA.Fingerprint()
	fpAB := mergedAB.Fingerprint()

	if fpBA != fpAB {
		t.Errorf("commutativity violated:\n  Apply(B,cpA) fingerprint = %s\n  Apply(A,cpB) fingerprint = %s",
			fpBA, fpAB)
	}
}

// TestApply_Idempotent verifies that applying a checkpoint of the same state
// twice (Apply(s, cp(s))) produces the same Fingerprint as a single apply.
// The second apply must not change the observable state.
func TestApply_Idempotent(t *testing.T) {
	s := session.NewCRDTSessionState()
	s.UpdateWindow(makeWindow("w1", "idempotent-test", "tiled", 4))
	s.UpdateWindow(makeWindow("w2", "another", "even-vertical", 2))

	cp, err := session.NewCRDTCheckpoint(s)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint: %v", err)
	}

	// First apply: merge s with its own checkpoint.
	once, err := checkpointmerge.Apply(s, cp)
	if err != nil {
		t.Fatalf("Apply (first): %v", err)
	}

	// Second apply: merge the result with the same checkpoint again.
	cp2, err := session.NewCRDTCheckpoint(s)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint (second): %v", err)
	}
	twice, err := checkpointmerge.Apply(once, cp2)
	if err != nil {
		t.Fatalf("Apply (second): %v", err)
	}

	fp1 := once.Fingerprint()
	fp2 := twice.Fingerprint()

	if fp1 != fp2 {
		t.Errorf("idempotence violated:\n  first apply  = %s\n  second apply = %s", fp1, fp2)
	}
}

// TestMigrate_EndToEnd verifies that Migrate(A, B) produces the same
// Fingerprint as Apply(B, NewCRDTCheckpoint(A)), confirming that the
// Migrate helper is a faithful wrapper around the export/import pipeline.
func TestMigrate_EndToEnd(t *testing.T) {
	stateA := session.NewCRDTSessionState()
	stateA.UpdateWindow(makeWindow("w1", "migrate-source", "tiled", 8))

	stateB := session.NewCRDTSessionState()
	stateB.UpdateWindow(makeWindow("w1", "local-state", "main-vertical", 3))
	stateB.UpdateWindow(makeWindow("w2", "only-local", "even-horizontal", 2))

	// Path 1: Migrate(A, B)
	migrateResult, err := checkpointmerge.Migrate(stateA, stateB)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Path 2: manual Apply(B, NewCRDTCheckpoint(A))
	cpA, err := session.NewCRDTCheckpoint(stateA)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint: %v", err)
	}
	applyResult, err := checkpointmerge.Apply(stateB, cpA)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	fpMigrate := migrateResult.Fingerprint()
	fpApply := applyResult.Fingerprint()

	if fpMigrate != fpApply {
		t.Errorf("Migrate/Apply mismatch:\n  Migrate fingerprint = %s\n  Apply fingerprint   = %s",
			fpMigrate, fpApply)
	}

	// Also assert the actual merged content for w1: A's Version=8 > B's Version=3
	// so A's values must win. This makes the test fail if Migrate mis-routes
	// the merge direction.
	w, ok := migrateResult.GetWindow("w1")
	if !ok {
		t.Fatal("expected window w1 in migrate result")
	}
	if w.Name != "migrate-source" {
		t.Errorf("Migrate w1 winner: got Name=%q, want %q", w.Name, "migrate-source")
	}
	// w2 is only in B; it must survive (union semantics).
	if _, ok := migrateResult.GetWindow("w2"); !ok {
		t.Error("Migrate: expected w2 (only-local) to survive in merged state")
	}
}

// TestApply_CorruptCheckpoint verifies that Apply returns a sentinel error
// (one of session.ErrCheckpoint*) when given a checkpoint with a tampered
// payload. The test constructs a valid checkpoint, flips a byte in its Data
// field, and expects Apply to reject it with a checksum or decode error.
func TestApply_CorruptCheckpoint(t *testing.T) {
	s := session.NewCRDTSessionState()
	s.UpdateWindow(makeWindow("w1", "to-corrupt", "tiled", 1))

	cp, err := session.NewCRDTCheckpoint(s)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint: %v", err)
	}

	// Corrupt the payload by flipping a byte. len(cp.Data) >= 1 is guaranteed
	// since we have at least one window encoded.
	if len(cp.Data) == 0 {
		t.Fatal("unexpected empty checkpoint data")
	}
	cp.Data[0] ^= 0xFF // flip all bits in the first byte

	local := session.NewCRDTSessionState()
	_, err = checkpointmerge.Apply(local, cp)
	if err == nil {
		t.Fatal("Apply with corrupt checkpoint: expected an error, got nil")
	}

	// The error must wrap one of the sentinel errors, not be a generic "unknown"
	// error. If a new error path is introduced that bypasses the sentinels, this
	// assertion catches it.
	isSentinel := errors.Is(err, session.ErrCheckpointChecksum) ||
		errors.Is(err, session.ErrCheckpointDecode) ||
		errors.Is(err, session.ErrCheckpointMagic) ||
		errors.Is(err, session.ErrCheckpointVersion)

	if !isSentinel {
		t.Errorf("Apply with corrupt checkpoint returned non-sentinel error: %v", err)
	}
}

// TestApply_NilLocal and TestApply_NilCheckpoint verify that nil inputs are
// rejected with a non-nil error (not a panic). These are defensive guards.
func TestApply_NilLocal(t *testing.T) {
	s := session.NewCRDTSessionState()
	cp, err := session.NewCRDTCheckpoint(s)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err = checkpointmerge.Apply(nil, cp)
	if err == nil {
		t.Error("Apply(nil, cp): expected error, got nil")
	}
}

func TestApply_NilCheckpoint(t *testing.T) {
	s := session.NewCRDTSessionState()
	_, err := checkpointmerge.Apply(s, nil)
	if err == nil {
		t.Error("Apply(s, nil): expected error, got nil")
	}
}
