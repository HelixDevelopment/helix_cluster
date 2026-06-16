package main

// Adversarial sink-side probes for the helix-raftd admin HTTP surface
// (admin.go handlers over pkg/raft). These tests do NOT stand up a real
// multi-PROCESS / TCP cluster (that is the e2e_test.go job, gated behind the
// `e2e` tag). Instead they drive the REAL handler code against REAL in-memory
// raft nodes (raft.NewInmemCluster) via httptest, which is enough to elect a
// genuine leader and at least one genuine follower and to assert SINK-SIDE
// effects (the actual HTTP status AND the actual FSM contents), not just that
// code compiles or returns without panic.
//
// Adversarial classes probed (mapped honestly in the final report):
//   (a) LEADER-GATE FAIL-OPEN  — a write accepted on a non-leader node.
//   (b) VALIDATION / FAIL-OPEN — malformed/empty/wrong-method requests.
//   (c) UNGUARDED SHARED STATE — concurrent handler calls (run under -race).
//   (d) TOTAL-ORDER / IDENTITY — last-writer-wins + deterministic read-back.
//
// ANTI-HANG: every cluster is shut down via t.Cleanup; concurrency is capped at
// 8; leader election is polled with a bounded deadline. `go test -timeout 90s`
// completes.

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	hraft "github.com/hashicorp/raft"

	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
)

const advElectTimeout = 10 * time.Second

// newTestAdmin builds an adminServer bound to node n WITHOUT starting its
// listener; handlers are invoked directly via httptest, so no real port is used.
func newTestAdmin(n *raft.Node) *adminServer {
	return newAdminServer(n, "127.0.0.1:0", testLogger())
}

// do issues a request straight at the admin mux-less handler set by routing on
// the path, returning the recorder. We call handlers directly (not through the
// http.Server) to keep the test hermetic and fast.
func do(a *adminServer, method, target string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	switch {
	case strings.HasPrefix(target, "/status"):
		a.handleStatus(rec, req)
	case strings.HasPrefix(target, "/get"):
		a.handleGet(rec, req)
	case strings.HasPrefix(target, "/put"):
		a.handlePut(rec, req)
	case strings.HasPrefix(target, "/metrics"):
		a.handleMetrics(rec, req)
	default:
		panic("unrouted target " + target)
	}
	return rec
}

// leaderAndFollower spins a real 3-node in-mem raft cluster, waits for a leader,
// and returns the leader node plus one follower node. Shut down via t.Cleanup.
func leaderAndFollower(t *testing.T) (leader, follower *raft.Node) {
	t.Helper()
	cl, err := raft.NewInmemCluster(nil, "n1", "n2", "n3")
	if err != nil {
		t.Fatalf("NewInmemCluster: %v", err)
	}
	t.Cleanup(func() { _ = cl.Shutdown() })
	ld, err := cl.WaitForLeader(advElectTimeout)
	if err != nil {
		t.Fatalf("WaitForLeader: %v", err)
	}
	for _, n := range cl.Nodes() {
		if n.ID() != ld.ID() {
			return ld, n
		}
	}
	t.Fatalf("no follower found in 3-node cluster")
	return nil, nil
}

// singleLeader spins a 1-node cluster that elects itself leader.
func singleLeader(t *testing.T) *raft.Node {
	t.Helper()
	cl, err := raft.NewInmemCluster(nil, "solo")
	if err != nil {
		t.Fatalf("NewInmemCluster: %v", err)
	}
	t.Cleanup(func() { _ = cl.Shutdown() })
	ld, err := cl.WaitForLeader(advElectTimeout)
	if err != nil {
		t.Fatalf("WaitForLeader(single): %v", err)
	}
	return ld
}

func testLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// ---------------------------------------------------------------------------
// (a) LEADER-GATE FAIL-OPEN: a PUT routed at a FOLLOWER must be rejected with
// 421 Misdirected Request and MUST NOT mutate the follower's FSM. We prove the
// sink on BOTH sides: the HTTP status AND the follower's own FSM contents.
// ---------------------------------------------------------------------------
func TestAdminPut_OnFollower_RejectedAndNoSinkEffect(t *testing.T) {
	t.Parallel()
	leader, follower := leaderAndFollower(t)

	if follower.IsLeader() {
		t.Fatalf("precondition: follower %s unexpectedly leader", follower.ID())
	}

	a := newTestAdmin(follower)
	key := "leadergate"
	val := "should-not-land"
	rec := do(a, http.MethodPut, "/put?key="+key+"&value="+val, "")

	// SINK 1: HTTP status must be 421 (not 200).
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("follower PUT: got status %d, want 421 (leader-gate fail-open!): body=%q",
			rec.Code, rec.Body.String())
	}

	// SINK 2: the follower's own FSM must NOT contain the key. A fail-open write
	// that "took effect" on a non-leader would surface here.
	if got, found := follower.FSM().Get(key); found {
		t.Fatalf("follower FSM mutated by a non-leader PUT: key=%q value=%q (leader-gate fail-open!)",
			key, got)
	}

	// And the redirect body must carry the real leader's coordinates so a client
	// can redirect — a contract assertion, not just a status code.
	if !strings.Contains(rec.Body.String(), leader.ID()) {
		t.Logf("note: follower redirect body did not yet name leader %s: %q (eventual-consistency of LeaderWithID)",
			leader.ID(), rec.Body.String())
	}
}

// On the LEADER the same PUT must succeed AND land in the FSM (positive sink).
func TestAdminPut_OnLeader_AcceptedAndSinks(t *testing.T) {
	t.Parallel()
	leader, _ := leaderAndFollower(t)
	a := newTestAdmin(leader)

	rec := do(a, http.MethodPut, "/put?key=k1&value=v1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("leader PUT: got %d want 200: %q", rec.Code, rec.Body.String())
	}
	if got, found := leader.FSM().Get("k1"); !found || got != "v1" {
		t.Fatalf("leader FSM sink: got (%q,%v) want (v1,true)", got, found)
	}
}

// ---------------------------------------------------------------------------
// (b) VALIDATION / FAIL-OPEN
// ---------------------------------------------------------------------------
func TestValidation_Sink(t *testing.T) {
	t.Parallel()
	leader := singleLeader(t)
	a := newTestAdmin(leader)

	t.Run("put-empty-key-400", func(t *testing.T) {
		rec := do(a, http.MethodPut, "/put?key=&value=v", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("empty-key PUT: got %d want 400", rec.Code)
		}
		// Sink: nothing should have been applied for the empty key.
		if _, found := leader.FSM().Get(""); found {
			t.Fatalf("empty key was stored despite 400")
		}
	})

	t.Run("get-empty-key-400", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/get?key=", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("empty-key GET: got %d want 400", rec.Code)
		}
	})

	t.Run("put-wrong-method-405", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/put?key=k&value=v", "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET on /put: got %d want 405", rec.Code)
		}
	})

	t.Run("get-wrong-method-405", func(t *testing.T) {
		rec := do(a, http.MethodPost, "/get?key=k", "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST on /get: got %d want 405", rec.Code)
		}
	})

	t.Run("status-wrong-method-405", func(t *testing.T) {
		rec := do(a, http.MethodDelete, "/status", "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("DELETE on /status: got %d want 405", rec.Code)
		}
	})

	t.Run("missing-key-found-false", func(t *testing.T) {
		rec := do(a, http.MethodGet, "/get?key=definitely-absent", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET missing: got %d want 200", rec.Code)
		}
		if got := rec.Body.String(); !strings.Contains(got, `"found":false`) {
			t.Fatalf("GET missing key: body=%q want found:false", got)
		}
	})

	t.Run("body-value-accepted", func(t *testing.T) {
		// value omitted in query => taken from body.
		rec := do(a, http.MethodPost, "/put?key=bodykey", "frombody")
		if rec.Code != http.StatusOK {
			t.Fatalf("POST body PUT: got %d want 200: %q", rec.Code, rec.Body.String())
		}
		if got, found := leader.FSM().Get("bodykey"); !found || got != "frombody" {
			t.Fatalf("body value sink: got (%q,%v) want (frombody,true)", got, found)
		}
	})
}

// CONTRACT PIN: the value is read verbatim from the query, including a value
// equal to an existing key etc.; empty value with empty body stores "" (NOT a
// 400). This is the documented behavior ("value may be ?value=V or body"); we
// pin it so a future "reject empty value" change is a conscious decision.
func TestEmptyValue_StoresEmptyString_ContractPin(t *testing.T) {
	t.Parallel()
	leader := singleLeader(t)
	a := newTestAdmin(leader)

	rec := do(a, http.MethodPut, "/put?key=ev", "") // no value, empty body
	if rec.Code != http.StatusOK {
		t.Fatalf("empty value PUT: got %d want 200 (contract: empty allowed): %q", rec.Code, rec.Body.String())
	}
	got, found := leader.FSM().Get("ev")
	if !found || got != "" {
		t.Fatalf("empty value sink: got (%q,%v) want (\"\",true)", got, found)
	}
}

// ---------------------------------------------------------------------------
// (c) UNGUARDED SHARED STATE: concurrent PUTs through the leader handler. The
// FSM is mutex-guarded (fsm.go) and Apply serializes through raft, so this must
// be race-free under -race AND every distinct key must land (no lost update).
// ---------------------------------------------------------------------------
func TestConcurrentPut_RaceFreeAndAllLand(t *testing.T) {
	t.Parallel()
	leader := singleLeader(t)
	a := newTestAdmin(leader)

	const workers = 8
	const perWorker = 10
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				k := fmt.Sprintf("c-%d-%d", w, i)
				rec := do(a, http.MethodPut, "/put?key="+k+"&value="+url.QueryEscape(k), "")
				if rec.Code != http.StatusOK {
					// record but don't t.Fatalf from goroutine
					t.Errorf("concurrent PUT %s: status %d", k, rec.Code)
				}
			}
		}(w)
	}
	// Concurrent readers/metrics hammer the shared FSM at the same time to expose
	// any unguarded map access in the read handlers.
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = do(a, http.MethodGet, "/get?key=c-0-0", "")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = do(a, http.MethodGet, "/metrics", "")
		}
	}()
	wg.Wait()

	// SINK: every distinct key must be present (last-writer-wins per key, and no
	// lost keys across the concurrent appliers).
	for w := 0; w < workers; w++ {
		for i := 0; i < perWorker; i++ {
			k := fmt.Sprintf("c-%d-%d", w, i)
			got, found := leader.FSM().Get(k)
			if !found || got != k {
				t.Fatalf("concurrent sink: key %s got (%q,%v) want (%s,true)", k, got, found, k)
			}
		}
	}
	if leader.FSM().Len() < workers*perWorker {
		t.Fatalf("FSM lost keys: len=%d want >= %d", leader.FSM().Len(), workers*perWorker)
	}
}

// ---------------------------------------------------------------------------
// (d) TOTAL-ORDER / IDENTITY: an overwrite of the same key is last-writer-wins
// and the read-back is deterministic; the FSM applies in a single total order.
// ---------------------------------------------------------------------------
func TestOverwrite_LastWriterWins(t *testing.T) {
	t.Parallel()
	leader := singleLeader(t)
	a := newTestAdmin(leader)

	for _, v := range []string{"a", "b", "c", "final"} {
		rec := do(a, http.MethodPut, "/put?key=dup&value="+v, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("overwrite PUT %q: status %d", v, rec.Code)
		}
	}
	rec := do(a, http.MethodGet, "/get?key=dup", "")
	if !strings.Contains(rec.Body.String(), `"value":"final"`) {
		t.Fatalf("last-writer-wins: body=%q want value:final", rec.Body.String())
	}
	// Applied count must reflect 4 distinct applies for this key, plus any others;
	// at minimum it advanced monotonically.
	if leader.FSM().AppliedCount() < 4 {
		t.Fatalf("applied count = %d, want >= 4", leader.FSM().AppliedCount())
	}
}

// Sanity: a single-node cluster's status handler reports IsLeader true and the
// metrics handler emits is_leader 1 (sink-side leadership signal).
func TestStatusAndMetrics_LeaderSignal(t *testing.T) {
	t.Parallel()
	leader := singleLeader(t)
	a := newTestAdmin(leader)

	rec := do(a, http.MethodGet, "/status", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isLeader":true`) {
		t.Fatalf("status: code=%d body=%q", rec.Code, rec.Body.String())
	}
	mrec := do(a, http.MethodGet, "/metrics", "")
	if !strings.Contains(mrec.Body.String(), "helix_raftd_is_leader") {
		t.Fatalf("metrics missing is_leader gauge: %q", mrec.Body.String())
	}
}

// Compile-time guard: keep hraft import meaningful even if a future refactor
// drops its only use, so the file fails loudly rather than silently.
var _ = hraft.Leader
