package inference

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/llm"
)

// ----------------------------------------------------------------------------
// (b) UNGUARDED SHARED STATE — concurrency under -race + conservation.
//
// The Router doc promises a registry guarded by sync.RWMutex. We hammer it:
// concurrent Register (writers) interleaved with Route / Candidates / Backends
// (readers). -race must stay clean (no torn slice append vs read).
// ----------------------------------------------------------------------------

// TestRouterConcurrentRegisterAndRoute_RaceClean drives concurrent writers
// (Register) against concurrent readers (Route / Candidates / Backends).
//
// SINK: -race detector. A missing lock on the backends slice would be reported
// as a data race here (append racing copy/range).
func TestRouterConcurrentRegisterAndRoute_RaceClean(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	// Seed one always-capable backend so Route has something to serve.
	must(t, r.Register(newRef("seed", true, "m")))

	const writers, readers, iters = 8, 8, 200
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// unique names so Register never rejects; grows the slice.
				_ = r.Register(newRef(fmt.Sprintf("w%d-%d", id, i), true, "m"))
			}
		}(w)
	}
	for rd := 0; rd < readers; rd++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := Request{Model: "m", Prompt: "hi"}
			for i := 0; i < iters; i++ {
				_, _ = r.Route(context.Background(), req)
				_ = r.Candidates(req)
				_ = r.Backends()
			}
		}()
	}
	wg.Wait()

	// Sink-side: every successfully-registered backend must be present.
	got := len(r.Backends())
	want := 1 + writers*iters
	if got != want {
		t.Fatalf("registered backend count = %d, want %d (lost registrations)", got, want)
	}
}

// TestRouterConcurrentRoute_Conservation fixes the registry, then fires many
// concurrent Route calls. CONSERVATION: every call must be served by exactly the
// correct backend ("only"); none lost, none misrouted, no spurious error.
//
// SINK: the actual served resp.Backend on each goroutine + an error tally.
func TestRouterConcurrentRoute_Conservation(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	must(t, r.Register(newRef("down", false, "m"))) // forces fallback every call
	must(t, r.Register(newRef("only", true, "m")))  // the sole healthy server

	const n = 500
	results := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			resp, err := r.Route(context.Background(), Request{Model: "m", Prompt: "hi"})
			results[idx] = resp.Backend
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	served := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("call %d errored: %v (want all served)", i, errs[i])
		}
		if results[i] != "only" {
			t.Fatalf("call %d served by %q, want %q (misrouted)", i, results[i], "only")
		}
		served++
	}
	if served != n {
		t.Fatalf("served %d/%d requests (conservation violated)", served, n)
	}
}

// ----------------------------------------------------------------------------
// (a)/(c)/(d) NUMERIC / VALIDATION — negative MaxTokens.
//
// Doc (Request.MaxTokens): "the requested generation budget; 0 means backend
// default." Capabilities.Supports gates only `req.MaxTokens > c.MaxTokens`, so a
// NEGATIVE budget is never rejected. referenceGenerate / LocalBackend.Infer then
// coerce `budget <= 0` to 16. We pin the SINK: what Tokens count a caller who
// passed MaxTokens=-5 actually receives.
// ----------------------------------------------------------------------------

// TestNegativeMaxTokensCoercedToDefault pins the observed contract: a negative
// MaxTokens is NOT rejected; it is treated like 0 (default budget 16). This is a
// deliberate "<=0 means default" coercion, mirrored in both backends. We assert
// the sink so any future drift (e.g. emitting Tokens=-5, or a negative-length
// slice elsewhere) is caught.
func TestNegativeMaxTokensCoercedToDefault(t *testing.T) {
	t.Parallel()
	b := &ReferenceBackend{BackendName: "ref", Up: true, Caps: Capabilities{Models: []string{"m"}}}

	// Negative budget is accepted (capability check does not reject it).
	if !b.Caps.Supports(Request{Model: "m", MaxTokens: -5}) {
		t.Fatal("precondition: negative MaxTokens unexpectedly rejected by Supports")
	}
	resp, err := b.Infer(context.Background(), Request{Model: "m", Prompt: "hi", MaxTokens: -5})
	if err != nil {
		t.Fatalf("Infer with negative MaxTokens errored: %v", err)
	}
	// SINK: the emitted token count is the default 16, never the negative input.
	if resp.Tokens != 16 {
		t.Fatalf("resp.Tokens = %d for MaxTokens=-5, want coerced default 16", resp.Tokens)
	}
	if resp.Tokens < 0 {
		t.Fatalf("resp.Tokens = %d is negative — fabricated invalid count", resp.Tokens)
	}
}

// TestMaxTokensCapStillRejectsNegativeAwareBackend documents that an explicit
// per-backend cap does NOT turn a negative request into a rejection: -5 is not
// > cap, so it passes capability matching and is served by the capped backend.
//
// MUTATION (would-fail direction): if Supports were changed to also reject
// req.MaxTokens < 0, this backend would no longer match and resp.Backend would
// be empty/error. We pin current behavior.
func TestNegativeMaxTokensPassesCapGate(t *testing.T) {
	t.Parallel()
	capped := &ReferenceBackend{
		BackendName: "capped", Up: true,
		Caps: Capabilities{Models: []string{"m"}, MaxTokens: 100},
	}
	r := NewRouter()
	must(t, r.Register(capped))
	resp, err := r.Route(context.Background(), Request{Model: "m", Prompt: "hi", MaxTokens: -1})
	if err != nil {
		t.Fatalf("Route with MaxTokens=-1 errored: %v", err)
	}
	if resp.Backend != "capped" {
		t.Fatalf("served backend = %q, want capped (negative budget passes gate)", resp.Backend)
	}
}

// ----------------------------------------------------------------------------
// (c) VALIDATION / FAIL-OPEN — empty Model.
//
// Request.Model is documented "(required)". Route itself only rejects an empty
// PROMPT; it never rejects an empty Model. Whether an empty-model request is
// refused depends entirely on capability matching. We pin BOTH sides.
// ----------------------------------------------------------------------------

// TestEmptyModelRejectedByNormalBackends: with a normal backend (Models:["m"]),
// an empty Model fails capability match -> ErrUnsupported. No fail-open here.
func TestEmptyModelRejectedByNormalBackends(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	must(t, r.Register(newRef("only", true, "m")))
	_, err := r.Route(context.Background(), Request{Model: "", Prompt: "hi"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("empty-model request err = %v, want ErrUnsupported", err)
	}
}

// TestEmptyModelServedIfBackendAdvertisesIt is the FAIL-OPEN pin: Route performs
// NO model-required validation of its own, so a backend that advertises "" in
// its Models will happily serve a Model:"" request. This is a latent risk: the
// "required" invariant lives only in capability data, not in Route.
//
// SINK: resp.Backend is the empty-model backend; the request is served, not
// rejected — proving Route does not enforce Model!="".
func TestEmptyModelServedIfBackendAdvertisesIt(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	// A backend that (mis)advertises the empty model name.
	must(t, r.Register(&ReferenceBackend{
		BackendName: "loose", Up: true, Caps: Capabilities{Models: []string{""}},
	}))
	resp, err := r.Route(context.Background(), Request{Model: "", Prompt: "hi"})
	if err != nil {
		t.Fatalf("expected empty-model to be served by loose backend, got err: %v", err)
	}
	if resp.Backend != "loose" {
		t.Fatalf("served backend = %q, want loose (Route does not enforce Model!=\"\")", resp.Backend)
	}
}

// ----------------------------------------------------------------------------
// LocalBackend REAL gating path — wire an actual llm.Manager (no mock of the
// manager; uses the real registry + a tiny real Generate backend).
// ----------------------------------------------------------------------------

type echoGen struct{}

func (echoGen) Generate(model, prompt string) (string, error) {
	return "out:" + model + ":" + prompt, nil
}

// TestLocalBackendGatesUnloadedModel proves the load-state contract end-to-end
// through the Router: a registered-but-UNLOADED model is refused (ErrModelNotReady
// surfaces via the all-candidates-failed wrap), and once LOADED it is served.
//
// SINK: the all-failed error wraps ErrModelNotReady before load; resp.Backend
// after load.
func TestLocalBackendGatesUnloadedModel(t *testing.T) {
	t.Parallel()
	mgr := llm.WithBackend(echoGen{})
	must(t, mgr.RegisterModel("m1", "/tmp/m1", "gguf"))

	lb := NewLocalBackend("local", mgr)
	r := NewRouter()
	must(t, r.Register(lb))

	// Registered but not loaded: LocalBackend.Available() is false (no loaded
	// model), so Route skips it -> all candidates fail. The underlying Infer would
	// return ErrModelNotReady; confirm Available gating refuses service.
	_, err := r.Route(context.Background(), Request{Model: "m1", Prompt: "hi"})
	if err == nil {
		t.Fatal("unloaded model was served; want refusal")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v; capability matched (model registered) so must NOT be ErrUnsupported", err)
	}

	// Direct Infer (bypassing Available gate) must surface the typed not-ready err.
	if _, ierr := lb.Infer(context.Background(), Request{Model: "m1", Prompt: "hi"}); !errors.Is(ierr, ErrModelNotReady) {
		t.Fatalf("unloaded Infer err = %v, want ErrModelNotReady", ierr)
	}

	// Now load it: it becomes available and is served.
	must(t, mgr.LoadModel("m1"))
	resp, err := r.Route(context.Background(), Request{Model: "m1", Prompt: "hi"})
	if err != nil {
		t.Fatalf("loaded model Route errored: %v", err)
	}
	if resp.Backend != "local" {
		t.Fatalf("served backend = %q, want local", resp.Backend)
	}
	if resp.Text != "out:m1:hi" {
		t.Fatalf("served text = %q, want real manager output out:m1:hi", resp.Text)
	}
}
