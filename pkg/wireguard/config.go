package wireguard

import (
	"encoding/base64"
	"fmt"

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
