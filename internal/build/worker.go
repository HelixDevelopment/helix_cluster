// Package build provides the build orchestration service for Helix Cluster OS.
package build

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Worker represents a build worker node in the cluster.
type Worker struct {
	ID       string
	Address  string
	Capacity int // max concurrent builds
	Load     int // current active builds
	Healthy  bool
	LastSeen time.Time
	Labels   map[string]string
	mu       sync.RWMutex
}

// WorkerPool manages registration, allocation, and release of build workers.
type WorkerPool struct {
	mu      sync.RWMutex
	workers map[string]*Worker
}

// NewWorkerPool creates an empty worker pool.
func NewWorkerPool() *WorkerPool {
	return &WorkerPool{
		workers: make(map[string]*Worker),
	}
}

// RegisterWorker adds or updates a worker in the pool.
func (p *WorkerPool) RegisterWorker(ctx context.Context, w *Worker) error {
	if w.ID == "" {
		return fmt.Errorf("worker ID is required")
	}
	if w.Capacity < 1 {
		return fmt.Errorf("worker capacity must be at least 1")
	}

	w.mu.Lock()
	w.Healthy = true
	w.LastSeen = time.Now().UTC()
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.workers[w.ID] = w
	return nil
}

// AllocateWorker selects and reserves an available worker for a build.
// It prefers the worker with the most remaining capacity.
func (p *WorkerPool) AllocateWorker(ctx context.Context) (*Worker, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var selected *Worker
	for _, w := range p.workers {
		w.mu.RLock()
		available := w.Healthy && w.Load < w.Capacity
		load := w.Load
		cap := w.Capacity
		w.mu.RUnlock()

		if !available {
			continue
		}
		if selected == nil {
			selected = w
			continue
		}
		selected.mu.RLock()
		selectedRemaining := selected.Capacity - selected.Load
		selected.mu.RUnlock()
		if cap-load > selectedRemaining {
			selected = w
		}
	}

	if selected == nil {
		return nil, fmt.Errorf("no available workers")
	}

	selected.mu.Lock()
	selected.Load++
	selected.mu.Unlock()
	return selected, nil
}

// ReleaseWorker decrements the load of a worker by ID.
func (p *WorkerPool) ReleaseWorker(workerID string) error {
	p.mu.RLock()
	w, ok := p.workers[workerID]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("worker %s not found", workerID)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Load > 0 {
		w.Load--
	}
	return nil
}

// GetWorker returns a worker by ID.
func (p *WorkerPool) GetWorker(workerID string) (*Worker, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	w, ok := p.workers[workerID]
	return w, ok
}

// ListWorkers returns all registered workers.
func (p *WorkerPool) ListWorkers() []*Worker {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Worker, 0, len(p.workers))
	for _, w := range p.workers {
		out = append(out, w)
	}
	return out
}

// RemoveWorker removes a worker from the pool.
func (p *WorkerPool) RemoveWorker(workerID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.workers, workerID)
}

// TotalCapacity returns the sum of all worker capacities.
func (p *WorkerPool) TotalCapacity() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	total := 0
	for _, w := range p.workers {
		w.mu.RLock()
		total += w.Capacity
		w.mu.RUnlock()
	}
	return total
}

// TotalLoad returns the sum of all current worker loads.
func (p *WorkerPool) TotalLoad() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	total := 0
	for _, w := range p.workers {
		w.mu.RLock()
		total += w.Load
		w.mu.RUnlock()
	}
	return total
}
