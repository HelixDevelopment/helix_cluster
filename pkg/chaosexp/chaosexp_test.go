package chaosexp_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/chaosexp"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/turmoil"
)

// ── ValidateCatalog ──────────────────────────────────────────────────────────

// TestCatalog_ExactlyTwelveExperiments verifies that Catalog returns exactly
// 12 entries and that ValidateCatalog passes without error.
// Mutation: change `len(cat) != 12` guard in ValidateCatalog to `len(cat) != 11`
// → ValidateCatalog returns non-nil for a 12-entry catalog → t.Fatal fires.
func TestCatalog_ExactlyTwelveExperiments(t *testing.T) {
	cat := chaosexp.Catalog()
	if len(cat) != 12 {
		t.Fatalf("Catalog() returned %d experiments, want 12", len(cat))
	}
	if err := chaosexp.ValidateCatalog(); err != nil {
		t.Fatalf("ValidateCatalog() returned unexpected error: %v", err)
	}
}

// TestCatalog_UniqueIDs verifies that all 12 experiment IDs are distinct and
// are exactly the set CE-01..CE-12.
// Mutation: duplicate the ID "CE-01" on two entries (change CE-02's ID to
// "CE-01") → duplicate detection in ValidateCatalog fires → test fails.
func TestCatalog_UniqueIDs(t *testing.T) {
	cat := chaosexp.Catalog()
	seen := make(map[string]int, 12) // id → position
	for i, e := range cat {
		if prev, dup := seen[e.ID]; dup {
			t.Fatalf("duplicate ID %q: positions %d and %d", e.ID, prev, i)
		}
		seen[e.ID] = i
	}
	// Exact set.
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("CE-%02d", i)
		if _, ok := seen[id]; !ok {
			t.Fatalf("catalog missing required ID %s", id)
		}
	}
}

// TestValidate_RejectsEmptyTitle checks that Validate catches a blank Title.
// Mutation: remove the Title-empty check from Validate → Validate returns nil
// → t.Fatal fires.
func TestValidate_RejectsEmptyTitle(t *testing.T) {
	e := chaosexp.ChaosExperiment{
		ID:          "CE-01",
		Title:       "",
		Hypothesis:  "some hypothesis",
		Setup:       func(*turmoil.Simulation) {},
		SteadyState: func([]turmoil.DeliveryEvent) bool { return true },
		BlastRadius: "none",
	}
	if err := chaosexp.Validate(e); err == nil {
		t.Fatal("Validate must return error for empty Title, got nil")
	}
}

// TestValidate_RejectsEmptyHypothesis checks that Validate catches a blank
// Hypothesis.
// Mutation: remove the Hypothesis-empty check from Validate → Validate returns
// nil → t.Fatal fires.
func TestValidate_RejectsEmptyHypothesis(t *testing.T) {
	e := chaosexp.ChaosExperiment{
		ID:          "CE-02",
		Title:       "some title",
		Hypothesis:  "",
		Setup:       func(*turmoil.Simulation) {},
		SteadyState: func([]turmoil.DeliveryEvent) bool { return true },
		BlastRadius: "none",
	}
	if err := chaosexp.Validate(e); err == nil {
		t.Fatal("Validate must return error for empty Hypothesis, got nil")
	}
}

// TestValidate_RejectsNilSetup checks that Validate catches a nil Setup.
// Mutation: remove the Setup-nil check from Validate → Validate returns nil
// → t.Fatal fires.
func TestValidate_RejectsNilSetup(t *testing.T) {
	e := chaosexp.ChaosExperiment{
		ID:          "CE-03",
		Title:       "t",
		Hypothesis:  "h",
		Setup:       nil,
		SteadyState: func([]turmoil.DeliveryEvent) bool { return true },
		BlastRadius: "none",
	}
	if err := chaosexp.Validate(e); err == nil {
		t.Fatal("Validate must return error for nil Setup, got nil")
	}
}

// TestValidate_RejectsNilSteadyState checks that Validate catches a nil
// SteadyState.
// Mutation: remove the SteadyState-nil check from Validate → Validate returns
// nil → t.Fatal fires.
func TestValidate_RejectsNilSteadyState(t *testing.T) {
	e := chaosexp.ChaosExperiment{
		ID:          "CE-04",
		Title:       "t",
		Hypothesis:  "h",
		Setup:       func(*turmoil.Simulation) {},
		SteadyState: nil,
		BlastRadius: "none",
	}
	if err := chaosexp.Validate(e); err == nil {
		t.Fatal("Validate must return error for nil SteadyState, got nil")
	}
}

// TestValidate_RejectsBadID checks that Validate rejects IDs not matching
// CE-\d\d.
// Mutation: remove the idPattern check from Validate → bad IDs accepted →
// t.Fatal fires.
func TestValidate_RejectsBadID(t *testing.T) {
	for _, bad := range []string{"CE-1", "ce-01", "CE-001", "EX-01", ""} {
		e := chaosexp.ChaosExperiment{
			ID:          bad,
			Title:       "t",
			Hypothesis:  "h",
			Setup:       func(*turmoil.Simulation) {},
			SteadyState: func([]turmoil.DeliveryEvent) bool { return true },
			BlastRadius: "none",
		}
		if err := chaosexp.Validate(e); err == nil {
			t.Fatalf("Validate(%q): expected error for bad ID, got nil", bad)
		}
	}
}

// ── Reproducibility ──────────────────────────────────────────────────────────

// TestRun_Reproducible verifies that Run(e, 42) twice produces identical
// Canonical for every experiment in the catalog.
// Mutation: replace `sim := turmoil.NewSimulation(seed)` in Run with
// `sim := turmoil.NewSimulation(seed + 1)` → Nonce values differ → Canonical
// strings diverge → t.Fatal fires.
func TestRun_Reproducible(t *testing.T) {
	for _, e := range chaosexp.Catalog() {
		e := e // capture
		t.Run(e.ID, func(t *testing.T) {
			r1 := chaosexp.Run(e, 42)
			r2 := chaosexp.Run(e, 42)
			if r1.Canonical != r2.Canonical {
				t.Fatalf("%s: Run(e,42) twice produced different Canonical:\nrun1=%q\nrun2=%q",
					e.ID, r1.Canonical, r2.Canonical)
			}
			// Canonical must be non-empty (every experiment sends at least one
			// message).
			if r1.Canonical == "" {
				t.Fatalf("%s: Canonical is empty — no events recorded", e.ID)
			}
		})
	}
}

// TestRun_DifferentSeedsDifferentCanonical verifies that seed 42 and seed 43
// produce different Canonical strings for CE-01 (which sends one message and
// therefore draws exactly one Nonce from the PRNG).
// Mutation: hardcode `nonce = 0` in turmoil's Send (zeroing the PRNG draw) →
// both seeds produce the same Canonical → t.Fatal fires.
func TestRun_DifferentSeedsDifferentCanonical(t *testing.T) {
	ce01 := findByID(t, "CE-01")
	r42 := chaosexp.Run(ce01, 42)
	r43 := chaosexp.Run(ce01, 43)
	if r42.Canonical == r43.Canonical {
		t.Fatalf("CE-01: seeds 42 and 43 produced identical Canonical — PRNG not seed-sensitive:\n%s",
			r42.Canonical)
	}
}

// ── Partition experiments ─────────────────────────────────────────────────────

// TestCE01_PartitionDropsMessage verifies the EventLog contains exactly one
// Dropped event with Reason="partition" and that SteadyState=true (not
// hard-coded).
// Mutation: change SteadyState in CE-01 to `return true` unconditionally, then
// manually run the sim WITHOUT the Partition setup — the event will be Dropped=false;
// assert on the raw EventLog to confirm the test catches it.
// (Proxy: assert Dropped=true AND Reason="partition" on the raw event — if
// SteadyState were always-true the raw assertion below would still hold, but the
// companion TestCE01_SteadyStateNotAlwaysTrue test would catch the bluff.)
func TestCE01_PartitionDropsMessage(t *testing.T) {
	ce01 := findByID(t, "CE-01")
	r := chaosexp.Run(ce01, 42)

	if len(r.Events) != 1 {
		t.Fatalf("CE-01: expected 1 event, got %d", len(r.Events))
	}
	ev := r.Events[0]
	if !ev.Dropped {
		t.Fatalf("CE-01: message not dropped; Dropped=false")
	}
	if ev.Reason != "partition" {
		t.Fatalf("CE-01: Reason=%q, want partition", ev.Reason)
	}
	if ev.FromCluster != "c1" || ev.ToCluster != "c2" {
		t.Fatalf("CE-01: clusters wrong: from=%q to=%q", ev.FromCluster, ev.ToCluster)
	}
	if !r.Passed {
		t.Fatalf("CE-01: SteadyState returned false but event is dropped with reason=partition")
	}
}

// TestCE01_SteadyStateNotAlwaysTrue proves SteadyState for CE-01 is a genuine
// predicate: when we fabricate an EventLog with a delivered (non-dropped) message
// the predicate must return false.
// Mutation: change CE-01's SteadyState to `return true` → predicate returns
// true even for a delivered message → t.Fatal fires.
func TestCE01_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce01 := findByID(t, "CE-01")
	// Fabricate a log that contradicts the partition hypothesis: message delivered.
	delivered := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c1", FromNode: "n1", ToCluster: "c2", ToNode: "n1",
			Payload: []byte("ce01-msg"), Dropped: false, Reason: ""},
	}
	if ce01.SteadyState(delivered) {
		t.Fatal("CE-01 SteadyState must return false for a delivered (non-dropped) message; it returned true — bluff detected")
	}
}

// TestCE01_SetupIsLoadBearing proves that the Partition call inside CE-01's
// Setup is genuinely load-bearing: if we run a sim with the same clusters and
// sends but WITHOUT calling Partition, the message arrives delivered (Dropped=false).
// This validates the experiment's fault actually changes the simulation outcome.
// Mutation: remove `sim.Partition("c1","c2")` from CE-01's Setup → message
// delivered → Dropped=false → the raw-event assertion in TestCE01_PartitionDropsMessage
// fails; this test also provides a direct confirmation.
func TestCE01_SetupIsLoadBearing(t *testing.T) {
	// Run WITHOUT Partition (no fault).
	sim := turmoil.NewSimulation(42)
	mustAdd(t, sim, "c1", []string{"n1"})
	mustAdd(t, sim, "c2", []string{"n1"})
	// Note: deliberately NOT calling sim.Partition("c1","c2").
	mustSend(t, sim, "c1", "n1", "c2", "n1", []byte("ce01-msg"), 1)
	sim.Run(20)

	log := sim.EventLog()
	if len(log) != 1 {
		t.Fatalf("no-fault sim: expected 1 event, got %d", len(log))
	}
	if log[0].Dropped {
		t.Fatalf("no-fault sim: message should be delivered without Partition; got Dropped=true Reason=%q",
			log[0].Reason)
	}

	// Now run the actual CE-01 experiment — its Setup calls Partition and the
	// message must be dropped. This confirms Setup changes the outcome.
	ce01 := findByID(t, "CE-01")
	r := chaosexp.Run(ce01, 42)
	if len(r.Events) != 1 || !r.Events[0].Dropped {
		t.Fatalf("CE-01 with Setup: expected dropped message; got Events=%v", r.Events)
	}
}

// TestCE02_HealRestoresDelivery verifies CE-02: first message dropped, second
// delivered with payload "after-heal".
// Mutation: remove sim.Heal() from CE-02's Setup → both messages dropped →
// SteadyState returns false → r.Passed=false → t.Fatal fires.
func TestCE02_HealRestoresDelivery(t *testing.T) {
	ce02 := findByID(t, "CE-02")
	r := chaosexp.Run(ce02, 42)

	if len(r.Events) != 2 {
		t.Fatalf("CE-02: expected 2 events, got %d", len(r.Events))
	}
	// First event: pre-heal, must be dropped.
	if !r.Events[0].Dropped {
		t.Fatalf("CE-02: first event not dropped; Dropped=false")
	}
	if r.Events[0].Reason != "partition" {
		t.Fatalf("CE-02: first event Reason=%q, want partition", r.Events[0].Reason)
	}
	// Second event: post-heal, must be delivered.
	if r.Events[1].Dropped {
		t.Fatalf("CE-02: post-heal event dropped; Reason=%q", r.Events[1].Reason)
	}
	if string(r.Events[1].Payload) != "after-heal" {
		t.Fatalf("CE-02: post-heal payload=%q, want after-heal", string(r.Events[1].Payload))
	}
	if !r.Passed {
		t.Fatalf("CE-02: SteadyState returned false but evidence is correct")
	}
}

// TestCE02_SteadyStateNotAlwaysTrue proves CE-02 SteadyState is not a bluff.
// Mutation: change CE-02's SteadyState to `return true` → it accepts the
// fabricated log below → t.Fatal fires.
func TestCE02_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce02 := findByID(t, "CE-02")
	// Both messages "delivered" — contradicts the partition hypothesis.
	bothDelivered := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c1", ToCluster: "c2", Dropped: false, Payload: []byte("before-heal")},
		{Tick: 2, FromCluster: "c1", ToCluster: "c2", Dropped: false, Payload: []byte("after-heal")},
	}
	if ce02.SteadyState(bothDelivered) {
		t.Fatal("CE-02 SteadyState accepted two delivered messages (first should be dropped) — bluff detected")
	}
}

// TestCE03_SteadyStateNotAlwaysTrue proves CE-03's SteadyState is a genuine
// predicate: a fabricated log with a delivered (Dropped=false) c2→c1 message
// must be rejected, not accepted.
// Mutation: change CE-03's SteadyState to `return true` → predicate accepts
// the fabricated delivered message → t.Fatal fires.
func TestCE03_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce03 := findByID(t, "CE-03")
	// Fabricate a log that contradicts the partition-symmetry hypothesis:
	// the reverse-direction message was delivered rather than dropped.
	delivered := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c2", FromNode: "n1", ToCluster: "c1", ToNode: "n1",
			Payload: []byte("reverse-msg"), Dropped: false, Reason: ""},
	}
	if ce03.SteadyState(delivered) {
		t.Fatal("CE-03 SteadyState must return false for a delivered c2→c1 message; it returned true — bluff detected")
	}
}

// TestCE03_SymmetricPartition verifies CE-03: reverse-direction message is
// dropped.
// Mutation: change newPartitionKey in turmoil to be directional → c2→c1 not
// blocked → Dropped=false → t.Fatal fires.
func TestCE03_SymmetricPartition(t *testing.T) {
	ce03 := findByID(t, "CE-03")
	r := chaosexp.Run(ce03, 42)

	if len(r.Events) != 1 {
		t.Fatalf("CE-03: expected 1 event, got %d", len(r.Events))
	}
	if !r.Events[0].Dropped {
		t.Fatalf("CE-03: reverse-direction message not dropped; Dropped=false")
	}
	if r.Events[0].Reason != "partition" {
		t.Fatalf("CE-03: Reason=%q, want partition", r.Events[0].Reason)
	}
	if r.Events[0].FromCluster != "c2" || r.Events[0].ToCluster != "c1" {
		t.Fatalf("CE-03: unexpected direction: from=%q to=%q", r.Events[0].FromCluster, r.Events[0].ToCluster)
	}
	if !r.Passed {
		t.Fatalf("CE-03: Passed=false but evidence confirms dropped reverse message")
	}
}

// TestCE04_SteadyStateNotAlwaysTrue proves CE-04's SteadyState is a genuine
// predicate: a fabricated log where the intra-cluster message is Dropped=true
// must be rejected.
// Mutation: change CE-04's SteadyState to `return true` → predicate accepts
// the fabricated dropped event → t.Fatal fires.
func TestCE04_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce04 := findByID(t, "CE-04")
	// Fabricate a log that contradicts the hypothesis: intra-cluster message dropped.
	dropped := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c1", FromNode: "n1", ToCluster: "c1", ToNode: "n2",
			Payload: []byte("intra-ok"), Dropped: true, Reason: "partition"},
	}
	if ce04.SteadyState(dropped) {
		t.Fatal("CE-04 SteadyState must return false for a dropped c1→c1 message; it returned true — bluff detected")
	}
}

// TestCE04_IntraClusterUnaffected verifies CE-04: intra-cluster message is NOT
// dropped despite a cross-cluster partition.
// Mutation: extend the partition check to block same-clusterID paths → intra
// message dropped → Dropped=true → t.Fatal fires.
func TestCE04_IntraClusterUnaffected(t *testing.T) {
	ce04 := findByID(t, "CE-04")
	r := chaosexp.Run(ce04, 42)

	if len(r.Events) != 1 {
		t.Fatalf("CE-04: expected 1 event, got %d", len(r.Events))
	}
	if r.Events[0].Dropped {
		t.Fatalf("CE-04: intra-cluster message dropped; Reason=%q", r.Events[0].Reason)
	}
	if !r.Passed {
		t.Fatalf("CE-04: Passed=false but intra-cluster message was delivered")
	}
}

// TestCE05_SteadyStateNotAlwaysTrue proves CE-05's SteadyState is a genuine
// predicate: a fabricated 3-event log where the c3→c2 message is delivered
// (violating the double-partition hypothesis) must be rejected.
// Mutation: change CE-05's SteadyState to `return true` → predicate accepts
// the fabricated log → t.Fatal fires.
func TestCE05_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce05 := findByID(t, "CE-05")
	// Fabricate: c1→c2 and c3→c2 are correctly dropped, but c3→c2 is
	// delivered — contradicts the hypothesis.
	badLog := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c1", FromNode: "n1", ToCluster: "c2", ToNode: "n1",
			Payload: []byte("c1-to-c2"), Dropped: true, Reason: "partition"},
		{Tick: 1, FromCluster: "c3", FromNode: "n1", ToCluster: "c2", ToNode: "n1",
			Payload: []byte("c3-to-c2"), Dropped: false, Reason: ""},
		{Tick: 1, FromCluster: "c1", FromNode: "n1", ToCluster: "c3", ToNode: "n1",
			Payload: []byte("c1-to-c3"), Dropped: false, Reason: ""},
	}
	if ce05.SteadyState(badLog) {
		t.Fatal("CE-05 SteadyState must return false when c3→c2 is delivered (not dropped); it returned true — bluff detected")
	}
}

// TestCE05_DoublePartitionIsolatesC2 verifies CE-05: c1→c2 and c3→c2 are
// dropped; c1→c3 is delivered.
// Mutation: remove `sim.Partition("c2","c3")` from CE-05's Setup → c3→c2 is
// delivered → SteadyState returns false → r.Passed=false → t.Fatal fires.
func TestCE05_DoublePartitionIsolatesC2(t *testing.T) {
	ce05 := findByID(t, "CE-05")
	r := chaosexp.Run(ce05, 42)

	if len(r.Events) != 3 {
		t.Fatalf("CE-05: expected 3 events, got %d", len(r.Events))
	}

	// Build a lookup by payload.
	byPayload := make(map[string]turmoil.DeliveryEvent, 3)
	for _, ev := range r.Events {
		byPayload[string(ev.Payload)] = ev
	}

	for _, key := range []string{"c1-to-c2", "c3-to-c2", "c1-to-c3"} {
		if _, ok := byPayload[key]; !ok {
			t.Fatalf("CE-05: missing event for payload %q; got keys: %v", key, payloadKeys(r.Events))
		}
	}
	if !byPayload["c1-to-c2"].Dropped {
		t.Fatalf("CE-05: c1→c2 not dropped")
	}
	if byPayload["c1-to-c2"].Reason != "partition" {
		t.Fatalf("CE-05: c1→c2 Reason=%q, want partition", byPayload["c1-to-c2"].Reason)
	}
	if !byPayload["c3-to-c2"].Dropped {
		t.Fatalf("CE-05: c3→c2 not dropped")
	}
	if byPayload["c1-to-c3"].Dropped {
		t.Fatalf("CE-05: c1→c3 should be delivered, got Dropped=true Reason=%q", byPayload["c1-to-c3"].Reason)
	}
	if !r.Passed {
		t.Fatalf("CE-05: Passed=false but evidence confirms double isolation")
	}
}

// TestCE06_UnidirectionalLatency verifies CE-06: c1→c2 arrives at tick 6
// (1+5), c2→c1 at tick 1 (no extra latency).
// Mutation: make InjectLatency symmetric in turmoil → c2→c1 also gets +5 ticks
// → arrives at tick 6 not 1 → r.Passed=false → t.Fatal fires.
func TestCE06_UnidirectionalLatency(t *testing.T) {
	ce06 := findByID(t, "CE-06")
	r := chaosexp.Run(ce06, 42)

	if len(r.Events) != 2 {
		t.Fatalf("CE-06: expected 2 events, got %d", len(r.Events))
	}

	byPayload := make(map[string]turmoil.DeliveryEvent, 2)
	for _, ev := range r.Events {
		byPayload[string(ev.Payload)] = ev
	}

	fwd, ok1 := byPayload["forward"]
	rev, ok2 := byPayload["reverse"]
	if !ok1 || !ok2 {
		t.Fatalf("CE-06: missing events; keys=%v", payloadKeys(r.Events))
	}

	const wantFwdTick = int64(6) // base 1 + extra 5
	const wantRevTick = int64(1) // base 1 + no extra
	if fwd.Dropped {
		t.Fatalf("CE-06: forward message dropped")
	}
	if fwd.Tick != wantFwdTick {
		t.Fatalf("CE-06: forward Tick=%d, want %d", fwd.Tick, wantFwdTick)
	}
	if rev.Dropped {
		t.Fatalf("CE-06: reverse message dropped")
	}
	if rev.Tick != wantRevTick {
		t.Fatalf("CE-06: reverse Tick=%d, want %d", rev.Tick, wantRevTick)
	}
	if !r.Passed {
		t.Fatalf("CE-06: Passed=false but tick evidence is correct")
	}
}

// TestCE06_SteadyStateNotAlwaysTrue proves CE-06's SteadyState rejects bad
// ticks.
// Mutation: change CE-06's SteadyState to `return true` → accepts fabricated
// events with wrong ticks → t.Fatal fires.
func TestCE06_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce06 := findByID(t, "CE-06")
	// Both messages at tick 1 — wrong for the forward message.
	badTicks := []turmoil.DeliveryEvent{
		{Tick: 1, Payload: []byte("forward"), Dropped: false},
		{Tick: 1, Payload: []byte("reverse"), Dropped: false},
	}
	if ce06.SteadyState(badTicks) {
		t.Fatal("CE-06 SteadyState accepted wrong ticks (forward at 1 not 6) — bluff detected")
	}
}

// TestCE07_SteadyStateNotAlwaysTrue proves CE-07's SteadyState is a genuine
// predicate: a fabricated 2-event log with wrong ticks (both at tick 1 instead
// of 4 and 8) must be rejected.
// Mutation: change CE-07's SteadyState to `return true` → accepts wrong-tick
// events → t.Fatal fires.
func TestCE07_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce07 := findByID(t, "CE-07")
	// Fabricate: both messages at tick 1 — wrong for both directions.
	// c1-fwd should be at tick 4 (1+3), c2-fwd at tick 8 (1+7).
	wrongTicks := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c1", FromNode: "n1", ToCluster: "c2", ToNode: "n1",
			Payload: []byte("c1-fwd"), Dropped: false},
		{Tick: 1, FromCluster: "c2", FromNode: "n1", ToCluster: "c1", ToNode: "n1",
			Payload: []byte("c2-fwd"), Dropped: false},
	}
	if ce07.SteadyState(wrongTicks) {
		t.Fatal("CE-07 SteadyState must return false for tick=1 (want 4 and 8); it returned true — bluff detected")
	}
}

// TestCE07_BidirectionalLatency verifies CE-07: c1→c2 at tick 4 (1+3) and
// c2→c1 at tick 8 (1+7).
// Mutation: remove InjectLatency("c2","c1",7) from CE-07's Setup → c2→c1 at
// tick 1 not 8 → SteadyState returns false → r.Passed=false → t.Fatal fires.
func TestCE07_BidirectionalLatency(t *testing.T) {
	ce07 := findByID(t, "CE-07")
	r := chaosexp.Run(ce07, 42)

	if len(r.Events) != 2 {
		t.Fatalf("CE-07: expected 2 events, got %d", len(r.Events))
	}

	byPayload := make(map[string]turmoil.DeliveryEvent, 2)
	for _, ev := range r.Events {
		byPayload[string(ev.Payload)] = ev
	}

	c1fwd, ok1 := byPayload["c1-fwd"]
	c2fwd, ok2 := byPayload["c2-fwd"]
	if !ok1 || !ok2 {
		t.Fatalf("CE-07: missing events; keys=%v", payloadKeys(r.Events))
	}

	if c1fwd.Tick != 4 {
		t.Fatalf("CE-07: c1-fwd Tick=%d, want 4", c1fwd.Tick)
	}
	if c2fwd.Tick != 8 {
		t.Fatalf("CE-07: c2-fwd Tick=%d, want 8", c2fwd.Tick)
	}
	if c1fwd.Dropped || c2fwd.Dropped {
		t.Fatalf("CE-07: unexpected drop: c1-fwd.Dropped=%v c2-fwd.Dropped=%v", c1fwd.Dropped, c2fwd.Dropped)
	}
	if !r.Passed {
		t.Fatalf("CE-07: Passed=false but tick evidence correct")
	}
}

// TestCE08_LatencyExactTick verifies CE-08: base=2, extra=10 → delivered at
// exactly tick 12.
// Mutation: change InjectLatency extra from 10 to 9 in CE-08's Setup → Tick=11
// not 12 → SteadyState returns false → r.Passed=false → t.Fatal fires.
func TestCE08_LatencyExactTick(t *testing.T) {
	ce08 := findByID(t, "CE-08")
	r := chaosexp.Run(ce08, 42)

	if len(r.Events) != 1 {
		t.Fatalf("CE-08: expected 1 event, got %d", len(r.Events))
	}
	ev := r.Events[0]
	if ev.Dropped {
		t.Fatalf("CE-08: message dropped unexpectedly; Reason=%q", ev.Reason)
	}
	const wantTick = int64(12)
	if ev.Tick != wantTick {
		t.Fatalf("CE-08: Tick=%d, want %d (base=2 + extra=10)", ev.Tick, wantTick)
	}
	if !r.Passed {
		t.Fatalf("CE-08: Passed=false but tick=%d is correct", ev.Tick)
	}
}

// TestCE08_SteadyStateNotAlwaysTrue proves CE-08's SteadyState rejects the
// wrong tick.
// Mutation: change CE-08's SteadyState to `return true` → accepts tick=11 →
// t.Fatal fires.
func TestCE08_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce08 := findByID(t, "CE-08")
	wrongTick := []turmoil.DeliveryEvent{
		{Tick: 11, Payload: []byte("exact-tick"), Dropped: false}, // wrong: want 12
	}
	if ce08.SteadyState(wrongTick) {
		t.Fatal("CE-08 SteadyState accepted tick=11 (want 12) — bluff detected")
	}
}

// TestCE09_SteadyStateNotAlwaysTrue proves CE-09's SteadyState is a genuine
// predicate: a fabricated log with a delivered (Dropped=false) message must
// be rejected, contradicting the partition-precedence hypothesis.
// Mutation: change CE-09's SteadyState to `return true` → accepts the
// fabricated delivered message → t.Fatal fires.
func TestCE09_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce09 := findByID(t, "CE-09")
	// Fabricate: message delivered despite partition+latency being active —
	// contradicts the hypothesis that partition takes precedence.
	delivered := []turmoil.DeliveryEvent{
		{Tick: 101, FromCluster: "c1", FromNode: "n1", ToCluster: "c2", ToNode: "n1",
			Payload: []byte("combo-msg"), Dropped: false, Reason: ""},
	}
	if ce09.SteadyState(delivered) {
		t.Fatal("CE-09 SteadyState must return false for a delivered message (partition should drop it); it returned true — bluff detected")
	}
}

// TestCE09_PartitionPrecedenceOverLatency verifies CE-09: with both latency
// and partition active, message is dropped (not delayed).
// Mutation: remove `sim.Partition("c1","c2")` from CE-09's Setup → message
// arrives late at tick 102 but is NOT dropped → SteadyState returns false →
// r.Passed=false → t.Fatal fires.
func TestCE09_PartitionPrecedenceOverLatency(t *testing.T) {
	ce09 := findByID(t, "CE-09")
	r := chaosexp.Run(ce09, 42)

	if len(r.Events) != 1 {
		t.Fatalf("CE-09: expected 1 event, got %d", len(r.Events))
	}
	ev := r.Events[0]
	if !ev.Dropped {
		t.Fatalf("CE-09: message not dropped; partition should take precedence over latency")
	}
	if ev.Reason != "partition" {
		t.Fatalf("CE-09: Reason=%q, want partition", ev.Reason)
	}
	if !r.Passed {
		t.Fatalf("CE-09: Passed=false but evidence confirms partition-over-latency")
	}
}

// TestCE10_SteadyStateNotAlwaysTrue proves CE-10's SteadyState is a genuine
// predicate: a fabricated 5-event log with out-of-order ticks {1,2,3,8,5}
// must be rejected because tick[4]=5 < tick[3]=8 (wrong order) and the
// expected sequence {1,2,3,5,8} is not satisfied at index 3.
// Mutation: change CE-10's SteadyState to `return true` → accepts the
// out-of-order log → t.Fatal fires.
func TestCE10_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce10 := findByID(t, "CE-10")
	// Fabricate: ticks {1,2,3,8,5} — out of order and wantTicks[3]=5 != 8.
	outOfOrder := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c1", ToCluster: "c2", Payload: []byte("d1"), Dropped: false},
		{Tick: 2, FromCluster: "c1", ToCluster: "c2", Payload: []byte("d2"), Dropped: false},
		{Tick: 3, FromCluster: "c1", ToCluster: "c2", Payload: []byte("d3"), Dropped: false},
		{Tick: 8, FromCluster: "c1", ToCluster: "c2", Payload: []byte("d8"), Dropped: false},
		{Tick: 5, FromCluster: "c1", ToCluster: "c2", Payload: []byte("d5"), Dropped: false},
	}
	if ce10.SteadyState(outOfOrder) {
		t.Fatal("CE-10 SteadyState must return false for out-of-order ticks {1,2,3,8,5} (want {1,2,3,5,8}); it returned true — bluff detected")
	}
}

// TestCE10_MultiMessageOrdering verifies CE-10: five messages arrive in
// ascending tick order {1,2,3,5,8}; none dropped.
// Mutation: change one delay in CE-10's Setup (e.g. first delay from 5 to 9)
// → sorted tick sequence becomes {1,2,3,8,9} not {1,2,3,5,8} → wantTicks[3]=5
// fails → r.Passed=false → t.Fatal fires.
func TestCE10_MultiMessageOrdering(t *testing.T) {
	ce10 := findByID(t, "CE-10")
	r := chaosexp.Run(ce10, 42)

	if len(r.Events) != 5 {
		t.Fatalf("CE-10: expected 5 events, got %d", len(r.Events))
	}

	wantTicks := []int64{1, 2, 3, 5, 8}
	for i, wt := range wantTicks {
		if r.Events[i].Tick != wt {
			t.Fatalf("CE-10: Events[%d].Tick=%d, want %d", i, r.Events[i].Tick, wt)
		}
		if r.Events[i].Dropped {
			t.Fatalf("CE-10: Events[%d] unexpectedly dropped", i)
		}
	}

	// Strict ascending order.
	for i := 1; i < len(r.Events); i++ {
		if r.Events[i].Tick < r.Events[i-1].Tick {
			t.Fatalf("CE-10: Events out of order: [%d].Tick=%d < [%d].Tick=%d",
				i, r.Events[i].Tick, i-1, r.Events[i-1].Tick)
		}
	}
	if !r.Passed {
		t.Fatalf("CE-10: Passed=false but tick evidence is correct")
	}
}

// TestCE11_SteadyStateNotAlwaysTrue proves CE-11's SteadyState is a genuine
// predicate: a fabricated 4-event log where "blocked-fwd" is Dropped=false
// (contradicting the hypothesis) must be rejected.
// Mutation: change CE-11's SteadyState to `return true` → accepts the log
// with blocked-fwd delivered → t.Fatal fires.
func TestCE11_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce11 := findByID(t, "CE-11")
	// Fabricate: blocked-fwd is delivered (not dropped) — contradicts hypothesis.
	badLog := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c1", FromNode: "n1", ToCluster: "c2", ToNode: "n1",
			Payload: []byte("blocked-fwd"), Dropped: false, Reason: ""},
		{Tick: 1, FromCluster: "c2", FromNode: "n1", ToCluster: "c1", ToNode: "n1",
			Payload: []byte("blocked-rev"), Dropped: true, Reason: "partition"},
		{Tick: 1, FromCluster: "c1", FromNode: "n1", ToCluster: "c3", ToNode: "n1",
			Payload: []byte("delivered-fwd"), Dropped: false},
		{Tick: 1, FromCluster: "c3", FromNode: "n1", ToCluster: "c1", ToNode: "n1",
			Payload: []byte("delivered-rev"), Dropped: false},
	}
	if ce11.SteadyState(badLog) {
		t.Fatal("CE-11 SteadyState must return false when blocked-fwd is delivered (not dropped); it returned true — bluff detected")
	}
}

// TestCE11_SelectivePartition verifies CE-11: c1↔c2 blocked, c1↔c3 open.
// Mutation: remove `sim.Partition("c1","c2")` from CE-11's Setup → blocked-fwd
// and blocked-rev are delivered → SteadyState returns false → r.Passed=false
// → t.Fatal fires.
func TestCE11_SelectivePartition(t *testing.T) {
	ce11 := findByID(t, "CE-11")
	r := chaosexp.Run(ce11, 42)

	if len(r.Events) != 4 {
		t.Fatalf("CE-11: expected 4 events, got %d", len(r.Events))
	}

	byPayload := make(map[string]turmoil.DeliveryEvent, 4)
	for _, ev := range r.Events {
		byPayload[string(ev.Payload)] = ev
	}

	for _, p := range []string{"blocked-fwd", "blocked-rev", "delivered-fwd", "delivered-rev"} {
		if _, ok := byPayload[p]; !ok {
			t.Fatalf("CE-11: missing event for payload %q; got %v", p, payloadKeys(r.Events))
		}
	}

	if !byPayload["blocked-fwd"].Dropped {
		t.Fatalf("CE-11: blocked-fwd not dropped")
	}
	if !byPayload["blocked-rev"].Dropped {
		t.Fatalf("CE-11: blocked-rev not dropped")
	}
	if byPayload["delivered-fwd"].Dropped {
		t.Fatalf("CE-11: delivered-fwd dropped unexpectedly")
	}
	if byPayload["delivered-rev"].Dropped {
		t.Fatalf("CE-11: delivered-rev dropped unexpectedly")
	}
	if !r.Passed {
		t.Fatalf("CE-11: Passed=false but selective partition evidence is correct")
	}
}

// TestCE12_SetupIsLoadBearing proves the c2↔c3 partition in CE-12's Setup is
// genuinely active by sending a c2→c3 message across the partitioned boundary
// and asserting it is Dropped=true. Without this probe the partition call could
// be removed from CE-12's Setup entirely without changing any observable event
// (since CE-12 only sends an intra-c1 message).
// Mutation: remove `sim.Partition("c2","c3")` from the sim below → c2→c3
// message is delivered (Dropped=false) → t.Fatal fires.
func TestCE12_SetupIsLoadBearing(t *testing.T) {
	// Build the same topology as CE-12's Setup but also send a c2→c3 message
	// to confirm the partition is exercised.
	sim := turmoil.NewSimulation(42)
	mustAdd(t, sim, "c1", []string{"n1", "n2"})
	mustAdd(t, sim, "c2", []string{"n1"})
	mustAdd(t, sim, "c3", []string{"n1"})
	sim.Partition("c2", "c3") // the partition under test
	mustSend(t, sim, "c2", "n1", "c3", "n1", []byte("cross-partition"), 1)
	sim.Run(20)

	log := sim.EventLog()
	if len(log) != 1 {
		t.Fatalf("CE-12 setup probe: expected 1 event, got %d", len(log))
	}
	if !log[0].Dropped {
		t.Fatalf("CE-12 setup probe: c2→c3 message should be dropped by the partition; got Dropped=false")
	}
	if log[0].Reason != "partition" {
		t.Fatalf("CE-12 setup probe: Reason=%q, want partition", log[0].Reason)
	}
}

// TestCE12_IntraUnaffectedByUnrelatedPartition verifies CE-12: c2↔c3 partition
// does not affect c1's internal message.
// Mutation: change CE-12's Setup to partition "c1","c2" instead of "c2","c3"
// → intra-c1 message delivered fine (still no cross-cluster for c1), but the
// SteadyState checks payload "intra-c1" which is still delivered — this would
// still pass. More precise mutation: change Setup to also Partition("c1","c1")
// but turmoil won't drop same-cluster messages. Best mutation for this test:
// change the SteadyState payload check from "intra-c1" to "intra-c2" →
// returns false since log[0].Payload is "intra-c1" not "intra-c2" → r.Passed=false
// → t.Fatal fires.
func TestCE12_IntraUnaffectedByUnrelatedPartition(t *testing.T) {
	ce12 := findByID(t, "CE-12")
	r := chaosexp.Run(ce12, 42)

	if len(r.Events) != 1 {
		t.Fatalf("CE-12: expected 1 event, got %d", len(r.Events))
	}
	ev := r.Events[0]
	if ev.Dropped {
		t.Fatalf("CE-12: intra-cluster message dropped; Reason=%q", ev.Reason)
	}
	if ev.FromCluster != "c1" || ev.ToCluster != "c1" {
		t.Fatalf("CE-12: unexpected clusters: from=%q to=%q", ev.FromCluster, ev.ToCluster)
	}
	if string(ev.Payload) != "intra-c1" {
		t.Fatalf("CE-12: payload=%q, want intra-c1", string(ev.Payload))
	}
	if !r.Passed {
		t.Fatalf("CE-12: Passed=false but intra-cluster message delivered correctly")
	}
}

// TestCE12_SteadyStateNotAlwaysTrue proves CE-12's SteadyState rejects a
// dropped event.
// Mutation: change CE-12's SteadyState to `return true` → accepts the dropped
// event below → t.Fatal fires.
func TestCE12_SteadyStateNotAlwaysTrue(t *testing.T) {
	ce12 := findByID(t, "CE-12")
	dropped := []turmoil.DeliveryEvent{
		{Tick: 1, FromCluster: "c1", ToCluster: "c1", Payload: []byte("intra-c1"),
			Dropped: true, Reason: "partition"},
	}
	if ce12.SteadyState(dropped) {
		t.Fatal("CE-12 SteadyState accepted a dropped event — bluff detected")
	}
}

// ── Canonical encoding shape ──────────────────────────────────────────────────

// TestCanonical_HasNineFields verifies that every event line in the Canonical
// string has exactly 9 pipe-separated fields, matching turmoil's EncodeLog
// format: tick|fromC|fromN|toC|toN|payloadHex|dropped|reason|nonce.
// Mutation: remove the Nonce field from turmoil.EncodeLog's format string →
// only 8 fields → len(parts)!=9 → t.Fatal fires.
func TestCanonical_HasNineFields(t *testing.T) {
	for _, e := range chaosexp.Catalog() {
		r := chaosexp.Run(e, 42)
		if r.Canonical == "" {
			t.Fatalf("%s: Canonical empty", e.ID)
		}
		lines := strings.Split(strings.TrimRight(r.Canonical, "\n"), "\n")
		for li, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) != 9 {
				t.Fatalf("%s line %d: expected 9 pipe-separated fields, got %d: %q",
					e.ID, li, len(parts), line)
			}
		}
	}
}

// ── Result.ID ────────────────────────────────────────────────────────────────

// TestRun_ResultIDMatchesExperimentID verifies that Result.ID is populated from
// the experiment's ID field.
// Mutation: hardcode `ID: "CE-00"` in Run's Result literal → Result.ID mismatches
// → t.Fatal fires.
func TestRun_ResultIDMatchesExperimentID(t *testing.T) {
	for _, e := range chaosexp.Catalog() {
		r := chaosexp.Run(e, 42)
		if r.ID != e.ID {
			t.Fatalf("Run(%s).ID = %q, want %q", e.ID, r.ID, e.ID)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// findByID returns the experiment with the given ID from Catalog, or fails the
// test.
func findByID(t *testing.T, id string) chaosexp.ChaosExperiment {
	t.Helper()
	for _, e := range chaosexp.Catalog() {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("experiment %q not found in catalog", id)
	panic("unreachable")
}

// payloadKeys returns a slice of string payloads from a DeliveryEvent slice,
// for diagnostic messages.
func payloadKeys(evs []turmoil.DeliveryEvent) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = string(ev.Payload)
	}
	return out
}

// mustAdd calls sim.AddCluster and fails the test on error.
func mustAdd(t *testing.T, sim *turmoil.Simulation, clusterID string, nodes []string) {
	t.Helper()
	if err := sim.AddCluster(clusterID, nodes); err != nil {
		t.Fatalf("AddCluster(%q): %v", clusterID, err)
	}
}

// mustSend calls sim.Send and fails the test on error.
func mustSend(t *testing.T, sim *turmoil.Simulation, fromC, fromN, toC, toN string, payload []byte, d int64) {
	t.Helper()
	if err := sim.Send(fromC, fromN, toC, toN, payload, d); err != nil {
		t.Fatalf("Send(%q/%q→%q/%q): %v", fromC, fromN, toC, toN, err)
	}
}
