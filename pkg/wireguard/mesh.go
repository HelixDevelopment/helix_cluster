package wireguard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
)

// MeshCoordinator coordinates WireGuard peers with SWIM membership.
type MeshCoordinator struct {
	mu        sync.RWMutex
	wgManager *Manager
	swimProto *swim.Protocol

	// Auto-sync config
	syncInterval time.Duration
	cancel       context.CancelFunc
}

// NewMeshCoordinator creates a coordinator.
func NewMeshCoordinator(wg *Manager, swim *swim.Protocol) *MeshCoordinator {
	return &MeshCoordinator{
		wgManager:    wg,
		swimProto:    swim,
		syncInterval: 30 * time.Second,
	}
}

// Start begins automatic peer sync with SWIM membership.
func (mc *MeshCoordinator) Start() error {
	mc.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	mc.mu.Lock()
	mc.cancel = cancel
	mc.mu.Unlock()

	go func() {
		ticker := time.NewTicker(mc.syncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = mc.SyncNow()
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

// Stop stops automatic sync.
func (mc *MeshCoordinator) Stop() error {
	mc.mu.Lock()
	cancel := mc.cancel
	mc.cancel = nil
	mc.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// SyncNow performs a one-time sync of SWIM members to WireGuard peers.
func (mc *MeshCoordinator) SyncNow() error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.swimProto == nil {
		return fmt.Errorf("swim protocol is nil")
	}
	if mc.wgManager == nil {
		return fmt.Errorf("wireguard manager is nil")
	}

	// Retrieve current SWIM members.
	members := mc.swimProto.Members()
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m.ID] = struct{}{}
		if !m.IsHealthy() {
			continue
		}

		// Extract WireGuard public key and endpoint from metadata.
		wgPubKey, ok := m.Metadata["wg_public_key"]
		if !ok || wgPubKey == "" {
			continue
		}
		endpoint := m.Metadata["wg_endpoint"]
		allowedIPs := []string{m.Address}
		if cidr, ok := m.Metadata["wg_allowed_ips"]; ok && cidr != "" {
			allowedIPs = append(allowedIPs, cidr)
		}

		peer := &PeerConfig{
			PublicKey:           wgPubKey,
			AllowedIPs:          allowedIPs,
			Endpoint:            endpoint,
			PersistentKeepalive: 25,
		}
		_ = mc.wgManager.AddPeer(peer)
	}

	// Remove peers that are no longer in SWIM.
	peers, _ := mc.wgManager.ListPeers()
	for _, p := range peers {
		if _, ok := memberSet[p.PublicKey]; !ok {
			_ = mc.wgManager.RemovePeer(p.PublicKey)
		}
	}

	return nil
}

// OnMemberJoined is called when SWIM detects a new member.
func (mc *MeshCoordinator) OnMemberJoined(member *swim.Member) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.wgManager == nil || member == nil {
		return
	}

	wgPubKey, ok := member.Metadata["wg_public_key"]
	if !ok || wgPubKey == "" {
		return
	}

	peer := &PeerConfig{
		PublicKey:           wgPubKey,
		AllowedIPs:          []string{member.Address},
		Endpoint:            member.Metadata["wg_endpoint"],
		PersistentKeepalive: 25,
	}
	if cidr, ok := member.Metadata["wg_allowed_ips"]; ok && cidr != "" {
		peer.AllowedIPs = append(peer.AllowedIPs, cidr)
	}
	_ = mc.wgManager.AddPeer(peer)
}

// OnMemberLeft is called when SWIM detects a member departure.
func (mc *MeshCoordinator) OnMemberLeft(memberID string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.wgManager == nil {
		return
	}

	// memberID may be the public key or an internal ID.
	// Attempt removal by memberID; if that fails, ignore.
	_ = mc.wgManager.RemovePeer(memberID)
}

// OnMemberSuspect is called when SWIM suspects a member.
func (mc *MeshCoordinator) OnMemberSuspect(memberID string) {
	// Suspected members are not immediately removed.
	// The next sync or failure confirmation will handle it.
}
