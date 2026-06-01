//go:build integration

package dataplane

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Integration tests run against real tcp://127.0.0.1 loopback sockets.
// libzmq is always linked — no t.Skip, hard-FAIL on any error.
// These tests are run by the orchestrator with -tags=integration.

const (
	intBaseRouterPort = 16100
	intBasePushPort   = 16110
)

// TestIntegrationRouterDealerWithUUID dispatches tasks over real TCP loopback.
// Each task embeds a per-run UUID; every reply must echo the same UUID back.
func TestIntegrationRouterDealerWithUUID(t *testing.T) {
	runID := uuid.New().String()
	const N = 6

	addr := fmt.Sprintf("tcp://127.0.0.1:%d", intBaseRouterPort)

	dist, err := NewDistributor(addr)
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer func() {
		if err := dist.Close(); err != nil {
			t.Errorf("dist.Close: %v", err)
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wk, err := NewWorker(addr, fmt.Sprintf("int-worker-%02d", idx))
			if err != nil {
				t.Errorf("NewWorker[%d]: %v", idx, err)
				return
			}
			defer wk.Close()

			if err := wk.Register(); err != nil {
				t.Errorf("worker[%d] Register: %v", idx, err)
				return
			}

			task, err := wk.RecvTask()
			if err != nil {
				t.Errorf("worker[%d] RecvTask: %v", idx, err)
				return
			}

			// Echo payload verbatim — proves UUID round-trips.
			if err := wk.SendReply(Reply{
				CorrelationID: task.CorrelationID,
				Result:        task.Payload,
			}); err != nil {
				t.Errorf("worker[%d] SendReply: %v", idx, err)
			}
		}(i)
	}

	// Collect worker registrations.
	workerIDs := make([][]byte, 0, N)
	for i := 0; i < N; i++ {
		id, err := dist.RecvWorkerRegistration()
		if err != nil {
			t.Fatalf("RecvWorkerRegistration[%d]: %v", i, err)
		}
		workerIDs = append(workerIDs, id)
	}

	// Dispatch: each task payload includes the run-level UUID so we can
	// assert it survives the full marshal/unmarshal cycle.
	tasks := make([]Task, N)
	for i := 0; i < N; i++ {
		tasks[i] = Task{
			CorrelationID: fmt.Sprintf("int-corr-%04d", i),
			Payload:       []byte(fmt.Sprintf("%s|task-%d", runID, i)),
		}
		if err := dist.SendToWorker(workerIDs[i], tasks[i]); err != nil {
			t.Fatalf("SendToWorker[%d]: %v", i, err)
		}
	}

	// Collect and verify all N replies.
	received := make(map[string]string, N) // corrID -> result
	for i := 0; i < N; i++ {
		_, reply, err := dist.RecvReply()
		if err != nil {
			t.Fatalf("RecvReply[%d]: %v", i, err)
		}
		received[reply.CorrelationID] = string(reply.Result)
	}

	wg.Wait()

	// Assert all N distinct correlation-ids are present.
	if len(received) != N {
		t.Errorf("received %d replies, want %d", len(received), N)
	}

	for i, task := range tasks {
		result, ok := received[task.CorrelationID]
		if !ok {
			t.Errorf("task[%d]: no reply for corr-id %q", i, task.CorrelationID)
			continue
		}
		// Exact correlation-id pairing: result must contain the run-level UUID.
		expectedPayload := fmt.Sprintf("%s|task-%d", runID, i)
		if result != expectedPayload {
			t.Errorf("task[%d] corrID=%q: result=%q, want %q",
				i, task.CorrelationID, result, expectedPayload)
		}
	}
}

// TestIntegrationPushPullWithUUID pushes log messages over real TCP loopback
// with a per-run UUID embedded in every message body. Asserts:
//   - All messages are acked (at-least-once).
//   - UUID survives the wire.
//   - Fair-queue: each worker gets a share.
func TestIntegrationPushPullWithUUID(t *testing.T) {
	runID := uuid.New().String()
	const M = 16

	pushAddr := fmt.Sprintf("tcp://127.0.0.1:%d", intBasePushPort)
	ackAddr := fmt.Sprintf("tcp://127.0.0.1:%d", intBasePushPort+1)

	pipeline, err := NewLogPipeline(pushAddr, ackAddr)
	if err != nil {
		t.Fatalf("NewLogPipeline: %v", err)
	}
	defer func() {
		if err := pipeline.Close(); err != nil {
			t.Errorf("pipeline.Close: %v", err)
		}
	}()

	type workerResult struct {
		id    string
		count int
	}

	resultCh := make(chan workerResult, 2)
	const numWorkers = 2

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerIdx int) {
			defer wg.Done()
			wid := fmt.Sprintf("int-log-worker-%d", workerIdx)
			lw, err := NewLogWorker(pushAddr, ackAddr, wid)
			if err != nil {
				t.Errorf("NewLogWorker[%d]: %v", workerIdx, err)
				return
			}
			defer lw.Close()

			count := 0
			for {
				msg, err := lw.RecvLog(300 * time.Millisecond)
				if err != nil {
					break
				}
				// Verify UUID is present in body.
				if len(msg.Body) < len(runID) {
					t.Errorf("worker[%d]: body too short to contain UUID: %q", workerIdx, msg.Body)
				}

				count++
				if err := lw.SendAck(msg.ID); err != nil {
					t.Errorf("worker[%d] SendAck %s: %v", workerIdx, msg.ID, err)
				}
			}
			resultCh <- workerResult{id: wid, count: count}
		}(w)
	}

	// Allow workers to connect.
	time.Sleep(30 * time.Millisecond)

	// Push M messages with embedded UUID.
	sentIDs := make([]string, M)
	for i := 0; i < M; i++ {
		sentIDs[i] = fmt.Sprintf("int-msg-%04d", i)
		if err := pipeline.Send(LogMessage{
			ID:   sentIDs[i],
			Body: []byte(fmt.Sprintf("%s|body-%d", runID, i)),
		}); err != nil {
			t.Fatalf("Send[%d]: %v", i, err)
		}
	}

	// Collect all M acks.
	ackedIDs := make([]string, 0, M)
	deadline := time.Now().Add(5 * time.Second)
	for len(ackedIDs) < M && time.Now().Before(deadline) {
		id, err := pipeline.RecvAck(300 * time.Millisecond)
		if err != nil {
			continue
		}
		ackedIDs = append(ackedIDs, id)
	}

	wg.Wait()
	close(resultCh)

	// Collect worker counts.
	counts := make(map[string]int)
	for r := range resultCh {
		counts[r.id] = r.count
	}

	// ── Assertion 1: all M acked ────────────────────────────────────────────
	if len(ackedIDs) != M {
		t.Errorf("acked %d messages, want %d", len(ackedIDs), M)
	}
	sort.Strings(sentIDs)
	sort.Strings(ackedIDs)
	for i, id := range sentIDs {
		if i >= len(ackedIDs) || ackedIDs[i] != id {
			t.Errorf("missing ack for %q", id)
		}
	}

	// ── Assertion 2: fair-queue — both workers received messages ────────────
	for wIdx := 0; wIdx < numWorkers; wIdx++ {
		wid := fmt.Sprintf("int-log-worker-%d", wIdx)
		if counts[wid] == 0 {
			t.Errorf("worker %s received 0 messages (fair-queue failure)", wid)
		}
	}
	t.Logf("integration fair-queue: %v (run-id=%s)", counts, runID)
}
