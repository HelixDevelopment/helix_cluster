package wireguard

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Manager manages a WireGuard interface and its peers.
type Manager struct {
	mu        sync.RWMutex
	config    *Config
	client    *wgctrl.Client
	peers     map[string]*PeerConfig // keyed by public key

	// Key rotation
	keyRotationInterval time.Duration
	keyRotationCancel   context.CancelFunc
}

// NewManager creates a WireGuard manager.
func NewManager(cfg *Config) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	client, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create wgctrl client: %w", err)
	}
	return &Manager{
		config: cfg,
		client: client,
		peers:  make(map[string]*PeerConfig),
	}, nil
}

// Start initializes the WireGuard interface.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	privKey, err := ParseKey(m.config.PrivateKey)
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}

	cfg := wgtypes.Config{
		PrivateKey:   &privKey,
		ListenPort:   &m.config.ListenPort,
		ReplacePeers: true,
	}

	if err := m.client.ConfigureDevice(m.config.InterfaceName, cfg); err != nil {
		return fmt.Errorf("failed to configure device %s: %w", m.config.InterfaceName, err)
	}
	return nil
}

// Stop tears down the interface.
func (m *Manager) Stop() error {
	m.DisableKeyRotation()

	m.mu.Lock()
	defer m.mu.Unlock()

	zeroKey := wgtypes.Key{}
	zeroPort := 0
	cfg := wgtypes.Config{
		PrivateKey:   &zeroKey,
		ListenPort:   &zeroPort,
		ReplacePeers: true,
	}

	if err := m.client.ConfigureDevice(m.config.InterfaceName, cfg); err != nil {
		return fmt.Errorf("failed to tear down device %s: %w", m.config.InterfaceName, err)
	}
	return m.client.Close()
}

// AddPeer adds or updates a peer.
func (m *Manager) AddPeer(peer *PeerConfig) error {
	if peer == nil {
		return fmt.Errorf("peer is nil")
	}

	pubKey, err := ParseKey(peer.PublicKey)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	var psk *wgtypes.Key
	if peer.PresharedKey != "" {
		pskKey, err := ParseKey(peer.PresharedKey)
		if err != nil {
			return fmt.Errorf("invalid preshared key: %w", err)
		}
		psk = &pskKey
	}

	allowedIPs := make([]net.IPNet, 0, len(peer.AllowedIPs))
	for _, cidr := range peer.AllowedIPs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("invalid allowed IP %q: %w", cidr, err)
		}
		allowedIPs = append(allowedIPs, *ipNet)
	}

	wgPeer := wgtypes.PeerConfig{
		PublicKey:                   pubKey,
		PresharedKey:                psk,
		AllowedIPs:                  allowedIPs,
		PersistentKeepaliveInterval: (*time.Duration)(nil),
		ReplaceAllowedIPs:           true,
	}
	if peer.PersistentKeepalive > 0 {
		interval := time.Duration(peer.PersistentKeepalive) * time.Second
		wgPeer.PersistentKeepaliveInterval = &interval
	}

	if peer.Endpoint != "" {
		udpAddr, err := net.ResolveUDPAddr("udp", peer.Endpoint)
		if err != nil {
			return fmt.Errorf("invalid endpoint %q: %w", peer.Endpoint, err)
		}
		wgPeer.Endpoint = udpAddr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.client.ConfigureDevice(m.config.InterfaceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{wgPeer},
	}); err != nil {
		return fmt.Errorf("failed to add peer: %w", err)
	}

	m.peers[peer.PublicKey] = peer
	return nil
}

// RemovePeer removes a peer by public key.
func (m *Manager) RemovePeer(publicKey string) error {
	pubKey, err := ParseKey(publicKey)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.client.ConfigureDevice(m.config.InterfaceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey: pubKey,
				Remove:    true,
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to remove peer: %w", err)
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

// PeerHandshakeTime returns time since last handshake.
func (m *Manager) PeerHandshakeTime(publicKey string) (time.Duration, error) {
	pubKey, err := ParseKey(publicKey)
	if err != nil {
		return 0, fmt.Errorf("invalid public key: %w", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	dev, err := m.client.Device(m.config.InterfaceName)
	if err != nil {
		return 0, fmt.Errorf("failed to get device: %w", err)
	}

	for _, p := range dev.Peers {
		if p.PublicKey == pubKey {
			if p.LastHandshakeTime.IsZero() {
				return 0, fmt.Errorf("no handshake recorded")
			}
			return time.Since(p.LastHandshakeTime), nil
		}
	}
	return 0, fmt.Errorf("peer not found on device: %s", publicKey)
}

// PeerRxTx returns bytes received/transmitted for a peer.
func (m *Manager) PeerRxTx(publicKey string) (rx, tx int64, err error) {
	pubKey, err := ParseKey(publicKey)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid public key: %w", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	dev, err := m.client.Device(m.config.InterfaceName)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get device: %w", err)
	}

	for _, p := range dev.Peers {
		if p.PublicKey == pubKey {
			return p.ReceiveBytes, p.TransmitBytes, nil
		}
	}
	return 0, 0, fmt.Errorf("peer not found on device: %s", publicKey)
}

// RotateKeys generates new key pair and updates interface.
func (m *Manager) RotateKeys() (newPublicKey string, err error) {
	privKeyStr, pubKeyStr, err := GenerateKeyPair()
	if err != nil {
		return "", fmt.Errorf("failed to generate key pair: %w", err)
	}

	privKey, err := ParseKey(privKeyStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse new private key: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.client.ConfigureDevice(m.config.InterfaceName, wgtypes.Config{
		PrivateKey: &privKey,
	}); err != nil {
		return "", fmt.Errorf("failed to rotate keys: %w", err)
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

	dev, err := m.client.Device(m.config.InterfaceName)
	if err != nil {
		return nil, fmt.Errorf("failed to get device: %w", err)
	}

	var rx, tx int64
	for _, p := range dev.Peers {
		rx += p.ReceiveBytes
		tx += p.TransmitBytes
	}

	return &InterfaceStats{
		Name:      dev.Name,
		Address:   m.config.Address,
		Port:      dev.ListenPort,
		PublicKey: dev.PublicKey.String(),
		Peers:     len(dev.Peers),
		RxBytes:   rx,
		TxBytes:   tx,
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
