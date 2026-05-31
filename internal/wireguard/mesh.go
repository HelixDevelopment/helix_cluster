package wireguard

import (
	"context"
	"fmt"
	"sync"

	pkgwg "github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// MeshCoordinator manages WireGuard peer configurations across the cluster.
type MeshCoordinator struct {
	mu      sync.RWMutex
	peers   map[string]*Peer             // keyed by peer ID
	manager *pkgwg.Manager               // underlying pkg wireguard manager
	configs map[string]*pkgwg.PeerConfig // cached peer configs keyed by peer ID
}

// NewMeshCoordinator creates a new mesh coordinator.
func NewMeshCoordinator(manager *pkgwg.Manager) *MeshCoordinator {
	return &MeshCoordinator{
		peers:   make(map[string]*Peer),
		manager: manager,
		configs: make(map[string]*pkgwg.PeerConfig),
	}
}

// AddPeer adds a peer to the mesh.
func (mc *MeshCoordinator) AddPeer(ctx context.Context, peer *Peer) error {
	// Validate up front so the manager==nil (no-device) path is held to the
	// same standard as the real path: an unusable peer must never enter the
	// coordinator's view of the mesh.
	if err := peer.Validate(); err != nil {
		return err
	}

	pkgPeer := &pkgwg.PeerConfig{
		PublicKey:           peer.PublicKey,
		AllowedIPs:          peer.AllowedIPs,
		Endpoint:            peer.Endpoint,
		PersistentKeepalive: peer.PersistentKeepalive,
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// If this ID already exists under a different public key, the old key must
	// be evicted from the manager, otherwise the stale peer lingers in the
	// WireGuard device (orphaned) until the next SyncMesh. Capture it before we
	// overwrite our bookkeeping.
	var stalePubKey string
	if prev, ok := mc.peers[peer.ID]; ok && prev.PublicKey != peer.PublicKey {
		stalePubKey = prev.PublicKey
	}

	// Push to the underlying manager first. If the manager rejects the peer
	// (e.g. invalid public key or AllowedIPs), we must NOT record it in our
	// maps: doing so would make ListPeers/GetPeerConfig advertise a peer that
	// is not actually present in the WireGuard device — a silent state
	// corruption that fails the end-user usability guarantee.
	if mc.manager != nil {
		if err := mc.manager.AddPeer(pkgPeer); err != nil {
			return fmt.Errorf("failed to add peer to manager: %w", err)
		}
	}

	// The new key is now installed; evict the superseded one so the device
	// does not retain a stale peer for this ID. This is only meaningful when a
	// manager is present — guarding on mc.manager != nil also avoids a nil
	// dereference on the no-device path. A failed eviction leaves an orphaned
	// peer in the device, so we surface it rather than silently swallowing it.
	if mc.manager != nil && stalePubKey != "" {
		if err := mc.manager.RemovePeer(stalePubKey); err != nil {
			return fmt.Errorf("failed to evict stale peer key for %s: %w", peer.ID, err)
		}
	}

	mc.peers[peer.ID] = peer
	mc.configs[peer.ID] = pkgPeer

	return nil
}

// RemovePeer removes a peer from the mesh by peer ID.
func (mc *MeshCoordinator) RemovePeer(ctx context.Context, peerID string) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	peer, ok := mc.peers[peerID]
	if !ok {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	delete(mc.peers, peerID)
	delete(mc.configs, peerID)

	if mc.manager != nil {
		_ = mc.manager.RemovePeer(peer.PublicKey)
	}

	return nil
}

// ListPeers returns all mesh peers.
func (mc *MeshCoordinator) ListPeers() []*Peer {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	list := make([]*Peer, 0, len(mc.peers))
	for _, p := range mc.peers {
		list = append(list, p)
	}
	return list
}

// GetPeerConfig returns the WireGuard config for a peer by peer ID.
func (mc *MeshCoordinator) GetPeerConfig(peerID string) (*pkgwg.PeerConfig, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	cfg, ok := mc.configs[peerID]
	if !ok {
		return nil, fmt.Errorf("peer config not found: %s", peerID)
	}
	return cfg, nil
}

// SyncMesh synchronizes mesh state across all peers.
func (mc *MeshCoordinator) SyncMesh(ctx context.Context) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.manager == nil {
		return nil
	}

	// Reconcile: ensure all known peers are present in the manager.
	for _, peer := range mc.peers {
		pkgPeer := &pkgwg.PeerConfig{
			PublicKey:           peer.PublicKey,
			AllowedIPs:          peer.AllowedIPs,
			Endpoint:            peer.Endpoint,
			PersistentKeepalive: peer.PersistentKeepalive,
		}
		if err := mc.manager.AddPeer(pkgPeer); err != nil {
			return fmt.Errorf("failed to sync peer %s: %w", peer.ID, err)
		}
	}

	// Retrieve current manager peers and remove any that are no longer in our mesh.
	mgrPeers, err := mc.manager.ListPeers()
	if err != nil {
		return fmt.Errorf("failed to list manager peers: %w", err)
	}

	meshPubKeys := make(map[string]struct{}, len(mc.peers))
	for _, p := range mc.peers {
		meshPubKeys[p.PublicKey] = struct{}{}
	}

	for _, mp := range mgrPeers {
		if _, ok := meshPubKeys[mp.PublicKey]; !ok {
			_ = mc.manager.RemovePeer(mp.PublicKey)
		}
	}

	return nil
}
