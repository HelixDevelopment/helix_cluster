package wireguard

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgwg "github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// newNoOpCoordinator returns a coordinator backed by a real (NoOp) manager so
// AddPeer exercises the actual key/CIDR validation path in pkg/wireguard.
func newNoOpCoordinator(t *testing.T) *MeshCoordinator {
	t.Helper()
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Stop() })
	return NewMeshCoordinator(mgr)
}

// TestAddPeerManagerRejectionNoStateLeak proves the rollback fix: when the
// underlying manager rejects a peer that PASSED Peer.Validate, the coordinator
// must NOT record it in peers/configs. We use an endpoint whose port is out of
// range (99999) — valid host:port syntax with a numeric port (so Validate's
// strconv.Atoi accepts it) that fails the manager's net.ResolveUDPAddr with no
// DNS lookup. This isolates the map-write ordering as the
// only thing preventing a leak. Before the fix the peer was inserted into the
// maps before the manager call, so ListPeers/GetPeerConfig advertised a peer
// that was never installed in the device.
//
// Mutation that fails this test: in AddPeer, move `mc.peers[peer.ID] = peer`
// and `mc.configs[peer.ID] = pkgPeer` back above the `mc.manager.AddPeer` call.
func TestAddPeerManagerRejectionNoStateLeak(t *testing.T) {
	mc := newNoOpCoordinator(t)
	ctx := context.Background()

	_, pub, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	bad := &Peer{
		ID:         "bad",
		PublicKey:  pub,
		Endpoint:   "1.2.3.4:99999", // syntactically valid, port out of range
		AllowedIPs: []string{"10.0.0.9/32"},
	}
	// Sanity: the peer is syntactically valid, so the leak guard is the only
	// line of defense.
	require.NoError(t, bad.Validate())

	require.Error(t, mc.AddPeer(ctx, bad), "manager must reject unresolvable endpoint")

	assert.Empty(t, mc.ListPeers(), "rejected peer must not appear in ListPeers")
	_, cfgErr := mc.GetPeerConfig("bad")
	assert.Error(t, cfgErr, "rejected peer must not have a cached config")
}

// TestAddPeerRekeyEvictsStaleManagerPeer proves that re-adding the same peer ID
// with a new public key evicts the old key from the manager, instead of
// leaving an orphaned peer in the device until the next SyncMesh.
//
// Mutation that fails this test: delete the `mc.manager.RemovePeer(stalePubKey)`
// call in AddPeer (the stale key then lingers and mgr.ListPeers returns 2).
func TestAddPeerRekeyEvictsStaleManagerPeer(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Stop() })

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()

	_, pubOld, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	_, pubNew, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "node", PublicKey: pubOld, AllowedIPs: []string{"10.0.0.1/32"}}))
	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "node", PublicKey: pubNew, AllowedIPs: []string{"10.0.0.1/32"}}))

	// Coordinator tracks exactly one peer for the ID, under the new key.
	require.Len(t, mc.ListPeers(), 1)
	pc, err := mc.GetPeerConfig("node")
	require.NoError(t, err)
	assert.Equal(t, pubNew, pc.PublicKey)

	// The manager (device) must hold only the new key — the old one is evicted.
	mgrPeers, err := mgr.ListPeers()
	require.NoError(t, err)
	require.Len(t, mgrPeers, 1)
	assert.Equal(t, pubNew, mgrPeers[0].PublicKey)
}

// TestAddPeerRekeyNilManagerNoPanic proves the rekey path is safe on a
// coordinator with no underlying manager (the no-device path). Re-adding a peer
// ID under a new public key sets stalePubKey != "", and before the fix the
// unconditional `mc.manager.RemovePeer(stalePubKey)` nil-dereferenced. The
// coordinator must instead complete without panic and advertise the new key.
//
// Mutation that fails this test: drop the `mc.manager != nil` guard from the
// stale-key eviction block in AddPeer (re-introducing the nil-pointer
// dereference) — the second AddPeer then panics and the test fails.
func TestAddPeerRekeyNilManagerNoPanic(t *testing.T) {
	mc := NewMeshCoordinator(nil)
	ctx := context.Background()

	_, pubOld, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	_, pubNew, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "node", PublicKey: pubOld, AllowedIPs: []string{"10.0.0.1/32"}}))
	// This second add triggers the stale-key eviction branch with a nil manager.
	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "node", PublicKey: pubNew, AllowedIPs: []string{"10.0.0.1/32"}}))

	require.Len(t, mc.ListPeers(), 1)
	pc, err := mc.GetPeerConfig("node")
	require.NoError(t, err)
	assert.Equal(t, pubNew, pc.PublicKey, "rekey must advertise the new public key on the no-device path")
}

// TestPeerValidate covers Peer.Validate accept/reject decisions. Each subcase
// names the field that makes a valid peer invalid.
//
// Mutation that fails this test: change `len(p.AllowedIPs) == 0` to a no-op
// (e.g. always pass) — the "no allowed IPs" case would then wrongly succeed.
func TestPeerValidate(t *testing.T) {
	_, pub, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	good := func() *Peer {
		return &Peer{ID: "p", PublicKey: pub, Endpoint: "1.2.3.4:51820", AllowedIPs: []string{"10.0.0.1/32"}, PersistentKeepalive: 25}
	}

	require.NoError(t, good().Validate())

	cases := []struct {
		name   string
		mutate func(*Peer)
	}{
		{"empty ID", func(p *Peer) { p.ID = "" }},
		{"bad public key", func(p *Peer) { p.PublicKey = "###" }},
		{"no allowed IPs", func(p *Peer) { p.AllowedIPs = nil }},
		{"bad allowed IP", func(p *Peer) { p.AllowedIPs = []string{"10.0.0.1"} }},
		{"bad endpoint host", func(p *Peer) { p.Endpoint = ":51820" }},
		{"bad endpoint port", func(p *Peer) { p.Endpoint = "1.2.3.4:wat" }},
		{"negative keepalive", func(p *Peer) { p.PersistentKeepalive = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := good()
			tc.mutate(p)
			assert.Error(t, p.Validate())
		})
	}

	var nilPeer *Peer
	assert.Error(t, nilPeer.Validate())
}

// TestLocalConfigValidate exercises private-key, port-range, per-peer and
// duplicate-ID validation of a LocalConfig.
//
// Mutation that fails this test: remove the duplicate-ID `seen` check in
// LocalConfig.Validate — the "duplicate peer ID" case would then pass.
func TestLocalConfigValidate(t *testing.T) {
	priv, pub, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	valid := &LocalConfig{
		PrivateKey: priv,
		ListenPort: 51820,
		Peers:      []Peer{{ID: "a", PublicKey: pub, AllowedIPs: []string{"10.0.0.1/32"}}},
	}
	require.NoError(t, valid.Validate())

	// Empty private key is allowed (key-less render).
	noKey := &LocalConfig{ListenPort: 51820, Peers: valid.Peers}
	assert.NoError(t, noKey.Validate())

	assert.Error(t, (&LocalConfig{PrivateKey: "bad"}).Validate())
	assert.Error(t, (&LocalConfig{ListenPort: 70000}).Validate())
	assert.Error(t, (&LocalConfig{ListenPort: -1}).Validate())

	dup := &LocalConfig{
		ListenPort: 51820,
		Peers: []Peer{
			{ID: "x", PublicKey: pub, AllowedIPs: []string{"10.0.0.1/32"}},
			{ID: "x", PublicKey: pub, AllowedIPs: []string{"10.0.0.2/32"}},
		},
	}
	assert.Error(t, dup.Validate())

	var nilCfg *LocalConfig
	assert.Error(t, nilCfg.Validate())
}

// TestConfigToINIMultiPeerAndOmission proves ConfigToINI renders multiple peers
// in order and omits optional fields when unset (no empty ListenPort line, no
// PersistentKeepalive line when zero).
//
// Mutation that fails this test: change the `peer.PersistentKeepalive > 0`
// guard in ConfigToINI to always emit the line — the zero-keepalive peer would
// then wrongly include `PersistentKeepalive = 0`.
func TestConfigToINIMultiPeerAndOmission(t *testing.T) {
	cfg := &LocalConfig{
		PrivateKey: "priv",
		// ListenPort intentionally 0 -> must be omitted.
		Peers: []Peer{
			{PublicKey: "pubA", AllowedIPs: []string{"10.0.0.1/32"}}, // keepalive 0 -> omitted
			{PublicKey: "pubB", Endpoint: "5.6.7.8:51820", AllowedIPs: []string{"10.0.0.2/32"}, PersistentKeepalive: 15},
		},
	}
	ini, err := ConfigToINI(cfg)
	require.NoError(t, err)

	assert.NotContains(t, ini, "ListenPort", "zero ListenPort must be omitted")
	assert.NotContains(t, ini, "PersistentKeepalive = 0")
	assert.Contains(t, ini, "PersistentKeepalive = 15")

	// Peer order is preserved: pubA's block precedes pubB's.
	assert.Less(t, strings.Index(ini, "pubA"), strings.Index(ini, "pubB"))
	assert.Equal(t, 2, strings.Count(ini, "[Peer]"))
}

// TestRemovePeerThenGetConfig proves RemovePeer purges the cached config, not
// just the peer entry — GetPeerConfig must fail afterward.
//
// Mutation that fails this test: delete `delete(mc.configs, peerID)` in
// RemovePeer — GetPeerConfig would then still return the stale config.
func TestRemovePeerPurgesConfig(t *testing.T) {
	mc := newNoOpCoordinator(t)
	ctx := context.Background()

	_, pub, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "z", PublicKey: pub, AllowedIPs: []string{"10.0.0.1/32"}}))

	_, err = mc.GetPeerConfig("z")
	require.NoError(t, err)

	require.NoError(t, mc.RemovePeer(ctx, "z"))
	_, err = mc.GetPeerConfig("z")
	assert.Error(t, err, "config cache must be purged on RemovePeer")
}

// TestSyncMeshRemovesOrphanedManagerPeer proves SyncMesh removes a peer that
// exists in the manager/device but is no longer part of the coordinator's mesh.
//
// Mutation that fails this test: delete the orphan-removal loop in SyncMesh
// (the `for _, mp := range mgrPeers` block) — the orphan would survive.
func TestSyncMeshRemovesOrphanedManagerPeer(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Stop() })

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()

	_, pubMesh, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	_, pubOrphan, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "mesh", PublicKey: pubMesh, AllowedIPs: []string{"10.0.0.1/32"}}))

	// Inject an orphan directly into the manager (not tracked by the coordinator).
	require.NoError(t, mgr.AddPeer(&pkgwg.PeerConfig{PublicKey: pubOrphan, AllowedIPs: []string{"10.0.0.99/32"}}))

	before, err := mgr.ListPeers()
	require.NoError(t, err)
	require.Len(t, before, 2)

	require.NoError(t, mc.SyncMesh(ctx))

	after, err := mgr.ListPeers()
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, pubMesh, after[0].PublicKey)
}
