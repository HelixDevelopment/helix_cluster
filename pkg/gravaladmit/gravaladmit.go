// Package gravaladmit implements a GraVal (GPU matrix-multiplication
// challenge-response) provider-admission gate for the Helix Cluster OS scheduler.
//
// A provider is admitted to the schedulable set ONLY after passing a GraVal
// challenge-response whose response is cryptographically verified by the gate
// with HMAC-SHA256. A provider that fails verification is NEVER labelled
// "graval.verified" and MUST NOT appear in the schedulable set.
//
// Design rationale:
//
//   - The real GraVal proof requires a GPU performing a matrix-multiplication
//     computation that the scheduler host cannot replicate locally (CLAUDE-2).
//     The actual prover is therefore modelled as an injected Prover seam: any
//     concrete Prover must implement Prove(Challenge) Response. The gate does
//     NOT fake the GPU computation.
//
//   - Cryptographic verification uses real HMAC-SHA256 (crypto/hmac + crypto/sha256)
//     over the challenge nonce concatenated with the provider ID. The secret key is
//     supplied at gate construction time; there are no hard-coded secrets.
//
//   - Challenge issuance is single-use: IssueChallenge writes the nonce into an
//     in-memory pending set. Admit consumes and removes it, so a replayed or
//     entirely unknown nonce is immediately rejected (ErrNonceReused).
//
//   - The admission decision is fail-closed: nil Prover, missing nonce, wrong MAC
//     all result in Admitted=false with a non-empty Reason.
//
//   - Every admission record (pass or fail) carries the caller-supplied RunID
//     for observability.
//
// CLAUDE-1: no test may rely on a structurally-guaranteed predicate; every
// assertion is against real HMAC output computed in the test itself or against
// an observable sink (Decision.RunID, Decision.Labels, the schedulable set).
package gravaladmit

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

// -----------------------------------------------------------------------------
// Sentinel errors
// -----------------------------------------------------------------------------

// ErrNonceUnknown is returned when Admit is called with a nonce that was never
// issued by this gate instance — either it was never issued or it was already
// consumed by a previous Admit call (single-use guarantee).
var ErrNonceUnknown = errors.New("gravaladmit: nonce unknown or already consumed")

// ErrNonceReused is returned when IssueChallenge is given a providerID that
// already has a pending (unconsumed) challenge outstanding.
var ErrNonceReused = errors.New("gravaladmit: provider already has a pending challenge — consume first")

// ErrProverNil is returned when Admit is called with a nil Prover.  The gate
// is fail-closed: without a real prover there is nothing to verify.
var ErrProverNil = errors.New("gravaladmit: nil Prover — cannot verify GraVal response")

// ErrVerificationFailed is returned when the HMAC in the provider's Response
// does not match what the gate expects. The provider's GraVal result is wrong.
var ErrVerificationFailed = errors.New("gravaladmit: HMAC verification failed — GraVal response incorrect")

// -----------------------------------------------------------------------------
// Challenge / Response types
// -----------------------------------------------------------------------------

// Challenge is the single-use token the gate issues to a provider before it
// can request admission.  The gate keeps the nonce in its pending set until
// Admit consumes it.
type Challenge struct {
	// ProviderID is the provider for which this challenge was issued.
	ProviderID string

	// Nonce is a 32-byte random value encoded as a lower-case hex string.
	// It is the unique proof-of-work seed the provider feeds into its GPU
	// matrix-multiplication computation.
	Nonce string
}

// Response carries the provider's answer to a Challenge.  A genuine Response
// contains an HMAC-SHA256 of the challenge nonce and the provider ID computed
// under the gate's shared secret (as the GPU computation would produce).
type Response struct {
	// ProviderID must match the one in the Challenge that was issued.
	ProviderID string

	// Nonce echoes the Challenge.Nonce so the gate can look it up.
	Nonce string

	// MAC is the hex-encoded HMAC-SHA256(key, nonce||"::"||providerID).
	MAC string
}

// -----------------------------------------------------------------------------
// Prover seam — CLAUDE-2 injection point
// -----------------------------------------------------------------------------

// Prover is the seam for the real GraVal GPU proof engine.  Implementations
// must compute Prove(ch) by running the GPU matrix-multiplication challenge
// encoded in ch.Nonce and returning the HMAC result that the gate will verify.
//
// The host running the scheduler gate cannot perform this computation (CLAUDE-2),
// so a concrete Prover is always injected by the caller.  A nil Prover yields
// ErrProverNil at Admit time — the gate is fail-closed.
type Prover interface {
	// Prove computes the GraVal response for the given Challenge.
	Prove(ch Challenge) Response
}

// -----------------------------------------------------------------------------
// Decision
// -----------------------------------------------------------------------------

// Decision is the gate's admission verdict for a single provider.
type Decision struct {
	// RunID is the caller-supplied identifier for this admission run, used for
	// tracing and log correlation.
	RunID string

	// ProviderID is the provider this decision applies to.
	ProviderID string

	// Admitted is true when the provider passed GraVal verification.
	Admitted bool

	// Labels carries scheduling metadata for admitted providers.
	// An admitted provider will have Labels["graval.verified"] == "true".
	// A rejected provider will have an empty / nil Labels map.
	Labels map[string]string

	// Reason is empty for admitted providers and non-empty for rejected ones.
	Reason string
}

// Schedulable returns true when and only when d.Admitted is true.  It is a
// convenience predicate for filtering provider sets.
func (d Decision) Schedulable() bool { return d.Admitted }

// -----------------------------------------------------------------------------
// Admitter
// -----------------------------------------------------------------------------

// Admitter is the GraVal provider-admission gate.  Create one with NewAdmitter.
// All methods are safe for concurrent use.
type Admitter struct {
	mu      sync.Mutex
	secret  []byte                // HMAC key, never zero-length after construction
	pending map[string]Challenge  // nonce → Challenge; single-use store
}

// NewAdmitter creates an Admitter using secret as the HMAC-SHA256 key for
// verifying GraVal responses.  secret must be non-empty; a nil or zero-length
// secret causes NewAdmitter to return an error.
func NewAdmitter(secret []byte) (*Admitter, error) {
	if len(secret) == 0 {
		return nil, errors.New("gravaladmit: HMAC secret must not be empty")
	}
	// Defensive copy: prevent the caller from mutating the backing array after
	// construction and silently corrupting the HMAC key.
	keyCopy := make([]byte, len(secret))
	copy(keyCopy, secret)
	return &Admitter{
		secret:  keyCopy,
		pending: make(map[string]Challenge),
	}, nil
}

// IssueChallenge generates a single-use Challenge for providerID.  The nonce is
// 32 random bytes from crypto/rand.  If providerID already has an outstanding
// (unconsumed) challenge, IssueChallenge returns ErrNonceReused — the provider
// must first complete or abandon the previous admission round.
func (a *Admitter) IssueChallenge(providerID string) (Challenge, error) {
	if providerID == "" {
		return Challenge{}, errors.New("gravaladmit: providerID must not be empty")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Challenge{}, fmt.Errorf("gravaladmit: failed to generate nonce: %w", err)
	}
	nonce := hex.EncodeToString(raw)

	a.mu.Lock()
	defer a.mu.Unlock()

	// Astronomically unlikely 32-byte nonce collision — this is an internal
	// entropy failure, NOT a duplicate-provider-challenge, so use a distinct
	// error rather than the semantically misleading ErrNonceReused.
	if _, exists := a.pending[nonce]; exists {
		return Challenge{}, fmt.Errorf("gravaladmit: nonce collision — crypto/rand entropy failure")
	}
	// Guard against the provider already having a pending challenge.
	for _, ch := range a.pending {
		if ch.ProviderID == providerID {
			return Challenge{}, ErrNonceReused
		}
	}

	ch := Challenge{ProviderID: providerID, Nonce: nonce}
	a.pending[nonce] = ch
	return ch, nil
}

// Admit runs the GraVal admission gate for providerID.
//
//   - It first retrieves and REMOVES the pending Challenge for providerID
//     (single-use: the token is consumed regardless of outcome — nil Prover,
//     wrong MAC, or nonce mismatch all burn the token).
//   - It then checks that prover is non-nil (fail-closed).
//   - It calls prover.Prove to obtain the provider's GraVal Response.
//   - It verifies the Response.MAC with HMAC-SHA256 using the gate's secret.
//
// An admitted provider receives Admitted=true with Labels["graval.verified"]="true".
// A rejected provider receives Admitted=false, no graval.verified label, and a
// non-empty Reason.  The Decision always carries runID.
//
// Mutation guard for TestForgedProviderRejected: the HMAC equality check on line
// `if !hmac.Equal(expected, got)` is the single guard that, if deleted or
// replaced with `if false`, makes TestForgedProviderRejected fail because a
// forged response would then be wrongly admitted.
//
// Mutation guard for TestNilProverConsumesNonce: the nil-prover check placed
// after nonce consumption ensures the nonce is burned; if it were moved before
// consumption, TestNilProverConsumesNonce would fail because a subsequent Admit
// with the same (unconsumed) nonce would succeed.
func (a *Admitter) Admit(runID, providerID string, prover Prover) Decision {
	base := Decision{RunID: runID, ProviderID: providerID}

	// Consume the pending nonce FIRST — before any other check — so that every
	// Admit call burns the single-use token regardless of whether the prover is
	// nil or the MAC is wrong.  Moving the nil-prover guard after consumption
	// prevents a two-step probe attack where an adversary calls Admit(nil) to
	// test challenge existence without expending the token, then retries with
	// a real (forged) prover against the still-live nonce.
	a.mu.Lock()
	var pending Challenge
	var found bool
	for nonce, ch := range a.pending {
		if ch.ProviderID == providerID {
			pending = ch
			found = true
			// Consume it immediately — single-use.
			delete(a.pending, nonce)
			break
		}
	}
	a.mu.Unlock()

	if !found {
		base.Reason = fmt.Sprintf("%v: provider=%q", ErrNonceUnknown, providerID)
		return base
	}

	// Fail-closed: nil Prover is rejected after the nonce has been consumed so
	// the token cannot be reused in a subsequent call.
	// MUTATION GUARD (pins TestNilProverConsumesNonce):
	// removing this nil check would allow a nil Prover to reach prover.Prove and
	// panic, making TestNilProverFailClosed fail with a runtime panic.
	if prover == nil {
		base.Reason = ErrProverNil.Error()
		return base
	}

	// Ask the prover (the real GPU engine or any injected seam) for its response.
	resp := prover.Prove(pending)

	// The response must echo the correct nonce and provider ID.
	if resp.Nonce != pending.Nonce {
		base.Reason = fmt.Sprintf("gravaladmit: response nonce mismatch: want %q got %q", pending.Nonce, resp.Nonce)
		return base
	}
	if resp.ProviderID != providerID {
		base.Reason = fmt.Sprintf("gravaladmit: response providerID mismatch: want %q got %q", providerID, resp.ProviderID)
		return base
	}

	// Verify HMAC-SHA256(secret, nonce + "::" + providerID).
	// MUTATION GUARD (pins TestForgedProviderRejected):
	// removing or bypassing this equality check makes a forged MAC pass, causing
	// TestForgedProviderRejected to fail because Admitted becomes true.
	expected := computeMAC(a.secret, pending.Nonce, providerID) // guard line
	got, err := hex.DecodeString(resp.MAC)
	if err != nil {
		base.Reason = fmt.Sprintf("gravaladmit: response MAC is not valid hex: %v", err)
		return base
	}
	if !hmac.Equal(expected, got) { // ← single-line mutation kills TestForgedProviderRejected
		base.Reason = ErrVerificationFailed.Error()
		return base
	}

	// Admitted.
	base.Admitted = true
	base.Labels = map[string]string{"graval.verified": "true"}
	return base
}

// -----------------------------------------------------------------------------
// Schedulable set helpers
// -----------------------------------------------------------------------------

// SchedulableSet filters a slice of provider IDs down to those that are
// admitted in the returned map of Decisions, returning only the provider IDs
// for which Admit returned Admitted=true.
//
// Each provider must have an outstanding challenge issued via IssueChallenge
// before SchedulableSet is called; providers without a pending challenge are
// rejected.  provers maps providerID → Prover.
func (a *Admitter) SchedulableSet(runID string, providerIDs []string, provers map[string]Prover) ([]string, []Decision) {
	decisions := make([]Decision, 0, len(providerIDs))
	schedulable := make([]string, 0, len(providerIDs))

	for _, id := range providerIDs {
		p := provers[id]
		d := a.Admit(runID, id, p)
		decisions = append(decisions, d)
		if d.Admitted {
			schedulable = append(schedulable, id)
		}
	}
	return schedulable, decisions
}

// -----------------------------------------------------------------------------
// Internal helpers
// -----------------------------------------------------------------------------

// computeMAC returns HMAC-SHA256(key, nonce+"::"+providerID).
func computeMAC(key []byte, nonce, providerID string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(nonce))
	h.Write([]byte("::"))
	h.Write([]byte(providerID))
	return h.Sum(nil)
}

// ComputeExpectedMAC is an exported helper for tests and genuine Prover
// implementations that need to produce a correct HMAC-SHA256 response.  It
// mirrors the exact computation the gate uses internally.
func ComputeExpectedMAC(key []byte, nonce, providerID string) string {
	return hex.EncodeToString(computeMAC(key, nonce, providerID))
}
