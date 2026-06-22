package wireguard

import "time"

// wgBackend is the OS-specific data-plane seam the Manager delegates to. There
// is exactly one shared Manager (carrying the peer table, key-rotation state and
// all pure-Go mesh logic); the platform difference is confined to this
// interface. Linux drives the in-kernel WireGuard device via wgctrl
// (manager_linux.go); darwin drives a real userspace wireguard-go + gVisor
// netstack device (manager_darwin.go). Both return real, measured values for
// their OS — neither is a NoOp stub.
//
// All keys crossing this seam are base64 (the on-the-wire Config/PeerConfig
// format); each backend converts to its native representation internally. This
// keeps the interface OS-neutral (no wgtypes / no netstack types leak through).
type wgBackend interface {
	// configureInterface brings the device up with the given private key and
	// listen port. On Linux this is ConfigureDevice with ReplacePeers; on darwin
	// it stands up the userspace device. address is the interface CIDR (used by
	// the darwin netstack TUN; ignored by the kernel path, which expects the
	// address to be assigned out-of-band as on a real wg interface).
	configureInterface(privateKey, address string, listenPort, mtu int) error

	// teardownInterface tears the device down (best-effort) and releases
	// resources. Safe to call even if configureInterface was never called.
	teardownInterface() error

	// configurePeer installs or updates a single peer on the live device.
	configurePeer(peer *PeerConfig) error

	// removePeer removes a peer by its base64 public key.
	removePeer(publicKey string) error

	// rotateDeviceKey replaces the device's own private key in place.
	rotateDeviceKey(privateKey string) error

	// peerHandshakeAge returns the time since the named peer last handshook.
	peerHandshakeAge(publicKey string) (time.Duration, error)

	// peerRxTx returns bytes received/transmitted for the named peer.
	peerRxTx(publicKey string) (rx, tx int64, err error)

	// deviceStats returns interface-level counters and the device public key.
	deviceStats() (devStats, error)

	// close releases any client/socket resources held by the backend.
	close() error
}

// devStats is the OS-neutral interface-level statistics snapshot a backend
// reports back to the Manager for InterfaceStats().
type devStats struct {
	Name       string
	ListenPort int
	PublicKey  string
	PeerCount  int
	RxBytes    int64
	TxBytes    int64
}
