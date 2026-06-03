//go:build darwin

// Package wireguard, on darwin, provides a REAL userspace WireGuard data path
// built on wireguard-go + a gVisor netstack TUN. macOS has no in-kernel
// WireGuard device, so the wgctrl/kernel path used on Linux cannot carry
// traffic here. Rather than fall back to a NoOp stub (a CLAUDE-2 PASS-bluff),
// this file stands up an actual encrypted WireGuard transport entirely in
// user space, with no root and no /dev/net/tun: a netstack.CreateNetTUN
// provides the virtual interface, conn.NewDefaultBind() provides the UDP
// socket, device.NewDevice runs the Noise handshake + transport, and
// dev.IpcSet pushes the peer configuration via the UAPI.
package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// keyToHex converts a base64-encoded WireGuard key (the on-the-wire format used
// by Config/PeerConfig) into the lowercase-hex form the wireguard-go UAPI
// expects for private_key / public_key / preshared_key lines. It validates the
// key length so a malformed/corrupted key surfaces a real error instead of
// silently producing a tunnel that can never handshake.
func keyToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("invalid base64 key: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("invalid key length: got %d, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// UserspaceDevice is a real userspace WireGuard device backed by a gVisor
// netstack TUN. It is the darwin equivalent of the kernel/wgctrl interface the
// Manager drives on Linux: bringing it up performs a genuine Noise handshake
// and carries encrypted transport packets over the userspace UDP bind.
//
// The embedded *netstack.Net exposes the in-stack dialer/listener
// (ListenTCP/DialContextTCP/DialPing/...) so callers — and tests — can move
// real traffic THROUGH the tunnel without touching the host network stack.
type UserspaceDevice struct {
	dev    *device.Device
	tnet   *netstack.Net
	logger *device.Logger
}

// NewUserspaceDevice creates and brings up a real userspace WireGuard device on
// the given local tunnel addresses, listening on listenPort, using privateKey
// (base64). It returns a started device whose handshake/transport machinery is
// live; configure peers via AddPeer before traffic can flow.
//
// This is a real-operation path: every failure (bad key, bad address, device
// bring-up error) is returned as a real error — there is no NoOp shortcut.
func NewUserspaceDevice(localAddrs []netip.Addr, dnsServers []netip.Addr, mtu, listenPort int, privateKey string) (*UserspaceDevice, error) {
	if len(localAddrs) == 0 {
		return nil, fmt.Errorf("at least one local tunnel address is required")
	}
	if mtu <= 0 {
		mtu = 1420
	}

	privHex, err := keyToHex(privateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	tun, tnet, err := netstack.CreateNetTUN(localAddrs, dnsServers, mtu)
	if err != nil {
		return nil, fmt.Errorf("failed to create netstack TUN: %w", err)
	}

	logger := device.NewLogger(device.LogLevelError, "wg-userspace ")
	dev := device.NewDevice(tun, conn.NewDefaultBind(), logger)

	// Configure the interface (private key + listen port) over the UAPI.
	var sb strings.Builder
	sb.WriteString("private_key=" + privHex + "\n")
	sb.WriteString("listen_port=" + strconv.Itoa(listenPort) + "\n")
	if err := dev.IpcSet(sb.String()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("failed to configure userspace device: %w", err)
	}

	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("failed to bring up userspace device: %w", err)
	}

	return &UserspaceDevice{dev: dev, tnet: tnet, logger: logger}, nil
}

// Net returns the in-stack network for dialing/listening THROUGH the tunnel.
func (u *UserspaceDevice) Net() *netstack.Net { return u.tnet }

// AddPeer configures (or updates) a peer on the live userspace device. This is
// the darwin equivalent of Manager.AddPeer's wgctrl ConfigureDevice call: it
// installs the peer's public key, optional preshared key, allowed-IPs, endpoint
// and keepalive directly into the running wireguard-go device via the UAPI, so
// a handshake to that peer can proceed. Real errors are returned for any
// malformed input.
func (u *UserspaceDevice) AddPeer(peer *PeerConfig) error {
	if peer == nil {
		return fmt.Errorf("peer is nil")
	}

	pubHex, err := keyToHex(peer.PublicKey)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("public_key=" + pubHex + "\n")

	if peer.PresharedKey != "" {
		pskHex, err := keyToHex(peer.PresharedKey)
		if err != nil {
			return fmt.Errorf("invalid preshared key: %w", err)
		}
		sb.WriteString("preshared_key=" + pskHex + "\n")
	}

	sb.WriteString("replace_allowed_ips=true\n")
	for _, cidr := range peer.AllowedIPs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fmt.Errorf("invalid allowed IP %q: %w", cidr, err)
		}
		sb.WriteString("allowed_ip=" + prefix.String() + "\n")
	}

	if peer.Endpoint != "" {
		ap, err := netip.ParseAddrPort(peer.Endpoint)
		if err != nil {
			return fmt.Errorf("invalid endpoint %q: %w", peer.Endpoint, err)
		}
		sb.WriteString("endpoint=" + ap.String() + "\n")
	}

	if peer.PersistentKeepalive > 0 {
		sb.WriteString("persistent_keepalive_interval=" + strconv.Itoa(peer.PersistentKeepalive) + "\n")
	}

	if err := u.dev.IpcSet(sb.String()); err != nil {
		return fmt.Errorf("failed to add peer: %w", err)
	}
	return nil
}

// Close tears down the userspace device and its netstack TUN.
func (u *UserspaceDevice) Close() error {
	if u.dev != nil {
		u.dev.Close()
	}
	return nil
}
