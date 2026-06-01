package sandbox

import (
	"fmt"
	"sync"
	"time"
)

// Guard enforces capability grants and resource limits for in-process
// operations. All methods are safe for concurrent use from multiple goroutines.
//
// NOTE on OS-level isolation: syscall-level sandboxing (seccomp-bpf,
// Linux namespaces, macOS sandbox/pledge-equivalent) requires kernel-level
// facilities that cannot be provided by a pure-Go in-process guard. This
// package documents that boundary explicitly rather than faking it: Guard
// provides in-process capability gating + resource limiting, which is the
// genuine new value over pkg/wasm (which has caps but not resource limits).
// External kernel isolation is out of scope for this package.
type Guard struct {
	mu      sync.Mutex
	caps    *Capabilities
	tracker *usageTracker
	audit   AuditSink
}

// NewGuard constructs a Guard backed by caps, limits, and audit. If caps is
// nil, all capabilities are denied. If audit is nil, a no-op sink is used.
// The clock parameter is used for duration tracking; pass nil to use
// time.Now (production) — tests inject a deterministic clock.
func NewGuard(caps *Capabilities, limits ResourceLimits, audit AuditSink) *Guard {
	return newGuardWithClock(caps, limits, audit, nil)
}

// newGuardWithClock is the internal constructor that accepts a clock seam for
// deterministic testing of MaxDuration.
func newGuardWithClock(caps *Capabilities, limits ResourceLimits, audit AuditSink, clock func() time.Time) *Guard {
	if caps == nil {
		caps = NewCapabilities() // deny-by-default empty set
	}
	if audit == nil {
		audit = nopAudit{}
	}
	return &Guard{
		caps:    caps,
		tracker: newUsageTracker(limits, clock),
		audit:   audit,
	}
}

// Check verifies that cap is granted and that resource limits have not been
// exceeded. On success it records an Allow audit entry and returns nil. On
// failure it records a Deny audit entry and returns a wrapped sentinel error.
//
// Error values:
//   - errors.Is(err, ErrCapabilityDenied): cap was not granted.
//   - errors.Is(err, ErrLimitExceeded):    MaxOps or MaxDuration exceeded.
func (g *Guard) Check(cap Capability) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 1. Capability gate (deny-by-default).
	if !g.caps.Has(cap) {
		reason := fmt.Sprintf("capability %q not granted", string(cap))
		g.audit.Record(AuditEntry{Cap: cap, Decision: Deny, Reason: reason})
		return fmt.Errorf("%w: %s", ErrCapabilityDenied, reason)
	}

	// 2. Duration limit — checked before incrementing the op counter so a
	//    timed-out Guard denies further operations even if MaxOps not reached.
	if err := g.tracker.checkDuration(); err != nil {
		reason := fmt.Sprintf("max duration %s exceeded", g.tracker.limits.MaxDuration)
		g.audit.Record(AuditEntry{Cap: cap, Decision: Deny, Reason: reason})
		return fmt.Errorf("%w: %s", ErrLimitExceeded, reason)
	}

	// 3. Op-count limit — increment THEN check so the first N ops succeed
	//    and the (N+1)th fails.
	if err := g.tracker.addOps(1); err != nil {
		reason := fmt.Sprintf("max ops %d exceeded", g.tracker.limits.MaxOps)
		g.audit.Record(AuditEntry{Cap: cap, Decision: Deny, Reason: reason})
		return fmt.Errorf("%w: %s", ErrLimitExceeded, reason)
	}

	// 4. All checks passed.
	g.audit.Record(AuditEntry{Cap: cap, Decision: Allow, Reason: ""})
	return nil
}

// Account accumulates n bytes against the MaxBytes limit. It returns
// ErrLimitExceeded (wrapped) if the running total exceeds the limit.
// Account does NOT consume from the MaxOps counter; it tracks a separate
// byte-volume budget intended for data-transfer accounting.
func (g *Guard) Account(bytes int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := g.tracker.addBytes(bytes); err != nil {
		reason := fmt.Sprintf("max bytes %d exceeded", g.tracker.limits.MaxBytes)
		// Use a synthetic capability name that reflects byte accounting.
		g.audit.Record(AuditEntry{Cap: "bytes", Decision: Deny, Reason: reason})
		return fmt.Errorf("%w: %s", ErrLimitExceeded, reason)
	}
	return nil
}
