// Package storage — Redis-backed store for Helix Cluster OS.
//
// Key schema:
//
//	clusteros:cache:{k}             – generic cache entries
//	clusteros:routing:{sessionID}   – session routing hash (HSET)
//	clusteros:ratelimit:{subject}   – rate-limit counter (user or "global")
//	clusteros:events:nodes          – pub/sub channel for NodeEvent messages
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ─── Key builders ────────────────────────────────────────────────────────────

const (
	keyPrefixCache     = "clusteros:cache:"
	keyPrefixRouting   = "clusteros:routing:"
	keyPrefixRateLimit = "clusteros:ratelimit:"
	chanNodeEvents     = "clusteros:events:nodes"
)

// CacheKey returns the full Redis key for a generic cache entry.
func CacheKey(k string) string { return keyPrefixCache + k }

// RoutingKey returns the full Redis key for a session routing hash.
func RoutingKey(sessionID string) string { return keyPrefixRouting + sessionID }

// RateLimitKey returns the full Redis key for a rate-limit counter.
func RateLimitKey(subject string) string { return keyPrefixRateLimit + subject }

// NodeEventsChannel returns the Redis pub/sub channel for node events.
func NodeEventsChannel() string { return chanNodeEvents }

// ─── NodeEvent ────────────────────────────────────────────────────────────────

// NodeEvent represents a cluster-membership change published on the node-events
// channel.  Fields are kept flat (JSON-serialisable) to stay wire-compatible
// with any subscriber.
type NodeEvent struct {
	// Type is the event kind, e.g. "join", "leave", "update".
	Type string `json:"type"`
	// NodeID identifies the node that changed state.
	NodeID string `json:"node_id"`
	// Payload carries arbitrary per-event metadata.
	Payload map[string]string `json:"payload,omitempty"`
}

// ─── Seam interfaces (unit-testable without a live Redis) ────────────────────

// hashSetter is the write half of the HSET/EXPIRE operation used by
// SetSessionRouting.  Injected at construction so tests supply a fake.
type hashSetter interface {
	// HSet stores fields as a Redis hash at key.
	HSet(ctx context.Context, key string, fields map[string]string) error
	// Expire sets the TTL on key.
	Expire(ctx context.Context, key string, ttl time.Duration) error
	// Del removes key. Used to compensate a committed HSet when the follow-up
	// Expire fails, so SetSessionRouting never leaves an un-expiring orphan hash.
	Del(ctx context.Context, key string) error
}

// hashGetter is the read half used by GetSessionRouting.
type hashGetter interface {
	// HGetAll returns all field-value pairs stored in the hash at key.
	// A missing key returns an empty (non-nil) map and no error.
	HGetAll(ctx context.Context, key string) (map[string]string, error)
}

// publisher is the write half of the pub/sub seam used by PublishNodeEvent.
type publisher interface {
	// Publish sends payload to channel.
	Publish(ctx context.Context, channel, payload string) error
}

// subscriber is the read half used by SubscribeNodeEvents.
type subscriber interface {
	// Subscribe opens a subscription to channel.  The returned channel
	// delivers raw message payloads; it is closed when ctx is cancelled.
	Subscribe(ctx context.Context, channel string) (<-chan string, error)
}

// redisSeam bundles all four sub-interfaces so RedisStore takes one parameter.
type redisSeam interface {
	hashSetter
	hashGetter
	publisher
	subscriber
}

// ─── RedisStore ───────────────────────────────────────────────────────────────

// RedisStore provides session-routing, pub/sub, and cache helpers backed by
// Redis.  Construct it with NewRedisStore (real go-redis client) or with
// newRedisStoreFromSeam (tests).
type RedisStore struct {
	seam redisSeam
}

// NewRedisStore creates a RedisStore wired to a real go-redis/v9 client.
// addr is the Redis address, e.g. "127.0.0.1:6379".
func NewRedisStore(addr string) (*RedisStore, error) {
	if addr == "" {
		return nil, fmt.Errorf("redis store: addr required")
	}
	impl, err := newGoRedisSeam(addr)
	if err != nil {
		return nil, fmt.Errorf("redis store: connect: %w", err)
	}
	return &RedisStore{seam: impl}, nil
}

// newRedisStoreFromSeam is used by unit tests to inject a fake seam.
func newRedisStoreFromSeam(s redisSeam) *RedisStore {
	return &RedisStore{seam: s}
}

// SetSessionRouting stores fields as a Redis hash at clusteros:routing:{sessionID}
// and sets its TTL.  fields must be non-nil; ttl must be > 0.
func (r *RedisStore) SetSessionRouting(ctx context.Context, sessionID string, fields map[string]string, ttl time.Duration) error {
	if sessionID == "" {
		return fmt.Errorf("SetSessionRouting: sessionID required")
	}
	if ttl <= 0 {
		return fmt.Errorf("SetSessionRouting: ttl must be positive")
	}
	key := RoutingKey(sessionID)
	if err := r.seam.HSet(ctx, key, fields); err != nil {
		return fmt.Errorf("SetSessionRouting HSET: %w", err)
	}
	if err := r.seam.Expire(ctx, key, ttl); err != nil {
		// The HSet already committed; without a TTL it would be an immortal orphan
		// pinning a stale session->node route. Compensate by deleting the key so
		// the operation is all-or-nothing, matching the documented "stores fields
		// AND sets its TTL" contract.
		_ = r.seam.Del(ctx, key)
		return fmt.Errorf("SetSessionRouting EXPIRE: %w", err)
	}
	return nil
}

// GetSessionRouting returns all fields from the routing hash for sessionID.
// A missing key returns an empty map and no error.
func (r *RedisStore) GetSessionRouting(ctx context.Context, sessionID string) (map[string]string, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("GetSessionRouting: sessionID required")
	}
	key := RoutingKey(sessionID)
	fields, err := r.seam.HGetAll(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("GetSessionRouting HGetAll: %w", err)
	}
	return fields, nil
}

// PublishNodeEvent serialises evt as JSON and publishes it to
// clusteros:events:nodes.
func (r *RedisStore) PublishNodeEvent(ctx context.Context, evt NodeEvent) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("PublishNodeEvent marshal: %w", err)
	}
	if err := r.seam.Publish(ctx, chanNodeEvents, string(payload)); err != nil {
		return fmt.Errorf("PublishNodeEvent publish: %w", err)
	}
	return nil
}

// SubscribeNodeEvents returns a channel that delivers deserialised NodeEvents
// published on clusteros:events:nodes.  The channel is closed when ctx is
// cancelled or the underlying subscription is torn down.
func (r *RedisStore) SubscribeNodeEvents(ctx context.Context) (<-chan NodeEvent, error) {
	raw, err := r.seam.Subscribe(ctx, chanNodeEvents)
	if err != nil {
		return nil, fmt.Errorf("SubscribeNodeEvents subscribe: %w", err)
	}
	out := make(chan NodeEvent, 32)
	go func() {
		defer close(out)
		for msg := range raw {
			var evt NodeEvent
			if err := json.Unmarshal([]byte(msg), &evt); err != nil {
				// Skip malformed messages; do not crash the subscriber.
				continue
			}
			select {
			case out <- evt:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
