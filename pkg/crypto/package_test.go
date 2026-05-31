package crypto

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"sync"
	"testing"
)

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

func TestGenerateKeyPair(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kp.Public) != ed25519.PublicKeySize {
		t.Errorf("expected public key size %d, got %d", ed25519.PublicKeySize, len(kp.Public))
	}
	if len(kp.Private) != ed25519.PrivateKeySize {
		t.Errorf("expected private key size %d, got %d", ed25519.PrivateKeySize, len(kp.Private))
	}
}

func TestSignVerify(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	msg := []byte("hello helix")

	sig, err := Sign(kp.Private, msg)
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}
	if sig == "" {
		t.Fatal("expected non-empty signature")
	}

	if err := Verify(kp.Public, msg, sig); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestVerifyBadSignature(t *testing.T) {
	kp, _ := GenerateKeyPair()
	msg := []byte("hello helix")
	sig, _ := Sign(kp.Private, msg)

	badMsg := []byte("tampered")
	if err := Verify(kp.Public, badMsg, sig); err == nil {
		t.Fatal("expected verification to fail for tampered message")
	}
}

func TestVerifyBadKey(t *testing.T) {
	kp1, _ := GenerateKeyPair()
	kp2, _ := GenerateKeyPair()
	msg := []byte("hello helix")
	sig, _ := Sign(kp1.Private, msg)

	if err := Verify(kp2.Public, msg, sig); err == nil {
		t.Fatal("expected verification to fail with wrong public key")
	}
}

func TestSignInvalidKey(t *testing.T) {
	_, err := Sign([]byte("short"), []byte("msg"))
	if err == nil {
		t.Fatal("expected error for invalid private key size")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef") // 32 bytes for AES-256
	plaintext := []byte("secret helix data")

	cipherHex, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if cipherHex == "" {
		t.Fatal("expected non-empty ciphertext")
	}

	decrypted, err := Decrypt(key, cipherHex)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted text does not match original: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDecryptEmptyPlaintext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	cipherHex, err := Encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	decrypted, err := Decrypt(key, cipherHex)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if len(decrypted) != 0 {
		t.Fatal("expected empty decrypted plaintext")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte("secret helix data")
	cipherHex, _ := Encrypt(key, plaintext)

	// Tamper with the ciphertext
	last := cipherHex[len(cipherHex)-1]
	tampered := cipherHex[:len(cipherHex)-1] + string(last+1)

	_, err := Decrypt(key, tampered)
	if err == nil {
		t.Fatal("expected error when decrypting tampered ciphertext")
	}
}

func TestDecryptInvalidHex(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	_, err := Decrypt(key, "not-hex!!!")
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestDeriveKey(t *testing.T) {
	password := []byte("helix-password")
	salt := []byte("fixed-salt")
	key1 := DeriveKey(password, salt, 100000, 32)
	key2 := DeriveKey(password, salt, 100000, 32)

	if len(key1) != 32 {
		t.Fatalf("expected key length 32, got %d", len(key1))
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("derived key must be deterministic for same inputs")
	}
}

func TestDeriveKeyDifferentInputs(t *testing.T) {
	key1 := DeriveKey([]byte("pass1"), []byte("salt"), 1000, 32)
	key2 := DeriveKey([]byte("pass2"), []byte("salt"), 1000, 32)
	if bytes.Equal(key1, key2) {
		t.Fatal("different passwords should produce different keys")
	}
}

// --- Mutation tests ---

func TestHash_Deterministic_Mutation(t *testing.T) {
	h1 := Hash([]byte("hello"))
	h2 := Hash([]byte("hello"))
	if h1 != h2 {
		t.Error("Hash must be deterministic for identical input")
	}
}

func TestHash_DifferentInput_Mutation(t *testing.T) {
	h1 := Hash([]byte("a"))
	h2 := Hash([]byte("b"))
	if h1 == h2 {
		t.Error("different inputs must produce different hashes")
	}
}

func TestGenerateKey_Length_Mutation(t *testing.T) {
	k := GenerateKey("any-seed")
	if len(k) != 32 {
		t.Errorf("GenerateKey must return exactly 32 bytes, got %d", len(k))
	}
	full := Hash([]byte("any-seed"))
	if k != full[:32] {
		t.Error("GenerateKey should be the first 32 chars of the full hash")
	}
}

func TestEncryptNonceUniqueness_Mutation(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte("helix")
	c1, _ := Encrypt(key, plaintext)
	c2, _ := Encrypt(key, plaintext)
	if c1 == c2 {
		t.Error("encrypting same plaintext twice must produce different ciphertexts (nonce uniqueness)")
	}
}

func TestEncryptDecryptConcurrent_Mutation(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			plaintext := []byte(strings.Repeat("x", idx+1))
			c, err := Encrypt(key, plaintext)
			if err != nil {
				t.Errorf("encrypt failed: %v", err)
				return
			}
			d, err := Decrypt(key, c)
			if err != nil {
				t.Errorf("decrypt failed: %v", err)
				return
			}
			if !bytes.Equal(d, plaintext) {
				t.Error("concurrent encrypt/decrypt mismatch")
			}
		}(i)
	}
	wg.Wait()
}

func TestDeriveKeyLength_Mutation(t *testing.T) {
	key := DeriveKey([]byte("p"), []byte("s"), 1000, 16)
	if len(key) != 16 {
		t.Fatalf("expected key length 16, got %d", len(key))
	}
}
