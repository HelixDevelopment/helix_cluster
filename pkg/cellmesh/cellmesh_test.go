package cellmesh_test

// Anti-bluff test discipline: each test names the EXACT one-line mutation
// that makes it FAIL and confirms that mutation still COMPILES.

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/cellmesh"
)

// ─── helpers ──────────────────────────────────────────────────────────────

// cellIDs extracts the CellID from each PeerEntry and returns a sorted slice.
func cellIDs(peers []cellmesh.PeerEntry) []string {
	ids := make([]string, len(peers))
	for i, p := range peers {
		ids[i] = p.CellID
	}
	slices.Sort(ids)
	return ids
}

// threeTestCells returns three disjoint CellPeer fixtures with non-overlapping CIDRs.
func threeTestCells() []cellmesh.CellPeer {
	return []cellmesh.CellPeer{
		{
			CellID:    "cell-alpha",
			PublicKey: "pubkey-alpha",
			Endpoint:  "10.100.0.1:51820",
			CIDRs:     []string{"10.1.0.0/16"},
		},
		{
			CellID:    "cell-beta",
			PublicKey: "pubkey-beta",
			Endpoint:  "10.100.0.2:51820",
			CIDRs:     []string{"10.2.0.0/16"},
		},
		{
			CellID:    "cell-gamma",
			PublicKey: "pubkey-gamma",
			Endpoint:  "10.100.0.3:51820",
			CIDRs:     []string{"10.3.0.0/16"},
		},
	}
}

// ─── TestBuildMesh_ThreeCells ─────────────────────────────────────────────
//
// Proves: each cell receives EXACTLY the other two as peers (never self);
// each peer's AllowedIPs == that peer cell's CIDRs (not the local cell's).
//
// Anti-bluff mutation (COMPILES): in BuildMesh, change the self-exclusion
// guard to `if peer.CellID != self.CellID { continue }` (inverted condition)
// -> this test FAILS because peerCount becomes 0 and cellIDs returns [] ≠
// ["cell-beta","cell-gamma"].
//
// Anti-bluff mutation 2 (COMPILES, AllowedIPs): in BuildMesh, replace
// `copy(allowedIPs, peer.CIDRs)` with `copy(allowedIPs, self.CIDRs)` ->
// AllowedIPs assertions below FAIL because every peer would carry the LOCAL
// cell's CIDRs instead of the peer's.
func TestBuildMesh_ThreeCells(t *testing.T) {
	cells := threeTestCells()
	mesh, err := cellmesh.BuildMesh(cells)
	if err != nil {
		t.Fatalf("BuildMesh returned unexpected error: %v", err)
	}

	// Expect one entry per cell.
	if len(mesh) != 3 {
		t.Fatalf("want 3 entries in mesh map, got %d", len(mesh))
	}

	wantPeersOf := map[string][]string{
		"cell-alpha": {"cell-beta", "cell-gamma"},
		"cell-beta":  {"cell-alpha", "cell-gamma"},
		"cell-gamma": {"cell-alpha", "cell-beta"},
	}
	wantCIDRsOf := map[string]string{
		"cell-alpha": "10.1.0.0/16",
		"cell-beta":  "10.2.0.0/16",
		"cell-gamma": "10.3.0.0/16",
	}

	for ownerID, wantPeerIDs := range wantPeersOf {
		peers, ok := mesh[ownerID]
		if !ok {
			t.Errorf("mesh missing entry for %q", ownerID)
			continue
		}

		// EXACT peer count.
		if len(peers) != 2 {
			t.Errorf("cell %q: want 2 peers, got %d", ownerID, len(peers))
			continue
		}

		// EXACT peer IDs (sorted).
		gotIDs := cellIDs(peers)
		if !slices.Equal(gotIDs, wantPeerIDs) {
			t.Errorf("cell %q: peer IDs = %v, want %v", ownerID, gotIDs, wantPeerIDs)
		}

		// Self must NEVER appear as a peer.
		for _, p := range peers {
			if p.CellID == ownerID {
				t.Errorf("cell %q: found self in own peer list", ownerID)
			}
		}

		// Each peer's AllowedIPs must equal THAT PEER CELL's CIDRs, not the local cell's.
		for _, p := range peers {
			wantCIDR := wantCIDRsOf[p.CellID]
			if len(p.AllowedIPs) != 1 || p.AllowedIPs[0] != wantCIDR {
				t.Errorf(
					"cell %q -> peer %q: AllowedIPs = %v, want [%s]",
					ownerID, p.CellID, p.AllowedIPs, wantCIDR,
				)
			}
		}
	}
}

// ─── TestBuildMesh_Deterministic ──────────────────────────────────────────
//
// Proves: output is sorted by CellID regardless of input order.
func TestBuildMesh_Deterministic(t *testing.T) {
	cells := threeTestCells()
	// Provide in reverse alphabetical order.
	rev := []cellmesh.CellPeer{cells[2], cells[1], cells[0]}
	mesh, err := cellmesh.BuildMesh(rev)
	if err != nil {
		t.Fatalf("BuildMesh error: %v", err)
	}
	peers := mesh["cell-alpha"]
	if len(peers) != 2 || peers[0].CellID != "cell-beta" || peers[1].CellID != "cell-gamma" {
		t.Errorf("peer order not deterministic: got %v", cellIDs(peers))
	}
}

// ─── TestBuildMesh_TooFewCells ────────────────────────────────────────────
//
// Proves: fewer than 2 cells is rejected.
func TestBuildMesh_TooFewCells(t *testing.T) {
	_, err := cellmesh.BuildMesh([]cellmesh.CellPeer{
		{CellID: "only", PublicKey: "k", Endpoint: "1.2.3.4:51820", CIDRs: []string{"10.0.0.0/8"}},
	})
	if err == nil {
		t.Fatal("want error for single-cell mesh, got nil")
	}
}

// ─── TestBuildMesh_DuplicateCellID ────────────────────────────────────────
//
// Proves: duplicate CellID is rejected.
func TestBuildMesh_DuplicateCellID(t *testing.T) {
	cells := threeTestCells()
	cells[1].CellID = cells[0].CellID // induce duplicate
	_, err := cellmesh.BuildMesh(cells)
	if err == nil {
		t.Fatal("want error for duplicate CellID, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention 'duplicate', got: %v", err)
	}
}

// ─── TestDetectOverlap_OverlappingCIDRs ───────────────────────────────────
//
// Proves: overlapping CIDRs across cells (10.0.0.0/16 and 10.0.1.0/24
// overlap because 10.0.1.0/24 is entirely within 10.0.0.0/16) yield an
// error naming both cells.
//
// Anti-bluff mutation (COMPILES): in DetectOverlap, replace
// `return fmt.Errorf(...)` with `return nil` -> this test FAILS because
// err is nil instead of non-nil.
func TestDetectOverlap_OverlappingCIDRs(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "cell-a", PublicKey: "ka", Endpoint: "1.0.0.1:51820", CIDRs: []string{"10.0.0.0/16"}},
		{CellID: "cell-b", PublicKey: "kb", Endpoint: "1.0.0.2:51820", CIDRs: []string{"10.0.1.0/24"}},
	}

	err := cellmesh.DetectOverlap(cells)
	if err == nil {
		t.Fatal("want overlap error, got nil")
	}
	// The error must name both cells.
	if !strings.Contains(err.Error(), "cell-a") {
		t.Errorf("error should name 'cell-a', got: %v", err)
	}
	if !strings.Contains(err.Error(), "cell-b") {
		t.Errorf("error should name 'cell-b', got: %v", err)
	}

	// BuildMesh must also reject this.
	_, errBuild := cellmesh.BuildMesh(cells)
	if errBuild == nil {
		t.Fatal("BuildMesh should propagate DetectOverlap error, got nil")
	}
	if !strings.Contains(errBuild.Error(), "cell-a") || !strings.Contains(errBuild.Error(), "cell-b") {
		t.Errorf("BuildMesh error should name both cells, got: %v", errBuild)
	}
}

// ─── TestDetectOverlap_NoOverlap ──────────────────────────────────────────
//
// Proves: disjoint CIDRs produce nil.
func TestDetectOverlap_NoOverlap(t *testing.T) {
	cells := threeTestCells()
	if err := cellmesh.DetectOverlap(cells); err != nil {
		t.Errorf("want nil for disjoint CIDRs, got: %v", err)
	}
}

// ─── TestDetectOverlap_ExactDuplicate ─────────────────────────────────────
//
// Proves: identical prefixes on different cells are detected as overlap.
func TestDetectOverlap_ExactDuplicate(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "x", PublicKey: "kx", Endpoint: "1.0.0.1:51820", CIDRs: []string{"10.0.0.0/16"}},
		{CellID: "y", PublicKey: "ky", Endpoint: "1.0.0.2:51820", CIDRs: []string{"10.0.0.0/16"}},
	}
	if err := cellmesh.DetectOverlap(cells); err == nil {
		t.Fatal("want error for identical prefix across cells, got nil")
	}
}

// ─── TestRenderParseRoundTrip ─────────────────────────────────────────────
//
// Proves: RenderConfig -> ParseConfig is a lossless round-trip: the parsed
// peer set matches the original peer list (CellID, PublicKey, Endpoint,
// AllowedIPs) exactly.
//
// Anti-bluff mutation (COMPILES): in RenderConfig, remove the peer loop body
// (produce only the [Interface] block) -> ParseConfig returns [] and the
// len(got) != len(want) check FAILS.
//
// Anti-bluff mutation 2 (COMPILES): in RenderConfig, always write
// `AllowedIPs = HARDCODED` ignoring p.AllowedIPs -> AllowedIPs equality
// assertion below FAILS.
func TestRenderParseRoundTrip(t *testing.T) {
	cells := threeTestCells()
	mesh, err := cellmesh.BuildMesh(cells)
	if err != nil {
		t.Fatalf("BuildMesh error: %v", err)
	}

	for _, ownerID := range []string{"cell-alpha", "cell-beta", "cell-gamma"} {
		peers := mesh[ownerID]
		rendered := cellmesh.RenderConfig(ownerID, "PRIVKEY-"+ownerID, peers)

		// Rendered text must be non-empty and contain [Interface] and [Peer].
		if !strings.Contains(rendered, "[Interface]") {
			t.Errorf("%s: rendered config missing [Interface]", ownerID)
		}
		if !strings.Contains(rendered, "[Peer]") {
			t.Errorf("%s: rendered config missing [Peer]", ownerID)
		}

		// Parse back.
		got, err := cellmesh.ParseConfig(rendered)
		if err != nil {
			t.Fatalf("%s: ParseConfig error: %v", ownerID, err)
		}

		if len(got) != len(peers) {
			t.Fatalf("%s: round-trip len: want %d, got %d", ownerID, len(peers), len(got))
		}

		// Build lookup by CellID for order-insensitive comparison.
		wantMap := make(map[string]cellmesh.PeerEntry, len(peers))
		for _, p := range peers {
			wantMap[p.CellID] = p
		}
		for _, g := range got {
			w, ok := wantMap[g.CellID]
			if !ok {
				t.Errorf("%s: parsed unexpected CellID %q", ownerID, g.CellID)
				continue
			}
			if g.PublicKey != w.PublicKey {
				t.Errorf("%s -> %s: PublicKey want %q, got %q", ownerID, g.CellID, w.PublicKey, g.PublicKey)
			}
			if g.Endpoint != w.Endpoint {
				t.Errorf("%s -> %s: Endpoint want %q, got %q", ownerID, g.CellID, w.Endpoint, g.Endpoint)
			}
			if !slices.Equal(g.AllowedIPs, w.AllowedIPs) {
				t.Errorf("%s -> %s: AllowedIPs want %v, got %v", ownerID, g.CellID, w.AllowedIPs, g.AllowedIPs)
			}
		}
	}
}

// ─── TestRenderConfig_MultipleCIDRs ──────────────────────────────────────
//
// Proves: multiple CIDRs per peer are joined with ", " and survive the
// round-trip.
func TestRenderConfig_MultipleCIDRs(t *testing.T) {
	peers := []cellmesh.PeerEntry{
		{
			CellID:     "cell-x",
			PublicKey:  "pubkey-x",
			Endpoint:   "1.2.3.4:51820",
			AllowedIPs: []string{"10.10.0.0/16", "172.16.0.0/12"},
		},
	}
	rendered := cellmesh.RenderConfig("local", "local-priv", peers)
	got, err := cellmesh.ParseConfig(rendered)
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 peer, got %d", len(got))
	}
	if !slices.Equal(got[0].AllowedIPs, peers[0].AllowedIPs) {
		t.Errorf("AllowedIPs round-trip: want %v, got %v", peers[0].AllowedIPs, got[0].AllowedIPs)
	}
}

// ─── TestApply_ReturnsErrUnsupported ─────────────────────────────────────
//
// Proves: Apply returns ErrUnsupported (non-nil, exact sentinel match via
// errors.Is). This enforces the CLAUDE-2 contract that Apply MUST NOT fake
// success when no real WireGuard device is available.
//
// Anti-bluff mutation (COMPILES): in Apply, change `return ErrUnsupported`
// to `return nil` -> errors.Is(err, ErrUnsupported) becomes false AND the
// `err != nil` guard triggers t.Fatal, so the test FAILS on both checks.
func TestApply_ReturnsErrUnsupported(t *testing.T) {
	err := cellmesh.Apply("cell-alpha", "[Interface]\nPrivateKey = foo\n")

	// Must be non-nil (no fake success).
	if err == nil {
		t.Fatal("Apply returned nil; expected ErrUnsupported (CLAUDE-2 violation: fake success)")
	}

	// Must be the exact sentinel — not just any error.
	if !errors.Is(err, cellmesh.ErrUnsupported) {
		t.Errorf("Apply error = %v; want errors.Is(err, ErrUnsupported) == true", err)
	}
}

// ─── TestApply_SentinelIdentity ───────────────────────────────────────────
//
// Proves: ErrUnsupported is a stable package-level sentinel, not a fresh
// error allocation per call, so errors.Is works across call sites.
func TestApply_SentinelIdentity(t *testing.T) {
	e1 := cellmesh.Apply("c1", "cfg")
	e2 := cellmesh.Apply("c2", "cfg")
	if !errors.Is(e1, e2) {
		t.Errorf("two Apply calls returned different sentinel values")
	}
	if !errors.Is(e1, cellmesh.ErrUnsupported) {
		t.Errorf("Apply result is not errors.Is(ErrUnsupported)")
	}
}

// ─── TestBuildMesh_AllowedIPsNeverSelf ────────────────────────────────────
//
// Explicit sink-side proof that AllowedIPs in every peer entry never contains
// any CIDR of the LOCAL cell — only the PEER cell's CIDRs.
//
// The self-CIDR set is built dynamically from the input cells so there is no
// brittle coupling between hardcoded maps and cell IDs: any change to
// threeTestCells() is automatically reflected here.
//
// Anti-bluff mutation (COMPILES): in BuildMesh set
// `allowedIPs = make([]string, len(self.CIDRs)); copy(allowedIPs, self.CIDRs)`
// -> a self-CIDR appears in AllowedIPs and the check below FAILS because
// selfCIDRs[ownerID] contains that exact CIDR.
func TestBuildMesh_AllowedIPsNeverSelf(t *testing.T) {
	cells := threeTestCells()

	// Build a set of CIDRs per cell from the INPUT so the test is
	// self-consistent regardless of cell IDs or CIDR values.
	selfCIDRs := make(map[string]map[string]bool, len(cells))
	for _, c := range cells {
		set := make(map[string]bool, len(c.CIDRs))
		for _, cidr := range c.CIDRs {
			set[cidr] = true
		}
		selfCIDRs[c.CellID] = set
	}

	mesh, err := cellmesh.BuildMesh(cells)
	if err != nil {
		t.Fatalf("BuildMesh error: %v", err)
	}

	for ownerID, peers := range mesh {
		ownCIDRs, ok := selfCIDRs[ownerID]
		if !ok {
			t.Errorf("mesh contains ownerID %q not present in input cells", ownerID)
			continue
		}
		for _, p := range peers {
			for _, ai := range p.AllowedIPs {
				if ownCIDRs[ai] {
					t.Errorf(
						"cell %q: peer %q AllowedIPs contains the LOCAL cell's CIDR %q",
						ownerID, p.CellID, ai,
					)
				}
			}
		}
	}
}

// ─── TestBuildMesh_AllowedIPsEquality ─────────────────────────────────────
//
// Proves: the AllowedIPs of each PeerEntry in the mesh is byte-for-byte equal
// to the corresponding CellPeer.CIDRs (original order preserved, no mutation).
func TestBuildMesh_AllowedIPsEquality(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "c1", PublicKey: "k1", Endpoint: "1.0.0.1:51820", CIDRs: []string{"10.1.0.0/16", "192.168.1.0/24"}},
		{CellID: "c2", PublicKey: "k2", Endpoint: "1.0.0.2:51820", CIDRs: []string{"10.2.0.0/16"}},
	}
	mesh, err := cellmesh.BuildMesh(cells)
	if err != nil {
		t.Fatalf("BuildMesh error: %v", err)
	}

	// From c2's perspective, its peer c1 must have AllowedIPs == c1.CIDRs.
	peersOfC2 := mesh["c2"]
	if len(peersOfC2) != 1 {
		t.Fatalf("c2 should have 1 peer, got %d", len(peersOfC2))
	}
	gotAIPs := peersOfC2[0].AllowedIPs
	wantAIPs := []string{"10.1.0.0/16", "192.168.1.0/24"}
	if !slices.Equal(gotAIPs, wantAIPs) {
		t.Errorf("c2 peer c1 AllowedIPs: want %v, got %v", wantAIPs, gotAIPs)
	}
}

// ─── TestParseConfig_MalformedMissingPubKey ───────────────────────────────
//
// Proves: ParseConfig returns an error when a [Peer] block has no PublicKey.
func TestParseConfig_MalformedMissingPubKey(t *testing.T) {
	cfg := "[Interface]\nPrivateKey = x\n\n[Peer]\nEndpoint = 1.2.3.4:51820\nAllowedIPs = 10.0.0.0/8\n"
	_, err := cellmesh.ParseConfig(cfg)
	if err == nil {
		t.Fatal("want error for missing PublicKey, got nil")
	}
}

// ─── TestParseConfig_MalformedMissingAllowedIPs ───────────────────────────
//
// Proves: ParseConfig returns an error when a [Peer] block has no AllowedIPs,
// enforcing the godoc contract: "ParseConfig returns an error if a [Peer]
// block is malformed (missing PublicKey, AllowedIPs, or Endpoint)."
//
// Without the AllowedIPs validation in flush(), ParseConfig would silently
// accept this block and return a PeerEntry with nil AllowedIPs — which
// RenderConfig would serialize as "AllowedIPs = " (empty), producing an
// invalid wg-quick config rejected by the real WireGuard toolchain.
//
// Anti-bluff mutation (COMPILES): in flush(), remove the AllowedIPs check
// `if len(cur.AllowedIPs) == 0 { return fmt.Errorf(...) }` -> err is nil
// and this test FAILS on the `err == nil` guard.
func TestParseConfig_MalformedMissingAllowedIPs(t *testing.T) {
	cfg := "[Interface]\nPrivateKey = x\n\n[Peer]\nPublicKey = k\nEndpoint = 1.2.3.4:51820\n"
	_, err := cellmesh.ParseConfig(cfg)
	if err == nil {
		t.Fatal("want error for missing AllowedIPs, got nil")
	}
	if !strings.Contains(err.Error(), "AllowedIPs") {
		t.Errorf("error should mention 'AllowedIPs', got: %v", err)
	}
}
