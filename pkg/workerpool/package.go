// Package workerpool provides a worker pool for Helix Cluster OS.
package workerpool

import "sync"

// Pool is a simple worker pool.
type Pool struct {
	wg       sync.WaitGroup
	work     chan func()
	stopCh   chan struct{}
	stopOnce sync.Once
}

// New creates a new Pool with n workers.
func New(n int) *Pool {
	p := &Pool{
		work:   make(chan func()),
		stopCh: make(chan struct{}),
	}
	for i := 0; i < n; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for {
		select {
		case fn := <-p.work:
			runTask(fn)
		case <-p.stopCh:
			return
		}
	}
}

// runTask executes a single task, recovering from any panic so that a
// misbehaving task cannot crash the worker goroutine (and thus the whole
// process). A nil task is skipped.
func runTask(fn func()) {
	if fn == nil {
		return
	}
	defer func() {
		// Swallow panics: one bad task must not kill the pool. The worker
		// goroutine survives and continues processing subsequent tasks.
		_ = recover()
	}()
	fn()
}

// Submit submits work to the pool. If the pool has already been stopped,
// Submit returns without blocking and without executing fn (the task is
// dropped rather than hanging the caller forever).
func (p *Pool) Submit(fn func()) {
	select {
	case <-p.stopCh:
		// Pool is stopped: do not block forever on the unbuffered channel.
		return
	default:
	}
	select {
	case p.work <- fn:
	case <-p.stopCh:
		// Pool was stopped while we were waiting to hand off the task.
		return
	}
}

// Stop stops the pool and waits for in-flight workers to finish. It is safe
// to call Stop multiple times; subsequent calls are no-ops.
func (p *Pool) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
	p.wg.Wait()
}
