package gpuattest

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// HXC-1569: seeded matmul proof-of-GPU-work (PoVW).

// ErrProofBroken is returned by VerifyProof when a chain link does not match.
var ErrProofBroken = errors.New("gpuattest: proof chain link mismatch")

// xorshift64 is a tiny self-contained deterministic PRNG. It does NOT touch the
// math/rand global state or any clock, so a given seed always yields the same
// stream on every machine. Seed 0 is remapped to a non-zero constant because
// xorshift has a fixed point at 0.
type xorshift64 struct{ s uint64 }

func newXorshift(seed uint64) *xorshift64 {
	if seed == 0 {
		seed = 0x9E3779B97F4A7C15 // golden-ratio constant; any non-zero works
	}
	return &xorshift64{s: seed}
}

func (x *xorshift64) next() uint64 {
	x.s ^= x.s << 13
	x.s ^= x.s >> 7
	x.s ^= x.s << 17
	return x.s
}

// genMatrix fills an N*N matrix (row-major) with small non-negative ints in
// [0,1023] drawn from the PRNG, so C=A*B stays well within int64.
func genMatrix(x *xorshift64, n int) []int64 {
	m := make([]int64, n*n)
	for i := range m {
		m[i] = int64(x.next() & 0x3FF)
	}
	return m
}

func matmul(a, b []int64, n int) []int64 {
	c := make([]int64, n*n)
	for i := 0; i < n; i++ {
		for k := 0; k < n; k++ {
			aik := a[i*n+k]
			if aik == 0 {
				continue
			}
			for j := 0; j < n; j++ {
				c[i*n+j] += aik * b[k*n+j]
			}
		}
	}
	return c
}

// hashRow folds prev || row-of-C into the next chain hash. Row elements are
// encoded big-endian so the hash depends on every element AND its position.
func hashRow(prev [32]byte, c []int64, row, n int) [32]byte {
	h := sha256.New()
	h.Write(prev[:])
	var buf [8]byte
	for j := 0; j < n; j++ {
		binary.BigEndian.PutUint64(buf[:], uint64(c[row*n+j])) //gosec:disable G115 -- full-width int64->uint64 bit-pattern for hashing; no truncation
		h.Write(buf[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Proof is a deterministic proof of having computed C = A*B for the matrices
// derived from Seed at size N. Chain[i] = H(Chain[i-1] || row_i(C)), with
// Chain[-1] taken as the zero hash. Root == Chain[N-1].
type Proof struct {
	Seed  uint64
	N     int
	Chain [][32]byte
	Root  [32]byte
}

// GenerateProof derives A and B from seed, computes C=A*B, and folds a SHA-256
// chain over the rows of C. n must be >= 1.
func GenerateProof(seed uint64, n int) (Proof, error) {
	if n < 1 {
		return Proof{}, errors.New("gpuattest: matrix size must be >= 1")
	}
	x := newXorshift(seed)
	a := genMatrix(x, n)
	b := genMatrix(x, n)
	c := matmul(a, b, n)

	chain := make([][32]byte, n)
	var prev [32]byte // zero hash seeds the chain
	for i := 0; i < n; i++ {
		prev = hashRow(prev, c, i, n)
		chain[i] = prev
	}
	return Proof{Seed: seed, N: n, Chain: chain, Root: prev}, nil
}

// VerifyProof independently re-derives A,B,C from the proof's seed/size and
// recomputes the whole chain, comparing every link. It returns:
//   - (true, -1, nil)  when every link and the root match.
//   - (false, i, ErrProofBroken) when link i is the FIRST that disagrees with
//     the recomputed value (or, if all links agree but the claimed Root does
//     not, i == N to flag the root).
func VerifyProof(p Proof) (ok bool, firstBrokenLink int, err error) {
	if p.N < 1 || len(p.Chain) != p.N {
		return false, 0, ErrProofBroken
	}
	x := newXorshift(p.Seed)
	a := genMatrix(x, p.N)
	b := genMatrix(x, p.N)
	c := matmul(a, b, p.N)

	var prev [32]byte
	for i := 0; i < p.N; i++ {
		prev = hashRow(prev, c, i, p.N)
		if prev != p.Chain[i] {
			return false, i, ErrProofBroken
		}
	}
	if prev != p.Root {
		return false, p.N, ErrProofBroken
	}
	return true, -1, nil
}
