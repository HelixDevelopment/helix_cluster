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
	"time"

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

// RemovePeer removes a peer from the live userspace device by its base64 public
// key. It is the darwin equivalent of Manager.RemovePeer's wgctrl path.
func (u *UserspaceDevice) RemovePeer(publicKey string) error {
	pubHex, err := keyToHex(publicKey)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("public_key=" + pubHex + "\n")
	sb.WriteString("remove=true\n")
	if err := u.dev.IpcSet(sb.String()); err != nil {
		return fmt.Errorf("failed to remove peer: %w", err)
	}
	return nil
}

// SetPrivateKey rotates the device's own private key in place, without dropping
// configured peers (replace_peers is NOT set). This is the darwin equivalent of
// the wgctrl deviceKeyOnly ConfigureDevice call used during key rotation.
func (u *UserspaceDevice) SetPrivateKey(privateKey string) error {
	privHex, err := keyToHex(privateKey)
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}
	if err := u.dev.IpcSet("private_key=" + privHex + "\n"); err != nil {
		return fmt.Errorf("failed to set private key: %w", err)
	}
	return nil
}

// PeerHandshakeAge returns the time since the named peer last completed a
// handshake, read from the live device via the wireguard-go UAPI (get=1). It is
// the darwin equivalent of reading LastHandshakeTime from wgctrl.
func (u *UserspaceDevice) PeerHandshakeAge(publicKey string) (time.Duration, error) {
	pubHex, err := keyToHex(publicKey)
	if err != nil {
		return 0, fmt.Errorf("invalid public key: %w", err)
	}
	p, ok, err := u.peerStat(pubHex)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("peer not found on device: %s", publicKey)
	}
	if p.handshakeSec == 0 && p.handshakeNsec == 0 {
		return 0, fmt.Errorf("no handshake recorded")
	}
	last := time.Unix(p.handshakeSec, p.handshakeNsec)
	return time.Since(last), nil
}

// PeerRxTx returns bytes received/transmitted for the named peer, read from the
// live device via the UAPI. Darwin equivalent of wgctrl ReceiveBytes/TransmitBytes.
func (u *UserspaceDevice) PeerRxTx(publicKey string) (rx, tx int64, err error) {
	pubHex, err := keyToHex(publicKey)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid public key: %w", err)
	}
	p, ok, err := u.peerStat(pubHex)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, fmt.Errorf("peer not found on device: %s", publicKey)
	}
	return p.rxBytes, p.txBytes, nil
}

// Totals returns aggregate rx/tx bytes and the live peer count, read from the
// device via the UAPI. Darwin equivalent of summing wgctrl device peer stats.
func (u *UserspaceDevice) Totals() (rx, tx int64, peers int) {
	out, err := u.dev.IpcGet()
	if err != nil {
		return 0, 0, 0
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := splitUAPI(line)
		if !ok {
			continue
		}
		switch k {
		case "public_key":
			peers++
		case "rx_bytes":
			if n, e := strconv.ParseInt(v, 10, 64); e == nil {
				rx += n
			}
		case "tx_bytes":
			if n, e := strconv.ParseInt(v, 10, 64); e == nil {
				tx += n
			}
		}
	}
	return rx, tx, peers
}

// peerStatLine holds the UAPI-reported counters for a single peer.
type peerStatLine struct {
	rxBytes       int64
	txBytes       int64
	handshakeSec  int64
	handshakeNsec int64
}

// peerStat reads the named peer's counters from the device via the UAPI get=1
// dump. wantHex is the lowercase-hex public key. The dump lists peers in order,
// each starting with a public_key line; counters belong to the most recent
// public_key seen.
func (u *UserspaceDevice) peerStat(wantHex string) (peerStatLine, bool, error) {
	out, err := u.dev.IpcGet()
	if err != nil {
		return peerStatLine{}, false, fmt.Errorf("failed to read device state: %w", err)
	}
	var cur string
	var stat peerStatLine
	var found bool
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := splitUAPI(line)
		if !ok {
			continue
		}
		switch k {
		case "public_key":
			if found {
				return stat, true, nil
			}
			cur = v
			stat = peerStatLine{}
		case "rx_bytes":
			if cur == wantHex {
				stat.rxBytes, _ = strconv.ParseInt(v, 10, 64)
				found = true
			}
		case "tx_bytes":
			if cur == wantHex {
				stat.txBytes, _ = strconv.ParseInt(v, 10, 64)
				found = true
			}
		case "last_handshake_time_sec":
			if cur == wantHex {
				stat.handshakeSec, _ = strconv.ParseInt(v, 10, 64)
				found = true
			}
		case "last_handshake_time_nsec":
			if cur == wantHex {
				stat.handshakeNsec, _ = strconv.ParseInt(v, 10, 64)
				found = true
			}
		}
	}
	if found {
		return stat, true, nil
	}
	return peerStatLine{}, false, nil
}

// splitUAPI splits a "key=value" UAPI line. Returns ok=false for blank lines.
func splitUAPI(line string) (key, value string, ok bool) {
	i := strings.IndexByte(line, '=')
	if i < 0 {
		return "", "", false
	}
	return line[:i], line[i+1:], true
}

// Close tears down the userspace device and its netstack TUN.
func (u *UserspaceDevice) Close() error {
	if u.dev != nil {
		u.dev.Close()
	}
	return nil
}
