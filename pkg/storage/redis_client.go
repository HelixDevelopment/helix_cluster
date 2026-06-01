package storage

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// goRedisSeam wraps a *goredis.Client and satisfies the redisSeam interface.
// All methods forward directly to the go-redis/v9 API with no additional logic
// so the seam stays thin and unit tests cover the store logic independently.
type goRedisSeam struct {
	client *goredis.Client
}

// newGoRedisSeam dials addr and pings to verify connectivity.
func newGoRedisSeam(addr string) (*goRedisSeam, error) {
	c := goredis.NewClient(&goredis.Options{Addr: addr})
	if err := c.Ping(context.Background()).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("redis ping %s: %w", addr, err)
	}
	return &goRedisSeam{client: c}, nil
}

// HSet stores fields as a Redis hash at key using go-redis v9 HSet.
// go-redis HSet accepts a map[string]interface{} or alternating key/value pairs;
// we convert map[string]string → []interface{} for the variadic form.
func (g *goRedisSeam) HSet(ctx context.Context, key string, fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return g.client.HSet(ctx, key, args...).Err()
}

// Expire sets the TTL on key.
func (g *goRedisSeam) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return g.client.Expire(ctx, key, ttl).Err()
}

// HGetAll returns all field-value pairs from the hash at key.
func (g *goRedisSeam) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	result, err := g.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Publish sends payload to channel.
func (g *goRedisSeam) Publish(ctx context.Context, channel, payload string) error {
	return g.client.Publish(ctx, channel, payload).Err()
}

// Subscribe opens a subscription to channel.  The returned string channel
// delivers raw message payloads and is closed when ctx is cancelled.
func (g *goRedisSeam) Subscribe(ctx context.Context, channel string) (<-chan string, error) {
	ps := g.client.Subscribe(ctx, channel)
	// Wait for the subscription confirmation to avoid a race where a publish
	// beats the subscription setup.
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, fmt.Errorf("subscribe receive: %w", err)
	}
	out := make(chan string, 64)
	go func() {
		defer close(out)
		defer ps.Close()
		ch := ps.Channel()
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- msg.Payload:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Compile-time check: goRedisSeam must satisfy redisSeam.
var _ redisSeam = (*goRedisSeam)(nil)
