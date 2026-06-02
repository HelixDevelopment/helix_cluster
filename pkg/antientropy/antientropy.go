// Package antientropy implements a Cassandra-style three-layer anti-entropy
// repair system over IN-MEMORY simulated replicas. It is pure Go, deterministic
// and uses no real network, host, infra or cgo. Distributed replicas are modeled
// as in-memory structures (maps); all versioning is via explicit integer version
// counters injected by the caller — never time.Now().
//
// The three cooperating mechanisms are:
//
//  1. Hinted handoff. When a target replica is DOWN at write time, the
//     coordinator records a HINT (the missed write) instead of dropping it.
//     When the replica returns, replayHints delivers every stored hint so the
//     replica catches up to the writes it missed while down.
//
//  2. Read repair. A read queries every UP replica. If they disagree, the
//     NEWEST value (highest version) wins AND every stale replica is repaired —
//     written back to the newest value — as a side effect of the read.
//
//  3. Merkle-tree diff. Each replica is summarized by a hierarchical (binary)
//     Merkle tree built over its sorted key/value set. Comparing two trees
//     identifies exactly the divergent keys by descending only into sub-trees
//     whose roots differ; sub-trees with matching roots are pruned (skipped
//     entirely), so identical replicas yield an empty diff without a full
//     key-by-key scan.
package antientropy

import (
	"sort"
)

// VersionedValue is a value tagged with a monotonically-increasing version. The
// version (not wall-clock time) decides which of two concurrent values is newer,
// so all logic is deterministic and testable.
type VersionedValue struct {
	Value   string
	Version uint64
}

// newer reports whether v is strictly newer than other (higher version wins).
func (v VersionedValue) newer(other VersionedValue) bool {
	return v.Version > other.Version
}

// Replica is an in-memory key/value store with a per-key version, plus an up/down
// flag used to simulate an unreachable node. It models one Cassandra replica.
type Replica struct {
	ID    string
	up    bool
	store map[string]VersionedValue
}

// NewReplica returns an empty, UP replica with the given id.
func NewReplica(id string) *Replica {
	return &Replica{ID: id, up: true, store: make(map[string]VersionedValue)}
}

// IsUp reports whether the replica is currently reachable.
func (r *Replica) IsUp() bool { return r.up }

// SetUp marks the replica reachable (true) or unreachable (false).
func (r *Replica) SetUp(up bool) { r.up = up }

// apply writes vv for key only if it is newer than what the replica already
// holds (last-write-wins by version). It reports whether the store changed.
// This is the convergence rule shared by direct writes, hint replay and read
// repair, guaranteeing all three drive the replica toward the newest value.
func (r *Replica) apply(key string, vv VersionedValue) bool {
	cur, ok := r.store[key]
	if ok && !vv.newer(cur) {
		return false
	}
	r.store[key] = vv
	return true
}

// Get returns the versioned value for key and whether it is present.
func (r *Replica) Get(key string) (VersionedValue, bool) {
	vv, ok := r.store[key]
	return vv, ok
}

// Snapshot returns a copy of the replica's plain key->value map (newest values),
// used to build a Merkle tree without exposing internal state.
func (r *Replica) Snapshot() map[string]string {
	out := make(map[string]string, len(r.store))
	for k, vv := range r.store {
		out[k] = vv.Value
	}
	return out
}

// hint is a write that was buffered for a replica that was down at write time.
type hint struct {
	key string
	vv  VersionedValue
}

// Coordinator fronts a fixed set of replicas and implements the write/read paths
// with hinted handoff and read repair. It is the single entry point an end user
// (client) talks to, exactly as in Cassandra.
type Coordinator struct {
	replicas []*Replica
	// hints maps replica ID -> ordered list of writes it missed while down.
	hints map[string][]hint
}

// NewCoordinator builds a coordinator over the given replicas.
func NewCoordinator(replicas ...*Replica) *Coordinator {
	return &Coordinator{replicas: replicas, hints: make(map[string][]hint)}
}

// Replicas returns the underlying replicas (read-only view for tests/inspection).
func (c *Coordinator) Replicas() []*Replica { return c.replicas }

// Write sends (key, value@version) to every replica. UP replicas apply it
// immediately (last-write-wins by version). For each DOWN replica the write is
// NOT dropped: a hint is buffered so the replica can catch up later via
// replayHints. Returns the number of replicas that were up and took the write.
func (c *Coordinator) Write(key, value string, version uint64) int {
	vv := VersionedValue{Value: value, Version: version}
	applied := 0
	for _, r := range c.replicas {
		if r.IsUp() {
			r.apply(key, vv)
			applied++
		} else {
			c.hints[r.ID] = append(c.hints[r.ID], hint{key: key, vv: vv})
		}
	}
	return applied
}

// PendingHints reports how many hints are currently buffered for a replica.
func (c *Coordinator) PendingHints(replicaID string) int {
	return len(c.hints[replicaID])
}

// replayHints delivers every buffered hint for the given replica, in arrival
// order, then clears the buffer. The replica must be UP; replay is the mechanism
// by which a recovered node catches up on writes it missed. Returns the number
// of hints delivered. If the replica is down, nothing is replayed.
func (c *Coordinator) replayHints(r *Replica) int {
	if !r.IsUp() {
		return 0
	}
	pending := c.hints[r.ID]
	for _, h := range pending {
		r.apply(h.key, h.vv)
	}
	delivered := len(pending)
	delete(c.hints, r.ID)
	return delivered
}

// ReplayHints is the exported wrapper around replayHints so callers (and the
// recovery path) can trigger handoff when a replica comes back up.
func (c *Coordinator) ReplayHints(r *Replica) int { return c.replayHints(r) }

// ReadResult is what a coordinated read returns to the client.
type ReadResult struct {
	Value    string
	Version  uint64
	Found    bool
	Repaired []string // IDs of replicas that were stale and got repaired
}

// Read queries every UP replica for key, picks the NEWEST value (highest
// version), and as a side effect REPAIRS every UP replica that held a stale or
// missing value by writing the newest value back to it. The set of repaired
// replica IDs is reported so callers can observe the repair actually happened.
//
// This is read repair: divergence discovered during a read is reconciled
// immediately, not left for a background process.
func (c *Coordinator) Read(key string) ReadResult {
	var best VersionedValue
	found := false
	// First pass: find the newest value across all up replicas.
	for _, r := range c.replicas {
		if !r.IsUp() {
			continue
		}
		if vv, ok := r.Get(key); ok {
			if !found || vv.newer(best) {
				best = vv
				found = true
			}
		}
	}
	if !found {
		return ReadResult{Found: false}
	}
	// Second pass: repair every up replica that is not already at the newest
	// value. apply returns true exactly when a write-back occurred.
	var repaired []string
	for _, r := range c.replicas {
		if !r.IsUp() {
			continue
		}
		if r.apply(key, best) {
			repaired = append(repaired, r.ID)
		}
	}
	sort.Strings(repaired)
	return ReadResult{Value: best.Value, Version: best.Version, Found: true, Repaired: repaired}
}
