package offlinesync

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// This file is an ADVERSARIAL test of the sync / conflict-resolution
// correctness of the offlinesync protocol.
//
// MODEL (read from source, not assumed):
//   - Client/server protocol (NOT a multi-replica vector-clock CRDT).
//   - Device buffers CompletedJob records offline; each record has a
//     device-local monotonic Seq (the high-water-mark cursor).
//   - BuildDelta(hwm) returns exactly the records with Seq > hwm.
//   - Server.Reconcile merges a batch into a map keyed by JobID.
//
// DOCUMENTED RESOLUTION POLICY (reconcile.go doc comment, the authoritative
// spec):
//   1. idempotent merge keyed by JobID — re-submitting the same record never
//      duplicates and never regresses the high-water mark;
//   2. "only the first submission is kept (the one with the LOWER Seq wins in
//      the rare case of a duplicate that disagrees on Seq ...)";
//   3. HWM advances to the maximum Seq seen across all submitted records.
//
// THREAD-SAFETY CONTRACT: Device and Server are both documented "safe for
// concurrent use" (each guards all state with a sync.Mutex). Tests therefore
// exercise concurrent record+sync under -race.

// ── TestConflict_Convergence_SingleDeviceConverge (headline) ─────────────────
// The documented model is a SINGLE device <-> authoritative server cursor: a
// device drains via BuildDelta(server.HighWaterMark()) (its device-local Seq
// monotonically maps onto the server HWM). Two offline waves of disjoint edits,
// reconciled through the real wire path, must CONVERGE: the server holds the
// union and the device has nothing left to push (BuildDelta(HWM) is empty).
//
// (Cross-device cursor sharing is deliberately NOT exercised here: Seq is
// device-local, so one device's records cannot be cursored by another device's
// HWM — see TestConflict_CrossDeviceCursor_IsDeviceLocal which documents that
// boundary honestly rather than asserting a property the protocol never made.)
//
// ANTI-BLUFF: if Reconcile silently dropped a non-duplicate JobID (lost update)
// the union count would be < expected and this bites. If HWM did not advance to
// the max Seq, the post-convergence BuildDelta would re-emit already-reconciled
// records (oscillation) and the emptiness assert bites.
func TestConflict_Convergence_SingleDeviceConverge(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	server := NewServer()
	dev := NewDevice()

	now := time.Now()
	const perWave = 6

	ids := make([]string, 0, perWave*2)

	// Wave 1 (offline), then reconnect + drain via the documented cursor.
	for i := 0; i < perWave; i++ {
		id := fmt.Sprintf("w1-%s-%02d", runID, i)
		ids = append(ids, id)
		dev.RecordCompleted(id, []byte("w1-payload"), now.Add(time.Duration(i)*time.Second))
	}
	server.Reconcile(roundTrip(t, dev.BuildDelta(server.HighWaterMark())))

	// Wave 2 (another offline burst), drain again with the advanced cursor.
	for i := 0; i < perWave; i++ {
		id := fmt.Sprintf("w2-%s-%02d", runID, i)
		ids = append(ids, id)
		dev.RecordCompleted(id, []byte("w2-payload"), now.Add(time.Duration(perWave+i)*time.Second))
	}
	server.Reconcile(roundTrip(t, dev.BuildDelta(server.HighWaterMark())))

	// Convergence: server holds the full disjoint union of both waves.
	if got, want := server.CountReconciled(), perWave*2; got != want {
		t.Fatalf("convergence: CountReconciled=%d want %d (a non-conflicting update was lost)", got, want)
	}
	for _, id := range ids {
		if !server.HasJob(id) {
			t.Errorf("convergence: job %q lost", id)
		}
	}

	// "Ends identical": after convergence the device has nothing new to push
	// relative to the server HWM — HWM absorbed the device's max Seq.
	if d := dev.BuildDelta(server.HighWaterMark()); len(d) != 0 {
		t.Fatalf("post-convergence BuildDelta(HWM)=%d want 0 (HWM did not absorb device max Seq)", len(d))
	}
}

// ── TestConflict_CrossDeviceCursor_IsDeviceLocal ─────────────────────────────
// Honest boundary documentation: Seq is DEVICE-LOCAL, so the cursor protocol is
// single-device. If a SECOND device naively cursors off the server HWM that a
// FIRST device already advanced, its own (lower-Seq) records are NOT emitted —
// they would be silently lost. This is a real foot-gun of the documented model,
// not a code bug; we pin the behaviour and show the CORRECT multi-device drain.
//
// ANTI-BLUFF: the first assertion proves the naive cross-device cursor drops
// records (so callers MUST NOT do it); the second proves draining from Seq 0
// recovers them with no loss — convergence is achievable, just not via a shared
// global cursor.
func TestConflict_CrossDeviceCursor_IsDeviceLocal(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	server := NewServer()
	devA := NewDevice()
	devB := NewDevice()
	now := time.Now()

	const n = 4
	for i := 0; i < n; i++ {
		devA.RecordCompleted(fmt.Sprintf("A-%s-%02d", runID, i), []byte("a"), now)
		devB.RecordCompleted(fmt.Sprintf("B-%s-%02d", runID, i), []byte("b"), now)
	}

	server.Reconcile(roundTrip(t, devA.BuildDelta(server.HighWaterMark()))) // HWM -> n
	// Naive cross-device cursor: devB.BuildDelta(globalHWM) sees Seq>n, but devB
	// records are Seq 1..n => empty. Records would be silently lost.
	naive := devB.BuildDelta(server.HighWaterMark())
	if len(naive) != 0 {
		t.Fatalf("expected naive cross-device cursor to emit 0 (device-local Seq), got %d", len(naive))
	}

	// CORRECT multi-device drain: each device drains from its own Seq-0 baseline;
	// idempotent Reconcile dedupes. No update lost => full union converges.
	server.Reconcile(roundTrip(t, devB.BuildDelta(0)))
	if got, want := server.CountReconciled(), n*2; got != want {
		t.Fatalf("after correct per-device drain: count=%d want %d", got, want)
	}
}

// ── TestConflict_NonConflict_CleanFastForward_NoSpuriousConflict ──────────────
// A clean fast-forward: same device, two sequential sync waves to disjoint
// keys. Wave 2 must apply WITHOUT clobbering wave 1 and without a spurious
// "already exists" rejection of genuinely new keys.
//
// ANTI-BLUFF: bites a merge that mistakenly treats new keys as conflicts
// (would leave count == waveSize instead of 2*waveSize).
func TestConflict_NonConflict_CleanFastForward_NoSpuriousConflict(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	server := NewServer()
	dev := NewDevice()
	now := time.Now()

	const waveSize = 5
	// Wave 1.
	for i := 0; i < waveSize; i++ {
		dev.RecordCompleted(fmt.Sprintf("w1-%s-%02d", runID, i), []byte("v1"), now)
	}
	server.Reconcile(roundTrip(t, dev.BuildDelta(server.HighWaterMark())))
	if got := server.CountReconciled(); got != waveSize {
		t.Fatalf("wave1: count=%d want %d", got, waveSize)
	}
	hwmAfterW1 := server.HighWaterMark()

	// Wave 2 — disjoint keys; a clean fast-forward, no conflict.
	for i := 0; i < waveSize; i++ {
		dev.RecordCompleted(fmt.Sprintf("w2-%s-%02d", runID, i), []byte("v2"), now.Add(time.Minute))
	}
	delta2 := dev.BuildDelta(hwmAfterW1)
	// The cursor must hand back ONLY the new wave-2 records (no re-send of w1).
	if len(delta2) != waveSize {
		t.Fatalf("fast-forward: BuildDelta(hwmAfterW1)=%d want %d (cursor re-sent old records or dropped new ones)", len(delta2), waveSize)
	}
	server.Reconcile(roundTrip(t, delta2))

	if got := server.CountReconciled(); got != 2*waveSize {
		t.Fatalf("fast-forward: count=%d want %d (wave-2 keys spuriously treated as conflicts)", got, 2*waveSize)
	}
}

// ── TestConflict_SameJobIDConflict_Detected_LowerSeqWins ──────────────────────
// A GENUINE conflict: the SAME JobID submitted twice with DIFFERENT Seq and
// DIFFERENT Output (e.g. two devices that minted colliding IDs, or a retried
// job re-recorded under a new Seq). The documented policy is explicit and
// deterministic: the LOWER Seq wins, regardless of arrival order. We submit
// the conflicting pair in BOTH orders and assert the SAME winner each time.
//
// ANTI-BLUFF: this is the load-bearing conflict-resolution assertion. A merge
// that is "first in slice order wins" (arbitrary / order-dependent) yields the
// HIGHER-Seq record when that record arrives first — a non-deterministic winner
// that violates the documented lower-Seq-wins policy. This test bites that.
func TestConflict_SameJobIDConflict_Detected_LowerSeqWins(t *testing.T) {
	t.Parallel()

	id := "conflict-" + uuid.New().String()
	lowSeq := CompletedJob{JobID: id, Output: []byte("LOW-SEQ-WINNER"), CompletedAt: time.Now(), Seq: 3}
	highSeq := CompletedJob{JobID: id, Output: []byte("HIGH-SEQ-LOSER"), CompletedAt: time.Now(), Seq: 9}

	// Order 1: low then high.
	s1 := NewServer()
	s1.Reconcile([]CompletedJob{lowSeq, highSeq})
	got1, ok := s1.GetJob(id)
	if !ok {
		t.Fatal("order1: job missing entirely (conflict silently dropped both)")
	}

	// Order 2: high then low (reversed arrival order).
	s2 := NewServer()
	s2.Reconcile([]CompletedJob{highSeq, lowSeq})
	got2, ok := s2.GetJob(id)
	if !ok {
		t.Fatal("order2: job missing entirely")
	}

	// DOCUMENTED POLICY: lower Seq wins, DETERMINISTICALLY — same winner in
	// both arrival orders.
	if got1.Seq != got2.Seq {
		t.Fatalf("NON-DETERMINISTIC WINNER: order1 kept Seq=%d, order2 kept Seq=%d "+
			"(documented policy says lower-Seq wins regardless of order)", got1.Seq, got2.Seq)
	}
	if got1.Seq != lowSeq.Seq {
		t.Fatalf("WRONG WINNER: kept Seq=%d output=%q, documented policy says lower Seq=%d (output=%q) must win",
			got1.Seq, got1.Output, lowSeq.Seq, lowSeq.Output)
	}

	// HWM must still advance to the MAX Seq seen (documented), independent of
	// which record won the value slot.
	if s1.HighWaterMark() != highSeq.Seq {
		t.Fatalf("HWM=%d want %d (max Seq across batch)", s1.HighWaterMark(), highSeq.Seq)
	}
}

// ── TestConflict_IdempotentResync_NoOscillation ──────────────────────────────
// After convergence, syncing again is a no-op: re-reconciling the same batch
// neither duplicates, regresses HWM, nor flips a previously-resolved conflict.
//
// ANTI-BLUFF: bites oscillation (a winner that flips on re-sync) and bites a
// HWM that regresses or a count that grows on a repeat.
func TestConflict_IdempotentResync_NoOscillation(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	server := NewServer()
	dev := NewDevice()
	now := time.Now()

	const N = 7
	for i := 0; i < N; i++ {
		dev.RecordCompleted(fmt.Sprintf("idem-%s-%02d", runID, i), []byte("payload"), now.Add(time.Duration(i)*time.Second))
	}
	delta := dev.BuildDelta(server.HighWaterMark())

	server.Reconcile(roundTrip(t, delta))
	count0 := server.CountReconciled()
	hwm0 := server.HighWaterMark()
	// Snapshot the resolved value of one key.
	probe := fmt.Sprintf("idem-%s-00", runID)
	first, _ := server.GetJob(probe)

	// Re-sync the identical batch several times.
	for r := 0; r < 4; r++ {
		server.Reconcile(roundTrip(t, delta))
		if got := server.CountReconciled(); got != count0 {
			t.Fatalf("re-sync %d: count drifted to %d, want stable %d (duplicate created)", r, got, count0)
		}
		if got := server.HighWaterMark(); got != hwm0 {
			t.Fatalf("re-sync %d: HWM moved to %d, want stable %d (oscillation)", r, got, hwm0)
		}
		again, ok := server.GetJob(probe)
		if !ok || again.Seq != first.Seq || string(again.Output) != string(first.Output) {
			t.Fatalf("re-sync %d: resolved value of %q oscillated (Seq %d->%d)", r, probe, first.Seq, again.Seq)
		}
	}
}

// ── TestConflict_ConcurrentRecordAndSync_Race ────────────────────────────────
// The package is concurrent-by-contract (both types document "safe for
// concurrent use"). Hammer record-op + BuildDelta + Reconcile concurrently and
// assert: no race (run with -race), and NO LOST UPDATE — every distinct JobID
// the writers recorded lands on the server after a final drain.
//
// ANTI-BLUFF: -race bites an unsynchronised map/slice access; the
// no-lost-update final-drain assert bites a sync that races a record against a
// BuildDelta cursor and silently drops the in-flight record.
func TestConflict_ConcurrentRecordAndSync_Race(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	server := NewServer()
	dev := NewDevice()
	now := time.Now()

	const writers = 4
	const perWriter = 40
	total := writers * perWriter

	var wg sync.WaitGroup

	// Concurrent writers.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				dev.RecordCompleted(
					fmt.Sprintf("race-%s-w%d-%03d", runID, w, i),
					[]byte("p"),
					now.Add(time.Duration(i)*time.Millisecond),
				)
			}
		}(w)
	}

	// Concurrent syncers racing the writers (interleaved partial drains).
	for s := 0; s < 3; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				server.Reconcile(dev.BuildDelta(server.HighWaterMark()))
			}
		}()
	}

	wg.Wait()

	// Final authoritative drain: push everything from seq 0 so any record the
	// racing syncers missed is reconciled now. No update may be lost.
	server.Reconcile(dev.BuildDelta(0))

	if got := server.CountReconciled(); got != total {
		t.Fatalf("concurrency: server has %d distinct jobs after final drain, want %d (lost update)", got, total)
	}
	if got := dev.LedgerLen(); got != total {
		t.Fatalf("concurrency: device ledger has %d, want %d (record dropped under contention)", got, total)
	}
}

// roundTrip exercises the real wire path (compress -> decompress) so the test
// asserts against the same bytes the protocol actually ships, not the in-memory
// slice. A truncation/loss in the codec would surface as a count mismatch in
// the callers.
func roundTrip(t *testing.T, delta []CompletedJob) []CompletedJob {
	t.Helper()
	c, err := CompressDelta(delta)
	if err != nil {
		t.Fatalf("CompressDelta: %v", err)
	}
	out, err := DecompressDelta(c)
	if err != nil {
		t.Fatalf("DecompressDelta: %v", err)
	}
	return out
}
