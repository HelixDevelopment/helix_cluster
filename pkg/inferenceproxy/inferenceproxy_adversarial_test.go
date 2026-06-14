package inferenceproxy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Adversarial probes for the proxy/forwarding bug classes:
//   (1) data race on shared proxy state under concurrent requests,
//   (2) request forwarded to wrong backend / response mis-routed (correlation),
//   (3) request/response fidelity loss (a field dropped or corrupted),
//   (4) backend selection (single-backend model -> the eligible backend),
//   (5) backend error/timeout surfaced, not swallowed into a fake success.
//
// The package is a SINGLE-backend admission-gate proxy (no backend registry /
// connection pool / stats counters). Proxy.Infer is: Authorize(req) FIRST, then
// backend.Infer(ctx, req), returning the backend's exact (resp, err) and
// recording one audit entry. Request is passed by value and the response is
// returned on the same stack frame — so correlation is structural. These tests
// PIN that contract so any future refactor that introduces shared in-flight
// state (a req->resp map, a pool) and thereby cross-wires responses is caught.
// ---------------------------------------------------------------------------

// echoBackend reflects the request it received back into the response so a
// caller can prove THE RESPONSE IT GOT corresponds to THE REQUEST IT SENT. It
// also captures the exact Request it was handed (under a lock) so request-side
// fidelity (every field passed through) can be asserted. This is the sink-side
// oracle for both correlation and fidelity.
type echoBackend struct {
	mu   sync.Mutex
	seen []Request
}

func (b *echoBackend) Infer(_ context.Context, req Request) (Response, error) {
	b.mu.Lock()
	b.seen = append(b.seen, req)
	b.mu.Unlock()
	// Echo identifying request fields into the response. Model is echoed verbatim;
	// Text encodes Subject+Prompt so a mis-routed response is detectable; Tokens
	// encodes the prompt length as an independent numeric correlator.
	return Response{
		Model:  req.Model,
		Text:   "echo:" + req.Subject + "/" + req.Prompt,
		Tokens: len(req.Prompt),
	}, nil
}

// TestFidelity_RequestPassedThroughVerbatim proves every Request field the
// backend needs is forwarded UNCHANGED — none dropped, blanked, or swapped.
//
// Mutation: blanking req.Prompt (or swapping req.Model) before
// p.backend.Infer in Proxy.Infer makes the captured request differ from the
// sent request and fails the field-by-field assertion. (The audit PromptBytes
// would also diverge.)
func TestFidelity_RequestPassedThroughVerbatim(t *testing.T) {
	backend := &echoBackend{}
	audit := NewMemAudit()
	p := mustProxy(t, backend, allowAll{}, audit, fixedClock(time.Unix(10, 0)))

	want := Request{
		Subject: "alice",
		Model:   "qwen-72b",
		Prompt:  "distinct-prompt-é→payload", // multibyte to catch byte/rune confusion
		Token:   "tok-abc",
		Scope:   "infer",
	}
	if _, err := p.Infer(context.Background(), want); err != nil {
		t.Fatalf("Infer: %v", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.seen) != 1 {
		t.Fatalf("backend saw %d requests, want exactly 1", len(backend.seen))
	}
	got := backend.seen[0]
	if got != want {
		t.Fatalf("backend received %+v, want byte-identical forwarded request %+v", got, want)
	}
	// Audit must record the SAME request's prompt byte length (not rune count).
	e := audit.Entries()[0]
	if e.PromptBytes != len(want.Prompt) {
		t.Fatalf("audit PromptBytes = %d, want %d (byte length of forwarded prompt)", e.PromptBytes, len(want.Prompt))
	}
}

// TestFidelity_ResponsePassedThroughVerbatim proves the backend's Response is
// returned to the caller with every field intact (no field zeroed/truncated).
//
// Mutation: returning Response{Model: resp.Model} (dropping Text/Tokens) or
// Response{} from Proxy.Infer fails the exact-field assertion.
func TestFidelity_ResponsePassedThroughVerbatim(t *testing.T) {
	want := Response{Model: "llama-3", Text: "the-exact-generated-text", Tokens: 123}
	backend := &countingBackend{resp: want}
	p := mustProxy(t, backend, allowAll{}, NewMemAudit(), fixedClock(time.Unix(11, 0)))

	got, err := p.Infer(context.Background(), Request{Subject: "a", Model: "llama-3", Prompt: "x"})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if got != want {
		t.Fatalf("response = %+v, want byte-identical backend response %+v", got, want)
	}
}

// TestCorrelation_ConcurrentDistinctRequests_NoCrossWiring is the centerpiece:
// many DISTINCT requests run concurrently through ONE proxy, and EACH caller
// must receive the response derived from ITS OWN request — never another
// goroutine's. The existing race tests send identical requests, so they cannot
// catch a cross-wired response; this one can because every request carries a
// unique Subject/Prompt and the echoBackend reflects them into the response.
//
// WHY IT BITES A CORRELATION BUG: if the proxy ever buffered the in-flight
// request or response in shared Proxy state (a map keyed by something colliding,
// a single reused field), a concurrent caller could receive a response built
// from a different goroutine's request. The per-goroutine equality check
// (response echoes exactly the subject+prompt this goroutine sent) fails the
// instant any response is built from the wrong request. Runs under -race so an
// unguarded shared write is also reported.
func TestCorrelation_ConcurrentDistinctRequests_NoCrossWiring(t *testing.T) {
	backend := &echoBackend{}
	audit := NewMemAudit()
	secret := []byte("corr-key")
	auth := NewTokenAuthorizer(secret) // empty scope set: any scope admits with a valid MAC
	p := mustProxy(t, backend, auth, audit, fixedClock(time.Unix(12, 0)))

	const n = 200
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			subject := fmt.Sprintf("subject-%04d", i)
			prompt := fmt.Sprintf("prompt-%04d-payload", i)
			model := fmt.Sprintf("model-%04d", i)
			token := auth.MintToken(subject) // token binds to THIS subject
			req := Request{Subject: subject, Model: model, Prompt: prompt, Token: token, Scope: "infer"}

			resp, err := p.Infer(context.Background(), req)
			if err != nil {
				errCh <- fmt.Errorf("req %d: unexpected error: %w", i, err)
				return
			}
			// The response MUST be the one derived from THIS request.
			wantText := "echo:" + subject + "/" + prompt
			if resp.Text != wantText {
				errCh <- fmt.Errorf("req %d: response Text = %q, want %q (RESPONSE MIS-ROUTED to wrong caller)", i, resp.Text, wantText)
				return
			}
			if resp.Model != model {
				errCh <- fmt.Errorf("req %d: response Model = %q, want %q (wrong model echoed)", i, resp.Model, model)
				return
			}
			if resp.Tokens != len(prompt) {
				errCh <- fmt.Errorf("req %d: response Tokens = %d, want %d (numeric correlator mismatch)", i, resp.Tokens, len(prompt))
				return
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	// Sink-side: the backend saw exactly n distinct requests, one per caller, with
	// no duplication or loss.
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.seen) != n {
		t.Fatalf("backend saw %d requests, want %d (one per caller)", len(backend.seen), n)
	}
	seenSubjects := make(map[string]int, n)
	for _, r := range backend.seen {
		seenSubjects[r.Subject]++
		// Each forwarded request must still be internally consistent (prompt matches
		// subject index) — proves no field was cross-wired on the request side either.
		var idx int
		if _, err := fmt.Sscanf(r.Subject, "subject-%04d", &idx); err != nil {
			t.Fatalf("backend saw malformed subject %q", r.Subject)
		}
		wantPrompt := fmt.Sprintf("prompt-%04d-payload", idx)
		if r.Prompt != wantPrompt {
			t.Fatalf("backend saw subject %q with prompt %q, want %q (REQUEST fields cross-wired)", r.Subject, r.Prompt, wantPrompt)
		}
	}
	for s, c := range seenSubjects {
		if c != 1 {
			t.Fatalf("subject %q forwarded %d times, want exactly 1 (duplicate/lost forward)", s, c)
		}
	}

	// Audit fidelity: every admitted request produced one allow entry whose
	// recorded Subject/PromptBytes match a real request — no entry attributes one
	// caller's bytes to another.
	if audit.Len() != n {
		t.Fatalf("audit Len = %d, want %d", audit.Len(), n)
	}
	for _, e := range audit.Entries() {
		if e.Decision != DecisionAllow {
			t.Fatalf("audit entry decision = %q, want allow", e.Decision)
		}
		var idx int
		if _, err := fmt.Sscanf(e.Subject, "subject-%04d", &idx); err != nil {
			t.Fatalf("audit entry malformed subject %q", e.Subject)
		}
		if want := len(fmt.Sprintf("prompt-%04d-payload", idx)); e.PromptBytes != want {
			t.Fatalf("audit entry for %q PromptBytes = %d, want %d (audit cross-wired)", e.Subject, e.PromptBytes, want)
		}
	}
}

// TestSelection_SingleBackend_AdmittedRequestAlwaysReachesIt pins the trivial
// "selection" contract of a single-backend proxy: an admitted request ALWAYS
// reaches the one backend exactly once, deterministically, with no path that
// silently drops it or substitutes a fake response.
//
// Mutation: short-circuiting Proxy.Infer to synthesize a response without
// calling backend.Infer (a fake-success path) leaves hits at 0 and returns a
// response the echoBackend never produced -> both assertions fail.
func TestSelection_SingleBackend_AdmittedRequestAlwaysReachesIt(t *testing.T) {
	backend := &echoBackend{}
	p := mustProxy(t, backend, allowAll{}, NewMemAudit(), fixedClock(time.Unix(13, 0)))

	for i := 0; i < 5; i++ {
		req := Request{Subject: "s", Model: "m", Prompt: fmt.Sprintf("p%d", i)}
		resp, err := p.Infer(context.Background(), req)
		if err != nil {
			t.Fatalf("Infer %d: %v", i, err)
		}
		if resp.Text != "echo:s/"+req.Prompt {
			t.Fatalf("req %d response = %q, want the single backend's echo (no fake-success substitution)", i, resp.Text)
		}
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.seen) != 5 {
		t.Fatalf("backend saw %d requests, want 5 (every admitted request reaches the one backend)", len(backend.seen))
	}
}

// errTimeoutBackend simulates a backend that respects context deadlines and
// surfaces a timeout, plus a configurable sentinel error path.
type errTimeoutBackend struct {
	err error
}

func (b *errTimeoutBackend) Infer(ctx context.Context, _ Request) (Response, error) {
	if b.err != nil {
		return Response{}, b.err
	}
	// Honor the deadline: block until ctx is done, then surface ctx.Err().
	<-ctx.Done()
	return Response{}, ctx.Err()
}

// TestErrorSurfacing_BackendTimeoutNotSwallowed proves a backend timeout
// (context deadline) is surfaced to the caller as a real error — NOT swallowed
// into a fake successful Response — and is recorded on the (allow-decision)
// audit entry.
//
// Mutation: a proxy that, on backend error, returned Response{}, nil (swallow)
// or fabricated a Response would fail errors.Is(err, context.DeadlineExceeded)
// and the non-empty-error audit assertion.
func TestErrorSurfacing_BackendTimeoutNotSwallowed(t *testing.T) {
	backend := &errTimeoutBackend{}
	audit := NewMemAudit()
	p := mustProxy(t, backend, allowAll{}, audit, fixedClock(time.Unix(14, 0)))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	resp, err := p.Infer(ctx, Request{Subject: "a", Model: "m", Prompt: "slow"})
	if err == nil {
		t.Fatalf("Infer returned nil error on backend timeout (resp=%+v) — timeout SWALLOWED into fake success", resp)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Infer err = %v, want context.DeadlineExceeded surfaced from backend", err)
	}
	if resp != (Response{}) {
		t.Fatalf("response = %+v, want zero Response when backend errors", resp)
	}
	// The request WAS admitted (auth passed), so the audit entry is an allow that
	// carries the backend error — proving the error reached the audit sink too.
	entries := audit.Entries()
	if len(entries) != 1 || entries[0].Decision != DecisionAllow {
		t.Fatalf("audit = %+v, want 1 allow entry (admitted, then backend timed out)", entries)
	}
	if !errors.Is(entries[0].Err, context.DeadlineExceeded) {
		t.Fatalf("audit Err = %v, want the surfaced timeout error", entries[0].Err)
	}
}

// TestErrorSurfacing_BackendSentinelErrorExact proves a non-timeout backend
// error is returned EXACTLY (errors.Is matches the sentinel), not replaced or
// dropped.
//
// Mutation: wrapping-away or swallowing the backend error fails errors.Is.
func TestErrorSurfacing_BackendSentinelErrorExact(t *testing.T) {
	sentinel := errors.New("backend: model unavailable")
	backend := &errTimeoutBackend{err: sentinel}
	p := mustProxy(t, backend, allowAll{}, NewMemAudit(), fixedClock(time.Unix(15, 0)))

	_, err := p.Infer(context.Background(), Request{Subject: "a", Model: "m", Prompt: "x"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Infer err = %v, want exact backend sentinel %v surfaced", err, sentinel)
	}
}
