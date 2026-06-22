package wireguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
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

// deviceIdentity is the reserved peer-keys identity used to track the
// manager's own device key rotation grace window via the peer-keys machinery.
const deviceIdentity = "__device__"

// RotateKeysTracked generates a fresh Curve25519 keypair, installs it as the
// manager's active key (superseding the previous one), advances the generation
// counter, and returns a RotationResult describing the change.
//
// If m.config.KeyOverlap > 0 the superseded public key is retained in the
// active-keys grace window for that duration, so remote peers that have not
// yet received the rotation notice can still establish sessions. The window is
// tracked internally via the same per-peer machinery used by
// RotatePeerKeyWithOverlap; callers retrieve the live + grace keys via
// ActiveKeys().
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
	if _, err := ParseKey(privKeyStr); err != nil {
		return nil, fmt.Errorf("failed to parse new private key: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Capture the state being superseded before mutating.
	prev := m.currentKeyStateLocked()

	if !m.config.NoOp {
		if err := m.backend.rotateDeviceKey(privKeyStr); err != nil {
			return nil, fmt.Errorf("failed to rotate keys on device: %w", err)
		}
	}

	m.config.PrivateKey = privKeyStr
	m.keyGeneration++
	m.keyRotatedAt = time.Now()

	// Track the device's own key in the peer-keys grace-window machinery so
	// that ActiveKeys() and mesh consumers can retrieve old+new keys during
	// the overlap window. We use the reserved identity deviceIdentity.
	if m.peerKeys == nil {
		m.peerKeys = make(map[string][]validKey)
	}
	now := m.clock()
	existing := m.peerKeys[deviceIdentity]

	// Bootstrap: if the device identity has never been seeded (first rotation),
	// create a synthetic live-key entry for the previous key so it can be
	// retained in the grace window.
	if len(existing) == 0 && prev.PublicKey != "" {
		existing = []validKey{{publicKey: prev.PublicKey}}
	}

	next := make([]validKey, 0, len(existing)+1)
	overlap := m.config.KeyOverlap
	for _, vk := range existing {
		if vk.publicKey == pubKeyStr {
			continue
		}
		if vk.expiresAt.IsZero() {
			// Previous live key: retain for the overlap window if configured.
			if overlap > 0 {
				next = append(next, validKey{publicKey: vk.publicKey, expiresAt: now.Add(overlap)})
			}
			continue
		}
		next = append(next, vk)
	}
	next = append(next, validKey{publicKey: pubKeyStr}) // live key
	if pruned := pruneExpired(next, now); pruned != nil {
		next = pruned
	}
	m.peerKeys[deviceIdentity] = next

	return &RotationResult{
		Generation:          m.keyGeneration,
		NewPrivateKey:       privKeyStr,
		NewPublicKey:        pubKeyStr,
		NewFingerprint:      newFP,
		PreviousPublicKey:   prev.PublicKey,
		PreviousFingerprint: prev.Fingerprint,
	}, nil
}

// ActiveKeys returns the set of public keys currently accepted for THIS
// manager's device identity — the live key plus any superseded keys whose
// grace/overlap window has not yet elapsed. The live key is always first.
//
// This builds on the same per-peer key-overlap machinery used by
// RotatePeerKeyWithOverlap; callers use it so that mesh peers that have not
// yet received a rotation notice can still establish sessions with the old key
// during the configured overlap window.
//
// Pure-Go: reads only in-memory state.
func (m *Manager) ActiveKeys() []string {
	return m.ValidPeerKeys(deviceIdentity)
}

// ---------------------------------------------------------------------------
// Per-peer key rotation with grace/overlap window
// ---------------------------------------------------------------------------

// validKey is one public key accepted for a peer, with an optional expiry. A
// zero expiry means the key is valid indefinitely (the current key); a non-zero
// expiry marks a superseded key that stays valid only until that instant so
// in-flight traffic encrypted under it is not dropped during rotation.
type validKey struct {
	publicKey string
	// expiresAt is the wall-clock instant after which the key is no longer
	// valid. Zero means "never expires" (the live key).
	expiresAt time.Time
}

// PeerKeyState reports the currently-valid public keys for a peer identity.
type PeerKeyState struct {
	// Identity is the caller-chosen logical peer identity.
	Identity string
	// Current is the newest (live) public key for the peer.
	Current string
	// ValidKeys is every public key that is currently accepted for the peer,
	// including Current and any not-yet-expired superseded keys. Order is
	// stable: Current first, then superseded keys by descending expiry.
	ValidKeys []string
}

// RotatePeerKeyWithOverlap rotates the public key for the logical peer
// identified by identity. The newPublicKey becomes the live key immediately,
// while the previous live key (if any) is kept valid for the overlap duration
// so traffic already encrypted under it is accepted until the grace window
// closes. An overlap <= 0 expires the old key immediately.
//
// It returns the resulting PeerKeyState. The returned ValidKeys reflects expiry
// as of the manager's clock at call time (already-expired old keys are pruned).
//
// Pure-Go: updates only in-memory state, no kernel or network access.
func (m *Manager) RotatePeerKeyWithOverlap(identity, newPublicKey string, overlap time.Duration) (PeerKeyState, error) {
	if identity == "" {
		return PeerKeyState{}, fmt.Errorf("peer identity is empty")
	}
	if _, err := ParseKey(newPublicKey); err != nil {
		return PeerKeyState{}, fmt.Errorf("invalid new public key: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.peerKeys == nil {
		m.peerKeys = make(map[string][]validKey)
	}
	now := m.clock()

	// Find the prior live key (the one with no expiry) and give it a deadline.
	existing := m.peerKeys[identity]
	next := make([]validKey, 0, len(existing)+1)
	for _, vk := range existing {
		if vk.publicKey == newPublicKey {
			// The new key already exists in some form; drop the old entry so
			// the fresh live entry below replaces it.
			continue
		}
		if vk.expiresAt.IsZero() {
			// Previous live key: it now becomes a superseded key that lingers
			// for the overlap window.
			if overlap > 0 {
				next = append(next, validKey{publicKey: vk.publicKey, expiresAt: now.Add(overlap)})
			}
			// overlap <= 0 => drop the old key immediately (do not append).
			continue
		}
		next = append(next, vk)
	}
	// Install the new live key (never-expires sentinel).
	next = append(next, validKey{publicKey: newPublicKey})

	if pruned := pruneExpired(next, now); pruned != nil {
		next = pruned
	}
	m.peerKeys[identity] = next

	return m.peerKeyStateLocked(identity, now), nil
}

// ValidPeerKeys returns the set of public keys currently accepted for the peer
// identity, pruning any keys whose overlap window has elapsed. The newest live
// key is first.
//
// Pure-Go: reads only in-memory state.
func (m *Manager) ValidPeerKeys(identity string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	if pruned := pruneExpired(m.peerKeys[identity], now); pruned != nil {
		m.peerKeys[identity] = pruned
	}
	return m.peerKeyStateLocked(identity, now).ValidKeys
}

// PeerKeyStateAt returns the full PeerKeyState for the identity as of the
// manager's clock.
func (m *Manager) PeerKeyState(identity string) PeerKeyState {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	if pruned := pruneExpired(m.peerKeys[identity], now); pruned != nil {
		m.peerKeys[identity] = pruned
	}
	return m.peerKeyStateLocked(identity, now)
}

// IsPeerKeyValid reports whether publicKey is currently accepted for the peer
// identity (live key or within an unexpired overlap window).
func (m *Manager) IsPeerKeyValid(identity, publicKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	for _, vk := range m.peerKeys[identity] {
		if vk.publicKey != publicKey {
			continue
		}
		if vk.expiresAt.IsZero() || now.Before(vk.expiresAt) {
			return true
		}
	}
	return false
}

// peerKeyStateLocked builds a PeerKeyState snapshot. Caller must hold m.mu and
// pass the evaluation time. It does not mutate state.
func (m *Manager) peerKeyStateLocked(identity string, now time.Time) PeerKeyState {
	keys := m.peerKeys[identity]
	st := PeerKeyState{Identity: identity}

	// Order: live key (no expiry) first, then unexpired superseded keys by
	// descending expiry (longest-lived overlap first) for determinism.
	live := make([]string, 0, 1)
	type expiring struct {
		key string
		at  time.Time
	}
	var lingering []expiring
	for _, vk := range keys {
		if vk.expiresAt.IsZero() {
			live = append(live, vk.publicKey)
			continue
		}
		if now.Before(vk.expiresAt) {
			lingering = append(lingering, expiring{vk.publicKey, vk.expiresAt})
		}
	}
	sort.SliceStable(lingering, func(i, j int) bool {
		return lingering[i].at.After(lingering[j].at)
	})

	if len(live) > 0 {
		st.Current = live[0]
	}
	st.ValidKeys = append(st.ValidKeys, live...)
	for _, e := range lingering {
		st.ValidKeys = append(st.ValidKeys, e.key)
	}
	return st
}

// pruneExpired returns keys with any entry whose overlap window has elapsed
// removed. It returns nil when there is nothing to prune (so callers can skip
// rewriting the map) and a fresh slice otherwise.
func pruneExpired(keys []validKey, now time.Time) []validKey {
	changed := false
	out := keys[:0:0]
	for _, vk := range keys {
		if vk.expiresAt.IsZero() || now.Before(vk.expiresAt) {
			out = append(out, vk)
		} else {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return out
}
