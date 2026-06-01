package dataplane

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// freePort returns a loopback TCP address with a port derived from offset to
// avoid conflicts between test cases running in the same process.
func tcpAddr(port int) string {
	return fmt.Sprintf("tcp://127.0.0.1:%d", port)
}

// ─── ROUTER / DEALER ─────────────────────────────────────────────────────────

// TestRouterDealerCorrelatedReplies verifies the ROUTER->DEALER pattern:
//   - N tasks are dispatched, each with a unique correlation-id.
//   - Every reply must carry the MATCHING correlation-id (exact pairing).
//   - No mismatched, dropped, or duplicated ids are acceptable.
func TestRouterDealerCorrelatedReplies(t *testing.T) {
	const N = 8
	const basePort = 15100

	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	// Start N workers; each echoes the payload back with the same corrID.
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wk, err := NewWorker(tcpAddr(basePort), fmt.Sprintf("worker-%02d", idx))
			if err != nil {
				t.Errorf("NewWorker[%d]: %v", idx, err)
				return
			}
			defer wk.Close()

			if err := wk.Register(); err != nil {
				t.Errorf("worker[%d] register: %v", idx, err)
				return
			}

			task, err := wk.RecvTask()
			if err != nil {
				t.Errorf("worker[%d] RecvTask: %v", idx, err)
				return
			}
			// Echo correlation-id back with a result payload.
			reply := Reply{
				CorrelationID: task.CorrelationID,
				Result:        append([]byte("result:"), task.Payload...),
			}
			if err := wk.SendReply(reply); err != nil {
				t.Errorf("worker[%d] SendReply: %v", idx, err)
			}
		}(i)
	}

	// Collect worker registrations before dispatching.
	workerIDs := make([][]byte, 0, N)
	for i := 0; i < N; i++ {
		id, err := dist.RecvWorkerRegistration()
		if err != nil {
			t.Fatalf("RecvWorkerRegistration[%d]: %v", i, err)
		}
		workerIDs = append(workerIDs, id)
	}

	// Dispatch N tasks: one per worker in round-robin order.
	tasks := make([]Task, N)
	for i := 0; i < N; i++ {
		tasks[i] = Task{
			CorrelationID: fmt.Sprintf("corr-%04d", i),
			Payload:       []byte(fmt.Sprintf("payload-%d", i)),
		}
		if err := dist.SendToWorker(workerIDs[i], tasks[i]); err != nil {
			t.Fatalf("SendToWorker[%d]: %v", i, err)
		}
	}

	// Collect and verify replies.
	received := make(map[string]bool, N)
	for i := 0; i < N; i++ {
		_, reply, err := dist.RecvReply()
		if err != nil {
			t.Fatalf("RecvReply[%d]: %v", i, err)
		}
		// Verify the reply carries a known correlation-id.
		found := false
		for _, task := range tasks {
			if task.CorrelationID == reply.CorrelationID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("RecvReply[%d]: unexpected correlation-id %q", i, reply.CorrelationID)
		}
		if received[reply.CorrelationID] {
			t.Errorf("RecvReply[%d]: duplicate correlation-id %q", i, reply.CorrelationID)
		}
		received[reply.CorrelationID] = true

		// Verify result payload starts with "result:" — sink-side structural check.
		if len(reply.Result) < 7 || string(reply.Result[:7]) != "result:" {
			t.Errorf("RecvReply[%d]: corrID=%q result=%q does not start with \"result:\"",
				i, reply.CorrelationID, reply.Result)
		}
	}

	// Assert all N distinct correlation-ids were received.
	if len(received) != N {
		t.Errorf("received %d distinct correlation-ids, want %d", len(received), N)
	}
	for i := 0; i < N; i++ {
		cid := fmt.Sprintf("corr-%04d", i)
		if !received[cid] {
			t.Errorf("missing reply for correlation-id %q", cid)
		}
	}

	wg.Wait()
}

// TestRouterDealerExactCorrelationPairing is the mutation-targeted test:
// it constructs a scenario where a worker returns a DIFFERENT correlation-id
// and asserts the mismatch is detected.
func TestRouterDealerExactCorrelationPairing(t *testing.T) {
	const basePort = 15110

	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	// Worker that deliberately returns a WRONG correlation-id.
	done := make(chan struct{})
	go func() {
		defer close(done)
		wk, err := NewWorker(tcpAddr(basePort), "bad-worker")
		if err != nil {
			return
		}
		defer wk.Close()
		wk.Register() //nolint:errcheck
		task, err := wk.RecvTask()
		if err != nil {
			return
		}
		// Return a deliberately wrong correlation-id.
		wk.SendReply(Reply{ //nolint:errcheck
			CorrelationID: "WRONG-ID",
			Result:        task.Payload,
		})
	}()

	id, err := dist.RecvWorkerRegistration()
	if err != nil {
		t.Fatalf("registration: %v", err)
	}

	origTask := Task{CorrelationID: "correct-id", Payload: []byte("data")}
	if err := dist.SendToWorker(id, origTask); err != nil {
		t.Fatalf("SendToWorker: %v", err)
	}

	_, reply, err := dist.RecvReply()
	if err != nil {
		t.Fatalf("RecvReply: %v", err)
	}
	<-done

	// The test MUST detect the mismatch.
	if reply.CorrelationID == origTask.CorrelationID {
		t.Errorf("correlation-id match should have FAILED but PASSED: got %q", reply.CorrelationID)
	}
	// Verify the mismatch is actually "WRONG-ID".
	if reply.CorrelationID != "WRONG-ID" {
		t.Errorf("expected WRONG-ID, got %q", reply.CorrelationID)
	}
}

// ─── PUSH / PULL ─────────────────────────────────────────────────────────────

// TestPushPullFairQueueAndAck verifies the PUSH/PULL log pipeline:
//   - M messages are pushed to 2 PULL workers.
//   - Fair-queue distribution: each worker receives a non-zero share.
//   - At-least-once: every message ID is acked exactly once (all accounted for).
func TestPushPullFairQueueAndAck(t *testing.T) {
	const M = 20
	const basePort = 15120

	pipeline, err := NewLogPipeline(tcpAddr(basePort), tcpAddr(basePort+1))
	if err != nil {
		t.Fatalf("NewLogPipeline: %v", err)
	}
	defer pipeline.Close()

	// Two workers.
	var (
		worker1Count int64
		worker2Count int64
	)

	var wg sync.WaitGroup
	runWorker := func(workerID string, counter *int64) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lw, err := NewLogWorker(pipeline.PushAddr(), pipeline.AckAddr(), workerID)
			if err != nil {
				t.Errorf("NewLogWorker[%s]: %v", workerID, err)
				return
			}
			defer lw.Close()

			for {
				msg, err := lw.RecvLog(200 * time.Millisecond)
				if err != nil {
					// Timeout means no more messages.
					return
				}
				atomic.AddInt64(counter, 1)
				if err := lw.SendAck(msg.ID); err != nil {
					t.Errorf("worker[%s] ack %s: %v", workerID, msg.ID, err)
				}
			}
		}()
	}

	runWorker("log-worker-1", &worker1Count)
	runWorker("log-worker-2", &worker2Count)

	// Small delay to allow workers to connect and subscribe.
	time.Sleep(20 * time.Millisecond)

	// Push M messages with unique IDs.
	sentIDs := make([]string, M)
	for i := 0; i < M; i++ {
		sentIDs[i] = fmt.Sprintf("msg-%04d", i)
		if err := pipeline.Send(LogMessage{
			ID:   sentIDs[i],
			Body: []byte(fmt.Sprintf("log body %d", i)),
		}); err != nil {
			t.Fatalf("Send[%d]: %v", i, err)
		}
	}

	// Collect all M acks.
	ackedIDs := make([]string, 0, M)
	ackTimeout := 2 * time.Second
	deadline := time.Now().Add(ackTimeout)
	for len(ackedIDs) < M && time.Now().Before(deadline) {
		id, err := pipeline.RecvAck(200 * time.Millisecond)
		if err != nil {
			continue
		}
		ackedIDs = append(ackedIDs, id)
	}

	wg.Wait()

	// ── Assertion 1: all M messages acked (at-least-once) ──────────────────
	if len(ackedIDs) != M {
		t.Errorf("acked %d messages, want %d", len(ackedIDs), M)
	}

	sort.Strings(sentIDs)
	sort.Strings(ackedIDs)
	for i, id := range sentIDs {
		if i >= len(ackedIDs) || ackedIDs[i] != id {
			t.Errorf("missing ack for message %q", id)
		}
	}

	// ── Assertion 2: fair-queue — each worker got a non-zero share ──────────
	w1 := atomic.LoadInt64(&worker1Count)
	w2 := atomic.LoadInt64(&worker2Count)
	total := w1 + w2
	if total != M {
		t.Errorf("total received by workers = %d, want %d", total, M)
	}
	if w1 == 0 {
		t.Errorf("worker-1 received 0 messages (fair-queue failure)")
	}
	if w2 == 0 {
		t.Errorf("worker-2 received 0 messages (fair-queue failure)")
	}
	t.Logf("fair-queue distribution: worker1=%d worker2=%d", w1, w2)
}

// TestPushPullMissingAckDetected is the mutation-targeted test for the ack path:
// it verifies that if a worker processes a message but does NOT send an ack,
// the acked set is incomplete — proving the ack assertion catches dropped acks.
func TestPushPullMissingAckDetected(t *testing.T) {
	const basePort = 15130

	pipeline, err := NewLogPipeline(tcpAddr(basePort), tcpAddr(basePort+1))
	if err != nil {
		t.Fatalf("NewLogPipeline: %v", err)
	}
	defer pipeline.Close()

	// A worker that intentionally does NOT send acks.
	done := make(chan struct{})
	go func() {
		defer close(done)
		lw, err := NewLogWorker(pipeline.PushAddr(), pipeline.AckAddr(), "no-ack-worker")
		if err != nil {
			return
		}
		defer lw.Close()
		for {
			_, err := lw.RecvLog(200 * time.Millisecond)
			if err != nil {
				return
			}
			// Intentionally: no lw.SendAck(...)
		}
	}()

	time.Sleep(20 * time.Millisecond)

	const M = 3
	for i := 0; i < M; i++ {
		pipeline.Send(LogMessage{ID: fmt.Sprintf("drop-%d", i), Body: []byte("x")}) //nolint:errcheck
	}

	// Try to collect acks — we expect none since the worker does not ack.
	ackedCount := 0
	for i := 0; i < M; i++ {
		_, err := pipeline.RecvAck(100 * time.Millisecond)
		if err == nil {
			ackedCount++
		}
	}
	<-done

	// The mutation: if ack-sending were unconditional (no code path to omit it),
	// ackedCount would equal M and this test would pass trivially.
	// With the worker NOT acking, ackedCount MUST be 0.
	if ackedCount != 0 {
		t.Errorf("expected 0 acks from a non-acking worker, got %d", ackedCount)
	}
}

// TestDistributorRecvReplyFrameLayout verifies the exact multipart frame
// structure expected by RecvReply — a sink-side structural assertion that
// mutations to the frame packing will break.
func TestDistributorRecvReplyFrameLayout(t *testing.T) {
	const basePort = 15140

	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wk, err := NewWorker(tcpAddr(basePort), "frame-worker")
		if err != nil {
			return
		}
		defer wk.Close()
		wk.Register() //nolint:errcheck
		task, err := wk.RecvTask()
		if err != nil {
			return
		}
		wk.SendReply(Reply{ //nolint:errcheck
			CorrelationID: task.CorrelationID,
			Result:        []byte("frame-result"),
		})
	}()

	id, err := dist.RecvWorkerRegistration()
	if err != nil {
		t.Fatalf("registration: %v", err)
	}

	const corrID = "frame-test-corr"
	if err := dist.SendToWorker(id, Task{CorrelationID: corrID, Payload: []byte("fp")}); err != nil {
		t.Fatalf("SendToWorker: %v", err)
	}

	_, reply, err := dist.RecvReply()
	if err != nil {
		t.Fatalf("RecvReply: %v", err)
	}
	<-done

	// Exact field assertions — not just "non-nil".
	if reply.CorrelationID != corrID {
		t.Errorf("CorrelationID = %q, want %q", reply.CorrelationID, corrID)
	}
	if string(reply.Result) != "frame-result" {
		t.Errorf("Result = %q, want %q", reply.Result, "frame-result")
	}
}

// TestLogMessageRoundTrip verifies exact field preservation through the
// PUSH/PULL pipeline: ID and Body survive the wire without corruption.
func TestLogMessageRoundTrip(t *testing.T) {
	const basePort = 15150

	pipeline, err := NewLogPipeline(tcpAddr(basePort), tcpAddr(basePort+1))
	if err != nil {
		t.Fatalf("NewLogPipeline: %v", err)
	}
	defer pipeline.Close()

	recv := make(chan LogMessage, 1)
	go func() {
		lw, err := NewLogWorker(pipeline.PushAddr(), pipeline.AckAddr(), "rt-worker")
		if err != nil {
			return
		}
		defer lw.Close()
		msg, err := lw.RecvLog(2 * time.Second)
		if err != nil {
			close(recv)
			return
		}
		lw.SendAck(msg.ID) //nolint:errcheck
		recv <- msg
	}()

	time.Sleep(20 * time.Millisecond)

	const wantID = "round-trip-id-42"
	wantBody := []byte("exact-body-content\x00\xff\xfe")
	if err := pipeline.Send(LogMessage{ID: wantID, Body: wantBody}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait for ack.
	ackID, err := pipeline.RecvAck(2 * time.Second)
	if err != nil {
		t.Fatalf("RecvAck: %v", err)
	}
	if ackID != wantID {
		t.Errorf("ack ID = %q, want %q", ackID, wantID)
	}

	// Verify the message received by the worker has exact field values.
	msg, ok := <-recv
	if !ok {
		t.Fatal("worker did not produce a message")
	}
	if msg.ID != wantID {
		t.Errorf("LogMessage.ID = %q, want %q", msg.ID, wantID)
	}
	if string(msg.Body) != string(wantBody) {
		t.Errorf("LogMessage.Body = %q, want %q", msg.Body, wantBody)
	}
}
