package gpu

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	defaultMonitorInterval   = 5 * time.Second
	defaultTemperatureThreshold = 95.0 // Celsius
	defaultUtilizationThreshold = 100.0 // Percent
)

// Monitor periodically checks GPU health and updates metrics.
type Monitor struct {
	mu        sync.RWMutex
	manager   *Manager
	interval  time.Duration
	tempThreshold    float64
	utilThreshold    float64
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	running   bool
}

// MonitorOption configures the Monitor.
type MonitorOption func(*Monitor)

// WithInterval sets the monitoring interval.
func WithInterval(d time.Duration) MonitorOption {
	return func(m *Monitor) {
		m.interval = d
	}
}

// WithTemperatureThreshold sets the temperature threshold for marking GPUs unhealthy.
func WithTemperatureThreshold(t float64) MonitorOption {
	return func(m *Monitor) {
		m.tempThreshold = t
	}
}

// WithUtilizationThreshold sets the utilization threshold for marking GPUs unhealthy.
func WithUtilizationThreshold(u float64) MonitorOption {
	return func(m *Monitor) {
		m.utilThreshold = u
	}
}

// newMonitor creates a new Monitor for the given Manager.
func newMonitor(mgr *Manager, opts ...MonitorOption) *Monitor {
	m := &Monitor{
		manager:       mgr,
		interval:      defaultMonitorInterval,
		tempThreshold: defaultTemperatureThreshold,
		utilThreshold: defaultUtilizationThreshold,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Start begins periodic GPU health monitoring.
func (m *Monitor) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.running = true

	m.wg.Add(1)
	go m.loop(ctx)

	return nil
}

// Stop stops the monitor.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()

	m.wg.Wait()
}

// IsRunning reports whether the monitor is currently running.
func (m *Monitor) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

func (m *Monitor) loop(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	// Run immediately on start
	m.checkAll()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAll()
		}
	}
}

func (m *Monitor) checkAll() {
	gpus := m.manager.ListGPUs()
	for _, gpu := range gpus {
		m.checkGPU(gpu.ID)
	}
}

func (m *Monitor) checkGPU(id string) {
	// Mock metrics: simulate temperature, memory usage, and utilization.
	temp := 30.0 + rand.Float64()*70.0       // 30-100 C
	util := rand.Float64() * 100.0           // 0-100%
	memUsed := rand.Float64() * 0.9          // 0-90% of total memory

	m.manager.updateGPU(id, func(gpu *GPU) {
		if gpu.Status == Offline {
			return
		}

		gpu.TemperatureC = temp
		gpu.UtilizationPercent = util

		// If already allocated, keep allocated unless unhealthy
		if gpu.Status == Allocated {
			if temp > m.tempThreshold || util > m.utilThreshold {
				gpu.Status = Unhealthy
			}
			return
		}

		if temp > m.tempThreshold || util > m.utilThreshold {
			gpu.Status = Unhealthy
		} else if gpu.Status == Unhealthy {
			gpu.Status = Available
		}

		_ = memUsed // memory usage tracked but does not affect status in mock
	})
}
