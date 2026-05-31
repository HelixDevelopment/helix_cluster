// Package etcd provides a client wrapper for etcd in Helix Cluster OS.
package etcd

import (
	"context"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// Client wraps go.etcd.io/etcd/client/v3 with convenience methods.
type Client struct {
	cli *clientv3.Client
}

// Config holds connection parameters for the etcd client.
type Config struct {
	Endpoints   []string
	DialTimeout time.Duration
}

// New creates a new etcd Client.
func New(cfg Config) (*Client, error) {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: cfg.DialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create etcd client: %w", err)
	}
	return &Client{cli: cli}, nil
}

// Put stores a key-value pair in etcd.
func (c *Client) Put(ctx context.Context, key, value string) error {
	_, err := c.cli.Put(ctx, key, value)
	if err != nil {
		return fmt.Errorf("etcd put failed: %w", err)
	}
	return nil
}

// PutWithLease stores a key-value pair with a lease (TTL).
func (c *Client) PutWithLease(ctx context.Context, key, value string, leaseID clientv3.LeaseID) error {
	_, err := c.cli.Put(ctx, key, value, clientv3.WithLease(leaseID))
	if err != nil {
		return fmt.Errorf("etcd put with lease failed: %w", err)
	}
	return nil
}

// Get retrieves the value for a given key.
// If the key does not exist, value is empty and ok is false.
func (c *Client) Get(ctx context.Context, key string) (value string, ok bool, err error) {
	resp, err := c.cli.Get(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("etcd get failed: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return "", false, nil
	}
	return string(resp.Kvs[0].Value), true, nil
}

// GetPrefix retrieves all key-value pairs with the given prefix.
func (c *Client) GetPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	resp, err := c.cli.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("etcd get prefix failed: %w", err)
	}
	out := make(map[string]string, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		out[string(kv.Key)] = string(kv.Value)
	}
	return out, nil
}

// Delete removes a key from etcd.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.cli.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("etcd delete failed: %w", err)
	}
	return nil
}

// DeletePrefix removes all keys with the given prefix.
func (c *Client) DeletePrefix(ctx context.Context, prefix string) error {
	_, err := c.cli.Delete(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("etcd delete prefix failed: %w", err)
	}
	return nil
}

// Watch watches for changes on a key and sends events on the returned channel.
// The channel is closed when the context is cancelled.
func (c *Client) Watch(ctx context.Context, key string) <-chan WatchEvent {
	ch := make(chan WatchEvent)
	go func() {
		defer close(ch)
		watchCh := c.cli.Watch(ctx, key)
		for wresp := range watchCh {
			if wresp.Err() != nil {
				select {
				case ch <- WatchEvent{Err: wresp.Err()}:
				case <-ctx.Done():
					return
				}
				continue
			}
			for _, ev := range wresp.Events {
				// ev.Type is an mvccpb.Event_EventType enum (PUT=0, DELETE=1).
				// Use String() so the field carries the human-readable token
				// ("PUT"/"DELETE"); a plain string(ev.Type) conversion would
				// emit the rune for the integer value (e.g. "\x00" for PUT),
				// which is unusable to any consumer matching on the type name.
				we := WatchEvent{
					Type:  ev.Type.String(),
					Key:   string(ev.Kv.Key),
					Value: string(ev.Kv.Value),
				}
				select {
				case ch <- we:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

// WatchEvent represents a single watch event.
type WatchEvent struct {
	Type  string
	Key   string
	Value string
	Err   error
}

// Lease grants a lease with the given TTL in seconds and returns the LeaseID.
func (c *Client) Lease(ctx context.Context, ttl int64) (clientv3.LeaseID, error) {
	resp, err := c.cli.Grant(ctx, ttl)
	if err != nil {
		return 0, fmt.Errorf("etcd grant lease failed: %w", err)
	}
	return resp.ID, nil
}

// RevokeLease revokes a lease, deleting all keys attached to it.
func (c *Client) RevokeLease(ctx context.Context, id clientv3.LeaseID) error {
	_, err := c.cli.Revoke(ctx, id)
	if err != nil {
		return fmt.Errorf("etcd revoke lease failed: %w", err)
	}
	return nil
}

// KeepAlive keeps a lease alive indefinitely.
// The returned channel receives keep-alive responses. Close the channel to stop.
func (c *Client) KeepAlive(ctx context.Context, id clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	ch, err := c.cli.KeepAlive(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("etcd keep alive failed: %w", err)
	}
	return ch, nil
}

// Lock acquires a distributed mutex on the given key.
// The returned function must be called to release the lock.
func (c *Client) Lock(ctx context.Context, key string) (unlock func() error, err error) {
	s, err := concurrency.NewSession(c.cli)
	if err != nil {
		return nil, fmt.Errorf("failed to create concurrency session: %w", err)
	}
	mu := concurrency.NewMutex(s, key)
	if err := mu.Lock(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}
	return func() error {
		defer s.Close()
		return mu.Unlock(ctx)
	}, nil
}

// Close closes the etcd client connection.
func (c *Client) Close() error {
	if c == nil || c.cli == nil {
		return nil
	}
	return c.cli.Close()
}

// Raw returns the underlying *clientv3.Client for advanced use cases.
func (c *Client) Raw() *clientv3.Client {
	return c.cli
}
