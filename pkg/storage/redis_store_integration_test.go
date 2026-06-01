//go:build integration

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// redisAddrForIntegration returns the Redis address from REDIS_ADDR or skips.
// The orchestrator always sets REDIS_ADDR=127.0.0.1:6379 before running the
// integration tier; t.Skip is only reached in a genuinely dependency-absent
// environment (developer machine with no local Redis running).
func redisAddrForIntegration(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set — skipping integration test (orchestrator sets it)")
	}
	return addr
}

// TestIntegrationSessionRoutingRoundTrip_WithTTLExpiry writes a session routing
// hash with a per-run UUID field, reads it back from a SECOND client asserting
// the UUID, waits past the TTL, and then confirms the key expired (EXISTS==0).
//
// Hard-fail conditions:
//   - UUID read from second client does not match the written UUID
//   - key still exists after TTL expiry
func TestIntegrationSessionRoutingRoundTrip_WithTTLExpiry(t *testing.T) {
	addr := redisAddrForIntegration(t)
	runID := uuid.New().String()
	sessionID := fmt.Sprintf("integ-sess-%s", runID)
	const shortTTL = 2 * time.Second

	// ── Write via primary store ──────────────────────────────────────────────
	store, err := NewRedisStore(addr)
	if err != nil {
		t.Fatalf("NewRedisStore(%q): %v", addr, err)
	}

	fields := map[string]string{
		"run_id":      runID,
		"target_node": "node-integ-1",
		"protocol":    "grpc",
	}
	ctx := context.Background()
	if err := store.SetSessionRouting(ctx, sessionID, fields, shortTTL); err != nil {
		t.Fatalf("SetSessionRouting: %v", err)
	}

	// ── Read back from a SECOND independent client ────────────────────────────
	client2 := goredis.NewClient(&goredis.Options{Addr: addr})
	defer client2.Close()

	key := RoutingKey(sessionID)
	got, err := client2.HGetAll(ctx, key).Result()
	if err != nil {
		t.Fatalf("second client HGetAll(%q): %v", key, err)
	}
	if got["run_id"] != runID {
		t.Fatalf("second client: run_id = %q, want %q (full hash: %v)", got["run_id"], runID, got)
	}
	if got["target_node"] != "node-integ-1" {
		t.Errorf("second client: target_node = %q, want node-integ-1", got["target_node"])
	}

	// ── Wait past the TTL and confirm the key has expired ────────────────────
	// Sleep for shortTTL + 500ms buffer to let Redis evict the key.
	time.Sleep(shortTTL + 500*time.Millisecond)

	exists, err := client2.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("EXISTS %q: %v", key, err)
	}
	if exists != 0 {
		t.Fatalf("key %q still exists after TTL expiry (EXISTS=%d); TTL was %v", key, exists, shortTTL)
	}
}

// TestIntegrationNodeEventPubSub subscribes to clusteros:events:nodes, publishes
// a NodeEvent with a per-run UUID NodeID, and asserts the subscriber receives
// exactly that event with the correct UUID.
//
// Hard-fail conditions:
//   - subscriber does not receive any event within 5 seconds
//   - received NodeID does not match the per-run UUID
func TestIntegrationNodeEventPubSub(t *testing.T) {
	addr := redisAddrForIntegration(t)
	runID := uuid.New().String()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// ── Subscriber store ─────────────────────────────────────────────────────
	subStore, err := NewRedisStore(addr)
	if err != nil {
		t.Fatalf("NewRedisStore(sub): %v", err)
	}

	eventCh, err := subStore.SubscribeNodeEvents(ctx)
	if err != nil {
		t.Fatalf("SubscribeNodeEvents: %v", err)
	}

	// ── Give the subscription a moment to complete handshake ─────────────────
	// go-redis Subscribe with Receive already awaits the confirmation, but an
	// extra brief pause prevents a publish arriving before the TCP subscription
	// is acknowledged on the server side.
	time.Sleep(100 * time.Millisecond)

	// ── Publisher store (separate client for realism) ────────────────────────
	pubStore, err := NewRedisStore(addr)
	if err != nil {
		t.Fatalf("NewRedisStore(pub): %v", err)
	}

	evt := NodeEvent{
		Type:   "join",
		NodeID: runID,
		Payload: map[string]string{
			"integration": "wave9",
		},
	}
	if err := pubStore.PublishNodeEvent(ctx, evt); err != nil {
		t.Fatalf("PublishNodeEvent: %v", err)
	}

	// ── Assert the subscriber received the UUID-tagged event ─────────────────
	select {
	case received, ok := <-eventCh:
		if !ok {
			t.Fatal("SubscribeNodeEvents channel closed before receiving event")
		}
		if received.NodeID != runID {
			t.Fatalf("received NodeID = %q, want %q (run_id)", received.NodeID, runID)
		}
		if received.Type != "join" {
			t.Errorf("received Type = %q, want join", received.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for NodeEvent — pubsub delivery failed")
	}
}
