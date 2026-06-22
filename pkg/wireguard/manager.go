package wireguard

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Manager manages a WireGuard interface and its peers. The OS-specific data
// plane lives behind the wgBackend seam (manager_linux.go = kernel/wgctrl,
// manager_darwin.go = userspace wireguard-go + netstack); all peer-table and
// key-rotation state below is OS-neutral and shared.
type Manager struct {
	mu      sync.RWMutex
	config  *Config
	backend wgBackend
	peers   map[string]*PeerConfig // keyed by public key

	// Key rotation
	keyRotationInterval time.Duration
	keyRotationCancel   context.CancelFunc

	// Tracked key-rotation state. keyGeneration starts at 0 (the key the
	// manager was constructed with) and increments on every tracked rotation.
	keyGeneration uint64
	keyRotatedAt  time.Time

	// Per-peer key-overlap windows. Keyed by a caller-chosen peer identity,
	// each entry holds the set of public keys currently accepted for that peer
	// (the new key plus any not-yet-expired old keys). See keyrotation.go.
	peerKeys map[string][]validKey

	// now is the clock used for overlap-window expiry. It is injectable so
	// tests can drive expiry deterministically; nil means time.Now.
	now func() time.Time
}

// NewManager creates a WireGuard manager. The per-OS backend is constructed by
// newBackend (manager_linux.go / manager_darwin.go). In NoOp mode no backend is
// created: NoOp is an explicit, test-only opt-in that performs no real device
// operations on any OS. Real-operation managers always get a real backend (the
// kernel device on Linux, the userspace device on darwin) — never a stub.
func NewManager(cfg *Config) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	m := &Manager{
		config:   cfg,
		peers:    make(map[string]*PeerConfig),
		peerKeys: make(map[string][]validKey),
	}
	if cfg.NoOp {
		return m, nil
	}
	backend, err := newBackend(cfg)
	if err != nil {
		return nil, err
	}
	m.backend = backend
	return m, nil
}

// clock returns the manager's time source, defaulting to time.Now.
func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Start initializes the WireGuard interface via the per-OS backend (kernel
// device on Linux, real userspace device on darwin). In NoOp mode it does
// nothing — NoOp is the explicit test-only opt-in.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.config.NoOp {
		return nil
	}
	return m.backend.configureInterface(
		m.config.PrivateKey, m.config.Address, m.config.ListenPort, m.config.MTU)
}

// Stop tears down the interface via the backend.
func (m *Manager) Stop() error {
	m.DisableKeyRotation()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.config.NoOp {
		return nil
	}
	if err := m.backend.teardownInterface(); err != nil {
		return err
	}
	return m.backend.close()
}

// AddPeer adds or updates a peer. The peer is installed on the live device via
// the backend (real on every OS) and tracked in the in-memory peer table.
func (m *Manager) AddPeer(peer *PeerConfig) error {
	if peer == nil {
		return fmt.Errorf("peer is nil")
	}

	// Validate up front so a malformed peer never reaches the device or the peer
	// table — sink-side contract preserved from the original implementation.
	if _, err := ParseKey(peer.PublicKey); err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}
	if peer.PresharedKey != "" {
		if _, err := ParseKey(peer.PresharedKey); err != nil {
			return fmt.Errorf("invalid preshared key: %w", err)
		}
	}
	if err := validateAllowedIPs(peer.AllowedIPs); err != nil {
		return err
	}
	if err := validateOptionalEndpoint(peer.Endpoint); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.NoOp {
		if err := m.backend.configurePeer(peer); err != nil {
			return err
		}
	}

	m.peers[peer.PublicKey] = peer
	return nil
}

// RemovePeer removes a peer by public key.
func (m *Manager) RemovePeer(publicKey string) error {
	if _, err := ParseKey(publicKey); err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.NoOp {
		if err := m.backend.removePeer(publicKey); err != nil {
			return err
		}
	}

	delete(m.peers, publicKey)
	return nil
}

// ListPeers returns all configured peers.
func (m *Manager) ListPeers() ([]*PeerConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]*PeerConfig, 0, len(m.peers))
	for _, p := range m.peers {
		list = append(list, p)
	}
	return list, nil
}

// GetPeer returns a specific peer.
func (m *Manager) GetPeer(publicKey string) (*PeerConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	peer, ok := m.peers[publicKey]
	if !ok {
		return nil, fmt.Errorf("peer not found: %s", publicKey)
	}
	return peer, nil
}

// PeerHandshakeTime returns time since last handshake, read from the live
// device via the per-OS backend.
func (m *Manager) PeerHandshakeTime(publicKey string) (time.Duration, error) {
	if m.config.NoOp {
		return 0, fmt.Errorf("no-op mode: no handshake recorded")
	}
	if _, err := ParseKey(publicKey); err != nil {
		return 0, fmt.Errorf("invalid public key: %w", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.backend.peerHandshakeAge(publicKey)
}

// PeerRxTx returns bytes received/transmitted for a peer, read from the live
// device via the per-OS backend.
func (m *Manager) PeerRxTx(publicKey string) (rx, tx int64, err error) {
	if m.config.NoOp {
		return 0, 0, fmt.Errorf("no-op mode: no stats available")
	}
	if _, err := ParseKey(publicKey); err != nil {
		return 0, 0, fmt.Errorf("invalid public key: %w", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.backend.peerRxTx(publicKey)
}

// RotateKeys generates new key pair and updates interface via the backend.
func (m *Manager) RotateKeys() (newPublicKey string, err error) {
	privKeyStr, pubKeyStr, err := GenerateKeyPair()
	if err != nil {
		return "", fmt.Errorf("failed to generate key pair: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.NoOp {
		if err := m.backend.rotateDeviceKey(privKeyStr); err != nil {
			return "", fmt.Errorf("failed to rotate keys: %w", err)
		}
	}

	m.config.PrivateKey = privKeyStr
	return pubKeyStr, nil
}

// EnableKeyRotation starts periodic key rotation.
func (m *Manager) EnableKeyRotation(interval time.Duration) {
	m.DisableKeyRotation()

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.keyRotationInterval = interval
	m.keyRotationCancel = cancel
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = m.RotateKeys()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// DisableKeyRotation stops key rotation.
func (m *Manager) DisableKeyRotation() {
	m.mu.Lock()
	cancel := m.keyRotationCancel
	m.keyRotationCancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// InterfaceStats returns interface-level statistics.
func (m *Manager) InterfaceStats() (*InterfaceStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.config.NoOp {
		return &InterfaceStats{
			Name:    m.config.InterfaceName,
			Address: m.config.Address,
			Port:    m.config.ListenPort,
			Peers:   len(m.peers),
		}, nil
	}

	st, err := m.backend.deviceStats()
	if err != nil {
		return nil, err
	}

	return &InterfaceStats{
		Name:      st.Name,
		Address:   m.config.Address,
		Port:      st.ListenPort,
		PublicKey: st.PublicKey,
		Peers:     st.PeerCount,
		RxBytes:   st.RxBytes,
		TxBytes:   st.TxBytes,
	}, nil
}

// InterfaceStats holds interface-level statistics.
type InterfaceStats struct {
	Name      string
	Address   string
	Port      int
	PublicKey string
	Peers     int
	RxBytes   int64
	TxBytes   int64
}
