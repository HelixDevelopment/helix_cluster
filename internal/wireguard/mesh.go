package wireguard

import (
	"context"
	"fmt"
	"sync"

	pkgwg "github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// MeshCoordinator manages WireGuard peer configurations across the cluster.
type MeshCoordinator struct {
	mu       sync.RWMutex
	peers    map[string]*Peer          // keyed by peer ID
	manager  *pkgwg.Manager            // underlying pkg wireguard manager
	configs  map[string]*pkgwg.PeerConfig // cached peer configs keyed by peer ID
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
	if peer == nil {
		return fmt.Errorf("peer is nil")
	}
	if peer.ID == "" {
		return fmt.Errorf("peer ID is empty")
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.peers[peer.ID] = peer

	pkgPeer := &pkgwg.PeerConfig{
		PublicKey:           peer.PublicKey,
		AllowedIPs:          peer.AllowedIPs,
		Endpoint:            peer.Endpoint,
		PersistentKeepalive: peer.PersistentKeepalive,
	}
	mc.configs[peer.ID] = pkgPeer

	if mc.manager != nil {
		if err := mc.manager.AddPeer(pkgPeer); err != nil {
			return fmt.Errorf("failed to add peer to manager: %w", err)
		}
	}

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
