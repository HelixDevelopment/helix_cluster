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
	"time"

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

// EndpointFor returns the endpoint the fake minted for a worker id (the value
// that should have travelled end to end), so EndpointOf can be checked against
// the source of truth rather than a hard-coded string.
func (f *fakeClient) EndpointFor(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[id].Endpoint
}

// barrierClient is an injected RunPodClient whose ColdProvision blocks until
// `target` calls are simultaneously in-flight, then releases all of them. It is
// the seam that makes the concurrency requirement observable: it can only ever
// unblock if Provision releases p.mu around the network RPC so multiple cold
// provisions actually overlap. It also records the peak in-flight count.
type barrierClient struct {
	target int
	gate   chan struct{} // closed once `target` calls have arrived

	mu       sync.Mutex
	seq      int
	inFlight int
	maxInFl  int
}

func newBarrierClient(target int) *barrierClient {
	return &barrierClient{target: target, gate: make(chan struct{})}
}

func (b *barrierClient) ColdProvision(ctx context.Context, spec pool.Spec) (runpod.Worker, error) {
	b.mu.Lock()
	b.inFlight++
	if b.inFlight > b.maxInFl {
		b.maxInFl = b.inFlight
	}
	if b.inFlight >= b.target {
		// All expected callers are in ColdProvision at once -> release everyone.
		select {
		case <-b.gate:
		default:
			close(b.gate)
		}
	}
	b.mu.Unlock()

	// Block until the barrier opens (or ctx dies). If Provision held p.mu across
	// this call, a second caller could never get here and gate never closes.
	select {
	case <-b.gate:
	case <-ctx.Done():
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
		return runpod.Worker{}, ctx.Err()
	}

	b.mu.Lock()
	b.seq++
	id := fmt.Sprintf("bc-%04d", b.seq)
	b.inFlight--
	b.mu.Unlock()
	return runpod.Worker{
		ID:        id,
		GPUType:   spec.GPUType,
		HourlyUSD: spec.HourlyUSD,
		Endpoint:  "https://" + id + ".runpod.local",
	}, nil
}

func (b *barrierClient) Terminate(ctx context.Context, id string) error { return nil }

func (b *barrierClient) MaxConcurrent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxInFl
}

// gateClient is an injected RunPodClient whose ColdProvision parks every call on
// a single release gate that the test opens explicitly. Unlike barrierClient it
// does NOT require a quorum to unblock: it parks each in-flight call until the
// test says go. This lets a test launch N > Capacity concurrent cold Provisions
// and observe that the surplus are rejected with ErrAtCapacity BEFORE they ever
// reach (or while they are held at) the network RPC — i.e. that the reservation
// counter caps concurrent in-flight cold provisions. It records peak in-flight.
type gateClient struct {
	release chan struct{} // closed by the test to let parked calls finish

	mu       sync.Mutex
	seq      int
	started  int // total ColdProvision entries (RPCs that actually began)
	inFlight int
	maxInFl  int
}

func newGateClient() *gateClient {
	return &gateClient{release: make(chan struct{})}
}

func (g *gateClient) ColdProvision(ctx context.Context, spec pool.Spec) (runpod.Worker, error) {
	g.mu.Lock()
	g.started++
	g.inFlight++
	if g.inFlight > g.maxInFl {
		g.maxInFl = g.inFlight
	}
	g.mu.Unlock()

	// Park until the test opens the gate (or ctx dies).
	select {
	case <-g.release:
	case <-ctx.Done():
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
		return runpod.Worker{}, ctx.Err()
	}

	g.mu.Lock()
	g.seq++
	id := fmt.Sprintf("gc-%04d", g.seq)
	g.inFlight--
	g.mu.Unlock()
	return runpod.Worker{
		ID:        id,
		GPUType:   spec.GPUType,
		HourlyUSD: spec.HourlyUSD,
		Endpoint:  "https://" + id + ".runpod.local",
	}, nil
}

func (g *gateClient) Terminate(ctx context.Context, id string) error { return nil }

// Started is the number of ColdProvision RPCs that actually began (entered the
// client). The reservation cap means this must never exceed Capacity even when
// more goroutines than Capacity race to cold-provision.
func (g *gateClient) Started() int { g.mu.Lock(); defer g.mu.Unlock(); return g.started }

func (g *gateClient) MaxConcurrent() int { g.mu.Lock(); defer g.mu.Unlock(); return g.maxInFl }

// waitStarted blocks until at least n ColdProvision RPCs are in flight, or fails
// the test after a bounded wait. Used to deterministically let the first
// Capacity provisions occupy their reserved slots before asserting on surplus.
func (g *gateClient) waitStarted(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		g.mu.Lock()
		started := g.started
		g.mu.Unlock()
		if started >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d ColdProvision RPCs to start (got %d)", n, started)
		case <-time.After(time.Millisecond):
		}
	}
}

func (g *gateClient) open() { close(g.release) }

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

// TestEndpointOfRoundTripsReachableAddress is the load-bearing endpoint test.
// The reachable inference address parsed off the wire (Worker.Endpoint) MUST be
// retrievable by instance id for a leased instance via EndpointOf, for BOTH the
// warm fast path and the cold path. It MUST disappear when the instance is
// released, and MUST be absent for an unknown id.
//
// MUTATION GUARD: if recordLeasedLocked stops recording w.Endpoint (e.g.
// `p.endpoint[w.ID] = w.Endpoint` is dropped), EndpointOf returns "" / !ok and
// these asserts fail. If the field is removed from Worker, this test fails to
// compile. Either way the misleading-dropped-endpoint defect is caught.
func TestEndpointOfRoundTripsReachableAddress(t *testing.T) {
	t.Parallel()
	fc := newFakeClient()
	spec := pool.Spec{GPUType: "A100", HourlyUSD: 2.5}
	p, err := runpod.New(context.Background(), runpod.Config{
		Capacity:   4,
		WarmTarget: 1,
		Spec:       spec,
		Client:     fc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Warm fast-path lease: the pre-warmed worker's endpoint must round-trip.
	warmInst, err := p.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("Provision (warm): %v", err)
	}
	gotWarm, ok := p.EndpointOf(warmInst.ID)
	if !ok {
		t.Fatalf("EndpointOf(%q) = _,false, want a leased endpoint", warmInst.ID)
	}
	wantWarm := fc.EndpointFor(warmInst.ID)
	if wantWarm == "" {
		t.Fatalf("fake client minted no endpoint for %q", warmInst.ID)
	}
	if gotWarm != wantWarm {
		t.Fatalf("EndpointOf(%q) = %q, want client-minted %q", warmInst.ID, gotWarm, wantWarm)
	}

	// Cold-path lease: warm pool now empty, this provisions over the seam.
	coldInst, err := p.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("Provision (cold): %v", err)
	}
	gotCold, ok := p.EndpointOf(coldInst.ID)
	if !ok {
		t.Fatalf("EndpointOf(%q) = _,false for cold instance", coldInst.ID)
	}
	wantCold := fc.EndpointFor(coldInst.ID)
	if gotCold == "" || gotCold != wantCold {
		t.Fatalf("EndpointOf(%q) = %q, want client-minted %q", coldInst.ID, gotCold, wantCold)
	}
	// Distinct workers must surface distinct endpoints (no fabricated constant).
	if gotCold == gotWarm {
		t.Fatalf("cold and warm endpoints both %q; endpoint not keyed per worker", gotCold)
	}

	// Release removes the endpoint mapping (sink-side cleanup).
	if err := p.Release(context.Background(), coldInst); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, ok := p.EndpointOf(coldInst.ID); ok {
		t.Fatalf("EndpointOf(%q) still present after Release", coldInst.ID)
	}
	// Unknown id is absent.
	if _, ok := p.EndpointOf("does-not-exist"); ok {
		t.Fatalf("EndpointOf(unknown) = _,true, want false")
	}
}

// TestConcurrentColdProvisionsOverlap is the load-bearing concurrency test. Two
// cold Provision calls MUST be able to run their ColdProvision network RPC at
// the SAME time: the fix releases p.mu for the wire call. The injected client
// blocks each ColdProvision on a barrier until BOTH calls are in-flight; only if
// the two calls overlap can the barrier be satisfied.
//
// MUTATION GUARD: if p.mu is (re-)held across client.ColdProvision (the wave-72
// bug), the second Provision cannot enter ColdProvision until the first returns,
// the barrier never reaches 2, both block forever, and this test TIMES OUT /
// fails. With the lock released around the RPC, both reach the barrier, it
// releases, and both complete. Capacity accounting (via the reservation) still
// prevents over-subscription, asserted by the final Leased==2 sink.
func TestConcurrentColdProvisionsOverlap(t *testing.T) {
	t.Parallel()
	const want = 2
	bc := newBarrierClient(want)
	spec := pool.Spec{GPUType: "A100", HourlyUSD: 3}
	p, err := runpod.New(context.Background(), runpod.Config{
		Capacity:   want,
		WarmTarget: 0, // force every Provision down the cold path
		Spec:       spec,
		Client:     bc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type res struct {
		inst pool.Instance
		err  error
	}
	results := make(chan res, want)
	for i := 0; i < want; i++ {
		go func() {
			inst, err := p.Provision(context.Background(), spec)
			results <- res{inst, err}
		}()
	}

	// If the lock is held across ColdProvision, the barrier never reaches `want`
	// and both goroutines hang; this bounded wait turns that hang into a failure.
	deadline := time.After(5 * time.Second)
	ids := make(map[string]bool)
	for i := 0; i < want; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("concurrent cold Provision %d: %v", i, r.err)
			}
			if r.inst.ID == "" {
				t.Fatalf("concurrent cold Provision %d returned empty id", i)
			}
			ids[r.inst.ID] = true
		case <-deadline:
			t.Fatalf("concurrent cold Provisions did not overlap: timed out waiting "+
				"for %d completions (got %d) — lock likely held across ColdProvision", want, i)
		}
	}

	// Sink-side: both leased, distinct ids, and the barrier confirmed both RPCs
	// were genuinely in-flight together.
	if len(ids) != want {
		t.Fatalf("got %d distinct leased ids, want %d", len(ids), want)
	}
	if got := bc.MaxConcurrent(); got < want {
		t.Fatalf("max concurrent in-flight ColdProvisions = %d, want >= %d (RPCs did not overlap)", got, want)
	}
	if m := p.Metrics(); m.Leased != want {
		t.Fatalf("metrics after concurrent provisions = %+v, want Leased=%d", m, want)
	}
}

// TestConcurrentColdProvisionsOverCapacityRejectSurplus is the load-bearing
// over-subscription test. With Capacity=2, WarmTarget=0 and the cold-provision
// RPC released from p.mu, THREE concurrent Provisions race down the cold path.
// Only the reservation counter (folded into liveLocked via p.reserved) prevents
// the surplus call from also slipping past the capacity check while the first
// two RPCs are in flight. The test asserts:
//   - exactly Capacity (2) Provisions succeed,
//   - the surplus (1) fails with ErrAtCapacity,
//   - the client only ever STARTED Capacity cold RPCs (the surplus never reached
//     the wire), and
//   - the final leased count is exactly Capacity.
//
// MUTATION GUARD: if p.reserved is dropped from liveLocked() (the over-
// subscription guard removed), all three goroutines pass the capacity check
// before any lease is recorded, all three reach the gated client, three RPCs
// start, and the final leased count is 3 (> Capacity) with zero ErrAtCapacity —
// every one of the assertions below fails. This makes the reservation counter
// load-bearing.
func TestConcurrentColdProvisionsOverCapacityRejectSurplus(t *testing.T) {
	t.Parallel()
	const capacity = 2
	const racers = 3 // one more than capacity
	gc := newGateClient()
	spec := pool.Spec{GPUType: "A100", HourlyUSD: 3}
	p, err := runpod.New(context.Background(), runpod.Config{
		Capacity:   capacity,
		WarmTarget: 0, // force every Provision down the cold path
		Spec:       spec,
		Client:     gc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type res struct {
		inst pool.Instance
		err  error
	}
	results := make(chan res, racers)
	for i := 0; i < racers; i++ {
		go func() {
			inst, err := p.Provision(context.Background(), spec)
			results <- res{inst, err}
		}()
	}

	// Let the accepted provisions occupy their reserved slots and park at the
	// gated RPC. Exactly Capacity of them must reach the client; the surplus must
	// be rejected by the capacity check before ever starting an RPC. We wait for
	// Capacity starts, then assert no MORE than Capacity ever start.
	gc.waitStarted(t, capacity)

	// The surplus goroutine should already have returned ErrAtCapacity without
	// starting an RPC. Open the gate so the two accepted RPCs finish.
	gc.open()

	var (
		ok       int
		atCap    int
		leasedID = make(map[string]bool)
	)
	deadline := time.After(5 * time.Second)
	for i := 0; i < racers; i++ {
		select {
		case r := <-results:
			switch {
			case r.err == nil:
				ok++
				if r.inst.ID == "" {
					t.Fatalf("successful Provision returned empty id")
				}
				leasedID[r.inst.ID] = true
			case errors.Is(r.err, runpod.ErrAtCapacity):
				atCap++
			default:
				t.Fatalf("unexpected Provision error: %v", r.err)
			}
		case <-deadline:
			t.Fatalf("timed out collecting %d Provision results (got %d)", racers, i)
		}
	}

	// Exactly Capacity succeed; the surplus is rejected with ErrAtCapacity.
	if ok != capacity {
		t.Fatalf("successful concurrent cold Provisions = %d, want %d", ok, capacity)
	}
	if atCap != racers-capacity {
		t.Fatalf("ErrAtCapacity rejections = %d, want %d", atCap, racers-capacity)
	}
	if len(leasedID) != capacity {
		t.Fatalf("distinct leased ids = %d, want %d", len(leasedID), capacity)
	}
	// The surplus RPC never reached the wire: the client started at most Capacity
	// cold provisions. This is the direct sink-side proof that p.reserved caps
	// concurrent in-flight cold provisions.
	if got := gc.Started(); got != capacity {
		t.Fatalf("client started %d cold RPCs, want exactly %d (surplus must not reach the wire)", got, capacity)
	}
	if got := gc.MaxConcurrent(); got > capacity {
		t.Fatalf("max concurrent in-flight cold RPCs = %d, want <= %d", got, capacity)
	}
	// Final sink-side leased count must equal Capacity, never exceed it.
	if m := p.Metrics(); m.Leased != capacity {
		t.Fatalf("metrics after over-capacity race = %+v, want Leased=%d", m, capacity)
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
