package healthmonitor

import (
	"errors"
	"testing"
	"time"
)

// runUUID is a fixed per-suite identifier logged for forensic traceability.
const runUUID = "HXC-1501-healthmonitor-9f2c1a7e-0d44-4f51-bb3a-7e1f2c6d8a01"

// fakeProvider is a deterministic in-memory HealthCheckable whose health is
// flipped explicitly by the test (no clock, no randomness in PASS/FAIL).
type fakeProvider struct {
	id  string
	err error // nil => healthy; non-nil => unhealthy
}

func (f *fakeProvider) ID() string { return f.id }

func (f *fakeProvider) CheckHealth() error { return f.err }

func tick(i int) time.Time {
	// Deterministic, monotonically increasing tick derived only from i.
	base := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(i) * DefaultInterval)
}

// Closure clause 1: a provider toggled unhealthy mid-run is detected exactly
// once at the toggle (event logged with provider+error, failover callback
// fired); toggling back healthy yields exactly one "recovered" event +
// OnRecovered.
//
// Mutation: if the detector were level-triggered (fire every Check while in a
// state) this test FAILS because failoverCount/recoverCount would exceed 1.
// Mutation: if CheckHealth's error were ignored (all treated healthy), no
// unhealthy event/callback fires and this test FAILS.
func TestUnhealthyAndRecoveryDetectedExactlyOnce(t *testing.T) {
	t.Logf("run-UUID=%s clause=1 (exactly-once edge transition)", runUUID)

	wantErr := errors.New("ECC uncorrectable error on GPU0")
	p := &fakeProvider{id: "gpu-0", err: nil}
	m := New(p)

	var failoverCount, recoverCount int
	var lastFailoverEvent, lastRecoverEvent Event
	m.OnUnhealthy(func(e Event) { failoverCount++; lastFailoverEvent = e })
	m.OnRecovered(func(e Event) { recoverCount++; lastRecoverEvent = e })

	t.Logf("before: IsHealthy(gpu-0)=%v failover=%d recover=%d events=%d",
		m.IsHealthy("gpu-0"), failoverCount, recoverCount, len(m.Events()))

	// Two healthy checks: no transitions expected.
	m.Check(tick(0))
	m.Check(tick(1))
	if failoverCount != 0 || recoverCount != 0 {
		t.Fatalf("healthy ticks must not fire callbacks: failover=%d recover=%d", failoverCount, recoverCount)
	}

	// Toggle unhealthy mid-run, then check several times while unhealthy.
	p.err = wantErr
	m.Check(tick(2)) // <- transition healthy->unhealthy fires here
	m.Check(tick(3)) // still unhealthy: NO new event (edge-trigger)
	m.Check(tick(4)) // still unhealthy: NO new event

	if failoverCount != 1 {
		t.Fatalf("expected exactly 1 failover callback, got %d (level-triggered?)", failoverCount)
	}
	if lastFailoverEvent.Kind != EventUnhealthy {
		t.Fatalf("expected EventUnhealthy, got %v", lastFailoverEvent.Kind)
	}
	if lastFailoverEvent.ProviderID != "gpu-0" {
		t.Fatalf("expected provider gpu-0, got %q", lastFailoverEvent.ProviderID)
	}
	if !errors.Is(lastFailoverEvent.Err, wantErr) {
		t.Fatalf("unhealthy event must carry CheckHealth error; got %v", lastFailoverEvent.Err)
	}
	if lastFailoverEvent.Message != "GPU device went unhealthy" {
		t.Fatalf("unexpected unhealthy message: %q", lastFailoverEvent.Message)
	}
	if !lastFailoverEvent.At.Equal(tick(2)) {
		t.Fatalf("unhealthy event must record toggle tick %v, got %v", tick(2), lastFailoverEvent.At)
	}
	if m.IsHealthy("gpu-0") {
		t.Fatalf("provider must be marked unhealthy after toggle")
	}

	// Toggle back healthy; check several times while healthy.
	p.err = nil
	m.Check(tick(5)) // <- transition unhealthy->healthy fires here
	m.Check(tick(6)) // still healthy: NO new event
	m.Check(tick(7)) // still healthy: NO new event

	if recoverCount != 1 {
		t.Fatalf("expected exactly 1 recovery callback, got %d (level-triggered?)", recoverCount)
	}
	if lastRecoverEvent.Kind != EventRecovered {
		t.Fatalf("expected EventRecovered, got %v", lastRecoverEvent.Kind)
	}
	if lastRecoverEvent.ProviderID != "gpu-0" {
		t.Fatalf("recovery for wrong provider: %q", lastRecoverEvent.ProviderID)
	}
	if lastRecoverEvent.Err != nil {
		t.Fatalf("recovery event must have nil Err, got %v", lastRecoverEvent.Err)
	}
	if !m.IsHealthy("gpu-0") {
		t.Fatalf("provider must be marked healthy after recovery")
	}

	// Exactly two events recorded overall: one unhealthy, one recovered.
	evs := m.Events()
	if len(evs) != 2 {
		t.Fatalf("expected exactly 2 transition events, got %d: %+v", len(evs), evs)
	}
	if evs[0].Kind != EventUnhealthy || evs[1].Kind != EventRecovered {
		t.Fatalf("event order wrong: %v then %v", evs[0].Kind, evs[1].Kind)
	}

	t.Logf("after: IsHealthy(gpu-0)=%v failover=%d recover=%d events=%d unhealthyErr=%q",
		m.IsHealthy("gpu-0"), failoverCount, recoverCount, len(evs), wantErr)
}

// Closure clause 2: a provider that stays healthy across many checks produces
// ZERO transition events and ZERO callbacks (proves edge-trigger, not
// level-trigger on the healthy state).
//
// Mutation: a level-triggered detector (or one seeding state as unhealthy)
// would emit events/callbacks here and FAIL the zero assertions.
func TestNoSpuriousEventsWhenAlwaysHealthy(t *testing.T) {
	t.Logf("run-UUID=%s clause=2 (no spurious events)", runUUID)

	p := &fakeProvider{id: "gpu-stable", err: nil}
	m := New(p)

	var failoverCount, recoverCount int
	m.OnUnhealthy(func(Event) { failoverCount++ })
	m.OnRecovered(func(Event) { recoverCount++ })

	t.Logf("before: events=%d failover=%d recover=%d", len(m.Events()), failoverCount, recoverCount)

	const checks = 50
	for i := 0; i < checks; i++ {
		if got := m.Check(tick(i)); len(got) != 0 {
			t.Fatalf("check %d on always-healthy provider produced events: %+v", i, got)
		}
	}

	if failoverCount != 0 || recoverCount != 0 {
		t.Fatalf("always-healthy provider must fire zero callbacks: failover=%d recover=%d", failoverCount, recoverCount)
	}
	if evs := m.Events(); len(evs) != 0 {
		t.Fatalf("always-healthy provider must record zero events, got %d: %+v", len(evs), evs)
	}
	if !m.IsHealthy("gpu-stable") {
		t.Fatalf("stable provider must remain healthy")
	}

	t.Logf("after: %d checks, events=%d failover=%d recover=%d (all zero as required)",
		checks, len(m.Events()), failoverCount, recoverCount)
}

// Closure clause 3: with multiple providers, one going unhealthy does not mark
// the others; only the failing provider triggers failover.
//
// Mutation: if Check ignored per-provider identity (marked all unhealthy on any
// failure) the healthy providers' IsHealthy would flip and failover events for
// the wrong ids would appear, FAILING this test.
func TestMultipleProvidersOnlyFailingTriggers(t *testing.T) {
	t.Logf("run-UUID=%s clause=3 (per-provider isolation)", runUUID)

	failErr := errors.New("thermal shutdown")
	pa := &fakeProvider{id: "gpu-a", err: nil}
	pb := &fakeProvider{id: "gpu-b", err: nil}
	pc := &fakeProvider{id: "gpu-c", err: nil}
	m := New(pa, pb, pc)

	failoverIDs := map[string]int{}
	m.OnUnhealthy(func(e Event) { failoverIDs[e.ProviderID]++ })

	t.Logf("before: a=%v b=%v c=%v", m.IsHealthy("gpu-a"), m.IsHealthy("gpu-b"), m.IsHealthy("gpu-c"))

	// Only gpu-b fails.
	pb.err = failErr
	fired := m.Check(tick(0))

	if len(fired) != 1 {
		t.Fatalf("exactly one provider should transition, got %d: %+v", len(fired), fired)
	}
	if fired[0].ProviderID != "gpu-b" {
		t.Fatalf("wrong provider failed over: %q", fired[0].ProviderID)
	}
	if failoverIDs["gpu-b"] != 1 {
		t.Fatalf("gpu-b should have exactly one failover, got %d", failoverIDs["gpu-b"])
	}
	if failoverIDs["gpu-a"] != 0 || failoverIDs["gpu-c"] != 0 {
		t.Fatalf("healthy providers must not fail over: a=%d c=%d", failoverIDs["gpu-a"], failoverIDs["gpu-c"])
	}

	if m.IsHealthy("gpu-b") {
		t.Fatalf("gpu-b must be unhealthy")
	}
	if !m.IsHealthy("gpu-a") || !m.IsHealthy("gpu-c") {
		t.Fatalf("gpu-a and gpu-c must remain healthy: a=%v c=%v", m.IsHealthy("gpu-a"), m.IsHealthy("gpu-c"))
	}

	t.Logf("after: a=%v b=%v c=%v failoverIDs=%v",
		m.IsHealthy("gpu-a"), m.IsHealthy("gpu-b"), m.IsHealthy("gpu-c"), failoverIDs)
}
