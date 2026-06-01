//go:build integration

package wireguard

// Integration tests for GenerateMeshConfigs + RotateMeshNodeKey.
//
// These tests are guarded by //go:build integration and are NOT run by the
// hermetic unit-test command.  The orchestrator runs them against the real
// host OS.  They hard-FAIL (t.Fatalf) on any unexpected error rather than
// t.Skip, because the dependency (wgtypes crypto) is always available.
//
// Per-run UUID tag is embedded in every generated config and asserted present
// on the sink side (the parsed config string), proving that THIS run's output
// is observed — not a cached or stale artifact.
//
// Cross-platform note: all operations are pure Go crypto + text rendering;
// there are no kernel WireGuard interface calls, so these tests run identically
// on Linux and macOS.

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// buildIntegrationNodes creates n mesh nodes with real keypairs and
// deterministic addresses for use in integration tests.
func buildIntegrationNodes(t *testing.T, n int) []MeshNode {
	t.Helper()
	nodes := make([]MeshNode, n)
	for i := range nodes {
		priv, _, err := GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair for node-%d failed: %v", i, err)
		}
		nodes[i] = MeshNode{
			ID:         fmt.Sprintf("node-%d", i),
			PrivateKey: priv,
			Address:    fmt.Sprintf("10.201.%d.%d/24", i/256, i%256+1),
			Endpoint:   fmt.Sprintf("198.51.100.%d:51820", i+1),
		}
	}
	return nodes
}

// TestIntegration_MeshConfigFullPairwiseRoundTrip generates an N-node mesh,
// asserts every pairwise peer relationship is present and parseable via a real
// wgtypes round-trip, and verifies the per-run UUID tag is embedded and
// asserted on the sink side.
//
// Hard-fail policy: t.Fatalf on any error because wgtypes is always available.
func TestIntegration_MeshConfigFullPairwiseRoundTrip(t *testing.T) {
	const N = 5
	runID := uuid.New().String()
	nodes := buildIntegrationNodes(t, N)

	// Compute expected public keys (real wgtypes derivation).
	wantPub := make([]string, N)
	for i, n := range nodes {
		priv, err := ParseKey(n.PrivateKey)
		if err != nil {
			t.Fatalf("ParseKey for node-%d: %v", i, err)
		}
		pub := priv.PublicKey()
		wantPub[i] = pub.String()

		// Verify the public key parses back via wgtypes.ParseKey — real round-trip.
		_, err = wgtypes.ParseKey(wantPub[i])
		if err != nil {
			t.Fatalf("wgtypes.ParseKey for node-%d pubkey: %v", i, err)
		}
	}

	configs, err := GenerateMeshConfigs(nodes, runID)
	if err != nil {
		t.Fatalf("GenerateMeshConfigs: %v", err)
	}
	if len(configs) != N {
		t.Fatalf("expected %d configs, got %d", N, len(configs))
	}

	for i, cfg := range configs {
		// Per-run UUID must appear in the config string (sink-side evidence).
		if !strings.Contains(cfg.ConfigString, runID) {
			t.Fatalf("node-%d config missing run UUID %s", i, runID)
		}

		parsed, err := ParseConfigString(cfg.ConfigString)
		if err != nil {
			t.Fatalf("ParseConfigString for node-%d: %v", i, err)
		}

		// ListenPort must be 51820.
		assert.Equal(t, 51820, parsed.Interface.ListenPort,
			"node-%d ListenPort must be 51820", i)

		// [Interface] PrivateKey must parse via wgtypes.
		_, err = wgtypes.ParseKey(parsed.Interface.PrivateKey)
		require.NoError(t, err, "node-%d interface private key must parse", i)

		// Exactly N-1 peer blocks.
		if len(parsed.Peers) != N-1 {
			t.Fatalf("node-%d: expected %d peers, got %d", i, N-1, len(parsed.Peers))
		}

		// Build a set of peer pubkeys in this config.
		peerPubSet := make(map[string]bool, N-1)
		for _, pb := range parsed.Peers {
			// Each peer's PublicKey must parse via wgtypes — real round-trip.
			_, err := wgtypes.ParseKey(pb.PublicKey)
			if err != nil {
				t.Fatalf("node-%d: peer pubkey %q wgtypes.ParseKey: %v", i, pb.PublicKey, err)
			}
			peerPubSet[pb.PublicKey] = true

			// AllowedIPs must parse as valid host prefix.
			for _, cidr := range pb.AllowedIPs {
				prefix, err := netip.ParsePrefix(cidr)
				if err != nil {
					t.Fatalf("node-%d: AllowedIPs %q netip.ParsePrefix: %v", i, cidr, err)
				}
				if prefix.Bits() != prefix.Addr().BitLen() {
					t.Fatalf("node-%d: AllowedIPs %s is not a host prefix", i, cidr)
				}
			}

			// Endpoint must be non-empty.
			if pb.Endpoint == "" {
				t.Fatalf("node-%d: peer %s has empty Endpoint", i, pb.PublicKey)
			}
		}

		// Self-pubkey must NOT be in the peer set.
		if peerPubSet[wantPub[i]] {
			t.Fatalf("node-%d: own pubkey appears as a peer (self-peer)", i)
		}

		// Every other node's pubkey must appear exactly once.
		for j, want := range wantPub {
			if j == i {
				continue
			}
			if !peerPubSet[want] {
				t.Fatalf("node-%d: missing peer pubkey for node-%d (%s)", i, j, want)
			}
		}
	}
}

// TestIntegration_KeyRotationUpdatesPubKeyAcrossAllConfigs rotates one node's
// key and verifies that exactly that node's pubkey is updated in every other
// node's peer block, while all other pairwise relationships remain unchanged.
// The per-run UUID is embedded and asserted present in all regenerated configs.
func TestIntegration_KeyRotationUpdatesPubKeyAcrossAllConfigs(t *testing.T) {
	const N = 4
	runID := uuid.New().String()
	nodes := buildIntegrationNodes(t, N)

	// Capture original public keys.
	origPubs := make([]string, N)
	for i, n := range nodes {
		priv, err := ParseKey(n.PrivateKey)
		if err != nil {
			t.Fatalf("ParseKey for node-%d: %v", i, err)
		}
		origPubs[i] = priv.PublicKey().String()
	}

	// Rotate node-2's key.
	const rotateIdx = 2
	newPub, configs, err := RotateMeshNodeKey(nodes, nodes[rotateIdx].ID, runID)
	if err != nil {
		t.Fatalf("RotateMeshNodeKey: %v", err)
	}

	// The returned newPub must parse via wgtypes — real round-trip.
	_, err = wgtypes.ParseKey(newPub)
	if err != nil {
		t.Fatalf("wgtypes.ParseKey on returned newPub: %v", err)
	}

	// newPub must differ from the original.
	if newPub == origPubs[rotateIdx] {
		t.Fatalf("key rotation did not produce a new public key")
	}

	// Verify per-run UUID appears in every regenerated config (sink-side).
	for i, cfg := range configs {
		if !strings.Contains(cfg.ConfigString, runID) {
			t.Fatalf("config for %s missing run UUID %s", cfg.NodeID, runID)
		}

		parsed, err := ParseConfigString(cfg.ConfigString)
		if err != nil {
			t.Fatalf("ParseConfigString for node-%d: %v", i, err)
		}

		for _, pb := range parsed.Peers {
			// All peer pubkeys must parse.
			_, err := wgtypes.ParseKey(pb.PublicKey)
			if err != nil {
				t.Fatalf("node-%d: peer pubkey %q wgtypes.ParseKey: %v", i, pb.PublicKey, err)
			}

			if i == rotateIdx {
				// The rotated node's own config must not reference its old pubkey as a peer.
				if pb.PublicKey == origPubs[rotateIdx] {
					t.Fatalf("node-%d (rotated): old pubkey still appears in its own peer list", i)
				}
			} else {
				// Other nodes must reference newPub (not the old one) for node-rotateIdx.
				if pb.PublicKey == origPubs[rotateIdx] {
					t.Fatalf("node-%d: old pubkey for rotated node still appears after rotation", i)
				}
			}
		}

		// In nodes that are not node-rotateIdx, the peer block for node-rotateIdx
		// must use newPub.
		if i != rotateIdx {
			var foundNew bool
			for _, pb := range parsed.Peers {
				if pb.PublicKey == newPub {
					foundNew = true
				}
			}
			if !foundNew {
				t.Fatalf("node-%d: new pubkey %s not found in peer list after rotation", i, newPub)
			}
		}

		// Non-rotated nodes' pubkeys (j ≠ rotateIdx) must be unchanged across
		// all configs that reference them.
		for j, origPub := range origPubs {
			if j == rotateIdx {
				continue // rotated node's pub changed — skip
			}
			if j == i {
				continue // self — not in peer list
			}
			var found bool
			for _, pb := range parsed.Peers {
				if pb.PublicKey == origPub {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("node-%d: unchanged node-%d pubkey %s missing from peer list", i, j, origPub)
			}
		}
	}
}
