package healthmonitor

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fsmRunUUID is a fixed per-suite identifier logged for forensic traceability.
const fsmRunUUID = "HXC-1501-healthmonitor-fsm-3b7d9e21-6c10-4a55-9f8e-2d4b1a0c7e93"

// togglingProvider is a deterministic HealthCheckable whose health is driven by
// an explicit error slot the test flips between Check calls. No clock, no
// randomness: the FSM input sequence is fully controlled by the test.
type togglingProvider struct {
	id  string
	err error
}

func (p *togglingProvider) ID() string         { return p.id }
func (p *togglingProvider) CheckHealth() error { return p.err }

// TestFSMExhaustiveTransitionMatrix drives the 2-state edge-triggered FSM
// (Healthy <-> Unhealthy) through EVERY (prev-state, observed-status) input
// combination and asserts the exact transition table, including that the two
// "same-state" inputs are no-ops (edge, not level).
//
// Transition table under test (state x input -> {newState, eventEmitted}):
//
//	(Healthy,   observe-healthy)   -> Healthy,   no event   (level no-op)
//	(Healthy,   observe-unhealthy) -> Unhealthy, EventUnhealthy (rising edge)
//	(Unhealthy, observe-unhealthy) -> Unhealthy, no event   (level no-op)
//	(Unhealthy, observe-healthy)   -> Healthy,   EventRecovered (falling edge)
//
// Anti-bluff / mutations that BITE:
//   - Make it level-triggered (delete the `nowHealthy == prevHealthy { continue }`
//     guard, or invert it): the two same-state rows would emit spurious events ->
//     `wantEvent==false` assertions FAIL.
//   - Off-by-one / inverted edge (swap EventRecovered/EventUnhealthy, or fire on
//     the wrong direction): the rising/falling rows assert the exact EventKind ->
//     FAIL.
//   - Ignore the per-Check error (treat all healthy): the rising-edge row never
//     transitions -> FAIL.
func TestFSMExhaustiveTransitionMatrix(t *testing.T) {
	t.Logf("run-UUID=%s clause=fsm-matrix (exhaustive 2-state edge table)", fsmRunUUID)

	failErr := errors.New("ECC uncorrectable")

	// Each step: the observed status for this Check, plus the expected post-Check
	// state and whether exactly one transition event of a given kind must fire.
	type step struct {
		name        string
		observeFail bool // true => CheckHealth returns failErr this Check
		wantHealthy bool // expected IsHealthy after the Check
		wantEvent   bool // true => exactly one event must fire this Check
		wantKind    EventKind
	}

	// The sequence visits all four (state,input) combinations. Starting state is
	// Healthy (New seeds healthy).
	steps := []step{
		// (Healthy, observe-healthy) -> Healthy, no event.
		{"healthy/observe-healthy(no-op)", false, true, false, 0},
		{"healthy/observe-healthy(no-op,repeat)", false, true, false, 0},
		// (Healthy, observe-unhealthy) -> Unhealthy, rising edge.
		{"healthy/observe-unhealthy(rising-edge)", true, false, true, EventUnhealthy},
		// (Unhealthy, observe-unhealthy) -> Unhealthy, no event.
		{"unhealthy/observe-unhealthy(no-op)", true, false, false, 0},
		{"unhealthy/observe-unhealthy(no-op,repeat)", true, false, false, 0},
		// (Unhealthy, observe-healthy) -> Healthy, falling edge.
		{"unhealthy/observe-healthy(falling-edge)", false, true, true, EventRecovered},
		// Back to (Healthy, observe-healthy) -> Healthy, no event.
		{"healthy/observe-healthy(after-recovery,no-op)", false, true, false, 0},
	}

	p := &togglingProvider{id: "gpu-fsm", err: nil}
	m := New(p)

	var unhealthyCB, recoveredCB int
	m.OnUnhealthy(func(Event) { unhealthyCB++ })
	m.OnRecovered(func(Event) { recoveredCB++ })

	wantUnhealthyCB, wantRecoveredCB := 0, 0
	totalEvents := 0

	for i, s := range steps {
		if s.observeFail {
			p.err = failErr
		} else {
			p.err = nil
		}

		fired := m.Check(tick(i))

		// Edge assertion: exactly the expected number of events this Check.
		wantN := 0
		if s.wantEvent {
			wantN = 1
		}
		if len(fired) != wantN {
			t.Fatalf("step %d %q: want %d event(s), got %d: %+v", i, s.name, wantN, len(fired), fired)
		}
		if s.wantEvent {
			if fired[0].Kind != s.wantKind {
				t.Fatalf("step %d %q: want kind %v, got %v", i, s.name, s.wantKind, fired[0].Kind)
			}
			if fired[0].ProviderID != "gpu-fsm" {
				t.Fatalf("step %d %q: wrong provider %q", i, s.name, fired[0].ProviderID)
			}
			if !fired[0].At.Equal(tick(i)) {
				t.Fatalf("step %d %q: event tick %v != %v", i, s.name, fired[0].At, tick(i))
			}
			// Rising edge must carry the error; falling edge must be nil.
			if s.wantKind == EventUnhealthy && !errors.Is(fired[0].Err, failErr) {
				t.Fatalf("step %d %q: rising edge must carry failErr, got %v", i, s.name, fired[0].Err)
			}
			if s.wantKind == EventRecovered && fired[0].Err != nil {
				t.Fatalf("step %d %q: falling edge must have nil Err, got %v", i, s.name, fired[0].Err)
			}
			totalEvents++
			if s.wantKind == EventUnhealthy {
				wantUnhealthyCB++
			} else {
				wantRecoveredCB++
			}
		}

		// State assertion: post-Check IsHealthy matches the FSM table.
		if got := m.IsHealthy("gpu-fsm"); got != s.wantHealthy {
			t.Fatalf("step %d %q: IsHealthy=%v, want %v", i, s.name, got, s.wantHealthy)
		}

		// Callback counts must track the cumulative expected edges exactly
		// (proves callbacks are edge-driven, not level-driven).
		if unhealthyCB != wantUnhealthyCB || recoveredCB != wantRecoveredCB {
			t.Fatalf("step %d %q: callbacks unhealthy=%d/%d recovered=%d/%d (level-triggered?)",
				i, s.name, unhealthyCB, wantUnhealthyCB, recoveredCB, wantRecoveredCB)
		}

		t.Logf("step %d %q: healthy=%v firedThisCheck=%d cumUnhealthyCB=%d cumRecoveredCB=%d",
			i, s.name, m.IsHealthy("gpu-fsm"), len(fired), unhealthyCB, recoveredCB)
	}

	// The recorded log must equal exactly the edges we drove, in order.
	evs := m.Events()
	if len(evs) != totalEvents {
		t.Fatalf("event log size=%d, want %d: %+v", len(evs), totalEvents, evs)
	}
	if len(evs) != 2 || evs[0].Kind != EventUnhealthy || evs[1].Kind != EventRecovered {
		t.Fatalf("event log order wrong, want [Unhealthy, Recovered], got %+v", evs)
	}
	t.Logf("done: totalEvents=%d unhealthyCB=%d recoveredCB=%d (matrix exhausted)",
		totalEvents, unhealthyCB, recoveredCB)
}

// TestFSMRepeatedSameStateNeverRefires hammers the level-vs-edge boundary
// directly: a long run of identical-status observations after a single edge
// must produce EXACTLY one event regardless of run length.
//
// Mutation that BITES: making the detector level-triggered re-fires on every
// Check while in-state -> the count explodes past 1 and the assertions FAIL.
func TestFSMRepeatedSameStateNeverRefires(t *testing.T) {
	t.Logf("run-UUID=%s clause=edge-no-refire", fsmRunUUID)

	failErr := errors.New("link down")
	p := &togglingProvider{id: "gpu-edge", err: nil}
	m := New(p)

	var cb int
	m.OnUnhealthy(func(Event) { cb++ })

	// One rising edge, then 200 identical unhealthy observations.
	p.err = failErr
	const dwell = 200
	for i := 0; i < dwell; i++ {
		fired := m.Check(tick(i))
		switch i {
		case 0:
			if len(fired) != 1 {
				t.Fatalf("first unhealthy Check must fire exactly 1 event, got %d", len(fired))
			}
		default:
			if len(fired) != 0 {
				t.Fatalf("Check %d on unchanged-unhealthy must fire 0 events (edge), got %d", i, len(fired))
			}
		}
	}
	if cb != 1 {
		t.Fatalf("exactly 1 unhealthy callback across %d identical observations, got %d (level-triggered?)", dwell, cb)
	}
	if evs := m.Events(); len(evs) != 1 {
		t.Fatalf("exactly 1 recorded event, got %d", len(evs))
	}
	t.Logf("done: %d identical unhealthy observations -> callbacks=%d events=%d (single edge)", dwell, cb, len(m.Events()))
}

// flakyProvider serves health from an atomically-updated slot so a writer
// goroutine can flip it concurrently with reader goroutines — exercising the
// real mutex-guarded state under -race.
type flakyProvider struct {
	id   string
	fail atomic.Bool
}

func (p *flakyProvider) ID() string { return p.id }
func (p *flakyProvider) CheckHealth() error {
	if p.fail.Load() {
		return errors.New("flaky")
	}
	return nil
}

// TestConcurrentCheckAndQueriesRace runs many concurrent goroutines that
// WRITE the guarded map/log/callbacks via Check while other goroutines READ it
// via IsHealthy / Events / OnUnhealthy. Under `go test -race` this deterministically
// flags any access to m.healthy / m.events / m.onUnhealthy that is NOT performed
// under m.mu.
//
// Anti-bluff / partial-lock mutation that BITES: drop the lock in ANY accessor
// (e.g. remove m.mu.Lock()/Unlock() from IsHealthy or Events, or read m.healthy
// in Check outside the lock) and the race detector reports a data race on the
// concurrent map read/write -> this test FAILS under -race. With every accessor
// correctly locked, it passes cleanly (proving the lock is load-bearing).
func TestConcurrentCheckAndQueriesRace(t *testing.T) {
	t.Logf("run-UUID=%s clause=concurrency-race", fsmRunUUID)

	const nProviders = 8
	providers := make([]HealthCheckable, 0, nProviders)
	flaky := make([]*flakyProvider, 0, nProviders)
	ids := make([]string, 0, nProviders)
	for i := 0; i < nProviders; i++ {
		fp := &flakyProvider{id: fmt.Sprintf("gpu-%d", i)}
		providers = append(providers, fp)
		flaky = append(flaky, fp)
		ids = append(ids, fp.id)
	}
	m := New(providers...)

	var failoverHits, recoverHits atomic.Int64
	m.OnUnhealthy(func(Event) { failoverHits.Add(1) })
	m.OnRecovered(func(Event) { recoverHits.Add(1) })

	const (
		writers    = 6
		readers    = 8
		iterations = 400
	)

	var wg sync.WaitGroup
	start := make(chan struct{})

	// Writers: flip provider health and drive Check (writes guarded state).
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				// Deterministically toggle a subset of providers each iteration.
				idx := (seed + i) % nProviders
				flaky[idx].fail.Store((i & 1) == 0)
				m.Check(tick(i))
			}
		}(w)
	}

	// Readers: hammer every guarded accessor concurrently with the writers.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				_ = m.IsHealthy(ids[(seed+i)%nProviders])
				_ = m.Events()
				// Re-register a callback to exercise the OnUnhealthy lock path
				// concurrently with Check reading m.onUnhealthy.
				if i%64 == 0 {
					m.OnUnhealthy(func(Event) { failoverHits.Add(1) })
				}
			}
		}(r)
	}

	close(start)
	wg.Wait()

	// Sanity: the monitor is still consistent and queryable after the storm.
	for _, id := range ids {
		_ = m.IsHealthy(id)
	}
	t.Logf("done: writers=%d readers=%d iters=%d failoverHits=%d recoverHits=%d events=%d (no race => locks load-bearing)",
		writers, readers, iterations, failoverHits.Load(), recoverHits.Load(), len(m.Events()))

	// Avoid an unused warning if time import is otherwise trimmed.
	_ = time.Second
}
