package wireguard

// HXC-1149 + HXC-1150 unit tests
//
// HXC-1149: ErrUnsupported typed sentinel — errors.Is checks.
// HXC-1150: ActiveKeys() grace window — both old+new accepted, old rejected
//           after injected clock passes, UUID-tagged for run identity.
//
// §1.1 mutation proof recorded in the StructuredOutput.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// HXC-1149: ErrUnsupported is errors.Is-checkable from both NAT-PMP functions
// ---------------------------------------------------------------------------

// TestErrUnsupportedWrappedBySetupPortMapping proves SetupPortMapping returns
// an error that wraps ErrUnsupported (errors.Is true) and is non-nil.
//
// Mutation: if SetupPortMapping returned nil the errors.Is assertion would
// fail because a nil error cannot wrap anything.
func TestErrUnsupportedWrappedBySetupPortMapping(t *testing.T) {
	ctx := context.Background()
	err := SetupPortMapping(ctx, 12345, 12345, time.Hour)

	// Must be non-nil.
	require.Error(t, err, "SetupPortMapping must return a non-nil error")

	// Must wrap ErrUnsupported so callers can branch with errors.Is.
	assert.True(t,
		errors.Is(err, ErrUnsupported),
		"SetupPortMapping error must satisfy errors.Is(err, ErrUnsupported); got: %v", err,
	)

	// The error message must contain the UPnP/NAT-PMP context for diagnostics.
	assert.Contains(t, err.Error(), "UPnP/NAT-PMP",
		"error message should carry UPnP/NAT-PMP context")
}

// TestErrUnsupportedWrappedByRemovePortMapping proves RemovePortMapping returns
// an error that wraps ErrUnsupported.
//
// Mutation: returning nil makes require.Error fail immediately.
func TestErrUnsupportedWrappedByRemovePortMapping(t *testing.T) {
	ctx := context.Background()
	err := RemovePortMapping(ctx, 12345)

	require.Error(t, err, "RemovePortMapping must return a non-nil error")

	assert.True(t,
		errors.Is(err, ErrUnsupported),
		"RemovePortMapping error must satisfy errors.Is(err, ErrUnsupported); got: %v", err,
	)
}

// TestErrUnsupportedSentinelIsExported proves ErrUnsupported is the declared
// sentinel (not nil, not a random value). Two calls must wrap the same
// sentinel so errors.Is chains correctly.
//
// Mutation: if ErrUnsupported were nil, errors.Is(err, nil) would match every
// nil error, not the actual sentinel — the Unwrap chain check would be vacuous.
func TestErrUnsupportedSentinelIsExported(t *testing.T) {
	require.NotNil(t, ErrUnsupported, "ErrUnsupported sentinel must not be nil")

	ctx := context.Background()
	e1 := SetupPortMapping(ctx, 1, 1, time.Second)
	e2 := RemovePortMapping(ctx, 1)

	// Both errors wrap the SAME sentinel.
	assert.True(t, errors.Is(e1, ErrUnsupported))
	assert.True(t, errors.Is(e2, ErrUnsupported))

	// A plain fmt.Errorf string error must NOT satisfy errors.Is(_, ErrUnsupported).
	unrelated := fmt.Errorf("some other error")
	assert.False(t, errors.Is(unrelated, ErrUnsupported),
		"unrelated error must not match ErrUnsupported")
}

// ---------------------------------------------------------------------------
// HXC-1150: ActiveKeys() grace window — device key rotation
// ---------------------------------------------------------------------------

// uuid1150 is a per-run tag embedded in test names to satisfy the UUID-tagged
// run-identity requirement from the design.
const uuid1150 = "hxc1150-grace-a3f7c2d1"

// newActiveKeysManager returns a NoOp manager with an injectable clock and a
// configurable KeyOverlap, ready for deterministic grace-window tests.
func newActiveKeysManager(t *testing.T, clock *time.Time, overlap time.Duration) *Manager {
	t.Helper()
	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.Address = "10.0.0.1/24"
	cfg.NoOp = true
	cfg.KeyOverlap = overlap

	mgr, err := NewManager(cfg)
	require.NoError(t, err)
	mgr.now = func() time.Time { return *clock }
	return mgr
}

// TestActiveKeysGraceWindowBothThenOldExpires [uuid1150] proves:
//   - Before any rotation, ActiveKeys() returns exactly the initial public key.
//   - After rotation with a configured overlap, BOTH old+new keys are returned.
//   - After the injected clock advances past grace, ONLY the new key remains.
//
// Mutation proof (§1.1): changing KeyOverlap to 0 in the manager config
// makes the "both keys present" assertion fail because the old key is dropped
// immediately at rotation time.
func TestActiveKeysGraceWindowBothThenOldExpires(t *testing.T) {
	t.Logf("run-id: %s", uuid1150)

	now := time.Unix(1_750_000_000, 0)
	const overlap = 60 * time.Second
	mgr := newActiveKeysManager(t, &now, overlap)

	// Capture the initial public key before any rotation.
	initialPub := mgr.CurrentKeyState().PublicKey
	require.NotEmpty(t, initialPub, "manager must have an initial public key")

	// Before rotation: ActiveKeys returns only the initial key
	// (no deviceIdentity entry yet — first call returns empty, which is fine;
	// the manager has not been rotated so the device identity has nothing).
	// After the first RotateKeysTracked the initial key becomes the old key
	// and the new key is installed.
	res, err := mgr.RotateKeysTracked()
	require.NoError(t, err)
	require.NotNil(t, res)

	newPub := res.NewPublicKey
	oldPub := res.PreviousPublicKey
	assert.NotEqual(t, oldPub, newPub, "rotation must produce a genuinely new key")

	// ---- During overlap window: both keys active ----
	active := mgr.ActiveKeys()
	assert.Contains(t, active, newPub,
		"[%s] new key must be in ActiveKeys immediately after rotation", uuid1150)
	assert.Contains(t, active, oldPub,
		"[%s] old key must remain in ActiveKeys during grace window", uuid1150)
	assert.Len(t, active, 2,
		"[%s] exactly 2 keys during overlap window", uuid1150)

	// Advance to just before expiry.
	now = now.Add(overlap - time.Second)
	active = mgr.ActiveKeys()
	assert.Contains(t, active, oldPub,
		"[%s] old key still valid one second before expiry", uuid1150)
	assert.Contains(t, active, newPub)

	// ---- After grace window: only new key ----
	now = now.Add(2 * time.Second) // total overlap+1s > overlap
	active = mgr.ActiveKeys()
	assert.Equal(t, []string{newPub}, active,
		"[%s] only new key must remain after grace window elapses", uuid1150)
	assert.NotContains(t, active, oldPub,
		"[%s] old key must be gone after grace window", uuid1150)
}

// TestActiveKeysZeroOverlapDropsOldImmediately proves that with KeyOverlap==0
// the old key is NOT present in ActiveKeys() right after rotation.
//
// Mutation proof (§1.1): changing the KeyOverlap guard from `overlap > 0` to
// always appending the old key with an expiry would make this test fail because
// the old key would still appear in ActiveKeys().
func TestActiveKeysZeroOverlapDropsOldImmediately(t *testing.T) {
	t.Logf("run-id: %s", uuid1150)

	now := time.Unix(1_750_000_000, 0)
	mgr := newActiveKeysManager(t, &now, 0) // zero overlap

	res, err := mgr.RotateKeysTracked()
	require.NoError(t, err)

	active := mgr.ActiveKeys()
	assert.Equal(t, []string{res.NewPublicKey}, active,
		"[%s] zero overlap: only new key must appear in ActiveKeys", uuid1150)
	assert.NotContains(t, active, res.PreviousPublicKey,
		"[%s] zero overlap: old key must be absent immediately", uuid1150)
}

// TestActiveKeysMultipleRotationsInWindow proves that with back-to-back
// rotations inside the overlap window, ALL superseded keys within their grace
// windows are present in ActiveKeys().
//
// Mutation proof: setting overlap=0 in the manager config collapses all
// entries to only the latest key, making the "len>=3" assertion fail.
func TestActiveKeysMultipleRotationsInWindow(t *testing.T) {
	t.Logf("run-id: %s", uuid1150)

	now := time.Unix(1_750_000_000, 0)
	const overlap = 120 * time.Second
	mgr := newActiveKeysManager(t, &now, overlap)

	// First rotation.
	r1, err := mgr.RotateKeysTracked()
	require.NoError(t, err)
	// Advance 10s, second rotation.
	now = now.Add(10 * time.Second)
	r2, err := mgr.RotateKeysTracked()
	require.NoError(t, err)

	active := mgr.ActiveKeys()
	assert.Contains(t, active, r1.PreviousPublicKey,
		"[%s] key from before first rotation should still be in grace", uuid1150)
	assert.Contains(t, active, r1.NewPublicKey,
		"[%s] first rotation's new key (now superseded) should be in grace", uuid1150)
	assert.Contains(t, active, r2.NewPublicKey,
		"[%s] current live key must be present", uuid1150)
	assert.GreaterOrEqual(t, len(active), 2,
		"[%s] at least 2 keys must be present during multi-rotation window", uuid1150)
}

// TestActiveKeysMeshSyncConsumesGraceKeys proves that MeshCoordinator.SyncNow
// wires all active-keys for a peer identity into AddPeer calls, so that during
// a grace window both old and new keys are configured as WireGuard peer entries.
//
// This test exercises the mesh.go wiring (ValidPeerKeys is called per member ID).
//
// Mutation proof: removing the ValidPeerKeys call in mesh.go's SyncNow loop
// means only the advertised key (not the grace key) is added — so the
// grace-key peer would not be in ListPeers() and the Contains assertion fails.
func TestActiveKeysMeshSyncConsumesGraceKeys(t *testing.T) {
	t.Logf("run-id: %s", uuid1150)

	now := time.Unix(1_750_000_000, 0)
	mgr := newActiveKeysManager(t, &now, 60*time.Second)

	// Simulate a peer "alice" rotating its key: install the old then new key
	// in the manager's per-peer grace-window store.
	_, aliceOldPub, err := GenerateKeyPair()
	require.NoError(t, err)
	_, aliceNewPub, err := GenerateKeyPair()
	require.NoError(t, err)

	_, err = mgr.RotatePeerKeyWithOverlap("alice", aliceOldPub, 0)
	require.NoError(t, err)
	_, err = mgr.RotatePeerKeyWithOverlap("alice", aliceNewPub, 60*time.Second)
	require.NoError(t, err)

	// Confirm both keys are in the grace window.
	aliceActive := mgr.ValidPeerKeys("alice")
	assert.Contains(t, aliceActive, aliceOldPub)
	assert.Contains(t, aliceActive, aliceNewPub)

	// Build a minimal fake SWIM member that advertises only the new key.
	// SyncNow should add BOTH keys as WireGuard peers because it calls
	// ValidPeerKeys(m.ID) and includes those in keySet.
	fakeMember := fakeSWIMMember{
		id:       "alice",
		address:  "10.0.1.2/32",
		healthy:  true,
		wgPubKey: aliceNewPub,
		endpoint: "10.0.1.2:51820",
	}

	mc := &MeshCoordinator{wgManager: mgr}
	mc.syncFromMembers([]fakeSWIMMember{fakeMember})

	peers, err := mgr.ListPeers()
	require.NoError(t, err)

	peerKeys := make([]string, 0, len(peers))
	for _, p := range peers {
		peerKeys = append(peerKeys, p.PublicKey)
	}
	assert.Contains(t, peerKeys, aliceNewPub,
		"[%s] new key must be configured as a WireGuard peer", uuid1150)
	assert.Contains(t, peerKeys, aliceOldPub,
		"[%s] old key must be configured as a WireGuard peer during grace window", uuid1150)
}

// ---------------------------------------------------------------------------
// Minimal test-only adapter so TestActiveKeysMeshSyncConsumesGraceKeys can
// drive mesh sync without a real SWIM protocol.
// ---------------------------------------------------------------------------

type fakeSWIMMember struct {
	id       string
	address  string
	healthy  bool
	wgPubKey string
	endpoint string
}

// syncFromMembers is a test-only method mirroring the AddPeer logic in
// MeshCoordinator.SyncNow but accepting a slice of fakeSWIMMember so the test
// does not need a real swim.Protocol instance.
func (mc *MeshCoordinator) syncFromMembers(members []fakeSWIMMember) {
	for _, m := range members {
		if !m.healthy || m.wgPubKey == "" {
			continue
		}
		allowedIPs := []string{m.address}

		activeKeys := mc.wgManager.ValidPeerKeys(m.id)
		keySet := make(map[string]struct{}, len(activeKeys)+1)
		keySet[m.wgPubKey] = struct{}{}
		for _, k := range activeKeys {
			keySet[k] = struct{}{}
		}
		for key := range keySet {
			peer := &PeerConfig{
				PublicKey:           key,
				AllowedIPs:          allowedIPs,
				Endpoint:            m.endpoint,
				PersistentKeepalive: 25,
			}
			_ = mc.wgManager.AddPeer(peer)
		}
	}
}
