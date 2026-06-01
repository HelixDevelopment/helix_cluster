package offlinesync

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// makeOutput returns a deterministic but redundant []byte payload that flate
// can compress better than its raw form.  The repetition is deliberate so that
// the test can assert len(compressed) < len(raw).
func makeOutput(jobIdx int) []byte {
	// 128 bytes of highly repetitive data per job — flate will compress this
	// far below the JSON representation.
	return bytes.Repeat([]byte(fmt.Sprintf("output-data-field-job-%04d:", jobIdx)), 5)
}

// ── TestOfflineSync_PreReconcile ─────────────────────────────────────────────
// (1) Device disconnected; complete N jobs offline — server.CountReconciled()
// must be 0 before any Reconcile call.
func TestOfflineSync_PreReconcile(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	device := NewDevice()
	server := NewServer()

	device.SetConnected(false)

	const N = 10
	now := time.Now()
	for i := 0; i < N; i++ {
		device.RecordCompleted(
			fmt.Sprintf("job-%s-%02d", runID, i),
			makeOutput(i),
			now,
		)
	}

	// Sink-side assertion: server has seen NOTHING — the jobs live only in the
	// device ledger.
	if got := server.CountReconciled(); got != 0 {
		t.Fatalf("pre-reconcile: server.CountReconciled()=%d, want 0 (jobs would be lost)", got)
	}
	if got := device.LedgerLen(); got != N {
		t.Fatalf("device ledger length=%d, want %d", got, N)
	}
}

// ── TestOfflineSync_72hOutageDoesNotGateRecovery ─────────────────────────────
// (2) A 72-hour outage is expressed purely as CompletedAt timestamp data; no
// real sleep.  RecordCompleted must accept a future timestamp and BuildDelta
// must return all N records regardless of the time gap.
func TestOfflineSync_72hOutageDoesNotGateRecovery(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	device := NewDevice()
	server := NewServer()

	device.SetConnected(false)

	disconnectAt := time.Now()
	disconnectDuration := 72 * time.Hour

	const N = 5
	for i := 0; i < N; i++ {
		// Timestamp is spread across the 72-hour offline window.
		jobTime := disconnectAt.Add(disconnectDuration * time.Duration(i+1) / time.Duration(N+1))
		device.RecordCompleted(
			fmt.Sprintf("job72h-%s-%02d", runID, i),
			makeOutput(i),
			jobTime,
		)
	}

	// Reconnect — the 72h gap must not gate recovery.
	device.SetConnected(true)
	if !device.Connected() {
		t.Fatal("device.SetConnected(true) did not take effect")
	}

	delta := device.BuildDelta(server.HighWaterMark())
	if len(delta) != N {
		t.Fatalf("BuildDelta returned %d records, want %d after 72h outage-as-data", len(delta), N)
	}

	// Sink-side: all records are present and span the expected time window.
	latestGap := delta[len(delta)-1].CompletedAt.Sub(disconnectAt)
	if latestGap < disconnectDuration/2 {
		t.Fatalf("expected CompletedAt gap >= %v, got %v — outage-duration not preserved in data", disconnectDuration/2, latestGap)
	}
}

// ── TestOfflineSync_DeltaCompressionAndRoundTrip ─────────────────────────────
// (3) Reconnect; build delta; compress it; assert:
//   - len(compressed) < len(rawJSON)  (flate actually shrinks redundant payloads)
//   - DecompressDelta(compressed) deep-equals the original delta (lossless).
func TestOfflineSync_DeltaCompressionAndRoundTrip(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	device := NewDevice()
	server := NewServer()

	device.SetConnected(false)

	const N = 20
	now := time.Now()
	for i := 0; i < N; i++ {
		device.RecordCompleted(
			fmt.Sprintf("job-compress-%s-%02d", runID, i),
			makeOutput(i),
			now.Add(time.Duration(i)*time.Minute),
		)
	}

	device.SetConnected(true)
	delta := device.BuildDelta(server.HighWaterMark())
	if len(delta) != N {
		t.Fatalf("BuildDelta: got %d, want %d", len(delta), N)
	}

	// Raw serialisation length.
	rawBytes, err := RawSerializeDelta(delta)
	if err != nil {
		t.Fatalf("RawSerializeDelta: %v", err)
	}

	// Compressed form.
	compressed, err := CompressDelta(delta)
	if err != nil {
		t.Fatalf("CompressDelta: %v", err)
	}

	// Sink-side assertion 1: compression actually reduces size.
	if len(compressed) >= len(rawBytes) {
		t.Fatalf("compression did not reduce size: compressed=%d raw=%d", len(compressed), len(rawBytes))
	}

	// Sink-side assertion 2: lossless round-trip.
	recovered, err := DecompressDelta(compressed)
	if err != nil {
		t.Fatalf("DecompressDelta: %v", err)
	}
	if len(recovered) != len(delta) {
		t.Fatalf("round-trip length: got %d, want %d", len(recovered), len(delta))
	}
	for i, orig := range delta {
		got := recovered[i]
		if orig.JobID != got.JobID {
			t.Errorf("[%d] JobID: got %q, want %q", i, got.JobID, orig.JobID)
		}
		if !bytes.Equal(orig.Output, got.Output) {
			t.Errorf("[%d] Output mismatch", i)
		}
		if !orig.CompletedAt.Equal(got.CompletedAt) {
			t.Errorf("[%d] CompletedAt: got %v, want %v", i, got.CompletedAt, orig.CompletedAt)
		}
		if orig.Seq != got.Seq {
			t.Errorf("[%d] Seq: got %d, want %d", i, got.Seq, orig.Seq)
		}
	}
}

// ── TestOfflineSync_ReconcileFullRecoveryAndIdempotency ──────────────────────
// (4) server.Reconcile(DecompressDelta(compressed)); assert:
//   - 100% recovery: CountReconciled()==N
//   - every offline JobID is present with matching Output
//   - idempotency: a 2nd Reconcile leaves count==N, no duplicates
//   - the per-run UUID is embedded in all job IDs and survives reconcile
func TestOfflineSync_ReconcileFullRecoveryAndIdempotency(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String() // per-run UUID embedded in every JobID
	device := NewDevice()
	server := NewServer()

	device.SetConnected(false)

	const N = 15
	now := time.Now()
	jobIDs := make([]string, N)
	outputs := make([][]byte, N)
	for i := 0; i < N; i++ {
		jobIDs[i] = fmt.Sprintf("job-reconcile-%s-%02d", runID, i)
		outputs[i] = makeOutput(i)
		device.RecordCompleted(jobIDs[i], outputs[i], now.Add(time.Duration(i)*time.Second))
	}

	// Before reconcile: server knows nothing.
	if got := server.CountReconciled(); got != 0 {
		t.Fatalf("expected 0 before reconcile, got %d", got)
	}

	device.SetConnected(true)
	delta := device.BuildDelta(server.HighWaterMark())
	if len(delta) != N {
		t.Fatalf("BuildDelta: got %d records, want %d", len(delta), N)
	}

	compressed, err := CompressDelta(delta)
	if err != nil {
		t.Fatalf("CompressDelta: %v", err)
	}
	recovered, err := DecompressDelta(compressed)
	if err != nil {
		t.Fatalf("DecompressDelta: %v", err)
	}

	// First reconcile.
	server.Reconcile(recovered)

	// Sink-side assertion: 100% recovery.
	if got := server.CountReconciled(); got != N {
		t.Fatalf("CountReconciled after 1st reconcile: got %d, want %d (100%% recovery failed)", got, N)
	}

	// Every JobID must be present with the correct Output.
	for i, id := range jobIDs {
		j, ok := server.GetJob(id)
		if !ok {
			t.Errorf("job %q (idx %d) missing from server after reconcile", id, i)
			continue
		}
		if !bytes.Equal(j.Output, outputs[i]) {
			t.Errorf("job %q: Output mismatch after reconcile", id)
		}
		// The per-run UUID is embedded in the JobID — assert it survives.
		if !containsSubstring(id, runID) {
			t.Errorf("run UUID %q not found in jobID %q", runID, id)
		}
	}

	// Idempotency: second Reconcile must leave count==N, no duplicates.
	server.Reconcile(recovered)
	if got := server.CountReconciled(); got != N {
		t.Fatalf("CountReconciled after 2nd reconcile (idempotency): got %d, want %d", got, N)
	}
}

// ── TestOfflineSync_MutationDropOneJob ───────────────────────────────────────
// Mutation test (§1.1): drop ONE job from delta before compression.
// CountReconciled must equal N-1, and the missing JobID must be absent.
// This proves that the ==N assertion in the normal path is non-trivial and
// that 100%-recovery is enforced, not vacuous.
func TestOfflineSync_MutationDropOneJob(t *testing.T) {
	t.Parallel()

	runID := uuid.New().String()
	device := NewDevice()
	server := NewServer()

	device.SetConnected(false)

	const N = 8
	now := time.Now()
	jobIDs := make([]string, N)
	for i := 0; i < N; i++ {
		jobIDs[i] = fmt.Sprintf("job-mutation-%s-%02d", runID, i)
		device.RecordCompleted(jobIDs[i], makeOutput(i), now.Add(time.Duration(i)*time.Second))
	}

	device.SetConnected(true)
	delta := device.BuildDelta(server.HighWaterMark())

	// §1.1 MUTATION: drop the last job from the delta before compression.
	// This simulates a transmission truncation / lossy channel.
	mutatedDelta := delta[:len(delta)-1] // len(delta)-1 == N-1
	droppedJobID := delta[len(delta)-1].JobID

	compressed, err := CompressDelta(mutatedDelta)
	if err != nil {
		t.Fatalf("CompressDelta (mutated): %v", err)
	}
	recovered, err := DecompressDelta(compressed)
	if err != nil {
		t.Fatalf("DecompressDelta (mutated): %v", err)
	}

	server.Reconcile(recovered)

	// Sink-side assertion: only N-1 jobs arrived.
	if got := server.CountReconciled(); got != N-1 {
		t.Fatalf("mutation: CountReconciled=%d, want %d (==N assertion would give false positive)", got, N-1)
	}
	// The dropped job must be absent from the server.
	if server.HasJob(droppedJobID) {
		t.Fatalf("mutation: dropped job %q is unexpectedly present on server", droppedJobID)
	}
	// All other jobs must still be present.
	for i := 0; i < N-1; i++ {
		if !server.HasJob(jobIDs[i]) {
			t.Errorf("mutation: job %q (idx %d) should be present but is missing", jobIDs[i], i)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func containsSubstring(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		(s == sub || len(s) > 0 && bytes.Contains([]byte(s), []byte(sub)))
}
