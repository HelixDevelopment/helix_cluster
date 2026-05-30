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
