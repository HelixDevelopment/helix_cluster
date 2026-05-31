package device

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPool_HandsOutExactlyNThenErrors is the core cap proof: a pool of size N
// hands out exactly N devices, the N+1th Acquire errors, and Available reaches 0.
//
// Mutation that kills it: make reserve() always return true (ignore the cap) ->
// the N+1th Acquire succeeds, Available goes negative, both assertions fail.
func TestPool_HandsOutExactlyNThenErrors(t *testing.T) {
	const N = 3
	prov := NewSoftwareProvisioner(100)
	pool, err := NewPool(prov, N)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	if pool.Available() != N {
		t.Fatalf("initial available = %d, want %d", pool.Available(), N)
	}

	var devs []*Device
	for i := 0; i < N; i++ {
		d, err := pool.Acquire(context.Background(), DeviceSpec{Tier: "T3"})
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		devs = append(devs, d)
		if want := N - (i + 1); pool.Available() != want {
			t.Errorf("after acquire %d available = %d, want %d", i, pool.Available(), want)
		}
	}
	if pool.Available() != 0 {
		t.Fatalf("available after N acquires = %d, want 0", pool.Available())
	}
	if pool.InUse() != N {
		t.Fatalf("in-use after N acquires = %d, want %d", pool.InUse(), N)
	}

	// N+1th must error.
	d, err := pool.Acquire(context.Background(), DeviceSpec{Tier: "T3"})
	if err == nil {
		t.Fatalf("acquire %d succeeded (device %v), want ErrPoolExhausted", N+1, d)
	}
	if !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("err = %v, want ErrPoolExhausted", err)
	}
	if pool.Available() != 0 {
		t.Errorf("available stayed %d after rejected acquire, want 0", pool.Available())
	}

	// Each provisioned device must be checked out as InUse (Claim ran).
	for i, dv := range devs {
		if dv.State() != StateInUse {
			t.Errorf("device %d state = %s, want InUse", i, dv.State())
		}
	}
}

// TestPool_ReleaseFreesSlot asserts Release returns the slot and restores the
// available count so a subsequent Acquire succeeds.
//
// Mutation that kills it: in Pool.Release, return nil without delete(p.live,...)
// -> the slot is never freed, Available stays 0, and the re-acquire errors.
func TestPool_ReleaseFreesSlot(t *testing.T) {
	prov := NewSoftwareProvisioner(101)
	pool, _ := NewPool(prov, 1)
	d, err := pool.Acquire(context.Background(), DeviceSpec{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if pool.Available() != 0 {
		t.Fatalf("available = %d, want 0", pool.Available())
	}
	// Exhausted now.
	if _, err := pool.Acquire(context.Background(), DeviceSpec{}); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("expected exhaustion, got %v", err)
	}
	// Release frees the slot.
	if err := pool.Release(context.Background(), d); err != nil {
		t.Fatalf("release: %v", err)
	}
	if pool.Available() != 1 {
		t.Fatalf("available after release = %d, want 1", pool.Available())
	}
	if d.State() != StateReleased {
		t.Errorf("released device state = %s, want Released", d.State())
	}
	// Re-acquire now succeeds.
	d2, err := pool.Acquire(context.Background(), DeviceSpec{})
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if d2 == nil {
		t.Fatal("re-acquire returned nil device")
	}
}

// TestPool_DoubleReleaseErrors asserts releasing the same device twice errors.
//
// Mutation that kills it: remove the live-membership check in Pool.Release
// (always proceed) -> the second release returns nil, failing the want-error.
func TestPool_DoubleReleaseErrors(t *testing.T) {
	prov := NewSoftwareProvisioner(102)
	pool, _ := NewPool(prov, 2)
	d, _ := pool.Acquire(context.Background(), DeviceSpec{})
	if err := pool.Release(context.Background(), d); err != nil {
		t.Fatalf("first release: %v", err)
	}
	err := pool.Release(context.Background(), d)
	if err == nil {
		t.Fatal("second release returned nil, want error")
	}
	if !errors.Is(err, ErrNotPooled) {
		t.Errorf("err = %v, want ErrNotPooled", err)
	}
}

// TestPool_GetBlocksUntilRelease asserts the blocking Get unblocks exactly when
// a Release frees a slot (sink-side: the blocked goroutine only completes after
// the release signal).
//
// Mutation that kills it: replace p.cond.Wait() in Get with an immediate
// ErrPoolExhausted return -> Get never blocks/unblocks; the ordering channel
// receives before the release, failing the "blocked before release" assertion.
func TestPool_GetBlocksUntilRelease(t *testing.T) {
	prov := NewSoftwareProvisioner(103)
	pool, _ := NewPool(prov, 1)
	d, err := pool.Acquire(context.Background(), DeviceSpec{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	var unblocked atomic.Bool
	ready := make(chan struct{})
	got := make(chan *Device, 1)
	go func() {
		// Signal that we are about to call Get. After this point the only thing
		// that can let Get succeed is a Release freeing the sole slot, so the
		// "blocked before release" assertion below rests on the pool's own
		// no-free-slot invariant, not on scheduler timing.
		close(ready)
		d2, err := pool.Get(context.Background(), DeviceSpec{})
		if err != nil {
			t.Errorf("blocked get: %v", err)
			got <- nil
			return
		}
		unblocked.Store(true)
		got <- d2
	}()

	<-ready
	// The pool is at cap (Available==0). While it stays at cap, Get cannot
	// succeed; assert it remains blocked. We confirm both the invariant (no free
	// slot, so success is impossible) and that the unblocked flag never flips
	// before we issue the Release.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if pool.Available() != 0 {
			t.Fatalf("available = %d before release, want 0 (slot freed without release)", pool.Available())
		}
		if unblocked.Load() {
			t.Fatal("Get unblocked before any Release while pool was at cap")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Release frees the only slot; Get must now complete.
	if err := pool.Release(context.Background(), d); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case d2 := <-got:
		if d2 == nil {
			t.Fatal("blocked Get returned nil device after release")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not unblock within 2s after Release")
	}
	if !unblocked.Load() {
		t.Fatal("unblocked flag not set after successful Get")
	}
}

// TestPool_GetCancelUnblocks asserts a blocked Get returns the ctx error on
// cancellation instead of hanging forever.
//
// Mutation that kills it: drop the context.AfterFunc broadcast in Get -> the
// blocked Wait is never woken on cancel and the test times out (fails).
func TestPool_GetCancelUnblocks(t *testing.T) {
	prov := NewSoftwareProvisioner(104)
	pool, _ := NewPool(prov, 1)
	if _, err := pool.Acquire(context.Background(), DeviceSpec{}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := pool.Get(ctx, DeviceSpec{})
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want wrapped context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Get did not return within 2s")
	}
}

// TestPool_ConcurrentNeverExceedsCap stresses concurrent Acquire/Release under
// -race and asserts the cap invariant holds throughout: InUse never exceeds cap
// and successful acquires never outnumber the cap at any instant.
//
// Mutation that kills it: remove the cap re-check in provisionAndTrack (the
// TOCTOU close) -> concurrent acquires over-hand-out, the maxInUse assertion
// (>cap) trips, and -race may also flag the live map.
func TestPool_ConcurrentNeverExceedsCap(t *testing.T) {
	const cap = 4
	prov := NewSoftwareProvisioner(105)
	pool, _ := NewPool(prov, cap)

	var maxInUse int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := pool.Acquire(context.Background(), DeviceSpec{Tier: "T2"})
			if err != nil {
				return // exhaustion is a legal outcome under contention
			}
			cur := int64(pool.InUse())
			for {
				m := atomic.LoadInt64(&maxInUse)
				if cur <= m || atomic.CompareAndSwapInt64(&maxInUse, m, cur) {
					break
				}
			}
			_ = pool.Release(context.Background(), d)
		}()
	}
	wg.Wait()

	if m := atomic.LoadInt64(&maxInUse); m > cap {
		t.Fatalf("observed max in-use %d, exceeds cap %d", m, cap)
	}
	if pool.Available() != cap {
		t.Errorf("available after drain = %d, want %d", pool.Available(), cap)
	}
	if pool.InUse() != 0 {
		t.Errorf("in-use after drain = %d, want 0", pool.InUse())
	}
}

// TestNewPool_RejectsNonPositiveCap asserts the constructor guards its invariant.
//
// Mutation that kills it: remove the cap<=0 guard in NewPool -> a zero-cap pool
// is returned with nil error, failing the want-error check.
func TestNewPool_RejectsNonPositiveCap(t *testing.T) {
	if _, err := NewPool(NewSoftwareProvisioner(1), 0); err == nil {
		t.Error("NewPool(_, 0) returned nil error, want error")
	}
	if _, err := NewPool(NewSoftwareProvisioner(1), -5); err == nil {
		t.Error("NewPool(_, -5) returned nil error, want error")
	}
}
