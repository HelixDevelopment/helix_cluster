package healthmonitor

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// advRunUUID is a fixed per-suite identifier logged for forensic traceability.
const advRunUUID = "HXC-1501-healthmonitor-adv-7a2e9c41-1f88-4d3a-bc55-90e2a1d6f4b7"

// scriptedProvider is a deterministic HealthCheckable whose CheckHealth result
// is driven by an explicit, mutex-protected error slot the test flips between
// Check calls. It also counts how many times CheckHealth was invoked so a test
// can assert each provider is probed exactly once per Check (no double-probe).
type scriptedProvider struct {
	id    string
	mu    sync.Mutex
	err   error
	probe atomic.Int64
}

func (p *scriptedProvider) ID() string { return p.id }

func (p *scriptedProvider) CheckHealth() error {
	p.probe.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *scriptedProvider) set(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
}

// -----------------------------------------------------------------------------
// CENTERPIECE 1 — TRANSITION CORRECTNESS over an adversarial input sequence.
//
// This drives the FSM through a hand-built, non-trivial sequence that includes:
//   - the initial state (Healthy, before any Check),
//   - repeated identical inputs in BOTH states (idempotent no-ops),
//   - a full flap cycle (Healthy->Unhealthy->Healthy),
//   - and an immediate second flap, to prove the FSM has no sticky state.
//
// At every step it asserts (a) the post-Check state, (b) the exact set of
// events fired THIS Check (kind + provider + tick + Err polarity), and (c) the
// cumulative recorded log. This is the documented transition table:
//
//	(Healthy,   observe-healthy)   -> Healthy,   {}                level no-op
//	(Healthy,   observe-unhealthy) -> Unhealthy, {EventUnhealthy}  rising edge
//	(Unhealthy, observe-unhealthy) -> Unhealthy, {}                level no-op
//	(Unhealthy, observe-healthy)   -> Healthy,   {EventRecovered}  falling edge
//
// Mutations that BITE:
//   - level-trigger (drop the `nowHealthy == prevHealthy { continue }` guard):
//     no-op steps emit events -> wantEvents mismatch FAILS.
//   - inverted edge (swap EventRecovered/EventUnhealthy): wantKind FAILS.
//   - ignore per-Check error: rising edge never fires -> FAILS.
func TestAdversarialTransitionSequenceExact(t *testing.T) {
	t.Logf("run-UUID=%s clause=transition-correctness", advRunUUID)

	failErr := errors.New("xid 79: fell off the bus")

	type step struct {
		name        string
		fail        bool
		wantHealthy bool
		wantKinds   []EventKind // events expected THIS Check, in order
	}

	// Initial state is Healthy (New seeds healthy). The sequence is deliberately
	// adversarial: it dwells, flaps, dwells again, and immediately re-flaps.
	steps := []step{
		{"init-dwell-healthy", false, true, nil},
		{"dwell-healthy-2", false, true, nil},
		{"dwell-healthy-3", false, true, nil},
		{"rising-edge-1", true, false, []EventKind{EventUnhealthy}},
		{"dwell-unhealthy-1", true, false, nil},
		{"dwell-unhealthy-2", true, false, nil},
		{"dwell-unhealthy-3", true, false, nil},
		{"falling-edge-1", false, true, []EventKind{EventRecovered}},
		{"dwell-healthy-after-recover", false, true, nil},
		// Immediate second flap — proves no sticky/one-shot latch.
		{"rising-edge-2", true, false, []EventKind{EventUnhealthy}},
		{"falling-edge-2", false, true, []EventKind{EventRecovered}},
		{"final-dwell-healthy", false, true, nil},
	}

	p := &scriptedProvider{id: "gpu-seq"}
	m := New(p)

	// Initial state BEFORE any Check: seeded healthy, empty log.
	if !m.IsHealthy("gpu-seq") {
		t.Fatalf("initial state must be healthy before any Check")
	}
	if n := len(m.Events()); n != 0 {
		t.Fatalf("initial event log must be empty, got %d", n)
	}

	var wantLog []EventKind
	for i, s := range steps {
		if s.fail {
			p.set(failErr)
		} else {
			p.set(nil)
		}

		fired := m.Check(tick(i))

		if len(fired) != len(s.wantKinds) {
			t.Fatalf("step %d %q: fired %d events, want %d: %+v",
				i, s.name, len(fired), len(s.wantKinds), fired)
		}
		for j, wk := range s.wantKinds {
			if fired[j].Kind != wk {
				t.Fatalf("step %d %q: event[%d] kind=%v want %v", i, s.name, j, fired[j].Kind, wk)
			}
			if fired[j].ProviderID != "gpu-seq" {
				t.Fatalf("step %d %q: event[%d] provider=%q", i, s.name, j, fired[j].ProviderID)
			}
			if !fired[j].At.Equal(tick(i)) {
				t.Fatalf("step %d %q: event[%d] tick=%v want %v", i, s.name, j, fired[j].At, tick(i))
			}
			// Polarity: rising edge carries the error, falling edge is nil.
			switch wk {
			case EventUnhealthy:
				if !errors.Is(fired[j].Err, failErr) {
					t.Fatalf("step %d %q: rising edge must carry failErr, got %v", i, s.name, fired[j].Err)
				}
			case EventRecovered:
				if fired[j].Err != nil {
					t.Fatalf("step %d %q: falling edge must have nil Err, got %v", i, s.name, fired[j].Err)
				}
			}
			wantLog = append(wantLog, wk)
		}

		if got := m.IsHealthy("gpu-seq"); got != s.wantHealthy {
			t.Fatalf("step %d %q: IsHealthy=%v want %v", i, s.name, got, s.wantHealthy)
		}

		// Each provider must be probed exactly once per Check (no double-probe,
		// no skipped probe). probe count must equal i+1 after step i.
		if got := p.probe.Load(); got != int64(i+1) {
			t.Fatalf("step %d %q: provider probed %d times total, want %d (probe-once-per-Check broken)",
				i, s.name, got, i+1)
		}

		// Cumulative log must match exactly what we've driven so far.
		evs := m.Events()
		if len(evs) != len(wantLog) {
			t.Fatalf("step %d %q: log size=%d want %d: %+v", i, s.name, len(evs), len(wantLog), evs)
		}
		for k := range wantLog {
			if evs[k].Kind != wantLog[k] {
				t.Fatalf("step %d %q: log[%d]=%v want %v", i, s.name, k, evs[k].Kind, wantLog[k])
			}
		}
	}

	// Full sequence drove exactly 2 rising + 2 falling edges.
	want := []EventKind{EventUnhealthy, EventRecovered, EventUnhealthy, EventRecovered}
	evs := m.Events()
	if len(evs) != len(want) {
		t.Fatalf("final log size=%d want %d: %+v", len(evs), len(want), evs)
	}
	for k := range want {
		if evs[k].Kind != want[k] {
			t.Fatalf("final log[%d]=%v want %v", k, evs[k].Kind, want[k])
		}
	}
	t.Logf("done: %d steps, log=%v probes=%d (transition table exact)", len(steps), want, p.probe.Load())
}

// -----------------------------------------------------------------------------
// CENTERPIECE 2 — EXACTLY-ONCE EDGES under a long flapping sequence.
//
// Drives N full flap cycles, each with random-length dwell in each state, and
// asserts: exactly one EventUnhealthy + one EventRecovered callback PER cycle,
// never on a dwell. The recorded log is exactly [U,R,U,R,...] of length 2N, and
// the callback counters equal N apiece. A "dropped" or "doubled" edge anywhere
// breaks the running equality immediately.
//
// Mutations that BITE:
//   - re-fire on repeated same-state input -> counts exceed N -> FAIL.
//   - drop an edge (e.g. only update state but skip event on one direction) ->
//     counts below N / log polarity wrong -> FAIL.
func TestExactlyOnceEdgesPerFlapCycle(t *testing.T) {
	t.Logf("run-UUID=%s clause=exactly-once-edges", advRunUUID)

	failErr := errors.New("nvlink timeout")
	p := &scriptedProvider{id: "gpu-flap"}
	m := New(p)

	var unhealthyCB, recoveredCB atomic.Int64
	m.OnUnhealthy(func(e Event) {
		if e.Kind != EventUnhealthy || e.ProviderID != "gpu-flap" {
			t.Errorf("bad unhealthy event: %+v", e)
		}
		unhealthyCB.Add(1)
	})
	m.OnRecovered(func(e Event) {
		if e.Kind != EventRecovered || e.ProviderID != "gpu-flap" {
			t.Errorf("bad recovered event: %+v", e)
		}
		recoveredCB.Add(1)
	})

	const cycles = 25
	// Deterministic pseudo-dwell lengths so the test is reproducible.
	dwell := func(n int) int { return 1 + (n*7+3)%5 } // 1..5

	tickN := 0
	var wantLog []EventKind
	for c := 0; c < cycles; c++ {
		// --- unhealthy dwell: first Check fires EventUnhealthy, rest are no-ops.
		p.set(failErr)
		du := dwell(c)
		for k := 0; k < du; k++ {
			fired := m.Check(tick(tickN))
			tickN++
			wantN := 0
			if k == 0 {
				wantN = 1
			}
			if len(fired) != wantN {
				t.Fatalf("cycle %d unhealthy dwell step %d: fired %d want %d", c, k, len(fired), wantN)
			}
			if k == 0 {
				if fired[0].Kind != EventUnhealthy {
					t.Fatalf("cycle %d: rising edge kind=%v", c, fired[0].Kind)
				}
				wantLog = append(wantLog, EventUnhealthy)
			}
		}
		// --- healthy dwell: first Check fires EventRecovered, rest are no-ops.
		p.set(nil)
		dh := dwell(c + 1)
		for k := 0; k < dh; k++ {
			fired := m.Check(tick(tickN))
			tickN++
			wantN := 0
			if k == 0 {
				wantN = 1
			}
			if len(fired) != wantN {
				t.Fatalf("cycle %d healthy dwell step %d: fired %d want %d", c, k, len(fired), wantN)
			}
			if k == 0 {
				if fired[0].Kind != EventRecovered {
					t.Fatalf("cycle %d: falling edge kind=%v", c, fired[0].Kind)
				}
				wantLog = append(wantLog, EventRecovered)
			}
		}

		// Running invariant: after c+1 complete cycles, exactly c+1 of each edge.
		if got := unhealthyCB.Load(); got != int64(c+1) {
			t.Fatalf("after cycle %d: unhealthy callbacks=%d want %d (doubled/dropped edge)", c, got, c+1)
		}
		if got := recoveredCB.Load(); got != int64(c+1) {
			t.Fatalf("after cycle %d: recovered callbacks=%d want %d (doubled/dropped edge)", c, got, c+1)
		}
	}

	if int(unhealthyCB.Load()) != cycles || int(recoveredCB.Load()) != cycles {
		t.Fatalf("final callbacks unhealthy=%d recovered=%d want %d each",
			unhealthyCB.Load(), recoveredCB.Load(), cycles)
	}
	evs := m.Events()
	if len(evs) != 2*cycles {
		t.Fatalf("log size=%d want %d", len(evs), 2*cycles)
	}
	for k := range wantLog {
		if evs[k].Kind != wantLog[k] {
			t.Fatalf("log[%d]=%v want %v", k, evs[k].Kind, wantLog[k])
		}
	}
	t.Logf("done: %d flap cycles -> %d events, unhealthyCB=%d recoveredCB=%d (exactly once per crossing)",
		cycles, len(evs), unhealthyCB.Load(), recoveredCB.Load())
}

// -----------------------------------------------------------------------------
// CENTERPIECE 3 — CONCURRENCY: Check() hammered concurrently with every query
// accessor under -race, asserting (a) no race / no panic, (b) per-provider
// exactly-once accounting holds even when many goroutines drive Check at once.
//
// To make the exactly-once guarantee CHECKABLE under concurrency, each provider
// here only ever transitions ONCE healthy->unhealthy (latching to failed). No
// matter how many goroutines race on Check, a latched provider can produce at
// most one EventUnhealthy and zero EventRecovered. We assert the per-id failover
// count is exactly 1 — a doubled edge (e.g. edge-detect outside the lock) would
// push some id's count to 2 and FAIL even if the race detector were silent.
//
// Mutations that BITE:
//   - remove the lock from any accessor or read m.healthy in Check outside the
//     lock -> -race reports a data race -> FAIL.
//   - detect/record the edge outside the lock -> two concurrent Checks both see
//     prevHealthy==true and both fire -> per-id failover count hits 2 -> FAIL.
func TestConcurrentCheckExactlyOnceLatched(t *testing.T) {
	t.Logf("run-UUID=%s clause=concurrency-exactly-once", advRunUUID)

	const nProviders = 12
	providers := make([]HealthCheckable, 0, nProviders)
	latched := make([]*latchProvider, 0, nProviders)
	ids := make([]string, 0, nProviders)
	for i := 0; i < nProviders; i++ {
		lp := &latchProvider{id: fmt.Sprintf("gpu-%d", i)}
		providers = append(providers, lp)
		latched = append(latched, lp)
		ids = append(ids, lp.id)
	}
	m := New(providers...)

	var failoverByID sync.Map // id -> *atomic.Int64
	var recoverHits atomic.Int64
	for _, id := range ids {
		var c atomic.Int64
		failoverByID.Store(id, &c)
	}
	m.OnUnhealthy(func(e Event) {
		if v, ok := failoverByID.Load(e.ProviderID); ok {
			v.(*atomic.Int64).Add(1)
		}
	})
	m.OnRecovered(func(Event) { recoverHits.Add(1) })

	const (
		writers    = 8
		readers    = 8
		iterations = 500
	)

	var wg sync.WaitGroup
	start := make(chan struct{})

	// At a fixed iteration, every writer latches every provider to failed. All
	// writers concurrently race on Check while the providers flip — yet each
	// provider must edge exactly once.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if i == iterations/2 {
					for _, lp := range latched {
						lp.fail.Store(true)
					}
				}
				m.Check(tick(i))
			}
		}()
	}

	// Readers hammer every guarded accessor concurrently with the writers.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				_ = m.IsHealthy(ids[(seed+i)%nProviders])
				_ = m.Events()
				if i%50 == 0 {
					m.OnRecovered(func(Event) { recoverHits.Add(1) })
				}
			}
		}(r)
	}

	close(start)
	wg.Wait()

	// Every provider was latched to failed, so each must read unhealthy now and
	// must have fired EXACTLY ONE failover — never two, despite concurrent Check.
	for _, id := range ids {
		if m.IsHealthy(id) {
			t.Fatalf("provider %s latched failed but reports healthy", id)
		}
		v, _ := failoverByID.Load(id)
		if got := v.(*atomic.Int64).Load(); got != 1 {
			t.Fatalf("provider %s failover count=%d, want exactly 1 (doubled/dropped edge under concurrency)", id, got)
		}
	}
	// Latched providers never recover, so zero recovery callbacks expected.
	if got := recoverHits.Load(); got != 0 {
		t.Fatalf("no provider recovered, but recover callbacks=%d", got)
	}
	// The recorded log must contain exactly nProviders unhealthy events.
	evs := m.Events()
	if len(evs) != nProviders {
		t.Fatalf("log size=%d, want %d (one unhealthy per provider)", len(evs), nProviders)
	}
	for _, e := range evs {
		if e.Kind != EventUnhealthy {
			t.Fatalf("unexpected event kind in log: %+v", e)
		}
	}
	t.Logf("done: writers=%d readers=%d iters=%d providers=%d -> each fired exactly 1 failover, log=%d (no race, exactly once)",
		writers, readers, iterations, nProviders, len(evs))

	_ = time.Second
}

// latchProvider transitions healthy->unhealthy at most once: once fail is set it
// stays failed. This makes the concurrent exactly-once edge count assertable.
type latchProvider struct {
	id   string
	fail atomic.Bool
}

func (p *latchProvider) ID() string { return p.id }
func (p *latchProvider) CheckHealth() error {
	if p.fail.Load() {
		return errors.New("latched-failed")
	}
	return nil
}

// -----------------------------------------------------------------------------
// EDGE — empty monitor (no providers) and unknown-id queries must not panic and
// must report the documented defaults.
func TestEdgeEmptyMonitorAndUnknownID(t *testing.T) {
	t.Logf("run-UUID=%s clause=edge-empty-and-unknown", advRunUUID)

	m := New() // no providers
	if fired := m.Check(time.Time{}); len(fired) != 0 {
		t.Fatalf("empty monitor Check must fire nothing, got %+v", fired)
	}
	if m.IsHealthy("does-not-exist") {
		t.Fatalf("unknown id must report unhealthy(false)")
	}
	if n := len(m.Events()); n != 0 {
		t.Fatalf("empty monitor must have empty log, got %d", n)
	}
	// Zero-value tick must not panic and must still record At correctly when an
	// edge fires.
	p := &scriptedProvider{id: "z", err: errors.New("boom")}
	m2 := New(p)
	fired := m2.Check(time.Time{})
	if len(fired) != 1 || !fired[0].At.IsZero() {
		t.Fatalf("zero-tick edge must record zero At, got %+v", fired)
	}
	t.Logf("done: empty monitor + unknown id + zero tick all safe")
}
