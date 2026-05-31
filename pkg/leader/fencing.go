package leader

import (
	"errors"
	"sync"
	"time"
)

// ErrLeaseHeld is returned by AcquireLease when a valid (un-expired) lease is
// still held — either by this node or, via a shared LeaseRegistry, by another
// node. Deterministic failover requires that a new candidate may acquire ONLY
// after the prior lease has expired.
var ErrLeaseHeld = errors.New("leader: lease still held by an un-expired holder")

// Clock is an injectable time source. It exists so that leadership failover and
// TTL/lease expiry can be exercised deterministically in tests with a fake clock
// (no time.Sleep, no wall-clock dependence). The production default is realClock.
type Clock interface {
	Now() time.Time
}

// realClock is the default, wall-clock-backed implementation of Clock.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// FencingToken is the monotonically increasing credential issued on each
// successful leadership acquisition. Consumers attach the token they observed
// from a leader to their writes; a downstream resource that tracks the highest
// epoch it has seen can reject any request carrying a strictly lower epoch,
// fencing out a stale (e.g. paused or partitioned) former leader.
type FencingToken struct {
	// HolderID is the node that acquired the lease.
	HolderID string
	// Epoch is the monotonic fencing number. Strictly increases across the whole
	// election lifetime; never reused.
	Epoch uint64
	// Expiry is the clock time at which this lease becomes invalid.
	Expiry time.Time
}

// IsStale reports whether this token is older than highestSeen, i.e. a newer
// leadership epoch has been issued since this token. A consumer that rejects
// stale tokens prevents a former leader from acting after losing leadership.
func (t FencingToken) IsStale(highestSeen uint64) bool {
	return t.Epoch < highestSeen
}

// LeaseRegistry is the shared split-brain guard. A single registry instance is
// shared (via SetLeaseRegistry) by all Election instances that contend for the
// SAME logical leadership. It guarantees that at most one holder owns a valid
// (un-expired) lease at any clock instant, and that each granted lease carries a
// strictly increasing epoch — so two nodes can never both hold a valid lease
// with the same epoch.
type LeaseRegistry struct {
	mu        sync.Mutex
	holderID  string
	epoch     uint64
	expiry    time.Time
	hasHolder bool
}

// NewLeaseRegistry creates an empty registry.
func NewLeaseRegistry() *LeaseRegistry {
	return &LeaseRegistry{}
}

// acquire attempts to grant the lease to holderID for ttl as of now. It succeeds
// only if there is no current holder OR the current lease has expired (now >=
// expiry) OR the current holder is requesting renewal. On success it issues a
// strictly greater epoch and returns it. On failure (a different, still-valid
// holder) it returns ErrLeaseHeld.
func (r *LeaseRegistry) acquire(holderID string, now time.Time, ttl time.Duration) (uint64, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	valid := r.hasHolder && now.Before(r.expiry)
	if valid && r.holderID != holderID {
		return 0, time.Time{}, ErrLeaseHeld
	}

	r.epoch++
	r.holderID = holderID
	r.expiry = now.Add(ttl)
	r.hasHolder = true
	return r.epoch, r.expiry, nil
}

// validFor reports whether holderID currently owns a valid (un-expired) lease in
// this registry as of now.
func (r *LeaseRegistry) validFor(holderID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hasHolder && r.holderID == holderID && now.Before(r.expiry)
}

// SetLeaseRegistry attaches a shared registry used as the cross-node split-brain
// guard. Elections sharing the same registry contend for one logical lease.
func (e *Election) SetLeaseRegistry(r *LeaseRegistry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.registry = r
}

// AcquireLease attempts to acquire (or renew) the leadership lease and returns a
// fresh FencingToken on success.
//
// Hardening guarantees:
//   - Fencing token: every successful acquisition issues a strictly increasing
//     epoch, so stale leaders are detectable by consumers (see FencingToken.IsStale).
//   - Deterministic failover: a new candidate may acquire ONLY after the current
//     lease has expired (now >= expiry). A second acquire before expiry by a
//     different node is rejected with ErrLeaseHeld and burns no epoch.
//   - Split-brain guard: when a shared LeaseRegistry is attached, the registry
//     enforces a single valid holder per clock instant across all nodes; a
//     contending node cannot acquire while another holds an un-expired lease.
func (e *Election) AcquireLease() (FencingToken, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.clock.Now()

	if e.registry != nil {
		epoch, expiry, err := e.registry.acquire(e.localID, now, e.ttl)
		if err != nil {
			return FencingToken{}, err
		}
		e.epoch = epoch
		e.leaseExpiry = expiry
		e.isLeader = true
		e.leaderID = e.localID
		e.lastElected = now
		return FencingToken{HolderID: e.localID, Epoch: epoch, Expiry: expiry}, nil
	}

	// Local (registry-less) mode: enforce deterministic failover against our own
	// lease. A new acquisition is permitted ONLY after the current lease has
	// expired (now >= expiry); acquiring while still valid is rejected and burns
	// no epoch. This mirrors the cross-node guarantee enforced by the registry.
	if !e.leaseExpiry.IsZero() && now.Before(e.leaseExpiry) {
		return FencingToken{}, ErrLeaseHeld
	}
	e.epoch++
	e.leaseExpiry = now.Add(e.ttl)
	e.isLeader = true
	e.leaderID = e.localID
	e.lastElected = now
	return FencingToken{HolderID: e.localID, Epoch: e.epoch, Expiry: e.leaseExpiry}, nil
}

// LeaseValid reports whether this node currently holds a valid (un-expired)
// lease, evaluated against the injectable clock. When a shared registry is
// attached, validity is authoritative against the registry (so a node whose
// lease was taken over by a peer correctly reports false), preventing split-brain.
func (e *Election) LeaseValid() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	now := e.clock.Now()
	if e.registry != nil {
		return e.registry.validFor(e.localID, now)
	}
	return !e.leaseExpiry.IsZero() && now.Before(e.leaseExpiry)
}

// Epoch returns the most recent fencing epoch issued to this node (0 if it has
// never acquired a lease).
func (e *Election) Epoch() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.epoch
}
