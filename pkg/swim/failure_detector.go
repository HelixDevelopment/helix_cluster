package swim

import (
	"sync"
	"time"
)

// FailureDetector handles the suspicion mechanism.
type FailureDetector struct {
	mu        sync.RWMutex
	suspects  map[string]*suspectRecord
	timeout   time.Duration
	onConfirm func(memberID string)
	onRefute  func(memberID string)
}

type suspectRecord struct {
	memberID    string
	incarnation uint32
	startedAt   time.Time
	timer       *time.Timer
}

// NewFailureDetector creates a failure detector.
func NewFailureDetector(timeout time.Duration, onConfirm, onRefute func(string)) *FailureDetector {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &FailureDetector{
		suspects:  make(map[string]*suspectRecord),
		timeout:   timeout,
		onConfirm: onConfirm,
		onRefute:  onRefute,
	}
}

// Suspect marks a member as suspected.
func (fd *FailureDetector) Suspect(memberID string, incarnation uint32) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	// A member already in a suspicion period MUST NOT have its death timer reset.
	// The probe loop re-suspects an unresponsive peer on every failed probe
	// (~every probeInterval); resetting the timer each time would starve the
	// suspicion timeout and the peer would never be Confirmed dead. Standard SWIM
	// runs a single suspicion period to completion, cleared only by Refute (the
	// peer responds) or Confirm (the timeout fires). Track the highest incarnation
	// but keep the original deadline.
	if existing, ok := fd.suspects[memberID]; ok {
		if incarnation > existing.incarnation {
			existing.incarnation = incarnation
		}
		return
	}

	rec := &suspectRecord{
		memberID:    memberID,
		incarnation: incarnation,
		startedAt:   time.Now(),
	}
	rec.timer = time.AfterFunc(fd.timeout, func() {
		fd.Confirm(memberID)
	})
	fd.suspects[memberID] = rec
}

// Refute removes suspicion when member responds. The onRefute callback (which in
// the protocol acquires p.mu and performs cluster work) is invoked AFTER fd.mu is
// released: holding fd.mu across the callback would impose a fragile fd.mu->p.mu
// lock order and serialize all suspicion processing behind cluster mutations.
func (fd *FailureDetector) Refute(memberID string) {
	fd.mu.Lock()
	rec, ok := fd.suspects[memberID]
	if !ok {
		fd.mu.Unlock()
		return
	}
	rec.timer.Stop()
	delete(fd.suspects, memberID)
	cb := fd.onRefute
	// Release fd.mu BEFORE invoking the callback: the callback (in the protocol)
	// acquires p.mu / re-enters the detector, so holding fd.mu here would impose a
	// fragile fd.mu->p.mu lock order and self-deadlock on re-entry.
	fd.mu.Unlock()
	if cb != nil {
		cb(memberID)
	}
}

// Confirm marks a member as dead (suspicion timeout expired). As with Refute, the
// onConfirm callback is invoked AFTER fd.mu is released to avoid an fd.mu->p.mu
// lock-order hazard and to keep suspicion bookkeeping from blocking on cluster
// work performed inside the callback.
func (fd *FailureDetector) Confirm(memberID string) {
	fd.mu.Lock()
	rec, ok := fd.suspects[memberID]
	if !ok {
		fd.mu.Unlock()
		return
	}
	rec.timer.Stop()
	delete(fd.suspects, memberID)
	cb := fd.onConfirm
	// Release fd.mu BEFORE invoking the callback (see Refute): the protocol's
	// onConfirm acquires p.mu and performs cluster work, and the callback may
	// re-enter the detector; holding fd.mu across it would deadlock.
	fd.mu.Unlock()
	if cb != nil {
		cb(memberID)
	}
}

// IsSuspected returns true if the member is currently suspected.
func (fd *FailureDetector) IsSuspected(memberID string) bool {
	fd.mu.RLock()
	defer fd.mu.RUnlock()
	_, ok := fd.suspects[memberID]
	return ok
}

// SuspectCount returns the number of currently suspected members.
func (fd *FailureDetector) SuspectCount() int {
	fd.mu.RLock()
	defer fd.mu.RUnlock()
	return len(fd.suspects)
}
