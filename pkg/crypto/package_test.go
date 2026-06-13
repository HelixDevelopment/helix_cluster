package crypto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
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

// TestHash_KnownAnswer pins SHA-256 against published KAT vectors so that a
// deterministic-but-wrong digest algorithm (e.g. SHA-512 truncated, MD5, a
// fixed string) is detected — length-only checks would not catch this.
func TestHash_KnownAnswer(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// NIST FIPS 180-4 / RFC 6234 SHA-256 of "abc".
		{"abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		// SHA-256 of the empty string.
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
	}
	for _, c := range cases {
		if got := Hash([]byte(c.in)); got != c.want {
			t.Errorf("Hash(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestGenerateKey_KnownAnswer pins GenerateKey to the first 32 hex chars of the
// real SHA-256 of the seed, so a wrong-but-deterministic derivation is detected.
func TestGenerateKey_KnownAnswer(t *testing.T) {
	// First 32 hex chars of SHA-256("abc").
	const want = "ba7816bf8f01cfea414140de5dae2223"
	if got := GenerateKey("abc"); got != want {
		t.Errorf("GenerateKey(%q) = %s, want %s", "abc", got, want)
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
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	msg := []byte("hello helix")
	sig, err := Sign(kp.Private, msg)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	badMsg := []byte("tampered")
	err = Verify(kp.Public, badMsg, sig)
	if err == nil {
		t.Fatal("expected verification to fail for tampered message")
	}
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("expected ErrVerifyFailed, got %v", err)
	}
}

func TestVerifyBadKey(t *testing.T) {
	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	msg := []byte("hello helix")
	sig, err := Sign(kp1.Private, msg)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	err = Verify(kp2.Public, msg, sig)
	if err == nil {
		t.Fatal("expected verification to fail with wrong public key")
	}
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("expected ErrVerifyFailed, got %v", err)
	}
}

func TestSignInvalidKey(t *testing.T) {
	_, err := Sign([]byte("short"), []byte("msg"))
	if err == nil {
		t.Fatal("expected error for invalid private key size")
	}
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestVerifyInvalidKey(t *testing.T) {
	err := Verify([]byte("short"), []byte("msg"), "00")
	if err == nil {
		t.Fatal("expected error for invalid public key size")
	}
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
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

// TestEncryptDecryptKeySizes proves AES-128 (16-byte) and AES-192 (24-byte)
// keys are supported in addition to AES-256 (32-byte), with full sink-side
// recovery of the plaintext.
func TestEncryptDecryptKeySizes(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
	}{
		{"AES-128", []byte("0123456789abcdef")},                 // 16 bytes
		{"AES-192", []byte("0123456789abcdef01234567")},         // 24 bytes
		{"AES-256", []byte("0123456789abcdef0123456789abcdef")}, // 32 bytes
	}
	plaintext := []byte("secret helix data across key sizes")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cipherHex, err := Encrypt(c.key, plaintext)
			if err != nil {
				t.Fatalf("encrypt failed: %v", err)
			}
			decrypted, err := Decrypt(c.key, cipherHex)
			if err != nil {
				t.Fatalf("decrypt failed: %v", err)
			}
			if !bytes.Equal(decrypted, plaintext) {
				t.Fatalf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
			}
		})
	}
}

// TestEncryptInvalidKeyLength proves a key that is not a valid AES size is
// rejected rather than silently accepted.
func TestEncryptInvalidKeyLength(t *testing.T) {
	_, err := Encrypt([]byte("short-key"), []byte("data"))
	if err == nil {
		t.Fatal("expected error for invalid AES key length on Encrypt")
	}
	_, err = Decrypt([]byte("short-key"), "00")
	if err == nil {
		t.Fatal("expected error for invalid AES key length on Decrypt")
	}
}

// TestDecryptWrongKey proves the AES-GCM ciphertext is bound to the key:
// decrypting with a different valid key fails authentication.
func TestDecryptWrongKey(t *testing.T) {
	keyA := []byte("0123456789abcdef0123456789abcdef")
	keyB := []byte("fedcba9876543210fedcba9876543210")
	plaintext := []byte("secret helix data")

	cipherHex, err := Encrypt(keyA, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if _, err := Decrypt(keyB, cipherHex); err == nil {
		t.Fatal("expected decryption with wrong key to fail authentication")
	}
}

// TestDecryptShortCiphertext exercises the ErrInvalidCiphertext length branch:
// a valid-hex payload shorter than the GCM nonce must be rejected.
func TestDecryptShortCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	// 4 bytes of valid hex — far shorter than the 12-byte GCM nonce.
	short := hex.EncodeToString([]byte{0x01, 0x02, 0x03, 0x04})
	_, err := Decrypt(key, short)
	if err == nil {
		t.Fatal("expected error for ciphertext shorter than nonce")
	}
	if !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte("secret helix data")
	cipherHex, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// Tamper with the ciphertext
	last := cipherHex[len(cipherHex)-1]
	tampered := cipherHex[:len(cipherHex)-1] + string(last+1)

	if _, err := Decrypt(key, tampered); err == nil {
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

// TestDeriveKey_KnownAnswer pins PBKDF2-HMAC-SHA256 to a published vector so a
// silent change of algorithm, hash, or iteration handling is detected. The
// expected value is the standard PBKDF2-SHA256 test vector for
// ("password","salt",4096,32).
func TestDeriveKey_KnownAnswer(t *testing.T) {
	const want = "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"
	got := hex.EncodeToString(DeriveKey([]byte("password"), []byte("salt"), 4096, 32))
	if got != want {
		t.Fatalf("DeriveKey KAT mismatch: got %s, want %s", got, want)
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
