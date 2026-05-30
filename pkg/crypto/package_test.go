package crypto

import "testing"

func TestHash(t *testing.T) {
	h := Hash([]byte("hello"))
	if len(h) != 64 {
		t.Errorf("expected sha256 hex length 64, got %d", len(h))
	}
}

func TestGenerateKey(t *testing.T) {
	k := GenerateKey("seed")
	if len(k) != 32 {
		t.Errorf("expected key length 32, got %d", len(k))
	}
}

// --- Mutation tests ---

func TestHash_Deterministic_Mutation(t *testing.T) {
	// Mutation: Hash uses non-deterministic or different algorithm → same input yields different output
	h1 := Hash([]byte("hello"))
	h2 := Hash([]byte("hello"))
	if h1 != h2 {
		t.Error("Hash must be deterministic for identical input")
	}
}

func TestHash_DifferentInput_Mutation(t *testing.T) {
	// Mutation: Hash returns constant regardless of input → different inputs collide
	h1 := Hash([]byte("a"))
	h2 := Hash([]byte("b"))
	if h1 == h2 {
		t.Error("different inputs must produce different hashes")
	}
}

func TestGenerateKey_Length_Mutation(t *testing.T) {
	// Mutation: GenerateKey truncates to wrong length or returns full hash
	k := GenerateKey("any-seed")
	if len(k) != 32 {
		t.Errorf("GenerateKey must return exactly 32 bytes, got %d", len(k))
	}
	// Also ensure it's a prefix of the full hash
	full := Hash([]byte("any-seed"))
	if k != full[:32] {
		t.Error("GenerateKey should be the first 32 chars of the full hash")
	}
}
