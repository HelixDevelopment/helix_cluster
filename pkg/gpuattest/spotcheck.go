package gpuattest

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// HXC-1570: O(1) spot-check verification via a Merkle tree over words.

// ErrSpotCheckFailed is wrapped by SpotCheck with the offending index.
var ErrSpotCheckFailed = errors.New("gpuattest: spot-check word mismatch")

// Domain separation tags keep a leaf hash from ever colliding with an internal
// node hash (second-preimage protection).
var (
	leafTag = []byte{0x00}
	nodeTag = []byte{0x01}
)

func leafHash(word []byte) [32]byte {
	h := sha256.New()
	h.Write(leafTag)
	h.Write(word)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func nodeHash(l, r [32]byte) [32]byte {
	h := sha256.New()
	h.Write(nodeTag)
	h.Write(l[:])
	h.Write(r[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// WordResponse is a PoVW-style response made of many words, each committed under
// a Merkle root. The verifier holds only Root (32 bytes); to check word i it is
// given the word plus a logarithmic inclusion proof — never the whole set.
type WordResponse struct {
	words  [][]byte
	leaves [][32]byte
	// levels[0] == leaves; each higher level halves (odd nodes duplicated).
	levels [][][32]byte
	Root   [32]byte
}

// NewWordResponse commits to the given words and builds the Merkle tree. The
// words slice is copied defensively so later external mutation cannot silently
// change committed state.
func NewWordResponse(words [][]byte) *WordResponse {
	if len(words) == 0 {
		return &WordResponse{Root: leafHash(nil)}
	}
	cp := make([][]byte, len(words))
	leaves := make([][32]byte, len(words))
	for i, w := range words {
		b := make([]byte, len(w))
		copy(b, w)
		cp[i] = b
		leaves[i] = leafHash(b)
	}

	levels := [][][32]byte{leaves}
	cur := leaves
	for len(cur) > 1 {
		next := make([][32]byte, 0, (len(cur)+1)/2)
		for i := 0; i < len(cur); i += 2 {
			l := cur[i]
			r := l // duplicate last node when the level is odd
			if i+1 < len(cur) {
				r = cur[i+1]
			}
			next = append(next, nodeHash(l, r))
		}
		levels = append(levels, next)
		cur = next
	}
	return &WordResponse{words: cp, leaves: leaves, levels: levels, Root: cur[0]}
}

// Len reports how many words were committed.
func (w *WordResponse) Len() int { return len(w.words) }

// Word returns a copy of word i (for a prover/transport layer). The verifier
// does not need the full set.
func (w *WordResponse) Word(i int) ([]byte, bool) {
	if i < 0 || i >= len(w.words) {
		return nil, false
	}
	b := make([]byte, len(w.words[i]))
	copy(b, w.words[i])
	return b, true
}

// merkleProof returns the sibling-hash path from leaf i up to the root.
func (w *WordResponse) merkleProof(i int) [][32]byte {
	var path [][32]byte
	idx := i
	for lvl := 0; lvl < len(w.levels)-1; lvl++ {
		cur := w.levels[lvl]
		sib := idx ^ 1 // sibling index
		if sib >= len(cur) {
			sib = idx // odd node duplicated itself
		}
		path = append(path, cur[sib])
		idx /= 2
	}
	return path
}

// verifyInclusion recomputes the root from a word + its proof path and compares
// to root. This is O(log N) per index — it never touches the other words.
func verifyInclusion(root [32]byte, i, n int, word []byte, path [][32]byte) bool {
	h := leafHash(word)
	idx := i
	width := n
	p := 0
	for width > 1 {
		if p >= len(path) {
			return false
		}
		sib := path[p]
		if idx%2 == 0 {
			h = nodeHash(h, sib)
		} else {
			h = nodeHash(sib, h)
		}
		idx /= 2
		width = (width + 1) / 2
		p++
	}
	return h == root && p == len(path)
}

// OpenWord is what a prover transmits for ONE spot-checked index: the claimed
// word plus the Merkle inclusion proof (sibling path) for that leaf position.
// The verifier holds only the trusted root and recomputes inclusion in
// O(log N); it never receives or touches the other N-1 words.
type OpenWord struct {
	Index int
	Word  []byte
	Proof [][32]byte
}

// Open produces the OpenWord a prover would send for index i, drawn from THIS
// (committed) response's genuine tree. A dishonest prover may later substitute
// OpenWord.Word with a tampered value; the proof path still comes from the
// genuine tree, so substitution at index i only ever fails the check at i.
func (w *WordResponse) Open(i int) (OpenWord, bool) {
	word, ok := w.Word(i)
	if !ok {
		return OpenWord{}, false
	}
	return OpenWord{Index: i, Word: word, Proof: w.merkleProof(i)}, true
}

// SpotCheck verifies ONLY the supplied opened words against the trusted root,
// using each word's Merkle inclusion proof. Total work is O(k log N),
// independent of the other N-k words. It returns nil if every opened word is
// genuine; on the first failure it returns an error naming that exact index
// (wrapping ErrSpotCheckFailed). n is the committed word count (tree width).
//
// A check at index i fails precisely when that index's transmitted word no
// longer hashes — through its genuine proof path — into the trusted root, i.e.
// exactly when the word at i was tampered. Indices whose words are untouched
// always pass, regardless of tampering elsewhere.
func SpotCheck(trustedRoot [32]byte, n int, opened []OpenWord) error {
	for _, ow := range opened {
		if ow.Index < 0 || ow.Index >= n {
			return fmt.Errorf("%w: index %d out of range [0,%d)", ErrSpotCheckFailed, ow.Index, n)
		}
		if !verifyInclusion(trustedRoot, ow.Index, n, ow.Word, ow.Proof) {
			return fmt.Errorf("%w: index %d", ErrSpotCheckFailed, ow.Index)
		}
	}
	return nil
}
