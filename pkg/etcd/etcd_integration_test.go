//go:build integration

package etcd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
)

// sharedEndpoint is the client endpoint of the ONE real ephemeral etcd booted
// for the whole package run. Booting a single container (rather than one per
// test) honors the "one etcd at a time" constraint, guarantees teardown via
// TestMain even if a test fails, and keeps container churn (and host memory
// pressure per §12) minimal. Tests isolate themselves with unique key prefixes.
var sharedEndpoint string

// TestMain boots a single REAL etcd via brokertest, runs every integration
// test against it, then tears the container down. No mocks — every operation in
// this file traverses a real etcd server over the network.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	endpoint, stop, err := brokertest.StartEtcd(ctx)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "brokertest.StartEtcd: %v\n", err)
		os.Exit(1)
	}
	sharedEndpoint = endpoint
	fmt.Fprintf(os.Stderr, "real etcd broker up at %s\n", endpoint)

	code := m.Run()

	stop()
	cancel()
	os.Exit(code)
}

// newClient builds a Client of THIS package's own exported API wired to the
// shared live etcd endpoint.
func newClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		Endpoints:   []string{sharedEndpoint},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New(%q): %v", sharedEndpoint, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestEtcd_PutGetRoundTrip is the CLAUDE-1 sink-side proof for the core KV path:
// a value written via Put is read back verbatim via Get against a REAL etcd.
func TestEtcd_PutGetRoundTrip(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const key = "/helix/test/kv1"
	const val = "hello-etcd"

	if err := c.Put(ctx, key, val); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok=false for a key that was just Put")
	}
	if got != val {
		t.Fatalf("Get value: got %q want %q", got, val)
	}
	t.Logf("real Put/Get round-trip: key=%s value=%s", key, got)
}

// TestEtcd_GetMissingKey proves a Get on an absent key returns ok=false with no
// error — sink-side absence, not a fabricated value.
func TestEtcd_GetMissingKey(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	got, ok, err := c.Get(ctx, "/helix/test/does-not-exist")
	if err != nil {
		t.Fatalf("Get(missing): %v", err)
	}
	if ok {
		t.Fatalf("Get(missing): ok=true, want false (value=%q)", got)
	}
	if got != "" {
		t.Fatalf("Get(missing): value=%q, want empty", got)
	}
	t.Log("real etcd: missing key correctly returns ok=false, empty value")
}

// TestEtcd_GetPrefixMultiKey proves GetPrefix returns exactly the keys under the
// prefix and excludes keys outside it, against a REAL etcd.
func TestEtcd_GetPrefixMultiKey(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	want := map[string]string{
		"/helix/svc/a": "1",
		"/helix/svc/b": "2",
		"/helix/svc/c": "3",
	}
	for k, v := range want {
		if err := c.Put(ctx, k, v); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	// A key OUTSIDE the prefix must not be returned.
	if err := c.Put(ctx, "/helix/other/z", "99"); err != nil {
		t.Fatalf("Put(other): %v", err)
	}

	got, err := c.GetPrefix(ctx, "/helix/svc/")
	if err != nil {
		t.Fatalf("GetPrefix: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("GetPrefix returned %d keys, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("GetPrefix[%s]: got %q want %q", k, got[k], v)
		}
	}
	if _, leaked := got["/helix/other/z"]; leaked {
		t.Error("GetPrefix leaked a key outside the prefix")
	}
	t.Logf("real etcd GetPrefix returned %d keys under /helix/svc/", len(got))
}

// TestEtcd_DeleteSinkSideAbsence proves Delete removes a single key (sink-side
// re-read shows ok=false) against a REAL etcd.
func TestEtcd_DeleteSinkSideAbsence(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const key = "/helix/test/to-delete"
	if err := c.Put(ctx, key, "doomed"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok, _ := c.Get(ctx, key); !ok {
		t.Fatal("precondition: key should exist after Put")
	}

	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if ok {
		t.Fatal("key still present after Delete (sink-side absence not proven)")
	}
	t.Log("real etcd: Delete removed key; sink-side re-read confirms absence")
}

// TestEtcd_DeletePrefixSinkSideAbsence proves DeletePrefix removes every key
// under a prefix while leaving keys outside it untouched, against a REAL etcd.
func TestEtcd_DeletePrefixSinkSideAbsence(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, k := range []string{"/helix/del/a", "/helix/del/b", "/helix/del/c"} {
		if err := c.Put(ctx, k, "x"); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	// A key outside the prefix that must survive.
	const survivor = "/helix/keep/z"
	if err := c.Put(ctx, survivor, "alive"); err != nil {
		t.Fatalf("Put(survivor): %v", err)
	}

	if err := c.DeletePrefix(ctx, "/helix/del/"); err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}

	remaining, err := c.GetPrefix(ctx, "/helix/del/")
	if err != nil {
		t.Fatalf("GetPrefix after DeletePrefix: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("DeletePrefix left %d keys behind: %v", len(remaining), remaining)
	}
	if _, ok, _ := c.Get(ctx, survivor); !ok {
		t.Fatal("DeletePrefix wrongly removed a key outside the prefix")
	}
	t.Log("real etcd: DeletePrefix cleared the subtree; out-of-prefix key survived")
}

// TestEtcd_WatchDeliversEvent proves Watch delivers a real PUT event carrying
// the correct Type/Key/Value, and that cancelling the context closes the
// channel — both verified against a REAL etcd.
func TestEtcd_WatchDeliversEvent(t *testing.T) {
	c := newClient(t)

	const key = "/helix/test/watched"

	watchCtx, watchCancel := context.WithCancel(context.Background())
	ch := c.Watch(watchCtx, key)

	// Give the watcher a moment to attach before mutating the key.
	time.Sleep(500 * time.Millisecond)

	putCtx, putCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer putCancel()
	if err := c.Put(putCtx, key, "watched-value"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("watch channel closed before delivering the PUT event")
		}
		if ev.Err != nil {
			t.Fatalf("watch event carried error: %v", ev.Err)
		}
		if ev.Type != "PUT" {
			t.Errorf("watch event Type: got %q want PUT", ev.Type)
		}
		if ev.Key != key {
			t.Errorf("watch event Key: got %q want %q", ev.Key, key)
		}
		if ev.Value != "watched-value" {
			t.Errorf("watch event Value: got %q want %q", ev.Value, "watched-value")
		}
		t.Logf("real etcd watch delivered: type=%s key=%s value=%s", ev.Type, ev.Key, ev.Value)
	case <-time.After(15 * time.Second):
		watchCancel()
		t.Fatal("timed out waiting for real etcd watch event")
	}

	// Cancelling the watch context MUST close the channel (drain remaining
	// events, then expect closure).
	watchCancel()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Log("real etcd watch channel closed after ctx cancel")
				return
			}
			// ignore any trailing event, keep waiting for closure
		case <-deadline:
			t.Fatal("watch channel did not close after context cancel")
		}
	}
}

// TestEtcd_LeaseExpiresKey proves a key written under a short TTL lease is
// present while the lease is alive and absent after it expires — real lease
// semantics against a REAL etcd.
func TestEtcd_LeaseExpiresKey(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	leaseID, err := c.Lease(ctx, 2) // 2-second TTL
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if leaseID == 0 {
		t.Fatal("Lease returned zero LeaseID")
	}

	const key = "/helix/test/leased"
	if err := c.PutWithLease(ctx, key, "ephemeral", leaseID); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}

	// While the lease is alive the key must be present.
	if _, ok, _ := c.Get(ctx, key); !ok {
		t.Fatal("leased key absent while lease should still be alive")
	}
	t.Logf("real etcd lease %x granted; leased key present", leaseID)

	// After the TTL elapses (plus margin) the key must be gone.
	time.Sleep(4 * time.Second)
	_, ok, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after lease expiry: %v", err)
	}
	if ok {
		t.Fatal("leased key still present after TTL expiry (lease not honored)")
	}
	t.Log("real etcd: leased key removed after TTL expiry")
}

// TestEtcd_RevokeLeaseDeletesKey proves RevokeLease immediately deletes the keys
// attached to it, against a REAL etcd.
func TestEtcd_RevokeLeaseDeletesKey(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	leaseID, err := c.Lease(ctx, 100) // long TTL; we revoke explicitly
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}

	const key = "/helix/test/revoke"
	if err := c.PutWithLease(ctx, key, "v", leaseID); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if _, ok, _ := c.Get(ctx, key); !ok {
		t.Fatal("precondition: leased key should exist before revoke")
	}

	if err := c.RevokeLease(ctx, leaseID); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}

	_, ok, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after revoke: %v", err)
	}
	if ok {
		t.Fatal("key still present after RevokeLease")
	}
	t.Log("real etcd: RevokeLease deleted the attached key")
}

// TestEtcd_LockMutualExclusion proves the distributed Lock provides real mutual
// exclusion: holder A blocks holder B until A unlocks. Verified by timing — B's
// acquisition cannot complete until after A releases — against a REAL etcd.
func TestEtcd_LockMutualExclusion(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const key = "/helix/test/lock"

	// A acquires the lock.
	unlockA, err := c.Lock(ctx, key)
	if err != nil {
		t.Fatalf("A Lock: %v", err)
	}

	bAcquired := make(chan time.Time, 1)
	go func() {
		// B tries to acquire the SAME lock; it must block until A unlocks.
		unlockB, err := c.Lock(ctx, key)
		if err != nil {
			t.Errorf("B Lock: %v", err)
			close(bAcquired)
			return
		}
		bAcquired <- time.Now()
		_ = unlockB()
	}()

	// Give B time to start blocking, then confirm it has NOT acquired.
	time.Sleep(1 * time.Second)
	select {
	case <-bAcquired:
		t.Fatal("B acquired the lock while A still held it (no mutual exclusion)")
	default:
		// expected: B is still blocked
	}

	releaseTime := time.Now()
	if err := unlockA(); err != nil {
		t.Fatalf("A Unlock: %v", err)
	}

	select {
	case acqTime, ok := <-bAcquired:
		if !ok {
			t.Fatal("B failed to acquire the lock after A released")
		}
		if acqTime.Before(releaseTime) {
			t.Fatal("B acquired before A released (clock-impossible without mutex violation)")
		}
		t.Logf("real etcd lock: B acquired %v after A released", acqTime.Sub(releaseTime))
	case <-time.After(15 * time.Second):
		t.Fatal("B never acquired the lock after A released")
	}
}

// TestEtcd_LockSerializesSharedCounter proves real mutual exclusion at the data
// level: many goroutines increment a shared counter under the distributed lock
// and the final value shows zero lost updates — against a REAL etcd.
func TestEtcd_LockSerializesSharedCounter(t *testing.T) {
	c := newClient(t)

	const key = "/helix/test/counter-lock"
	const workers = 8

	var counter int
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			unlock, err := c.Lock(ctx, key)
			if err != nil {
				t.Errorf("worker Lock: %v", err)
				return
			}
			// Non-atomic read-modify-write; correct only under real mutual exclusion.
			v := counter
			time.Sleep(5 * time.Millisecond)
			counter = v + 1
			if err := unlock(); err != nil {
				t.Errorf("worker Unlock: %v", err)
			}
		}()
	}
	wg.Wait()

	if counter != workers {
		t.Fatalf("shared counter = %d, want %d (lost updates => lock not mutually exclusive)", counter, workers)
	}
	t.Logf("real etcd lock serialized %d workers; counter=%d with no lost updates", workers, counter)
}
