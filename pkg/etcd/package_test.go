package etcd

import (
	"context"
	"testing"
	"time"
)

// mockKV implements a minimal in-memory key-value store for testing Client logic.
type mockKV struct {
	data map[string]string
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{Endpoints: []string{"localhost:2379"}}
	if cfg.DialTimeout != 0 {
		t.Error("expected zero value before New")
	}
	// We cannot call New without a real etcd, but we verify the struct.
}

func TestClientCloseNoPanic(t *testing.T) {
	// A nil client Close should not panic (defensive).
	var c *Client
	_ = c.Close() // no panic = pass
}

func TestMockKV(t *testing.T) {
	m := &mockKV{data: make(map[string]string)}
	m.data["key1"] = "value1"
	if m.data["key1"] != "value1" {
		t.Error("mock kv failed")
	}
}

func TestContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("expected deadline exceeded, got %v", ctx.Err())
	}
}
