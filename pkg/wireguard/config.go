package wireguard

import (
	"encoding/base64"
	"fmt"
	"net"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Config holds WireGuard interface configuration.
type Config struct {
	InterfaceName string
	ListenPort    int
	PrivateKey    string
	Address       string // CIDR
	MTU           int

	// NAT traversal
	EnableUPnP        bool
	EnableNATPMapping bool

	// NoOp disables real WireGuard interface operations.
	// Useful for testing on platforms without WireGuard kernel support.
	NoOp bool

	// KeyOverlap is the duration the superseded device key remains valid after
	// RotateKeysTracked is called, so in-flight traffic encrypted under it is
	// not dropped during rotation. Zero disables the grace window (the old key
	// is invalidated immediately).
	KeyOverlap time.Duration
}

// PeerConfig holds configuration for a single peer.
type PeerConfig struct {
	PublicKey           string
	PresharedKey        string // optional
	AllowedIPs          []string
	Endpoint            string // host:port
	PersistentKeepalive int    // seconds
}

// GenerateKeyPair generates a new WireGuard key pair.
func GenerateKeyPair() (privateKey, publicKey string, err error) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}
	pub := key.PublicKey()
	return base64.StdEncoding.EncodeToString(key[:]),
		base64.StdEncoding.EncodeToString(pub[:]),
		nil
}

// validateAllowedIPs checks that every allowed-IP entry is a valid CIDR. It is
// OS-neutral so the shared Manager.AddPeer can reject malformed peers before
// they reach any per-OS backend.
func validateAllowedIPs(cidrs []string) error {
	for _, cidr := range cidrs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid allowed IP %q: %w", cidr, err)
		}
	}
	return nil
}

// validateOptionalEndpoint checks that a non-empty endpoint resolves to a UDP
// address. An empty endpoint is allowed (peers may be endpoint-less, e.g. when
// only an inbound handshake is expected) — matching the original AddPeer
// contract. For mandatory-endpoint rendering see validateEndpoint in configgen.go.
func validateOptionalEndpoint(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	if _, err := net.ResolveUDPAddr("udp", endpoint); err != nil {
		return fmt.Errorf("invalid endpoint %q: %w", endpoint, err)
	}
	return nil
}

// derivePublicKey returns the base64 Curve25519 public key for a base64
// private key. It is OS-neutral (used by both the kernel and userspace
// backends) and surfaces a real error for a malformed key.
func derivePublicKey(privateKey string) (string, error) {
	priv, err := ParseKey(privateKey)
	if err != nil {
		return "", err
	}
	return priv.PublicKey().String(), nil
}

// ParseKey parses a base64-encoded WireGuard key.
func ParseKey(key string) (wgtypes.Key, error) {
	var k wgtypes.Key
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return k, fmt.Errorf("invalid base64 key: %w", err)
	}
	if len(b) != len(k) {
		return k, fmt.Errorf("invalid key length: got %d, want %d", len(b), len(k))
	}
	copy(k[:], b)
	return k, nil
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		InterfaceName:     "wg0",
		ListenPort:        51820,
		MTU:               1420,
		EnableUPnP:        false,
		EnableNATPMapping: false,
	}
}
