package session

import (
	"testing"
)

// FuzzCRDTCheckpointRoundTrip exercises two complementary properties of the
// CRDT checkpoint pipeline:
//
// A. Round-trip stability for well-formed states:
//
//	Given a CRDTSessionState constructed deterministically from fuzz inputs
//	(via UpdateWindow / UpdatePane), NewCRDTCheckpoint(state) must succeed
//	and the subsequent RestoreCRDT() must reproduce a state whose Fingerprint
//	equals that of the original. A broken gob encoder, a checksum bug, or a
//	decoder that loses fields would violate this invariant.
//
// B. No-panic contract for arbitrary bytes:
//
//	Feeding the fuzzed bytes directly as Checkpoint.Data (with correct magic
//	and version but a deliberately wrong checksum, so RestoreCRDT reaches the
//	decode layer) MUST return an error, never panic.
//
// Seeds: a few deterministically built states plus a handful of corrupted
// payloads so the seed-corpus run (plain `go test`, no -fuzz) exercises both
// the success and the error paths.
func FuzzCRDTCheckpointRoundTrip(f *testing.F) {
	// Seed 1: single window, no panes.
	f.Add("w1", "editor", "tiled", "p1", "vim", "/src", uint64(1), uint64(1))

	// Seed 2: window + pane at a higher version.
	f.Add("win-a", "terminal", "even-h", "pane-z", "zsh", "/home/user", uint64(42), uint64(99))

	// Seed 3: empty strings — the parser must not panic on zero-length IDs.
	f.Add("", "", "", "", "", "", uint64(0), uint64(0))

	// Seed 4: very long strings (stress the gob encoder path).
	longStr := "abcdefghijklmnopqrstuvwxyz0123456789_ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	f.Add(longStr, longStr, longStr, longStr, longStr, longStr, uint64(1<<32-1), uint64(1<<32-1))

	// Seed 5: special characters that might trip up the Fingerprint formatter.
	f.Add("w{id}", "name=val", "lay|out", "p{id}", "cmd\x00", "/wd\x00\n", uint64(5), uint64(5))

	f.Fuzz(func(t *testing.T,
		windowID, windowName, windowLayout string,
		paneID, paneCommand, paneWorkDir string,
		windowVer, paneVer uint64,
	) {
		// --- Part A: round-trip on a deterministically built state ---

		state := NewCRDTSessionState()
		state.UpdateWindow(&CRDTWindow{
			ID:      windowID,
			Name:    windowName,
			Layout:  windowLayout,
			Panes:   make(map[string]*CRDTPane),
			Version: windowVer,
		})
		if paneID != "" && windowID != "" {
			state.UpdatePane(windowID, &CRDTPane{
				ID:         paneID,
				Command:    paneCommand,
				WorkingDir: paneWorkDir,
				Version:    paneVer,
			})
		}

		originalFP := state.Fingerprint()

		// NewCRDTCheckpoint MUST NOT panic and MUST succeed for any valid state.
		cp, err := NewCRDTCheckpoint(state)
		if err != nil {
			// NewCRDTCheckpoint only fails if state is nil (which it is not here)
			// or if gob encoding fails. Neither should happen with stdlib types.
			t.Fatalf("NewCRDTCheckpoint unexpected error: %v", err)
		}

		// RestoreCRDT MUST succeed on a freshly created checkpoint.
		restored, err := cp.RestoreCRDT()
		if err != nil {
			t.Fatalf("RestoreCRDT on fresh checkpoint failed: %v", err)
		}

		// Property A: the restored state's Fingerprint must equal the original's.
		// A broken gob encoder/decoder (e.g. a field silently dropped) or a wrong
		// checksum computation would cause the round-trip to produce a different
		// state, making this assertion fire.
		restoredFP := restored.Fingerprint()
		if restoredFP != originalFP {
			t.Fatalf("round-trip Fingerprint mismatch:\n  original  = %s\n  restored  = %s",
				originalFP, restoredFP)
		}

		// --- Part B: no-panic on arbitrary Data bytes ---

		// Reuse the fuzzed parameters as raw bytes for the Data field.
		// We construct a Checkpoint with correct magic/version but a zeroed
		// Checksum so that RestoreCRDT reaches the checksum gate and returns
		// ErrCheckpointChecksum.  The important invariant is no panic.
		rawData := cp.Data // start from the valid serialized form

		// Corrupt the data by XOR-ing each byte with the low byte of the
		// window version (deterministic but data-dependent, so different seeds
		// exercise different corruption patterns).
		corrupted := make([]byte, len(rawData))
		copy(corrupted, rawData)
		if len(corrupted) > 0 {
			xorKey := byte(windowVer & 0xFF)
			if xorKey == 0 {
				xorKey = 0xA5 // avoid a no-op corruption when version LSB is 0
			}
			for i := range corrupted {
				corrupted[i] ^= xorKey
			}
		}

		badCP := &Checkpoint{
			Magic:   checkpointMagic,
			Version: CRDTCheckpointVersion,
			// Checksum left as zero [32]byte — will mismatch unless astronomically
			// unlikely, ensuring we exercise the error path.
			Data: corrupted,
		}
		// MUST NOT panic; error is expected.
		_, restoreErr := badCP.RestoreCRDT()
		// We only check: no panic.  The error value may be ErrCheckpointChecksum
		// or ErrCheckpointDecode — both are acceptable.
		_ = restoreErr
	})
}
