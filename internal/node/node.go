package node

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
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

	protocol     *swim.Protocol
	wg           *wireguard.Manager
	registry     *discovery.ServiceRegistry
	aggregator   *resources.NodeAggregator
	pollInterval time.Duration
	effectiveTTL time.Duration

	// backendType records which discovery backend is in use: "etcd" or "in-memory".
	backendType string
	// etcdClient is non-nil when the etcd backend is selected; closed in Stop().
	etcdClient *etcd.Client

	mu     sync.RWMutex
	status Status
	ctx    context.Context
	cancel context.CancelFunc
	wgProc sync.WaitGroup
}

// Status represents the operational state of a node agent.
type Status string

const (
	StatusStarting  Status = "starting"
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
	StatusStopping  Status = "stopping"
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
	// EtcdEndpoints, when non-empty, selects the etcd discovery backend.
	// If empty the in-memory backend is used (single-node / testing).
	EtcdEndpoints []string
}

// swimDeadHorizonDefault is the SWIM dead-detection horizon computed from the
// default SuspicionMult (4) × ProbeInterval (1 s). This is the minimum TTL
// an etcd lease must carry so only genuinely dead nodes expire.
const swimDeadHorizonDefault = 4 * time.Second

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

	// Compute the effective TTL: clamp cfg.DiscoveryTTL to be at least the SWIM
	// dead-detection horizon so the etcd lease auto-expires only genuinely dead
	// nodes (not ones that are merely slow to heartbeat).
	effectiveTTL := cfg.DiscoveryTTL
	if effectiveTTL < swimDeadHorizonDefault {
		effectiveTTL = swimDeadHorizonDefault
	}
	a.effectiveTTL = effectiveTTL

	// Initialize discovery registry — select etcd or in-memory backend.
	if len(cfg.EtcdEndpoints) > 0 {
		ec, err := etcd.New(etcd.Config{
			Endpoints:   cfg.EtcdEndpoints,
			DialTimeout: 10 * time.Second,
		})
		if err != nil {
			cancel()
			return nil, fmt.Errorf("etcd client init: %w", err)
		}
		a.etcdClient = ec
		a.backendType = "etcd"
		const etcdPrefix = "helix-node"
		backend := discovery.NewEtcdBackend(ec, etcdPrefix)
		a.registry = discovery.NewServiceRegistry(backend)
	} else {
		a.backendType = "in-memory"
		backend := discovery.NewInMemoryBackend()
		a.registry = discovery.NewServiceRegistry(backend)
	}

	// Initialize resource aggregator
	a.aggregator = resources.NewNodeAggregator(cfg.ResourcePollInterval)

	// Resource poll interval drives the background poller. Fall back to a safe
	// default when the operator leaves it unset so the poller never spins.
	a.pollInterval = cfg.ResourcePollInterval
	if a.pollInterval <= 0 {
		a.pollInterval = 30 * time.Second
	}

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

	// Register with discovery — stamp TTL and resource metadata.
	inst := &discovery.Instance{
		ID:       a.ID,
		Service:  "helix-node",
		Address:  a.protocol.LocalAddr(),
		TTL:      a.effectiveTTL,
		Metadata: a.BuildInstanceMetadata(),
	}
	regCtx, regCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer regCancel()
	if err := a.registry.Register(regCtx, inst); err != nil {
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

	var errs []error
	deregCtx, deregCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer deregCancel()
	if err := a.registry.Deregister(deregCtx, "helix-node", a.ID); err != nil {
		errs = append(errs, fmt.Errorf("deregister: %w", err))
	}
	if err := a.wg.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("wireguard stop: %w", err))
	}
	if err := a.protocol.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("protocol stop: %w", err))
	}
	// Close the etcd client if we opened one.
	if a.etcdClient != nil {
		if err := a.etcdClient.Close(); err != nil {
			errs = append(errs, fmt.Errorf("etcd client close: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	return nil
}

// BackendType returns the name of the discovery backend in use: "etcd" or
// "in-memory". It is set once in NewAgent and never changes after that.
func (a *Agent) BackendType() string {
	return a.backendType
}

// EffectiveTTL returns the clamped discovery TTL that was stamped on the
// discovery Instance at registration time. It is guaranteed to be >= the SWIM
// dead-detection horizon (swimDeadHorizonDefault).
func (a *Agent) EffectiveTTL() time.Duration {
	return a.effectiveTTL
}

// BuildInstanceMetadata constructs the metadata map that is attached to the
// discovery Instance. It merges a.Labels with resource-capacity fields derived
// from a.Capacity (when non-nil). Callers receive a fresh copy; mutations do
// not affect Agent state.
func (a *Agent) BuildInstanceMetadata() map[string]string {
	meta := make(map[string]string)

	// Forward all labels first so capacity fields can coexist.
	for k, v := range a.Labels {
		meta[k] = v
	}

	// Attach resource capacity when available.
	if a.Capacity != nil {
		meta["cpu_cores"] = strconv.Itoa(a.Capacity.CPU.Cores)
		meta["memory_total_kb"] = strconv.FormatInt(a.Capacity.Memory.TotalKB, 10)
		meta["gpu_count"] = strconv.Itoa(a.Capacity.GPU.Count)
		meta["gpu_model"] = a.Capacity.GPU.Model
	}

	return meta
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
	ticker := time.NewTicker(a.pollInterval)
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
