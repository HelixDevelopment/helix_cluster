package antientropy

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// MerkleTree is a hierarchical (binary) Merkle summary over a replica's sorted
// key/value set. Unlike a flat root-only hash, the internal nodes let MerkleDiff
// PRUNE whole sub-trees whose roots match, descending only where roots differ.
//
// Leaves are sorted by key so that two replicas with the same key set partition
// their key space identically; this makes per-node hashes comparable range-for-
// range and is what lets matching ranges be skipped without inspecting their keys.
type MerkleTree struct {
	root *node
	// keys is the sorted key slice this tree was built from (leaf order).
	keys []string
}

type node struct {
	hash  [32]byte
	left  *node
	right *node
	// leaf is set only on leaf nodes; it carries the single key this leaf covers.
	leaf string
	// isLeaf distinguishes a leaf (covers one key) from an internal node.
	isLeaf bool
}

// leafDigest hashes one key/value pair with length-prefixing so ("ab","c") and
// ("a","bc") cannot collide.
func leafDigest(key, value string) [32]byte {
	h := sha256.New()
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(key)))
	h.Write(n[:])
	h.Write([]byte(key))
	binary.BigEndian.PutUint64(n[:], uint64(len(value)))
	h.Write(n[:])
	h.Write([]byte(value))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// internalDigest combines two child hashes into a parent hash.
func internalDigest(l, r [32]byte) [32]byte {
	h := sha256.New()
	h.Write(l[:])
	h.Write(r[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// NewMerkleTree builds a balanced binary Merkle tree over kv. Keys are sorted so
// the leaf order — and therefore the tree shape — is deterministic and identical
// for any two trees sharing the same key set.
func NewMerkleTree(kv map[string]string) *MerkleTree {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t := &MerkleTree{keys: keys}
	if len(keys) == 0 {
		return t
	}
	leaves := make([]*node, len(keys))
	for i, k := range keys {
		leaves[i] = &node{hash: leafDigest(k, kv[k]), leaf: k, isLeaf: true}
	}
	t.root = buildLevel(leaves)
	return t
}

// buildLevel folds a slice of nodes pairwise into parents until one root remains.
// An odd node at a level is carried up unchanged (it becomes the left child of a
// parent that has no right sibling).
func buildLevel(nodes []*node) *node {
	for len(nodes) > 1 {
		next := make([]*node, 0, (len(nodes)+1)/2)
		for i := 0; i < len(nodes); i += 2 {
			if i+1 < len(nodes) {
				l, r := nodes[i], nodes[i+1]
				next = append(next, &node{hash: internalDigest(l.hash, r.hash), left: l, right: r})
			} else {
				next = append(next, nodes[i])
			}
		}
		nodes = next
	}
	return nodes[0]
}

// Root returns the top-level Merkle root hash (empty tree -> zero hash).
func (t *MerkleTree) Root() [32]byte {
	if t.root == nil {
		return [32]byte{}
	}
	return t.root.hash
}

// Equal reports whether two trees summarize identical key/value sets by comparing
// only their constant-size root hashes — the cheap anti-entropy fast path.
func (t *MerkleTree) Equal(other *MerkleTree) bool {
	return t.Root() == other.Root()
}

// MerkleDiff returns the keys whose values differ between a and b, including keys
// present in exactly one tree. Identical replicas yield an empty (nil) result.
//
// When the two trees share the same key set (same shape), divergence is found by
// descending the trees in lockstep and PRUNING any sub-tree pair whose hashes are
// equal — those ranges are provably identical and are never scanned key-by-key.
// When the key sets differ (shapes differ), it falls back to a leaf-set
// comparison, which still reports exactly the differing/missing keys.
//
// The returned slice is sorted for determinism.
func MerkleDiff(a, b *MerkleTree) []string {
	if a.Equal(b) {
		return nil
	}
	diff := make(map[string]struct{})
	if sameKeys(a.keys, b.keys) && a.root != nil && b.root != nil {
		descend(a.root, b.root, diff)
	} else {
		leafSetDiff(a, b, diff)
	}
	out := make([]string, 0, len(diff))
	for k := range diff {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diffStats counts how many node pairs the structural descent visited and how
// many sub-trees it pruned. It lets tests prove pruning actually occurred (the
// number of visited nodes is far below the leaf count for large matching trees).
type diffStats struct {
	visited int // node pairs entered by descend
	pruned  int // node pairs short-circuited because their hashes matched
}

// MerkleDiffInstrumented behaves like MerkleDiff but also returns pruning stats.
// It is exported for tests/operators to verify the sub-tree skip is real.
func MerkleDiffInstrumented(a, b *MerkleTree) ([]string, diffStats) {
	var st diffStats
	if a.Equal(b) {
		if a.root != nil && b.root != nil {
			st.visited = 1
			st.pruned = 1
		}
		return nil, st
	}
	diff := make(map[string]struct{})
	if sameKeys(a.keys, b.keys) && a.root != nil && b.root != nil {
		descendStats(a.root, b.root, diff, &st)
	} else {
		leafSetDiff(a, b, diff)
	}
	out := make([]string, 0, len(diff))
	for k := range diff {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, st
}

// descend walks two structurally-identical sub-trees. If the two node hashes are
// equal the entire sub-tree is pruned (returns immediately) — this is the
// sub-tree skip that avoids a full scan. At leaves, an unequal hash means that
// single key's value diverged.
func descend(x, y *node, diff map[string]struct{}) {
	var st diffStats
	descendStats(x, y, diff, &st)
}

// descendStats is descend with instrumentation threaded through.
func descendStats(x, y *node, diff map[string]struct{}, st *diffStats) {
	st.visited++
	if x.hash == y.hash {
		st.pruned++
		return // matching sub-tree: prune, do not scan its keys
	}
	if x.isLeaf {
		// Same key set guarantees y is the same leaf key; hashes differ -> value diverged.
		diff[x.leaf] = struct{}{}
		return
	}
	descendChildStats(x.left, y.left, diff, st)
	descendChildStats(x.right, y.right, diff, st)
}

// descendChildStats handles a possibly-nil child slot with instrumentation.
func descendChildStats(x, y *node, diff map[string]struct{}, st *diffStats) {
	switch {
	case x == nil && y == nil:
		return
	case x == nil:
		collectLeaves(y, diff)
	case y == nil:
		collectLeaves(x, diff)
	default:
		descendStats(x, y, diff, st)
	}
}

// collectLeaves adds every key under n to diff (used when one side lacks a child).
func collectLeaves(n *node, diff map[string]struct{}) {
	if n == nil {
		return
	}
	if n.isLeaf {
		diff[n.leaf] = struct{}{}
		return
	}
	collectLeaves(n.left, diff)
	collectLeaves(n.right, diff)
}

// sameKeys reports whether two sorted key slices are identical.
func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// leafSetDiff compares the per-key leaf hashes directly; used when the two trees
// have different key sets so the structural descent does not apply.
func leafSetDiff(a, b *MerkleTree, diff map[string]struct{}) {
	la := a.leafHashes()
	lb := b.leafHashes()
	for k, h := range la {
		if oh, ok := lb[k]; !ok || oh != h {
			diff[k] = struct{}{}
		}
	}
	for k, h := range lb {
		if oh, ok := la[k]; !ok || oh != h {
			diff[k] = struct{}{}
		}
	}
}

// leafHashes returns key -> leaf hash for every leaf in the tree.
func (t *MerkleTree) leafHashes() map[string][32]byte {
	out := make(map[string][32]byte, len(t.keys))
	if t.root != nil {
		walkLeaves(t.root, out)
	}
	return out
}

func walkLeaves(n *node, out map[string][32]byte) {
	if n == nil {
		return
	}
	if n.isLeaf {
		out[n.leaf] = n.hash
		return
	}
	walkLeaves(n.left, out)
	walkLeaves(n.right, out)
}
