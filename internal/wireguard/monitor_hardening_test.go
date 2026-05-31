package wireguard

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgwg "github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// TestMonitorReconnectTransition proves the disconnect->reconnect state machine:
// once a peer flips from unhealthy back to healthy, onReconnect fires, the peer
// leaves DisconnectedPeers, and IsHealthy returns true.
//
// Mutation that fails this test: in checkAll, remove the
// `delete(m.disconnected, peer.ID)` line in the reconnect branch — the peer
// would stay in disconnected forever and onReconnect would never fire.
func TestMonitorReconnectTransition(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Stop() })

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()
	_, pub, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "peer-1", PublicKey: pub, AllowedIPs: []string{"10.0.0.1/32"}}))

	var healthy atomic.Bool // starts false => disconnected
	var reconnects int32
	var disconnects int32

	monitor := NewMeshMonitor(mc,
		WithMonitorInterval(20*time.Millisecond),
		WithHandshakeChecker(func(peerID string, peer *Peer) bool {
			return healthy.Load()
		}),
		WithOnDisconnect(func(peerID string) { atomic.AddInt32(&disconnects, 1) }),
		WithOnReconnect(func(peerID string) { atomic.AddInt32(&reconnects, 1) }),
	)

	require.NoError(t, monitor.Start(ctx))
	t.Cleanup(func() { _ = monitor.Stop() })

	// Phase 1: disconnected.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&disconnects) >= 1 && !monitor.IsHealthy()
	}, time.Second, 10*time.Millisecond)
	assert.Contains(t, monitor.DisconnectedPeers(), "peer-1")

	// Phase 2: flip healthy; expect a reconnect transition.
	healthy.Store(true)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reconnects) >= 1 && monitor.IsHealthy()
	}, time.Second, 10*time.Millisecond)
	assert.NotContains(t, monitor.DisconnectedPeers(), "peer-1")
}

// TestMonitorCleansUpRemovedPeer proves a peer that disappears from the
// coordinator is dropped from the disconnected set (no leak / no stale report).
//
// Mutation that fails this test: remove the "Clean up peers that have been
// removed from the coordinator" loop in checkAll — peer-gone would remain in
// DisconnectedPeers after removal.
func TestMonitorCleansUpRemovedPeer(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Stop() })

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()
	_, pub, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "peer-gone", PublicKey: pub, AllowedIPs: []string{"10.0.0.1/32"}}))

	monitor := NewMeshMonitor(mc,
		WithMonitorInterval(20*time.Millisecond),
		WithHandshakeChecker(func(peerID string, peer *Peer) bool { return false }),
	)
	require.NoError(t, monitor.Start(ctx))
	t.Cleanup(func() { _ = monitor.Stop() })

	require.Eventually(t, func() bool {
		return len(monitor.DisconnectedPeers()) == 1
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, mc.RemovePeer(ctx, "peer-gone"))

	require.Eventually(t, func() bool {
		return len(monitor.DisconnectedPeers()) == 0 && monitor.IsHealthy()
	}, time.Second, 10*time.Millisecond)
}

// TestMonitorStringReflectsState proves String reports live peer and
// disconnected counts rather than a constant.
//
// Mutation that fails this test: hardcode disconnected=0 in MeshMonitor.String
// — the post-check assertion on "disconnected=1" would fail.
func TestMonitorStringReflectsState(t *testing.T) {
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Stop() })

	mc := NewMeshCoordinator(mgr)
	ctx := context.Background()
	_, pub, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	require.NoError(t, mc.AddPeer(ctx, &Peer{ID: "peer-1", PublicKey: pub, AllowedIPs: []string{"10.0.0.1/32"}}))

	monitor := NewMeshMonitor(mc,
		WithMonitorInterval(20*time.Millisecond),
		WithHandshakeChecker(func(peerID string, peer *Peer) bool { return false }),
	)

	assert.Contains(t, monitor.String(), "peers=1")
	assert.Contains(t, monitor.String(), "disconnected=0")

	require.NoError(t, monitor.Start(ctx))
	t.Cleanup(func() { _ = monitor.Stop() })

	require.Eventually(t, func() bool {
		return strings.Contains(monitor.String(), "disconnected=1")
	}, time.Second, 10*time.Millisecond)
}
