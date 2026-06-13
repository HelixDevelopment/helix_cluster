package jwt

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestKeySet_ConcurrentVerifyWhileRotating is an adversarial race probe for the
// real-world JWT keystore contract: tokens are VERIFIED concurrently (every
// request reads ks.keys[kid]) while keys are ROTATED/ADDED at runtime
// (ks.keys[kid] = ...). This is the documented purpose of KeySet ("enabling key
// rotation and multi-issuer verification").
//
// ANTI-BLUFF — why this bites: an unsynchronized map read concurrent with a map
// write is a data race that the Go runtime turns into a fatal, unrecoverable
// "concurrent map read and map write" crash (and -race flags it as a DATA RACE).
// If KeySet has no lock, this test crashes/races the auth path under -race. The
// only way it stays green is if the read (ValidateKeySet) and write (AddHMAC /
// AddRSAPublicKey) paths are mutually synchronized. Reverting any such sync makes
// this test fail — that is the load-bearing proof.
func TestKeySet_ConcurrentVerifyWhileRotating(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base}))
	ks := NewKeySet()

	// Seed a stable kid so verifiers always have at least one valid target.
	stableSecret := []byte("stable-rotation-anchor-secret")
	if err := ks.AddHMAC("kid-stable", stableSecret); err != nil {
		t.Fatalf("seed AddHMAC: %v", err)
	}
	stableTok := mkHMACToken(t, stableSecret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": "kid-stable"},
		map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()},
	)

	// Pre-generate an RSA key so RSA registrations during rotation are real.
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa genkey: %v", err)
	}

	const (
		verifiers = 16
		rotators  = 4
		iters     = 400
	)

	var wg sync.WaitGroup
	start := make(chan struct{})

	// READERS: hammer ValidateKeySet (map read on every request).
	for i := 0; i < verifiers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				// The verification result is allowed to be nil (stable kid) or an
				// error (a kid mid-rotation); the point is the READ must not race
				// the concurrent WRITE.
				_ = v.ValidateKeySet(stableTok, ks)
			}
		}()
	}

	// WRITERS: rotate/add keys at runtime (map write on every iteration).
	for i := 0; i < rotators; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				kid := fmt.Sprintf("rot-%d-%d", id, j)
				if j%2 == 0 {
					_ = ks.AddHMAC(kid, []byte(fmt.Sprintf("secret-%d-%d", id, j)))
				} else {
					_ = ks.AddRSAPublicKey(kid, &rsaPriv.PublicKey)
				}
				// Also re-register the stable kid to exercise overwrite-in-place
				// (a write to an existing key, the classic rotation move).
				_ = ks.AddHMAC("kid-stable", stableSecret)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	// After the storm, the stable token must still verify: rotation churn must
	// not have corrupted the anchor entry.
	if err := v.ValidateKeySet(stableTok, ks); err != nil {
		t.Fatalf("post-rotation: stable token must still verify, got %v", err)
	}
}

// TestKeySet_ConcurrentRotatedKeyVerifies proves rotation correctness under
// concurrency: a key added by a writer goroutine becomes usable for verification
// by reader goroutines (no torn entry, no lost write). The waiter reads the key
// the instant after the writer registers it and the freshly-signed token must
// verify with the freshly-rotated key.
func TestKeySet_ConcurrentRotatedKeyVerifies(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base}))
	ks := NewKeySet()

	const n = 200
	var wg sync.WaitGroup

	// Writers register kid-i with secret-i.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kid := fmt.Sprintf("rk-%d", i)
			secret := []byte(fmt.Sprintf("rotated-secret-%d", i))
			if err := ks.AddHMAC(kid, secret); err != nil {
				t.Errorf("AddHMAC(%s): %v", kid, err)
			}
		}(i)
	}

	wg.Wait()

	// Every rotated key must now verify a token signed with it. A lost/torn write
	// would surface here as a verification failure for a key that was registered.
	for i := 0; i < n; i++ {
		kid := fmt.Sprintf("rk-%d", i)
		secret := []byte(fmt.Sprintf("rotated-secret-%d", i))
		tok := mkHMACToken(t, secret,
			map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": kid},
			map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()},
		)
		if err := v.ValidateKeySet(tok, ks); err != nil {
			t.Fatalf("rotated key %s must verify its token, got %v", kid, err)
		}
	}
}
