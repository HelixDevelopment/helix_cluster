package wireguard

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOverlapManager returns a NoOp manager with an injectable clock so overlap
// expiry can be driven deterministically (no sleeping, no flakiness).
func newOverlapManager(t *testing.T, clock *time.Time) *Manager {
	t.Helper()
	mgr := newRotationManager(t)
	mgr.now = func() time.Time { return *clock }
	return mgr
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestPeerKeyOverlapBothValidThenExpires proves the core grace-window contract:
// during the overlap window BOTH the old and new peer keys are accepted, and
// after the window elapses ONLY the new key remains valid.
//
// Mutation that kills it: set overlap to 0 (drop the old key immediately) in
// RotatePeerKeyWithOverlap — the "both valid during overlap" assertions fail
// because the old key is gone the instant the new key is installed.
func TestPeerKeyOverlapBothValidThenExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mgr := newOverlapManager(t, &now)

	_, oldPub, err := GenerateKeyPair()
	require.NoError(t, err)
	_, newPub, err := GenerateKeyPair()
	require.NoError(t, err)

	// Install the original key (no overlap needed — it's the first one).
	st, err := mgr.RotatePeerKeyWithOverlap("nodeA", oldPub, 0)
	require.NoError(t, err)
	assert.Equal(t, oldPub, st.Current)
	assert.Equal(t, []string{oldPub}, st.ValidKeys)

	// Rotate to the new key with a 30s overlap window.
	st, err = mgr.RotatePeerKeyWithOverlap("nodeA", newPub, 30*time.Second)
	require.NoError(t, err)

	// During the overlap window BOTH keys are valid; the new one is current.
	assert.Equal(t, newPub, st.Current)
	assert.True(t, contains(st.ValidKeys, oldPub), "old key must remain valid during overlap")
	assert.True(t, contains(st.ValidKeys, newPub), "new key must be valid")
	assert.Len(t, st.ValidKeys, 2)

	assert.True(t, mgr.IsPeerKeyValid("nodeA", oldPub))
	assert.True(t, mgr.IsPeerKeyValid("nodeA", newPub))
	assert.ElementsMatch(t, []string{oldPub, newPub}, mgr.ValidPeerKeys("nodeA"))

	// Advance to just before expiry: old key still valid.
	now = now.Add(29 * time.Second)
	assert.True(t, mgr.IsPeerKeyValid("nodeA", oldPub), "old key valid until window closes")
	assert.ElementsMatch(t, []string{oldPub, newPub}, mgr.ValidPeerKeys("nodeA"))

	// Advance past expiry: ONLY the new key remains valid.
	now = now.Add(2 * time.Second) // total +31s > 30s window
	assert.False(t, mgr.IsPeerKeyValid("nodeA", oldPub), "old key must expire after overlap")
	assert.True(t, mgr.IsPeerKeyValid("nodeA", newPub))
	assert.Equal(t, []string{newPub}, mgr.ValidPeerKeys("nodeA"))

	final := mgr.PeerKeyState("nodeA")
	assert.Equal(t, newPub, final.Current)
	assert.Equal(t, []string{newPub}, final.ValidKeys)
}

// TestPeerKeyZeroOverlapDropsOldImmediately proves the documented zero-overlap
// behavior: with overlap <= 0 the old key is dropped the instant the new key
// is installed (no grace window).
//
// Mutation that kills it: change `if overlap > 0` to always append the old key
// with an expiry — the old key would linger and the "only new valid" assertion
// fails.
func TestPeerKeyZeroOverlapDropsOldImmediately(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mgr := newOverlapManager(t, &now)

	_, oldPub, err := GenerateKeyPair()
	require.NoError(t, err)
	_, newPub, err := GenerateKeyPair()
	require.NoError(t, err)

	_, err = mgr.RotatePeerKeyWithOverlap("nodeB", oldPub, time.Hour)
	require.NoError(t, err)

	st, err := mgr.RotatePeerKeyWithOverlap("nodeB", newPub, 0)
	require.NoError(t, err)

	assert.Equal(t, newPub, st.Current)
	assert.Equal(t, []string{newPub}, st.ValidKeys)
	assert.False(t, mgr.IsPeerKeyValid("nodeB", oldPub), "zero overlap must drop old key immediately")
	assert.True(t, mgr.IsPeerKeyValid("nodeB", newPub))
}

// TestPeerKeyOverlapRejectsBadKey proves an invalid public key is rejected and
// does not mutate the peer's valid-key set.
//
// Mutation that kills it: remove the ParseKey validation in
// RotatePeerKeyWithOverlap — the bad key would be installed and require.Error
// fails.
func TestPeerKeyOverlapRejectsBadKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mgr := newOverlapManager(t, &now)

	_, good, err := GenerateKeyPair()
	require.NoError(t, err)
	_, err = mgr.RotatePeerKeyWithOverlap("nodeC", good, 0)
	require.NoError(t, err)

	_, err = mgr.RotatePeerKeyWithOverlap("nodeC", "not-a-valid-key", time.Minute)
	require.Error(t, err)

	// State is unchanged: the good key is still the only valid one.
	assert.Equal(t, []string{good}, mgr.ValidPeerKeys("nodeC"))

	// Empty identity is rejected too.
	_, err = mgr.RotatePeerKeyWithOverlap("", good, 0)
	require.Error(t, err)
}

// TestPeerKeyOverlapIsolatedPerIdentity proves overlap windows are tracked
// independently per peer identity.
//
// Mutation that kills it: key peerKeys by a constant instead of identity — the
// two peers' key sets would collide and these per-identity assertions fail.
func TestPeerKeyOverlapIsolatedPerIdentity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mgr := newOverlapManager(t, &now)

	_, a, err := GenerateKeyPair()
	require.NoError(t, err)
	_, b, err := GenerateKeyPair()
	require.NoError(t, err)

	_, err = mgr.RotatePeerKeyWithOverlap("alice", a, 0)
	require.NoError(t, err)
	_, err = mgr.RotatePeerKeyWithOverlap("bob", b, 0)
	require.NoError(t, err)

	assert.Equal(t, []string{a}, mgr.ValidPeerKeys("alice"))
	assert.Equal(t, []string{b}, mgr.ValidPeerKeys("bob"))
	assert.False(t, mgr.IsPeerKeyValid("alice", b))
	assert.False(t, mgr.IsPeerKeyValid("bob", a))
}

// TestPeerKeyOverlapDescendingExpiryOrder proves that after two rapid rotations
// within a long overlap window, all three keys are valid and ordered current-
// first then by descending expiry.
//
// Mutation that kills it: remove the descending-expiry sort in
// peerKeyStateLocked — the ValidKeys ordering assertion (newer overlap before
// older) fails.
func TestPeerKeyOverlapDescendingExpiryOrder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mgr := newOverlapManager(t, &now)

	_, k0, err := GenerateKeyPair()
	require.NoError(t, err)
	_, k1, err := GenerateKeyPair()
	require.NoError(t, err)
	_, k2, err := GenerateKeyPair()
	require.NoError(t, err)

	_, err = mgr.RotatePeerKeyWithOverlap("n", k0, 0)
	require.NoError(t, err)

	// First rotation: k0 lingers, expiry now+100s.
	_, err = mgr.RotatePeerKeyWithOverlap("n", k1, 100*time.Second)
	require.NoError(t, err)
	// Advance 10s, then rotate again: k1 lingers with a LATER expiry than k0.
	now = now.Add(10 * time.Second)
	st, err := mgr.RotatePeerKeyWithOverlap("n", k2, 100*time.Second)
	require.NoError(t, err)

	// k0 expires at t0+100, k1 expires at t0+110. Current is k2.
	require.Equal(t, []string{k2, k1, k0}, st.ValidKeys)
	assert.Equal(t, k2, st.Current)
}
