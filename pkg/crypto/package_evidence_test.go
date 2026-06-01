package crypto

import (
	"bytes"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/evidence"
)

// TestEncryptDecrypt_TokenRoundTrip_Evidence is the §7.1-evidence proof that the
// AES-GCM Encrypt/Decrypt pair performs a REAL, recoverable round-trip. A unique
// per-run token is embedded into the plaintext, encrypted, and then recovered by
// Decrypt. The recovered plaintext must contain the exact token (positive sink
// evidence), the ciphertext must differ from the plaintext (state delta), and
// the ciphertext must NOT leak the token. A no-op or broken cipher (e.g. one
// that returns the plaintext or a constant) cannot satisfy all three.
func TestEncryptDecrypt_TokenRoundTrip_Evidence(t *testing.T) {
	e := evidence.NewT(t, "crypto-aesgcm-token-roundtrip", t.TempDir())
	token := e.Token()
	key := []byte("0123456789abcdef0123456789abcdef") // AES-256

	plaintext := []byte("helix-secret-payload:" + token)

	cipherHex, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// §7.1 #2 DELTA: ciphertext must differ from plaintext (no echo/no-op).
	e.MustDelta(t, "ciphertext-differs-from-plaintext", string(plaintext), cipherHex)

	// Negative confinement: the unique token must NOT appear in the ciphertext.
	if bytes.Contains([]byte(cipherHex), []byte(token)) {
		t.Fatalf("ciphertext leaks the plaintext token %q — encryption is not real", token)
	}

	recovered, err := Decrypt(key, cipherHex)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	// §7.1 #3 POSITIVE: the per-run token is recovered through the round-trip.
	e.MustPositive(t, "decrypted-contains-token", string(recovered), token)

	if !bytes.Equal(recovered, plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", recovered, plaintext)
	}

	// Capture sink-side artifacts (ciphertext + recovered) for forensic review.
	if _, err := e.Artifact("ciphertext.hex", []byte(cipherHex)); err != nil {
		t.Fatalf("artifact ciphertext: %v", err)
	}
	if _, err := e.Artifact("recovered.txt", recovered); err != nil {
		t.Fatalf("artifact recovered: %v", err)
	}
}
