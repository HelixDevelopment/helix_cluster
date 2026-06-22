//go:build linux

// Package wireguard, on linux, drives the in-kernel WireGuard device through
// wgctrl — the native, kernel-accelerated data path. This file is the linux
// implementation of the wgBackend seam; the OS-neutral Manager (manager.go)
// delegates every device operation here. The behaviour is identical to the
// pre-seam Manager: ConfigureDevice with ReplacePeers on bring-up, per-peer
// ConfigureDevice on AddPeer/RemovePeer, Device() reads for stats.
package wireguard

import (
	"fmt"
	"net"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// deviceKeyOnly builds a wgtypes.Config that updates only the interface private
// key. Used by rotateDeviceKey on the kernel path.
func deviceKeyOnly(privKey wgtypes.Key) wgtypes.Config {
	return wgtypes.Config{PrivateKey: &privKey}
}

// kernelBackend is the linux wgBackend backed by an in-kernel WireGuard device.
type kernelBackend struct {
	client        *wgctrl.Client
	interfaceName string
}

// newBackend creates the linux (kernel/wgctrl) backend. It is the per-OS
// constructor the shared NewManager calls.
func newBackend(cfg *Config) (wgBackend, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create wgctrl client: %w", err)
	}
	return &kernelBackend{client: client, interfaceName: cfg.InterfaceName}, nil
}

func (b *kernelBackend) configureInterface(privateKey, _ string, listenPort, _ int) error {
	privKey, err := ParseKey(privateKey)
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}
	cfg := wgtypes.Config{
		PrivateKey:   &privKey,
		ListenPort:   &listenPort,
		ReplacePeers: true,
	}
	if err := b.client.ConfigureDevice(b.interfaceName, cfg); err != nil {
		return fmt.Errorf("failed to configure device %s: %w", b.interfaceName, err)
	}
	return nil
}

func (b *kernelBackend) teardownInterface() error {
	zeroKey := wgtypes.Key{}
	zeroPort := 0
	cfg := wgtypes.Config{
		PrivateKey:   &zeroKey,
		ListenPort:   &zeroPort,
		ReplacePeers: true,
	}
	if err := b.client.ConfigureDevice(b.interfaceName, cfg); err != nil {
		return fmt.Errorf("failed to tear down device %s: %w", b.interfaceName, err)
	}
	return nil
}

func (b *kernelBackend) configurePeer(peer *PeerConfig) error {
	wgPeer, err := peerToWgtypes(peer)
	if err != nil {
		return err
	}
	if err := b.client.ConfigureDevice(b.interfaceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{wgPeer},
	}); err != nil {
		return fmt.Errorf("failed to add peer: %w", err)
	}
	return nil
}

func (b *kernelBackend) removePeer(publicKey string) error {
	pubKey, err := ParseKey(publicKey)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}
	if err := b.client.ConfigureDevice(b.interfaceName, wgtypes.Config{
		Peers: []wgtypes.PeerConfig{{PublicKey: pubKey, Remove: true}},
	}); err != nil {
		return fmt.Errorf("failed to remove peer: %w", err)
	}
	return nil
}

func (b *kernelBackend) rotateDeviceKey(privateKey string) error {
	privKey, err := ParseKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to parse new private key: %w", err)
	}
	if err := b.client.ConfigureDevice(b.interfaceName, deviceKeyOnly(privKey)); err != nil {
		return fmt.Errorf("failed to rotate keys on device: %w", err)
	}
	return nil
}

func (b *kernelBackend) peerHandshakeAge(publicKey string) (time.Duration, error) {
	pubKey, err := ParseKey(publicKey)
	if err != nil {
		return 0, fmt.Errorf("invalid public key: %w", err)
	}
	dev, err := b.client.Device(b.interfaceName)
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

func (b *kernelBackend) peerRxTx(publicKey string) (rx, tx int64, err error) {
	pubKey, err := ParseKey(publicKey)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid public key: %w", err)
	}
	dev, err := b.client.Device(b.interfaceName)
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

func (b *kernelBackend) deviceStats() (devStats, error) {
	dev, err := b.client.Device(b.interfaceName)
	if err != nil {
		return devStats{}, fmt.Errorf("failed to get device: %w", err)
	}
	var rx, tx int64
	for _, p := range dev.Peers {
		rx += p.ReceiveBytes
		tx += p.TransmitBytes
	}
	return devStats{
		Name:       dev.Name,
		ListenPort: dev.ListenPort,
		PublicKey:  dev.PublicKey.String(),
		PeerCount:  len(dev.Peers),
		RxBytes:    rx,
		TxBytes:    tx,
	}, nil
}

func (b *kernelBackend) close() error {
	if b.client != nil {
		return b.client.Close()
	}
	return nil
}

// peerToWgtypes converts an OS-neutral PeerConfig into the wgtypes.PeerConfig
// the kernel path expects. Kept here so manager.go carries no wgtypes/net peer
// translation and stays OS-neutral.
func peerToWgtypes(peer *PeerConfig) (wgtypes.PeerConfig, error) {
	pubKey, err := ParseKey(peer.PublicKey)
	if err != nil {
		return wgtypes.PeerConfig{}, fmt.Errorf("invalid public key: %w", err)
	}

	var psk *wgtypes.Key
	if peer.PresharedKey != "" {
		pskKey, err := ParseKey(peer.PresharedKey)
		if err != nil {
			return wgtypes.PeerConfig{}, fmt.Errorf("invalid preshared key: %w", err)
		}
		psk = &pskKey
	}

	allowedIPs := make([]net.IPNet, 0, len(peer.AllowedIPs))
	for _, cidr := range peer.AllowedIPs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return wgtypes.PeerConfig{}, fmt.Errorf("invalid allowed IP %q: %w", cidr, err)
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
			return wgtypes.PeerConfig{}, fmt.Errorf("invalid endpoint %q: %w", peer.Endpoint, err)
		}
		wgPeer.Endpoint = udpAddr
	}
	return wgPeer, nil
}
