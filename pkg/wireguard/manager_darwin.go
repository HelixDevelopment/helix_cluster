//go:build darwin

// Package wireguard, on darwin, drives a REAL userspace WireGuard device
// (wireguard-go + gVisor netstack) through the wgBackend seam. macOS has no
// in-kernel WireGuard device, so the wgctrl/kernel path used on Linux cannot
// carry traffic here. Rather than fall back to a NoOp stub (a CLAUDE-2
// PASS-bluff), the shared Manager delegates to this backend, which stands up an
// actual encrypted transport via the existing UserspaceDevice (netstack_darwin.go).
package wireguard

import (
	"fmt"
	"net/netip"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/tun/netstack"
)

// userspaceBackend is the darwin wgBackend backed by a real userspace
// wireguard-go device on a gVisor netstack TUN.
type userspaceBackend struct {
	mu   sync.Mutex
	dev  *UserspaceDevice
	name string
	// listenPort is remembered from configureInterface so deviceStats can report
	// it (the userspace UAPI does not surface it back as cleanly as wgctrl).
	listenPort int
	// publicKey is derived once at bring-up so deviceStats can report it without
	// re-deriving on every call.
	publicKey string
}

// newBackend creates the darwin (userspace) backend. It is the per-OS
// constructor the shared NewManager calls. The device is created lazily in
// configureInterface (Start), mirroring the kernel path where the device only
// becomes live on Start().
func newBackend(cfg *Config) (wgBackend, error) {
	return &userspaceBackend{name: cfg.InterfaceName}, nil
}

// parseAddrs converts an interface CIDR (or bare address) into the netip.Addr
// slice the userspace netstack TUN expects. An empty address yields an error —
// a real userspace tunnel must have at least one in-stack address.
func parseAddrs(address string) ([]netip.Addr, error) {
	if address == "" {
		return nil, fmt.Errorf("interface address is required for the userspace WireGuard device on darwin")
	}
	if prefix, err := netip.ParsePrefix(address); err == nil {
		return []netip.Addr{prefix.Addr()}, nil
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return nil, fmt.Errorf("invalid interface address %q: %w", address, err)
	}
	return []netip.Addr{addr}, nil
}

func (b *userspaceBackend) configureInterface(privateKey, address string, listenPort, mtu int) error {
	addrs, err := parseAddrs(address)
	if err != nil {
		return err
	}

	pub, err := derivePublicKey(privateKey)
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}

	dev, err := NewUserspaceDevice(addrs, nil, mtu, listenPort, privateKey)
	if err != nil {
		return fmt.Errorf("failed to bring up userspace device %s: %w", b.name, err)
	}

	b.mu.Lock()
	// If a previous device was up, close it before replacing (ReplacePeers-style
	// fresh bring-up parity with the kernel path).
	if b.dev != nil {
		_ = b.dev.Close()
	}
	b.dev = dev
	b.listenPort = listenPort
	b.publicKey = pub
	b.mu.Unlock()
	return nil
}

func (b *userspaceBackend) teardownInterface() error {
	b.mu.Lock()
	dev := b.dev
	b.dev = nil
	b.mu.Unlock()
	if dev != nil {
		return dev.Close()
	}
	return nil
}

func (b *userspaceBackend) configurePeer(peer *PeerConfig) error {
	b.mu.Lock()
	dev := b.dev
	b.mu.Unlock()
	if dev == nil {
		return fmt.Errorf("userspace device not started; call Start() before AddPeer")
	}
	return dev.AddPeer(peer)
}

func (b *userspaceBackend) removePeer(publicKey string) error {
	b.mu.Lock()
	dev := b.dev
	b.mu.Unlock()
	if dev == nil {
		// Nothing live to remove from; the Manager still drops it from its table.
		return nil
	}
	return dev.RemovePeer(publicKey)
}

func (b *userspaceBackend) rotateDeviceKey(privateKey string) error {
	pub, err := derivePublicKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to parse new private key: %w", err)
	}
	b.mu.Lock()
	dev := b.dev
	b.mu.Unlock()
	if dev == nil {
		return fmt.Errorf("userspace device not started; cannot rotate device key")
	}
	if err := dev.SetPrivateKey(privateKey); err != nil {
		return fmt.Errorf("failed to rotate keys on userspace device: %w", err)
	}
	b.mu.Lock()
	b.publicKey = pub
	b.mu.Unlock()
	return nil
}

func (b *userspaceBackend) peerHandshakeAge(publicKey string) (time.Duration, error) {
	b.mu.Lock()
	dev := b.dev
	b.mu.Unlock()
	if dev == nil {
		return 0, fmt.Errorf("userspace device not started")
	}
	return dev.PeerHandshakeAge(publicKey)
}

func (b *userspaceBackend) peerRxTx(publicKey string) (rx, tx int64, err error) {
	b.mu.Lock()
	dev := b.dev
	b.mu.Unlock()
	if dev == nil {
		return 0, 0, fmt.Errorf("userspace device not started")
	}
	return dev.PeerRxTx(publicKey)
}

func (b *userspaceBackend) deviceStats() (devStats, error) {
	b.mu.Lock()
	dev := b.dev
	name := b.name
	port := b.listenPort
	pub := b.publicKey
	b.mu.Unlock()
	if dev == nil {
		return devStats{}, fmt.Errorf("userspace device not started")
	}
	rx, tx, peers := dev.Totals()
	return devStats{
		Name:       name,
		ListenPort: port,
		PublicKey:  pub,
		PeerCount:  peers,
		RxBytes:    rx,
		TxBytes:    tx,
	}, nil
}

func (b *userspaceBackend) close() error {
	return b.teardownInterface()
}

// userspaceNet exposes the live in-stack network for dialing/listening THROUGH
// the tunnel. Returns nil if the device is not up. Used by Manager.UserspaceNet.
func (b *userspaceBackend) userspaceNet() *netstack.Net {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dev == nil {
		return nil
	}
	return b.dev.Net()
}

// UserspaceNet returns the darwin userspace device's in-stack network so callers
// (and tests) can dial/listen THROUGH the real encrypted tunnel without touching
// the host network stack. Returns nil for a NoOp manager or before Start().
func (m *Manager) UserspaceNet() *netstack.Net {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config.NoOp || m.backend == nil {
		return nil
	}
	ub, ok := m.backend.(*userspaceBackend)
	if !ok {
		return nil
	}
	return ub.userspaceNet()
}
