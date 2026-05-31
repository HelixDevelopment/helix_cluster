// Package inferenceproxy implements a guarded inference proxy for Helix Cluster
// OS. The proxy fronts an inference Backend and enforces an ADMISSION gate
// (an Authorizer) before any request is forwarded. The ordering guarantee is
// the security-critical contract: authorization runs FIRST, and a denied
// request never reaches the backend. Every request — allowed or denied — is
// recorded to an audit sink (who/model/decision/bytes/timestamp).
//
// SCOPE / HONESTY: this package is self-contained and stdlib-only. It does NOT
// import pkg/inference, the model registry, or the digital.vasic.security
// submodule. Real serving engines plug in behind the Backend interface; real
// identity/policy systems plug in behind the Authorizer interface. The bundled
// TokenAuthorizer is a WORKING reference that verifies request tokens with a
// real HMAC-SHA256 keyed MAC and constant-time comparison (crypto/hmac,
// crypto/sha256, crypto/subtle) — not a no-op marked "real".
package inferenceproxy

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors so callers (and tests) can branch on the failure class with
// errors.Is rather than string matching.
var (
	// ErrUnauthorized is returned (wrapped) when the admission gate denies a
	// request. When the proxy returns an error that Is ErrUnauthorized, the
	// backend was NEVER invoked.
	ErrUnauthorized = errors.New("inferenceproxy: request not authorized")
	// ErrNilBackend is returned by NewProxy when no backend is supplied.
	ErrNilBackend = errors.New("inferenceproxy: backend is required")
	// ErrNilAuthorizer is returned by NewProxy when no authorizer is supplied.
	ErrNilAuthorizer = errors.New("inferenceproxy: authorizer is required")
)

// Request is a single inference request flowing through the proxy. It is
// defined locally so the package does not depend on pkg/inference.
type Request struct {
	// Subject identifies the caller (the "who" in the audit record).
	Subject string
	// Model is the requested model name.
	Model string
	// Prompt is the input text; its byte length is recorded for audit.
	Prompt string
	// Token is the bearer credential the Authorizer validates.
	Token string
	// Scope is the capability the request needs (e.g. "infer").
	Scope string
}

// Response is the result returned by a Backend.
type Response struct {
	// Model echoes the served model name.
	Model string
	// Text is the generated output.
	Text string
	// Tokens is the number of tokens emitted.
	Tokens int
}

// Backend is the downstream inference provider fronted by the proxy. It is a
// minimal in-package seam; a real engine implements this interface.
type Backend interface {
	// Infer runs a single request and returns its response or an error.
	Infer(ctx context.Context, req Request) (Response, error)
}

// Authorizer is the admission gate. Authorize returns nil to ADMIT the request
// and a non-nil error to DENY it. It MUST NOT mutate or forward the request.
type Authorizer interface {
	// Authorize decides whether the request may proceed to the backend.
	Authorize(req Request) error
}

// Decision is the recorded outcome of an admission check.
type Decision string

const (
	// DecisionAllow marks a request that passed admission and reached the backend.
	DecisionAllow Decision = "allow"
	// DecisionDeny marks a request rejected by admission; backend never called.
	DecisionDeny Decision = "deny"
)

// AuditEntry is a single sink-side record of a request passing through the
// proxy. One entry is produced per Infer call regardless of the decision.
type AuditEntry struct {
	// Subject is the caller identity ("who").
	Subject string
	// Model is the requested model.
	Model string
	// Decision is allow or deny.
	Decision Decision
	// PromptBytes is the byte length of the request prompt.
	PromptBytes int
	// Time is when the decision was recorded.
	Time time.Time
	// Err is the reason for a deny (or a propagated backend error on an allow);
	// nil on a clean allow.
	Err error
}

// AuditSink receives one AuditEntry per request. Implementations must be safe
// to call from the goroutine that invokes Infer.
type AuditSink interface {
	// Record appends an audit entry.
	Record(entry AuditEntry)
}

// Clock supplies the current time; injectable for deterministic tests.
type Clock func() time.Time

// Proxy is a guarded inference proxy. It authorizes every request BEFORE
// forwarding, rejects denied requests without ever touching the backend, and
// records every request to the audit sink.
type Proxy struct {
	backend    Backend
	authorizer Authorizer
	audit      AuditSink
	now        Clock
}

// NewProxy constructs a Proxy. backend and authorizer are required. A nil audit
// sink is replaced with a no-op sink; a nil clock defaults to time.Now.
func NewProxy(backend Backend, authorizer Authorizer, audit AuditSink, clock Clock) (*Proxy, error) {
	if backend == nil {
		return nil, ErrNilBackend
	}
	if authorizer == nil {
		return nil, ErrNilAuthorizer
	}
	if audit == nil {
		audit = nopSink{}
	}
	if clock == nil {
		clock = time.Now
	}
	return &Proxy{
		backend:    backend,
		authorizer: authorizer,
		audit:      audit,
		now:        clock,
	}, nil
}

// Infer applies the admission gate and, only on success, forwards to the
// backend. The ordering is the contract: Authorize runs FIRST. On denial the
// backend is never invoked, an error wrapping ErrUnauthorized is returned, and
// a DecisionDeny audit entry is recorded. On admission the request is forwarded;
// whether the backend succeeds or errors, a DecisionAllow audit entry is
// recorded and the backend's exact response/error is returned.
func (p *Proxy) Infer(ctx context.Context, req Request) (Response, error) {
	// ADMISSION FIRST. If this moves below the backend call, an unauthorized
	// request reaches the backend — exactly the mutation the tests kill.
	if err := p.authorizer.Authorize(req); err != nil {
		denyErr := fmt.Errorf("%w: %w", ErrUnauthorized, err)
		p.audit.Record(AuditEntry{
			Subject:     req.Subject,
			Model:       req.Model,
			Decision:    DecisionDeny,
			PromptBytes: len(req.Prompt),
			Time:        p.now(),
			Err:         denyErr,
		})
		return Response{}, denyErr
	}

	resp, err := p.backend.Infer(ctx, req)
	p.audit.Record(AuditEntry{
		Subject:     req.Subject,
		Model:       req.Model,
		Decision:    DecisionAllow,
		PromptBytes: len(req.Prompt),
		Time:        p.now(),
		Err:         err,
	})
	return resp, err
}

// nopSink discards audit entries; used when the caller supplies no sink.
type nopSink struct{}

// Record implements AuditSink.
func (nopSink) Record(AuditEntry) {}

// compile-time assertion.
var _ AuditSink = nopSink{}
