package hybridkex

import (
	"testing"
)

// FuzzRespond feeds arbitrary bytes into the two parse-from-bytes fields of an
// InitiatorHello (X25519 public key + ML-KEM-768 encapsulation-key bytes) and
// drives the REAL Respond path: X25519 public-key parse, ECDH, ML-KEM
// encapsulation-key parse, encapsulate, and HKDF derive. Invariant: hostile
// bytes must produce a clean error (never a panic, never an out-of-range slice
// inside crypto/ecdh or crypto/mlkem), and an error must never come back with a
// non-nil Responder / hello / key.
func FuzzRespond(f *testing.F) {
	f.Add([]byte{}, []byte{})
	f.Add(make([]byte, 32), make([]byte, 1184)) // X25519 pub is 32B; ML-KEM-768 enc key is 1184B

	// A real, well-formed InitiatorHello so the corpus contains a valid path.
	if _, hello, err := NewInitiator(); err == nil {
		f.Add(hello.X25519Pub, hello.MLKEMEncKey)
	}

	f.Fuzz(func(t *testing.T, x25519Pub, mlkemEncKey []byte) {
		hello := &InitiatorHello{X25519Pub: x25519Pub, MLKEMEncKey: mlkemEncKey}
		resp, rhello, key, err := Respond(hello)
		if err != nil {
			if resp != nil || rhello != nil || key != nil {
				t.Fatalf("Respond returned non-nil outputs alongside error: %v", err)
			}
			return
		}
		// On success every output must be well-formed.
		if resp == nil || rhello == nil {
			t.Fatal("Respond returned nil Responder/hello with nil error")
		}
		if len(key) != SessionKeyLen {
			t.Fatalf("derived key length %d, want %d", len(key), SessionKeyLen)
		}
	})
}

// FuzzFinish feeds arbitrary bytes into the two parse-from-bytes fields of a
// ResponderHello (X25519 public key + ML-KEM-768 ciphertext) and drives the
// REAL Initiator.Finish path against a freshly generated Initiator: X25519
// public-key parse, ECDH, ML-KEM decapsulation (implicit-rejection), and HKDF
// derive. Invariant: hostile bytes produce a clean error or a well-formed key,
// never a panic.
func FuzzFinish(f *testing.F) {
	f.Add(make([]byte, 32), make([]byte, 1088)) // ML-KEM-768 ciphertext is 1088B
	f.Add([]byte{}, []byte{})

	// A real, well-formed ResponderHello (from a complete handshake) for the
	// valid path.
	if in, ihello, err := NewInitiator(); err == nil {
		if _, rhello, _, err := Respond(ihello); err == nil {
			f.Add(rhello.X25519Pub, rhello.MLKEMCipher)
			_ = in
		}
	}

	f.Fuzz(func(t *testing.T, x25519Pub, mlkemCipher []byte) {
		in, _, err := NewInitiator()
		if err != nil {
			t.Fatalf("NewInitiator: %v", err)
		}
		rhello := &ResponderHello{X25519Pub: x25519Pub, MLKEMCipher: mlkemCipher}
		key, err := in.Finish(rhello)
		if err != nil {
			if key != nil {
				t.Fatalf("Finish returned non-nil key alongside error: %v", err)
			}
			return
		}
		// ML-KEM Decapsulate is implicit-rejection: a wrong-length ciphertext
		// errors, but a correct-length garbage ciphertext yields a pseudorandom
		// key with no error. Either way the key must be the right length.
		if len(key) != SessionKeyLen {
			t.Fatalf("derived key length %d, want %d", len(key), SessionKeyLen)
		}
	})
}
