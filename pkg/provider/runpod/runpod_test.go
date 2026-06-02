package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/pool"
	"github.com/HelixDevelopment/helix_cluster/pkg/provider/runpod"
)

// compile-time proof RunPodProvider satisfies the pkg/pool.GPUProvider seam.
var _ pool.GPUProvider = (*runpod.RunPodProvider)(nil)

// fakeClient is a real-state in-process RunPodClient. It is NOT a no-op mock: it
// mints real worker IDs, tracks the set of live workers, and counts every
// ColdProvision / Terminate call so tests can assert which provisioning PATH
// actually executed (warm fast-path vs cold client call). This is the injected
// seam standing in for the out-of-host RunPod control API.
type fakeClient struct {
	mu        sync.Mutex
	seq       int
	live      map[string]runpod.Worker
	coldCalls int
	termCalls int
	failCold  error
}

func newFakeClient() *fakeClient {
	return &fakeClient{live: make(map[string]runpod.Worker)}
}

func (f *fakeClient) ColdProvision(ctx context.Context, spec pool.Spec) (runpod.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coldCalls++
	if f.failCold != nil {
		return runpod.Worker{}, f.failCold
	}
	f.seq++
	w := runpod.Worker{
		ID:        fmt.Sprintf("w-%04d", f.seq),
		GPUType:   spec.GPUType,
		HourlyUSD: spec.HourlyUSD,
		Endpoint:  fmt.Sprintf("https://worker-%04d.runpod.local", f.seq),
	}
	f.live[w.ID] = w
	return w, nil
}

func (f *fakeClient) Terminate(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.termCalls++
	delete(f.live, id)
	return nil
}

func (f *fakeClient) ColdCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.coldCalls }
func (f *fakeClient) TermCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.termCalls }
func (f *fakeClient) LiveCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.live) }

// TestProvisionWarmFastPathDoesNotCallClient is the load-bearing closure test.
// With a pre-warmed pool, Provision MUST be served from the warm pool WITHOUT
// any cold client call, must mark the instance Origin=warm, and must decrement
// the warm pool. This is what the warm-pool fast-path check buys us.
//
// MUTATION GUARD: if the fast-path branch (`if len(p.warm) > 0 { ... }`) is
// removed/inverted so Provision always cold-provisions, then:
//   - coldCalls would jump from 2 (pre-warm) to 3, failing the cold-call assert
//   - Origin would be "cold", failing the origin assert
//   - WarmCount would not decrement to 1, failing the warm-count assert
func TestProvisionWarmFastPathDoesNotCallClient(t *testing.T) {
	t.Parallel()
	fc := newFakeClient()
	spec := pool.Spec{GPUType: "A100", HourlyUSD: 2.5}
	p, err := runpod.New(context.Background(), runpod.Config{
		Capacity:   4,
		WarmTarget: 2,
		Spec:       spec,
		Client:     fc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Pre-warm cold-provisioned exactly WarmTarget workers.
	if got := fc.ColdCalls(); got != 2 {
		t.Fatalf("pre-warm cold calls = %d, want 2", got)
	}
	if got := p.WarmCount(); got != 2 {
		t.Fatalf("warm count after pre-warm = %d, want 2", got)
	}

	coldBefore := fc.ColdCalls()
	inst, err := p.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("Provision (warm): %v", err)
	}
	// FAST PATH proof #1: no new cold client call.
	if got := fc.ColdCalls(); got != coldBefore {
		t.Fatalf("warm Provision triggered a cold client call: cold calls %d -> %d", coldBefore, got)
	}
	// FAST PATH proof #2: origin is warm.
	if o, ok := p.OriginOf(inst.ID); !ok || o != runpod.OriginWarm {
		t.Fatalf("OriginOf(%q) = %v,%v want warm,true", inst.ID, o, ok)
	}
	// FAST PATH proof #3: warm pool decremented.
	if got := p.WarmCount(); got != 1 {
		t.Fatalf("warm count after warm Provision = %d, want 1", got)
	}
	// Sink-side: the instance is now listed as leased.
	list, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != inst.ID {
		t.Fatalf("List = %+v, want exactly the leased instance %q", list, inst.ID)
	}
	if m := p.Metrics(); m.WarmHits != 1 || m.ColdCalls != 0 {
		t.Fatalf("metrics after one warm Provision = %+v, want WarmHits=1 ColdCalls=0", m)
	}
}

// TestProvisionColdWhenWarmEmpty proves that with the warm pool exhausted,
// Provision falls through to a real cold client call and marks Origin=cold.
func TestProvisionColdWhenWarmEmpty(t *testing.T) {
	t.Parallel()
	fc := newFakeClient()
	spec := pool.Spec{GPUType: "H100", HourlyUSD: 4}
	p, err := runpod.New(context.Background(), runpod.Config{
		Capacity:   4,
		WarmTarget: 0, // no pre-warm -> warm pool empty
		Spec:       spec,
		Client:     fc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := fc.ColdCalls(); got != 0 {
		t.Fatalf("cold calls after zero-warm New = %d, want 0", got)
	}

	inst, err := p.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("Provision (cold): %v", err)
	}
	// Cold path proof: the client WAS called exactly once.
	if got := fc.ColdCalls(); got != 1 {
		t.Fatalf("cold Provision client calls = %d, want 1", got)
	}
	if o, ok := p.OriginOf(inst.ID); !ok || o != runpod.OriginCold {
		t.Fatalf("OriginOf(%q) = %v,%v want cold,true", inst.ID, o, ok)
	}
	if got := fc.LiveCount(); got != 1 {
		t.Fatalf("client live workers = %d, want 1", got)
	}
	if m := p.Metrics(); m.ColdCalls != 1 || m.WarmHits != 0 {
		t.Fatalf("metrics after one cold Provision = %+v, want ColdCalls=1 WarmHits=0", m)
	}
}

// TestReleaseReWarmsAndUpdatesSinks proves Release returns the worker to the
// warm pool (re-warm), removes it from the live leased list, and that a
// subsequent Provision then hits the fast path again (no new cold call).
func TestReleaseReWarmsAndUpdatesSinks(t *testing.T) {
	t.Parallel()
	fc := newFakeClient()
	spec := pool.Spec{GPUType: "A100", HourlyUSD: 2}
	p, err := runpod.New(context.Background(), runpod.Config{
		Capacity:   2,
		WarmTarget: 1,
		Spec:       spec,
		Client:     fc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	inst, err := p.Provision(context.Background(), spec) // serves the 1 warm
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if p.WarmCount() != 0 {
		t.Fatalf("warm count after warm Provision = %d, want 0", p.WarmCount())
	}
	if list, _ := p.List(context.Background()); len(list) != 1 {
		t.Fatalf("leased list = %d, want 1", len(list))
	}

	coldBefore := fc.ColdCalls()
	if err := p.Release(context.Background(), inst); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Sink-side: re-warmed (back in the warm pool), not in the leased list.
	if got := p.WarmCount(); got != 1 {
		t.Fatalf("warm count after Release = %d, want 1 (re-warmed)", got)
	}
	if list, _ := p.List(context.Background()); len(list) != 0 {
		t.Fatalf("leased list after Release = %d, want 0", len(list))
	}
	// Re-warm reuses the worker; it must NOT be terminated over the wire.
	if got := fc.TermCalls(); got != 0 {
		t.Fatalf("Release within warm target terminated a worker: term calls = %d, want 0", got)
	}

	// Next Provision hits the fast path again -> no new cold call.
	if _, err := p.Provision(context.Background(), spec); err != nil {
		t.Fatalf("Provision after re-warm: %v", err)
	}
	if got := fc.ColdCalls(); got != coldBefore {
		t.Fatalf("Provision after re-warm cold-provisioned: cold calls %d -> %d", coldBefore, got)
	}
}

// TestReleaseSurplusTerminates proves a Release beyond WarmTarget terminates the
// surplus worker over the wire instead of growing the warm pool unbounded.
func TestReleaseSurplusTerminates(t *testing.T) {
	t.Parallel()
	fc := newFakeClient()
	spec := pool.Spec{GPUType: "A100", HourlyUSD: 2}
	p, err := runpod.New(context.Background(), runpod.Config{
		Capacity:   3,
		WarmTarget: 0, // never re-warm -> every Release is surplus
		Spec:       spec,
		Client:     fc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	inst, err := p.Provision(context.Background(), spec) // cold
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := p.Release(context.Background(), inst); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := fc.TermCalls(); got != 1 {
		t.Fatalf("surplus Release term calls = %d, want 1", got)
	}
	if got := p.WarmCount(); got != 0 {
		t.Fatalf("warm count after surplus Release = %d, want 0", got)
	}
}

// TestReleaseUnknownInstance proves a double/wrong release surfaces an error.
func TestReleaseUnknownInstance(t *testing.T) {
	t.Parallel()
	fc := newFakeClient()
	p, err := runpod.New(context.Background(), runpod.Config{Capacity: 1, Client: fc})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = p.Release(context.Background(), pool.Instance{ID: "nope"})
	if !errors.Is(err, runpod.ErrUnknownInstance) {
		t.Fatalf("Release(unknown) err = %v, want ErrUnknownInstance", err)
	}
}

// TestCapacityEnforced proves Provision past Capacity fails with ErrAtCapacity
// (no warm reserve to draw on).
func TestCapacityEnforced(t *testing.T) {
	t.Parallel()
	fc := newFakeClient()
	spec := pool.Spec{GPUType: "A100"}
	p, err := runpod.New(context.Background(), runpod.Config{Capacity: 1, WarmTarget: 0, Spec: spec, Client: fc})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Provision(context.Background(), spec); err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	_, err = p.Provision(context.Background(), spec)
	if !errors.Is(err, runpod.ErrAtCapacity) {
		t.Fatalf("over-capacity Provision err = %v, want ErrAtCapacity", err)
	}
	if got := p.Capacity(); got != 1 {
		t.Fatalf("Capacity() = %d, want 1", got)
	}
}

// TestPreWarmFailureRollsBack proves New does not return a half-warmed provider
// when a pre-warm cold provision fails: it terminates whatever it warmed.
func TestPreWarmFailureRollsBack(t *testing.T) {
	t.Parallel()
	fc := newFakeClient()
	fc.failCold = errors.New("runpod down")
	_, err := runpod.New(context.Background(), runpod.Config{Capacity: 3, WarmTarget: 2, Client: fc})
	if err == nil {
		t.Fatalf("New with failing pre-warm: want error, got nil")
	}
}

// TestNewRejectsBadCapacity proves a misconfigured provider never builds.
func TestNewRejectsBadCapacity(t *testing.T) {
	t.Parallel()
	if _, err := runpod.New(context.Background(), runpod.Config{Capacity: 0, Client: newFakeClient()}); err == nil {
		t.Fatalf("New(capacity=0): want error, got nil")
	}
}

// --- Integration test against a REAL in-process net/http/httptest server ---
// This exercises the DEFAULT HTTPRunPodClient end to end over real sockets (no
// injected fake): the warm-pool logic drives real HTTP cold-provision and the
// fast path is proven by counting server-side cold-provision hits. This is the
// CLAUDE-1 consumer-style proof that the feature works over the wire, not just
// against a fake.

func TestHTTPClientWarmPoolEndToEnd(t *testing.T) {
	t.Parallel()
	var (
		mu       sync.Mutex
		provHits int
		termHits int
		seq      int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/workers":
			mu.Lock()
			provHits++
			seq++
			id := fmt.Sprintf("srv-%04d", seq)
			mu.Unlock()
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(runpod.Worker{
				ID:        id,
				GPUType:   "A100",
				HourlyUSD: 2.5,
				Endpoint:  "https://" + id + ".runpod.local",
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/workers/"):
			mu.Lock()
			termHits++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	spec := pool.Spec{GPUType: "A100", HourlyUSD: 2.5}
	p, err := runpod.New(context.Background(), runpod.Config{
		Capacity:   4,
		WarmTarget: 2,
		Spec:       spec,
		BaseURL:    srv.URL, // default HTTPRunPodClient hits the real test server
	})
	if err != nil {
		t.Fatalf("New (http): %v", err)
	}
	mu.Lock()
	prewarmHits := provHits
	mu.Unlock()
	if prewarmHits != 2 {
		t.Fatalf("server saw %d pre-warm provisions, want 2", prewarmHits)
	}

	// Warm Provision: served from warm pool -> server provision count UNCHANGED.
	inst, err := p.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("Provision (http warm): %v", err)
	}
	mu.Lock()
	afterWarm := provHits
	mu.Unlock()
	if afterWarm != prewarmHits {
		t.Fatalf("warm Provision hit the server: provisions %d -> %d", prewarmHits, afterWarm)
	}
	if o, _ := p.OriginOf(inst.ID); o != runpod.OriginWarm {
		t.Fatalf("http warm Provision origin = %v, want warm", o)
	}

	// Drain warm pool then Provision -> must hit the server (real cold provision).
	if _, err := p.Provision(context.Background(), spec); err != nil { // serves last warm
		t.Fatalf("Provision drain warm: %v", err)
	}
	if p.WarmCount() != 0 {
		t.Fatalf("warm count = %d, want 0 after draining", p.WarmCount())
	}
	cold, err := p.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("Provision (http cold): %v", err)
	}
	mu.Lock()
	afterCold := provHits
	mu.Unlock()
	if afterCold != prewarmHits+1 {
		t.Fatalf("cold Provision server provisions = %d, want %d", afterCold, prewarmHits+1)
	}
	if o, _ := p.OriginOf(cold.ID); o != runpod.OriginCold {
		t.Fatalf("http cold Provision origin = %v, want cold", o)
	}
	// Endpoint came off the wire, proving real parse (not fabricated).
	if !strings.HasPrefix(cold.ID, "srv-") {
		t.Fatalf("cold worker id = %q, want server-minted srv-*", cold.ID)
	}
}
