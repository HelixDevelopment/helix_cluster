package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
	"github.com/stretchr/testify/suite"
)

// EtcdClientMock is a thread-safe mock of pkg/etcd.Client for integration tests.
type EtcdClientMock struct {
	mu      sync.RWMutex
	data    map[string]string
	leases  map[int64]int64 // leaseID -> TTL
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

// IntegrationSuite provides shared infrastructure for integration tests.
type IntegrationSuite struct {
	suite.Suite
	ctx    context.Context
	cancel context.CancelFunc
	// Shared resources that tests may use.
	DiscoveryRegistry *discovery.ServiceRegistry
	EtcdMock          *EtcdClientMock
}

// SetupSuite creates shared resources before any test runs.
func (s *IntegrationSuite) SetupSuite() {
	s.ctx, s.cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	s.EtcdMock = NewEtcdClientMock()
	s.DiscoveryRegistry = discovery.NewServiceRegistry(discovery.NewInMemoryBackend())
}

// TeardownSuite cleans up shared resources after all tests finish.
func (s *IntegrationSuite) TeardownSuite() {
	if s.cancel != nil {
		s.cancel()
	}
}

// SetupTest runs before each individual test.
func (s *IntegrationSuite) SetupTest() {
	// Ensure a fresh context per test if the parent expired.
	if s.ctx.Err() != nil {
		s.ctx, s.cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	}
}

// TearDownTest runs after each individual test.
func (s *IntegrationSuite) TearDownTest() {
	// No per-test cleanup needed; each test file constructs its own resources.
}

func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationSuite))
}
