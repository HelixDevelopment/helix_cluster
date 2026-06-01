// Package checkpointmerge implements the container-checkpoint migration
// orchestration layer for Helix Cluster OS (HXC-1152).
//
// The genuine gap this package fills: taking an incoming Checkpoint (produced
// by node A) and MERGING it into a local CRDTSessionState (on node B) to
// produce a convergent result. The CRDT merge logic and checkpoint
// serialization already exist in pkg/session; this package wires them into a
// single, tested migration primitive.
//
// # Import-cycle note
//
// pkg/session/crdt_checkpoint.go exposes NewCRDTCheckpoint and
// Checkpoint.RestoreCRDT to allow pkg/checkpoint_merge to call back into the
// session package without creating a cycle. pkg/session does NOT import
// pkg/checkpoint_merge; higher-level orchestration code calls
// checkpointmerge.Apply / checkpointmerge.Migrate.
//
// # Wiring into checkpointStrategy
//
// pkg/session/migration.go's checkpointStrategy.PostMigrate is the intended
// call site for the CRDT-merge step on the target node. Because importing
// pkg/checkpoint_merge from pkg/session would create a cycle, the wiring is
// done by the higher-level orchestration layer:
//
//	// On the target node, after the checkpoint has been received:
//	merged, err := checkpointmerge.Apply(localCRDTState, receivedCheckpoint)
//
// The checkpointStrategy.CanMigrate remains false (live-process re-attach is
// still out of scope); this package handles the pure-state convergence half.
package checkpointmerge

import (
	"fmt"

	"github.com/HelixDevelopment/helix_cluster/pkg/session"
)

// Apply restores the CRDTSessionState encoded in cp, then merges it into a
// clone of local, returning the convergent result. Neither local nor cp is
// mutated.
//
// The merge follows LWW (last-write-wins) semantics as defined by
// session.CRDTSessionState.Merge: higher window/pane Version wins; equal
// versions are resolved by a deterministic content-key tiebreak so that
// Apply(A, cpB) and Apply(B, cpA) converge to the same observable state.
//
// Apply returns a sentinel error (session.ErrCheckpoint*) if cp is corrupt,
// has the wrong magic, the wrong version, or fails the checksum gate.
func Apply(local *session.CRDTSessionState, cp *session.Checkpoint) (*session.CRDTSessionState, error) {
	if local == nil {
		return nil, fmt.Errorf("checkpointmerge: local state is nil")
	}
	if cp == nil {
		return nil, fmt.Errorf("checkpointmerge: checkpoint is nil")
	}

	// Decode the checkpoint payload into a CRDTSessionState.
	// RestoreCRDT validates magic, version (must be CRDTCheckpointVersion=2),
	// and SHA-256 checksum before decoding.
	incoming, err := cp.RestoreCRDT()
	if err != nil {
		return nil, fmt.Errorf("checkpointmerge: restore checkpoint: %w", err)
	}

	// Clone local so we never mutate the caller's state.
	merged := local.Clone()

	// Merge incoming into the clone. LWW semantics: windows/panes with higher
	// Version dominate; equal versions use a deterministic content-key total
	// order so the merge is commutative and idempotent.
	if err := merged.Merge(incoming); err != nil {
		return nil, fmt.Errorf("checkpointmerge: merge: %w", err)
	}

	return merged, nil
}

// Migrate models the full export-from-A -> merge-on-B pipeline end to end:
//
//  1. NewCRDTCheckpoint(source) — serialize source's CRDT state into a
//     versioned, checksummed Checkpoint (as node A would produce).
//  2. Apply(local, cp) — restore and merge the checkpoint into local (as node
//     B would consume).
//
// The returned state is the convergent result. source and local are not
// mutated.
func Migrate(source *session.CRDTSessionState, local *session.CRDTSessionState) (*session.CRDTSessionState, error) {
	if source == nil {
		return nil, fmt.Errorf("checkpointmerge: source state is nil")
	}
	cp, err := session.NewCRDTCheckpoint(source)
	if err != nil {
		return nil, fmt.Errorf("checkpointmerge: create checkpoint: %w", err)
	}
	result, err := Apply(local, cp)
	if err != nil {
		return nil, fmt.Errorf("checkpointmerge: apply: %w", err)
	}
	return result, nil
}
