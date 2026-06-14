package crypto

// Adversarial sink-side probes for pkg/crypto.
//
// pkg/crypto is a thin wrapper over vetted stdlib primitives (crypto/ed25519,
// crypto/aes + crypto/cipher GCM, golang.org/x/crypto/pbkdf2). The classic
// crypto bug classes — non-constant-time MAC/signature compare, fail-open
// verify, AEAD that returns garbage on tamper, nonce reuse — would surface at
// the boundary between this package and the stdlib it delegates to. These tests
// assert the actual ACCEPT/REJECT verdict at the sink, with real tampered /
// empty / truncated / zero inputs. No "did not panic" assertions.
//
// All tests here PASS on unmodified production code. Each is paired below with a
// documented mutation that flips production behavior and the verdict it makes
// the test deliver — proving the test bites, not bluffs.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) NON-CONSTANT-TIME / TAMPER — signature verification
// ---------------------------------------------------------------------------

// TestAdv_Verify_EmptySignature_Rejected probes the FAIL-OPEN class directly:
// an empty signature hex ("") decodes to a zero-length []byte. Verify passes
// the public-key length check, then hands a zero-length sig to ed25519.Verify.
// A fail-open implementation (or one that returned nil before reaching Verify)
// would ACCEPT. The contract ("signature verification failed") demands REJECT.
//
// Mutation proof (production REJECTS, mutant ACCEPTS):
//
//	In Verify, replace the verification guard with an early success:
//	    if len(sig) == 0 { return nil }   // fail-open on empty sig
//	-> this test then FAILS ("empty signature accepted"), proving it bites.
func TestAdv_Verify_EmptySignature_Rejected(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	msg := []byte("authenticate-me")

	err = Verify(kp.Public, msg, "")
	if err == nil {
		t.Fatal("SINK BUG: Verify accepted an EMPTY signature — fail-open")
	}
}

// TestAdv_Verify_AllZeroSignature_Rejected feeds a syntactically valid,
// correctly-sized (64-byte) but ALL-ZERO signature. A broken compare that
// treats the zero value as a match (the "zero-MAC matches" fail-open) would
// ACCEPT. ed25519.Verify must REJECT it.
//
// Mutation proof: same fail-open guard as above but keyed on an all-zero sig
// -> test FAILS, proving the all-zero vector is genuinely rejected today.
func TestAdv_Verify_AllZeroSignature_Rejected(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	zero := make([]byte, ed25519.SignatureSize) // 64 zero bytes
	if err := Verify(kp.Public, []byte("msg"), hex.EncodeToString(zero)); err == nil {
		t.Fatal("SINK BUG: Verify accepted an all-zero 64-byte signature")
	}
}

// TestAdv_Verify_TruncatedSignature_Rejected takes a VALID signature, lops off
// its last byte (wrong length, 63 bytes), and confirms it is rejected. A
// length-confusion bypass (padding/truncating to "fit") would accept. Because
// Verify does not itself length-check the decoded sig, this asserts that the
// underlying ed25519.Verify rejects wrong-length signatures — pinning the
// guarantee that the missing explicit check is harmless.
//
// Mutation proof: if Verify zero-padded the sig up to SignatureSize before
// calling ed25519.Verify, a benign truncation could change semantics; this
// test pins that no such padding/acceptance happens.
func TestAdv_Verify_TruncatedSignature_Rejected(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	msg := []byte("hello helix")
	sigHex, err := Sign(kp.Private, msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	raw, err := hex.DecodeString(sigHex)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	truncated := hex.EncodeToString(raw[:len(raw)-1]) // 63 bytes
	if err := Verify(kp.Public, msg, truncated); err == nil {
		t.Fatal("SINK BUG: Verify accepted a truncated (63-byte) signature")
	}
}

// TestAdv_Verify_SingleBitFlip_Rejected flips one bit of a VALID signature and
// confirms rejection — the canonical tamper test against signatures. ed25519
// verification is constant-time internally; we assert the accept/reject verdict.
//
// Mutation proof: replace the `if !ed25519.Verify(...)` body with `return nil`
// -> this test FAILS ("accepted a bit-flipped signature").
func TestAdv_Verify_SingleBitFlip_Rejected(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	msg := []byte("integrity-protected")
	sigHex, err := Sign(kp.Private, msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	raw, err := hex.DecodeString(sigHex)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[0] ^= 0x01 // flip a single bit
	if err := Verify(kp.Public, msg, hex.EncodeToString(raw)); err == nil {
		t.Fatal("SINK BUG: Verify accepted a single-bit-flipped signature")
	}
}

// TestAdv_Verify_ValidStillAccepts is the positive control: after all the
// rejection probes, a genuine signature must still verify. Guards against an
// over-eager fix that rejects everything (deny-all is not security).
func TestAdv_Verify_ValidStillAccepts(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	msg := []byte("legit message")
	sigHex, err := Sign(kp.Private, msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(kp.Public, msg, sigHex); err != nil {
		t.Fatalf("SINK BUG: valid signature rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// (b) FAIL-OPEN — AEAD decrypt with empty / nil / zero inputs
// ---------------------------------------------------------------------------

// TestAdv_Decrypt_EmptyString_Rejected feeds the empty hex string "". It
// decodes to a zero-length ciphertext, which is shorter than the 12-byte GCM
// nonce and MUST hit the ErrInvalidCiphertext length guard — never returning a
// (possibly empty, "successful") plaintext.
//
// Mutation proof (production REJECTS via ErrInvalidCiphertext, mutant ACCEPTS):
//
//	Change the guard `if len(ciphertext) < gcm.NonceSize()` to
//	`if len(ciphertext) < 0` (never true) -> Open is handed a malformed slice;
//	at minimum the ErrInvalidCiphertext branch is no longer taken and this
//	test's errors.Is check FAILS.
func TestAdv_Decrypt_EmptyString_Rejected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	out, err := Decrypt(key, "")
	if err == nil {
		t.Fatalf("SINK BUG: Decrypt(\"\") returned no error, plaintext=%q", out)
	}
	if !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext for empty ciphertext, got %v", err)
	}
}

// TestAdv_Decrypt_NonceOnly_Rejected supplies exactly a nonce-sized payload and
// nothing else: len == NonceSize, so the length guard passes, but GCM.Open is
// then given an EMPTY ciphertext+tag and must fail authentication (no 16-byte
// tag present). A fail-open AEAD would return empty plaintext with nil error.
func TestAdv_Decrypt_NonceOnly_Rejected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	// 12 bytes == GCM nonce size, with no tag/ciphertext following.
	payload := hex.EncodeToString(make([]byte, 12))
	if out, err := Decrypt(key, payload); err == nil {
		t.Fatalf("SINK BUG: Decrypt accepted nonce-only payload, plaintext=%q", out)
	}
}

// TestAdv_Decrypt_TagTampered_Rejected encrypts, then flips a bit in the LAST
// byte (the GCM auth tag region) of the raw ciphertext and confirms Open
// rejects it. This targets the authentication tag specifically rather than the
// body, proving the "T" in AEAD is enforced.
func TestAdv_Decrypt_TagTampered_Rejected(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	cipherHex, err := Encrypt(key, []byte("authenticated payload"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := hex.DecodeString(cipherHex)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0x80 // perturb the auth tag
	if out, err := Decrypt(key, hex.EncodeToString(raw)); err == nil {
		t.Fatalf("SINK BUG: Decrypt accepted tag-tampered ciphertext, plaintext=%q", out)
	}
}

// TestAdv_Encrypt_NilAndEmptyKey_Rejected asserts that a nil key and a
// zero-length key are rejected by Encrypt (aes.NewCipher rejects invalid key
// sizes). An empty key silently accepted would be a catastrophic fail-open
// ("encryption" under no key).
func TestAdv_Encrypt_NilAndEmptyKey_Rejected(t *testing.T) {
	if _, err := Encrypt(nil, []byte("data")); err == nil {
		t.Fatal("SINK BUG: Encrypt accepted a nil key")
	}
	if _, err := Encrypt([]byte{}, []byte("data")); err == nil {
		t.Fatal("SINK BUG: Encrypt accepted an empty key")
	}
}

// ---------------------------------------------------------------------------
// (c) ROUND-TRIP INTEGRITY across awkward inputs
// ---------------------------------------------------------------------------

// TestAdv_RoundTrip_BinaryAndLong exercises the AEAD round-trip on inputs the
// existing suite does not: a payload containing every byte value 0x00..0xFF
// (binary safety / no premature NUL termination) and a large payload. Both must
// recover BYTE-IDENTICAL. Asserts sink-side equality, not "no error".
func TestAdv_RoundTrip_BinaryAndLong(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	large := bytes.Repeat([]byte("helix-"), 5000) // ~30 KiB

	for name, pt := range map[string][]byte{
		"all-byte-values": allBytes,
		"large-30KiB":     large,
		"single-nul":      {0x00},
	} {
		t.Run(name, func(t *testing.T) {
			ct, err := Encrypt(key, pt)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			got, err := Decrypt(key, ct)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if !bytes.Equal(got, pt) {
				t.Fatalf("round-trip corrupted %s: len got=%d want=%d", name, len(got), len(pt))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (d) NONCE REUSE — structural proof from the wire format
// ---------------------------------------------------------------------------

// TestAdv_NoNonceReuse_AcrossManyEncryptions strengthens the existing
// pair-wise nonce check: it extracts the 12-byte prepended nonce from many
// encryptions of the SAME plaintext under the SAME key and asserts all nonces
// are distinct. A fixed/zero nonce (catastrophic for GCM confidentiality and
// integrity) would collide immediately.
//
// Mutation proof: in Encrypt, replace the io.ReadFull(rand.Reader, nonce) line
// with a no-op (leave nonce all-zero) -> every nonce collides and this test
// FAILS on the first duplicate.
func TestAdv_NoNonceReuse_AcrossManyEncryptions(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	pt := []byte("same plaintext every time")
	const n = 200
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		ctHex, err := Encrypt(key, pt)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		raw, err := hex.DecodeString(ctHex)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(raw) < 12 {
			t.Fatalf("ciphertext shorter than nonce: %d", len(raw))
		}
		nonce := string(raw[:12])
		if _, dup := seen[nonce]; dup {
			t.Fatalf("SINK BUG: nonce reuse detected after %d encryptions", i)
		}
		seen[nonce] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// CONTRACT PIN — GenerateKey is a DETERMINISTIC PLACEHOLDER, not a secret.
// ---------------------------------------------------------------------------

// TestAdv_GenerateKey_IsDeterministicPlaceholder_NotSecret documents and pins
// the explicit contract: GenerateKey is doc-commented "deterministic
// placeholder key". It returns SHA-256(seed)[:32] with NO secret randomness —
// identical seeds yield identical keys. This is NOT a bug (the doc disclaims
// it), but a latent-risk pin: were any caller to use it as a real AES key (it
// is 32 HEX chars == 16 raw bytes, AES-128-worth of entropy at best, fully
// predictable from the seed), that would be the defect. This test fails loudly
// if the function ever silently starts claiming to be random, forcing a
// doc/contract review.
func TestAdv_GenerateKey_IsDeterministicPlaceholder_NotSecret(t *testing.T) {
	const seed = "fixed-seed-value"
	a := GenerateKey(seed)
	b := GenerateKey(seed)
	if a != b {
		t.Fatal("contract drift: GenerateKey is documented deterministic but produced differing output")
	}
	// It is the hex prefix of SHA-256(seed): predictable, not secret.
	if a != Hash([]byte(seed))[:32] {
		t.Fatal("contract drift: GenerateKey no longer equals SHA-256(seed)[:32]")
	}
	if len(a) != 32 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("GenerateKey must be 32 lowercase hex chars, got %q", a)
	}
}
