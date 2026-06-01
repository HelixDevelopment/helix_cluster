package wireguard

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// MeshNode describes a single node participating in the WireGuard mesh.
// All fields are mandatory for config generation.
type MeshNode struct {
	// ID is a caller-chosen stable identifier for this node (e.g. "node-0").
	ID string

	// PrivateKey is the base64-encoded Curve25519 private key for this node.
	// GenerateKeyPair() produces a suitable value.
	PrivateKey string

	// Address is the CIDR address assigned to this node's WireGuard interface
	// (e.g. "10.200.0.1/24").  Every peer in the mesh is given AllowedIPs equal
	// to the host /32 (or /128) derived from this address.
	Address string

	// Endpoint is the publicly-reachable "host:port" for this node
	// (e.g. "203.0.113.1:51820").  It is written into every peer config that
	// references this node.
	Endpoint string
}

// MeshConfig is the generated WireGuard configuration for a single node in an
// N-node full mesh.  For node i it contains one [Peer] block for every other
// node j (i ≠ j).
type MeshConfig struct {
	// NodeID is the ID of the node this config belongs to.
	NodeID string

	// ConfigString is the rendered wg-quick-compatible configuration text,
	// including an [Interface] block and one [Peer] block per remote node.
	ConfigString string

	// Peers is the ordered list of peer public keys included in this config.
	// len(Peers) == N-1 for an N-node mesh.
	Peers []string
}

// GenerateMeshConfigs generates a full-mesh WireGuard configuration for every
// node in nodes.  Each node receives a MeshConfig containing an [Interface]
// block and one [Peer] block per other node.
//
// The listen port is fixed at 51820 (the canonical WireGuard port).
// AllowedIPs for each peer is the /32 (IPv4) or /128 (IPv6) host address
// derived from that peer's Address field.
//
// The optional tag parameter is appended as a comment on the first line of
// every generated config, enabling callers to embed a per-run UUID or other
// tracing label.  Pass "" to omit the tag comment.
//
// Pure-Go: no kernel or network access.  Works identically on every OS.
func GenerateMeshConfigs(nodes []MeshNode, tag string) ([]*MeshConfig, error) {
	if len(nodes) < 2 {
		return nil, fmt.Errorf("mesh requires at least 2 nodes, got %d", len(nodes))
	}

	// Pre-validate every node and derive its public key.
	type nodeInfo struct {
		node      MeshNode
		privKey   wgtypes.Key
		pubKey    wgtypes.Key
		pubKeyB64 string
		hostCIDR  string // /32 or /128 for AllowedIPs
	}
	infos := make([]nodeInfo, len(nodes))

	for i, n := range nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("node %d: ID is empty", i)
		}
		if n.Address == "" {
			return nil, fmt.Errorf("node %s: Address is empty", n.ID)
		}
		if n.Endpoint == "" {
			return nil, fmt.Errorf("node %s: Endpoint is empty", n.ID)
		}

		priv, err := ParseKey(n.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("node %s: invalid private key: %w", n.ID, err)
		}

		// Derive the host /32 or /128 from the interface Address.
		hostCIDR, err := hostPrefix(n.Address)
		if err != nil {
			return nil, fmt.Errorf("node %s: invalid address %q: %w", n.ID, n.Address, err)
		}

		pub := priv.PublicKey()
		infos[i] = nodeInfo{
			node:      n,
			privKey:   priv,
			pubKey:    pub,
			pubKeyB64: pub.String(),
			hostCIDR:  hostCIDR,
		}
	}

	configs := make([]*MeshConfig, len(nodes))
	for i, self := range infos {
		var b strings.Builder

		// Optional per-run tag comment.
		if tag != "" {
			fmt.Fprintf(&b, "# MeshTag: %s\n", tag)
		}

		// [Interface] block.
		b.WriteString("[Interface]\n")
		fmt.Fprintf(&b, "PrivateKey = %s\n", self.node.PrivateKey)
		fmt.Fprintf(&b, "Address = %s\n", self.node.Address)
		fmt.Fprintf(&b, "ListenPort = %d\n", 51820)

		peerPubKeys := make([]string, 0, len(nodes)-1)
		for j, peer := range infos {
			if i == j {
				// No self-peer.
				continue
			}
			peerPubKeys = append(peerPubKeys, peer.pubKeyB64)

			b.WriteString("\n[Peer]\n")
			fmt.Fprintf(&b, "PublicKey = %s\n", peer.pubKeyB64)
			fmt.Fprintf(&b, "AllowedIPs = %s\n", peer.hostCIDR)
			fmt.Fprintf(&b, "Endpoint = %s\n", peer.node.Endpoint)
			fmt.Fprintf(&b, "PersistentKeepalive = 25\n")
		}

		configs[i] = &MeshConfig{
			NodeID:       self.node.ID,
			ConfigString: b.String(),
			Peers:        peerPubKeys,
		}
	}
	return configs, nil
}

// RotateMeshNodeKey rotates the key for the node identified by nodeID within
// the nodes slice: it generates a fresh keypair, replaces that node's
// PrivateKey in-place, and regenerates all mesh configs using the same tag.
//
// The returned slice is a fresh generation of []*MeshConfig.  Only the configs
// that reference nodeID (either as the interface owner or as a peer) change;
// all other pairwise relationships are structurally identical.
//
// Returns the new public key for the rotated node alongside the configs.
//
// Pure-Go: no kernel or network access.
func RotateMeshNodeKey(nodes []MeshNode, nodeID string, tag string) (newPubKey string, configs []*MeshConfig, err error) {
	// Find the node to rotate.
	idx := -1
	for i, n := range nodes {
		if n.ID == nodeID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", nil, fmt.Errorf("node %q not found in mesh", nodeID)
	}

	// Generate a fresh keypair.
	privStr, pubStr, err := GenerateKeyPair()
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate key pair for node %q: %w", nodeID, err)
	}

	// Clone the nodes slice so we do not mutate the caller's slice.
	updated := make([]MeshNode, len(nodes))
	copy(updated, nodes)
	updated[idx].PrivateKey = privStr

	configs, err = GenerateMeshConfigs(updated, tag)
	if err != nil {
		return "", nil, fmt.Errorf("failed to regenerate mesh after key rotation: %w", err)
	}
	return pubStr, configs, nil
}

// hostPrefix converts a CIDR (e.g. "10.200.0.1/24") to the host-only prefix
// (/32 for IPv4, /128 for IPv6), e.g. "10.200.0.1/32".  This is the
// AllowedIPs entry that routes traffic exclusively to that peer's interface
// address.
func hostPrefix(cidr string) (string, error) {
	// Use net/netip for a modern, allocation-light parse.
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		// Try treating it as a bare address (no prefix length).
		addr, err2 := netip.ParseAddr(cidr)
		if err2 != nil {
			return "", fmt.Errorf("not a valid CIDR or address: %w", err)
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	addr := prefix.Addr()
	bits := addr.BitLen() // 32 for IPv4, 128 for IPv6
	return netip.PrefixFrom(addr, bits).String(), nil
}
