// Package discovery — LastSeen freshness regression test.
//
// Observed (live) defect: a registered node's etcd descriptor LastSeen field
// stays frozen at registration time while its lease is actively renewed every
// keep-alive cycle. Liveness is correctly lease-backed, but any consumer that
// reads LastSeen to judge recency gets a stale (hours-old) value because the
// descriptor is only re-written on full re-registration.
//
// Fix: the lease keep-alive loop re-Puts the descriptor with a refreshed
// LastSeen on each renewal tick (same lease, cadence-matched — no thrash), so
// the persisted LastSeen reflects actual recency for as long as the node lives.
package discovery

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// keepAlivePulseClient lets a test drive keep-alive responses on demand and
// inspect the value last written under the lease. It models real etcd, which
// emits a LeaseKeepAliveResponse on the channel each time it renews the lease.
type keepAlivePulseClient struct {
	mu        sync.Mutex
	data      map[string]string
	pulse     chan *clientv3.LeaseKeepAliveResponse
	kaStarted chan struct{}
}

func newKeepAlivePulseClient() *keepAlivePulseClient {
	return &keepAlivePulseClient{
		data:      make(map[string]string),
		pulse:     make(chan *clientv3.LeaseKeepAliveResponse, 1),
		kaStarted: make(chan struct{}, 1),
	}
}

func (c *keepAlivePulseClient) Put(ctx context.Context, key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
	return nil
}

func (c *keepAlivePulseClient) PutWithLease(ctx context.Context, key, value string, _ clientv3.LeaseID) error {
	return c.Put(ctx, key, value)
}

func (c *keepAlivePulseClient) GetPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.data))
	for k, v := range c.data {
		if len(prefix) == 0 || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			out[k] = v
		}
	}
	return out, nil
}

func (c *keepAlivePulseClient) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, key)
	return nil
}

func (c *keepAlivePulseClient) Watch(ctx context.Context, key string) <-chan etcd.WatchEvent {
	// Unused by this test; return a closed channel.
	ch := make(chan etcd.WatchEvent)
	close(ch)
	return ch
}

func (c *keepAlivePulseClient) Lease(ctx context.Context, ttl int64) (clientv3.LeaseID, error) {
	return 7, nil
}

func (c *keepAlivePulseClient) KeepAlive(ctx context.Context, id clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	out := make(chan *clientv3.LeaseKeepAliveResponse)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case resp, ok := <-c.pulse:
				if !ok {
					return
				}
				select {
				case out <- resp:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	select {
	case c.kaStarted <- struct{}{}:
	default:
	}
	return out, nil
}

// storedLastSeen reads back the persisted descriptor for key and returns its
// LastSeen field. It is the sink-side observation a freshness consumer makes.
func (c *keepAlivePulseClient) storedLastSeen(t *testing.T, key string) time.Time {
	t.Helper()
	c.mu.Lock()
	v, ok := c.data[key]
	c.mu.Unlock()
	if !ok {
		t.Fatalf("no value stored under %q", key)
	}
	var inst Instance
	if err := json.Unmarshal([]byte(v), &inst); err != nil {
		t.Fatalf("unmarshal stored descriptor: %v", err)
	}
	return inst.LastSeen
}

// TestKeepAliveRefreshesLastSeen is the freshness regression guard.
//
// RED before fix: keepAlive drains keep-alive responses and never re-Puts the
// descriptor, so the stored LastSeen stays at the registration timestamp even
// after the lease is renewed — a stale value handed to freshness consumers.
//
// GREEN after fix: each keep-alive renewal re-Puts the descriptor with
// LastSeen = clock.Now(), so the persisted LastSeen advances with the lease.
func TestKeepAliveRefreshesLastSeen(t *testing.T) {
	client := newKeepAlivePulseClient()
	b := NewEtcdBackend(client, "test")

	// Deterministic clock so we can assert an exact advance without real sleeps.
	// fixedClock is the package's existing mutex-guarded test Clock.
	clk := &fixedClock{t: time.Unix(1_000_000, 0).UTC()}
	b.setClock(clk)

	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	defer lifeCancel()
	b.SetKeepAliveContext(lifeCtx)

	regTime := clk.Now()
	inst := &Instance{
		ID:       "n1",
		Service:  "nodes",
		Address:  "10.0.0.1",
		Port:     80,
		Healthy:  true,
		TTL:      30 * time.Second,
		LastSeen: regTime,
	}
	ek := "test/nodes/n1"

	if err := b.Put(context.Background(), "nodes/n1", inst); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Sanity: at registration the stored LastSeen equals regTime.
	if got := client.storedLastSeen(t, ek); !got.Equal(regTime) {
		t.Fatalf("stored LastSeen at registration = %v, want %v", got, regTime)
	}

	// Wait for keep-alive to have started.
	select {
	case <-client.kaStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("keep-alive goroutine never started")
	}

	// Advance the clock and fire ONE keep-alive renewal — exactly what real etcd
	// does roughly every TTL/3.
	clk.set(clk.Now().Add(10 * time.Second))
	wantLastSeen := clk.Now()
	client.pulse <- &clientv3.LeaseKeepAliveResponse{ID: 7, TTL: 30}

	// Poll the sink: the stored descriptor's LastSeen must advance to reflect the
	// renewal. Before the fix this never happens (RED → timeout/fatal).
	deadline := time.After(2 * time.Second)
	for {
		got := client.storedLastSeen(t, ek)
		if got.Equal(wantLastSeen) {
			break // GREEN: LastSeen advanced with the lease renewal
		}
		select {
		case <-deadline:
			t.Fatalf("stored LastSeen did not advance after keep-alive renewal: got %v, want %v (LastSeen is stale while lease is renewed)", got, wantLastSeen)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
