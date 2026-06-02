// Package preempt implements value-multiplier (Gepetto-model) preemption for
// GPU scheduling. It is pure-Go and fully deterministic: it operates on
// in-memory Node/Instance structures and performs no I/O, no clock reads, and
// no host/network access.
//
// # Model
//
// A Node has a finite integer Capacity occupied by running Instances. Each
// Instance carries a Value and a Multiplier; its effective priority is
//
//	EffectivePriority = Value * Multiplier
//
// When an incoming workload wants to land on a node that does not have free
// capacity, Preempt decides whether to evict the lowest-effective-priority
// running instances to make room. The core rule is STRICT:
//
//	A running instance is a preemption candidate ONLY if the incoming
//	workload's effective priority STRICTLY EXCEEDS the candidate's effective
//	priority. Equal effective priority does NOT preempt.
//
// # Protection guard
//
// An Instance marked SoleGlobal (the last-of-its-kind for some scarce class)
// is protected: it is NEVER selected as a victim, even by a strictly
// higher-priority incoming workload. This prevents the scheduler from
// destroying the only surviving instance of a critical class.
package preempt

import "sort"

// Instance is a running unit occupying capacity on a Node.
type Instance struct {
	// ID uniquely identifies the instance.
	ID string
	// Value is the base worth of the instance.
	Value int64
	// Multiplier scales Value into the effective priority.
	Multiplier int64
	// Size is how much node Capacity this instance occupies (>= 1).
	Size int64
	// SoleGlobal marks this instance as the protected last-of-its-kind; it is
	// never preempted regardless of incoming priority.
	SoleGlobal bool
}

// EffectivePriority returns Value * Multiplier, the Gepetto effective priority.
func (in Instance) EffectivePriority() int64 {
	return in.Value * in.Multiplier
}

// Node has a finite Capacity occupied by Running instances.
type Node struct {
	// Capacity is the total schedulable capacity of the node (>= 0).
	Capacity int64
	// Running are the instances currently occupying the node.
	Running []Instance
}

// Used returns the total Size occupied by Running instances.
func (n Node) Used() int64 {
	var used int64
	for _, in := range n.Running {
		used += in.Size
	}
	return used
}

// Free returns the unoccupied capacity (Capacity - Used), never negative.
func (n Node) Free() int64 {
	free := n.Capacity - n.Used()
	if free < 0 {
		return 0
	}
	return free
}

// Preempt decides whether the incoming workload can be placed on node, possibly
// by evicting victims to make room.
//
// Behaviour:
//
//   - The lowest-effective-priority eligible running instances are considered
//     for eviction, lowest first. An instance is ELIGIBLE only if it is not
//     SoleGlobal AND incoming.EffectivePriority() STRICTLY EXCEEDS the
//     candidate's EffectivePriority(). Protected (SoleGlobal) and
//     equal-or-higher-priority instances are never evicted.
//
//   - Victims are accumulated (lowest priority first) only as long as the
//     currently freed capacity is still short of incoming.Size; the loop stops
//     before appending any victim once there is already enough room. Therefore
//     when the node already has enough Free capacity the loop appends nothing
//     and the incoming is placed with NO victims (victims is empty, placed is
//     true).
//
//   - If, after considering all eligible instances, there is still not enough
//     room, the incoming is NOT placed: placed is false and victims is empty (no
//     eviction is performed for a placement that cannot succeed).
//
// The returned victims slice is a fresh copy; node is not mutated.
func Preempt(node Node, incoming Instance) (victims []Instance, placed bool) {
	need := incoming.Size

	incPri := incoming.EffectivePriority()

	// Build the eligible candidate list: not protected, and strictly lower
	// effective priority than the incoming workload.
	type cand struct {
		in  Instance
		idx int // original index, for a stable deterministic tie-break
	}
	var cands []cand
	for i, in := range node.Running {
		if in.SoleGlobal {
			// Protection guard: last-of-its-kind is never a victim.
			continue
		}
		if incPri > in.EffectivePriority() {
			cands = append(cands, cand{in: in, idx: i})
		}
	}

	// Order candidates lowest effective priority first; deterministic tie-break
	// on original index so equal-priority candidates evict in a stable order.
	sort.SliceStable(cands, func(a, b int) bool {
		pa := cands[a].in.EffectivePriority()
		pb := cands[b].in.EffectivePriority()
		if pa != pb {
			return pa < pb
		}
		return cands[a].idx < cands[b].idx
	})

	// Accumulate victims (lowest first) until we would have enough room.
	freed := node.Free()
	var chosen []Instance
	for _, c := range cands {
		if freed >= need {
			break
		}
		chosen = append(chosen, c.in)
		freed += c.in.Size
	}

	if freed < need {
		// Cannot make room even after evicting every eligible candidate.
		// Do not evict anything for a placement that cannot succeed.
		return nil, false
	}

	return chosen, true
}
