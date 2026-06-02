// Package configsync provides conflict-free, coordinator-less synchronisation
// of cell-local configuration overrides across Helix Cluster replicas.
//
// Each replica owns a [ConfigStore] that holds one [crdt.LWWRegister] per
// configuration key and a single [crdt.ORSet] that tracks which keys are
// "live" (i.e. not deleted). Merging two stores is performed key-by-key via
// LWWRegister.Merge (timestamp wins; equal-timestamp tiebreak delegates to
// ReplicaID lexicographic ordering) plus ORSet.Merge for the live-key set.
// The composition is commutative, associative, and idempotent — all three
// CRDT laws are upheld without a coordinator.
//
// # Delete semantics
//
// Deleting a key calls [crdt.ORSet].Remove, which tombstones every add-tag
// currently observed by this replica. A subsequent [ConfigStore.Get] for that
// key returns (_, false) even if its LWWRegister still holds a stale value,
// because [ConfigStore.Keys] / [ConfigStore.Snapshot] only enumerate keys
// that the ORSet reports as live. A concurrent Set on another replica
// (with a higher timestamp) that arrives via Merge resurrects the key by
// re-adding it to the live-set before merging the LWW value — making the
// final state deterministic without external coordination.
package configsync

import (
	"sort"
	"sync"

	"github.com/HelixDevelopment/helix_cluster/pkg/crdt"
)

// ConfigStore is a per-replica, goroutine-safe map of configuration keys to
// values, backed by a [crdt.LWWRegister] per key and a [crdt.ORSet] that
// tracks live (non-deleted) keys.
//
// All exported methods are safe for concurrent use.
type ConfigStore struct {
	mu       sync.RWMutex
	replica  crdt.ReplicaID
	regs     map[string]*crdt.LWWRegister // one register per key (including tombstoned keys)
	liveKeys *crdt.ORSet                  // tracks which keys are currently live
}

// NewConfigStore returns an empty [ConfigStore] owned by the given replica.
func NewConfigStore(replica crdt.ReplicaID) *ConfigStore {
	return &ConfigStore{
		replica:  replica,
		regs:     make(map[string]*crdt.LWWRegister),
		liveKeys: crdt.NewORSet(),
	}
}

// Set writes value for key at timestamp using this store's ReplicaID.
// It updates the key's LWWRegister (the register's own LWW rule decides
// whether the write wins) and adds the key to the live-set ORSet so that
// previously deleted keys become visible again.
func (s *ConfigStore) Set(key, value string, timestamp uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reg := s.reg(key)
	reg.Set(value, timestamp, s.replica)
	// (Re-)add the key to the live-set; ORSet.Add mints a fresh unique tag, so
	// a concurrent Remove on another replica does not retroactively tombstone
	// this write (add-wins semantics).
	s.liveKeys.Add(key, s.replica)
}

// Get returns the current value for key and whether the key is live.
// A key is considered absent if it was never set, or if it has been deleted
// and not subsequently re-set (via a direct call or a Merge).
func (s *ConfigStore) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.liveKeys.Contains(key) {
		return "", false
	}
	reg, ok := s.regs[key]
	if !ok {
		return "", false
	}
	v, set := reg.Value()
	if !set {
		return "", false
	}
	return v, true
}

// Delete removes key from the live-set by tombstoning every add-tag this
// replica has currently observed. The underlying LWWRegister value is
// intentionally preserved so that a concurrent higher-timestamp Set arriving
// via Merge can resurrect the key deterministically.
func (s *ConfigStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.liveKeys.Remove(key)
}

// Merge integrates the state of other into s. For each key that appears in
// either store the corresponding LWWRegisters are merged (the higher-timestamp
// write wins; equal timestamps break by ReplicaID). The live-set ORSets are
// also merged, meaning deletions and concurrent adds both propagate correctly.
//
// Merge is safe to call concurrently on s; it locks both stores during the
// operation (s first, then a snapshot of other to avoid deadlock).
func (s *ConfigStore) Merge(other *ConfigStore) {
	// Take a consistent snapshot of other under its own read-lock so that we
	// never hold two store locks simultaneously (avoids AB/BA deadlock).
	otherRegs, otherLive := other.snapshot()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Merge the live-key ORSet first.
	s.liveKeys = s.liveKeys.Merge(otherLive)

	// Merge every register from other into s.
	for key, otherReg := range otherRegs {
		local := s.reg(key)
		merged := local.Merge(otherReg)
		s.regs[key] = merged
	}
}

// Keys returns a sorted slice of keys that are currently live (i.e. present
// and not deleted).
func (s *ConfigStore) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	raw := s.liveKeys.Elements()
	// Filter to keys that have a set register value; defensive against a
	// live-set entry that somehow lost its register (should never happen in
	// normal operation but keeps the contract clean).
	out := raw[:0:len(raw)]
	for _, k := range raw {
		if reg, ok := s.regs[k]; ok {
			if _, set := reg.Value(); set {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Snapshot returns a map of all live key–value pairs.
func (s *ConfigStore) Snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string)
	for _, k := range s.liveKeys.Elements() {
		reg, ok := s.regs[k]
		if !ok {
			continue
		}
		v, set := reg.Value()
		if set {
			result[k] = v
		}
	}
	return result
}

// reg returns (and lazily creates) the LWWRegister for key.
// Must be called with s.mu held (at least for writing).
func (s *ConfigStore) reg(key string) *crdt.LWWRegister {
	if r, ok := s.regs[key]; ok {
		return r
	}
	r := crdt.NewLWWRegister()
	s.regs[key] = r
	return r
}

// snapshot returns deep copies of the register map and live-set of s, under
// s's read-lock. Used by Merge to avoid holding two locks simultaneously.
func (s *ConfigStore) snapshot() (map[string]*crdt.LWWRegister, *crdt.ORSet) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	regs := make(map[string]*crdt.LWWRegister, len(s.regs))
	for k, r := range s.regs {
		regs[k] = r.Clone()
	}
	return regs, s.liveKeys.Clone()
}
