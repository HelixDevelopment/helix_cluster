package cellmesh_test

// Adversarial audit of pkg/cellmesh focused on the dangerous failure modes:
//
//   1. DetectOverlap FALSE NEGATIVE — a pair of cells whose CIDRs genuinely
//      overlap (partial-containment, full-containment, identical, default
//      route, IPv6) reported as non-overlapping. This is the linter-style
//      "miss a real defect" direction and is the centerpiece below.
//   2. DetectOverlap FALSE POSITIVE — adjacent-but-disjoint / exact-touch
//      ranges flagged as overlapping. Pins the inclusive/exclusive boundary.
//   3. Render -> Parse round-trip losing or corrupting a field (CellID,
//      PublicKey, Endpoint, AllowedIPs — including multi-CIDR ordering).
//   4. BuildMesh topology — exact peer set / AllowedIPs sourcing.
//
// Anti-bluff discipline: each centerpiece test documents the exact one-line
// production mutation that makes it FAIL, and that mutation was confirmed to
// compile and to bite during this audit.

import (
	"slices"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/cellmesh"
)

// ─── 2a. OVERLAP TRUE-POSITIVE (centerpiece) ──────────────────────────────
//
// Every one of these pairs GENUINELY overlaps; DetectOverlap MUST report each.
// A miss here is the dangerous false negative. The table spans partial overlap
// (containment), full containment, identical, default-route superset, single
// /32 host identity, /31-contains-/32, and IPv6 containment.
//
// Anti-bluff mutation (COMPILES, bites this test): in DetectOverlap flip the
// detector to `if !a.prefix.Overlaps(b.prefix)` — confirmed: with the flip the
// genuinely-overlapping pairs return nil and every sub-case below FAILS.
func TestDetectOverlap_TruePositiveMatrix(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"identical /16", "10.0.0.0/16", "10.0.0.0/16"},
		{"containment /16 ⊃ /24", "10.0.0.0/16", "10.0.1.0/24"},
		{"containment reversed /24 ⊂ /16", "10.0.7.0/24", "10.0.0.0/16"},
		{"default route ⊃ /8", "0.0.0.0/0", "10.0.0.0/8"},
		{"identical /32 host", "10.0.0.5/32", "10.0.0.5/32"},
		{"/31 contains /32", "10.0.0.0/31", "10.0.0.1/32"},
		{"ipv6 /48 ⊃ /64", "fd00:a::/48", "fd00:a:0:1::/64"},
		{"ipv6 default ⊃ /8", "::/0", "fd00::/8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cells := []cellmesh.CellPeer{
				{CellID: "cell-a", PublicKey: "ka", Endpoint: "1.0.0.1:51820", CIDRs: []string{tc.a}},
				{CellID: "cell-b", PublicKey: "kb", Endpoint: "1.0.0.2:51820", CIDRs: []string{tc.b}},
			}
			err := cellmesh.DetectOverlap(cells)
			if err == nil {
				t.Fatalf("FALSE NEGATIVE: %s vs %s overlap but DetectOverlap returned nil", tc.a, tc.b)
			}
			// Error must name both cells and both prefixes (operator must locate the conflict).
			for _, want := range []string{"cell-a", "cell-b", tc.a, tc.b} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("overlap error %q must mention %q", err.Error(), want)
				}
			}
			// BuildMesh must refuse to build a topology with an overlapping pair.
			if _, be := cellmesh.BuildMesh(cells); be == nil {
				t.Errorf("BuildMesh accepted overlapping CIDRs %s / %s", tc.a, tc.b)
			}
		})
	}
}

// ─── 2a'. OVERLAP HIDDEN IN A MULTI-CIDR CELL ─────────────────────────────
//
// The overlapping prefix is the SECOND entry of a cell that advertises several
// disjoint prefixes, and the conflicting cell is at a non-adjacent index. This
// pins that the O(n²) pairwise scan does not skip any (cell, prefix) pair.
func TestDetectOverlap_HiddenPairInMultiCIDR(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "a", PublicKey: "ka", Endpoint: "1.0.0.1:51820", CIDRs: []string{"10.0.0.0/16", "192.168.0.0/16"}},
		{CellID: "b", PublicKey: "kb", Endpoint: "1.0.0.2:51820", CIDRs: []string{"172.16.0.0/12"}},
		{CellID: "c", PublicKey: "kc", Endpoint: "1.0.0.3:51820", CIDRs: []string{"203.0.113.0/24", "192.168.5.0/24"}}, // 192.168.5/24 ⊂ a's 192.168/16
	}
	err := cellmesh.DetectOverlap(cells)
	if err == nil {
		t.Fatal("FALSE NEGATIVE: 192.168.5.0/24 (cell c) ⊂ 192.168.0.0/16 (cell a) not detected")
	}
	if !strings.Contains(err.Error(), "192.168") {
		t.Errorf("error should name the 192.168 conflict, got: %v", err)
	}
}

// ─── 2b. OVERLAP TRUE-NEGATIVE / BOUNDARY (centerpiece) ───────────────────
//
// Adjacent-but-disjoint and clearly-separate ranges MUST NOT be flagged. This
// pins the exact boundary: half-open subnets that merely TOUCH at an edge
// (e.g. 10.0.0.0/24 ends, 10.0.1.0/24 begins) are NOT an overlap, and a /32
// host adjacent to another /32 is NOT an overlap. IPv4-vs-IPv6 never overlap.
//
// Anti-bluff mutation (COMPILES, bites this test): in DetectOverlap flip the
// detector to `if !a.prefix.Overlaps(b.prefix)` — confirmed: with the flip
// these disjoint pairs are wrongly reported as overlapping and every sub-case
// FAILS (a false positive).
func TestDetectOverlap_BoundaryTrueNegativeMatrix(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"adjacent /24 touch", "10.0.0.0/24", "10.0.1.0/24"},
		{"adjacent /16 touch", "10.0.0.0/16", "10.1.0.0/16"},
		{"adjacent /32 hosts", "10.0.0.0/32", "10.0.0.1/32"},
		{"split /1 halves", "0.0.0.0/1", "128.0.0.0/1"},
		{"v4 vs v6 never overlap", "10.0.0.0/8", "fd00::/8"},
		{"v6 disjoint /64", "fd00:a::/64", "fd00:b::/64"},
		{"far-apart prefixes", "10.0.0.0/16", "203.0.113.0/24"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cells := []cellmesh.CellPeer{
				{CellID: "cell-a", PublicKey: "ka", Endpoint: "1.0.0.1:51820", CIDRs: []string{tc.a}},
				{CellID: "cell-b", PublicKey: "kb", Endpoint: "1.0.0.2:51820", CIDRs: []string{tc.b}},
			}
			if err := cellmesh.DetectOverlap(cells); err != nil {
				t.Fatalf("FALSE POSITIVE: disjoint %s / %s flagged as overlap: %v", tc.a, tc.b, err)
			}
			// And BuildMesh must succeed on a fully-disjoint topology.
			if _, be := cellmesh.BuildMesh(cells); be != nil {
				t.Errorf("BuildMesh rejected disjoint CIDRs %s / %s: %v", tc.a, tc.b, be)
			}
		})
	}
}

// ─── 2b'. INTRA-CELL OVERLAP IS DELIBERATELY IGNORED ──────────────────────
//
// Per the godoc contract, two overlapping prefixes owned by the SAME cell are
// NOT an error (only cross-cell overlap matters). This pins that documented
// asymmetry so a future "tidy-up" that drops the same-cell guard is caught.
//
// Anti-bluff mutation (COMPILES, bites this test): in DetectOverlap delete the
// `if a.cellID == b.cellID { continue }` guard — confirmed: the same-cell
// overlapping pair is then reported and this test FAILS.
func TestDetectOverlap_IntraCellOverlapIgnored(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "a", PublicKey: "ka", Endpoint: "1.0.0.1:51820", CIDRs: []string{"10.0.0.0/16", "10.0.1.0/24"}}, // self-overlap
		{CellID: "b", PublicKey: "kb", Endpoint: "1.0.0.2:51820", CIDRs: []string{"192.168.0.0/16"}},
	}
	if err := cellmesh.DetectOverlap(cells); err != nil {
		t.Fatalf("intra-cell overlap must be ignored, got error: %v", err)
	}
}

// ─── 2b”. INVALID CIDR SURFACES AS ERROR ─────────────────────────────────
//
// A malformed prefix must be rejected with a named error, not silently treated
// as non-overlapping (which would be a false negative by omission).
func TestDetectOverlap_InvalidCIDRRejected(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "a", PublicKey: "ka", Endpoint: "1.0.0.1:51820", CIDRs: []string{"not-a-cidr"}},
		{CellID: "b", PublicKey: "kb", Endpoint: "1.0.0.2:51820", CIDRs: []string{"10.0.0.0/16"}},
	}
	err := cellmesh.DetectOverlap(cells)
	if err == nil {
		t.Fatal("invalid CIDR must produce an error, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-cidr") {
		t.Errorf("error should name the bad CIDR, got: %v", err)
	}
}

// ─── 3. ROUND-TRIP FIDELITY (centerpiece) ─────────────────────────────────
//
// A fully-populated peer set (distinct CellID / PublicKey / Endpoint, and a
// multi-CIDR AllowedIPs whose ORDER matters) must survive RenderConfig ->
// ParseConfig byte-for-byte per field. Order-preserving comparison (not a
// set) catches any reordering or de-duplication in either direction.
//
// Anti-bluff mutation (COMPILES, bites this test): in RenderConfig change the
// AllowedIPs join separator from ", " to "," — confirmed: ParseConfig splits
// on ", " and yields a single fused token instead of two CIDRs, so the
// AllowedIPs equality assertion FAILS.
func TestRenderParse_FullFidelityOrdered(t *testing.T) {
	want := []cellmesh.PeerEntry{
		{
			CellID:     "cell-zeta",
			PublicKey:  "PUBKEY-ZETA-base64==",
			Endpoint:   "203.0.113.9:51821",
			AllowedIPs: []string{"10.9.0.0/16", "172.20.0.0/14", "fd00:9::/48"},
		},
		{
			CellID:     "cell-eta",
			PublicKey:  "PUBKEY-ETA-base64==",
			Endpoint:   "[2001:db8::1]:51820",
			AllowedIPs: []string{"10.8.0.0/16"},
		},
	}

	rendered := cellmesh.RenderConfig("cell-local", "PRIV-LOCAL", want)
	got, err := cellmesh.ParseConfig(rendered)
	if err != nil {
		t.Fatalf("ParseConfig error on well-formed render: %v\n%s", err, rendered)
	}
	if len(got) != len(want) {
		t.Fatalf("round-trip length: want %d peers, got %d\nrendered:\n%s", len(want), len(got), rendered)
	}

	// Peer ORDER is preserved by Render/Parse (Render emits in slice order,
	// Parse appends in encounter order) — assert index-aligned equality.
	for i := range want {
		w, g := want[i], got[i]
		if g.CellID != w.CellID {
			t.Errorf("peer[%d] CellID: want %q, got %q", i, w.CellID, g.CellID)
		}
		if g.PublicKey != w.PublicKey {
			t.Errorf("peer[%d] PublicKey: want %q, got %q", i, w.PublicKey, g.PublicKey)
		}
		if g.Endpoint != w.Endpoint {
			t.Errorf("peer[%d] Endpoint: want %q, got %q", i, w.Endpoint, g.Endpoint)
		}
		if !slices.Equal(g.AllowedIPs, w.AllowedIPs) {
			t.Errorf("peer[%d] AllowedIPs (ordered): want %v, got %v", i, w.AllowedIPs, g.AllowedIPs)
		}
	}
}

// ─── 3'. CellID ROUND-TRIP DEPENDS ON THE COMMENT MARKER ──────────────────
//
// CellID is carried ONLY by the "# Peer cell: <id>" comment (it is not a
// wg-quick key). This pins that the comment is the round-trip channel for
// CellID, and that an unusual-but-realistic ID (colon, embedded spaces)
// survives intact.
//
// Anti-bluff mutation (COMPILES, bites this test): in RenderConfig change the
// comment to `"\n# Peer: %s\n"` (drop the " cell:" the parser matches) —
// confirmed: ParseConfig no longer recovers CellID, every parsed CellID is ""
// and the assertion FAILS.
func TestRenderParse_CellIDViaComment(t *testing.T) {
	want := []cellmesh.PeerEntry{
		{CellID: "cell:weird id-1", PublicKey: "k1", Endpoint: "1.2.3.4:51820", AllowedIPs: []string{"10.1.0.0/16"}},
		{CellID: "cell-plain", PublicKey: "k2", Endpoint: "5.6.7.8:51820", AllowedIPs: []string{"10.2.0.0/16"}},
	}
	got, err := cellmesh.ParseConfig(cellmesh.RenderConfig("local", "priv", want))
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("want %d peers, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i].CellID != want[i].CellID {
			t.Errorf("peer[%d] CellID round-trip: want %q, got %q", i, want[i].CellID, got[i].CellID)
		}
	}
}

// ─── 3”. ROUND-TRIP IS STABLE UNDER RE-RENDER (idempotent) ───────────────
//
// Render(Parse(Render(x))) == Render(x): parsing then re-rendering reproduces
// the identical wire text. Guards against any asymmetric whitespace/separator
// handling that a single Render->Parse might mask.
func TestRenderParse_ReRenderStable(t *testing.T) {
	peers := []cellmesh.PeerEntry{
		{CellID: "c1", PublicKey: "k1", Endpoint: "1.1.1.1:51820", AllowedIPs: []string{"10.1.0.0/16", "10.11.0.0/16"}},
		{CellID: "c2", PublicKey: "k2", Endpoint: "2.2.2.2:51820", AllowedIPs: []string{"10.2.0.0/16"}},
	}
	first := cellmesh.RenderConfig("local", "priv", peers)
	parsed, err := cellmesh.ParseConfig(first)
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}
	second := cellmesh.RenderConfig("local", "priv", parsed)
	if first != second {
		t.Errorf("re-render not stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// ─── 3”'. MALFORMED CONFIG ERRORS, NEVER SILENT-PARTIAL ──────────────────
//
// Each malformed [Peer] block (missing PublicKey / AllowedIPs / Endpoint) must
// produce an error rather than a partially-populated PeerEntry. A silent
// partial parse would later render an invalid wg-quick config.
func TestParseConfig_MalformedNeverSilentPartial(t *testing.T) {
	cases := []struct {
		name, cfg, mustMention string
	}{
		{
			name:        "missing PublicKey",
			cfg:         "[Interface]\nPrivateKey = x\n\n[Peer]\nAllowedIPs = 10.0.0.0/8\nEndpoint = 1.2.3.4:51820\n",
			mustMention: "PublicKey",
		},
		{
			name:        "missing AllowedIPs",
			cfg:         "[Interface]\nPrivateKey = x\n\n[Peer]\nPublicKey = k\nEndpoint = 1.2.3.4:51820\n",
			mustMention: "AllowedIPs",
		},
		{
			name:        "missing Endpoint",
			cfg:         "[Interface]\nPrivateKey = x\n\n[Peer]\nPublicKey = k\nAllowedIPs = 10.0.0.0/8\n",
			mustMention: "Endpoint",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cellmesh.ParseConfig(tc.cfg)
			if err == nil {
				t.Fatalf("malformed block (%s) must error, got peers=%+v", tc.name, got)
			}
			if got != nil {
				t.Errorf("on error, returned peers should be nil, got %+v", got)
			}
			if !strings.Contains(err.Error(), tc.mustMention) {
				t.Errorf("error should mention %q, got: %v", tc.mustMention, err)
			}
		})
	}
}

// ─── 4. BUILD CORRECTNESS — peer set + AllowedIPs sourcing ────────────────
//
// For an N-cell mesh each cell sees EXACTLY the other N-1 cells (never self),
// and each PeerEntry.AllowedIPs equals THAT peer cell's CIDRs (not the local
// cell's). Verified across a 4-cell mesh so an off-by-one in the self-exclusion
// would surface.
//
// Anti-bluff mutation (COMPILES, bites this test): in BuildMesh invert the
// self guard to `if peer.CellID != self.CellID { continue }` — confirmed: peer
// lists become empty (or self-only) and the exact-peer-set assertion FAILS.
func TestBuildMesh_FourCellExactTopology(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "c-a", PublicKey: "ka", Endpoint: "10.0.0.1:51820", CIDRs: []string{"10.1.0.0/16"}},
		{CellID: "c-b", PublicKey: "kb", Endpoint: "10.0.0.2:51820", CIDRs: []string{"10.2.0.0/16", "10.20.0.0/16"}},
		{CellID: "c-c", PublicKey: "kc", Endpoint: "10.0.0.3:51820", CIDRs: []string{"10.3.0.0/16"}},
		{CellID: "c-d", PublicKey: "kd", Endpoint: "10.0.0.4:51820", CIDRs: []string{"10.4.0.0/16"}},
	}
	cidrOf := map[string][]string{}
	pkOf := map[string]string{}
	epOf := map[string]string{}
	for _, c := range cells {
		cidrOf[c.CellID] = c.CIDRs
		pkOf[c.CellID] = c.PublicKey
		epOf[c.CellID] = c.Endpoint
	}

	mesh, err := cellmesh.BuildMesh(cells)
	if err != nil {
		t.Fatalf("BuildMesh error: %v", err)
	}
	if len(mesh) != len(cells) {
		t.Fatalf("want %d mesh entries, got %d", len(cells), len(mesh))
	}

	for owner, peers := range mesh {
		if len(peers) != len(cells)-1 {
			t.Errorf("cell %q: want %d peers, got %d", owner, len(cells)-1, len(peers))
		}
		seen := map[string]bool{}
		for _, p := range peers {
			if p.CellID == owner {
				t.Errorf("cell %q: self appears in its own peer list", owner)
			}
			if seen[p.CellID] {
				t.Errorf("cell %q: duplicate peer %q", owner, p.CellID)
			}
			seen[p.CellID] = true

			// AllowedIPs / PublicKey / Endpoint must be the PEER's, exactly.
			if !slices.Equal(p.AllowedIPs, cidrOf[p.CellID]) {
				t.Errorf("cell %q -> peer %q: AllowedIPs %v, want peer's CIDRs %v",
					owner, p.CellID, p.AllowedIPs, cidrOf[p.CellID])
			}
			if p.PublicKey != pkOf[p.CellID] {
				t.Errorf("cell %q -> peer %q: PublicKey %q, want %q", owner, p.CellID, p.PublicKey, pkOf[p.CellID])
			}
			if p.Endpoint != epOf[p.CellID] {
				t.Errorf("cell %q -> peer %q: Endpoint %q, want %q", owner, p.CellID, p.Endpoint, epOf[p.CellID])
			}
		}
	}
}

// ─── 4'. BUILD AllowedIPs IS A COPY, NOT AN ALIAS ─────────────────────────
//
// Mutating the returned AllowedIPs slice must not corrupt a sibling peer entry
// or the caller's input — BuildMesh copies CIDRs per the godoc.
func TestBuildMesh_AllowedIPsIsDefensiveCopy(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "a", PublicKey: "ka", Endpoint: "1.0.0.1:51820", CIDRs: []string{"10.1.0.0/16"}},
		{CellID: "b", PublicKey: "kb", Endpoint: "1.0.0.2:51820", CIDRs: []string{"10.2.0.0/16"}},
	}
	mesh, err := cellmesh.BuildMesh(cells)
	if err != nil {
		t.Fatalf("BuildMesh error: %v", err)
	}
	// Corrupt the AllowedIPs that b holds for peer a.
	mesh["b"][0].AllowedIPs[0] = "0.0.0.0/0"
	// a's original input CIDRs must be untouched.
	if cells[0].CIDRs[0] != "10.1.0.0/16" {
		t.Errorf("BuildMesh aliased input CIDRs: cells[0].CIDRs[0] = %q", cells[0].CIDRs[0])
	}
}

// ─── 5. EDGES — empty / single / max range ────────────────────────────────

// Empty and single-cell meshes are rejected (need ≥2 cells to form a mesh).
func TestBuildMesh_EdgeEmptyAndSingle(t *testing.T) {
	if _, err := cellmesh.BuildMesh(nil); err == nil {
		t.Error("empty mesh must error")
	}
	if _, err := cellmesh.BuildMesh([]cellmesh.CellPeer{}); err == nil {
		t.Error("zero-length mesh must error")
	}
	single := []cellmesh.CellPeer{{CellID: "solo", PublicKey: "k", Endpoint: "1.2.3.4:51820", CIDRs: []string{"10.0.0.0/8"}}}
	if _, err := cellmesh.BuildMesh(single); err == nil {
		t.Error("single-cell mesh must error")
	}
}

// DetectOverlap on the maximal range vs any non-empty subset is an overlap;
// the full-space prefix is the strongest superset and must never be missed.
func TestDetectOverlap_MaxRangeContainsEverything(t *testing.T) {
	for _, sub := range []string{"10.0.0.0/8", "192.168.1.0/24", "203.0.113.7/32"} {
		cells := []cellmesh.CellPeer{
			{CellID: "world", PublicKey: "kw", Endpoint: "1.0.0.1:51820", CIDRs: []string{"0.0.0.0/0"}},
			{CellID: "sub", PublicKey: "ks", Endpoint: "1.0.0.2:51820", CIDRs: []string{sub}},
		}
		if err := cellmesh.DetectOverlap(cells); err == nil {
			t.Errorf("0.0.0.0/0 must overlap %s, got nil", sub)
		}
	}
}

// DetectOverlap and BuildMesh tolerate a single cell with zero CIDRs paired
// with a normal cell (no prefix to conflict) — documents that an empty CIDR
// set is not itself an overlap.
func TestDetectOverlap_ZeroCIDRCellIsNoOverlap(t *testing.T) {
	cells := []cellmesh.CellPeer{
		{CellID: "a", PublicKey: "ka", Endpoint: "1.0.0.1:51820", CIDRs: nil},
		{CellID: "b", PublicKey: "kb", Endpoint: "1.0.0.2:51820", CIDRs: []string{"10.0.0.0/16"}},
	}
	if err := cellmesh.DetectOverlap(cells); err != nil {
		t.Errorf("zero-CIDR cell must not produce overlap, got: %v", err)
	}
}

// Sanity: a single empty config (interface only, no peers) parses to zero
// peers without error — the empty mesh is representable on the wire.
func TestParseConfig_InterfaceOnly(t *testing.T) {
	peers, err := cellmesh.ParseConfig("# CellMesh config for cell: solo\n[Interface]\nPrivateKey = x\n")
	if err != nil {
		t.Fatalf("interface-only config must parse cleanly, got: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("interface-only config should yield 0 peers, got %d", len(peers))
	}
}
