package wireguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// KeyFingerprint returns a short, stable fingerprint for a base64-encoded
// WireGuard key. It is a deterministic function of the key bytes: identical
// keys always yield identical fingerprints and distinct keys yield distinct
// fingerprints (modulo SHA-256 collision resistance). The fingerprint is the
// first 8 bytes of SHA-256(key bytes), hex-encoded (16 hex characters).
//
// Pure-Go: no kernel or network access.
func KeyFingerprint(key string) (string, error) {
	k, err := ParseKey(key)
	if err != nil {
		return "", fmt.Errorf("cannot fingerprint key: %w", err)
	}
	sum := sha256.Sum256(k[:])
	return hex.EncodeToString(sum[:8]), nil
}

// KeyState is an immutable snapshot of the manager's active key material at a
// given rotation generation. It is safe to retain a KeyState across subsequent
// rotations — it is a value copy and never mutated in place.
type KeyState struct {
	// Generation is a monotonically increasing counter. Generation 0 is the
	// key the manager started with; each rotation increments it by one.
	Generation uint64
	// PublicKey is the base64-encoded Curve25519 public key for this state.
	PublicKey string
	// Fingerprint is KeyFingerprint(PublicKey).
	Fingerprint string
	// RotatedAt is the wall-clock time this state became active.
	RotatedAt time.Time
}

// Supersedes reports whether s is a strictly later key state than other —
// i.e. s was produced by a rotation that came after other. A state never
// supersedes itself or any state at an equal/greater generation.
func (s KeyState) Supersedes(other KeyState) bool {
	return s.Generation > other.Generation
}

// RotationResult records the outcome of a tracked key rotation, including the
// fresh key material and the (now superseded) previous key for audit/detection.
type RotationResult struct {
	Generation          uint64
	NewPrivateKey       string
	NewPublicKey        string
	NewFingerprint      string
	PreviousPublicKey   string
	PreviousFingerprint string
}

// KeyGeneration returns the current key generation counter.
func (m *Manager) KeyGeneration() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.keyGeneration
}

// CurrentKeyState returns an immutable snapshot of the manager's active key
// state. The returned value is a copy and is unaffected by later rotations.
//
// If no public key has been derived yet (e.g. the configured private key is
// empty/invalid), PublicKey and Fingerprint may be empty but Generation is
// still meaningful.
func (m *Manager) CurrentKeyState() KeyState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentKeyStateLocked()
}

// currentKeyStateLocked builds a KeyState from the live config. Caller must
// hold at least a read lock.
func (m *Manager) currentKeyStateLocked() KeyState {
	st := KeyState{
		Generation: m.keyGeneration,
		RotatedAt:  m.keyRotatedAt,
	}
	if m.config != nil && m.config.PrivateKey != "" {
		if priv, err := ParseKey(m.config.PrivateKey); err == nil {
			pub := priv.PublicKey()
			st.PublicKey = pub.String()
			if fp, err := KeyFingerprint(st.PublicKey); err == nil {
				st.Fingerprint = fp
			}
		}
	}
	return st
}

// RotateKeysTracked generates a fresh Curve25519 keypair, installs it as the
// manager's active key (superseding the previous one), advances the generation
// counter, and returns a RotationResult describing the change.
//
// On non-NoOp managers the new private key is pushed to the device via wgctrl;
// in NoOp mode only the in-memory config is updated. This method is purely
// computational with respect to key generation — it performs no network calls.
func (m *Manager) RotateKeysTracked() (*RotationResult, error) {
	privKeyStr, pubKeyStr, err := GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate key pair: %w", err)
	}
	newFP, err := KeyFingerprint(pubKeyStr)
	if err != nil {
		return nil, fmt.Errorf("failed to fingerprint new key: %w", err)
	}
	privKey, err := ParseKey(privKeyStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse new private key: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Capture the state being superseded before mutating.
	prev := m.currentKeyStateLocked()

	if !m.config.NoOp {
		if err := m.client.ConfigureDevice(m.config.InterfaceName, deviceKeyOnly(privKey)); err != nil {
			return nil, fmt.Errorf("failed to rotate keys on device: %w", err)
		}
	}

	m.config.PrivateKey = privKeyStr
	m.keyGeneration++
	m.keyRotatedAt = time.Now()

	return &RotationResult{
		Generation:          m.keyGeneration,
		NewPrivateKey:       privKeyStr,
		NewPublicKey:        pubKeyStr,
		NewFingerprint:      newFP,
		PreviousPublicKey:   prev.PublicKey,
		PreviousFingerprint: prev.Fingerprint,
	}, nil
}
