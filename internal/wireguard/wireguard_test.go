package wireguard

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgwg "github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

func TestPeerAddRemoveList(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	defer mgr.Stop()

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()

	_, pub1, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	_, pub2, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	p1 := &Peer{ID: "peer-1", PublicKey: pub1, Endpoint: "10.0.0.1:51820", AllowedIPs: []string{"10.0.0.1/32"}}
	p2 := &Peer{ID: "peer-2", PublicKey: pub2, Endpoint: "10.0.0.2:51820", AllowedIPs: []string{"10.0.0.2/32"}}

	err = mc.AddPeer(ctx, p1)
	require.NoError(t, err)
	err = mc.AddPeer(ctx, p2)
	require.NoError(t, err)

	peers := mc.ListPeers()
	require.Len(t, peers, 2)

	err = mc.RemovePeer(ctx, "peer-1")
	require.NoError(t, err)

	peers = mc.ListPeers()
	require.Len(t, peers, 1)
	assert.Equal(t, "peer-2", peers[0].ID)

	err = mc.RemovePeer(ctx, "peer-1")
	assert.Error(t, err)
}

func TestMeshSync(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	defer mgr.Stop()

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()

	_, pub1, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	p1 := &Peer{ID: "peer-1", PublicKey: pub1, Endpoint: "10.0.0.1:51820", AllowedIPs: []string{"10.0.0.1/32"}}
	err = mc.AddPeer(ctx, p1)
	require.NoError(t, err)

	err = mc.SyncMesh(ctx)
	require.NoError(t, err)

	mgrPeers, err := mgr.ListPeers()
	require.NoError(t, err)
	require.Len(t, mgrPeers, 1)
	assert.Equal(t, pub1, mgrPeers[0].PublicKey)
}

func TestConcurrentPeerOperations(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	defer mgr.Stop()

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, pub, err := pkgwg.GenerateKeyPair()
			if err != nil {
				return
			}
			peer := &Peer{
				ID:         string(rune('a' + i%26)),
				PublicKey:  pub,
				Endpoint:   "10.0.0.1:51820",
				AllowedIPs: []string{"10.0.0.1/32"},
			}
			_ = mc.AddPeer(ctx, peer)
		}(i)
	}
	wg.Wait()

	peers := mc.ListPeers()
	assert.GreaterOrEqual(t, len(peers), 1)
}

func TestConfigGeneration(t *testing.T) {
	cfg := &LocalConfig{
		PrivateKey: "priv",
		ListenPort: 51820,
		Peers: []Peer{
			{PublicKey: "pub1", Endpoint: "1.2.3.4:51820", AllowedIPs: []string{"10.0.0.1/32"}, PersistentKeepalive: 25},
		},
	}

	ini, err := ConfigToINI(cfg)
	require.NoError(t, err)
	assert.Contains(t, ini, "[Interface]")
	assert.Contains(t, ini, "PrivateKey = priv")
	assert.Contains(t, ini, "ListenPort = 51820")
	assert.Contains(t, ini, "[Peer]")
	assert.Contains(t, ini, "PublicKey = pub1")
	assert.Contains(t, ini, "Endpoint = 1.2.3.4:51820")
	assert.Contains(t, ini, "AllowedIPs = 10.0.0.1/32")
	assert.Contains(t, ini, "PersistentKeepalive = 25")
}

func TestConfigToININil(t *testing.T) {
	_, err := ConfigToINI(nil)
	assert.Error(t, err)
}

func TestMonitorLifecycle(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	defer mgr.Stop()

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()

	_, pub1, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	p1 := &Peer{ID: "peer-1", PublicKey: pub1, Endpoint: "10.0.0.1:51820", AllowedIPs: []string{"10.0.0.1/32"}}
	require.NoError(t, mc.AddPeer(ctx, p1))

	var disconnected []string
	var mu sync.Mutex

	monitor := NewMeshMonitor(mc,
		WithMonitorInterval(50*time.Millisecond),
		WithHandshakeChecker(func(peerID string, peer *Peer) bool {
			return peerID != "peer-1" // simulate peer-1 as disconnected
		}),
		WithOnDisconnect(func(peerID string) {
			mu.Lock()
			defer mu.Unlock()
			disconnected = append(disconnected, peerID)
		}),
	)

	err = monitor.Start(ctx)
	require.NoError(t, err)

	// Wait for at least one check cycle.
	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	assert.Contains(t, disconnected, "peer-1")
	mu.Unlock()

	assert.False(t, monitor.IsHealthy())
	assert.Contains(t, monitor.DisconnectedPeers(), "peer-1")

	err = monitor.Stop()
	require.NoError(t, err)
}

func TestMonitorStartStopIdempotent(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	defer mgr.Stop()

	mc := NewMeshCoordinator(mgr)
	monitor := NewMeshMonitor(mc, WithMonitorInterval(50*time.Millisecond))

	ctx := context.Background()
	require.NoError(t, monitor.Start(ctx))
	time.Sleep(30 * time.Millisecond)
	require.NoError(t, monitor.Stop())
	require.NoError(t, monitor.Stop()) // idempotent
}

func TestGetPeerConfig(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	defer mgr.Stop()

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()

	_, pub1, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)

	p1 := &Peer{ID: "peer-1", PublicKey: pub1, Endpoint: "10.0.0.1:51820", AllowedIPs: []string{"10.0.0.1/32"}, PersistentKeepalive: 25}
	require.NoError(t, mc.AddPeer(ctx, p1))

	pc, err := mc.GetPeerConfig("peer-1")
	require.NoError(t, err)
	assert.Equal(t, pub1, pc.PublicKey)
	assert.Equal(t, "10.0.0.1:51820", pc.Endpoint)
	assert.Equal(t, []string{"10.0.0.1/32"}, pc.AllowedIPs)
	assert.Equal(t, 25, pc.PersistentKeepalive)

	_, err = mc.GetPeerConfig("missing")
	assert.Error(t, err)
}

func TestGenerateKeyPair(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	require.NoError(t, err)
	assert.NotEmpty(t, priv)
	assert.NotEmpty(t, pub)
	assert.NotEqual(t, priv, pub)
}
