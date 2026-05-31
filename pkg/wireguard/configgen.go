package wireguard

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// validateCIDR returns an error if cidr is not a valid CIDR notation address.
func validateCIDR(cidr string) error {
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return fmt.Errorf("invalid allowed IP %q: %w", cidr, err)
	}
	return nil
}

// validateEndpoint returns an error if endpoint is not a valid host:port.
func validateEndpoint(endpoint string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint %q: %w", endpoint, err)
	}
	if host == "" {
		return fmt.Errorf("invalid endpoint %q: empty host", endpoint)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("invalid endpoint %q: non-numeric port", endpoint)
	}
	return nil
}

// ConfigString renders this peer as a WireGuard `[Peer]` configuration block.
//
// AllowedIPs are validated as CIDRs and joined comma-separated on a single
// line. Endpoint and PresharedKey are emitted only when set, and validated
// when present. PersistentKeepalive is emitted only when > 0 (0 means
// disabled and is omitted, matching wg-quick semantics).
//
// Pure-Go: no kernel or network access.
func (p *PeerConfig) ConfigString() (string, error) {
	if p == nil {
		return "", fmt.Errorf("peer config is nil")
	}
	if _, err := ParseKey(p.PublicKey); err != nil {
		return "", fmt.Errorf("invalid peer public key: %w", err)
	}
	if len(p.AllowedIPs) == 0 {
		return "", fmt.Errorf("peer %s has no AllowedIPs", p.PublicKey)
	}
	for _, cidr := range p.AllowedIPs {
		if err := validateCIDR(cidr); err != nil {
			return "", err
		}
	}
	if p.PresharedKey != "" {
		if _, err := ParseKey(p.PresharedKey); err != nil {
			return "", fmt.Errorf("invalid preshared key: %w", err)
		}
	}
	if p.Endpoint != "" {
		if err := validateEndpoint(p.Endpoint); err != nil {
			return "", err
		}
	}

	var b strings.Builder
	b.WriteString("[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
	if p.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", p.PresharedKey)
	}
	fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(p.AllowedIPs, ", "))
	if p.Endpoint != "" {
		fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
	}
	if p.PersistentKeepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.PersistentKeepalive)
	}
	return b.String(), nil
}

// GenerateConfigString renders the full WireGuard configuration for this
// manager: an `[Interface]` block followed by one `[Peer]` block per
// configured peer. A `# KeyGeneration: N` comment records the current key
// rotation generation so that a regenerated config can be detected as
// superseding an earlier one.
//
// Pure-Go: reads only in-memory state, no kernel or network access.
func (m *Manager) GenerateConfigString() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.config == nil {
		return "", fmt.Errorf("manager has no config")
	}
	if _, err := ParseKey(m.config.PrivateKey); err != nil {
		return "", fmt.Errorf("invalid interface private key: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# KeyGeneration: %d\n", m.keyGeneration)
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", m.config.PrivateKey)
	if m.config.Address != "" {
		fmt.Fprintf(&b, "Address = %s\n", m.config.Address)
	}
	if m.config.ListenPort > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", m.config.ListenPort)
	}
	if m.config.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", m.config.MTU)
	}

	for _, p := range m.peers {
		block, err := p.ConfigString()
		if err != nil {
			return "", fmt.Errorf("peer %s: %w", p.PublicKey, err)
		}
		b.WriteString("\n")
		b.WriteString(block)
	}
	return b.String(), nil
}

// ParsedConfig is the structured result of parsing a WireGuard config string.
type ParsedConfig struct {
	Interface InterfaceSection
	Peers     []PeerConfig
}

// InterfaceSection holds the parsed `[Interface]` fields.
type InterfaceSection struct {
	PrivateKey string
	Address    string
	ListenPort int
	MTU        int
}

// ParseConfigString parses a WireGuard configuration string (as produced by
// GenerateConfigString or wg-quick) back into structured form. It is the
// inverse of GenerateConfigString for the fields both support, enabling
// round-trip testing.
//
// Pure-Go: no kernel or network access.
func ParseConfigString(s string) (*ParsedConfig, error) {
	cfg := &ParsedConfig{}
	var (
		section string // "interface" | "peer"
		curPeer *PeerConfig
	)

	flushPeer := func() {
		if curPeer != nil {
			cfg.Peers = append(cfg.Peers, *curPeer)
			curPeer = nil
		}
	}

	for lineNo, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			switch strings.ToLower(strings.Trim(line, "[]")) {
			case "interface":
				section = "interface"
			case "peer":
				flushPeer()
				section = "peer"
				curPeer = &PeerConfig{}
			default:
				return nil, fmt.Errorf("line %d: unknown section %q", lineNo+1, line)
			}
			continue
		}

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: malformed entry %q", lineNo+1, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		switch section {
		case "interface":
			switch key {
			case "PrivateKey":
				cfg.Interface.PrivateKey = val
			case "Address":
				cfg.Interface.Address = val
			case "ListenPort":
				n, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("line %d: invalid ListenPort %q", lineNo+1, val)
				}
				cfg.Interface.ListenPort = n
			case "MTU":
				n, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("line %d: invalid MTU %q", lineNo+1, val)
				}
				cfg.Interface.MTU = n
			}
		case "peer":
			if curPeer == nil {
				return nil, fmt.Errorf("line %d: peer entry before [Peer] header", lineNo+1)
			}
			switch key {
			case "PublicKey":
				curPeer.PublicKey = val
			case "PresharedKey":
				curPeer.PresharedKey = val
			case "AllowedIPs":
				parts := strings.Split(val, ",")
				ips := make([]string, 0, len(parts))
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						ips = append(ips, p)
					}
				}
				curPeer.AllowedIPs = ips
			case "Endpoint":
				curPeer.Endpoint = val
			case "PersistentKeepalive":
				n, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("line %d: invalid PersistentKeepalive %q", lineNo+1, val)
				}
				curPeer.PersistentKeepalive = n
			}
		default:
			return nil, fmt.Errorf("line %d: entry outside any section", lineNo+1)
		}
	}
	flushPeer()
	return cfg, nil
}
