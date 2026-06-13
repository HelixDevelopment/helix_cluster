// Package chaos provides fault injection primitives for deterministic cluster testing.
package chaos

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

var (
	ErrAlreadyApplied = errors.New("fault already applied")
	ErrNotApplied     = errors.New("fault not applied")
)

// Fault is the interface for chaos faults that can be applied and restored.
type Fault interface {
	Apply() error
	Restore() error
	Name() string
}

// NetworkPartition isolates a set of nodes from each other.
type NetworkPartition struct {
	Nodes   []string
	applied bool
}

// Name returns the fault name.
func (np *NetworkPartition) Name() string {
	return "network-partition"
}

// Apply simulates applying a network partition.
func (np *NetworkPartition) Apply() error {
	if np.applied {
		return fmt.Errorf("partition already applied: %w", ErrAlreadyApplied)
	}
	np.applied = true
	return nil
}

// Restore removes the network partition.
func (np *NetworkPartition) Restore() error {
	if !np.applied {
		return fmt.Errorf("partition not applied: %w", ErrNotApplied)
	}
	np.applied = false
	return nil
}

// PacketLoss drops a percentage of packets between nodes.
type PacketLoss struct {
	From    string
	To      string
	Percent float64
	rng     *rand.Rand
	mu      sync.Mutex
	applied bool
}

// NewPacketLoss creates a new packet-loss fault.
func NewPacketLoss(from, to string, percent float64, rng *rand.Rand) *PacketLoss {
	return &PacketLoss{From: from, To: to, Percent: percent, rng: rng}
}

// Name returns the fault name.
func (pl *PacketLoss) Name() string {
	return "packet-loss"
}

// Apply enables packet loss simulation.
func (pl *PacketLoss) Apply() error {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.applied {
		return fmt.Errorf("packet loss already applied: %w", ErrAlreadyApplied)
	}
	pl.applied = true
	return nil
}

// Restore disables packet loss simulation.
func (pl *PacketLoss) Restore() error {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if !pl.applied {
		return fmt.Errorf("packet loss not applied: %w", ErrNotApplied)
	}
	pl.applied = false
	return nil
}

// ShouldDrop returns true if the next packet should be dropped.
func (pl *PacketLoss) ShouldDrop() bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if !pl.applied {
		return false
	}
	return pl.rng.Float64() < pl.Percent/100.0
}

// LatencyInjection adds artificial latency to message delivery.
type LatencyInjection struct {
	From    string
	To      string
	Delay   time.Duration
	Jitter  time.Duration
	rng     *rand.Rand
	mu      sync.Mutex
	applied bool
}

// NewLatencyInjection creates a latency injection fault.
func NewLatencyInjection(from, to string, delay, jitter time.Duration, rng *rand.Rand) *LatencyInjection {
	return &LatencyInjection{From: from, To: to, Delay: delay, Jitter: jitter, rng: rng}
}

// Name returns the fault name.
func (li *LatencyInjection) Name() string {
	return "latency-injection"
}

// Apply enables latency injection.
func (li *LatencyInjection) Apply() error {
	li.mu.Lock()
	defer li.mu.Unlock()
	if li.applied {
		return fmt.Errorf("latency injection already applied: %w", ErrAlreadyApplied)
	}
	li.applied = true
	return nil
}

// Restore disables latency injection.
func (li *LatencyInjection) Restore() error {
	li.mu.Lock()
	defer li.mu.Unlock()
	if !li.applied {
		return fmt.Errorf("latency injection not applied: %w", ErrNotApplied)
	}
	li.applied = false
	return nil
}

// NextDelay returns the delay for the next message.
func (li *LatencyInjection) NextDelay() time.Duration {
	li.mu.Lock()
	defer li.mu.Unlock()
	if !li.applied {
		return 0
	}
	jitter := time.Duration(0)
	if li.Jitter > 0 {
		jitter = time.Duration(li.rng.Int63n(int64(li.Jitter)))
	}
	return li.Delay + jitter
}

// NodeCrash simulates a node crashing and becoming unavailable.
type NodeCrash struct {
	NodeID  string
	applied bool
}

// Name returns the fault name.
func (nc *NodeCrash) Name() string {
	return "node-crash"
}

// Apply simulates a node crash.
func (nc *NodeCrash) Apply() error {
	if nc.applied {
		return fmt.Errorf("node crash already applied: %w", ErrAlreadyApplied)
	}
	nc.applied = true
	return nil
}

// Restore simulates node recovery.
func (nc *NodeCrash) Restore() error {
	if !nc.applied {
		return fmt.Errorf("node crash not applied: %w", ErrNotApplied)
	}
	nc.applied = false
	return nil
}

// IsCrashed reports whether the node is currently crashed.
func (nc *NodeCrash) IsCrashed() bool {
	return nc.applied
}

// ResourceExhaustion simulates CPU or memory exhaustion on a node.
type ResourceExhaustion struct {
	NodeID   string
	Resource string // "cpu" or "memory"
	Percent  float64
	applied  bool
}

// Name returns the fault name.
func (re *ResourceExhaustion) Name() string {
	return "resource-exhaustion"
}

// Apply enables resource exhaustion.
func (re *ResourceExhaustion) Apply() error {
	if re.applied {
		return fmt.Errorf("resource exhaustion already applied: %w", ErrAlreadyApplied)
	}
	re.applied = true
	return nil
}

// Restore disables resource exhaustion.
func (re *ResourceExhaustion) Restore() error {
	if !re.applied {
		return fmt.Errorf("resource exhaustion not applied: %w", ErrNotApplied)
	}
	re.applied = false
	return nil
}

// ChaosRunner orchestrates multiple faults.
type ChaosRunner struct {
	mu     sync.RWMutex
	faults []Fault
}

// NewChaosRunner creates a new chaos runner.
func NewChaosRunner() *ChaosRunner {
	return &ChaosRunner{faults: make([]Fault, 0)}
}

// AddFault registers a fault with the runner.
func (cr *ChaosRunner) AddFault(f Fault) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.faults = append(cr.faults, f)
}

// ApplyAll applies all registered faults.
func (cr *ChaosRunner) ApplyAll() error {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	for _, f := range cr.faults {
		if err := f.Apply(); err != nil {
			return fmt.Errorf("apply %s: %w", f.Name(), err)
		}
	}
	return nil
}

// RestoreAll restores all registered faults.
func (cr *ChaosRunner) RestoreAll() error {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	for _, f := range cr.faults {
		if err := f.Restore(); err != nil {
			return fmt.Errorf("restore %s: %w", f.Name(), err)
		}
	}
	return nil
}

// ListFaults returns the names of all registered faults.
func (cr *ChaosRunner) ListFaults() []string {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	out := make([]string, len(cr.faults))
	for i, f := range cr.faults {
		out[i] = f.Name()
	}
	return out
}

// ActiveFaults returns faults that are currently applied.
func (cr *ChaosRunner) ActiveFaults() []Fault {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	var out []Fault
	for _, f := range cr.faults {
		switch ft := f.(type) {
		case *NodeCrash:
			if ft.IsCrashed() {
				out = append(out, f)
			}
		default:
			// Other fault types don't expose applied state directly in interface.
		}
	}
	return out
}
