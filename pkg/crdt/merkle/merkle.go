// Package merkle provides a merkle-tree summary over a key/value set to drive
// anti-entropy between CRDT replicas. Two replicas compare root hashes; if they
// differ, Diff descends to enumerate exactly the keys whose values disagree (or
// are missing on one side) so only those keys need to be exchanged.
//
// The construction is deterministic and order-independent: a tree built from
// the same key/value set always yields the same root, regardless of insertion
// order. It is stdlib-only (crypto/sha256, encoding/binary, sort).
package merkle

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// Tree is an immutable merkle summary of a key/value set. Build it with New.
type Tree struct {
	// leaves maps key -> per-key leaf hash (hash of key||value).
	leaves map[string][32]byte
	root   [32]byte
}

// leafHash hashes a single key/value pair. Lengths are prefixed so that
// ("ab","c") and ("a","bc") cannot collide.
func leafHash(key, value string) [32]byte {
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

// New builds a Tree over the given key/value map.
func New(kv map[string]string) *Tree {
	t := &Tree{leaves: make(map[string][32]byte, len(kv))}
	for k, v := range kv {
		t.leaves[k] = leafHash(k, v)
	}
	t.root = computeRoot(t.leaves)
	return t
}

// computeRoot folds the per-key leaf hashes into a single root in a way that is
// independent of map iteration order: keys are sorted, then leaf hashes are
// concatenated and hashed. An empty set hashes to the zero-length digest.
func computeRoot(leaves map[string][32]byte) [32]byte {
	keys := make([]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		lh := leaves[k]
		h.Write(lh[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Root returns the merkle root hash.
func (t *Tree) Root() [32]byte {
	return t.root
}

// Equal reports whether two trees summarize identical key/value sets, comparing
// only the constant-size root hash (the cheap anti-entropy fast path).
func (t *Tree) Equal(other *Tree) bool {
	return t.root == other.root
}

// Diff returns the keys whose values differ between t and other, including keys
// present in exactly one of the two trees. When roots are equal the slice is
// empty. The result is sorted for determinism.
//
// This is the "descend on root mismatch" step of anti-entropy: only the
// returned keys need their values shipped to reconcile the two replicas.
func (t *Tree) Diff(other *Tree) []string {
	if t.Equal(other) {
		return nil
	}
	diff := make(map[string]struct{})
	for k, lh := range t.leaves {
		if oh, ok := other.leaves[k]; !ok || oh != lh {
			diff[k] = struct{}{}
		}
	}
	for k, oh := range other.leaves {
		if lh, ok := t.leaves[k]; !ok || lh != oh {
			diff[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(diff))
	for k := range diff {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
