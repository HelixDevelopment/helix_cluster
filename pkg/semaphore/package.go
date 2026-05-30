// Package semaphore provides a counting semaphore for Helix Cluster OS.
package semaphore

// Semaphore is a counting semaphore.
type Semaphore struct {
	ch chan struct{}
}

// New creates a Semaphore with capacity n.
func New(n int) *Semaphore {
	return &Semaphore{ch: make(chan struct{}, n)}
}

// Acquire acquires a slot.
func (s *Semaphore) Acquire() {
	s.ch <- struct{}{}
}

// Release releases a slot.
func (s *Semaphore) Release() {
	<-s.ch
}

// TryAcquire tries to acquire without blocking.
func (s *Semaphore) TryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}
