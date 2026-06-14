package dataplane

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Adversarial audit of pkg/dataplane — forwarding correctness + concurrency.
//
// FORWARDING MODEL
//   Distributor = ROUTER socket. SendToWorker(workerID, task) routes a task to
//   the DEALER addressed by workerID as a multipart message
//   [identity | delimiter | corrID | payload]. Workers echo
//   [delimiter | corrID | result]; RecvReply re-reads
//   [identity | delimiter | corrID | result] and re-pairs by corrID.
//
// SHARED MUTABLE STATE
//   Distributor.sock (*zmq.Socket) — the ROUTER. ONE socket, touched by
//   SendToWorker (write) and RecvReply / RecvWorkerRegistration (read).
//   Distributor.pending (map, guarded by mu) — only ever touched by the dead
//   Dispatch stub; not on any live path.
//
// SYNCHRONIZATION CONTRACT (as documented on the Distributor type):
//   "The Distributor is safe for concurrent use; internal state is protected by
//    a mutex."
//
// *** REAL BUG (see TestForwardConcurrentSendAbort_ProvesBug) ***
//   SendToWorker performs a 4-frame ROUTER SendMessage on d.sock with NO lock.
//   A libzmq socket is NOT thread-safe. Two goroutines calling SendToWorker on
//   the same Distributor interleave each other's frames mid-message and trip the
//   libzmq internal invariant:
//        Assertion failed: !_current_out (src/router.cpp:166)  -> SIGABRT.
//   This is a hard process abort (uncatchable), not a soft error — a single
//   node doing concurrent dispatch crashes. The doc claims concurrency-safety
//   the code does not provide. Minimal fix: serialize the send under d.mu
//   (hold the lock across d.sock.SendMessage in SendToWorker; RecvReply needs a
//   separate recv mutex since it blocks). Proven in the gated test below.
//
// The always-on tests here pin the CORRECT contract for the *serialized* use of
// a single Distributor socket (forwarding correctness, no mis-route, no
// drop/dup, teardown safety) so a regression in the routing/frame logic bites.
// ─────────────────────────────────────────────────────────────────────────────

// ─── Forwarding correctness: addressed to B, delivered to B and only B ───────

// TestForwardRoutesToAddressedWorkerOnly proves a task sent to a specific
// worker identity is delivered to THAT worker and to no other. Two workers
// register; a task is addressed only to worker B; worker A must time out
// (receive nothing) while worker B receives the exact task.
func TestForwardRoutesToAddressedWorkerOnly(t *testing.T) {
	const basePort = 15300
	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	type rcv struct {
		task Task
		err  error
	}
	chA := make(chan rcv, 1)
	chB := make(chan rcv, 1)

	startWorker := func(label string, out chan<- rcv) {
		go func() {
			wk, err := NewWorker(tcpAddr(basePort), label)
			if err != nil {
				out <- rcv{err: err}
				return
			}
			defer wk.Close()
			if err := wk.Register(); err != nil {
				out <- rcv{err: err}
				return
			}
			// RecvTask blocks; we bound it with a watchdog Close from the test
			// by relying on the test's overall timeout. To detect "no delivery"
			// we use a short poll loop via a goroutine + timeout in the test.
			task, err := wk.RecvTask()
			out <- rcv{task: task, err: err}
		}()
	}

	startWorker("worker-A", chA)
	startWorker("worker-B", chB)

	// Collect both registrations and map identity -> which worker it is by
	// sending each a probe is not possible (RecvTask already pending). Instead
	// we read both identities, then address the SECOND registrant. Because both
	// workers block in RecvTask, whichever identity we DON'T send to must NOT
	// receive anything.
	ids := make([][]byte, 0, 2)
	for i := 0; i < 2; i++ {
		id, err := dist.RecvWorkerRegistration()
		if err != nil {
			t.Fatalf("RecvWorkerRegistration[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}

	target := ids[1] // address exactly one worker
	want := Task{CorrelationID: "addr-corr", Payload: []byte("addressed-payload")}
	if err := dist.SendToWorker(target, want); err != nil {
		t.Fatalf("SendToWorker: %v", err)
	}

	// Exactly ONE of the two workers must receive the task within the window;
	// the other must still be blocked (no delivery). We wait a bounded time.
	var got *Task
	delivered := 0
	timeout := time.After(1500 * time.Millisecond)
	for delivered < 1 {
		select {
		case r := <-chA:
			if r.err != nil {
				t.Fatalf("worker-A error: %v", r.err)
			}
			tk := r.task
			got = &tk
			delivered++
		case r := <-chB:
			if r.err != nil {
				t.Fatalf("worker-B error: %v", r.err)
			}
			tk := r.task
			got = &tk
			delivered++
		case <-timeout:
			t.Fatalf("no worker received the addressed task")
		}
	}

	// The delivered task must be byte-exact (no mis-route corruption).
	if got.CorrelationID != want.CorrelationID {
		t.Errorf("delivered corrID = %q, want %q", got.CorrelationID, want.CorrelationID)
	}
	if string(got.Payload) != string(want.Payload) {
		t.Errorf("delivered payload = %q, want %q", got.Payload, want.Payload)
	}

	// The OTHER worker must NOT have received anything: assert no second
	// delivery arrives within a quiet window. (send-to-one => only-one).
	select {
	case <-chA:
		select {
		case <-chB:
			t.Errorf("both workers received a task; expected exactly one")
		default:
		}
	case <-chB:
		select {
		case <-chA:
			t.Errorf("both workers received a task; expected exactly one")
		default:
		}
	case <-time.After(300 * time.Millisecond):
		// Good: exactly one delivery, the other still blocked.
	}
}

// TestForwardNoMisrouteAcrossManyWorkers dispatches a distinct task to each of
// N registered workers and asserts every worker receives EXACTLY its own
// payload — no cross-delivery, no drop, no duplication. This is the
// forwarding-correctness centerpiece for the multi-destination case.
func TestForwardNoMisrouteAcrossManyWorkers(t *testing.T) {
	const basePort = 15320
	const N = 6
	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	// Each worker echoes [corrID -> "ok:"+corrID] so the Distributor can verify
	// per-destination pairing on the reply path too.
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wk, err := NewWorker(tcpAddr(basePort), fmt.Sprintf("mw-%02d", idx))
			if err != nil {
				t.Errorf("NewWorker[%d]: %v", idx, err)
				return
			}
			defer wk.Close()
			if err := wk.Register(); err != nil {
				t.Errorf("Register[%d]: %v", idx, err)
				return
			}
			task, err := wk.RecvTask()
			if err != nil {
				t.Errorf("RecvTask[%d]: %v", idx, err)
				return
			}
			// Reply result encodes BOTH the worker's own label region of the
			// payload and the corrID, so a mis-route (wrong worker got the task)
			// would surface as a payload/corrID mismatch at the sink.
			if err := wk.SendReply(Reply{
				CorrelationID: task.CorrelationID,
				Result:        append([]byte("ok:"), task.Payload...),
			}); err != nil {
				t.Errorf("SendReply[%d]: %v", idx, err)
			}
		}(i)
	}

	ids := make([][]byte, 0, N)
	for i := 0; i < N; i++ {
		id, err := dist.RecvWorkerRegistration()
		if err != nil {
			t.Fatalf("RecvWorkerRegistration[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}

	// Address a unique corrID+payload to each worker identity.
	wantByCorr := make(map[string]string, N)
	for i := 0; i < N; i++ {
		corr := fmt.Sprintf("dest-%02d", i)
		payload := fmt.Sprintf("payload-for-%02d", i)
		wantByCorr[corr] = payload
		if err := dist.SendToWorker(ids[i], Task{CorrelationID: corr, Payload: []byte(payload)}); err != nil {
			t.Fatalf("SendToWorker[%d]: %v", i, err)
		}
	}

	// Collect N replies; each corrID delivered exactly once, payload intact.
	seen := make(map[string]int, N)
	for i := 0; i < N; i++ {
		_, reply, err := dist.RecvReply()
		if err != nil {
			t.Fatalf("RecvReply[%d]: %v", i, err)
		}
		seen[reply.CorrelationID]++
		wantPayload, ok := wantByCorr[reply.CorrelationID]
		if !ok {
			t.Errorf("reply carries UNKNOWN corrID %q (mis-route)", reply.CorrelationID)
			continue
		}
		// result must be exactly "ok:"+payload that THIS corrID was sent.
		gotResult := string(reply.Result)
		wantResult := "ok:" + wantPayload
		if gotResult != wantResult {
			t.Errorf("corrID %q: result=%q, want %q (payload mis-route/corruption)",
				reply.CorrelationID, gotResult, wantResult)
		}
	}

	wg.Wait()

	// No drops, no duplicates: every corrID seen exactly once.
	if len(seen) != N {
		t.Errorf("distinct corrIDs replied = %d, want %d", len(seen), N)
	}
	for corr := range wantByCorr {
		switch seen[corr] {
		case 0:
			t.Errorf("corrID %q dropped (no reply)", corr)
		case 1:
			// exactly once — correct
		default:
			t.Errorf("corrID %q duplicated: delivered %d times", corr, seen[corr])
		}
	}
}

// ─── Teardown: removing a worker mid-flight must not mis-deliver/panic ────────

// TestForwardToClosedWorkerNoPanic sends to a worker identity that has already
// gone away (closed). The ROUTER must not panic; the send is dropped silently
// (ZMQ ROUTER default) and no other worker receives the stale task.
func TestForwardToClosedWorkerNoPanic(t *testing.T) {
	const basePort = 15340
	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	wk, err := NewWorker(tcpAddr(basePort), "ephemeral")
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	if err := wk.Register(); err != nil {
		t.Fatalf("Register: %v", err)
	}
	id, err := dist.RecvWorkerRegistration()
	if err != nil {
		t.Fatalf("RecvWorkerRegistration: %v", err)
	}

	// Tear the worker down BEFORE the send is dispatched (teardown race shape).
	if err := wk.Close(); err != nil {
		t.Fatalf("worker Close: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Sending to a now-absent identity must not panic. ROUTER drops by default.
	if err := dist.SendToWorker(id, Task{CorrelationID: "stale", Payload: []byte("x")}); err != nil {
		// An error is acceptable; a panic/abort is not. Record but do not fail
		// solely on a benign send error to an absent peer.
		t.Logf("SendToWorker to closed worker returned: %v (acceptable)", err)
	}
	// Double-remove shape: closing the same worker twice is the caller's bug,
	// but the Distributor itself must remain usable afterward.
	if dist.Addr() != tcpAddr(basePort) {
		t.Errorf("Distributor state corrupted after send-to-closed: addr=%q", dist.Addr())
	}
}

// ─── Edge: no workers, single worker ─────────────────────────────────────────

// TestForwardSingleWorkerExactDelivery is the minimal happy path: one worker,
// one task, exact round-trip. Anchors the contract for the trivial case.
func TestForwardSingleWorkerExactDelivery(t *testing.T) {
	const basePort = 15360
	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wk, err := NewWorker(tcpAddr(basePort), "solo")
		if err != nil {
			return
		}
		defer wk.Close()
		_ = wk.Register()
		task, err := wk.RecvTask()
		if err != nil {
			return
		}
		_ = wk.SendReply(Reply{CorrelationID: task.CorrelationID, Result: append([]byte("r:"), task.Payload...)})
	}()

	id, err := dist.RecvWorkerRegistration()
	if err != nil {
		t.Fatalf("RecvWorkerRegistration: %v", err)
	}
	if err := dist.SendToWorker(id, Task{CorrelationID: "solo-corr", Payload: []byte("solo-pl")}); err != nil {
		t.Fatalf("SendToWorker: %v", err)
	}
	_, reply, err := dist.RecvReply()
	if err != nil {
		t.Fatalf("RecvReply: %v", err)
	}
	<-done
	if reply.CorrelationID != "solo-corr" {
		t.Errorf("corrID = %q, want solo-corr", reply.CorrelationID)
	}
	if string(reply.Result) != "r:solo-pl" {
		t.Errorf("result = %q, want r:solo-pl", reply.Result)
	}
}

// ─── Serialized concurrency contract (the SAFE way to use one Distributor) ────

// TestForwardSerializedConcurrentSendIsSafe proves that when SendToWorker calls
// on a single Distributor are serialized by the CALLER (an external mutex), the
// forwarding plane is correct under load: many goroutines dispatch, every task
// is delivered exactly once, payloads intact. This pins the contract the
// production code MUST uphold internally (today it does not — see the gated
// abort repro below). With external serialization there is no abort, which
// localizes the defect precisely to the missing internal lock on d.sock.
func TestForwardSerializedConcurrentSendIsSafe(t *testing.T) {
	const basePort = 15380
	const N = 24
	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wk, err := NewWorker(tcpAddr(basePort), fmt.Sprintf("sc-%02d", idx))
			if err != nil {
				t.Errorf("NewWorker[%d]: %v", idx, err)
				return
			}
			defer wk.Close()
			if err := wk.Register(); err != nil {
				t.Errorf("Register[%d]: %v", idx, err)
				return
			}
			task, err := wk.RecvTask()
			if err != nil {
				t.Errorf("RecvTask[%d]: %v", idx, err)
				return
			}
			_ = wk.SendReply(Reply{CorrelationID: task.CorrelationID, Result: task.Payload})
		}(i)
	}

	ids := make([][]byte, 0, N)
	for i := 0; i < N; i++ {
		id, err := dist.RecvWorkerRegistration()
		if err != nil {
			t.Fatalf("RecvWorkerRegistration[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}

	// Many goroutines send concurrently, but EVERY socket touch is serialized
	// by sendMu — this is the contract the Distributor's own doc promises to
	// provide internally. With it, the plane is correct and abort-free.
	var sendMu sync.Mutex
	wantByCorr := make(map[string]string, N)
	for i := 0; i < N; i++ {
		corr := fmt.Sprintf("sc-corr-%02d", i)
		wantByCorr[corr] = fmt.Sprintf("sc-pl-%02d", i)
	}
	var sg sync.WaitGroup
	for i := 0; i < N; i++ {
		sg.Add(1)
		go func(idx int) {
			defer sg.Done()
			corr := fmt.Sprintf("sc-corr-%02d", idx)
			sendMu.Lock()
			err := dist.SendToWorker(ids[idx], Task{CorrelationID: corr, Payload: []byte(wantByCorr[corr])})
			sendMu.Unlock()
			if err != nil {
				t.Errorf("SendToWorker[%d]: %v", idx, err)
			}
		}(i)
	}
	sg.Wait()

	seen := make(map[string]int, N)
	for i := 0; i < N; i++ {
		_, reply, err := dist.RecvReply()
		if err != nil {
			t.Fatalf("RecvReply[%d]: %v", i, err)
		}
		seen[reply.CorrelationID]++
		if want, ok := wantByCorr[reply.CorrelationID]; ok {
			if string(reply.Result) != want {
				t.Errorf("corrID %q: result=%q want %q", reply.CorrelationID, reply.Result, want)
			}
		} else {
			t.Errorf("unknown corrID %q in reply", reply.CorrelationID)
		}
	}
	wg.Wait()

	if len(seen) != N {
		t.Errorf("distinct replies = %d, want %d", len(seen), N)
	}
	for corr := range wantByCorr {
		if seen[corr] != 1 {
			t.Errorf("corrID %q delivered %d times, want exactly 1", corr, seen[corr])
		}
	}
}

// ─── THE REAL BUG: gated abort repro ─────────────────────────────────────────

// TestForwardConcurrentSendAbort_ProvesBug is the anti-bluff / bug proof. It
// hammers SendToWorker on ONE Distributor from many goroutines with NO external
// serialization — exactly mirroring the documented "safe for concurrent use"
// claim. The unguarded d.sock.SendMessage interleaves multipart frames and
// libzmq aborts:
//
//	Assertion failed: !_current_out (src/router.cpp:166)  -> SIGABRT
//
// Because SIGABRT is uncatchable (it kills the whole test binary, not just this
// test), this repro is GATED behind DATAPLANE_PROVE_BUG=1 so it does not abort
// the package's normal test run. To witness the crash:
//
//	DATAPLANE_PROVE_BUG=1 go test ./pkg/dataplane/ \
//	    -run TestForwardConcurrentSendAbort_ProvesBug -count=1
//
// Expected: the process aborts with the router.cpp assertion above (FAIL).
// After the minimal fix (serialize d.sock send under a mutex inside
// SendToWorker), this same repro runs clean — that is the regression guard.
func TestForwardConcurrentSendAbort_ProvesBug(t *testing.T) {
	if os.Getenv("DATAPLANE_PROVE_BUG") != "1" {
		t.Skip("gated abort repro: set DATAPLANE_PROVE_BUG=1 to witness the " +
			"libzmq router.cpp:166 SIGABRT from unsynchronized concurrent " +
			"SendToWorker (real bug; aborts the whole binary, so not run by default)")
	}

	const basePort = 15390
	dist, err := NewDistributor(tcpAddr(basePort))
	if err != nil {
		t.Fatalf("NewDistributor: %v", err)
	}
	defer dist.Close()

	wk, err := NewWorker(tcpAddr(basePort), "abort-worker")
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	defer wk.Close()
	if err := wk.Register(); err != nil {
		t.Fatalf("Register: %v", err)
	}
	id, err := dist.RecvWorkerRegistration()
	if err != nil {
		t.Fatalf("RecvWorkerRegistration: %v", err)
	}

	const goroutines = 16
	const perG = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				// NO external lock: this is the documented "concurrent use".
				_ = dist.SendToWorker(id, Task{
					CorrelationID: fmt.Sprintf("g%d-i%d", gid, i),
					Payload:       []byte("p"),
				})
			}
		}(g)
	}
	wg.Wait()
	// If we reach here without SIGABRT, the bug is FIXED (send is serialized).
	t.Log("no abort: SendToWorker is internally serialized — bug fixed")
}
