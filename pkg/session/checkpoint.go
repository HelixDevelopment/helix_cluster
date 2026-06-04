package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
)

// CheckpointVersion is the on-the-wire format version for a session
// Checkpoint. Bump it whenever the serialized layout changes in a way that
// older Restore implementations cannot interpret. Restore rejects any snapshot
// whose Version it does not recognize.
const CheckpointVersion uint32 = 1

// checkpointMagic guards against feeding arbitrary / foreign bytes into the
// gob decoder. It is the first thing Restore validates.
var checkpointMagic = [4]byte{'H', 'X', 'C', 'K'}

// Sentinel errors for Restore failures so callers can branch on cause.
var (
	// ErrCheckpointMagic is returned when the snapshot does not carry the
	// checkpoint magic prefix (not a Checkpoint, or truncated header).
	ErrCheckpointMagic = errors.New("session: not a checkpoint snapshot (bad magic)")
	// ErrCheckpointVersion is returned when the snapshot version is not
	// understood by this build.
	ErrCheckpointVersion = errors.New("session: unsupported checkpoint version")
	// ErrCheckpointChecksum is returned when the payload checksum does not
	// match the recorded checksum (tampered / corrupted snapshot).
	ErrCheckpointChecksum = errors.New("session: checkpoint checksum mismatch")
	// ErrCheckpointDecode is returned when the payload cannot be decoded.
	ErrCheckpointDecode = errors.New("session: checkpoint payload decode failed")
)

// Checkpoint is an opaque, versioned, self-validating snapshot of a session's
// migratable state. It is the pure-Go state-only migration unit: serialize on
// the source with NewCheckpoint, ship the bytes, Restore on the target.
//
// The snapshot is "opaque" to callers: they should treat Data as bytes and not
// reach inside. Integrity is protected by a SHA-256 checksum over (Version ||
// Data); Restore recomputes and compares it before decoding, so a single
// flipped bit anywhere in the body is rejected rather than silently restored.
type Checkpoint struct {
	Magic    [4]byte  // format guard, must equal checkpointMagic
	Version  uint32   // CheckpointVersion at write time
	Checksum [32]byte // sha256(Version-bytes || Data)
	Data     []byte   // gob-encoded migratableState payload (opaque)
}

// migratableState is the concrete, versioned payload carried by a Checkpoint.
// It mirrors the subset of Session that is meaningful to move between nodes.
// Process-attached, node-local fields (NodeID, Status) are intentionally NOT
// part of the restored identity comparison; the target owns those.
type migratableState struct {
	ID            SessionID
	Name          string
	Owner         string
	Backend       SessionBackend
	CPURequest    int64
	MemoryRequest int64
	GPURequest    []string
	Windows       []Window
	Environment   map[string]string
	Labels        map[string]string
}

// NewCheckpoint serializes a session's migratable state into a versioned,
// checksummed Checkpoint. The session is not mutated. Binary-ish payloads
// inside windows/panes (e.g. CRDTState, PTYPath) survive the round-trip
// byte-for-byte.
func NewCheckpoint(session *Session) (*Checkpoint, error) {
	if session == nil {
		return nil, fmt.Errorf("session: cannot checkpoint a nil session")
	}

	ms := migratableState{
		ID:            session.ID,
		Name:          session.Name,
		Owner:         session.Owner,
		Backend:       session.Backend,
		CPURequest:    session.CPURequest,
		MemoryRequest: session.MemoryRequest,
		GPURequest:    session.GPURequest,
		Windows:       session.Windows,
		Environment:   session.Environment,
		Labels:        session.Labels,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&ms); err != nil {
		return nil, fmt.Errorf("session: encode checkpoint payload: %w", err)
	}
	data := buf.Bytes()

	cp := &Checkpoint{
		Magic:    checkpointMagic,
		Version:  CheckpointVersion,
		Checksum: checksumOf(CheckpointVersion, data),
		Data:     data,
	}
	return cp, nil
}

// checkpointWire is a plain mirror of Checkpoint used purely for gob transport.
// Marshalling Checkpoint directly would recurse: Checkpoint defines
// MarshalBinary, which makes gob treat it as a GobEncoder and call MarshalBinary
// again. checkpointWire has no such method, so gob encodes it field-by-field.
type checkpointWire struct {
	Magic    [4]byte
	Version  uint32
	Checksum [32]byte
	Data     []byte
}

// MarshalBinary renders the Checkpoint as a single opaque byte slice suitable
// for shipping over the wire or persisting. The encoding is gob over the
// Checkpoint header + payload, so MarshalBinary/UnmarshalCheckpoint is a
// byte-exact round-trip of the underlying Data.
func (c *Checkpoint) MarshalBinary() ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("session: cannot marshal nil checkpoint")
	}
	w := checkpointWire{
		Magic:    c.Magic,
		Version:  c.Version,
		Checksum: c.Checksum,
		Data:     c.Data,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&w); err != nil {
		return nil, fmt.Errorf("session: marshal checkpoint: %w", err)
	}
	return buf.Bytes(), nil
}

// UnmarshalCheckpoint parses bytes produced by Checkpoint.MarshalBinary back
// into a Checkpoint without yet validating or decoding the payload. Use Restore
// to validate integrity and obtain a Session.
func UnmarshalCheckpoint(data []byte) (*Checkpoint, error) {
	var w checkpointWire
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&w); err != nil {
		return nil, fmt.Errorf("session: unmarshal checkpoint: %w", err)
	}
	return &Checkpoint{
		Magic:    w.Magic,
		Version:  w.Version,
		Checksum: w.Checksum,
		Data:     w.Data,
	}, nil
}

// Restore validates a Checkpoint (magic, version, checksum) and reconstructs
// the migratable Session state on the target. The returned Session carries the
// source's identity and migratable state; targetNode is stamped as the new
// NodeID and Status is set to migrating so callers can resume it locally.
//
// Restore is the integrity gate: a wrong magic, an unknown version, or any
// corruption of the payload (checksum mismatch) is rejected with a specific
// sentinel error instead of producing a partial or zero state.
func (c *Checkpoint) Restore(targetNode string) (*Session, error) {
	if c == nil {
		return nil, fmt.Errorf("session: cannot restore a nil checkpoint")
	}
	if c.Magic != checkpointMagic {
		return nil, fmt.Errorf("%w: got %v", ErrCheckpointMagic, c.Magic)
	}
	if c.Version != CheckpointVersion {
		return nil, fmt.Errorf("%w: snapshot=%d supported=%d",
			ErrCheckpointVersion, c.Version, CheckpointVersion)
	}
	if got := checksumOf(c.Version, c.Data); got != c.Checksum {
		return nil, fmt.Errorf("%w: recorded=%x recomputed=%x",
			ErrCheckpointChecksum, c.Checksum, got)
	}

	var ms migratableState
	if err := gob.NewDecoder(bytes.NewReader(c.Data)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCheckpointDecode, err)
	}

	restored := &Session{
		ID:            ms.ID,
		Name:          ms.Name,
		Owner:         ms.Owner,
		Backend:       ms.Backend,
		Status:        StatusMigrating,
		NodeID:        targetNode,
		CPURequest:    ms.CPURequest,
		MemoryRequest: ms.MemoryRequest,
		GPURequest:    ms.GPURequest,
		Windows:       ms.Windows,
		Environment:   ms.Environment,
		Labels:        ms.Labels,
	}
	return restored, nil
}

// checksumOf computes sha256 over the version bytes followed by the payload.
// Binding the version into the digest means a snapshot whose declared version
// is tampered with (without re-deriving the checksum) also fails the checksum
// gate, not just the version gate.
func checksumOf(version uint32, data []byte) [32]byte {
	h := sha256.New()
	var vb [4]byte
	vb[0] = byte((version >> 24) & 0xff)
	vb[1] = byte((version >> 16) & 0xff)
	vb[2] = byte((version >> 8) & 0xff)
	vb[3] = byte(version & 0xff)
	h.Write(vb[:])
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
