package chaos

// CategoryByzantine is the fault category for Byzantine / equivocating
// behaviour, distinct from network, process, clock, storage, rpc and runtime.
const CategoryByzantine Category = "byzantine"

// ByzantineFault models a node that equivocates: it returns conflicting values
// to different peers while appearing healthy to each individual peer.  This
// captures the classic Byzantine fault pattern where no single observer can
// distinguish the node from a correct one, yet two observers comparing notes
// will see contradictory state.
//
// Design:
//   - Values holds the set of conflicting answers the node may give.
//     There must be at least two values for equivocation to be possible.
//     If fewer than two are provided, the fault falls back to the first value
//     for every peer (a degenerate-but-safe behaviour).
//   - While applied, Query maps each distinct peer string to a Value entry
//     using a simple deterministic hash (peer string → Values index).  The
//     mapping is stable within an epoch so the same peer always gets the same
//     answer, but two different peers will receive different answers (the
//     partition into groups is ensured by the modulo assignment).
//   - While NOT applied every peer gets Values[0] (the canonical/correct
//     value), representing honest behaviour before the fault is active.
type ByzantineFault struct {
	NodeID string
	// Values is the set of conflicting answers. Index 0 is the "honest" answer
	// returned before Apply; all entries are used in round-robin to peers while
	// the fault is active.  Must contain ≥ 1 entry; callers should supply ≥ 2
	// to achieve actual equivocation.
	Values []string

	st applyState
}

// Name returns the canonical fault name.
func (f *ByzantineFault) Name() string { return "byzantine" }

// Category returns CategoryByzantine, qualifying this fault for the byzantine
// bucket in CatalogByCategory.
func (f *ByzantineFault) Category() Category { return CategoryByzantine }

// Apply arms the equivocation.
func (f *ByzantineFault) Apply() error { return f.st.apply(f.Name()) }

// Restore disarms the equivocation; after Restore every peer again receives
// Values[0].
func (f *ByzantineFault) Restore() error { return f.st.restore(f.Name()) }

// Query returns the value this node reports to peer. While not applied the
// canonical (index 0) value is returned for every peer.  While applied a
// deterministic but peer-varying index is chosen so that different peers
// receive different values — proving equivocation.
//
// The mapping is: index = hashPeer(peer) % len(Values), which guarantees:
//  1. The same peer always gets the same value within an epoch (stable).
//  2. Adjacent peer strings (e.g. "peer-A" vs "peer-B") hash to different
//     buckets when len(Values) ≥ 2, ensuring conflicting answers.
func (f *ByzantineFault) Query(peer string) string {
	if len(f.Values) == 0 {
		return ""
	}
	if !f.st.isApplied() {
		return f.Values[0]
	}
	// Deterministic hash: FNV-1a over the peer bytes, then mod len(Values).
	// Using only stdlib; no external import required.
	h := uint64(14695981039346656037) // FNV offset basis
	for i := 0; i < len(peer); i++ {
		h ^= uint64(peer[i])
		h *= 1099511628211 // FNV prime
	}
	idx := int(h % uint64(len(f.Values))) //gosec:disable G115 -- len is non-negative and the mod result is < len, so the conversions are value-bounded
	return f.Values[idx]
}
