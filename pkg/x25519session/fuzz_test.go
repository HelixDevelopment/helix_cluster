package x25519session

import (
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

// FuzzPeerPublicKey models the real wire path: a peer's X25519 public key
// arrives as raw bytes, is parsed via ecdh.X25519().NewPublicKey, and is then
// fed to the REAL DeriveSessionKey alongside a locally generated private key.
//
// DeriveSessionKey itself takes an already-parsed *ecdh.PublicKey, so the
// parse-from-bytes entry point that this package depends on is NewPublicKey;
// this fuzzer exercises that boundary plus the full ECDH + HKDF derivation.
// Invariant: arbitrary peer-key bytes must produce a clean parse/ECDH error or
// a well-formed 32-byte key — never a panic.
func FuzzPeerPublicKey(f *testing.F) {
	f.Add([]byte{}, "session-label")
	f.Add(make([]byte, 32), "session-label")
	f.Add(make([]byte, 5), "")

	// A real, valid X25519 public key so the corpus has a derivable path.
	if _, pub, err := GenerateEphemeral(rand.Reader); err == nil {
		f.Add(pub.Bytes(), "valid-label")
	}

	// One fixed local private key reused across iterations (cheap, real key).
	myPriv, _, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		f.Fatalf("GenerateEphemeral: %v", err)
	}

	f.Fuzz(func(t *testing.T, peerBytes []byte, label string) {
		curve := ecdh.X25519()
		peerPub, err := curve.NewPublicKey(peerBytes)
		if err != nil {
			// Malformed peer key bytes: rejected cleanly by crypto/ecdh.
			return
		}
		key, err := DeriveSessionKey(myPriv, peerPub, label)
		if err != nil {
			// Expected for empty label (ErrEmptyLabel) or ECDH failures.
			if key != nil {
				t.Fatalf("DeriveSessionKey returned non-nil key alongside error: %v", err)
			}
			return
		}
		if len(key) != SessionKeyLength {
			t.Fatalf("derived key length %d, want %d", len(key), SessionKeyLength)
		}
	})
}
