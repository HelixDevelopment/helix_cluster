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

	if existing, ok := fd.suspects[memberID]; ok {
		existing.timer.Stop()
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

// Refute removes suspicion when member responds.
func (fd *FailureDetector) Refute(memberID string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	if rec, ok := fd.suspects[memberID]; ok {
		rec.timer.Stop()
		delete(fd.suspects, memberID)
		if fd.onRefute != nil {
			fd.onRefute(memberID)
		}
	}
}

// Confirm marks a member as dead (suspicion timeout expired).
func (fd *FailureDetector) Confirm(memberID string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	if rec, ok := fd.suspects[memberID]; ok {
		rec.timer.Stop()
		delete(fd.suspects, memberID)
		if fd.onConfirm != nil {
			fd.onConfirm(memberID)
		}
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
