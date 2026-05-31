package wireguard

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// HandshakeChecker is the function signature used to check peer connectivity.
// Returns true if the peer is connected, false otherwise.
type HandshakeChecker func(peerID string, peer *Peer) bool

// MeshMonitor periodically checks mesh connectivity health.
type MeshMonitor struct {
	mu              sync.RWMutex
	coordinator     *MeshCoordinator
	interval        time.Duration
	checker         HandshakeChecker
	disconnected    map[string]time.Time // peer ID -> time first seen disconnected
	onDisconnect    func(peerID string)
	onReconnect     func(peerID string)
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

// MonitorOption configures a MeshMonitor.
type MonitorOption func(*MeshMonitor)

// WithMonitorInterval sets the check interval.
func WithMonitorInterval(d time.Duration) MonitorOption {
	return func(m *MeshMonitor) {
		m.interval = d
	}
}

// WithHandshakeChecker sets a custom handshake checker.
func WithHandshakeChecker(fn HandshakeChecker) MonitorOption {
	return func(m *MeshMonitor) {
		m.checker = fn
	}
}

// WithOnDisconnect sets a callback for peer disconnections.
func WithOnDisconnect(fn func(peerID string)) MonitorOption {
	return func(m *MeshMonitor) {
		m.onDisconnect = fn
	}
}

// WithOnReconnect sets a callback for peer reconnections.
func WithOnReconnect(fn func(peerID string)) MonitorOption {
	return func(m *MeshMonitor) {
		m.onReconnect = fn
	}
}

// NewMeshMonitor creates a new mesh monitor.
func NewMeshMonitor(coordinator *MeshCoordinator, opts ...MonitorOption) *MeshMonitor {
	m := &MeshMonitor{
		coordinator:  coordinator,
		interval:     5 * time.Second,
		checker:      defaultHandshakeChecker,
		disconnected: make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// defaultHandshakeChecker is a mock implementation that always reports healthy.
func defaultHandshakeChecker(peerID string, peer *Peer) bool {
	return true
}

// Start begins periodic health checks.
func (m *MeshMonitor) Start(ctx context.Context) error {
	m.Stop()

	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		// Run initial check immediately.
		m.checkAll(ctx)

		for {
			select {
			case <-ticker.C:
				m.checkAll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

// Stop halts the monitor.
func (m *MeshMonitor) Stop() error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	return nil
}

// checkAll performs connectivity checks for all peers and handles state transitions.
func (m *MeshMonitor) checkAll(ctx context.Context) {
	if m.coordinator == nil {
		return
	}

	peers := m.coordinator.ListPeers()
	seen := make(map[string]struct{}, len(peers))

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, peer := range peers {
		seen[peer.ID] = struct{}{}
		connected := m.checker(peer.ID, peer)

		if !connected {
			if _, ok := m.disconnected[peer.ID]; !ok {
				m.disconnected[peer.ID] = time.Now()
				if m.onDisconnect != nil {
					m.onDisconnect(peer.ID)
				}
			}
		} else {
			if _, wasDisconnected := m.disconnected[peer.ID]; wasDisconnected {
				delete(m.disconnected, peer.ID)
				if m.onReconnect != nil {
					m.onReconnect(peer.ID)
				}
			}
		}
	}

	// Clean up peers that have been removed from the coordinator.
	for id := range m.disconnected {
		if _, ok := seen[id]; !ok {
			delete(m.disconnected, id)
		}
	}

	// Trigger mesh re-sync if any peers are disconnected.
	if len(m.disconnected) > 0 {
		_ = m.coordinator.SyncMesh(ctx)
	}
}

// DisconnectedPeers returns the IDs of currently disconnected peers.
func (m *MeshMonitor) DisconnectedPeers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]string, 0, len(m.disconnected))
	for id := range m.disconnected {
		list = append(list, id)
	}
	return list
}

// IsHealthy returns true if all peers are connected.
func (m *MeshMonitor) IsHealthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.disconnected) == 0
}

// String returns a summary of the monitor state.
func (m *MeshMonitor) String() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fmt.Sprintf("MeshMonitor(peers=%d, disconnected=%d)", len(m.coordinator.ListPeers()), len(m.disconnected))
}
