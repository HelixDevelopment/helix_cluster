//go:build integration

package leader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// sharedEndpoint is the client endpoint of the single real ephemeral etcd
// started for the whole package integration run.
var sharedEndpoint string

// TestMain boots a single REAL etcd via brokertest, runs every integration
// test against it, then tears the container down.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	endpoint, stop, err := brokertest.StartEtcd(ctx)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "brokertest.StartEtcd: %v\n", err)
		os.Exit(1)
	}
	sharedEndpoint = endpoint
	fmt.Fprintf(os.Stderr, "real etcd up at %s\n", endpoint)

	code := m.Run()
	stop()
	cancel()
	os.Exit(code)
}

// newCli creates a real etcd client connected to sharedEndpoint.
func newCli(t *testing.T) *clientv3.Client {
	t.Helper()
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{sharedEndpoint},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("clientv3.New(%q): %v", sharedEndpoint, err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// newRealElection creates an EtcdElection from a real etcd client.
func newRealElection(t *testing.T, localID, elecKey string, cli *clientv3.Client) *EtcdElection {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ee, err := NewEtcdElectionFromClient(ctx, cli, EtcdElectionConfig{
		LocalID:     localID,
		ElectionKey: elecKey,
	}, 5 /* TTL seconds */)
	if err != nil {
		t.Fatalf("NewEtcdElectionFromClient(%q): %v", localID, err)
	}
	t.Cleanup(func() { _ = ee.Close() })
	return ee
}

// TestEtcdElection_ExactlyOneLeaderAmongCandidates: two candidates campaign
// concurrently against REAL etcd — exactly one wins; the other blocks until
// the first resigns, then wins with a strictly higher fencing epoch.
//
// The per-run UUID is embedded in the leader value; we read it back from etcd
// via QueryLeader to prove the value actually landed in the real KV store.
func TestEtcdElection_ExactlyOneLeaderAmongCandidates(t *testing.T) {
	runID := uuid.New().String()
	elecKey := fmt.Sprintf("/helix/inttest/leader/%s", runID)

	cliA := newCli(t)
	cliB := newCli(t)

	eeA := newRealElection(t, "node-a", elecKey, cliA)
	eeB := newRealElection(t, "node-b", elecKey, cliB)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Campaign A in foreground — with no other holder, wins immediately.
	tokA, err := eeA.Campaign(ctx, runID)
	if err != nil {
		t.Fatalf("A Campaign: %v", err)
	}
	if !eeA.IsLeader() {
		t.Fatal("node-a must be leader after Campaign")
	}
	if tokA.Epoch == 0 {
		t.Fatal("A fencing epoch must be non-zero")
	}

	// --- Sink-side evidence: read the leader value back from REAL etcd ---
	lv, err := eeA.QueryLeader(ctx)
	if err != nil {
		t.Fatalf("QueryLeader while A holds: %v", err)
	}
	if lv.HolderID != "node-a" {
		t.Fatalf("etcd leader value HolderID=%q want %q", lv.HolderID, "node-a")
	}
	if lv.RunID != runID {
		t.Fatalf("etcd leader value RunID=%q want %q — per-run UUID not in sink", lv.RunID, runID)
	}
	t.Logf("sink-side: etcd leader value = %+v", lv)

	// Campaign B in background — must block while A holds.
	bDone := make(chan FencingToken, 1)
	bErrCh := make(chan error, 1)
	go func() {
		tok, err := eeB.Campaign(ctx, runID)
		if err != nil {
			bErrCh <- err
			return
		}
		bDone <- tok
	}()

	// Give B 2 s to try; it must NOT have won while A holds.
	time.Sleep(2 * time.Second)
	select {
	case tokB := <-bDone:
		t.Fatalf("B acquired while A still holds (mutual exclusion broken): tokB.Epoch=%d", tokB.Epoch)
	case err := <-bErrCh:
		t.Fatalf("B Campaign errored: %v", err)
	default:
		t.Log("real etcd: B correctly blocked while A holds the election")
	}

	// Resign A — B should win with a strictly higher fencing epoch.
	resignTime := time.Now()
	if err := eeA.Resign(ctx); err != nil {
		t.Fatalf("A Resign: %v", err)
	}

	select {
	case tokB := <-bDone:
		t.Logf("B won %v after A resigned; B.Epoch=%d A.Epoch=%d", time.Since(resignTime), tokB.Epoch, tokA.Epoch)
		if tokB.Epoch <= tokA.Epoch {
			t.Fatalf("failover epoch not monotonic: A.epoch=%d B.epoch=%d", tokA.Epoch, tokB.Epoch)
		}
		// Stale-leader detection: A's token must now be stale vs B's epoch.
		if !StaleLeader(tokA, tokB.Epoch) {
			t.Fatalf("A's fencing token (epoch=%d) should be stale vs B's epoch=%d", tokA.Epoch, tokB.Epoch)
		}
	case err := <-bErrCh:
		t.Fatalf("B Campaign failed: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("B never won after A resigned")
	}
}

// TestEtcdElection_FencingEpochIncreasesOnFailover: lease revocation (TTL
// expiry) causes a new candidate to win with a strictly higher epoch.
//
// We simulate TTL expiry by closing A's session (which revokes the lease),
// then campaign B and assert B's epoch > A's epoch.
func TestEtcdElection_FencingEpochIncreasesOnFailover(t *testing.T) {
	runID := uuid.New().String()
	elecKey := fmt.Sprintf("/helix/inttest/failover/%s", runID)

	cliA := newCli(t)
	cliB := newCli(t)

	// Use a short TTL (2 s) so the session expires quickly.
	ctxA, cancelA := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelA()

	eeA, err := NewEtcdElectionFromClient(ctxA, cliA, EtcdElectionConfig{
		LocalID:     "node-a",
		ElectionKey: elecKey,
	}, 2 /* TTL=2s */)
	if err != nil {
		t.Fatalf("NewEtcdElectionFromClient A: %v", err)
	}

	tokA, err := eeA.Campaign(ctxA, runID)
	if err != nil {
		t.Fatalf("A Campaign: %v", err)
	}
	t.Logf("A won with epoch=%d", tokA.Epoch)

	// Close A's backend session — this revokes the lease and signals failover.
	if err := eeA.Close(); err != nil {
		t.Logf("A Close (expected error on session close): %v", err)
	}

	// Give etcd a moment to process the lease revocation.
	time.Sleep(1 * time.Second)

	ctx := context.Background()
	eeB := newRealElection(t, "node-b", elecKey, cliB)

	tokB, err := eeB.Campaign(ctx, runID)
	if err != nil {
		t.Fatalf("B Campaign after A closed: %v", err)
	}
	t.Logf("B won with epoch=%d (A.epoch=%d)", tokB.Epoch, tokA.Epoch)

	if tokB.Epoch <= tokA.Epoch {
		t.Fatalf("fencing epoch must increase on failover: A.epoch=%d B.epoch=%d", tokA.Epoch, tokB.Epoch)
	}

	// Sink-side: B's run_id is in the leader value.
	lv, err := eeB.QueryLeader(ctx)
	if err != nil {
		t.Fatalf("QueryLeader after failover: %v", err)
	}
	if lv.RunID != runID {
		t.Fatalf("leader value RunID=%q want %q after failover", lv.RunID, runID)
	}
	if lv.HolderID != "node-b" {
		t.Fatalf("leader value HolderID=%q want %q after failover", lv.HolderID, "node-b")
	}
}

// TestEtcdElection_LeaderValueContainsUUID: the per-run UUID written to etcd
// by the winning Campaign is readable as JSON and contains exactly the runID
// passed to Campaign (anti-bluff evidence requirement).
func TestEtcdElection_LeaderValueContainsUUID(t *testing.T) {
	runID := uuid.New().String()
	elecKey := fmt.Sprintf("/helix/inttest/uuid/%s", runID)

	cli := newCli(t)
	ee := newRealElection(t, "node-uuid", elecKey, cli)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := ee.Campaign(ctx, runID); err != nil {
		t.Fatalf("Campaign: %v", err)
	}

	// Read the raw leader key from etcd and parse JSON.
	resp, err := cli.Get(ctx, elecKey, clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("etcd Get prefix %q: %v", elecKey, err)
	}
	if len(resp.Kvs) == 0 {
		t.Fatalf("no keys under %q in real etcd after Campaign", elecKey)
	}

	var lv LeaderValue
	if err := json.Unmarshal(resp.Kvs[0].Value, &lv); err != nil {
		t.Fatalf("parse leader value %q: %v", resp.Kvs[0].Value, err)
	}
	if lv.RunID != runID {
		t.Fatalf("leader value RunID=%q want %q (UUID not in sink)", lv.RunID, runID)
	}
	if lv.HolderID != "node-uuid" {
		t.Fatalf("leader value HolderID=%q want %q", lv.HolderID, "node-uuid")
	}
	t.Logf("sink-side leader value in real etcd: %+v", lv)
}

// TestEtcdElection_EpochDurableAcrossRestart: the epoch counter in etcd
// persists across a new session, so a new leader always gets a higher epoch
// than any previous leader in the same election key.
func TestEtcdElection_EpochDurableAcrossRestart(t *testing.T) {
	runID := uuid.New().String()
	elecKey := fmt.Sprintf("/helix/inttest/durable/%s", runID)

	// First leader wins and resigns.
	cli1 := newCli(t)
	ctx := context.Background()
	ee1 := newRealElection(t, "first", elecKey, cli1)
	tok1, err := ee1.Campaign(ctx, runID)
	if err != nil {
		t.Fatalf("first Campaign: %v", err)
	}
	if err := ee1.Resign(ctx); err != nil {
		t.Fatalf("first Resign: %v", err)
	}

	// Second leader (new session, new client) — epoch must be > first.
	cli2 := newCli(t)
	ee2 := newRealElection(t, "second", elecKey, cli2)
	tok2, err := ee2.Campaign(ctx, runID)
	if err != nil {
		t.Fatalf("second Campaign: %v", err)
	}
	if tok2.Epoch <= tok1.Epoch {
		t.Fatalf("durable epoch must increase: first.epoch=%d second.epoch=%d", tok1.Epoch, tok2.Epoch)
	}
	t.Logf("durable epoch across restart: first=%d second=%d", tok1.Epoch, tok2.Epoch)
}

// TestEtcdElection_StaleLeaderRejectedByConsumer: a consumer that tracks the
// highest fencing epoch it has seen correctly rejects a stale (former) leader.
func TestEtcdElection_StaleLeaderRejectedByConsumer(t *testing.T) {
	runID := uuid.New().String()
	elecKey := fmt.Sprintf("/helix/inttest/stale/%s", runID)

	cli := newCli(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var mu sync.Mutex
	highestEpoch := uint64(0)

	recordEpoch := func(tok FencingToken) {
		mu.Lock()
		defer mu.Unlock()
		if tok.Epoch > highestEpoch {
			highestEpoch = tok.Epoch
		}
	}
	isStaleEpoch := func(tok FencingToken) bool {
		mu.Lock()
		defer mu.Unlock()
		return StaleLeader(tok, highestEpoch)
	}

	// Round 1: first leader.
	ee1 := newRealElection(t, "round1", elecKey, cli)
	tok1, err := ee1.Campaign(ctx, runID)
	if err != nil {
		t.Fatalf("round1 Campaign: %v", err)
	}
	recordEpoch(tok1)
	if err := ee1.Resign(ctx); err != nil {
		t.Fatalf("round1 Resign: %v", err)
	}

	// Round 2: second leader — consumer sees new highest epoch.
	cli2 := newCli(t)
	ee2 := newRealElection(t, "round2", elecKey, cli2)
	tok2, err := ee2.Campaign(ctx, runID)
	if err != nil {
		t.Fatalf("round2 Campaign: %v", err)
	}
	recordEpoch(tok2)

	// Now tok1 must be stale, tok2 must not.
	if !isStaleEpoch(tok1) {
		t.Fatalf("tok1 (epoch=%d) should be stale vs highest=%d", tok1.Epoch, highestEpoch)
	}
	if isStaleEpoch(tok2) {
		t.Fatalf("tok2 (epoch=%d) must NOT be stale vs highest=%d", tok2.Epoch, highestEpoch)
	}
	t.Logf("consumer correctly rejects stale leader: tok1.epoch=%d tok2.epoch=%d highest=%d",
		tok1.Epoch, tok2.Epoch, highestEpoch)
}
