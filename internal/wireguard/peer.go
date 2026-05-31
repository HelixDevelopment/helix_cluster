package wireguard

import (
	"fmt"
	"net"
	"strconv"
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

// Validate reports whether the peer is usable as a mesh member. A peer that
// passes Validate can be installed by the underlying manager and rendered into
// a functional WireGuard config; one that fails would produce a [Peer] block
// that silently does nothing for the end user.
//
// Pure-Go: no kernel or network access (DNS is not resolved here).
func (p *Peer) Validate() error {
	if p == nil {
		return fmt.Errorf("peer is nil")
	}
	if p.ID == "" {
		return fmt.Errorf("peer ID is empty")
	}
	if _, err := pkgwg.ParseKey(p.PublicKey); err != nil {
		return fmt.Errorf("peer %s: invalid public key: %w", p.ID, err)
	}
	if len(p.AllowedIPs) == 0 {
		return fmt.Errorf("peer %s: no AllowedIPs", p.ID)
	}
	for _, cidr := range p.AllowedIPs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("peer %s: invalid AllowedIP %q: %w", p.ID, cidr, err)
		}
	}
	if p.Endpoint != "" {
		host, port, err := net.SplitHostPort(p.Endpoint)
		if err != nil {
			return fmt.Errorf("peer %s: invalid endpoint %q: %w", p.ID, p.Endpoint, err)
		}
		if host == "" {
			return fmt.Errorf("peer %s: invalid endpoint %q: empty host", p.ID, p.Endpoint)
		}
		if _, err := strconv.Atoi(port); err != nil {
			return fmt.Errorf("peer %s: invalid endpoint %q: non-numeric port", p.ID, p.Endpoint)
		}
	}
	if p.PersistentKeepalive < 0 {
		return fmt.Errorf("peer %s: negative PersistentKeepalive %d", p.ID, p.PersistentKeepalive)
	}
	return nil
}

// Validate reports whether the local node configuration is internally
// consistent: a valid (or empty) private key, an in-range listen port, and
// peers that each pass Validate with no duplicate IDs.
//
// Pure-Go: no kernel or network access.
func (cfg *LocalConfig) Validate() error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if cfg.PrivateKey != "" {
		if _, err := pkgwg.ParseKey(cfg.PrivateKey); err != nil {
			return fmt.Errorf("invalid private key: %w", err)
		}
	}
	if cfg.ListenPort < 0 || cfg.ListenPort > 65535 {
		return fmt.Errorf("listen port %d out of range [0,65535]", cfg.ListenPort)
	}
	seen := make(map[string]struct{}, len(cfg.Peers))
	for i := range cfg.Peers {
		if err := cfg.Peers[i].Validate(); err != nil {
			return err
		}
		if _, dup := seen[cfg.Peers[i].ID]; dup {
			return fmt.Errorf("duplicate peer ID %q", cfg.Peers[i].ID)
		}
		seen[cfg.Peers[i].ID] = struct{}{}
	}
	return nil
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
