package chutes

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// Attestation errors.
var (
	// ErrChallengeSize is returned for a malformed (empty) challenge nonce.
	ErrChallengeSize = errors.New("chutes: challenge nonce is empty")
	// ErrAttestFailed is returned by Verify when a response does not match the
	// expected proof for a challenge.
	ErrAttestFailed = errors.New("chutes: attestation verification failed")
)

// Challenge is a verifier-issued nonce a GPU node must respond to.
type Challenge struct {
	// Nonce is fresh random bytes that bind a response to this challenge.
	Nonce []byte
	// NodeID names the GPU node under attestation.
	NodeID string
}

// Response is a node's reply to a Challenge.
type Response struct {
	// NodeID echoes the challenged node.
	NodeID string
	// Proof is the node's computed proof over the nonce (HMAC tag for the
	// reference implementation).
	Proof []byte
}

// Attestor is the GPU-attestation seam. A verifier issues a Challenge, a node
// computes a Response, and the verifier checks it. digital.vasic.security/pkg/
// gpuattest (real proof-of-GPU / TEE quote attestation) implements this same
// interface and plugs in unchanged.
type Attestor interface {
	// Challenge mints a fresh challenge for nodeID.
	Challenge(nodeID string) (Challenge, error)
	// Respond computes a node's Response to a challenge.
	Respond(c Challenge) (Response, error)
	// Verify reports whether r is a valid response to c.
	Verify(c Challenge, r Response) error
}

// HMACAttestor is a REAL, working HMAC-SHA256 reference attestor. The node and
// verifier share a secret; a valid proof is HMAC(secret, nodeID||nonce). This
// actually verifies — a wrong secret, wrong node, or tampered proof fails Verify
// via constant-time comparison.
type HMACAttestor struct {
	secret []byte
}

// NewHMACAttestor builds an attestor from a shared secret (must be non-empty).
func NewHMACAttestor(secret []byte) (*HMACAttestor, error) {
	if len(secret) == 0 {
		return nil, errors.New("chutes: attestor secret is empty")
	}
	s := make([]byte, len(secret))
	copy(s, secret)
	return &HMACAttestor{secret: s}, nil
}

// proof computes the canonical HMAC tag binding nodeID and nonce.
func (a *HMACAttestor) proof(nodeID string, nonce []byte) []byte {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(nodeID))
	mac.Write(nonce)
	return mac.Sum(nil)
}

// Challenge implements Attestor with a 32-byte random nonce.
func (a *HMACAttestor) Challenge(nodeID string) (Challenge, error) {
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Challenge{}, fmt.Errorf("chutes: challenge nonce: %w", err)
	}
	return Challenge{Nonce: nonce, NodeID: nodeID}, nil
}

// Respond implements Attestor: an honest node computes the correct proof.
func (a *HMACAttestor) Respond(c Challenge) (Response, error) {
	if len(c.Nonce) == 0 {
		return Response{}, ErrChallengeSize
	}
	return Response{NodeID: c.NodeID, Proof: a.proof(c.NodeID, c.Nonce)}, nil
}

// Verify implements Attestor. It recomputes the expected proof and compares in
// constant time; the node IDs must also match so a proof for node A cannot
// satisfy a challenge for node B.
func (a *HMACAttestor) Verify(c Challenge, r Response) error {
	if len(c.Nonce) == 0 {
		return ErrChallengeSize
	}
	if c.NodeID != r.NodeID {
		return fmt.Errorf("%w: node mismatch %q != %q", ErrAttestFailed, c.NodeID, r.NodeID)
	}
	want := a.proof(c.NodeID, c.Nonce)
	if !hmac.Equal(want, r.Proof) {
		return fmt.Errorf("%w: proof mismatch", ErrAttestFailed)
	}
	return nil
}

// compile-time assertion.
var _ Attestor = (*HMACAttestor)(nil)

// AttestationResult pairs a challenge with its response for batch verification.
type AttestationResult struct {
	Challenge Challenge
	Response  Response
}

// BatchReport summarizes a batch verification run.
type BatchReport struct {
	// Total is the number of attestations checked.
	Total int
	// Passed is the number that verified successfully.
	Passed int
	// Failures maps the result index to its verification error, for diagnostics.
	Failures map[int]error
}

// PassRate returns the fraction of attestations that passed, in [0,1]. An empty
// batch has a pass-rate of 0 (nothing proved).
func (r BatchReport) PassRate() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.Passed) / float64(r.Total)
}

// BatchVerify verifies many attestations with the given Attestor and returns a
// BatchReport whose PassRate() is the exact fraction that passed. Each result is
// verified independently; one failure does not abort the batch.
func BatchVerify(a Attestor, results []AttestationResult) BatchReport {
	rep := BatchReport{Total: len(results), Failures: make(map[int]error)}
	for i, res := range results {
		if err := a.Verify(res.Challenge, res.Response); err != nil {
			rep.Failures[i] = err
			continue
		}
		rep.Passed++
	}
	return rep
}
