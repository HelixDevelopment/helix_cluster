package inferenceproxy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingBackend is a test Backend that records how many times Infer was
// actually invoked. The hit counter is the SINK-SIDE proof of forwarding: a
// denied request must leave it at zero.
type countingBackend struct {
	hits atomic.Int64
	resp Response
	err  error
}

func (b *countingBackend) Infer(_ context.Context, _ Request) (Response, error) {
	b.hits.Add(1)
	return b.resp, b.err
}

// allowAll / denyAll are minimal Authorizers for ordering tests.
type allowAll struct{}

func (allowAll) Authorize(Request) error { return nil }

type denyAll struct{ err error }

func (d denyAll) Authorize(Request) error { return d.err }

func fixedClock(ts time.Time) Clock { return func() time.Time { return ts } }

// mustProxy builds a proxy or fails the test.
func mustProxy(t *testing.T, b Backend, a Authorizer, s AuditSink, c Clock) *Proxy {
	t.Helper()
	p, err := NewProxy(b, a, s, c)
	if err != nil {
		t.Fatalf("NewProxy: unexpected error: %v", err)
	}
	return p
}

// TestAuthorizedRequest_ReachesBackend_ExactResponse proves an admitted request
// is forwarded EXACTLY once and the proxy returns the backend's exact response.
//
// Mutation: making Proxy.Infer return Response{} instead of the backend's resp
// (or not calling p.backend.Infer at all) breaks the exact-field assertions and
// the hits==1 assertion.
func TestAuthorizedRequest_ReachesBackend_ExactResponse(t *testing.T) {
	backend := &countingBackend{resp: Response{Model: "qwen", Text: "[ref] hello", Tokens: 7}}
	audit := NewMemAudit()
	p := mustProxy(t, backend, allowAll{}, audit, fixedClock(time.Unix(1000, 0)))

	got, err := p.Infer(context.Background(), Request{Subject: "alice", Model: "qwen", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Infer: unexpected error: %v", err)
	}
	if got.Model != "qwen" || got.Text != "[ref] hello" || got.Tokens != 7 {
		t.Fatalf("response = %+v, want exact backend response {qwen,[ref] hello,7}", got)
	}
	if h := backend.hits.Load(); h != 1 {
		t.Fatalf("backend hits = %d, want exactly 1 (request must be forwarded once)", h)
	}
}

// TestUnauthorizedRequest_Rejected_BackendNeverHit is the security-critical
// test: a denied request must be rejected with ErrUnauthorized AND the backend
// hit counter must stay ZERO (sink-side proof it was never forwarded).
//
// Mutation: a proxy that forwards BEFORE authorizing (move p.backend.Infer above
// the Authorize check) lets this unauthorized request hit the backend, making
// hits == 1 and failing the "stays zero" assertion. Dropping the Authorize
// check entirely fails it the same way.
func TestUnauthorizedRequest_Rejected_BackendNeverHit(t *testing.T) {
	backend := &countingBackend{resp: Response{Model: "qwen", Text: "SHOULD NOT APPEAR"}}
	audit := NewMemAudit()
	denyReason := errors.New("no token")
	p := mustProxy(t, backend, denyAll{err: denyReason}, audit, fixedClock(time.Unix(2000, 0)))

	got, err := p.Infer(context.Background(), Request{Subject: "mallory", Model: "qwen", Prompt: "attack"})
	if err == nil {
		t.Fatalf("Infer: expected rejection error, got nil (response=%+v)", got)
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Infer error = %v, want wrapping ErrUnauthorized", err)
	}
	if !errors.Is(err, denyReason) {
		t.Fatalf("Infer error = %v, want to wrap the authorizer reason %v", err, denyReason)
	}
	if got != (Response{}) {
		t.Fatalf("response = %+v, want zero Response on rejection", got)
	}
	// SINK-SIDE PROOF: backend was never invoked.
	if h := backend.hits.Load(); h != 0 {
		t.Fatalf("backend hits = %d, want 0 (denied request MUST NOT reach backend)", h)
	}
}

// TestAuditEntry_AllowDecision asserts an admitted request produces exactly one
// audit entry with the allow decision, correct who/model/bytes/ts.
//
// Mutation: recording DecisionDeny on the allow path (or omitting the Record
// call) fails the decision / count assertions.
func TestAuditEntry_AllowDecision(t *testing.T) {
	backend := &countingBackend{resp: Response{Model: "qwen", Text: "ok", Tokens: 1}}
	audit := NewMemAudit()
	ts := time.Unix(1717, 0)
	p := mustProxy(t, backend, allowAll{}, audit, fixedClock(ts))

	if _, err := p.Infer(context.Background(), Request{Subject: "alice", Model: "qwen", Prompt: "hello"}); err != nil {
		t.Fatalf("Infer: %v", err)
	}
	entries := audit.Entries()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want exactly 1", len(entries))
	}
	e := entries[0]
	if e.Decision != DecisionAllow {
		t.Fatalf("decision = %q, want %q", e.Decision, DecisionAllow)
	}
	if e.Subject != "alice" || e.Model != "qwen" {
		t.Fatalf("audit who/model = %q/%q, want alice/qwen", e.Subject, e.Model)
	}
	if e.PromptBytes != len("hello") {
		t.Fatalf("audit PromptBytes = %d, want %d", e.PromptBytes, len("hello"))
	}
	if !e.Time.Equal(ts) {
		t.Fatalf("audit Time = %v, want %v", e.Time, ts)
	}
	if e.Err != nil {
		t.Fatalf("audit Err = %v, want nil on clean allow", e.Err)
	}
}

// TestAuditEntry_DenyDecision asserts a denied request produces exactly one
// audit entry with the deny decision and the deny reason recorded, with the
// backend never touched.
//
// Mutation: recording DecisionAllow on the deny path fails the decision
// assertion; skipping Record on deny fails the count assertion.
func TestAuditEntry_DenyDecision(t *testing.T) {
	backend := &countingBackend{}
	audit := NewMemAudit()
	ts := time.Unix(42, 0)
	p := mustProxy(t, backend, denyAll{err: errors.New("nope")}, audit, fixedClock(ts))

	if _, err := p.Infer(context.Background(), Request{Subject: "mallory", Model: "llama", Prompt: "xyz"}); err == nil {
		t.Fatalf("Infer: expected rejection")
	}
	entries := audit.Entries()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want exactly 1", len(entries))
	}
	e := entries[0]
	if e.Decision != DecisionDeny {
		t.Fatalf("decision = %q, want %q", e.Decision, DecisionDeny)
	}
	if e.Subject != "mallory" || e.Model != "llama" || e.PromptBytes != 3 {
		t.Fatalf("audit = %+v, want who=mallory model=llama bytes=3", e)
	}
	if !errors.Is(e.Err, ErrUnauthorized) {
		t.Fatalf("audit Err = %v, want wrapping ErrUnauthorized", e.Err)
	}
	if h := backend.hits.Load(); h != 0 {
		t.Fatalf("backend hits = %d, want 0 on deny", h)
	}
}

// TestBackendError_Propagates proves that when admission passes but the backend
// fails, the proxy returns the backend's EXACT error and still records an allow
// audit entry carrying that error.
//
// Mutation: swallowing the backend error (return nil) fails errors.Is; recording
// the wrong decision fails the allow assertion.
func TestBackendError_Propagates(t *testing.T) {
	boom := errors.New("backend exploded")
	backend := &countingBackend{err: boom}
	audit := NewMemAudit()
	p := mustProxy(t, backend, allowAll{}, audit, fixedClock(time.Unix(5, 0)))

	_, err := p.Infer(context.Background(), Request{Subject: "alice", Model: "qwen", Prompt: "go"})
	if !errors.Is(err, boom) {
		t.Fatalf("Infer error = %v, want the backend error %v to propagate", err, boom)
	}
	if h := backend.hits.Load(); h != 1 {
		t.Fatalf("backend hits = %d, want 1 (admitted request reaches backend)", h)
	}
	entries := audit.Entries()
	if len(entries) != 1 || entries[0].Decision != DecisionAllow {
		t.Fatalf("audit = %+v, want 1 allow entry (admitted then backend-failed)", entries)
	}
	if !errors.Is(entries[0].Err, boom) {
		t.Fatalf("audit Err = %v, want the propagated backend error", entries[0].Err)
	}
}

// TestNewProxy_RequiresBackendAndAuthorizer pins the constructor guards.
//
// Mutation: dropping the nil-backend guard returns a non-nil proxy here and
// fails the ErrNilBackend assertion.
func TestNewProxy_RequiresBackendAndAuthorizer(t *testing.T) {
	if _, err := NewProxy(nil, allowAll{}, nil, nil); !errors.Is(err, ErrNilBackend) {
		t.Fatalf("NewProxy(nil backend) err = %v, want ErrNilBackend", err)
	}
	if _, err := NewProxy(&countingBackend{}, nil, nil, nil); !errors.Is(err, ErrNilAuthorizer) {
		t.Fatalf("NewProxy(nil authorizer) err = %v, want ErrNilAuthorizer", err)
	}
}

// TestProxy_ConcurrentInfer exercises the proxy under the race detector with a
// mix of allowed and denied requests, asserting the backend hit count equals the
// number of ALLOWED requests exactly — no denied request leaks through.
//
// Mutation: forwarding before authorizing inflates the hit count past the
// allowed total and fails the exact-equality assertion under -race.
func TestProxy_ConcurrentInfer(t *testing.T) {
	backend := &countingBackend{resp: Response{Model: "qwen", Text: "x"}}
	audit := NewMemAudit()
	secret := []byte("k")
	auth := NewTokenAuthorizer(secret, "infer")
	p := mustProxy(t, backend, auth, audit, fixedClock(time.Unix(1, 0)))

	const allowed, denied = 50, 50
	goodToken := auth.MintToken("alice")

	var wg sync.WaitGroup
	for i := 0; i < allowed; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Infer(context.Background(), Request{Subject: "alice", Model: "qwen", Prompt: "p", Token: goodToken, Scope: "infer"})
		}()
	}
	for i := 0; i < denied; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Infer(context.Background(), Request{Subject: "alice", Model: "qwen", Prompt: "p", Token: "deadbeef", Scope: "infer"})
		}()
	}
	wg.Wait()

	if h := backend.hits.Load(); h != allowed {
		t.Fatalf("backend hits = %d, want exactly %d (only allowed requests forwarded)", h, allowed)
	}
	if audit.Len() != allowed+denied {
		t.Fatalf("audit entries = %d, want %d (one per request)", audit.Len(), allowed+denied)
	}
}
