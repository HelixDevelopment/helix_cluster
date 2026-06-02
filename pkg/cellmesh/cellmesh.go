// Package cellmesh manages WireGuard inter-cell mesh configuration for
// Helix Cluster OS. It owns three concerns:
//
//   - Config GENERATION: BuildMesh computes a full-mesh peer table for every
//     cell (each cell's config lists every other cell as a peer, with
//     AllowedIPs set to that peer cell's advertised CIDRs).
//   - CIDR topology validation: DetectOverlap rejects configurations in which
//     two cells advertise overlapping IP prefixes, which would cause routing
//     ambiguity in the inter-cell fabric.
//   - Config RENDERING / PARSING: RenderConfig emits a wg-quick-compatible
//     text block; ParseConfig is its inverse, enabling round-trip validation.
//
// Applying a WireGuard configuration to a kernel device is explicitly OUT OF
// SCOPE for this package: the Apply function always returns ErrUnsupported
// (CLAUDE-2 sentinel). Real application is performed by pkg/wireguard at the
// appropriate layer.
//
// All generation and parsing functions are pure-Go, dependency-free, and
// deterministic. They work identically on every OS.
package cellmesh

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// ─── Public sentinel ───────────────────────────────────────────────────────

// ErrUnsupported is returned by Apply to signal that this environment does
// not support applying a WireGuard config to a kernel device. Callers that
// want to check for this condition must use errors.Is.
var ErrUnsupported = errors.New("cellmesh: applying WireGuard config unsupported in this environment")

// ─── Input/output types ────────────────────────────────────────────────────

// CellPeer describes a single cell in the inter-cell mesh. It is the input
// type for BuildMesh and DetectOverlap. Callers define CellPeer directly;
// there is no coupling to pkg/federation.Cell (which has no CIDR field).
type CellPeer struct {
	// CellID is a stable, globally unique identifier for this cell
	// (e.g. "cell-us-east-1a"). Must be non-empty and unique across the mesh.
	CellID string

	// PublicKey is the base64-encoded Curve25519 public key of this cell's
	// WireGuard interface (e.g. the output of "wg pubkey").
	PublicKey string

	// Endpoint is the publicly reachable "host:port" address of this cell's
	// WireGuard listener (e.g. "203.0.113.7:51820").
	Endpoint string

	// CIDRs is the set of IP prefixes this cell advertises into the mesh.
	// Each entry must be a valid CIDR in the form accepted by net/netip.ParsePrefix
	// (e.g. "10.1.0.0/16", "fd00:a::0/48").
	// CIDRs MUST NOT overlap with those of any other cell in the mesh; if they
	// do, BuildMesh and DetectOverlap return an error naming both conflicting cells.
	CIDRs []string
}

// PeerEntry is one element in the per-cell peer list produced by BuildMesh.
// It describes a remote cell as it should appear in a WireGuard [Peer] block.
type PeerEntry struct {
	// CellID identifies the remote cell.
	CellID string

	// PublicKey is the remote cell's WireGuard public key.
	PublicKey string

	// Endpoint is the remote cell's "host:port" address.
	Endpoint string

	// AllowedIPs is the set of CIDRs this peer cell advertises. Traffic
	// destined for any of these prefixes is routed through this peer.
	// Exactly equal to the source CellPeer.CIDRs for that cell.
	AllowedIPs []string
}

// ─── Core logic ───────────────────────────────────────────────────────────

// BuildMesh computes a full-mesh WireGuard peer configuration for every cell
// in cells. For each cell C, the returned entry is a slice of PeerEntry values
// covering every other cell (C is never included in its own peer list).
//
// The AllowedIPs field of each PeerEntry is set to the exact CIDRs advertised
// by that peer cell (CellPeer.CIDRs), preserving the original order.
//
// The peer list for each cell is sorted lexicographically by CellID so the
// output is deterministic regardless of the order in which cells was provided.
//
// BuildMesh returns an error if:
//   - len(cells) < 2
//   - any two cells share the same CellID
//   - any two cells advertise overlapping CIDRs (via DetectOverlap)
func BuildMesh(cells []CellPeer) (map[string][]PeerEntry, error) {
	if len(cells) < 2 {
		return nil, fmt.Errorf("cellmesh: BuildMesh requires at least 2 cells, got %d", len(cells))
	}

	// Duplicate CellID check.
	seen := make(map[string]struct{}, len(cells))
	for _, c := range cells {
		if _, dup := seen[c.CellID]; dup {
			return nil, fmt.Errorf("cellmesh: duplicate CellID %q", c.CellID)
		}
		seen[c.CellID] = struct{}{}
	}

	// CIDR overlap check.
	if err := DetectOverlap(cells); err != nil {
		return nil, err
	}

	// Sort a stable copy by CellID so every cell sees peers in the same order.
	sorted := make([]CellPeer, len(cells))
	copy(sorted, cells)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CellID < sorted[j].CellID })

	result := make(map[string][]PeerEntry, len(sorted))
	for _, self := range sorted {
		peers := make([]PeerEntry, 0, len(sorted)-1)
		for _, peer := range sorted {
			if peer.CellID == self.CellID {
				// A cell is never its own peer.
				continue
			}
			// Copy the peer's CIDRs verbatim as AllowedIPs.
			allowedIPs := make([]string, len(peer.CIDRs))
			copy(allowedIPs, peer.CIDRs)
			peers = append(peers, PeerEntry{
				CellID:     peer.CellID,
				PublicKey:  peer.PublicKey,
				Endpoint:   peer.Endpoint,
				AllowedIPs: allowedIPs,
			})
		}
		result[self.CellID] = peers
	}
	return result, nil
}

// DetectOverlap inspects all CIDRs across cells and returns an error if any
// two cells advertise overlapping prefixes. The error message names both
// conflicting cells and the two overlapping prefixes.
//
// Overlap is determined with net/netip.Prefix.Overlaps, which returns true
// when one prefix is a subset, superset, or exact match of the other.
//
// CIDRs within the same cell are NOT checked for self-overlap; only
// cross-cell overlap is detected.
func DetectOverlap(cells []CellPeer) error {
	type prefixOwner struct {
		prefix netip.Prefix
		cellID string
		raw    string
	}

	// Parse and collect all prefixes with their owning cell.
	var all []prefixOwner
	for _, c := range cells {
		for _, raw := range c.CIDRs {
			pfx, err := netip.ParsePrefix(raw)
			if err != nil {
				return fmt.Errorf("cellmesh: cell %q has invalid CIDR %q: %w", c.CellID, raw, err)
			}
			all = append(all, prefixOwner{prefix: pfx, cellID: c.CellID, raw: raw})
		}
	}

	// O(n²) pairwise check — mesh sizes are small; readability > micro-opt.
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			a, b := all[i], all[j]
			if a.cellID == b.cellID {
				// Same cell: intra-cell CIDR overlap is not our concern.
				continue
			}
			if a.prefix.Overlaps(b.prefix) {
				return fmt.Errorf(
					"cellmesh: CIDR overlap between cell %q (%s) and cell %q (%s)",
					a.cellID, a.raw, b.cellID, b.raw,
				)
			}
		}
	}
	return nil
}

// ─── Config rendering and parsing ─────────────────────────────────────────

// RenderConfig produces a deterministic wg-quick-compatible configuration
// string for the local cell. The output contains:
//
//   - An [Interface] block with PrivateKey = localPrivKeyRef (a reference
//     string the caller provides; RenderConfig does not validate it as a real
//     key so that tests can use descriptive labels).
//   - One [Peer] block per entry in peers, in the order peers is given.
//     Each [Peer] block contains PublicKey, AllowedIPs (comma-separated), and
//     Endpoint fields. A # CellID comment precedes each [Peer] block.
//
// The output is stable: equal inputs always produce equal output.
func RenderConfig(localCellID string, localPrivKeyRef string, peers []PeerEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# CellMesh config for cell: %s\n", localCellID)
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", localPrivKeyRef)

	for _, p := range peers {
		fmt.Fprintf(&b, "\n# Peer cell: %s\n", p.CellID)
		b.WriteString("[Peer]\n")
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(p.AllowedIPs, ", "))
		fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
	}
	return b.String()
}

// ParseConfig is the inverse of RenderConfig. It reads the wg-quick-style
// text produced by RenderConfig and reconstructs the peer list. The local
// cell's [Interface] block is skipped; only [Peer] blocks are decoded.
//
// ParseConfig is tolerant of leading/trailing whitespace on each line and of
// blank lines between blocks, matching the behaviour of wg-quick itself.
//
// A # Peer cell: <id> comment before each [Peer] block is used to recover
// the CellID. If that comment is absent, CellID is left empty.
//
// AllowedIPs is split on ", " (comma-space) to reconstruct the original slice.
//
// ParseConfig returns an error if a [Peer] block is malformed (missing
// PublicKey, AllowedIPs, or Endpoint).
func ParseConfig(text string) ([]PeerEntry, error) {
	lines := strings.Split(text, "\n")

	var peers []PeerEntry
	var cur *PeerEntry
	pendingCellID := ""

	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.PublicKey == "" {
			return fmt.Errorf("cellmesh: [Peer] block missing PublicKey")
		}
		if len(cur.AllowedIPs) == 0 {
			return fmt.Errorf("cellmesh: [Peer] block missing AllowedIPs")
		}
		if cur.Endpoint == "" {
			return fmt.Errorf("cellmesh: [Peer] block missing Endpoint")
		}
		peers = append(peers, *cur)
		cur = nil
		return nil
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)

		// Blank lines between blocks are harmless.
		if line == "" {
			continue
		}

		// Comments: look for our own # Peer cell: <id> annotation.
		if strings.HasPrefix(line, "#") {
			const marker = "# Peer cell: "
			if strings.HasPrefix(line, marker) {
				pendingCellID = strings.TrimPrefix(line, marker)
			}
			continue
		}

		// Section headers.
		if line == "[Interface]" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if line == "[Peer]" {
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &PeerEntry{CellID: pendingCellID}
			pendingCellID = ""
			continue
		}

		// Key = Value pairs.
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			// Ignore unrecognised lines (forward-compatible).
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])

		switch key {
		case "PublicKey":
			if cur != nil {
				cur.PublicKey = val
			}
		case "AllowedIPs":
			if cur != nil {
				parts := strings.Split(val, ", ")
				cur.AllowedIPs = parts
			}
		case "Endpoint":
			if cur != nil {
				cur.Endpoint = val
			}
		}
	}

	if err := flush(); err != nil {
		return nil, err
	}
	return peers, nil
}

// ─── Apply ─────────────────────────────────────────────────────────────────

// Apply always returns ErrUnsupported. Applying a WireGuard configuration to
// a kernel or userspace WireGuard device requires elevated privileges and a
// real WG interface, which this package deliberately does not attempt. Use
// pkg/wireguard (with platform-specific build-tag implementations) for real
// application of mesh configs.
//
// CLAUDE-2 contract: this function MUST NOT return nil. Returning nil would
// be a PASS-bluff (success on a feature that does not work).
func Apply(localCellID string, configText string) error {
	// Suppress "declared and not used" if callers are not yet wired:
	_ = localCellID
	_ = configText
	return ErrUnsupported
}
