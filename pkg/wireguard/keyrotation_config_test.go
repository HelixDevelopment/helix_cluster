package wireguard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Key rotation (pure-Go, NoOp manager — no kernel/network calls)
// ---------------------------------------------------------------------------

// newRotationManager returns a NoOp manager with a real generated private key.
func newRotationManager(t *testing.T) *Manager {
	t.Helper()
	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.Address = "10.0.0.1/24"
	cfg.NoOp = true

	mgr, err := NewManager(cfg)
	require.NoError(t, err)
	return mgr
}

// TestKeyFingerprintDeterministicAndDistinct proves the fingerprint is a stable
// function of the key, and that distinct keys produce distinct fingerprints.
func TestKeyFingerprintDeterministicAndDistinct(t *testing.T) {
	_, pubA, err := GenerateKeyPair()
	require.NoError(t, err)
	_, pubB, err := GenerateKeyPair()
	require.NoError(t, err)

	fpA1, err := KeyFingerprint(pubA)
	require.NoError(t, err)
	fpA2, err := KeyFingerprint(pubA)
	require.NoError(t, err)
	fpB, err := KeyFingerprint(pubB)
	require.NoError(t, err)

	// Deterministic.
	assert.Equal(t, fpA1, fpA2)
	// Distinct keys => distinct fingerprints (mutation: must NOT collide).
	assert.NotEqual(t, fpA1, fpB)
	// Non-empty, hex-ish, reasonable length (SHA-256 truncated -> 16 hex chars).
	assert.NotEmpty(t, fpA1)
	assert.Len(t, fpA1, 16)

	// Invalid key is rejected.
	_, err = KeyFingerprint("not-a-key")
	assert.Error(t, err)
}

// TestRotateKeysTracked proves a rotation produces a fresh keypair that differs
// from the old one, bumps the generation counter, and updates the fingerprint.
func TestRotateKeysTracked(t *testing.T) {
	mgr := newRotationManager(t)

	gen0 := mgr.KeyGeneration()
	assert.Equal(t, uint64(0), gen0)

	oldPriv := mgr.config.PrivateKey
	oldState := mgr.CurrentKeyState()
	require.NotNil(t, oldState)

	res, err := mgr.RotateKeysTracked()
	require.NoError(t, err)
	require.NotNil(t, res)

	// New keys are non-empty and genuinely fresh.
	assert.NotEmpty(t, res.NewPublicKey)
	assert.NotEmpty(t, res.NewPrivateKey)
	assert.NotEqual(t, oldPriv, res.NewPrivateKey)           // private key rotated
	assert.NotEqual(t, oldState.PublicKey, res.NewPublicKey) // public key rotated

	// Generation counter advanced by exactly one.
	assert.Equal(t, gen0+1, res.Generation)
	assert.Equal(t, gen0+1, mgr.KeyGeneration())

	// Fingerprint changed and matches the new public key.
	wantFP, err := KeyFingerprint(res.NewPublicKey)
	require.NoError(t, err)
	assert.Equal(t, wantFP, res.NewFingerprint)
	assert.NotEqual(t, oldState.Fingerprint, res.NewFingerprint)

	// Manager config now holds the new private key.
	assert.Equal(t, res.NewPrivateKey, mgr.config.PrivateKey)

	// The result records the superseded (old) key for audit/detection.
	assert.Equal(t, oldState.PublicKey, res.PreviousPublicKey)
	assert.Equal(t, oldState.Fingerprint, res.PreviousFingerprint)
}

// TestRotationSupersedesOldKey proves the new state supersedes the previous one
// and that Supersedes() correctly detects the relationship (and its negation).
func TestRotationSupersedesOldKey(t *testing.T) {
	mgr := newRotationManager(t)

	before := mgr.CurrentKeyState()
	res, err := mgr.RotateKeysTracked()
	require.NoError(t, err)
	after := mgr.CurrentKeyState()

	// after supersedes before.
	assert.True(t, after.Supersedes(before))
	// before does NOT supersede after (mutation: ordering must be strict).
	assert.False(t, before.Supersedes(after))
	// A state never supersedes itself.
	assert.False(t, after.Supersedes(after))

	// Generations strictly increase.
	assert.Greater(t, after.Generation, before.Generation)
	assert.Equal(t, res.Generation, after.Generation)

	// Two rotations: each supersedes its predecessor.
	mid := mgr.CurrentKeyState()
	_, err = mgr.RotateKeysTracked()
	require.NoError(t, err)
	latest := mgr.CurrentKeyState()
	assert.True(t, latest.Supersedes(mid))
	assert.True(t, latest.Supersedes(before))
}

// TestKeyStateImmutableCopy proves CurrentKeyState returns a snapshot that does
// not change when the manager later rotates.
func TestKeyStateImmutableCopy(t *testing.T) {
	mgr := newRotationManager(t)

	snap := mgr.CurrentKeyState()
	snapGen := snap.Generation
	snapFP := snap.Fingerprint

	_, err := mgr.RotateKeysTracked()
	require.NoError(t, err)

	// Old snapshot is unaffected by the rotation.
	assert.Equal(t, snapGen, snap.Generation)
	assert.Equal(t, snapFP, snap.Fingerprint)
	// Live state moved on.
	assert.NotEqual(t, snap.Fingerprint, mgr.CurrentKeyState().Fingerprint)
}

// ---------------------------------------------------------------------------
// Peer / interface config generation correctness
// ---------------------------------------------------------------------------

// TestPeerConfigStringEmitsFields proves AllowedIPs, Endpoint and
// PersistentKeepalive are emitted into a valid wg config block.
func TestPeerConfigStringEmitsFields(t *testing.T) {
	_, pub, err := GenerateKeyPair()
	require.NoError(t, err)
	_, psk, err := GenerateKeyPair()
	require.NoError(t, err)

	peer := &PeerConfig{
		PublicKey:           pub,
		PresharedKey:        psk,
		AllowedIPs:          []string{"10.0.0.2/32", "10.0.1.0/24"},
		Endpoint:            "192.168.1.5:51820",
		PersistentKeepalive: 25,
	}

	out, err := peer.ConfigString()
	require.NoError(t, err)

	assert.Contains(t, out, "[Peer]")
	assert.Contains(t, out, "PublicKey = "+pub)
	assert.Contains(t, out, "PresharedKey = "+psk)
	// AllowedIPs are comma-joined on a single line.
	assert.Contains(t, out, "AllowedIPs = 10.0.0.2/32, 10.0.1.0/24")
	assert.Contains(t, out, "Endpoint = 192.168.1.5:51820")
	assert.Contains(t, out, "PersistentKeepalive = 25")

	// Mutation: a wrong keepalive value must NOT appear.
	assert.NotContains(t, out, "PersistentKeepalive = 0")
	assert.NotContains(t, out, "PersistentKeepalive = 26")
	// Mutation: an IP that was not configured must NOT appear.
	assert.NotContains(t, out, "10.0.0.3/32")
}

// TestPeerConfigStringOmitsOptional proves optional fields are omitted when empty
// rather than emitted with empty values.
func TestPeerConfigStringOmitsOptional(t *testing.T) {
	_, pub, err := GenerateKeyPair()
	require.NoError(t, err)

	peer := &PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"10.0.0.9/32"},
		// No PresharedKey, no Endpoint, keepalive 0.
	}
	out, err := peer.ConfigString()
	require.NoError(t, err)

	assert.Contains(t, out, "PublicKey = "+pub)
	assert.Contains(t, out, "AllowedIPs = 10.0.0.9/32")
	assert.NotContains(t, out, "PresharedKey")
	assert.NotContains(t, out, "Endpoint")
	// keepalive 0 means disabled => omitted entirely.
	assert.NotContains(t, out, "PersistentKeepalive")
}

// TestPeerConfigValidation proves bad input is rejected (mutation: invalid CIDR
// / endpoint / key must error, not silently pass).
func TestPeerConfigValidation(t *testing.T) {
	_, pub, err := GenerateKeyPair()
	require.NoError(t, err)

	// Bad public key.
	_, err = (&PeerConfig{PublicKey: "bad", AllowedIPs: []string{"10.0.0.2/32"}}).ConfigString()
	assert.Error(t, err)

	// Bad CIDR.
	_, err = (&PeerConfig{PublicKey: pub, AllowedIPs: []string{"10.0.0.2"}}).ConfigString()
	assert.Error(t, err)

	// Bad endpoint.
	_, err = (&PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"10.0.0.2/32"},
		Endpoint:   "no-port",
	}).ConfigString()
	assert.Error(t, err)

	// Missing AllowedIPs.
	_, err = (&PeerConfig{PublicKey: pub}).ConfigString()
	assert.Error(t, err)
}

// TestInterfaceConfigRoundTrip proves a full interface+peers config string is
// generated and parses back to equivalent structures.
func TestInterfaceConfigRoundTrip(t *testing.T) {
	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)
	_, peerPub, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.Address = "10.0.0.1/24"
	cfg.ListenPort = 51820
	cfg.MTU = 1420
	cfg.NoOp = true

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	peer := &PeerConfig{
		PublicKey:           peerPub,
		AllowedIPs:          []string{"10.0.0.2/32"},
		Endpoint:            "203.0.113.7:51820",
		PersistentKeepalive: 25,
	}
	require.NoError(t, mgr.AddPeer(peer))

	out, err := mgr.GenerateConfigString()
	require.NoError(t, err)

	assert.Contains(t, out, "[Interface]")
	assert.Contains(t, out, "PrivateKey = "+priv)
	assert.Contains(t, out, "Address = 10.0.0.1/24")
	assert.Contains(t, out, "ListenPort = 51820")
	assert.Contains(t, out, "MTU = 1420")
	assert.Contains(t, out, "[Peer]")
	assert.Contains(t, out, "PublicKey = "+peerPub)

	// Round-trip parse.
	parsed, err := ParseConfigString(out)
	require.NoError(t, err)
	assert.Equal(t, priv, parsed.Interface.PrivateKey)
	assert.Equal(t, "10.0.0.1/24", parsed.Interface.Address)
	assert.Equal(t, 51820, parsed.Interface.ListenPort)
	assert.Equal(t, 1420, parsed.Interface.MTU)
	require.Len(t, parsed.Peers, 1)
	assert.Equal(t, peerPub, parsed.Peers[0].PublicKey)
	assert.Equal(t, []string{"10.0.0.2/32"}, parsed.Peers[0].AllowedIPs)
	assert.Equal(t, "203.0.113.7:51820", parsed.Peers[0].Endpoint)
	assert.Equal(t, 25, parsed.Peers[0].PersistentKeepalive)
}

// TestGeneratedConfigSupersedesAfterRotation proves that rotating the manager's
// key changes the emitted [Interface] PrivateKey, superseding the old config.
func TestGeneratedConfigSupersedesAfterRotation(t *testing.T) {
	mgr := newRotationManager(t)

	before, err := mgr.GenerateConfigString()
	require.NoError(t, err)
	oldPriv := mgr.config.PrivateKey

	_, err = mgr.RotateKeysTracked()
	require.NoError(t, err)

	after, err := mgr.GenerateConfigString()
	require.NoError(t, err)

	// The old private key no longer appears; the config is superseded.
	assert.NotEqual(t, before, after)
	assert.NotContains(t, after, "PrivateKey = "+oldPriv)
	assert.Contains(t, after, "PrivateKey = "+mgr.config.PrivateKey)

	// And the generation marker advanced.
	assert.Contains(t, after, "# KeyGeneration: 1")
	assert.True(t, strings.Contains(before, "# KeyGeneration: 0"))
}
