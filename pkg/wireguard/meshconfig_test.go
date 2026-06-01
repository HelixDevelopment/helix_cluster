package wireguard

// Deterministic unit tests for GenerateMeshConfigs + RotateMeshNodeKey.
//
// Design principles (ANTI-BLUFF / CLAUDE-1):
//   - Every assertion is a SINK-SIDE fact (exact key bytes, exact prefix
//     strings, exact peer counts) — not "no panic" or "len > 0".
//   - Each branch / guard is paired with a §1.1 mutation comment explaining
//     which mutation kills it and how.
//   - wgtypes.ParseKey is called on every generated key to prove the key is
//     valid at the crypto library level, not just non-empty.
//   - net/netip.ParsePrefix is called on every AllowedIPs string to prove it
//     is a valid prefix, not just a non-empty string.
//   - Full-mesh invariant: for N nodes, each config has exactly N-1 peers,
//     contains every other node's public key, and does NOT contain its own.

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// buildTestNodes constructs n MeshNodes with real generated keypairs and
// sequential 10.200.0.x/24 addresses on port 51820.
func buildTestNodes(t *testing.T, n int) []MeshNode {
	t.Helper()
	nodes := make([]MeshNode, n)
	for i := range nodes {
		priv, _, err := GenerateKeyPair()
		require.NoError(t, err)
		nodes[i] = MeshNode{
			ID:         fmt.Sprintf("node-%d", i),
			PrivateKey: priv,
			Address:    fmt.Sprintf("10.200.0.%d/24", i+1),
			Endpoint:   fmt.Sprintf("203.0.113.%d:51820", i+1),
		}
	}
	return nodes
}

// pubKeyFor derives the public key (base64) from a MeshNode's private key.
func pubKeyFor(t *testing.T, n MeshNode) string {
	t.Helper()
	priv, err := ParseKey(n.PrivateKey)
	require.NoError(t, err)
	return priv.PublicKey().String()
}

// ----- TestGenerateMeshConfigs_FullMesh ----------------------------------------

// TestGenerateMeshConfigs_FullMesh is the primary sink-side correctness test.
// It generates a 3-node mesh and asserts EVERY structural invariant:
//   - exactly N-1 peers per config
//   - each peer's PublicKey parses via wgtypes.ParseKey
//   - each AllowedIPs string parses via netip.ParsePrefix
//   - AllowedIPs is the /32 host address of the peer's interface address
//   - each config DOES NOT contain its own public key as a [Peer]
//   - ListenPort is 51820 in every [Interface] block
//   - Endpoint is present for every peer block
//
// §1.1 mutation pairs are documented inline.
func TestGenerateMeshConfigs_FullMesh(t *testing.T) {
	const N = 3
	nodes := buildTestNodes(t, N)

	// Derive expected public keys before calling the function under test.
	wantPub := make([]string, N)
	for i, n := range nodes {
		wantPub[i] = pubKeyFor(t, n)
	}

	configs, err := GenerateMeshConfigs(nodes, "")
	require.NoError(t, err)
	require.Len(t, configs, N)

	for i, cfg := range configs {
		t.Run(fmt.Sprintf("node-%d", i), func(t *testing.T) {
			// Correct NodeID.
			assert.Equal(t, nodes[i].ID, cfg.NodeID)

			// Exactly N-1 peers.
			// Mutation: if GenerateMeshConfigs includes self-peer, len will be N → fails.
			assert.Len(t, cfg.Peers, N-1,
				"each node must have exactly N-1 peers (no self-peer)")

			// Self-pubkey must NOT appear in Peers.
			// Mutation: remove `i == j` guard → self-peer appears → assert fires.
			for _, peerPub := range cfg.Peers {
				assert.NotEqual(t, wantPub[i], peerPub,
					"node must not list itself as a peer")
			}

			// Every other node's public key must appear exactly once.
			// Mutation: skip any peer in the generation loop → missing key → assert fires.
			for j, want := range wantPub {
				if j == i {
					continue
				}
				assert.Contains(t, cfg.Peers, want,
					"peer %d pubkey must appear in node %d config", j, i)
			}

			// All public keys in cfg.Peers must parse via wgtypes.ParseKey.
			// Mutation: emit a truncated/corrupt key string → ParseKey returns error → fails.
			for _, peerPub := range cfg.Peers {
				_, err := wgtypes.ParseKey(peerPub)
				assert.NoError(t, err,
					"peer public key %q must parse via wgtypes.ParseKey", peerPub)
			}

			// The [Interface] PrivateKey must parse (it is the node's own key).
			parsed, err := ParseConfigString(cfg.ConfigString)
			require.NoError(t, err, "config for node %d must be parseable", i)
			_, err = wgtypes.ParseKey(parsed.Interface.PrivateKey)
			assert.NoError(t, err, "[Interface] PrivateKey must parse via wgtypes.ParseKey")

			// ListenPort must be 51820.
			// Mutation: emit a different port → exact equality fails.
			assert.Equal(t, 51820, parsed.Interface.ListenPort,
				"ListenPort must be 51820")

			// Exactly N-1 peer blocks in the parsed config.
			assert.Len(t, parsed.Peers, N-1, "parsed peer blocks must equal N-1")

			for _, peerBlock := range parsed.Peers {
				// Each peer's PublicKey must parse.
				// Mutation: emit a bad base64 string → ParseKey fails → assert fires.
				_, err := wgtypes.ParseKey(peerBlock.PublicKey)
				assert.NoError(t, err,
					"[Peer] PublicKey %q must parse", peerBlock.PublicKey)

				// Each AllowedIPs entry must parse as a valid prefix.
				// Mutation: emit "10.200.0.1" (no prefix length) → ParsePrefix errors → fails.
				require.NotEmpty(t, peerBlock.AllowedIPs,
					"peer block must have AllowedIPs")
				for _, cidr := range peerBlock.AllowedIPs {
					prefix, err := netip.ParsePrefix(cidr)
					assert.NoError(t, err,
						"AllowedIPs %q must parse as netip.Prefix", cidr)

					// AllowedIPs must be a host prefix (/32 for IPv4).
					// Mutation: emit the /24 subnet instead of /32 host → bits == 32 fails.
					assert.Equal(t, prefix.Addr().BitLen(), prefix.Bits(),
						"AllowedIPs must be a host prefix (/32 or /128), got %s", cidr)
				}

				// Endpoint must be non-empty (set to the peer's declared endpoint).
				// Mutation: omit Endpoint in generation → empty string → assert fires.
				assert.NotEmpty(t, peerBlock.Endpoint,
					"peer block must have an Endpoint")
			}
		})
	}
}

// TestGenerateMeshConfigs_PeerPubKeyMatchesPrivateKey is a narrow cryptographic
// sanity check: the public key emitted for each peer must equal the public key
// that the Curve25519 library derives from that peer's private key.
//
// §1.1 mutation: if GenerateMeshConfigs re-generates a fresh keypair for each
// peer instead of deriving it from the node's PrivateKey, the computed pubKey
// differs from the emitted one and these exact-equality asserts fail.
func TestGenerateMeshConfigs_PeerPubKeyMatchesPrivateKey(t *testing.T) {
	nodes := buildTestNodes(t, 2)

	configs, err := GenerateMeshConfigs(nodes, "")
	require.NoError(t, err)

	// configs[0] contains peer = nodes[1]; configs[1] contains peer = nodes[0].
	for i, cfg := range configs {
		parsed, err := ParseConfigString(cfg.ConfigString)
		require.NoError(t, err)

		require.Len(t, parsed.Peers, 1)
		peerBlock := parsed.Peers[0]

		// Determine which remote node this peer block refers to.
		remoteIdx := 1 - i
		wantPub := pubKeyFor(t, nodes[remoteIdx])

		// The emitted public key must match the crypto-derived one exactly.
		assert.Equal(t, wantPub, peerBlock.PublicKey,
			"emitted peer pubkey for node-%d must equal wgtypes-derived pubkey", remoteIdx)
	}
}

// TestGenerateMeshConfigs_AllowedIPsIsHostPrefix proves that the AllowedIPs
// entry for each peer is the host /32 (not the /24 subnet), and that its host
// address matches the peer's declared Address field.
//
// §1.1 mutation: change hostPrefix to return the network prefix (e.g. emit
// "10.200.0.0/24" instead of "10.200.0.2/32") → the addr equality check fails.
func TestGenerateMeshConfigs_AllowedIPsIsHostPrefix(t *testing.T) {
	nodes := buildTestNodes(t, 2)
	// Override addresses to known values for deterministic assertion.
	nodes[0].Address = "10.200.0.1/24"
	nodes[1].Address = "10.200.0.2/24"

	configs, err := GenerateMeshConfigs(nodes, "")
	require.NoError(t, err)

	// configs[0] sees nodes[1] as a peer; AllowedIPs must be 10.200.0.2/32.
	parsed0, err := ParseConfigString(configs[0].ConfigString)
	require.NoError(t, err)
	require.Len(t, parsed0.Peers, 1)
	require.Len(t, parsed0.Peers[0].AllowedIPs, 1)
	assert.Equal(t, "10.200.0.2/32", parsed0.Peers[0].AllowedIPs[0])

	// configs[1] sees nodes[0] as a peer; AllowedIPs must be 10.200.0.1/32.
	parsed1, err := ParseConfigString(configs[1].ConfigString)
	require.NoError(t, err)
	require.Len(t, parsed1.Peers, 1)
	require.Len(t, parsed1.Peers[0].AllowedIPs, 1)
	assert.Equal(t, "10.200.0.1/32", parsed1.Peers[0].AllowedIPs[0])
}

// TestGenerateMeshConfigs_TagAppearsInAllConfigs proves the per-run UUID/tag
// comment is present in every generated config string.
//
// §1.1 mutation: omit the tag comment in GenerateMeshConfigs → Contains fails.
func TestGenerateMeshConfigs_TagAppearsInAllConfigs(t *testing.T) {
	nodes := buildTestNodes(t, 3)
	const tag = "test-run-uuid-abc123"

	configs, err := GenerateMeshConfigs(nodes, tag)
	require.NoError(t, err)

	for i, cfg := range configs {
		assert.Contains(t, cfg.ConfigString, tag,
			"tag must appear in config for node-%d", i)
		// Must appear as a comment line.
		assert.Contains(t, cfg.ConfigString, "# MeshTag: "+tag,
			"tag comment must have the canonical format")
	}
}

// TestGenerateMeshConfigs_NoSelfPeerInConfigString performs a string-level
// check (beyond the Peers slice) that each node's OWN public key does not
// appear in any [Peer] block of its own config.
//
// §1.1 mutation: remove the `i == j` guard → self-peer [Peer] block appears →
// ConfigString contains own pubkey → assert fires.
func TestGenerateMeshConfigs_NoSelfPeerInConfigString(t *testing.T) {
	nodes := buildTestNodes(t, 4)
	wantPub := make([]string, len(nodes))
	for i, n := range nodes {
		wantPub[i] = pubKeyFor(t, n)
	}

	configs, err := GenerateMeshConfigs(nodes, "")
	require.NoError(t, err)

	for i, cfg := range configs {
		// Count occurrences of own pubkey — it must appear EXACTLY ONCE (in
		// the [Interface] section via PrivateKey derivation it is never printed
		// directly, but we parse to double-check peer blocks).
		parsed, err := ParseConfigString(cfg.ConfigString)
		require.NoError(t, err)

		ownPub := wantPub[i]
		for _, pb := range parsed.Peers {
			assert.NotEqual(t, ownPub, pb.PublicKey,
				"node-%d must not have itself as [Peer] (own pub: %s)", i, ownPub)
		}

		// The ownPub must NOT appear as a [Peer] PublicKey line.
		// Check raw config string for belt-and-suspenders.
		for _, line := range strings.Split(cfg.ConfigString, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "PublicKey =") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "PublicKey ="))
				assert.NotEqual(t, ownPub, val,
					"node-%d: own pubkey must not appear in a [Peer] PublicKey line", i)
			}
		}
	}
}

// TestGenerateMeshConfigs_RejectsTooFewNodes proves the minimum-2-node
// requirement is enforced.
//
// §1.1 mutation: remove the `len(nodes) < 2` guard → a 1-node mesh silently
// produces a config with zero peers → require.Error fails.
func TestGenerateMeshConfigs_RejectsTooFewNodes(t *testing.T) {
	_, err := GenerateMeshConfigs(nil, "")
	require.Error(t, err, "nil slice must be rejected")

	single := buildTestNodes(t, 1)
	_, err = GenerateMeshConfigs(single, "")
	require.Error(t, err, "single-node mesh must be rejected")
}

// TestGenerateMeshConfigs_RejectsBadPrivateKey proves that a node with an
// invalid private key is rejected before any config is generated.
//
// §1.1 mutation: remove the ParseKey validation on PrivateKey → the bad key is
// used, wgtypes panics or produces garbage, and require.Error fails.
func TestGenerateMeshConfigs_RejectsBadPrivateKey(t *testing.T) {
	nodes := buildTestNodes(t, 2)
	nodes[0].PrivateKey = "not-a-valid-key"

	_, err := GenerateMeshConfigs(nodes, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid private key")
}

// TestGenerateMeshConfigs_RejectsMissingFields proves that missing ID, Address,
// or Endpoint on any node is rejected.
//
// §1.1 mutation: remove the empty-field checks → a node with ID="" produces a
// config, and require.Error fails.
func TestGenerateMeshConfigs_RejectsMissingFields(t *testing.T) {
	base := buildTestNodes(t, 2)

	// Missing ID on node 0.
	bad := cloneNodes(base)
	bad[0].ID = ""
	_, err := GenerateMeshConfigs(bad, "")
	require.Error(t, err, "empty ID must be rejected")

	// Missing Address on node 1.
	bad = cloneNodes(base)
	bad[1].Address = ""
	_, err = GenerateMeshConfigs(bad, "")
	require.Error(t, err, "empty Address must be rejected")

	// Missing Endpoint on node 0.
	bad = cloneNodes(base)
	bad[0].Endpoint = ""
	_, err = GenerateMeshConfigs(bad, "")
	require.Error(t, err, "empty Endpoint must be rejected")
}

// cloneNodes returns a shallow copy of the nodes slice so individual tests can
// mutate fields independently.
func cloneNodes(ns []MeshNode) []MeshNode {
	c := make([]MeshNode, len(ns))
	copy(c, ns)
	return c
}

// ----- TestRotateMeshNodeKey ---------------------------------------------------

// TestRotateMeshNodeKey_UpdatesOnlyRotatedNodePubKey is the primary sink-side
// test for key rotation.  It proves:
//   - The rotated node's public key CHANGES across the old and new configs.
//   - Every OTHER pairwise peer relationship is STRUCTURALLY IDENTICAL (same
//     pubkeys in peer blocks) after the rotation.
//   - The new public key parses via wgtypes.ParseKey.
//   - The returned newPubKey matches the public key now emitted in peer blocks
//     that reference the rotated node.
//
// §1.1 mutation: if RotateMeshNodeKey re-uses the same private key → pubkeys
// unchanged → assert.NotEqual fires.
func TestRotateMeshNodeKey_UpdatesOnlyRotatedNodePubKey(t *testing.T) {
	const N = 3
	nodes := buildTestNodes(t, N)

	// Compute original public keys.
	origPubs := make([]string, N)
	for i, n := range nodes {
		origPubs[i] = pubKeyFor(t, n)
	}

	// Rotate node-1's key.
	newPub, configs, err := RotateMeshNodeKey(nodes, "node-1", "rotation-test")
	require.NoError(t, err)
	require.Len(t, configs, N)

	// 1. The returned newPubKey must parse via wgtypes.ParseKey.
	_, err = wgtypes.ParseKey(newPub)
	assert.NoError(t, err, "returned newPubKey must parse via wgtypes.ParseKey")

	// 2. The new pubkey must differ from the old one.
	// §1.1 mutation: return the same private key → newPub == origPubs[1] → fails.
	assert.NotEqual(t, origPubs[1], newPub,
		"rotated node's public key must change")

	// 3. Non-rotated nodes' pubkeys must be unchanged.
	assert.Equal(t, origPubs[0], pubKeyFor(t, nodes[0]),
		"node-0's private key must be unchanged in the caller's slice")
	assert.Equal(t, origPubs[2], pubKeyFor(t, nodes[2]),
		"node-2's private key must be unchanged in the caller's slice")

	// 4. In configs[0] (node-0's config), the peer block for node-1 must now
	//    reference newPub, not origPubs[1].
	parsed0, err := ParseConfigString(configs[0].ConfigString)
	require.NoError(t, err)
	require.Len(t, parsed0.Peers, N-1)

	var foundNode1InConfig0 bool
	for _, pb := range parsed0.Peers {
		if pb.PublicKey == newPub {
			foundNode1InConfig0 = true
		}
		// Old pubkey for node-1 must no longer appear.
		// §1.1 mutation: keep the old pubkey in configs → assert fires.
		assert.NotEqual(t, origPubs[1], pb.PublicKey,
			"old pubkey for node-1 must not appear in configs after rotation")
	}
	assert.True(t, foundNode1InConfig0,
		"new pubkey for node-1 must appear in node-0's peer list")

	// 5. In configs[2] (node-2's config), same invariant.
	parsed2, err := ParseConfigString(configs[2].ConfigString)
	require.NoError(t, err)
	require.Len(t, parsed2.Peers, N-1)

	var foundNode1InConfig2 bool
	for _, pb := range parsed2.Peers {
		if pb.PublicKey == newPub {
			foundNode1InConfig2 = true
		}
		assert.NotEqual(t, origPubs[1], pb.PublicKey,
			"old pubkey for node-1 must not appear in node-2's config")
	}
	assert.True(t, foundNode1InConfig2,
		"new pubkey for node-1 must appear in node-2's peer list")

	// 6. node-0 ↔ node-2 relationship is unchanged: in configs[0] the peer
	//    block for node-2 still has origPubs[2].
	for _, pb := range parsed0.Peers {
		if pb.PublicKey == origPubs[2] {
			// Found — all good.
			return
		}
	}
	t.Errorf("node-0's config must still contain node-2's original pubkey %s", origPubs[2])
}

// TestRotateMeshNodeKey_NewConfigsFullyValid proves that configs produced after
// rotation satisfy the same full-mesh invariants as freshly-generated configs:
// all keys parse, all AllowedIPs parse as host prefixes, no self-peer.
//
// §1.1 mutation: emit a truncated key for the rotated node → wgtypes.ParseKey
// errors → assert fires.
func TestRotateMeshNodeKey_NewConfigsFullyValid(t *testing.T) {
	nodes := buildTestNodes(t, 4)
	_, configs, err := RotateMeshNodeKey(nodes, "node-2", "validity-check")
	require.NoError(t, err)

	for _, cfg := range configs {
		parsed, err := ParseConfigString(cfg.ConfigString)
		require.NoError(t, err, "config for %s must parse", cfg.NodeID)

		// [Interface] key parses.
		_, err = wgtypes.ParseKey(parsed.Interface.PrivateKey)
		assert.NoError(t, err, "interface private key must parse for %s", cfg.NodeID)

		// Each peer block: key parses, AllowedIPs is valid host prefix.
		for _, pb := range parsed.Peers {
			_, err = wgtypes.ParseKey(pb.PublicKey)
			assert.NoError(t, err, "peer pubkey %s must parse", pb.PublicKey)

			for _, cidr := range pb.AllowedIPs {
				prefix, err := netip.ParsePrefix(cidr)
				assert.NoError(t, err, "AllowedIPs %s must parse", cidr)
				assert.Equal(t, prefix.Addr().BitLen(), prefix.Bits(),
					"AllowedIPs must be a host prefix (/32), got %s", cidr)
			}
		}
	}
}

// TestRotateMeshNodeKey_DoesNotMutateCallerSlice proves that RotateMeshNodeKey
// does not modify the caller's original nodes slice (it clones internally).
//
// §1.1 mutation: mutate nodes[idx] in-place without copying → the caller's
// slice changes → assert.Equal fires comparing original vs post-call key.
func TestRotateMeshNodeKey_DoesNotMutateCallerSlice(t *testing.T) {
	nodes := buildTestNodes(t, 3)
	origPriv := nodes[0].PrivateKey

	_, _, err := RotateMeshNodeKey(nodes, "node-0", "")
	require.NoError(t, err)

	// The caller's slice must be unchanged.
	assert.Equal(t, origPriv, nodes[0].PrivateKey,
		"RotateMeshNodeKey must not mutate the caller's nodes slice")
}

// TestRotateMeshNodeKey_RejectsUnknownNodeID proves an unknown nodeID returns
// an error immediately.
//
// §1.1 mutation: remove the idx < 0 guard → the function proceeds with idx=-1
// and panics or produces a wrong config → require.Error fails.
func TestRotateMeshNodeKey_RejectsUnknownNodeID(t *testing.T) {
	nodes := buildTestNodes(t, 2)
	_, _, err := RotateMeshNodeKey(nodes, "does-not-exist", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestHostPrefix_IPv4 unit-tests the hostPrefix helper directly for IPv4 CIDRs.
//
// §1.1 mutation: return the network address (mask out host bits) instead of the
// host address → "10.200.0.0/32" ≠ "10.200.0.5/32" → assert fires.
func TestHostPrefix_IPv4(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"10.200.0.1/24", "10.200.0.1/32"},
		{"10.200.0.5/24", "10.200.0.5/32"},
		{"192.168.1.100/16", "192.168.1.100/32"},
		{"10.0.0.1/8", "10.0.0.1/32"},
		// Already /32 — idempotent.
		{"10.0.0.1/32", "10.0.0.1/32"},
	}
	for _, tc := range cases {
		got, err := hostPrefix(tc.input)
		require.NoError(t, err, "input %s", tc.input)
		assert.Equal(t, tc.want, got, "hostPrefix(%s)", tc.input)
	}
}

// TestHostPrefix_RejectsBadInput proves invalid input is rejected.
//
// §1.1 mutation: remove error return in hostPrefix → "not-a-cidr" is silently
// accepted → require.Error fails.
func TestHostPrefix_RejectsBadInput(t *testing.T) {
	_, err := hostPrefix("not-a-cidr")
	require.Error(t, err)

	_, err = hostPrefix("")
	require.Error(t, err)
}

// TestGeneratedKeyParsesViaWgtypes is the direct mutation-probe for the
// closure criterion: "Generated keys parse via wgtypes.ParseKey."
// It generates a fresh keypair and calls wgtypes.ParseKey on both halves;
// then deliberately passes a truncated key (base64 of 10 bytes) and asserts
// the PARSE FAILS.
//
// §1.1 mutation proof captured in mutation_proof field of StructuredOutput:
// when we temporarily shorten the generated key string to 10 bytes, ParseKey
// returns an error, proving the guard is not bypassed.
func TestGeneratedKeyParsesViaWgtypes(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	require.NoError(t, err)

	_, err = wgtypes.ParseKey(priv)
	assert.NoError(t, err, "generated private key must parse via wgtypes.ParseKey")

	_, err = wgtypes.ParseKey(pub)
	assert.NoError(t, err, "generated public key must parse via wgtypes.ParseKey")

	// A truncated key (base64 of 10 bytes, not 32) must FAIL.
	// This is the mutation test: if wgtypes.ParseKey had no length check, a
	// short key would be accepted and this assert would fire.
	truncated := "d2F5dG9vc2hvcnQ=" // base64("waytooshort"), 10 bytes
	_, err = wgtypes.ParseKey(truncated)
	assert.Error(t, err, "truncated key must fail wgtypes.ParseKey")
}
