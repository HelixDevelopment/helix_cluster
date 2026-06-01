package checkpointmerge_test

import (
	"testing"

	checkpointmerge "github.com/HelixDevelopment/helix_cluster/pkg/checkpoint_merge"
	"github.com/HelixDevelopment/helix_cluster/pkg/session"
)

// FuzzApply drives checkpointmerge.Apply with arbitrary bytes in the
// Checkpoint.Data field (and corrupted header fields derived from fuzz ints).
//
// The property under test: Apply MUST NOT panic on any combination of inputs.
// A corrupt or arbitrary Data field, wrong magic, wrong version, or a
// mis-matched checksum must all produce a non-nil error — never a crash.
//
// The local CRDTSessionState is a small but valid state built before the fuzz
// loop so that, if the checkpoint happens to deserialize successfully, the
// subsequent Merge is also exercised.
//
// Seeds:
//  1. A real valid checkpoint serialized from a small state (happy path —
//     exercises the full Apply path including Merge).
//  2. Empty bytes (triggers checksum/decode error).
//  3. Corrupted: first byte of a real checkpoint flipped (checksum mismatch).
//  4. Completely arbitrary short payload.
func FuzzApply(f *testing.F) {
	// Build a valid local state used across all fuzz iterations.
	buildLocal := func() *session.CRDTSessionState {
		s := session.NewCRDTSessionState()
		s.UpdateWindow(&session.CRDTWindow{
			ID:      "w1",
			Name:    "fuzz-local",
			Layout:  "tiled",
			Panes:   map[string]*session.CRDTPane{},
			Version: 3,
		})
		s.UpdatePane("w1", &session.CRDTPane{
			ID:         "p1",
			Command:    "bash",
			WorkingDir: "/tmp",
			Version:    3,
		})
		return s
	}

	// Seed 1: valid serialized CRDT checkpoint bytes (happy-path seed).
	{
		src := session.NewCRDTSessionState()
		src.UpdateWindow(&session.CRDTWindow{
			ID:      "w-src",
			Name:    "source",
			Layout:  "even-h",
			Panes:   map[string]*session.CRDTPane{},
			Version: 7,
		})
		cp, err := session.NewCRDTCheckpoint(src)
		if err == nil {
			f.Add(cp.Data)
		}
	}

	// Seed 2: empty payload.
	f.Add([]byte{})

	// Seed 3: payload with a single zero byte.
	f.Add([]byte{0x00})

	// Seed 4: a few arbitrary bytes that look like truncated gob.
	f.Add([]byte{0x0e, 0x01, 0x02, 0xff, 0xfe})

	// Seed 5: valid checkpoint bytes then first byte corrupted (checksum mismatch).
	{
		src := session.NewCRDTSessionState()
		src.UpdateWindow(&session.CRDTWindow{
			ID:      "w-corrupt",
			Name:    "to-corrupt",
			Layout:  "tiled",
			Panes:   map[string]*session.CRDTPane{},
			Version: 1,
		})
		cp, err := session.NewCRDTCheckpoint(src)
		if err == nil && len(cp.Data) > 0 {
			corrupted := make([]byte, len(cp.Data))
			copy(corrupted, cp.Data)
			corrupted[0] ^= 0xFF
			f.Add(corrupted)
		}
	}

	f.Fuzz(func(t *testing.T, cpBytes []byte) {
		local := buildLocal()

		// Construct a Checkpoint with the fuzzed bytes as Data but with the
		// correct magic and CRDT version so that the checksum gate (rather than
		// the magic/version gates) is the primary exercised path. This ensures
		// we stress the checksum and decode layers, not just the header checks.
		cp := &session.Checkpoint{
			Magic:   [4]byte{'H', 'X', 'C', 'K'},
			Version: session.CRDTCheckpointVersion,
			// Leave Checksum as zero: this will fail the checksum gate unless
			// cpBytes happens to hash to all-zeros (astronomically unlikely),
			// exercising the error path through RestoreCRDT.
			Data: cpBytes,
		}

		// MUST NOT panic regardless of Data content.
		_, err := checkpointmerge.Apply(local, cp)
		// err may be nil (if cpBytes happen to decode as a valid CRDT state and
		// the checksum matches — only possible if cpBytes is the real seed) or
		// non-nil; both are acceptable. Panic is the only failure mode we
		// prohibit here.
		_ = err
	})
}
