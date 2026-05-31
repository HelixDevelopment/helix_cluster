// Package discovery provides tests for the etcd-backed discovery backend.
package discovery

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// mockEtcdClient satisfies the EtcdClient interface used by EtcdBackend.
type mockEtcdClient struct {
	mu   sync.RWMutex
	data map[string]string
}

func newMockEtcdClient() *mockEtcdClient {
	return &mockEtcdClient{data: make(map[string]string)}
}

func (m *mockEtcdClient) Put(ctx context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *mockEtcdClient) PutWithLease(ctx context.Context, key, value string, leaseID clientv3.LeaseID) error {
	return m.Put(ctx, key, value)
}

func (m *mockEtcdClient) GetPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string)
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out, nil
}

func (m *mockEtcdClient) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *mockEtcdClient) Watch(ctx context.Context, key string) <-chan etcd.WatchEvent {
	ch := make(chan etcd.WatchEvent)
	close(ch)
	return ch
}

func (m *mockEtcdClient) Lease(ctx context.Context, ttl int64) (clientv3.LeaseID, error) {
	return 1, nil
}

func (m *mockEtcdClient) KeepAlive(ctx context.Context, id clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	ch := make(chan *clientv3.LeaseKeepAliveResponse)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func TestEtcdBackendPutAndGet(t *testing.T) {
	mock := newMockEtcdClient()
	b := NewEtcdBackend(mock, "test")

	ctx := context.Background()
	inst := &Instance{
		ID:      "i1",
		Service: "svc",
		Address: "10.0.0.1",
		Port:    80,
		Healthy: true,
	}

	if err := b.Put(ctx, "svc/i1", inst); err != nil {
		t.Fatalf("Put: %v", err)
	}

	list, err := b.List(ctx, "svc/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(list))
	}
	if list[0].ID != "i1" {
		t.Errorf("id = %s, want i1", list[0].ID)
	}
}

func TestEtcdBackendDelete(t *testing.T) {
	mock := newMockEtcdClient()
	b := NewEtcdBackend(mock, "test")

	ctx := context.Background()
	inst := &Instance{ID: "i2", Service: "svc", Address: "10.0.0.2", Port: 80, Healthy: true}
	_ = b.Put(ctx, "svc/i2", inst)

	if err := b.Delete(ctx, "svc/i2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	list, err := b.List(ctx, "svc/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 instances, got %d", len(list))
	}
}

func TestEtcdBackendWatch(t *testing.T) {
	mock := newMockEtcdClient()
	b := NewEtcdBackend(mock, "test")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := b.Watch(ctx, "svc/")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Write an instance; because the mock watch is a no-op, we just verify
	// the channel is created and closes when context is cancelled.
	inst := &Instance{ID: "i3", Service: "svc", Address: "10.0.0.3", Port: 80, Healthy: true}
	_ = b.Put(ctx, "svc/i3", inst)

	// Give the watcher goroutine a moment.
	time.Sleep(50 * time.Millisecond)

	// Cancel context and ensure channel closes.
	cancel()
	select {
	case <-ch:
		// expected closed
	case <-time.After(time.Second):
		t.Error("watch channel did not close")
	}
}

func TestEtcdBackendPrefix(t *testing.T) {
	mock := newMockEtcdClient()
	b := NewEtcdBackend(mock, "prefix")

	ctx := context.Background()
	_ = b.Put(ctx, "a/1", &Instance{ID: "1", Service: "a", Address: "10.0.0.1", Port: 80, Healthy: true})
	_ = b.Put(ctx, "a/2", &Instance{ID: "2", Service: "a", Address: "10.0.0.2", Port: 80, Healthy: true})
	_ = b.Put(ctx, "b/1", &Instance{ID: "3", Service: "b", Address: "10.0.0.3", Port: 80, Healthy: true})

	list, err := b.List(ctx, "a/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 instances, got %d", len(list))
	}
}
