package e2e

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/build"
	internalgateway "github.com/HelixDevelopment/helix_cluster/internal/gateway"
	internalhealth "github.com/HelixDevelopment/helix_cluster/internal/health"
	internalscheduler "github.com/HelixDevelopment/helix_cluster/internal/scheduler"
	internalsession "github.com/HelixDevelopment/helix_cluster/internal/session"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// EtcdClientMock is a thread-safe mock of pkg/etcd.Client for e2e tests.
type EtcdClientMock struct {
	mu      sync.RWMutex
	data    map[string]string
	leases  map[int64]int64
	watchCh map[string]chan etcd.WatchEvent
}

// NewEtcdClientMock creates a new mock etcd client.
func NewEtcdClientMock() *EtcdClientMock {
	return &EtcdClientMock{
		data:    make(map[string]string),
		leases:  make(map[int64]int64),
		watchCh: make(map[string]chan etcd.WatchEvent),
	}
}

func (m *EtcdClientMock) Put(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *EtcdClientMock) PutWithLease(_ context.Context, key, value string, leaseID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	m.leases[leaseID] = 60
	return nil
}

func (m *EtcdClientMock) GetPrefix(_ context.Context, prefix string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string)
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out[k] = v
		}
	}
	return out, nil
}

func (m *EtcdClientMock) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *EtcdClientMock) Watch(_ context.Context, key string) <-chan etcd.WatchEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan etcd.WatchEvent, 10)
	m.watchCh[key] = ch
	return ch
}

func (m *EtcdClientMock) Lease(_ context.Context, ttl int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := int64(len(m.leases) + 1)
	m.leases[id] = ttl
	return id, nil
}

func (m *EtcdClientMock) KeepAlive(_ context.Context, _ int64) (<-chan any, error) {
	ch := make(chan any, 1)
	return ch, nil
}

// InProcessCluster holds all in-process services for e2e testing.
type InProcessCluster struct {
	Ctx    context.Context
	Cancel context.CancelFunc

	EtcdMock    *EtcdClientMock
	Discovery   *discovery.ServiceRegistry
	HealthAgg   *internalhealth.Aggregator
	HealthSrv   *internalhealth.Server
	Gateway     *internalgateway.Gateway
	Scheduler   *internalscheduler.Server
	Session     *internalsession.Server
	BuildOrch   *build.Orchestrator
	BuildPool   *build.WorkerPool
	BuildSrv    *build.Server

	grpcServer *grpc.Server
	listener   net.Listener
}

// NewInProcessCluster creates and starts all services in-process.
func NewInProcessCluster(t testing.TB) *InProcessCluster {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	// 1. In-memory etcd mock
	etcdMock := NewEtcdClientMock()

	// 2. Discovery registry
	backend := discovery.NewInMemoryBackend()
	reg := discovery.NewServiceRegistry(backend)

	// 3. Health aggregator + server
	agg := internalhealth.NewAggregator()
	agg.RegisterCheck("etcd", internalhealth.CheckFunc(func(_ context.Context) (internalhealth.Status, error) {
		return internalhealth.Healthy, nil
	}))
	agg.RegisterCheck("gateway", internalhealth.CheckFunc(func(_ context.Context) (internalhealth.Status, error) {
		return internalhealth.Healthy, nil
	}))
	agg.RegisterCheck("scheduler", internalhealth.CheckFunc(func(_ context.Context) (internalhealth.Status, error) {
		return internalhealth.Healthy, nil
	}))
	agg.RegisterCheck("session", internalhealth.CheckFunc(func(_ context.Context) (internalhealth.Status, error) {
		return internalhealth.Healthy, nil
	}))
	agg.RegisterCheck("build", internalhealth.CheckFunc(func(_ context.Context) (internalhealth.Status, error) {
		return internalhealth.Healthy, nil
	}))
	agg.RunChecks(ctx)
	healthSrv := internalhealth.NewServer(agg)

	// 4. Gateway
	gw, err := internalgateway.NewGateway()
	if err != nil {
		cancel()
		t.Fatalf("gateway init: %v", err)
	}

	// 5. Scheduler server
	sched := internalscheduler.NewServer()

	// 6. Session server — in-memory (nil) backend for the e2e harness; the real
	// tmux backend path is proven separately by HXC-1136's dedicated E2E.
	sess := internalsession.NewServerWithTmuxBackend(nil)

	// 7. Build service
	pool := build.NewWorkerPool()
	orch := build.NewOrchestrator(2, nil)
	orch.Start(ctx)
	buildSrv := build.NewServer(orch, pool)

	// 8. gRPC server (bind to random port)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	buildSrv.Register(grpcSrv)

	go func() {
		if err := grpcSrv.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			// Log but don't fail; tests will fail on other assertions.
		}
	}()

	return &InProcessCluster{
		Ctx:         ctx,
		Cancel:      cancel,
		EtcdMock:    etcdMock,
		Discovery:   reg,
		HealthAgg:   agg,
		HealthSrv:   healthSrv,
		Gateway:     gw,
		Scheduler:   sched,
		Session:     sess,
		BuildOrch:   orch,
		BuildPool:   pool,
		BuildSrv:    buildSrv,
		grpcServer:  grpcSrv,
		listener:    lis,
	}
}

// GatewayAddr returns the gateway HTTP address (not started by default).
func (c *InProcessCluster) GatewayAddr() string {
	return ""
}

// GRPCAddr returns the bound gRPC address.
func (c *InProcessCluster) GRPCAddr() string {
	return c.listener.Addr().String()
}

// NewGRPCConn creates a new gRPC client connection to the in-process server.
func (c *InProcessCluster) NewGRPCConn(t testing.TB) *grpc.ClientConn {
	conn, err := grpc.NewClient(c.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	return conn
}

// Stop gracefully shuts down all services.
func (c *InProcessCluster) Stop() {
	c.grpcServer.GracefulStop()
	c.BuildOrch.Stop()
	c.Cancel()
}

// E2ESuite provides shared infrastructure for e2e tests.
type E2ESuite struct {
	suite.Suite
	Cluster *InProcessCluster
}

func (s *E2ESuite) SetupSuite() {
	s.Cluster = NewInProcessCluster(s.T())
}

func (s *E2ESuite) TearDownSuite() {
	if s.Cluster != nil {
		s.Cluster.Stop()
	}
}

func TestE2ESuite(t *testing.T) {
	suite.Run(t, new(E2ESuite))
}

// Helper to wait for a condition with timeout.
func waitFor(condition func() bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("condition not met within %v", timeout)
}

// Helper to run health checks and assert healthy.
func (s *E2ESuite) runHealthChecks() {
	st, _ := s.Cluster.HealthAgg.StatusWithDetails()
	s.Equal(internalhealth.Healthy, st, "expected overall health to be healthy")
}
