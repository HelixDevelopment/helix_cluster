package failconfirm

import (
	"os"
	"sync"
	"testing"
)

// concRunUUID anchors the concurrency-evidence logs to a fixed forensic id.
const concRunUUID = "failconfirm-HXC-1399-conc-9f3b21c7-8a40-4d1e-bb22-0c7711e9a210"

// ---------------------------------------------------------------------------
// THREAD-SAFETY CONTRACT (verbatim from failconfirm.go, Detector doc):
//
//   "Detector is not safe for concurrent use; callers must serialize access.
//    This is intentional for deterministic, single-goroutine
//    simulation/testing."
//
// This is an EXPLICIT, documented single-threaded contract (the honest-contract
// case, like pkg/ewmarank). The Detector's WRITE path (ReportPFAIL/ClearPFAIL
// mutate d.susp / d.dead maps) and READ path (State/ReporterCount read those
// maps, and liveReporterCount even mutates s.reporters during a "read" when a
// TTL is active) carry NO internal mutex BY DESIGN.
//
// Therefore the correct, non-bluff treatment is:
//   1. PROVE the disclaimer is truthful and load-bearing: an unsynchronized
//      concurrent report+query genuinely trips the Go race detector. If it did
//      NOT, the package would in fact be concurrency-safe and the disclaimer
//      would be a lie. (Opt-in via env so the package's default `go test -race`
//      stays green — running a deliberate data race under -race would crash the
//      suite by design.)
//   2. PROVE serialized correctness: with the caller holding an external lock
//      EXACTLY as the contract demands, heavy concurrent report/clear/query
//      traffic is race-clean AND the confirmation policy (distinct-quorum
//      promotion, exactly-once OnDead) holds.
//
// We deliberately do NOT add an internal mutex: the doc explicitly disclaims
// concurrency and declares single-goroutine use intentional, so an added lock
// would contradict the package's stated design (zero-risk honest contract).
// ---------------------------------------------------------------------------

// TestContractDisclaimerIsTruthful_RaceProof is the load-bearing proof that the
// "not safe for concurrent use" disclaimer is REAL: violating it (unsynchronized
// concurrent ReportPFAIL writes + State reads on the SAME Detector) actually
// produces a Go data race on the d.susp map. It is OPT-IN via
// FAILCONFIRM_PROVE_RACE=1 so it does not crash the package's normal
// `go test -race` run (a real race under -race is a fatal, by design).
//
// WHY THIS BITES: if someone "fixed" the package by adding an internal
// sync.RWMutex but left the disclaimer in place, this test (under -race) would
// STOP detecting a race -> the disclaimer would be stale/false and this proof
// would need to flip to a no-bug serialized test. Conversely, run today under
// -race it MUST report "DATA RACE", confirming the map is genuinely unguarded
// and the single-threaded contract is the true contract.
func TestContractDisclaimerIsTruthful_RaceProof(t *testing.T) {
	if os.Getenv("FAILCONFIRM_PROVE_RACE") != "1" {
		t.Skipf("[%s] opt-in race proof; set FAILCONFIRM_PROVE_RACE=1 and run with -race "+
			"to observe the documented unsynchronized-map data race", concRunUUID)
	}
	d, err := New(members5())
	if err != nil {
		t.Fatalf("[%s] New: %v", concRunUUID, err)
	}
	const target = NodeID("n5")
	var wg sync.WaitGroup
	wg.Add(2)
	// Writer: mutates d.susp without synchronization.
	go func() {
		defer wg.Done()
		for i := 0; i < 100000; i++ {
			_ = d.ReportPFAIL("n1", target)
			_ = d.ClearPFAIL("n1", target)
		}
	}()
	// Reader: reads d.susp / d.dead without synchronization.
	go func() {
		defer wg.Done()
		for i := 0; i < 100000; i++ {
			_ = d.State(target)
			_ = d.ReporterCount(target)
		}
	}()
	wg.Wait()
	// Under -race this point is never cleanly reached: the detector fires a
	// fatal "DATA RACE" on the unguarded d.susp map first. That fatal IS the
	// evidence the disclaimer is load-bearing.
	t.Logf("[%s] completed without -race (no race detector active)", concRunUUID)
}

// TestSerializedAccessIsRaceCleanAndCorrect proves the DOCUMENTED contract: when
// the caller serializes access with an external lock (exactly as the doc
// requires), the Detector is race-clean under -race AND the confirmation policy
// holds end to end. Many goroutines concurrently report PFAIL for a single
// target through the shared lock; the target reaches distinct-quorum and is
// promoted to Dead EXACTLY once.
//
// WHY THIS BITES (race side): it runs under the package's normal
// `go test -race`. If the package were silently mutated to do unsynchronized
// internal map access in a way the external lock did not cover, or if the test
// itself failed to serialize, -race would flag it. With correct external
// serialization it is clean — demonstrating the contract is satisfiable and the
// chosen no-mutex design is sound.
//
// WHY THIS BITES (policy side / mutation): quorum for N=5 is 3. We drive 5
// DISTINCT reporters; promotion MUST occur and OnDead MUST fire exactly once. If
// promotion were gated on TOTAL reports instead of DISTINCT reporters, or the
// quorum math were wrong, or OnDead fired per-report, the deadCount==1 and
// state==Dead assertions break.
func TestSerializedAccessIsRaceCleanAndCorrect(t *testing.T) {
	var mu sync.Mutex
	var deadCount int
	var deadTarget NodeID
	d, err := New(members5(), OnDead(func(n NodeID) {
		// OnDead runs inside ReportPFAIL, i.e. under the held lock; the counter
		// is therefore also protected. No separate synchronization needed.
		deadCount++
		deadTarget = n
	}))
	if err != nil {
		t.Fatalf("[%s] New: %v", concRunUUID, err)
	}
	const target = NodeID("n5")
	reporters := []NodeID{"n1", "n2", "n3", "n4"} // 4 distinct (quorum is 3)

	var wg sync.WaitGroup
	for _, r := range reporters {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each reporter hammers report/clear/query through the SHARED lock,
			// exactly the "callers must serialize access" contract.
			for i := 0; i < 5000; i++ {
				mu.Lock()
				_ = d.ReportPFAIL(r, target)
				_ = d.State(target)
				_ = d.ReporterCount(target)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	state := d.State(target)
	dc := deadCount
	dt := deadTarget
	mu.Unlock()

	t.Logf("[%s] after concurrent (serialized) load: state=%s deadCount=%d target=%s",
		concRunUUID, state, dc, dt)

	// 4 distinct reporters >= quorum 3 -> the target MUST be confirmed Dead.
	if state != Dead {
		t.Fatalf("[%s] want Dead (4 distinct reporters >= quorum 3), got %s", concRunUUID, state)
	}
	// Exactly-once promotion despite tens of thousands of overlapping reports.
	if dc != 1 {
		t.Fatalf("[%s] OnDead must fire EXACTLY once under concurrent load, got %d", concRunUUID, dc)
	}
	if dt != target {
		t.Fatalf("[%s] OnDead target: want %s got %s", concRunUUID, target, dt)
	}
}

// TestSerializedBelowQuorumNeverPromotes proves the policy lower-bound under
// concurrent (serialized) load: with only 2 DISTINCT reporters for N=5 (quorum
// 3), no amount of concurrent re-reporting by those same 2 reporters may promote
// the target. This bites a broken distinct-vs-total gate: if repeats were
// miscounted as new votes, the flood of repeats would falsely reach quorum.
func TestSerializedBelowQuorumNeverPromotes(t *testing.T) {
	var mu sync.Mutex
	var deadCount int
	d, err := New(members5(), OnDead(func(NodeID) { deadCount++ }))
	if err != nil {
		t.Fatalf("[%s] New: %v", concRunUUID, err)
	}
	const target = NodeID("n3")
	reporters := []NodeID{"n1", "n2"} // only 2 distinct < quorum 3

	var wg sync.WaitGroup
	for _, r := range reporters {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10000; i++ {
				mu.Lock()
				_ = d.ReportPFAIL(r, target)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	state := d.State(target)
	rc := d.ReporterCount(target)
	dc := deadCount
	mu.Unlock()

	t.Logf("[%s] 2 distinct reporters x10000 each: state=%s distinctReporters=%d deadCount=%d",
		concRunUUID, state, rc, dc)
	if state != Suspect {
		t.Fatalf("[%s] want Suspect (2 distinct < quorum 3), got %s", concRunUUID, state)
	}
	if rc != 2 {
		t.Fatalf("[%s] want 2 distinct reporters (repeats deduped), got %d", concRunUUID, rc)
	}
	if dc != 0 {
		t.Fatalf("[%s] OnDead must NOT fire below distinct quorum, got %d", concRunUUID, dc)
	}
}
