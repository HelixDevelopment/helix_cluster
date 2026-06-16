package runpod_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/pool"
	"github.com/HelixDevelopment/helix_cluster/pkg/provider/runpod"
)

// ---------------------------------------------------------------------------
// Adversarial probes for pkg/provider/runpod.
//
// Each probe targets a recurring killer edge and is classified inline:
//   REAL BUG           — current code is wrong; test asserts the CORRECT behavior.
//   DOCUMENTED CONTRACT — pins behavior the docstring promises; no fix wanted.
//   LATENT RISK        — surprising-but-tolerable; pinned so a regression is loud.
// ---------------------------------------------------------------------------

// failTermClient cold-provisions real ids but FAILS every Terminate, so the
// surplus-terminate path and the pre-warm rollback path can be probed.
type failTermClient struct {
	mu        sync.Mutex
	seq       int
	live      map[string]runpod.Worker
	termCalls int
	termErr   error
}

func newFailTermClient(termErr error) *failTermClient {
	return &failTermClient{live: make(map[string]runpod.Worker), termErr: termErr}
}

func (f *failTermClient) ColdProvision(ctx context.Context, spec pool.Spec) (runpod.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	w := runpod.Worker{
		ID:        fmt.Sprintf("ft-%04d", f.seq),
		GPUType:   spec.GPUType,
		HourlyUSD: spec.HourlyUSD,
		Endpoint:  fmt.Sprintf("https://ft-%04d.local", f.seq),
	}
	f.live[w.ID] = w
	return w, nil
}

func (f *failTermClient) Terminate(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.termCalls++
	if f.termErr != nil {
		return f.termErr
	}
	delete(f.live, id)
	return nil
}

func (f *failTermClient) TermCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.termCalls }

// fixedClient always returns the SAME worker handle (constant id + price). Used
// to probe duplicate-id capacity accounting and NaN/Inf price flow-through.
type fixedClient struct {
	w   runpod.Worker
	mu  sync.Mutex
	n   int
}

func (c *fixedClient) ColdProvision(ctx context.Context, spec pool.Spec) (runpod.Worker, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.w, nil
}
func (c *fixedClient) Terminate(ctx context.Context, id string) error { return nil }

// ---------------------------------------------------------------------------

// TestCapacityZeroNegativeFailsClosed — capacity 0 / negative MUST be rejected
// by New, never silently treated as "unlimited" or "1".
// CLASS: DOCUMENTED CONTRACT (New: "capacity must be > 0").
func TestCapacityZeroNegativeFailsClosed(t *testing.T) {
	for _, cap := range []int{0, -1, -1000} {
		if _, err := runpod.New(context.Background(), runpod.Config{
			Capacity: cap,
			Client:   newFakeClient(),
		}); err == nil {
			t.Fatalf("New with capacity=%d: want error, got nil (fail-open quota)", cap)
		}
	}
}

// TestProvisionAtCapacityFailsClosed — once Capacity leased instances are live,
// the next cold Provision MUST fail with ErrAtCapacity, not over-subscribe.
// CLASS: DOCUMENTED CONTRACT.
func TestProvisionAtCapacityFailsClosed(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClient()
	p, err := runpod.New(ctx, runpod.Config{Capacity: 2, Client: fc})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.Provision(ctx, pool.Spec{GPUType: "A100"}); err != nil {
			t.Fatalf("provision %d: %v", i, err)
		}
	}
	if _, err := p.Provision(ctx, pool.Spec{GPUType: "A100"}); !errors.Is(err, runpod.ErrAtCapacity) {
		t.Fatalf("over-capacity provision: want ErrAtCapacity, got %v", err)
	}
}

// TestNaNInfPriceFlowsThroughIntact — a worker whose HourlyUSD is NaN/+Inf/-Inf
// must NOT panic and must NOT be silently rewritten to 0; the price the wire
// reported is what the leased Instance reports (sink-side honesty). This probes
// that no min/max/comparator over price corrupts selection or accounting.
// CLASS: LATENT RISK (no price comparator exists today; pin against one creeping in).
func TestNaNInfPriceFlowsThroughIntact(t *testing.T) {
	ctx := context.Background()
	for _, price := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		fc := &fixedClient{w: runpod.Worker{ID: "p1", GPUType: "A100", HourlyUSD: price, Endpoint: "e"}}
		p, err := runpod.New(ctx, runpod.Config{Capacity: 1, Client: fc})
		if err != nil {
			t.Fatal(err)
		}
		inst, err := p.Provision(ctx, pool.Spec{GPUType: "A100"})
		if err != nil {
			t.Fatalf("provision price=%v: %v", price, err)
		}
		if math.IsNaN(price) {
			if !math.IsNaN(inst.HourlyUSD) {
				t.Fatalf("NaN price corrupted to %v", inst.HourlyUSD)
			}
		} else if inst.HourlyUSD != price {
			t.Fatalf("price %v corrupted to %v", price, inst.HourlyUSD)
		}
	}
}

// TestSurplusTerminateFailureSurfaces — when a released worker is beyond the
// warm target it is terminated over the wire; if Terminate FAILS, Release MUST
// surface the error (never swallow it into a fake success). And the instance
// must NOT remain leased (it has been handed back).
// CLASS: DOCUMENTED CONTRACT.
func TestSurplusTerminateFailureSurfaces(t *testing.T) {
	ctx := context.Background()
	termErr := errors.New("boom")
	fc := newFailTermClient(termErr)
	// WarmTarget 0 => every released worker is surplus => terminated.
	p, err := runpod.New(ctx, runpod.Config{Capacity: 1, WarmTarget: 0, Client: fc})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := p.Provision(ctx, pool.Spec{GPUType: "A100"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Release(ctx, inst); err == nil {
		t.Fatal("Release surplus with failing Terminate: want error, got nil (swallowed failure)")
	}
	if fc.TermCalls() != 1 {
		t.Fatalf("Terminate calls = %d, want 1", fc.TermCalls())
	}
	// The released worker must be gone from tracking and not block a re-provision.
	if _, ok := p.OriginOf(inst.ID); ok {
		t.Fatalf("instance %q still tracked after Release", inst.ID)
	}
	if _, err := p.Provision(ctx, pool.Spec{GPUType: "A100"}); err != nil {
		t.Fatalf("re-provision after surplus release: %v (capacity slot leaked)", err)
	}
}

// TestPreWarmRollbackOnFailure — if a pre-warm cold provision fails partway,
// New MUST roll back (Terminate) the workers it already warmed and return an
// error, never a half-built provider that lies about being warm.
// CLASS: DOCUMENTED CONTRACT.
func TestPreWarmRollbackOnFailure(t *testing.T) {
	ctx := context.Background()
	fc := &countingFailAfter{n: 2, live: make(map[string]runpod.Worker)}
	_, err := runpod.New(ctx, runpod.Config{Capacity: 5, WarmTarget: 4, Client: fc})
	if err == nil {
		t.Fatal("New with failing pre-warm: want error, got nil")
	}
	// The 2 successfully-warmed workers must have been terminated (rolled back).
	if fc.TermCalls() != 2 {
		t.Fatalf("rollback Terminate calls = %d, want 2 (warmed workers leaked)", fc.TermCalls())
	}
}

// countingFailAfter succeeds n cold provisions then fails; tracks terminates.
type countingFailAfter struct {
	mu        sync.Mutex
	n         int
	seq       int
	termCalls int
	live      map[string]runpod.Worker
}

func (c *countingFailAfter) ColdProvision(ctx context.Context, spec pool.Spec) (runpod.Worker, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seq >= c.n {
		return runpod.Worker{}, errors.New("provision exhausted")
	}
	c.seq++
	w := runpod.Worker{ID: fmt.Sprintf("cf-%04d", c.seq), GPUType: spec.GPUType, Endpoint: "e"}
	c.live[w.ID] = w
	return w, nil
}
func (c *countingFailAfter) Terminate(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.termCalls++
	delete(c.live, id)
	return nil
}
func (c *countingFailAfter) TermCalls() int { c.mu.Lock(); defer c.mu.Unlock(); return c.termCalls }

// TestColdProvisionEmptyIDFailsClosed — a 2xx response carrying an EMPTY worker
// id must NOT fabricate a leased instance with id ""; Provision must error.
// CLASS: DOCUMENTED CONTRACT (recordLeased must never run on empty id).
func TestColdProvisionEmptyIDFailsClosed(t *testing.T) {
	ctx := context.Background()
	fc := &fixedClient{w: runpod.Worker{ID: "", GPUType: "A100", HourlyUSD: 1.0}}
	p, err := runpod.New(ctx, runpod.Config{Capacity: 1, Client: fc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(ctx, pool.Spec{GPUType: "A100"}); !errors.Is(err, runpod.ErrColdProvisionFailed) {
		t.Fatalf("empty-id provision: want ErrColdProvisionFailed, got %v", err)
	}
}

// TestHTTPGarbage2xxBodyDoesNotFabricateSuccess — the default HTTP client given
// a 200 with a garbage/non-JSON body must NOT panic (DoS) and must NOT return a
// fake-success Worker; it must return an error.
// CLASS: DOCUMENTED CONTRACT (DoS / fabricated success).
func TestHTTPGarbage2xxBodyDoesNotFabricateSuccess(t *testing.T) {
	ctx := context.Background()
	bodies := []string{
		"not json at all <<<>>>",
		`{"id":`,            // truncated json
		`[1,2,3]`,           // wrong shape (array)
		`"just a string"`,   // wrong shape (string)
		``,                  // empty 2xx body
		`null`,              // json null -> zero worker -> empty id
	}
	for _, b := range bodies {
		body := b
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		p, err := runpod.New(ctx, runpod.Config{Capacity: 1, BaseURL: srv.URL})
		if err != nil {
			srv.Close()
			t.Fatalf("New: %v", err)
		}
		_, perr := p.Provision(ctx, pool.Spec{GPUType: "A100"})
		if perr == nil {
			srv.Close()
			t.Fatalf("garbage 2xx body %q fabricated a success", body)
		}
		srv.Close()
	}
}

// TestReleaseUnknownInstanceFailsClosed — releasing an instance the provider
// never handed out (or already released) MUST return ErrUnknownInstance, so
// double-release / wrong-provider bugs surface instead of silently succeeding.
// CLASS: DOCUMENTED CONTRACT.
func TestReleaseUnknownInstanceFailsClosed(t *testing.T) {
	ctx := context.Background()
	p, err := runpod.New(ctx, runpod.Config{Capacity: 2, Client: newFakeClient()})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Release(ctx, pool.Instance{ID: "never-existed"}); !errors.Is(err, runpod.ErrUnknownInstance) {
		t.Fatalf("release unknown: want ErrUnknownInstance, got %v", err)
	}
	// Double-release: provision, release into warm, release again must fail.
	inst, err := p.Provision(ctx, pool.Spec{GPUType: "A100"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Release(ctx, inst); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := p.Release(ctx, inst); !errors.Is(err, runpod.ErrUnknownInstance) {
		t.Fatalf("double release: want ErrUnknownInstance, got %v", err)
	}
}

// TestDuplicateWorkerIDRejected_HXC1910 is the regression guard for the fixed
// duplicate-worker-id capacity bypass.
//
// THE BUG (fixed): recordLeasedLocked does `p.leased[w.ID] = w` keyed by w.ID.
// If the control plane returned the SAME worker id for two distinct cold
// provisions, the second silently OVERWROTE the first, so len(p.leased) stayed
// 1, liveLocked() undercounted, the Capacity gate never tripped, and the
// provider rented REAL billable workers without bound (Capacity=2 → 10 accepted).
//
// THE FIX: Provision fails closed when ColdProvision returns an already-leased
// id (ErrColdProvisionFailed: duplicate worker id). A conformant backend mints
// unique ids so this never fires in normal use; a misbehaving one is rejected
// instead of leaking untracked workers.
//
// CONTRACT: pool.GPUProvider.Capacity is a hard upper bound; a Provision beyond
// it (or one that cannot be tracked) must fail. This test FAILS if the dup guard
// is removed (the bypass returns and accepted climbs to 10).
func TestDuplicateWorkerIDRejected_HXC1910(t *testing.T) {
	ctx := context.Background()
	fc := &fixedClient{w: runpod.Worker{ID: "dup", GPUType: "A100", HourlyUSD: 1.0, Endpoint: "e"}}
	p, err := runpod.New(ctx, runpod.Config{Capacity: 2, Client: fc})
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	var lastErr error
	for i := 0; i < 10; i++ {
		if _, e := p.Provision(ctx, pool.Spec{GPUType: "A100"}); e == nil {
			accepted++
		} else {
			lastErr = e
		}
	}
	// Fixed: only the FIRST unique id is leased; every duplicate id is rejected
	// fail-closed, so the provider never over-subscribes Capacity or leaks workers.
	if accepted != 1 {
		t.Fatalf("duplicate worker id must be rejected fail-closed: accepted=%d, want exactly 1 "+
			"(if accepted>1 the dup guard in Provision was removed and the Capacity quota is bypassed)", accepted)
	}
	if l := p.Metrics().Leased; l != 1 {
		t.Fatalf("Metrics.Leased=%d, want 1 (one unique worker leased)", l)
	}
	if lastErr == nil {
		t.Fatalf("expected a duplicate-worker-id rejection error from the 2nd+ provision, got none")
	}
}
