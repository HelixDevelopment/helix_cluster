// Package discovery provides service discovery for Helix Cluster OS.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// EtcdClient abstracts the etcd.Client methods used by EtcdBackend.
type EtcdClient interface {
	Put(ctx context.Context, key, value string) error
	PutWithLease(ctx context.Context, key, value string, leaseID clientv3.LeaseID) error
	GetPrefix(ctx context.Context, prefix string) (map[string]string, error)
	Delete(ctx context.Context, key string) error
	Watch(ctx context.Context, key string) <-chan etcd.WatchEvent
	Lease(ctx context.Context, ttl int64) (clientv3.LeaseID, error)
	KeepAlive(ctx context.Context, id clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error)
}

// EtcdBackend implements discovery.Backend using an etcd client.
type EtcdBackend struct {
	client EtcdClient
	prefix string

	// keepAliveCtx is the LIFETIME context for background lease keep-alive
	// goroutines. It MUST outlive any single Put/Register RPC: the lease must
	// be renewed for as long as the node is alive, not just for the duration of
	// the short, bounded registration call. Defaults to context.Background()
	// (renews forever) and is overridden via SetKeepAliveContext so the agent
	// can tie keep-alive to its own shutdown context. D14 regression: previously
	// keep-alive inherited the per-call 10s registration context, which was
	// cancelled by `defer regCancel()` the instant Register returned — so the
	// lease expired after TTL and the /clusteros/nodes/<id> key vanished even
	// though the agent process stayed alive.
	keepAliveCtx context.Context

	// clock supplies the timestamp stamped into a node descriptor's LastSeen on
	// each lease keep-alive renewal, so the persisted LastSeen tracks actual
	// recency instead of freezing at registration time. Defaults to systemClock
	// (time.Now); overridable in tests for deterministic assertions. Read under
	// mu.RLock; replaced only via setClock.
	clock Clock

	mu       sync.RWMutex
	watchers map[string][]chan BackendEvent
}

// NewEtcdBackend creates a new etcd-backed discovery backend.
func NewEtcdBackend(client EtcdClient, prefix string) *EtcdBackend {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}
	return &EtcdBackend{
		client:       client,
		prefix:       prefix,
		keepAliveCtx: context.Background(),
		clock:        systemClock{},
		watchers:     make(map[string][]chan BackendEvent),
	}
}

// setClock overrides the timestamp source used to refresh LastSeen on lease
// keep-alive. A nil clock is ignored (the default systemClock is retained).
// Intended for deterministic tests.
func (b *EtcdBackend) setClock(c Clock) {
	if c == nil {
		return
	}
	b.mu.Lock()
	b.clock = c
	b.mu.Unlock()
}

// now returns the backend's current time via its Clock.
func (b *EtcdBackend) now() time.Time {
	b.mu.RLock()
	c := b.clock
	b.mu.RUnlock()
	if c == nil {
		return time.Now()
	}
	return c.Now()
}

// SetKeepAliveContext sets the lifetime context used for background lease
// keep-alive. Pass the agent's long-lived context (cancelled only on Stop) so
// the lease is renewed for the whole agent lifetime. A nil ctx is ignored
// (keeps the default context.Background()).
func (b *EtcdBackend) SetKeepAliveContext(ctx context.Context) {
	if ctx != nil {
		b.keepAliveCtx = ctx
	}
}

func (b *EtcdBackend) instanceKey(key string) string {
	return b.prefix + key
}

// Put stores an instance in etcd with optional TTL via lease.
func (b *EtcdBackend) Put(ctx context.Context, key string, inst *Instance) error {
	data, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("marshal instance: %w", err)
	}

	ek := b.instanceKey(key)
	var leaseID clientv3.LeaseID
	if inst.TTL > 0 {
		id, err := b.client.Lease(ctx, int64(inst.TTL.Seconds()))
		if err != nil {
			return fmt.Errorf("grant lease: %w", err)
		}
		leaseID = id
		// Start keep-alive in background bound to the LIFETIME context, NOT the
		// per-call ctx. The Lease/PutWithLease RPCs below keep using ctx (the
		// bounded registration context) for their own timeout, but the renewal
		// loop must survive long after Register returns. See D14 above.
		//
		// keepAlive also refreshes the descriptor's LastSeen on every renewal so
		// the persisted value tracks actual recency for the whole node lifetime,
		// rather than freezing at registration time while the lease keeps being
		// renewed. We hand it an independent clone keyed on this leased entry; the
		// goroutine owns that copy and re-Puts it under the same lease each tick.
		go b.keepAlive(b.keepAliveCtx, leaseID, ek, key, cloneInstance(inst))
	}

	if leaseID != 0 {
		err = b.client.PutWithLease(ctx, ek, string(data), leaseID)
	} else {
		err = b.client.Put(ctx, ek, string(data))
	}
	if err != nil {
		return fmt.Errorf("etcd put: %w", err)
	}

	b.notify(key, BackendEvent{Type: EventRegister, Key: key, Instance: inst})
	return nil
}

// keepAlive renews the lease for as long as ctx lives and, on each renewal,
// re-Puts the descriptor with a refreshed LastSeen so a freshness consumer
// reading /clusteros/nodes/<id> sees a current timestamp — not the stale
// registration-time value. The re-Put is cadence-matched to etcd's own
// keep-alive responses (~TTL/3), so it adds no extra etcd traffic beyond the
// renewals that already happen. inst is owned exclusively by this goroutine
// (the caller passed a clone), so mutating its LastSeen here is race-free.
func (b *EtcdBackend) keepAlive(ctx context.Context, id clientv3.LeaseID, ek, key string, inst *Instance) {
	ch, err := b.client.KeepAlive(ctx, id)
	if err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			b.refreshLastSeen(ctx, id, ek, key, inst)
		}
	}
}

// refreshLastSeen stamps inst.LastSeen with the current clock time and re-Puts
// the descriptor under the existing lease, then notifies watchers. Best-effort:
// a transient etcd write error is swallowed because the lease itself is still
// renewed (liveness is unaffected) and the next keep-alive tick retries.
func (b *EtcdBackend) refreshLastSeen(ctx context.Context, id clientv3.LeaseID, ek, key string, inst *Instance) {
	inst.LastSeen = b.now()
	data, err := json.Marshal(inst)
	if err != nil {
		return
	}
	if err := b.client.PutWithLease(ctx, ek, string(data), id); err != nil {
		return
	}
	b.notify(key, BackendEvent{Type: EventRegister, Key: key, Instance: cloneInstance(inst)})
}

// Delete removes an instance from etcd.
func (b *EtcdBackend) Delete(ctx context.Context, key string) error {
	ek := b.instanceKey(key)
	if err := b.client.Delete(ctx, ek); err != nil {
		return fmt.Errorf("etcd delete: %w", err)
	}
	b.notify(key, BackendEvent{Type: EventDeregister, Key: key, Instance: nil})
	return nil
}

// List returns all instances matching the prefix.
func (b *EtcdBackend) List(ctx context.Context, prefix string) ([]*Instance, error) {
	ek := b.instanceKey(prefix)
	kvs, err := b.client.GetPrefix(ctx, ek)
	if err != nil {
		return nil, fmt.Errorf("etcd list: %w", err)
	}

	var out []*Instance
	for _, v := range kvs {
		var inst Instance
		if err := json.Unmarshal([]byte(v), &inst); err != nil {
			continue
		}
		out = append(out, &inst)
	}
	return out, nil
}

// Watch returns a channel of backend events for keys matching the prefix.
func (b *EtcdBackend) Watch(ctx context.Context, prefix string) (<-chan BackendEvent, error) {
	ek := b.instanceKey(prefix)
	ch := make(chan BackendEvent, 10)

	b.mu.Lock()
	b.watchers[prefix] = append(b.watchers[prefix], ch)
	b.mu.Unlock()

	go func() {
		defer func() {
			b.mu.Lock()
			for i, c := range b.watchers[prefix] {
				if c == ch {
					b.watchers[prefix] = append(b.watchers[prefix][:i], b.watchers[prefix][i+1:]...)
					break
				}
			}
			b.mu.Unlock()
			close(ch)
		}()
		watchCh := b.client.Watch(ctx, ek)
		for we := range watchCh {
			if we.Err != nil {
				continue
			}
			inst, err := b.decodeValue(we.Value)
			if err != nil {
				continue
			}
			evtType := EventRegister
			if we.Type == "DELETE" {
				evtType = EventDeregister
			}
			select {
			case ch <- BackendEvent{Type: evtType, Key: strings.TrimPrefix(we.Key, b.prefix), Instance: inst}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

func (b *EtcdBackend) decodeValue(v string) (*Instance, error) {
	var inst Instance
	if err := json.Unmarshal([]byte(v), &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

func (b *EtcdBackend) notify(key string, evt BackendEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for prefix, chs := range b.watchers {
		if strings.HasPrefix(key, prefix) {
			for _, ch := range chs {
				select {
				case ch <- evt:
				default:
				}
			}
		}
	}
}
