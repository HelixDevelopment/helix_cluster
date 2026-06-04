package device

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrPoolExhausted is returned by Acquire (non-blocking) when the cap is reached.
var ErrPoolExhausted = errors.New("device: pool exhausted")

// ErrNotPooled is returned when Release is given a device the pool never handed out.
var ErrNotPooled = errors.New("device: device not owned by pool")

// Pool is a capacity-capped device pool over any Provisioner. It hands out at
// most cap live devices; the cap+1-th Acquire errors (ErrPoolExhausted) while
// Get blocks until a Release frees a slot. Released devices are reclaimed and the
// available count is restored — this count is the sink-side proof of the cap.
type Pool struct {
	prov Provisioner
	cap  int

	mu    sync.Mutex
	cond  *sync.Cond
	live  map[string]*Device // currently checked-out devices, keyed by ID
}

// NewPool builds a Pool of capacity cap backed by prov. cap must be > 0.
func NewPool(prov Provisioner, cap int) (*Pool, error) {
	if cap <= 0 {
		return nil, fmt.Errorf("device: pool cap must be positive, got %d", cap)
	}
	p := &Pool{
		prov: prov,
		cap:  cap,
		live: make(map[string]*Device),
	}
	p.cond = sync.NewCond(&p.mu)
	return p, nil
}

// Cap returns the configured capacity.
func (p *Pool) Cap() int { return p.cap }

// Available returns the number of free slots (cap minus live checkouts). This is
// the sink-side number tests assert against to prove the cap is enforced.
func (p *Pool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cap - len(p.live)
}

// InUse returns the number of currently checked-out devices.
func (p *Pool) InUse() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.live)
}

// reserve attempts to claim a slot. It is the single chokepoint that enforces
// the cap; a Pool that bypassed this check would over-hand-out and fail the
// "cap+1 blocked/errored" assertion.
func (p *Pool) reserve() bool {
	if len(p.live) >= p.cap {
		return false
	}
	return true
}

// Acquire hands out a device without blocking. It returns ErrPoolExhausted when
// the cap is already reached.
func (p *Pool) Acquire(ctx context.Context, spec DeviceSpec) (*Device, error) {
	p.mu.Lock()
	if !p.reserve() {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: cap=%d", ErrPoolExhausted, p.cap)
	}
	// NOTE: reserve() only peeks; no slot is reserved here and the lock is
	// dropped before provisioning. A TOCTOU window therefore exists between this
	// reserve() pass and the live-map insert in provisionAndTrack. That window is
	// closed there by re-checking len(p.live) >= p.cap under the lock, which
	// rejects (and tears down) any device that lost the race for the last slot.
	p.mu.Unlock()

	d, err := p.provisionAndTrack(ctx, spec)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Get hands out a device, blocking until a slot is free or ctx is done. On ctx
// cancellation it returns the context error.
func (p *Pool) Get(ctx context.Context, spec DeviceSpec) (*Device, error) {
	p.mu.Lock()
	// Wake the waiter if ctx is cancelled.
	stop := context.AfterFunc(ctx, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
	defer stop()

	for !p.reserve() {
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, fmt.Errorf("device: get cancelled: %w", err)
		}
		p.cond.Wait()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, fmt.Errorf("device: get cancelled: %w", err)
		}
	}
	p.mu.Unlock()

	return p.provisionAndTrack(ctx, spec)
}

// provisionAndTrack provisions a device and, on success, records it as live. The
// cap re-check inside the lock closes the TOCTOU window between reserve() and
// tracking so concurrent callers can never exceed cap.
func (p *Pool) provisionAndTrack(ctx context.Context, spec DeviceSpec) (*Device, error) {
	d, err := p.prov.Provision(ctx, spec)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.live) >= p.cap {
		// Lost a race for the last slot; release the just-provisioned device.
		// Best-effort teardown so we don't leak the backend resource. This MUST
		// use a detached context, not the request ctx: the request is failing and
		// its ctx is about to be cancelled, which would abort the very cleanup we
		// need to run. The teardown therefore deliberately outlives the request.
		go func() { _ = p.prov.Release(context.Background(), d) }() //gosec:disable G118 -- detached best-effort teardown must outlive the cancelled request ctx, so request-scoped ctx is intentionally NOT used
		return nil, fmt.Errorf("%w: cap=%d (lost race)", ErrPoolExhausted, p.cap)
	}
	// Mark the provisioner-side device as claimed (Ready->InUse) when supported.
	if c, ok := p.prov.(interface{ Claim(*Device) error }); ok {
		if err := c.Claim(d); err != nil {
			return nil, fmt.Errorf("device %s: claim: %w", d.ID, err)
		}
	}
	p.live[d.ID] = d
	return d, nil
}

// Release returns a device to the pool, freeing its slot and tearing down the
// backend resource. A device not owned by the pool (or already released) errors.
func (p *Pool) Release(ctx context.Context, d *Device) error {
	p.mu.Lock()
	if _, ok := p.live[d.ID]; !ok {
		p.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotPooled, d.ID)
	}
	delete(p.live, d.ID)
	p.mu.Unlock()

	if err := p.prov.Release(ctx, d); err != nil {
		// Re-insert so accounting stays truthful if teardown failed.
		p.mu.Lock()
		p.live[d.ID] = d
		p.mu.Unlock()
		return err
	}
	// Slot freed: wake one blocked Get.
	p.mu.Lock()
	p.cond.Signal()
	p.mu.Unlock()
	return nil
}
