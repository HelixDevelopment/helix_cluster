// Package rebalance implements Kafka-style cooperative-sticky incremental
// partition rebalancing in pure, deterministic Go.
//
// Model: a fixed set of partitions is assigned across a set of consumers
// (the group members). On a membership change (a consumer joins or a consumer
// leaves), Reassign computes a NEW assignment that is balanced — every consumer
// holds a partition count within +/-1 of the average — while MINIMIZING the
// number of partitions that change owner. Existing (consumer, partition) pairs
// are kept STICKY: a partition is only moved when it MUST move to restore
// balance (because its owner left, or because its owner is over the per-consumer
// ceiling and an under-target consumer needs work).
//
// This mirrors the Kafka CooperativeStickyAssignor goal: minimal partition
// movement across rebalances so that revoked partitions (and the associated
// state / in-flight work) are kept to the absolute minimum.
package rebalance

import "sort"

// Assignment maps a consumer ID to the set of partition IDs it owns.
type Assignment map[string][]string

// Reassign computes a new, balanced, minimal-movement assignment of partitions
// across members, starting from the previous assignment prev.
//
// Rules:
//   - Every partition in partitions is assigned to exactly one member.
//   - A member keeps a previously-owned partition unless that partition must be
//     moved to restore balance (sticky).
//   - Partitions whose previous owner is no longer a member are released and
//     redistributed to the least-loaded members.
//   - Partitions not present in partitions are dropped; partitions present but
//     never previously assigned are treated as new and given to least-loaded
//     members.
//   - Final loads differ by at most 1 (each member holds floor or ceil of the
//     average).
//
// The algorithm is deterministic: inputs are sorted, and ties are broken by
// member ID, so identical inputs always yield identical output.
func Reassign(prev Assignment, members []string, partitions []string) Assignment {
	// Sorted, de-duplicated member set for deterministic iteration.
	memberSet := make(map[string]bool, len(members))
	sortedMembers := make([]string, 0, len(members))
	for _, m := range members {
		if !memberSet[m] {
			memberSet[m] = true
			sortedMembers = append(sortedMembers, m)
		}
	}
	sort.Strings(sortedMembers)

	next := make(Assignment, len(sortedMembers))
	for _, m := range sortedMembers {
		next[m] = []string{}
	}

	if len(sortedMembers) == 0 {
		return next
	}

	// Valid partition set (sorted for deterministic placement of free ones).
	validPart := make(map[string]bool, len(partitions))
	sortedParts := make([]string, 0, len(partitions))
	for _, p := range partitions {
		if !validPart[p] {
			validPart[p] = true
			sortedParts = append(sortedParts, p)
		}
	}
	sort.Strings(sortedParts)

	// Step 1: keep sticky ownership for partitions whose previous owner is still
	// a member. Collect the rest (orphaned or brand-new) as "free".
	owned := make(map[string]string) // partition -> current (kept) owner
	for _, m := range sortedMembers {
		prevParts := append([]string(nil), prev[m]...)
		sort.Strings(prevParts)
		for _, p := range prevParts {
			if !validPart[p] {
				continue // partition no longer exists
			}
			if _, already := owned[p]; already {
				continue // defensive: a partition listed under two owners
			}
			owned[p] = m
			next[m] = append(next[m], p)
		}
	}

	free := make([]string, 0, len(sortedParts))
	for _, p := range sortedParts {
		if _, ok := owned[p]; !ok {
			free = append(free, p)
		}
	}

	// Targets: with N members and P partitions, ceil = P/N rounded up.
	// The first (P mod N) members (by load order) may hold ceil; the rest floor.
	n := len(sortedMembers)
	p := len(sortedParts)
	floor := p / n
	rem := p % n // number of members allowed to hold the higher (ceil) count

	// Step 2: assign free partitions to the least-loaded members first, never
	// exceeding ceil. ceil = floor + 1 when rem > 0, else floor.
	ceil := floor
	if rem > 0 {
		ceil = floor + 1
	}
	for _, part := range free {
		m := leastLoadedUnderCap(next, sortedMembers, ceil)
		next[m] = append(next[m], part)
	}

	// Step 3: rebalance overloaded members down to target. Some members may
	// still exceed ceil if a member that left dumped many partitions, or if
	// sticky retention left an old owner above ceil. Move the minimal number of
	// partitions from over-ceil members to under-floor members.
	rebalanceDown(next, sortedMembers, floor, ceil)

	// Stable, deterministic ordering of each member's partition list.
	for _, m := range sortedMembers {
		sort.Strings(next[m])
	}
	return next
}

// leastLoadedUnderCap returns the member with the fewest partitions that is
// still strictly below cap; ties broken by member ID. If all are at cap it
// returns the least-loaded member regardless (cap is a soft preference that the
// caller's totals guarantee is satisfiable in the normal path).
func leastLoadedUnderCap(a Assignment, members []string, cap int) string {
	best := ""
	bestLoad := -1
	for _, m := range members {
		l := len(a[m])
		if l >= cap {
			continue
		}
		if bestLoad == -1 || l < bestLoad {
			bestLoad = l
			best = m
		}
	}
	if best != "" {
		return best
	}
	// Fallback: every member is at/above cap — pick the globally least loaded.
	for _, m := range members {
		l := len(a[m])
		if bestLoad == -1 || l < bestLoad {
			bestLoad = l
			best = m
		}
	}
	return best
}

// rebalanceDown moves partitions from members above ceil to members below floor
// until max-min <= 1. It moves the minimal number of partitions: only the
// surplus above ceil from donors, only enough to bring receivers up to floor.
// Donors and receivers are processed in deterministic order.
func rebalanceDown(a Assignment, members []string, floor, ceil int) {
	for {
		// Find a donor strictly above ceil (surplus) and a receiver below floor.
		donor := ""
		for _, m := range members {
			if len(a[m]) > ceil {
				donor = m
				break
			}
		}
		if donor == "" {
			// No member above ceil. Now ensure no member is below floor by
			// pulling from members above floor (i.e. at ceil).
			receiver := ""
			for _, m := range members {
				if len(a[m]) < floor {
					receiver = m
					break
				}
			}
			if receiver == "" {
				return // balanced
			}
			// Donor is any member above floor.
			for _, m := range members {
				if len(a[m]) > floor {
					donor = m
					break
				}
			}
			if donor == "" {
				return // cannot improve (shouldn't happen with valid totals)
			}
			moveOne(a, donor, receiver)
			continue
		}
		// Donor above ceil: give to the least-loaded member (preferring below
		// floor, else below ceil).
		receiver := leastLoadedReceiver(a, members, ceil)
		if receiver == "" || receiver == donor {
			return
		}
		moveOne(a, donor, receiver)
	}
}

// leastLoadedReceiver picks the least-loaded member strictly below cap; ties by
// member ID order (members is pre-sorted).
func leastLoadedReceiver(a Assignment, members []string, cap int) string {
	best := ""
	bestLoad := -1
	for _, m := range members {
		l := len(a[m])
		if l >= cap {
			continue
		}
		if bestLoad == -1 || l < bestLoad {
			bestLoad = l
			best = m
		}
	}
	return best
}

// moveOne moves one partition (deterministically the lexicographically last,
// to keep the operation stable) from donor to receiver.
func moveOne(a Assignment, donor, receiver string) {
	parts := a[donor]
	if len(parts) == 0 {
		return
	}
	sort.Strings(parts)
	last := len(parts) - 1
	moved := parts[last]
	a[donor] = parts[:last]
	a[receiver] = append(a[receiver], moved)
}

// Moved returns the set of partitions whose owning consumer changed between
// prev and next. A partition counts as moved if its owner differs, if it was
// newly assigned (absent in prev), or if it was dropped (absent in next).
// The result is sorted for deterministic comparison.
func Moved(prev, next Assignment) []string {
	prevOwner := make(map[string]string)
	for c, parts := range prev {
		for _, p := range parts {
			prevOwner[p] = c
		}
	}
	nextOwner := make(map[string]string)
	for c, parts := range next {
		for _, p := range parts {
			nextOwner[p] = c
		}
	}

	movedSet := make(map[string]bool)
	for p, no := range nextOwner {
		if prevOwner[p] != no { // includes "" (newly assigned)
			movedSet[p] = true
		}
	}
	for p := range prevOwner {
		if _, stillThere := nextOwner[p]; !stillThere {
			movedSet[p] = true // dropped
		}
	}

	out := make([]string, 0, len(movedSet))
	for p := range movedSet {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Loads returns each member's partition count (used by tests and callers that
// want to assert the balance invariant).
func Loads(a Assignment) map[string]int {
	out := make(map[string]int, len(a))
	for c, parts := range a {
		out[c] = len(parts)
	}
	return out
}
