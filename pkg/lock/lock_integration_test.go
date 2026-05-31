//go:build integration

package lock

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"

	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
)

// sharedEndpoint is the client endpoint of the ONE real ephemeral etcd booted
// for the whole package run. A single container (rather than one per test)
// honors the "one etcd at a time" constraint, guarantees teardown via TestMain
// even if a test fails, and minimizes host memory pressure per §12. Tests
// isolate themselves with unique lock-key prefixes.
var sharedEndpoint string

// TestMain boots a single REAL etcd via brokertest, runs every integration test
// against it, then tears the container down. No mocks — every lock operation in
// this file traverses a real etcd server.
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

// newLocker builds an EtcdLocker (and returns its raw etcd Client for sink-side
// key inspection) wired to the shared live etcd endpoint.
func newLocker(t *testing.T) (*EtcdLocker, *etcd.Client) {
	t.Helper()
	c, err := etcd.New(etcd.Config{
		Endpoints:   []string{sharedEndpoint},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd.New(%q): %v", sharedEndpoint, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return NewEtcdLocker(c), c
}

// TestEtcdLocker_AcquireBlockRelease proves the core distributed-lock contract
// against a REAL etcd: A acquires, B blocks while A holds, A releases, then B
// acquires. Verified by timing — B cannot complete acquisition until A unlocks.
func TestEtcdLocker_AcquireBlockRelease(t *testing.T) {
	locker, _ := newLocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const key = "/helix/lock/abr"

	unlockA, err := locker.Lock(ctx, key)
	if err != nil {
		t.Fatalf("A Lock: %v", err)
	}

	bAcquired := make(chan time.Time, 1)
	go func() {
		unlockB, err := locker.Lock(ctx, key)
		if err != nil {
			t.Errorf("B Lock: %v", err)
			close(bAcquired)
			return
		}
		bAcquired <- time.Now()
		_ = unlockB()
	}()

	time.Sleep(1 * time.Second)
	select {
	case <-bAcquired:
		t.Fatal("B acquired while A still held the lock (no mutual exclusion)")
	default:
	}

	releaseTime := time.Now()
	if err := unlockA(); err != nil {
		t.Fatalf("A Unlock: %v", err)
	}

	select {
	case acqTime, ok := <-bAcquired:
		if !ok {
			t.Fatal("B failed to acquire after A released")
		}
		if acqTime.Before(releaseTime) {
			t.Fatal("B acquired before A released (mutex violation)")
		}
		t.Logf("real EtcdLocker: B acquired %v after A released", acqTime.Sub(releaseTime))
	case <-time.After(15 * time.Second):
		t.Fatal("B never acquired after A released")
	}
}

// TestEtcdLocker_KeyPresentWhileHeldGoneAfterRelease proves sink-side state: the
// distributed lock materializes a key under the lock prefix in REAL etcd while
// held, and that key is gone after release.
func TestEtcdLocker_KeyPresentWhileHeldGoneAfterRelease(t *testing.T) {
	locker, raw := newLocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const key = "/helix/lock/keycheck"

	unlock, err := locker.Lock(ctx, key)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// The etcd concurrency mutex writes an ephemeral key under the lock prefix.
	held, err := raw.GetPrefix(ctx, key)
	if err != nil {
		_ = unlock()
		t.Fatalf("GetPrefix(while held): %v", err)
	}
	if len(held) == 0 {
		_ = unlock()
		t.Fatal("no lock key present in etcd while the lock is held")
	}
	t.Logf("real etcd: %d lock key(s) present under %s while held", len(held), key)

	if err := unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	after, err := raw.GetPrefix(ctx, key)
	if err != nil {
		t.Fatalf("GetPrefix(after release): %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("lock key(s) still present after release: %v", after)
	}
	t.Log("real etcd: lock key gone after release")
}

// TestEtcdLocker_NoLostUpdatesAcrossClients proves real mutual exclusion at the
// data level across SEPARATE etcd clients (distinct concurrency sessions, as
// separate processes/nodes would have): each worker uses its own client+locker,
// increments a shared in-process counter under the same distributed lock key,
// and the final value shows zero lost updates — against a REAL etcd.
func TestEtcdLocker_NoLostUpdatesAcrossClients(t *testing.T) {
	const key = "/helix/lock/multiclient-counter"
	const workers = 6

	var counter int
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker gets its OWN etcd client and EtcdLocker — distinct
			// concurrency sessions, exactly as separate processes/nodes would.
			c, err := etcd.New(etcd.Config{
				Endpoints:   []string{sharedEndpoint},
				DialTimeout: 10 * time.Second,
			})
			if err != nil {
				t.Errorf("worker etcd.New: %v", err)
				return
			}
			defer c.Close()
			locker := NewEtcdLocker(c)

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			unlock, err := locker.Lock(ctx, key)
			if err != nil {
				t.Errorf("worker Lock: %v", err)
				return
			}
			// Non-atomic read-modify-write; correct only under real mutual exclusion.
			v := counter
			time.Sleep(10 * time.Millisecond)
			counter = v + 1
			if err := unlock(); err != nil {
				t.Errorf("worker Unlock: %v", err)
			}
		}()
	}
	wg.Wait()

	if counter != workers {
		t.Fatalf("shared counter = %d, want %d (lost updates => distributed lock not mutually exclusive)", counter, workers)
	}
	t.Logf("real EtcdLocker across %d independent clients: counter=%d with no lost updates", workers, counter)
}

// TestEtcdLocker_ReleaseAllowsReacquire proves the post-release path: after a
// holder releases, a subsequent Lock on the same key succeeds promptly against a
// REAL etcd (no stale lock left behind).
func TestEtcdLocker_ReleaseAllowsReacquire(t *testing.T) {
	locker, _ := newLocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const key = "/helix/lock/reacquire"

	unlock1, err := locker.Lock(ctx, key)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	if err := unlock1(); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		unlock2, err := locker.Lock(ctx, key)
		if err != nil {
			done <- err
			return
		}
		done <- unlock2()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("re-acquire after release: %v", err)
		}
		t.Log("real EtcdLocker: re-acquired promptly after release")
	case <-time.After(10 * time.Second):
		t.Fatal("re-acquire blocked after release (stale lock left behind)")
	}
}
