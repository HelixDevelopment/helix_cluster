// Package attestadmit implements an attestation-gated scheduler admission
// predicate for Helix Cluster OS GPU nodes (registry item HXC-1586).
//
// A scheduler holds a set of candidate Nodes, each presenting a GPU attestation
// triple (Challenge, Response, Descriptor) produced by the REAL pkg/gpuattest
// suite. Before any scoring/placement decision is made, the admission predicate
// filters the candidate set down to ONLY the nodes whose attestation is genuine:
// a node carrying a spoofed, forged, tampered, wrong-key or expired response is
// rejected and never reaches scoring.
//
// The package deliberately reuses pkg/gpuattest.Verifier.Verify as the single
// source of truth for "is this attestation genuine?". It does NOT reimplement
// any crypto, expiry or replay logic — Admit is a thin gate that calls Verify
// and turns its nil/sentinel verdict into an admit/reject decision.
//
// Determinism / CLAUDE-2: this is platform-agnostic pure Go. There is no clock,
// no /proc, no cgroup, no cgo, no network. The caller supplies an explicit
// nowTick, so admission outcomes are fully reproducible on every supported OS.
package attestadmit

import "github.com/HelixDevelopment/helix_cluster/pkg/gpuattest"

// Node is a scheduler placement candidate together with the GPU attestation it
// presents. The attestation triple is exactly what pkg/gpuattest.Verify needs:
// the Challenge the verifier issued, the device's signed Response, and the
// Descriptor the node claims. A genuine node supplies a Response its real device
// produced for Challenge against Descriptor; a spoofed node supplies a forged or
// mismatched triple.
type Node struct {
	// ID identifies the node for placement bookkeeping. It is opaque to the
	// admission predicate and is used only so callers can recognise which
	// candidates survived filtering.
	ID string

	// Challenge is the attestation challenge the verifier issued to this node.
	Challenge gpuattest.Challenge
	// Response is the node's signed answer to Challenge.
	Response gpuattest.Response
	// Descriptor is the device identity the node presents at admission time.
	Descriptor gpuattest.Descriptor
}

// Admit reports whether node may proceed to scoring at nowTick. It returns true
// ONLY when verifier.Verify accepts the node's attestation triple as genuine
// (Verify returns nil). Any sentinel error from Verify — forged descriptor,
// wrong key, tampered payload, replayed nonce or expired challenge — yields
// false, so the node is filtered out BEFORE scoring.
//
// Admit performs no crypto itself; the verdict is entirely delegated to the real
// gpuattest verifier. A nil verifier is treated as "cannot attest", so nothing
// is admitted (fail-closed) rather than silently accepting unverified nodes.
func Admit(verifier *gpuattest.Verifier, node Node, nowTick int64) bool {
	if verifier == nil {
		return false
	}
	return verifier.Verify(node.Challenge, node.Response, node.Descriptor, nowTick) == nil
}

// Placeable returns the subset of nodes whose attestation Admit accepts at
// nowTick, preserving input order. Rejected (spoofed/expired/...) nodes are
// absent from the result entirely — they are never handed to a scorer.
//
// Because Verify consumes a genuine challenge's nonce (single-use, replay
// protection), Placeable verifies each node exactly once against the shared
// verifier. The returned slice is freshly allocated and never aliases nodes.
func Placeable(verifier *gpuattest.Verifier, nodes []Node, nowTick int64) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if Admit(verifier, n, nowTick) {
			out = append(out, n)
		}
	}
	return out
}
