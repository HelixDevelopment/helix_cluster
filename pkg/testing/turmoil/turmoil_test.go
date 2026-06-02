package turmoil_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/turmoil"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// buildSim constructs a Simulation with two clusters (c1, c2) each having one
// node ("n1"). It is the standard setup reused across many tests.
func buildSim(seed int64) *turmoil.Simulation {
	s := turmoil.NewSimulation(seed)
	if err := s.AddCluster("c1", []string{"n1"}); err != nil {
		panic(err)
	}
	if err := s.AddCluster("c2", []string{"n1"}); err != nil {
		panic(err)
	}
	return s
}

// ── AddCluster ────────────────────────────────────────────────────────────────

// TestAddCluster_DuplicateReturnsError verifies that registering the same
// clusterID twice returns ErrDuplicateCluster.
// Mutation: delete the duplicate-check return in AddCluster → no error returned → fails.
func TestAddCluster_DuplicateReturnsError(t *testing.T) {
	s := turmoil.NewSimulation(1)
	if err := s.AddCluster("c1", []string{"n1"}); err != nil {
		t.Fatalf("first AddCluster: %v", err)
	}
	err := s.AddCluster("c1", []string{"n2"})
	if err == nil {
		t.Fatal("expected error for duplicate cluster, got nil")
	}
	// Must be (or wrap) the typed sentinel.
	if !errors.Is(err, turmoil.ErrDuplicateCluster) {
		t.Fatalf("expected ErrDuplicateCluster, got %v", err)
	}
}

// TestSend_UnknownClusterReturnsError checks that Send rejects an unknown
// fromCluster with ErrUnknownCluster.
// Mutation: remove the fromCluster validation call → no error → fails.
func TestSend_UnknownClusterReturnsError(t *testing.T) {
	s := buildSim(1)
	err := s.Send("ghost", "n1", "c2", "n1", []byte("hi"), 1)
	if err == nil {
		t.Fatal("expected error for unknown cluster, got nil")
	}
	if !errors.Is(err, turmoil.ErrUnknownCluster) {
		t.Fatalf("expected ErrUnknownCluster, got %v", err)
	}
}

// TestSend_UnknownNodeReturnsError checks that Send rejects an unknown toNode.
// Mutation: remove the toNode validation call → no error → fails.
func TestSend_UnknownNodeReturnsError(t *testing.T) {
	s := buildSim(1)
	err := s.Send("c1", "n1", "c2", "ghost", []byte("hi"), 1)
	if err == nil {
		t.Fatal("expected error for unknown node, got nil")
	}
	if !errors.Is(err, turmoil.ErrUnknownNode) {
		t.Fatalf("expected ErrUnknownNode, got %v", err)
	}
}

// ── DETERMINISM (core contract) ───────────────────────────────────────────────

// TestDeterminism_SameSeedProducesIdenticalLog is the core contract: two
// independent NewSimulation(42) instances with identical operations produce
// exactly equal EventLog entries, including the seed-derived Nonce field.
// Mutation: set de.Nonce = 0 unconditionally in handleDelivery (zeroing the
// nonce before appending to log) → Nonce fields differ from seed-derived values
// → nonce field comparison in the loop fails.
func TestDeterminism_SameSeedProducesIdenticalLog(t *testing.T) {
	run := func() []turmoil.DeliveryEvent {
		s := turmoil.NewSimulation(42)
		must(t, s.AddCluster("c1", []string{"n1", "n2"}))
		must(t, s.AddCluster("c2", []string{"n1", "n2"}))
		must(t, s.Send("c1", "n1", "c2", "n1", []byte("alpha"), 3))
		must(t, s.Send("c1", "n2", "c2", "n2", []byte("beta"), 1))
		must(t, s.Send("c2", "n1", "c1", "n1", []byte("gamma"), 5))
		s.Run(100)
		return s.EventLog()
	}

	log1 := run()
	log2 := run()

	if len(log1) != len(log2) {
		t.Fatalf("log length mismatch: run1=%d run2=%d", len(log1), len(log2))
	}
	for i := range log1 {
		a, b := log1[i], log2[i]
		if a.Tick != b.Tick || a.FromCluster != b.FromCluster || a.FromNode != b.FromNode ||
			a.ToCluster != b.ToCluster || a.ToNode != b.ToNode ||
			!bytes.Equal(a.Payload, b.Payload) || a.Dropped != b.Dropped || a.Reason != b.Reason ||
			a.Nonce != b.Nonce {
			t.Fatalf("event[%d] diverged:\nrun1: %+v\nrun2: %+v", i, a, b)
		}
	}
	// Must have produced exactly the 3 sends.
	if len(log1) != 3 {
		t.Fatalf("expected 3 events, got %d", len(log1))
	}
	// All Nonces must be non-zero: the seed-42 PRNG never produces 0 for the
	// first three Int63 draws (confirmed empirically; any change to the nonce
	// draw or seed will reveal itself here or in the divergence test below).
	for i, ev := range log1 {
		if ev.Nonce == 0 {
			t.Fatalf("log1[%d].Nonce == 0: RNG was not consumed during Send", i)
		}
	}
}

// TestDeterminism_DifferentSeedsDiverge asserts that two Simulations with
// different seeds produce different canonical logs. This proves the PRNG is
// genuinely consumed: if the nonce draw were removed from Send, both
// simulations would produce identical canonical output regardless of seed and
// this test would fail.
// Mutation: remove the nonce draw (nonce := s.eng.RNG().Int63()) from Send →
// DeliveryEvent.Nonce is always 0 → EncodeLog output is seed-independent →
// enc42 == enc43 → t.Fatal fires.
func TestDeterminism_DifferentSeedsDiverge(t *testing.T) {
	run := func(seed int64) string {
		s := turmoil.NewSimulation(seed)
		must(t, s.AddCluster("c1", []string{"n1"}))
		must(t, s.AddCluster("c2", []string{"n1"}))
		must(t, s.Send("c1", "n1", "c2", "n1", []byte("msg"), 1))
		s.Run(10)
		return turmoil.EncodeLog(s.EventLog())
	}

	enc42 := run(42)
	enc43 := run(43)
	if enc42 == enc43 {
		t.Fatalf("simulations with different seeds produced identical canonical logs:\n%s", enc42)
	}
}

// TestDeterminism_CanonicalEncodingMatchesBothRuns cross-checks via the
// text-encoded canonical form rather than field-by-field comparison, ensuring
// the encoding itself is stable across two independent runs with the same seed.
// Mutation: change payload of one Send from []byte{0xDE,0xAD} to
// []byte{0xDE,0xAE} only in one run → canonical strings differ → fails.
func TestDeterminism_CanonicalEncodingMatchesBothRuns(t *testing.T) {
	build := func() *turmoil.Simulation {
		s := turmoil.NewSimulation(77)
		must(t, s.AddCluster("x", []string{"a"}))
		must(t, s.AddCluster("y", []string{"b"}))
		must(t, s.Send("x", "a", "y", "b", []byte{0xDE, 0xAD}, 2))
		s.Run(10)
		return s
	}

	enc1 := turmoil.EncodeLog(build().EventLog())
	enc2 := turmoil.EncodeLog(build().EventLog())
	if enc1 != enc2 {
		t.Fatalf("canonical encodings differ:\nrun1:\n%s\nrun2:\n%s", enc1, enc2)
	}
	// Concrete assertion: exactly one event, not dropped, tick=2, payload=dead,
	// nonce must be non-zero (seed-77 PRNG draw).
	if enc1 == "" {
		t.Fatal("expected non-empty canonical log")
	}
	// Extract nonce from enc1 to validate structure without hard-coding its value.
	// Format: tick|fromCluster|fromNode|toCluster|toNode|payloadHex|dropped|reason|nonce\n
	var tick, dropped, nonce int64
	var fromC, fromN, toC, toN, payloadHex, reason string
	n, err := fmt.Sscanf(enc1, "%d|%s", &tick, &fromC)
	if n != 2 || err != nil {
		t.Fatalf("canonical log malformed: %q", enc1)
	}
	// Full parse via Sscanf with pipe-delimited format isn't ergonomic;
	// use manual split to validate all fields.
	parts := splitPipe(enc1[:len(enc1)-1]) // strip trailing newline
	if len(parts) != 9 {
		t.Fatalf("expected 9 pipe-delimited fields, got %d in %q", len(parts), enc1)
	}
	if parts[0] != "2" {
		t.Fatalf("field[0] (tick) = %q, want \"2\"", parts[0])
	}
	fromC = parts[1]
	fromN = parts[2]
	toC = parts[3]
	toN = parts[4]
	payloadHex = parts[5]
	dropped, reason, nonce = 0, parts[7], 0
	fmt.Sscanf(parts[5], "%s", &payloadHex)
	fmt.Sscanf(parts[6], "%d", &dropped)
	fmt.Sscanf(parts[8], "%d", &nonce)

	if fromC != "x" || fromN != "a" || toC != "y" || toN != "b" {
		t.Fatalf("endpoint fields wrong: fromC=%q fromN=%q toC=%q toN=%q", fromC, fromN, toC, toN)
	}
	if payloadHex != "dead" {
		t.Fatalf("payload hex = %q, want \"dead\"", payloadHex)
	}
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if nonce == 0 {
		t.Fatalf("nonce == 0: RNG was not consumed by seed-77 simulation")
	}
	_ = tick
}

// splitPipe splits s on '|' and returns the parts.
func splitPipe(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// ── PARTITION blocks delivery ─────────────────────────────────────────────────

// TestPartition_BlocksCrossClusterDelivery verifies that a message sent across
// a partitioned link is recorded as Dropped with Reason=="partition".
// Mutation: comment out the partition-check block in handleDelivery → message
// is not dropped → Dropped==false → test fails.
func TestPartition_BlocksCrossClusterDelivery(t *testing.T) {
	s := buildSim(10)
	s.Partition("c1", "c2")
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("blocked"), 1))
	s.Run(10)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	ev := log[0]
	if !ev.Dropped {
		t.Fatalf("expected message to be dropped by partition, got Dropped=false")
	}
	if ev.Reason != "partition" {
		t.Fatalf("expected Reason=partition, got %q", ev.Reason)
	}
}

// TestPartition_HealAllowsDelivery verifies that after Heal a new Send IS
// delivered (not dropped).
// Mutation: remove the Heal call → partition still in place → second message
// also dropped → Dropped==true → test fails.
func TestPartition_HealAllowsDelivery(t *testing.T) {
	s := buildSim(11)
	s.Partition("c1", "c2")
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("first"), 1))
	s.Run(5)

	s.Heal("c1", "c2")
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("second"), 1))
	s.Run(5)

	log := s.EventLog()
	if len(log) != 2 {
		t.Fatalf("expected 2 events total, got %d", len(log))
	}
	if !log[0].Dropped {
		t.Fatal("first message (pre-heal) should be dropped")
	}
	if log[1].Dropped {
		t.Fatalf("second message (post-heal) should be delivered; Reason=%q", log[1].Reason)
	}
	// Concrete payload check — sink-side evidence.
	if !bytes.Equal(log[1].Payload, []byte("second")) {
		t.Fatalf("post-heal payload = %q, want \"second\"", log[1].Payload)
	}
}

// TestPartition_IsSymmetric verifies that Partition("c1","c2") also blocks
// c2→c1 messages (symmetric).
// Mutation: change newPartitionKey to a directional key → c2→c1 not blocked →
// test fails.
func TestPartition_IsSymmetric(t *testing.T) {
	s := buildSim(12)
	s.Partition("c1", "c2")
	// Send in the reverse direction.
	must(t, s.Send("c2", "n1", "c1", "n1", []byte("reverse"), 1))
	s.Run(10)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if !log[0].Dropped {
		t.Fatal("reverse-direction message should also be dropped by symmetric partition")
	}
}

// TestPartition_DoesNotAffectIntraCluster verifies that a cross-cluster
// partition leaves intra-cluster messages unaffected.
// Mutation: extend the partition check to also block same-cluster messages →
// intra-cluster message dropped → test fails.
func TestPartition_DoesNotAffectIntraCluster(t *testing.T) {
	s := turmoil.NewSimulation(13)
	must(t, s.AddCluster("c1", []string{"n1", "n2"}))
	must(t, s.AddCluster("c2", []string{"n1"}))

	s.Partition("c1", "c2") // only blocks cross-cluster

	// Intra-cluster: c1/n1 → c1/n2
	must(t, s.Send("c1", "n1", "c1", "n2", []byte("intra"), 1))
	s.Run(10)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if log[0].Dropped {
		t.Fatalf("intra-cluster message must not be dropped by cross-cluster partition; Reason=%q", log[0].Reason)
	}
}

// ── LATENCY ───────────────────────────────────────────────────────────────────

// TestLatency_ExtraTicksAddedToDelay verifies that InjectLatency(c1,c2,+5)
// causes a message with delayTicks=1 to be delivered at tick 6 (1+5).
// Mutation: remove the extra-latency addition in Send (total = delayTicks) →
// message delivered at tick 1 instead of 6 → tick assertion fails.
func TestLatency_ExtraTicksAddedToDelay(t *testing.T) {
	s := buildSim(20)
	s.InjectLatency("c1", "c2", 5)
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("slow"), 1))
	s.Run(100)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if log[0].Dropped {
		t.Fatalf("message should be delivered, not dropped")
	}
	const wantTick = int64(6) // 1 base + 5 extra
	if log[0].Tick != wantTick {
		t.Fatalf("expected Tick=%d, got Tick=%d", wantTick, log[0].Tick)
	}
}

// TestLatency_IsDirectional verifies that extra latency on c1→c2 does NOT
// affect c2→c1 messages (directional, not symmetric).
// Mutation: make InjectLatency symmetric → c2→c1 also gets +5 ticks →
// test fails because c2→c1 tick is 6 not 1.
func TestLatency_IsDirectional(t *testing.T) {
	s := buildSim(21)
	s.InjectLatency("c1", "c2", 5) // affects c1→c2 only
	must(t, s.Send("c2", "n1", "c1", "n1", []byte("fast"), 1))
	s.Run(100)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	const wantTick = int64(1)
	if log[0].Tick != wantTick {
		t.Fatalf("c2→c1 should not be delayed; expected Tick=%d, got Tick=%d", wantTick, log[0].Tick)
	}
}

// TestLatency_ZeroExtraLeavesTickUnchanged confirms baseline: InjectLatency
// with 0 extra ticks produces the same tick as no injection.
// Mutation: add 1 unconditionally to total in Send → tick=2 not 1 → fails.
func TestLatency_ZeroExtraLeavesTickUnchanged(t *testing.T) {
	s := buildSim(22)
	s.InjectLatency("c1", "c2", 0) // no extra
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("normal"), 1))
	s.Run(100)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if log[0].Tick != 1 {
		t.Fatalf("expected Tick=1 with zero latency injection, got %d", log[0].Tick)
	}
}

// ── ordering / tie-breaking ───────────────────────────────────────────────────

// TestRun_VirtualTimeOrderingDeterministic sends several messages with distinct
// delays and asserts the event log is in ascending tick order.
// Mutation: change one delayTicks value so tick order disagrees with the asserted
// sequence → order check fails.
func TestRun_VirtualTimeOrderingDeterministic(t *testing.T) {
	s := turmoil.NewSimulation(30)
	must(t, s.AddCluster("c1", []string{"n1"}))
	must(t, s.AddCluster("c2", []string{"n1"}))

	delays := []int64{5, 2, 8, 1, 3}
	for _, d := range delays {
		must(t, s.Send("c1", "n1", "c2", "n1", []byte(fmt.Sprintf("d%d", d)), d))
	}
	s.Run(100)

	log := s.EventLog()
	if len(log) != len(delays) {
		t.Fatalf("expected %d events, got %d", len(delays), len(log))
	}
	for i := 1; i < len(log); i++ {
		if log[i].Tick < log[i-1].Tick {
			t.Fatalf("events out of order: log[%d].Tick=%d < log[%d].Tick=%d",
				i, log[i].Tick, i-1, log[i-1].Tick)
		}
	}
	// Assert concrete tick sequence: 1,2,3,5,8.
	wantTicks := []int64{1, 2, 3, 5, 8}
	for i, wt := range wantTicks {
		if log[i].Tick != wt {
			t.Fatalf("log[%d].Tick=%d, want %d", i, log[i].Tick, wt)
		}
	}
}

// TestRun_MaxStepsRespected ensures Run(n) processes at most n events even
// when more are queued.
// Mutation: change maxSteps from 2 to 100 in the Run call → all 5 events
// processed → len(log)==5 != 2 → fails.
func TestRun_MaxStepsRespected(t *testing.T) {
	s := buildSim(31)
	for i := 0; i < 5; i++ {
		must(t, s.Send("c1", "n1", "c2", "n1", []byte("x"), int64(i+1)))
	}
	processed := s.Run(2)
	if processed != 2 {
		t.Fatalf("expected 2 events processed, got %d", processed)
	}
	if len(s.EventLog()) != 2 {
		t.Fatalf("expected 2 log entries after Run(2), got %d", len(s.EventLog()))
	}
}

// ── payload integrity ─────────────────────────────────────────────────────────

// TestPayload_DeliveredIntact confirms that the payload recorded in the
// DeliveryEvent is a byte-for-byte copy of what was sent.
// Mutation: change copyBytes to return a nil slice → payload is nil → bytes.Equal
// fails.
func TestPayload_DeliveredIntact(t *testing.T) {
	s := buildSim(40)
	want := []byte{0x01, 0x02, 0x03, 0xFF}
	must(t, s.Send("c1", "n1", "c2", "n1", want, 1))
	s.Run(10)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if !bytes.Equal(log[0].Payload, want) {
		t.Fatalf("payload mismatch: got %x, want %x", log[0].Payload, want)
	}
}

// TestPayload_MutationAfterSendDoesNotAffectLog verifies that mutating the
// original slice after Send does not corrupt the logged payload (deep copy).
// Mutation: change copyBytes to store a slice header alias (no copy) →
// mutation of orig corrupts log[0].Payload → bytes.Equal fails.
func TestPayload_MutationAfterSendDoesNotAffectLog(t *testing.T) {
	s := buildSim(41)
	orig := []byte{0xAA, 0xBB}
	must(t, s.Send("c1", "n1", "c2", "n1", orig, 1))
	orig[0] = 0xFF // mutate after Send
	s.Run(10)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if log[0].Payload[0] != 0xAA {
		t.Fatalf("payload[0]=%02x, want 0xAA — copyBytes must deep-copy", log[0].Payload[0])
	}
}

// ── EventLog isolation ────────────────────────────────────────────────────────

// TestEventLog_ReturnsCopy verifies that mutating the returned slice does not
// affect subsequent EventLog() calls.
// Mutation: return s.log directly (no copy) in EventLog → the mutation leaks
// back → the second EventLog()[0].Reason changes → test fails.
func TestEventLog_ReturnsCopy(t *testing.T) {
	s := buildSim(50)
	s.Partition("c1", "c2")
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("x"), 1))
	s.Run(10)

	log1 := s.EventLog()
	if len(log1) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log1))
	}
	log1[0].Reason = "MUTATED"

	log2 := s.EventLog()
	if log2[0].Reason != "partition" {
		t.Fatalf("EventLog() must return a copy; Reason was mutated to %q", log2[0].Reason)
	}
}

// ── multi-cluster / multiple nodes ───────────────────────────────────────────

// TestMultipleNodesInCluster verifies that messages between different nodes
// within the same cluster are tracked independently.
// Mutation: ignore the toNode field in handleDelivery → both events show same
// ToNode → second assertion ToNode=="n2" fails.
func TestMultipleNodesInCluster(t *testing.T) {
	s := turmoil.NewSimulation(60)
	must(t, s.AddCluster("c1", []string{"n1", "n2", "n3"}))

	must(t, s.Send("c1", "n1", "c1", "n2", []byte("to-n2"), 1))
	must(t, s.Send("c1", "n1", "c1", "n3", []byte("to-n3"), 2))
	s.Run(100)

	log := s.EventLog()
	if len(log) != 2 {
		t.Fatalf("expected 2 events, got %d", len(log))
	}
	if log[0].ToNode != "n2" {
		t.Fatalf("log[0].ToNode=%q, want n2", log[0].ToNode)
	}
	if log[1].ToNode != "n3" {
		t.Fatalf("log[1].ToNode=%q, want n3", log[1].ToNode)
	}
}

// TestIntraClusterUnaffectedByOtherPartition confirms that a partition between
// c2 and c3 leaves c1's intra-cluster messages untouched.
// Mutation: extend partition logic to block ALL cross-partition traffic
// regardless of clusterID → c1 intra message accidentally drops → fails.
func TestIntraClusterUnaffectedByOtherPartition(t *testing.T) {
	s := turmoil.NewSimulation(61)
	must(t, s.AddCluster("c1", []string{"n1", "n2"}))
	must(t, s.AddCluster("c2", []string{"n1"}))
	must(t, s.AddCluster("c3", []string{"n1"}))

	s.Partition("c2", "c3") // does NOT involve c1

	must(t, s.Send("c1", "n1", "c1", "n2", []byte("intra-c1"), 1))
	s.Run(10)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if log[0].Dropped {
		t.Fatalf("intra-c1 message dropped by a c2<->c3 partition: reason=%q", log[0].Reason)
	}
}

// TestPartitionAndLatencyCombined verifies that a partition takes precedence
// over latency: even if latency is injected, a partitioned message is still
// dropped (not just delayed).
// Mutation: apply latency first then return early without checking partition →
// message delivered not dropped → test fails.
func TestPartitionAndLatencyCombined(t *testing.T) {
	s := buildSim(70)
	s.InjectLatency("c1", "c2", 100) // large extra delay
	s.Partition("c1", "c2")          // should still block
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("combo"), 1))
	s.Run(1000)

	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if !log[0].Dropped {
		t.Fatal("partition must block message even when latency is injected")
	}
	if log[0].Reason != "partition" {
		t.Fatalf("reason = %q, want partition", log[0].Reason)
	}
}

// TestEncodeLog_UsedByBothRuns verifies that EncodeLog (the exported canonical
// encoder in turmoil.go) produces the same output for two independent runs
// with the same seed, and that the output includes the nonce field.
// Mutation: remove Nonce from the EncodeLog format string → nonce always
// absent → two different-seed runs produce identical output → divergence check fails.
func TestEncodeLog_UsedByBothRuns(t *testing.T) {
	buildAndEncode := func(seed int64) string {
		s := turmoil.NewSimulation(seed)
		must(t, s.AddCluster("a", []string{"x"}))
		must(t, s.AddCluster("b", []string{"y"}))
		must(t, s.Send("a", "x", "b", "y", []byte{0x01}, 1))
		s.Run(10)
		return turmoil.EncodeLog(s.EventLog())
	}

	// Same seed → same output.
	enc1 := buildAndEncode(99)
	enc2 := buildAndEncode(99)
	if enc1 != enc2 {
		t.Fatalf("same seed produced different canonical logs:\nrun1:%s\nrun2:%s", enc1, enc2)
	}

	// Different seed → different output (nonce field differs).
	enc3 := buildAndEncode(100)
	if enc1 == enc3 {
		t.Fatal("different seeds produced identical canonical logs — nonce not in encoding or RNG not consumed")
	}

	// Verify the format has 9 pipe-separated fields per line.
	line := enc1[:len(enc1)-1] // strip trailing newline
	parts := splitPipe(line)
	if len(parts) != 9 {
		t.Fatalf("EncodeLog line has %d pipe fields, want 9: %q", len(parts), enc1)
	}
}

// ── error helpers ─────────────────────────────────────────────────────────────

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
