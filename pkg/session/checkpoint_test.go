package session

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

// sampleSession builds a session with a known, non-trivial state including
// binary-ish payloads (CRDTState byte slices, PTY paths), maps and slices so
// the round-trip assertions are field-exact and meaningful.
func sampleSession() *Session {
	return &Session{
		ID:            "sess-checkpoint-1",
		Name:          "build-pipeline",
		Owner:         "alice",
		Status:        StatusRunning,
		Backend:       BackendTmux,
		NodeID:        "node-source",
		CPURequest:    1500,
		MemoryRequest: 2 << 30,
		GPURequest:    []string{"gpu-0", "gpu-3"},
		Environment:   map[string]string{"PATH": "/usr/bin", "LANG": "en_US.UTF-8"},
		Labels:        map[string]string{"team": "infra", "tier": "prod"},
		Windows: []Window{
			{
				ID:        "win-0",
				Name:      "editor",
				Layout:    "even-horizontal",
				Active:    true,
				CRDTState: []byte{0x00, 0x01, 0xff, 0xfe, 0x7f, 0x80},
				Panes: []Pane{
					{
						ID:          "pane-0",
						Command:     "nvim main.go",
						WorkingDir:  "/home/alice/proj",
						Environment: map[string]string{"TERM": "xterm-256color"},
						PTYPath:     "/dev/pts/4",
						CPULimit:    500,
						MemoryLimit: 1 << 30,
						GPUDevice:   "gpu-0",
						Status:      PaneRunning,
						CRDTState:   []byte("\x00binary\xffpane\x00state"),
					},
				},
			},
			{
				ID:        "win-1",
				Name:      "logs",
				Layout:    "main-vertical",
				CRDTState: []byte{0xde, 0xad, 0xbe, 0xef},
			},
		},
	}
}

// assertMigratableEqual compares exactly the fields a Checkpoint carries.
// NodeID and Status are owned by the target and excluded by design.
func assertMigratableEqual(t *testing.T, want, got *Session) {
	t.Helper()
	if want.ID != got.ID {
		t.Fatalf("ID: want %q got %q", want.ID, got.ID)
	}
	if want.Name != got.Name {
		t.Fatalf("Name: want %q got %q", want.Name, got.Name)
	}
	if want.Owner != got.Owner {
		t.Fatalf("Owner: want %q got %q", want.Owner, got.Owner)
	}
	if want.Backend != got.Backend {
		t.Fatalf("Backend: want %q got %q", want.Backend, got.Backend)
	}
	if want.CPURequest != got.CPURequest {
		t.Fatalf("CPURequest: want %d got %d", want.CPURequest, got.CPURequest)
	}
	if want.MemoryRequest != got.MemoryRequest {
		t.Fatalf("MemoryRequest: want %d got %d", want.MemoryRequest, got.MemoryRequest)
	}
	if !reflect.DeepEqual(want.GPURequest, got.GPURequest) {
		t.Fatalf("GPURequest: want %v got %v", want.GPURequest, got.GPURequest)
	}
	if !reflect.DeepEqual(want.Environment, got.Environment) {
		t.Fatalf("Environment: want %v got %v", want.Environment, got.Environment)
	}
	if !reflect.DeepEqual(want.Labels, got.Labels) {
		t.Fatalf("Labels: want %v got %v", want.Labels, got.Labels)
	}
	if !reflect.DeepEqual(want.Windows, got.Windows) {
		t.Fatalf("Windows mismatch:\n want %#v\n got  %#v", want.Windows, got.Windows)
	}
}

// TestCheckpointRestore_FieldExactRoundTrip checkpoints a known session and
// restores it into a fresh target, asserting field-by-field equality.
//
// MUTATION: in Checkpoint.Restore, replace the populated `restored := &Session{
// ID: ms.ID, ...}` body with `restored := &Session{}` (ignore the snapshot,
// return a zero state) -> every field assertion below fails. That is the
// anti-bluff gate proving Restore actually reads the snapshot.
func TestCheckpointRestore_FieldExactRoundTrip(t *testing.T) {
	src := sampleSession()

	cp, err := NewCheckpoint(src)
	if err != nil {
		t.Fatalf("NewCheckpoint: %v", err)
	}

	got, err := cp.Restore("node-target")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	assertMigratableEqual(t, src, got)

	// Target owns these.
	if got.NodeID != "node-target" {
		t.Fatalf("NodeID: want node-target got %q", got.NodeID)
	}
	if got.Status != StatusMigrating {
		t.Fatalf("Status: want migrating got %v", got.Status)
	}
}

// TestCheckpointRestore_BinaryByteExact proves binary-ish payloads survive the
// full Marshal/Unmarshal/Restore round trip byte-for-byte.
//
// MUTATION: change the gob payload in NewCheckpoint to drop Windows
// (`Windows: nil`) -> the byte comparisons below fail (restored CRDTState is
// nil, not the original bytes).
func TestCheckpointRestore_BinaryByteExact(t *testing.T) {
	src := sampleSession()
	wantW0 := append([]byte(nil), src.Windows[0].CRDTState...)
	wantP0 := append([]byte(nil), src.Windows[0].Panes[0].CRDTState...)
	wantW1 := append([]byte(nil), src.Windows[1].CRDTState...)

	cp, err := NewCheckpoint(src)
	if err != nil {
		t.Fatalf("NewCheckpoint: %v", err)
	}
	blob, err := cp.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	cp2, err := UnmarshalCheckpoint(blob)
	if err != nil {
		t.Fatalf("UnmarshalCheckpoint: %v", err)
	}
	got, err := cp2.Restore("node-target")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if !bytes.Equal(got.Windows[0].CRDTState, wantW0) {
		t.Fatalf("win0 CRDTState: want %x got %x", wantW0, got.Windows[0].CRDTState)
	}
	if !bytes.Equal(got.Windows[0].Panes[0].CRDTState, wantP0) {
		t.Fatalf("pane0 CRDTState: want %x got %x", wantP0, got.Windows[0].Panes[0].CRDTState)
	}
	if !bytes.Equal(got.Windows[1].CRDTState, wantW1) {
		t.Fatalf("win1 CRDTState: want %x got %x", wantW1, got.Windows[1].CRDTState)
	}
	if got.Windows[0].Panes[0].PTYPath != "/dev/pts/4" {
		t.Fatalf("PTYPath not preserved: %q", got.Windows[0].Panes[0].PTYPath)
	}
}

// TestCheckpoint_TamperedPayloadRejected flips a byte in the payload and asserts
// Restore rejects it with the checksum sentinel.
//
// MUTATION: delete the checksum comparison block in Restore (the
// `if got := checksumOf(...); got != c.Checksum { return ... }`) -> the
// tampered snapshot restores "successfully" and this test's expectation of an
// error fails.
func TestCheckpoint_TamperedPayloadRejected(t *testing.T) {
	cp, err := NewCheckpoint(sampleSession())
	if err != nil {
		t.Fatalf("NewCheckpoint: %v", err)
	}
	if len(cp.Data) == 0 {
		t.Fatalf("empty payload")
	}
	// Flip a bit somewhere in the middle of the payload.
	cp.Data[len(cp.Data)/2] ^= 0x01

	_, err = cp.Restore("node-target")
	if !errors.Is(err, ErrCheckpointChecksum) {
		t.Fatalf("expected ErrCheckpointChecksum, got %v", err)
	}
}

// TestCheckpoint_WrongVersionRejected asserts an unknown version is rejected.
//
// MUTATION: delete the version check block in Restore -> a snapshot with a
// future version decodes (or fails opaquely) instead of cleanly returning
// ErrCheckpointVersion, failing this assertion.
func TestCheckpoint_WrongVersionRejected(t *testing.T) {
	cp, err := NewCheckpoint(sampleSession())
	if err != nil {
		t.Fatalf("NewCheckpoint: %v", err)
	}
	cp.Version = CheckpointVersion + 99
	// Re-derive the checksum so the version gate, not the checksum gate, is
	// what rejects it — proving the version check is independently load-bearing.
	cp.Checksum = checksumOf(cp.Version, cp.Data)

	_, err = cp.Restore("node-target")
	if !errors.Is(err, ErrCheckpointVersion) {
		t.Fatalf("expected ErrCheckpointVersion, got %v", err)
	}
}

// TestCheckpoint_BadMagicRejected asserts foreign bytes are rejected by magic.
//
// MUTATION: delete the magic check in Restore -> a snapshot with a corrupted
// magic proceeds to version/checksum handling instead of returning
// ErrCheckpointMagic, failing this assertion.
func TestCheckpoint_BadMagicRejected(t *testing.T) {
	cp, err := NewCheckpoint(sampleSession())
	if err != nil {
		t.Fatalf("NewCheckpoint: %v", err)
	}
	cp.Magic[0] = 'X'

	_, err = cp.Restore("node-target")
	if !errors.Is(err, ErrCheckpointMagic) {
		t.Fatalf("expected ErrCheckpointMagic, got %v", err)
	}
}

// TestCheckpoint_VersionTamperCaughtByChecksum proves the version is bound into
// the checksum: tampering the version WITHOUT re-deriving the checksum is
// caught by the checksum gate.
//
// MUTATION: in checksumOf, stop writing the version bytes (`h.Write(vb[:])`)
// -> a version-only tamper no longer perturbs the digest and this test, which
// expects a checksum error, fails.
func TestCheckpoint_VersionTamperCaughtByChecksum(t *testing.T) {
	cp, err := NewCheckpoint(sampleSession())
	if err != nil {
		t.Fatalf("NewCheckpoint: %v", err)
	}
	// Tamper the version but keep it within the supported value so the version
	// gate passes; the checksum (which binds version) must catch it. We set it
	// equal to supported but corrupt nothing else — instead corrupt via a value
	// the checksum was not computed over.
	original := cp.Version
	cp.Version = original // unchanged value, but we corrupt checksum below to isolate binding
	// Directly prove binding: compute a checksum for a DIFFERENT version and
	// install it; Restore must reject because supported-version digest differs.
	cp.Checksum = checksumOf(original+1, cp.Data)

	_, err = cp.Restore("node-target")
	if !errors.Is(err, ErrCheckpointChecksum) {
		t.Fatalf("expected ErrCheckpointChecksum from version-bound digest, got %v", err)
	}
}

// TestCheckpoint_NilSession asserts a nil session cannot be checkpointed.
//
// MUTATION: remove the nil guard in NewCheckpoint -> it panics / returns a
// non-error, failing the error expectation here.
func TestCheckpoint_NilSession(t *testing.T) {
	if _, err := NewCheckpoint(nil); err == nil {
		t.Fatalf("expected error checkpointing nil session")
	}
}

// TestCheckpointStrategy_RegisteredAndProducesRealSnapshot proves the strategy
// is wired into the planner and its Migrate produces a real, non-empty,
// restorable snapshot sized to the actual serialized blob.
//
// MUTATION: make checkpointStrategy.Migrate return
// `&MigrationResult{DataSize: 0}` without building the checkpoint -> the
// DataSize > 0 assertion fails. Make it not register in NewMigrationPlanner ->
// the registration assertion fails.
func TestCheckpointStrategy_RegisteredAndProducesRealSnapshot(t *testing.T) {
	mp := NewMigrationPlanner()
	impl, ok := mp.strategies[StrategyCheckpoint]
	if !ok {
		t.Fatalf("checkpoint strategy not registered in planner")
	}

	// Honest contract: live-process re-attach is out of scope, so CanMigrate
	// reports false (keeps planner preferring CRDT for live sessions).
	if can, _ := impl.CanMigrate(sampleSession()); can {
		t.Fatalf("checkpoint CanMigrate must be false (live-process seam out of scope)")
	}

	res, err := impl.Migrate(context.Background(), sampleSession(), "node-target")
	if err != nil {
		t.Fatalf("checkpoint Migrate: %v", err)
	}
	if res.DataSize <= 0 {
		t.Fatalf("expected real snapshot DataSize > 0, got %d", res.DataSize)
	}
}

// TestCheckpointStrategy_PreMigrateValidates proves PreMigrate exercises the
// real serializer.
//
// MUTATION: make PreMigrate `return nil` unconditionally -> the nil-session
// error expectation fails.
func TestCheckpointStrategy_PreMigrateValidates(t *testing.T) {
	s := &checkpointStrategy{}
	if err := s.PreMigrate(context.Background(), nil, "node-target"); err == nil {
		t.Fatalf("expected PreMigrate error for nil session")
	}
	if err := s.PreMigrate(context.Background(), sampleSession(), "node-target"); err != nil {
		t.Fatalf("PreMigrate unexpected error: %v", err)
	}
}
