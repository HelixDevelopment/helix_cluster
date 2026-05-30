package node

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
	"github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// Agent represents a single node in the HelixCluster.
type Agent struct {
	ID       string
	Region   string
	Labels   map[string]string
	Capacity *resources.NodeResources

	protocol   *swim.Protocol
	wg         *wireguard.Manager
	registry   *discovery.ServiceRegistry
	aggregator *resources.NodeAggregator

	mu      sync.RWMutex
	status  Status
	ctx     context.Context
	cancel  context.CancelFunc
	wgProc  sync.WaitGroup
}

// Status represents the operational state of a node agent.
type Status string

const (
	StatusStarting   Status = "starting"
	StatusHealthy    Status = "healthy"
	StatusDegraded   Status = "degraded"
	StatusUnhealthy  Status = "unhealthy"
	StatusStopping   Status = "stopping"
)

// Config holds agent configuration.
type Config struct {
	ID                   string
	Region               string
	Labels               map[string]string
	SwimBindAddr         string
	SwimBindPort         int
	SwimPeers            []string
	WgListenPort         int
	WgPrivateKey         string
	WgNoOp               bool // disable real WireGuard (for testing)
	DiscoveryTTL         time.Duration
	ResourcePollInterval time.Duration
}

// NewAgent creates a new node agent.
func NewAgent(cfg *Config) (*Agent, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	ctx, cancel := context.WithCancel(context.Background())

	a := &Agent{
		ID:     cfg.ID,
		Region: cfg.Region,
		Labels: cfg.Labels,
		status: StatusStarting,
		ctx:    ctx,
		cancel: cancel,
	}

	// Initialize SWIM protocol
	swimCfg := &swim.Config{
		LocalID:  cfg.ID,
		BindAddr: cfg.SwimBindAddr,
		BindPort: cfg.SwimBindPort,
	}
	protocol, err := swim.NewProtocol(swimCfg)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("swim init: %w", err)
	}
	a.protocol = protocol

	// Initialize WireGuard
	wgCfg := &wireguard.Config{
		ListenPort: cfg.WgListenPort,
		PrivateKey: cfg.WgPrivateKey,
		NoOp:       cfg.WgNoOp,
	}
	wgMgr, err := wireguard.NewManager(wgCfg)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("wireguard init: %w", err)
	}
	a.wg = wgMgr

	// Initialize discovery registry
	backend := discovery.NewInMemoryBackend()
	a.registry = discovery.NewServiceRegistry(backend)

	// Initialize resource aggregator
	a.aggregator = resources.NewNodeAggregator(cfg.ResourcePollInterval)

	return a, nil
}

// Start begins all agent subsystems.
func (a *Agent) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.status != StatusStarting {
		return fmt.Errorf("agent already started or stopped")
	}

	// Start SWIM gossip
	if err := a.protocol.Start(); err != nil {
		return fmt.Errorf("swim start: %w", err)
	}

	// Start WireGuard
	if err := a.wg.Start(); err != nil {
		return fmt.Errorf("wireguard start: %w", err)
	}

	// Start resource collection
	a.wgProc.Add(1)
	go a.resourcePoller()

	// Register with discovery
	inst := &discovery.Instance{
		ID:       a.ID,
		Service:  "helix-node",
		Address:  a.protocol.LocalAddr(),
		Metadata: a.Labels,
	}
	if err := a.registry.Register(context.Background(), inst); err != nil {
		return fmt.Errorf("discovery register: %w", err)
	}

	a.status = StatusHealthy
	return nil
}

// Stop gracefully shuts down the agent.
func (a *Agent) Stop() error {
	a.mu.Lock()
	a.status = StatusStopping
	a.mu.Unlock()

	a.cancel()
	a.wgProc.Wait()

	_ = a.registry.Deregister(context.Background(), "helix-node", a.ID)
	_ = a.wg.Stop()
	_ = a.protocol.Stop()

	return nil
}

// GetStatus returns the current agent status.
func (a *Agent) GetStatus() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

// CollectResources gathers current node resources.
func (a *Agent) CollectResources() error {
	return a.aggregator.Collect()
}

// resourcePoller periodically collects resource metrics.
func (a *Agent) resourcePoller() {
	defer a.wgProc.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			_ = a.aggregator.Collect()
		}
	}
}
