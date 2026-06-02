// Package watchtower implements a deterministic watchtower-style liveness +
// integrity prober for cluster instances.
//
// Model: each registered instance carries an expected integrity baseline — a
// pinned digest of its expected command/config. A Prober checks two things per
// probe cycle:
//
//   - Liveness: a caller-supplied health signal (liveOK) reports whether the
//     instance is reachable/responding.
//   - Integrity: the instance's currently observed integrity digest is compared
//     against the pinned expected digest. A mismatch means the instance is
//     running an unexpected command/config (TAMPERED).
//
// An instance is FLAGGED (and may be quarantined) if it fails liveness OR fails
// integrity. Liveness alone never clears integrity: a live-but-tampered instance
// stays flagged. The package is pure Go, deterministic, and holds no clocks,
// network, or host dependencies — all observations are injected by the caller.
package watchtower

import (
	"errors"
	"sort"
	"sync"
)

// ErrUnknownInstance is returned by Check for an instance that was never
// registered with the Prober.
var ErrUnknownInstance = errors.New("watchtower: unknown instance")

// ErrEmptyInstanceID is returned by Register when given an empty instance ID.
var ErrEmptyInstanceID = errors.New("watchtower: empty instance id")

// Result is the outcome of a single Check.
type Result struct {
	// Live reports the injected liveness signal for this probe.
	Live bool
	// IntegrityOK is true iff the observed digest equals the pinned expected
	// digest for the instance.
	IntegrityOK bool
	// Flagged is true iff the instance failed liveness OR integrity.
	Flagged bool
}

// baseline is the per-instance pinned expectation.
type baseline struct {
	expectedDigest string
}

// Prober tracks expected integrity baselines for instances and evaluates
// liveness + integrity probes against them, maintaining a set of flagged
// (quarantine-candidate) instances.
type Prober struct {
	mu       sync.Mutex
	expected map[string]baseline
	flagged  map[string]struct{}
}

// NewProber returns an empty Prober ready for registration.
func NewProber() *Prober {
	return &Prober{
		expected: make(map[string]baseline),
		flagged:  make(map[string]struct{}),
	}
}

// Register pins expectedDigest as the integrity baseline for instanceID.
// Re-registering an instance replaces its baseline and clears any prior flag
// (it represents a freshly attested instance). An empty instanceID is rejected.
func (p *Prober) Register(instanceID, expectedDigest string) error {
	if instanceID == "" {
		return ErrEmptyInstanceID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expected[instanceID] = baseline{expectedDigest: expectedDigest}
	delete(p.flagged, instanceID)
	return nil
}

// Check evaluates one probe for instanceID using the injected liveness signal
// (liveOK) and the currently observed integrity digest (observedDigest).
//
// The instance is flagged when it is not live OR when its observed digest does
// not match the pinned expected digest. A flag, once raised, stays raised until
// the instance is re-Registered (re-attested) — a later healthy probe does not
// silently clear a prior tamper detection.
func (p *Prober) Check(instanceID string, liveOK bool, observedDigest string) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	base, ok := p.expected[instanceID]
	if !ok {
		return Result{}, ErrUnknownInstance
	}

	integrityOK := observedDigest == base.expectedDigest
	flaggedNow := !liveOK || !integrityOK

	if flaggedNow {
		p.flagged[instanceID] = struct{}{}
	}

	res := Result{
		Live:        liveOK,
		IntegrityOK: integrityOK,
		// Report sticky flag state: flagged now OR previously flagged.
		Flagged: p.isFlaggedLocked(instanceID),
	}
	return res, nil
}

// IsFlagged reports whether instanceID is currently in the flagged set.
func (p *Prober) IsFlagged(instanceID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isFlaggedLocked(instanceID)
}

func (p *Prober) isFlaggedLocked(instanceID string) bool {
	_, ok := p.flagged[instanceID]
	return ok
}

// Flagged returns the sorted set of currently flagged instance IDs.
func (p *Prober) Flagged() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.flagged))
	for id := range p.flagged {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
