// Package swim provides the SWIM (Scalable Weakly-consistent Infection-style
// Membership) gossip protocol implementation. This file adds a pure, stateless
// HierarchicalTopology that partitions a membership snapshot into LAN groups
// (intra-group flood) and elects deterministic per-group WAN delegates so that
// cross-group gossip is O(groups) rather than O(nodes).
//
// The topology is intentionally decoupled from the live gossip loop: it is
// computed once from an immutable []*Member snapshot and is replaced wholesale
// when membership changes. Wiring it into the protocol runtime is a separate
// future item (HXC-1344 follow-up).
package swim

import (
	"fmt"
	"hash/fnv"
	"sort"
)

// DefaultGroupKeyFn returns the value of the "zone" metadata key, or the empty
// string if the key is absent or if the member has no Metadata map.
//
// This is the default groupKeyFn used by [NewHierarchicalTopology] when the
// caller passes nil.
func DefaultGroupKeyFn(m *Member) string {
	if m.Metadata == nil {
		return ""
	}
	return m.Metadata["zone"]
}

// stableHash returns a deterministic 64-bit hash of a string using FNV-1a.
// It never panics: the hash.Hash64 Write method is documented as always
// returning (n, nil).
func stableHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprint(h, s) // nolint:errcheck — FNV Write is infallible.
	return h.Sum64()
}

// HierarchicalTopology is an immutable, read-only view of a membership snapshot
// organised into a two-tier gossip hierarchy:
//
//   - Intra-group (LAN): flood-style gossip to all alive peers within the same
//     group.
//   - Inter-group (WAN): only the K elected delegates per group participate.
//     The WAN fanout is therefore O(groups × K) not O(total_nodes).
//
// Groups are derived from the injected groupKeyFn. Delegates are elected
// deterministically by sorting the alive members of each group by the stable
// FNV-1a hash of their ID and taking the first delegatesPerGroup members.
// Determinism is guaranteed: given the same membership snapshot and the same
// groupKeyFn, every call to NewHierarchicalTopology produces an identical
// topology.
//
// [StateAlive] and [StateSuspect] members are gossip-eligible; [StateDead]
// and [StateLeft] members are excluded.
type HierarchicalTopology struct {
	// groups maps group key -> sorted slice of alive member IDs.
	groups map[string][]string

	// delegates maps group key -> elected delegate IDs (sorted by member ID for
	// stable output, elected by stableHash of ID).
	delegates map[string][]*Member

	// memberGroup maps member ID -> group key for O(1) GroupFor lookups.
	memberGroup map[string]string

	// delegateSet is the flat set of all delegate IDs for O(1) IsDelegate.
	delegateSet map[string]bool

	// memberByID maps member ID -> *Member for SelectGossipTargets.
	memberByID map[string]*Member

	// sortedGroupKeys is the pre-sorted list of group keys for Groups().
	sortedGroupKeys []string
}

// NewHierarchicalTopology builds an immutable HierarchicalTopology from
// members, a group-key function, and the maximum number of delegates to elect
// per group.
//
// Parameters:
//   - members: snapshot of all cluster members (may include non-gossip-eligible
//     members; only [StateAlive] and [StateSuspect] members participate in
//     gossip — matching the live protocol's gossip-eligible definition).
//   - groupKeyFn: maps a *Member to its group/zone key. Pass nil to use
//     [DefaultGroupKeyFn] (reads Member.Metadata["zone"]).
//   - delegatesPerGroup: maximum number of WAN delegates to elect per group
//     (clamped to the number of gossip-eligible members in that group). Values
//     ≤ 0 are treated as 1.
func NewHierarchicalTopology(
	members []*Member,
	groupKeyFn func(*Member) string,
	delegatesPerGroup int,
) *HierarchicalTopology {
	if groupKeyFn == nil {
		groupKeyFn = DefaultGroupKeyFn
	}
	if delegatesPerGroup <= 0 {
		delegatesPerGroup = 1
	}

	// groupMembers collects alive members per group.
	groupMembers := make(map[string][]*Member)
	memberGroup := make(map[string]string)
	memberByID := make(map[string]*Member)

	for _, m := range members {
		// StateDead and StateLeft members are not gossip-eligible; StateAlive and
		// StateSuspect both participate in gossip rounds (matching protocol.go's
		// live treatment of suspects as active participants).
		if m.State == StateDead || m.State == StateLeft {
			continue
		}
		key := groupKeyFn(m)
		groupMembers[key] = append(groupMembers[key], m)
		memberGroup[m.ID] = key
		memberByID[m.ID] = m
	}

	// Build sorted group keys.
	sortedGroupKeys := make([]string, 0, len(groupMembers))
	for k := range groupMembers {
		sortedGroupKeys = append(sortedGroupKeys, k)
	}
	sort.Strings(sortedGroupKeys)

	// For each group sort members by stableHash(ID) and elect the first K.
	delegates := make(map[string][]*Member, len(groupMembers))
	delegateSet := make(map[string]bool)

	for _, gk := range sortedGroupKeys {
		alive := groupMembers[gk]

		// Sort by stableHash(ID) — deterministic tie-break on ID string if hashes
		// collide (extremely rare with FNV-1a on distinct strings).
		sort.Slice(alive, func(i, j int) bool {
			hi := stableHash(alive[i].ID)
			hj := stableHash(alive[j].ID)
			if hi != hj {
				return hi < hj
			}
			return alive[i].ID < alive[j].ID
		})

		k := delegatesPerGroup
		if k > len(alive) {
			k = len(alive)
		}
		elected := alive[:k]

		// Store a copy of the elected slice (already sorted by hash; expose sorted
		// by ID for stable test assertions).
		byID := make([]*Member, len(elected))
		copy(byID, elected)
		sort.Slice(byID, func(i, j int) bool { return byID[i].ID < byID[j].ID })
		delegates[gk] = byID

		for _, d := range elected {
			delegateSet[d.ID] = true
		}
	}

	// groups maps group key -> sorted member IDs (for introspection; not used by
	// SelectGossipTargets which works directly on memberByID).
	groups := make(map[string][]string, len(groupMembers))
	for gk, ms := range groupMembers {
		ids := make([]string, len(ms))
		for i, m := range ms {
			ids[i] = m.ID
		}
		sort.Strings(ids)
		groups[gk] = ids
	}

	return &HierarchicalTopology{
		groups:          groups,
		delegates:       delegates,
		memberGroup:     memberGroup,
		delegateSet:     delegateSet,
		memberByID:      memberByID,
		sortedGroupKeys: sortedGroupKeys,
	}
}

// Groups returns the sorted list of all group keys derived from the membership
// snapshot (only groups that contain at least one alive member appear).
func (ht *HierarchicalTopology) Groups() []string {
	out := make([]string, len(ht.sortedGroupKeys))
	copy(out, ht.sortedGroupKeys)
	return out
}

// Delegates returns the deterministically elected WAN delegates for group.
// The returned slice is sorted by member ID for stable iteration. Returns nil
// if the group is unknown.
func (ht *HierarchicalTopology) Delegates(group string) []*Member {
	ds, ok := ht.delegates[group]
	if !ok {
		return nil
	}
	out := make([]*Member, len(ds))
	copy(out, ds)
	return out
}

// IsDelegate reports whether the member identified by memberID is a WAN
// delegate in any group.
func (ht *HierarchicalTopology) IsDelegate(memberID string) bool {
	return ht.delegateSet[memberID]
}

// GroupFor returns the group key of the member identified by memberID.
// Returns the empty string if memberID is unknown or the member was not alive
// at topology construction time.
func (ht *HierarchicalTopology) GroupFor(memberID string) string {
	return ht.memberGroup[memberID]
}

// SelectGossipTargets returns the recommended set of gossip targets for a
// given node:
//
//   - Non-delegates: returns up to intraFanout alive peers from the same group
//     (excluding self), sorted deterministically by stableHash(ID). This
//     implements intra-group LAN flood.
//
//   - Delegates: returns up to intraFanout alive same-group peers (same as
//     above) PLUS the delegates of every OTHER group (WAN fanout). The combined
//     slice is deduplicated and sorted by member ID for deterministic output.
//
// selfID is excluded from every returned slice. intraFanout ≤ 0 returns an
// empty intra-group slice (delegates still receive cross-group targets).
func (ht *HierarchicalTopology) SelectGossipTargets(selfID string, intraFanout int) []*Member {
	selfGroup := ht.memberGroup[selfID]

	// --- intra-group peers (same group, excluding self) ---
	intraIDs := ht.groups[selfGroup]
	// Sort by stableHash for deterministic selection.
	intraByHash := make([]*Member, 0, len(intraIDs))
	for _, id := range intraIDs {
		if id == selfID {
			continue
		}
		if m, ok := ht.memberByID[id]; ok {
			intraByHash = append(intraByHash, m)
		}
	}
	sort.Slice(intraByHash, func(i, j int) bool {
		hi := stableHash(intraByHash[i].ID)
		hj := stableHash(intraByHash[j].ID)
		if hi != hj {
			return hi < hj
		}
		return intraByHash[i].ID < intraByHash[j].ID
	})

	var intra []*Member
	if intraFanout > 0 {
		limit := intraFanout
		if limit > len(intraByHash) {
			limit = len(intraByHash)
		}
		intra = intraByHash[:limit]
	}

	if !ht.delegateSet[selfID] {
		// Non-delegate: only intra-group peers.
		out := make([]*Member, len(intra))
		copy(out, intra)
		return out
	}

	// Delegate: intra peers + delegates of all OTHER groups.
	seen := make(map[string]bool, len(intra))
	result := make([]*Member, 0, len(intra)+len(ht.sortedGroupKeys))

	for _, m := range intra {
		if !seen[m.ID] {
			seen[m.ID] = true
			result = append(result, m)
		}
	}

	for _, gk := range ht.sortedGroupKeys {
		if gk == selfGroup {
			continue
		}
		for _, d := range ht.delegates[gk] {
			if d.ID == selfID || seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			result = append(result, d)
		}
	}

	// Sort final output by member ID for deterministic ordering.
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
