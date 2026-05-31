package wireguard

import (
	"fmt"
	"strings"

	pkgwg "github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// Peer represents a mesh peer.
type Peer struct {
	ID                  string
	PublicKey           string
	Endpoint            string
	AllowedIPs          []string
	PersistentKeepalive int // seconds
}

// LocalConfig holds configuration for the local WireGuard node.
type LocalConfig struct {
	PrivateKey string
	ListenPort int
	Peers      []Peer
}

// GenerateKeyPair generates a WireGuard key pair.
// Delegates to pkg/wireguard for real key generation; can be mocked in tests.
func GenerateKeyPair() (privateKey, publicKey string, err error) {
	return pkgwg.GenerateKeyPair()
}

// ConfigToINI converts a LocalConfig to WireGuard INI format.
func ConfigToINI(cfg *LocalConfig) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is nil")
	}

	var b strings.Builder

	b.WriteString("[Interface]\n")
	if cfg.PrivateKey != "" {
		b.WriteString(fmt.Sprintf("PrivateKey = %s\n", cfg.PrivateKey))
	}
	if cfg.ListenPort > 0 {
		b.WriteString(fmt.Sprintf("ListenPort = %d\n", cfg.ListenPort))
	}
	b.WriteString("\n")

	for _, peer := range cfg.Peers {
		b.WriteString("[Peer]\n")
		if peer.PublicKey != "" {
			b.WriteString(fmt.Sprintf("PublicKey = %s\n", peer.PublicKey))
		}
		if peer.Endpoint != "" {
			b.WriteString(fmt.Sprintf("Endpoint = %s\n", peer.Endpoint))
		}
		for _, ip := range peer.AllowedIPs {
			b.WriteString(fmt.Sprintf("AllowedIPs = %s\n", ip))
		}
		if peer.PersistentKeepalive > 0 {
			b.WriteString(fmt.Sprintf("PersistentKeepalive = %d\n", peer.PersistentKeepalive))
		}
		b.WriteString("\n")
	}

	return b.String(), nil
}
