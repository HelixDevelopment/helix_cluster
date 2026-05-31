// Package discovery provides service discovery for Helix Cluster OS.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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

	mu       sync.RWMutex
	watchers map[string][]chan BackendEvent
}

// NewEtcdBackend creates a new etcd-backed discovery backend.
func NewEtcdBackend(client EtcdClient, prefix string) *EtcdBackend {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}
	return &EtcdBackend{
		client:   client,
		prefix:   prefix,
		watchers: make(map[string][]chan BackendEvent),
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
		// Start keep-alive in background.
		go b.keepAlive(ctx, leaseID)
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

func (b *EtcdBackend) keepAlive(ctx context.Context, id clientv3.LeaseID) {
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
		}
	}
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
