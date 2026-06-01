package infra

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ServiceStatus represents the status of an infrastructure service.
type ServiceStatus struct {
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Healthy  bool              `json:"healthy"`
	Uptime   time.Duration     `json:"uptime"`
	Ports    []string          `json:"ports"`
	Latency  time.Duration     `json:"latency"`
	Message  string            `json:"message"`
	Labels   map[string]string `json:"labels"`
	Replicas int               `json:"replicas"`
}

// VMNode represents a virtual machine node.
type VMNode struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Status  string            `json:"status"`
	IP      string            `json:"ip"`
	CPU     int               `json:"cpu"`
	Memory  int64             `json:"memory"`
	Labels  map[string]string `json:"labels"`
	Created time.Time         `json:"created"`
}

// InfraConfig holds configuration for the infrastructure orchestrator.
type InfraConfig struct {
	ConfigPath     string        `json:"config_path"`
	ProjectName    string        `json:"project_name"`
	DefaultTimeout time.Duration `json:"default_timeout"`
	WaitForHealthy bool          `json:"wait_for_healthy"`
	Services       []string      `json:"services"`
}

// DefaultConfig returns a default InfraConfig.
func DefaultConfig() *InfraConfig {
	return &InfraConfig{
		ProjectName:    "helix-cluster",
		DefaultTimeout: 5 * time.Minute,
		Services:       DefaultServices(),
	}
}

// DefaultServices returns the list of all 20 default infrastructure services.
func DefaultServices() []string {
	return []string{
		"postgres-primary", "postgres-replica",
		"redis-master", "redis-replica",
		"etcd-1", "etcd-2", "etcd-3",
		"nats", "kafka", "rabbitmq",
		"prometheus", "grafana", "jaeger",
		"vault",
		"vm-node-1", "vm-node-2", "vm-node-3", "vm-node-4", "vm-node-5",
	}
}

// Orchestrator is the seam through which a REAL infrastructure orchestrator
// (one that actually boots containers/VMs, probes services over the network,
// streams real logs, and scales real replicas) is injected.
//
// Contract for a REAL implementation:
//   - Boot MUST actually start each named service (container / process / VM)
//     and only report Status "running" / Healthy true after a real readiness
//     probe to that service succeeds.
//   - Health MUST perform a live probe (TCP/HTTP/gRPC/PING/etc.) against the
//     service and report Healthy false for a service that is down or absent,
//     with Latency measured from the actual round-trip — never a constant.
//   - Logs MUST stream real log output from the running service, honoring
//     follow/tail/since/timestamps.
//   - Scale MUST create/destroy real replicas observable in the runtime.
//   - VMSpawn/VMDestroy/VMSSH MUST create/destroy reachable nodes and return
//     an SSH target that actually accepts a session.
//
// The default constructor (NewOrchestrator) returns simulatedOrchestrator,
// which satisfies NONE of the above guarantees — see its documentation.
type Orchestrator interface {
	Boot(ctx context.Context, services []string) ([]ServiceStatus, error)
	Stop(ctx context.Context, services []string, removeVolumes bool) ([]ServiceStatus, error)
	Status(ctx context.Context, services []string) ([]ServiceStatus, error)
	Health(ctx context.Context, services []string) ([]ServiceStatus, error)
	Logs(ctx context.Context, service string, follow bool, tail int, since string, timestamps bool) error
	Scale(ctx context.Context, service string, replicas int) (ServiceStatus, error)
	VMSpawn(ctx context.Context, count int) ([]VMNode, error)
	VMDestroy(ctx context.Context, nodeID string) error
	VMList(ctx context.Context) ([]VMNode, error)
	VMStatus(ctx context.Context, nodeID string) (VMNode, error)
	VMSSH(ctx context.Context, nodeID string) (string, error)
	VMSimulateFailure(ctx context.Context, nodeID string) error
	VMSimulatePartition(ctx context.Context, nodeID string, duration time.Duration) error
	// Simulated reports whether this orchestrator is the NON-PRODUCTION
	// simulation. A real implementation MUST return false.
	Simulated() bool
}

// simulatedOrchestrator is a NON-PRODUCTION, SIMULATED infrastructure
// orchestrator.
//
// WARNING (PCS-6 / CLAUDE-1): This orchestrator does NOT perform ANY real
// infrastructure operation. It boots NOTHING (no container, no process, no VM),
// probes NOTHING (Health never opens a socket — it echoes the canned Healthy
// flag that Boot unconditionally set to true), streams NO logs (Logs only
// checks the in-memory map and returns nil; follow/tail/since/timestamps are
// ignored), creates NO real replicas (Scale only writes a struct field), and
// spawns NO real machines (VMSpawn fabricates CPU/Memory/IP constants with no
// hypervisor or cloud call; VMSSH returns a formatted "ssh user@<ip>" string
// for a host that does not exist and is not reachable).
//
// Every method is pure in-memory bookkeeping over two maps. A green test using
// this type proves ONLY that the bookkeeping state machine behaves (insert /
// lookup / mutate / delete, plus the partition auto-heal timer) — it is NOT
// evidence that any service booted, is healthy, is reachable, or is usable by
// an end user. Inject a real Orchestrator (see the Orchestrator interface and
// infra_integration_test.go) for production use.
//
// The type is intentionally named so it is unmistakable in stack traces, logs,
// and code review that this is a fake.
type simulatedOrchestrator struct {
	mu       sync.RWMutex
	config   *InfraConfig
	services map[string]*ServiceStatus
	vmNodes  map[string]*VMNode
}

// InfraOrchestrator is retained as a backward-compatible alias for the
// concrete simulation type. It carries the same NON-PRODUCTION guarantees as
// simulatedOrchestrator — see that type's WARNING documentation.
type InfraOrchestrator = simulatedOrchestrator

// NewOrchestrator creates a new infrastructure orchestrator from the given
// config.
//
// WARNING (PCS-6 / CLAUDE-1): The returned orchestrator is a
// simulatedOrchestrator — a NON-PRODUCTION simulation that boots nothing,
// probes nothing, and returns canned values. Its Simulated() method returns
// true. It exists so the control-plane plumbing and state machine can be
// exercised in unit tests; it MUST NOT be relied upon as a working
// infrastructure orchestrator. Wire a real Orchestrator implementation (per the
// Orchestrator interface) for production.
func NewOrchestrator(cfg *InfraConfig) *simulatedOrchestrator {
	return &simulatedOrchestrator{
		config:   cfg,
		services: make(map[string]*ServiceStatus),
		vmNodes:  make(map[string]*VMNode),
	}
}

// Simulated reports that this orchestrator is the NON-PRODUCTION simulation. It
// always returns true. Callers and tests MUST assert o.Simulated() to confirm
// they are running against the fake rather than a real orchestrator.
func (o *simulatedOrchestrator) Simulated() bool { return true }

// compile-time assertion that the simulation satisfies the Orchestrator seam.
var _ Orchestrator = (*simulatedOrchestrator)(nil)

// Boot starts the specified services. If no services are specified, all are started.
func (o *simulatedOrchestrator) Boot(ctx context.Context, services []string) ([]ServiceStatus, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(services) == 0 {
		services = o.config.Services
	}
	results := make([]ServiceStatus, 0, len(services))
	for _, name := range services {
		status := ServiceStatus{
			Name:    name,
			Status:  "running",
			Healthy: true,
			Ports:   []string{},
			Message: "booted",
		}
		o.services[name] = &status
		results = append(results, status)
	}
	return results, nil
}

// Stop stops the specified services. If no services are specified, all are stopped.
func (o *simulatedOrchestrator) Stop(ctx context.Context, services []string, removeVolumes bool) ([]ServiceStatus, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(services) == 0 {
		for name := range o.services {
			services = append(services, name)
		}
	}
	results := make([]ServiceStatus, 0, len(services))
	for _, name := range services {
		status := ServiceStatus{
			Name:    name,
			Status:  "stopped",
			Healthy: false,
			Message: "stopped",
		}
		if removeVolumes {
			status.Message = "stopped and volumes removed"
		}
		o.services[name] = &status
		results = append(results, status)
	}
	return results, nil
}

// Status returns the status of the specified services, or all if none specified.
func (o *simulatedOrchestrator) Status(ctx context.Context, services []string) ([]ServiceStatus, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if len(services) == 0 {
		for name := range o.services {
			services = append(services, name)
		}
		if len(services) == 0 {
			services = o.config.Services
		}
	}
	results := make([]ServiceStatus, 0, len(services))
	for _, name := range services {
		if s, ok := o.services[name]; ok {
			results = append(results, *s)
		} else {
			results = append(results, ServiceStatus{
				Name:    name,
				Status:  "not_found",
				Healthy: false,
				Message: "service not found",
			})
		}
	}
	return results, nil
}

// Health runs health checks for the specified services, or all if none specified.
func (o *simulatedOrchestrator) Health(ctx context.Context, services []string) ([]ServiceStatus, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(services) == 0 {
		for name := range o.services {
			services = append(services, name)
		}
		if len(services) == 0 {
			services = o.config.Services
		}
	}
	results := make([]ServiceStatus, 0, len(services))
	for _, name := range services {
		if s, ok := o.services[name]; ok {
			s.Latency = 1 * time.Millisecond
			results = append(results, *s)
		} else {
			results = append(results, ServiceStatus{
				Name:    name,
				Status:  "not_found",
				Healthy: false,
				Message: "service not found",
			})
		}
	}
	return results, nil
}

// Logs streams logs from a service.
func (o *simulatedOrchestrator) Logs(ctx context.Context, service string, follow bool, tail int, since string, timestamps bool) error {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if _, ok := o.services[service]; !ok {
		return fmt.Errorf("service %s not found", service)
	}
	return nil
}

// Scale scales a service to N replicas.
func (o *simulatedOrchestrator) Scale(ctx context.Context, service string, replicas int) (ServiceStatus, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s, ok := o.services[service]; ok {
		s.Replicas = replicas
		s.Message = fmt.Sprintf("scaled to %d replicas", replicas)
		return *s, nil
	}
	status := ServiceStatus{
		Name:     service,
		Status:   "running",
		Healthy:  true,
		Replicas: replicas,
		Message:  fmt.Sprintf("scaled to %d replicas", replicas),
	}
	o.services[service] = &status
	return status, nil
}

// VMSpawn spawns N VM nodes.
func (o *simulatedOrchestrator) VMSpawn(ctx context.Context, count int) ([]VMNode, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	nodes := make([]VMNode, 0, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("vm-%d", len(o.vmNodes)+i+1)
		node := VMNode{
			ID:      id,
			Name:    id,
			Status:  "running",
			IP:      fmt.Sprintf("10.0.0.%d", len(o.vmNodes)+i+1),
			CPU:     2,
			Memory:  4096,
			Labels:  map[string]string{},
			Created: time.Now(),
		}
		o.vmNodes[id] = &node
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// VMDestroy destroys a VM node by ID.
func (o *simulatedOrchestrator) VMDestroy(ctx context.Context, nodeID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.vmNodes[nodeID]; !ok {
		return fmt.Errorf("vm node %s not found", nodeID)
	}
	delete(o.vmNodes, nodeID)
	return nil
}

// VMList lists all VM nodes.
func (o *simulatedOrchestrator) VMList(ctx context.Context) ([]VMNode, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	nodes := make([]VMNode, 0, len(o.vmNodes))
	for _, node := range o.vmNodes {
		nodes = append(nodes, *node)
	}
	return nodes, nil
}

// VMStatus returns the status of a specific VM node.
func (o *simulatedOrchestrator) VMStatus(ctx context.Context, nodeID string) (VMNode, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if node, ok := o.vmNodes[nodeID]; ok {
		return *node, nil
	}
	return VMNode{}, fmt.Errorf("vm node %s not found", nodeID)
}

// VMSSH returns the SSH command/connection string for a VM node.
func (o *simulatedOrchestrator) VMSSH(ctx context.Context, nodeID string) (string, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	node, ok := o.vmNodes[nodeID]
	if !ok {
		return "", fmt.Errorf("vm node %s not found", nodeID)
	}
	return fmt.Sprintf("ssh user@%s", node.IP), nil
}

// VMSimulateFailure simulates a failure on a VM node.
func (o *simulatedOrchestrator) VMSimulateFailure(ctx context.Context, nodeID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	node, ok := o.vmNodes[nodeID]
	if !ok {
		return fmt.Errorf("vm node %s not found", nodeID)
	}
	node.Status = "failed"
	return nil
}

// VMSimulatePartition simulates a network partition on a VM node for a given duration.
func (o *simulatedOrchestrator) VMSimulatePartition(ctx context.Context, nodeID string, duration time.Duration) error {
	o.mu.Lock()
	node, ok := o.vmNodes[nodeID]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("vm node %s not found", nodeID)
	}
	node.Status = "partitioned"
	o.mu.Unlock()
	go func() {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-timer.C:
			o.mu.Lock()
			node.Status = "running"
			o.mu.Unlock()
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		}
	}()
	return nil
}
