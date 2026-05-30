// Package workerpool provides a worker pool for Helix Cluster OS.
package workerpool

import "sync"

// Pool is a simple worker pool.
type Pool struct {
	wg     sync.WaitGroup
	work   chan func()
	stopCh chan struct{}
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
			if fn != nil {
				fn()
			}
		case <-p.stopCh:
			return
		}
	}
}

// Submit submits work to the pool.
func (p *Pool) Submit(fn func()) {
	select {
	case p.work <- fn:
	default:
	}
}

// Stop stops the pool.
func (p *Pool) Stop() {
	close(p.stopCh)
	p.wg.Wait()
}
