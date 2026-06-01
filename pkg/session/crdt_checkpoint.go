package session

import (
	"fmt"
)

// CRDTCheckpointVersion distinguishes CRDT-payload checkpoints from
// session-payload (migratableState) checkpoints so that Restore and
// RestoreCRDT fail fast when handed the wrong kind.
const CRDTCheckpointVersion uint32 = 2

// NewCRDTCheckpoint serializes a CRDTSessionState into a versioned,
// checksummed Checkpoint. The payload is the gob encoding produced by
// CRDTSessionState.ToBytes(). The CRDTCheckpointVersion (2) in the header
// distinguishes this from a session-state Checkpoint (version 1), so
// Checkpoint.Restore will reject it and RestoreCRDT will accept it.
func NewCRDTCheckpoint(state *CRDTSessionState) (*Checkpoint, error) {
	if state == nil {
		return nil, fmt.Errorf("session: cannot checkpoint nil CRDT state")
	}
	data, err := state.ToBytes()
	if err != nil {
		return nil, fmt.Errorf("session: encode CRDT checkpoint payload: %w", err)
	}
	cp := &Checkpoint{
		Magic:    checkpointMagic,
		Version:  CRDTCheckpointVersion,
		Checksum: checksumOf(CRDTCheckpointVersion, data),
		Data:     data,
	}
	return cp, nil
}

// RestoreCRDT validates a CRDT Checkpoint (magic, version, checksum) and
// decodes the payload into a CRDTSessionState. It is the counterpart to
// NewCRDTCheckpoint and rejects checkpoints created by NewCheckpoint (session
// version 1) or any checkpoint with a tampered header/payload.
func (c *Checkpoint) RestoreCRDT() (*CRDTSessionState, error) {
	if c == nil {
		return nil, fmt.Errorf("session: cannot restore nil checkpoint")
	}
	if c.Magic != checkpointMagic {
		return nil, fmt.Errorf("%w: got %v", ErrCheckpointMagic, c.Magic)
	}
	if c.Version != CRDTCheckpointVersion {
		return nil, fmt.Errorf("%w: snapshot=%d supported=%d (want CRDT version %d)",
			ErrCheckpointVersion, c.Version, CRDTCheckpointVersion, CRDTCheckpointVersion)
	}
	if got := checksumOf(c.Version, c.Data); got != c.Checksum {
		return nil, fmt.Errorf("%w: recorded=%x recomputed=%x",
			ErrCheckpointChecksum, c.Checksum, got)
	}
	state, err := FromBytes(c.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCheckpointDecode, err)
	}
	return state, nil
}
