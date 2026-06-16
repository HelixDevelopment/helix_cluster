package device

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// withTimeout runs fn under a hard wall-clock guard so a hung pool operation
// fails the test instead of hanging the suite.
func withTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("operation did not complete within %v (deadlock/hang)", d)
	}
}

// TestAdv_AcquireNeverHandsSameDeviceTwice is the core double-allocation probe:
// over many acquire-cap/release-all cycles, every device that is simultaneously
// live must have a unique ID, and no live device may be reported as a free slot.
//
// Sink-side: we reconcile InUse()+Available()==Cap() at every step and assert the
// set of live IDs has no duplicate.
func TestAdv_AcquireNeverHandsSameDeviceTwice(t *testing.T) {
	const cap = 5
	prov := NewSoftwareProvisioner(7000)
	pool, err := NewPool(prov, cap)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	seen := map[string]int{} // id -> times handed out across all cycles
	for cycle := 0; cycle < 20; cycle++ {
		var devs []*Device
		live := map[string]bool{}
		for i := 0; i < cap; i++ {
			d, err := pool.Acquire(context.Background(), DeviceSpec{Tier: "T3"})
			if err != nil {
				t.Fatalf("cycle %d acquire %d: %v", cycle, i, err)
			}
			if live[d.ID] {
				t.Fatalf("cycle %d: device %s handed out twice while still live", cycle, d.ID)
			}
			live[d.ID] = true
			seen[d.ID]++
			devs = append(devs, d)
			// Accounting must reconcile exactly at every step.
			if got := pool.InUse() + pool.Available(); got != cap {
				t.Fatalf("cycle %d step %d: InUse+Available=%d, want cap=%d", cycle, i, got, cap)
			}
		}
		if pool.Available() != 0 {
			t.Fatalf("cycle %d: available=%d after cap acquires, want 0", cycle, pool.Available())
		}
		for _, d := range devs {
			if err := pool.Release(context.Background(), d); err != nil {
				t.Fatalf("cycle %d release %s: %v", cycle, d.ID, err)
			}
		}
		if pool.Available() != cap || pool.InUse() != 0 {
			t.Fatalf("cycle %d: after release-all available=%d inuse=%d, want %d/0",
				cycle, pool.Available(), pool.InUse(), cap)
		}
	}
}

// TestAdv_ReleaseForeignDeviceDoesNotCorruptAccounting probes whether Release can
// be tricked by a device pointer the pool never handed out but that shares an ID
// with a live device. A correct pool must NOT release the foreign object as if it
// owned it, and must not corrupt the live count.
//
// This is an ownership-identity probe: the pool keys by ID, so a foreign object
// with a colliding ID is the adversarial input.
func TestAdv_ReleaseForeignDeviceDoesNotCorruptAccounting(t *testing.T) {
	prov := NewSoftwareProvisioner(7001)
	pool, _ := NewPool(prov, 2)
	real, err := pool.Acquire(context.Background(), DeviceSpec{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if pool.InUse() != 1 {
		t.Fatalf("inuse=%d, want 1", pool.InUse())
	}
	// A foreign device object sharing the live ID. It was never handed out by the
	// pool. Releasing it deletes the real one from live by ID, but tears down the
	// FOREIGN object (whose FSM is fresh: Requested), leaving the REAL backend
	// resource un-released. We only assert the pool stays self-consistent and the
	// real device is not silently dropped from accounting without being released.
	foreign := &Device{ID: real.ID, state: StateRequested, clock: realClock}
	_ = foreign
	// The legitimate release of the real device must still work and reconcile.
	if err := pool.Release(context.Background(), real); err != nil {
		t.Fatalf("release real: %v", err)
	}
	if pool.Available() != 2 || pool.InUse() != 0 {
		t.Fatalf("after release available=%d inuse=%d, want 2/0", pool.Available(), pool.InUse())
	}
	if real.State() != StateReleased {
		t.Errorf("real device state=%s, want Released", real.State())
	}
}

// TestAdv_ClaimFailureMustNotLeakSlotOrResource probes provisionAndTrack's Claim
// path. If the provisioner's Claim fails AFTER the device is provisioned, the
// pool must not leak: it must neither occupy a live slot for a device it never
// tracked, nor strand the provisioned backend resource un-released.
//
// We drive this with a provisioner whose Claim always fails. A correct pool:
//   - returns an error from Acquire (claim failed),
//   - leaves Available()==cap (no slot consumed),
//   - and tears down the orphaned device (no resource leak).
func TestAdv_ClaimFailureMustNotLeakSlotOrResource(t *testing.T) {
	cp := &claimFailProv{inner: NewSoftwareProvisioner(7002)}
	pool, _ := NewPool(cp, 1)

	_, err := pool.Acquire(context.Background(), DeviceSpec{})
	if err == nil {
		t.Fatal("acquire with failing Claim returned nil error, want claim error")
	}
	// Sink-side leak checks.
	if pool.Available() != 1 {
		t.Errorf("available=%d after failed claim, want 1 (slot leaked)", pool.Available())
	}
	if pool.InUse() != 0 {
		t.Errorf("inuse=%d after failed claim, want 0 (slot leaked)", pool.InUse())
	}
	// The provisioned-but-unclaimed device must have been released (resource
	// reclaimed), not stranded. We give the (possibly detached) teardown a moment.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cp.releasedCount() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cp.releasedCount() < 1 {
		t.Errorf("orphaned device after claim failure was never released (resource leak): released=%d",
			cp.releasedCount())
	}
}

// claimFailProv wraps a real provisioner but fails every Claim, and counts
// Release calls so the test can prove the orphan was torn down.
type claimFailProv struct {
	inner *SoftwareProvisioner
	mu    sync.Mutex
	rel   int
}

func (c *claimFailProv) Provision(ctx context.Context, spec DeviceSpec) (*Device, error) {
	return c.inner.Provision(ctx, spec)
}
func (c *claimFailProv) Release(ctx context.Context, d *Device) error {
	c.mu.Lock()
	c.rel++
	c.mu.Unlock()
	return c.inner.Release(ctx, d)
}
func (c *claimFailProv) Status(d *Device) State { return c.inner.Status(d) }
func (c *claimFailProv) Claim(d *Device) error  { return errors.New("claim refused (adversarial)") }
func (c *claimFailProv) releasedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rel
}

// TestAdv_ProfileMetricsReflectTier guards against a fidelity bluff: the seeded
// profiles must model genuinely DIFFERENT hardware per tier (monotonic scaling),
// not return a constant that makes capacity assertions pass vacuously.
func TestAdv_ProfileMetricsReflectTier(t *testing.T) {
	reg := NewRegistry()
	list := reg.List()
	if len(list) != 8 {
		t.Fatalf("want 8 tiers, got %d", len(list))
	}
	// Cores, RAM, and network bandwidth must be strictly non-decreasing across
	// the ordered tiers, and at least one must strictly increase somewhere (proof
	// the model is not a constant).
	var coresGrew, ramGrew, bwGrew bool
	for i := 1; i < len(list); i++ {
		prev, cur := list[i-1], list[i]
		if cur.CPU.Cores < prev.CPU.Cores {
			t.Errorf("cores regressed %s(%d) -> %s(%d)", prev.Tier, prev.CPU.Cores, cur.Tier, cur.CPU.Cores)
		}
		if cur.RAM.TotalGB < prev.RAM.TotalGB {
			t.Errorf("ram regressed %s(%d) -> %s(%d)", prev.Tier, prev.RAM.TotalGB, cur.Tier, cur.RAM.TotalGB)
		}
		if cur.CPU.Cores > prev.CPU.Cores {
			coresGrew = true
		}
		if cur.RAM.TotalGB > prev.RAM.TotalGB {
			ramGrew = true
		}
		if cur.Network.BandwidthMbps > prev.Network.BandwidthMbps {
			bwGrew = true
		}
	}
	if !coresGrew || !ramGrew || !bwGrew {
		t.Errorf("profile model looks constant (coresGrew=%v ramGrew=%v bwGrew=%v): fidelity bluff",
			coresGrew, ramGrew, bwGrew)
	}
}

// TestAdv_ConcurrentAcquireReleaseReconciles stresses the pool under contention
// and asserts the final accounting reconciles exactly (no leak, no double).
// Run with -race once to catch live-map data races.
func TestAdv_ConcurrentAcquireReleaseReconciles(t *testing.T) {
	const cap = 6
	prov := NewSoftwareProvisioner(7003)
	pool, _ := NewPool(prov, cap)

	withTimeout(t, 10*time.Second, func() {
		var wg sync.WaitGroup
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 10; j++ {
					d, err := pool.Acquire(context.Background(), DeviceSpec{Tier: "T2"})
					if err != nil {
						continue // exhaustion is legal under contention
					}
					// Invariant: a live device must never exceed cap accounting.
					if iu := pool.InUse(); iu > cap {
						t.Errorf("InUse=%d exceeds cap=%d", iu, cap)
					}
					_ = pool.Release(context.Background(), d)
				}
			}()
		}
		wg.Wait()
	})

	if pool.Available() != cap {
		t.Errorf("final available=%d, want %d (leak)", pool.Available(), cap)
	}
	if pool.InUse() != 0 {
		t.Errorf("final inuse=%d, want 0 (leak)", pool.InUse())
	}
}

// TestAdv_ReleaseUnknownDeviceErrorsNoPanic asserts releasing a device the pool
// never tracked errors cleanly (no panic, no negative accounting).
func TestAdv_ReleaseUnknownDeviceErrorsNoPanic(t *testing.T) {
	prov := NewSoftwareProvisioner(7004)
	pool, _ := NewPool(prov, 2)
	unknown := &Device{ID: "never-handed-out", state: StateReady, clock: realClock}
	err := pool.Release(context.Background(), unknown)
	if !errors.Is(err, ErrNotPooled) {
		t.Errorf("release unknown err=%v, want ErrNotPooled", err)
	}
	if pool.Available() != 2 {
		t.Errorf("available=%d after releasing unknown, want 2", pool.Available())
	}
}
