package scheduler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// AttestationGate is an attestation-gated admission Plugin (Phase 8C
// "trusted-execution" concept). For jobs that REQUIRE a trusted / attested
// node, its Filter ADMITS only nodes that present a currently-valid,
// cryptographically-verifiable attestation token bound to that node, and
// REJECTS any node presenting no token, an expired token, a token for a
// different node, or a forged/tampered token. Jobs that do not require
// attestation are never filtered out by this plugin.
//
// It is additive and backward-compatible: it implements the existing Plugin
// interface (Name/Filter/Score/Bind) and stores all of its inputs in the
// existing Job.Labels / Node.Labels maps under reserved, namespaced keys
// (mirroring ClassAdMatch, CostAwareGPUPlacement and TierPowerAware). No field
// on Job or Node changes shape. Like the other matching plugins it does not
// consume resources in Bind (NodeResourcesFit owns resource accounting).
//
// HONEST SECURITY SEAM: this plugin does NOT import the digital.vasic.security
// submodule and does NOT talk to a real TPM / SGX / SEV quote service. Token
// MINTING and VERIFICATION go through the injectable Attestor interface. The
// in-package default, HMACAttestor, is a WORKING stdlib reference built on
// crypto/hmac + crypto/sha256: it actually signs and verifies node-bound,
// time-bounded tokens, and a tampered token genuinely fails verification. A
// real deployment would replace it with a verifier backed by hardware-rooted
// quotes; the seam is the Attestor interface, not a no-op stub.
type AttestationGate struct {
	// Attestor verifies node attestation tokens. If nil, Filter treats every
	// attestation-required node as UNVERIFIED and rejects it (fail-closed).
	Attestor Attestor
	// Now returns the current time; injectable for deterministic expiry tests.
	// If nil, time.Now is used.
	Now func() time.Time
}

// Reserved label keys. They are namespaced under "helix.attestation/" so they
// never collide with the require_/prefer_ convention used by CapabilityMatch or
// the bare resource labels used by the other plugins.
const (
	// LabelJobRequireAttestation, when set truthy on a Job, demands that the
	// placement node present a currently-valid attestation token. Absent/false
	// => the job has no attestation constraint and any node admits it.
	LabelJobRequireAttestation = "helix.attestation/required"

	// LabelNodeAttestationToken carries the node's attestation token (the
	// opaque string produced by Attestor.Issue). Absent => the node is
	// unattested and is rejected for attestation-required jobs.
	LabelNodeAttestationToken = "helix.attestation/token"
)

// Attestor mints and verifies node attestation tokens. It is the injectable
// seam that keeps real attestation backends (TPM/SGX/SEV quote services) out of
// this package while still allowing real cryptographic verification in tests
// and the default deployment path.
type Attestor interface {
	// Issue mints a token asserting that nodeID is attested until notAfter.
	Issue(nodeID string, notAfter time.Time) (string, error)
	// Verify returns true iff token is a well-formed, untampered token that was
	// issued for nodeID and has not expired as of now.
	Verify(nodeID, token string, now time.Time) bool
}

// SetJobRequireAttestation marks a job as requiring an attested node (reserved
// Job.Labels key), allocating the map if needed. Backward-compatible: jobs that
// never call this carry no attestation constraint.
func SetJobRequireAttestation(job *Job, required bool) {
	ensureJobLabels(job)
	job.Labels[LabelJobRequireAttestation] = strconv.FormatBool(required)
}

// SetNodeAttestationToken attaches an attestation token to a node (reserved
// Node.Labels key), allocating the map if needed.
func SetNodeAttestationToken(node *Node, token string) {
	ensureNodeLabels(node)
	node.Labels[LabelNodeAttestationToken] = token
}

func (a *AttestationGate) Name() string { return "AttestationGate" }

// jobRequiresAttestation reports whether the job demands an attested node.
func jobRequiresAttestation(job *Job) bool {
	v := labelValue(job.Labels, LabelJobRequireAttestation)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(strings.ToLower(v))
	if err != nil {
		// A non-empty, non-false-parseable value is treated as truthy so a
		// stray "yes"/"1"-style label still fails closed (demands attestation),
		// never silently admitting an unattested node.
		return !strings.EqualFold(v, "false")
	}
	return b
}

// now returns the gate's clock, defaulting to time.Now.
func (a *AttestationGate) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Filter admits a node for an attestation-required job ONLY when the node
// presents a token that the Attestor verifies as currently valid and bound to
// that node. A missing token, an expired token, a token issued for a different
// node, or a forged/tampered token is rejected. When no Attestor is configured
// the gate fails CLOSED for attestation-required jobs (rejects every node),
// never silently admitting an unverified node. Jobs that do not require
// attestation are admitted on every (non-nil) node by this plugin.
func (a *AttestationGate) Filter(job *Job, node *Node) bool {
	if job == nil || node == nil {
		return false
	}
	if !jobRequiresAttestation(job) {
		return true
	}
	// Attestation is required from here on: fail closed without a verifier.
	if a.Attestor == nil {
		return false
	}
	token := labelValue(node.Labels, LabelNodeAttestationToken)
	if token == "" {
		return false
	}
	return a.Attestor.Verify(node.ID, token, a.now())
}

// Score is a no-op (0) for this admission plugin: it expresses a hard
// constraint via Filter and does not bias ranking among admitted nodes.
func (a *AttestationGate) Score(job *Job, node *Node) int { return 0 }

// Bind is a no-op success for this admission plugin; it does not consume
// resources (NodeResourcesFit owns resource accounting) and must not break the
// bind pipeline.
func (a *AttestationGate) Bind(job *Job, node *Node) bool { return true }

// ---------------------------------------------------------------------------
// HMACAttestor: a WORKING stdlib reference Attestor.
// ---------------------------------------------------------------------------

// HMACAttestor is the default, in-package, WORKING reference implementation of
// Attestor. A token has the form
//
//	base64url(nodeID) "." base64url(unixExpiry) "." base64url(HMAC-SHA256(key, payload))
//
// where payload is the first two fields joined by ".". Verification recomputes
// the MAC over the presented node id + expiry using crypto/hmac and compares it
// in constant time (hmac.Equal), then checks the bound node id and expiry. Any
// tampering with the node id, expiry, or MAC changes the recomputed MAC (or the
// bound fields) and verification fails — this is genuine cryptographic
// verification, not a stub.
type HMACAttestor struct {
	// Key is the shared secret used to sign and verify tokens. Different keys
	// produce mutually-unverifiable tokens, so a token minted by an attacker
	// without Key cannot pass Verify.
	Key []byte
}

// NewHMACAttestor constructs an HMACAttestor with the given secret key.
func NewHMACAttestor(key []byte) *HMACAttestor {
	return &HMACAttestor{Key: key}
}

const attestTokenSep = "."

// sign computes the base64url HMAC-SHA256 over payload using the attestor key.
func (h *HMACAttestor) sign(payload string) string {
	mac := hmac.New(sha256.New, h.Key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue mints a node-bound token that expires at notAfter.
func (h *HMACAttestor) Issue(nodeID string, notAfter time.Time) (string, error) {
	idPart := base64.RawURLEncoding.EncodeToString([]byte(nodeID))
	expPart := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(notAfter.Unix(), 10)))
	payload := idPart + attestTokenSep + expPart
	return payload + attestTokenSep + h.sign(payload), nil
}

// Verify returns true iff token is an untampered HMAC token issued for nodeID
// and not expired as of now. It checks, in order: structural shape, MAC
// authenticity (constant-time), node-id binding, and expiry.
func (h *HMACAttestor) Verify(nodeID, token string, now time.Time) bool {
	parts := strings.Split(token, attestTokenSep)
	if len(parts) != 3 {
		return false
	}
	idPart, expPart, macPart := parts[0], parts[1], parts[2]
	payload := idPart + attestTokenSep + expPart

	// 1. Authenticity: recompute the MAC and compare in constant time. A forged
	// or tampered token (wrong key, altered id/expiry/mac) fails here.
	expectedMAC := h.sign(payload)
	if !hmac.Equal([]byte(macPart), []byte(expectedMAC)) {
		return false
	}

	// 2. Node binding: the token must have been issued for THIS node.
	idRaw, err := base64.RawURLEncoding.DecodeString(idPart)
	if err != nil || string(idRaw) != nodeID {
		return false
	}

	// 3. Expiry: the token must not have expired as of now.
	expRaw, err := base64.RawURLEncoding.DecodeString(expPart)
	if err != nil {
		return false
	}
	expUnix, err := strconv.ParseInt(string(expRaw), 10, 64)
	if err != nil {
		return false
	}
	if !now.Before(time.Unix(expUnix, 0)) {
		return false
	}
	return true
}
