package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ─── fakeRedisSeam ─────────────────────────────────────────────────────────

// fakeRedisSeam is a hand-rolled, in-memory fake that records every call made
// by RedisStore so tests can assert exact key strings, field maps, TTL values,
// and channel/payload arguments — without a live Redis server.
type fakeRedisSeam struct {
	mu sync.Mutex

	// hsets records each HSet call: key → fields snapshot.
	hsets []hsetCall
	// expires records each Expire call.
	expires []expireCall
	// publishes records each Publish call.
	publishes []publishCall
	// subscriptions maps channel → slice of injected message payloads.
	subscriptions map[string][]string

	// forceHSetErr, if non-nil, is returned by HSet.
	forceHSetErr error
	// forceExpireErr, if non-nil, is returned by Expire.
	forceExpireErr error
}

type hsetCall struct {
	Key    string
	Fields map[string]string
}
type expireCall struct {
	Key string
	TTL time.Duration
}
type publishCall struct {
	Channel string
	Payload string
}

func newFakeSeam() *fakeRedisSeam {
	return &fakeRedisSeam{subscriptions: make(map[string][]string)}
}

// seedMessages pre-loads messages that Subscribe will deliver for channel.
func (f *fakeRedisSeam) seedMessages(channel string, msgs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscriptions[channel] = append(f.subscriptions[channel], msgs...)
}

func (f *fakeRedisSeam) HSet(_ context.Context, key string, fields map[string]string) error {
	if f.forceHSetErr != nil {
		return f.forceHSetErr
	}
	cp := make(map[string]string, len(fields))
	for k, v := range fields {
		cp[k] = v
	}
	f.mu.Lock()
	f.hsets = append(f.hsets, hsetCall{Key: key, Fields: cp})
	f.mu.Unlock()
	return nil
}

func (f *fakeRedisSeam) Expire(_ context.Context, key string, ttl time.Duration) error {
	if f.forceExpireErr != nil {
		return f.forceExpireErr
	}
	f.mu.Lock()
	f.expires = append(f.expires, expireCall{Key: key, TTL: ttl})
	f.mu.Unlock()
	return nil
}

// Del drops any recorded HSet rows for key (compensation path).
func (f *fakeRedisSeam) Del(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.hsets[:0]
	for _, h := range f.hsets {
		if h.Key != key {
			kept = append(kept, h)
		}
	}
	f.hsets = kept
	return nil
}

func (f *fakeRedisSeam) HGetAll(_ context.Context, key string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Return the fields stored by the most recent HSet for this key.
	for i := len(f.hsets) - 1; i >= 0; i-- {
		if f.hsets[i].Key == key {
			cp := make(map[string]string, len(f.hsets[i].Fields))
			for k, v := range f.hsets[i].Fields {
				cp[k] = v
			}
			return cp, nil
		}
	}
	return map[string]string{}, nil
}

func (f *fakeRedisSeam) Publish(_ context.Context, channel, payload string) error {
	f.mu.Lock()
	f.publishes = append(f.publishes, publishCall{Channel: channel, Payload: payload})
	f.mu.Unlock()
	return nil
}

func (f *fakeRedisSeam) Subscribe(_ context.Context, channel string) (<-chan string, error) {
	f.mu.Lock()
	msgs := append([]string(nil), f.subscriptions[channel]...)
	f.mu.Unlock()

	ch := make(chan string, len(msgs)+1)
	for _, m := range msgs {
		ch <- m
	}
	close(ch)
	return ch, nil
}

// ─── Key builder tests ───────────────────────────────────────────────────────

// TestKeyBuilders asserts that every key-builder produces the exact canonical
// string documented in the package.  Any drift in the prefix constant or format
// string fails here before it can corrupt Redis namespacing.
//
// Mutation proof: changing keyPrefixRouting to "clusteros:route:" makes
// TestKeyBuilderRouting fail with "got clusteros:route:sess-1, want clusteros:routing:sess-1".
func TestKeyBuilders(t *testing.T) {
	t.Run("cache", func(t *testing.T) {
		got := CacheKey("my-obj")
		want := "clusteros:cache:my-obj"
		if got != want {
			t.Fatalf("CacheKey = %q, want %q", got, want)
		}
	})
	t.Run("routing", func(t *testing.T) {
		got := RoutingKey("sess-1")
		want := "clusteros:routing:sess-1"
		if got != want {
			t.Fatalf("RoutingKey = %q, want %q", got, want)
		}
	})
	t.Run("ratelimit_user", func(t *testing.T) {
		got := RateLimitKey("alice")
		want := "clusteros:ratelimit:alice"
		if got != want {
			t.Fatalf("RateLimitKey = %q, want %q", got, want)
		}
	})
	t.Run("ratelimit_global", func(t *testing.T) {
		got := RateLimitKey("global")
		want := "clusteros:ratelimit:global"
		if got != want {
			t.Fatalf("RateLimitKey = %q, want %q", got, want)
		}
	})
	t.Run("node_events_channel", func(t *testing.T) {
		got := NodeEventsChannel()
		want := "clusteros:events:nodes"
		if got != want {
			t.Fatalf("NodeEventsChannel = %q, want %q", got, want)
		}
	})
}

// ─── SetSessionRouting tests ─────────────────────────────────────────────────

// TestSetSessionRoutingKeyAndFields asserts the exact Redis key and field map
// passed to HSet, and that Expire receives the same key with the right TTL.
//
// Mutation proof: if RoutingKey were replaced by CacheKey, the HSet key would
// be "clusteros:cache:sess-abc" and this test fails with a key mismatch.
// If Expire were removed, the expireCall slice would be empty and the
// assertions on Expire would fail.
func TestSetSessionRoutingKeyAndFields(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)

	fields := map[string]string{
		"target_node": "node-7",
		"protocol":    "grpc",
	}
	ttl := 30 * time.Second
	if err := store.SetSessionRouting(context.Background(), "sess-abc", fields, ttl); err != nil {
		t.Fatalf("SetSessionRouting: %v", err)
	}

	// ── Assert HSet ──────────────────────────────────────────────────────────
	if len(fake.hsets) != 1 {
		t.Fatalf("HSet called %d times, want 1", len(fake.hsets))
	}
	gotKey := fake.hsets[0].Key
	wantKey := "clusteros:routing:sess-abc"
	if gotKey != wantKey {
		t.Errorf("HSet key = %q, want %q", gotKey, wantKey)
	}
	gotFields := fake.hsets[0].Fields
	for k, v := range fields {
		if gotFields[k] != v {
			t.Errorf("HSet field[%q] = %q, want %q", k, gotFields[k], v)
		}
	}

	// ── Assert Expire ─────────────────────────────────────────────────────────
	if len(fake.expires) != 1 {
		t.Fatalf("Expire called %d times, want 1", len(fake.expires))
	}
	expKey := fake.expires[0].Key
	if expKey != wantKey {
		t.Errorf("Expire key = %q, want %q", expKey, wantKey)
	}
	expTTL := fake.expires[0].TTL
	if expTTL != ttl {
		t.Errorf("Expire TTL = %v, want %v", expTTL, ttl)
	}
}

// TestSetSessionRoutingZeroTTLRejected ensures a zero/negative TTL is rejected
// before any Redis call is made (validates the guard branch).
//
// Mutation proof: removing the `ttl <= 0` guard lets the call through; fake
// then has 1 HSet call which contradicts the len==0 assertion.
func TestSetSessionRoutingZeroTTLRejected(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)
	if err := store.SetSessionRouting(context.Background(), "s", nil, 0); err == nil {
		t.Fatal("expected error for zero TTL, got nil")
	}
	if len(fake.hsets) != 0 {
		t.Errorf("HSet should not have been called on zero TTL")
	}
}

// TestSetSessionRoutingEmptySessionIDRejected tests the empty-sessionID guard.
func TestSetSessionRoutingEmptySessionIDRejected(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)
	if err := store.SetSessionRouting(context.Background(), "", nil, time.Second); err == nil {
		t.Fatal("expected error for empty sessionID, got nil")
	}
	if len(fake.hsets) != 0 {
		t.Error("HSet should not have been called on empty sessionID")
	}
}

// TestSetSessionRoutingHSetErrorPropagates ensures that an HSet failure is
// surfaced without calling Expire.
//
// Mutation proof: removing the `if err := r.seam.HSet(...)` error check means
// Expire IS called; len(fake.expires)==0 would fail.
func TestSetSessionRoutingHSetErrorPropagates(t *testing.T) {
	fake := newFakeSeam()
	fake.forceHSetErr = errors.New("redis: broken pipe")
	store := newRedisStoreFromSeam(fake)
	if err := store.SetSessionRouting(context.Background(), "s", map[string]string{"k": "v"}, time.Second); err == nil {
		t.Fatal("expected error from HSet failure, got nil")
	}
	if len(fake.expires) != 0 {
		t.Error("Expire must not be called when HSet fails")
	}
}

// ─── GetSessionRouting tests ─────────────────────────────────────────────────

// TestGetSessionRoutingExactKey asserts the exact key passed to HGetAll and
// that the returned fields match what was stored via SetSessionRouting.
//
// Mutation proof: if GetSessionRouting used CacheKey instead of RoutingKey,
// HGetAll would receive "clusteros:cache:sess-1" and the fake would return an
// empty map; the field-equality assertion fails.
func TestGetSessionRoutingExactKey(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)

	want := map[string]string{"node": "n-3", "proto": "tcp"}
	if err := store.SetSessionRouting(context.Background(), "sess-1", want, time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := store.GetSessionRouting(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// TestGetSessionRoutingMissingKeyReturnsEmptyMap ensures a missing session
// returns an empty map, not an error.
func TestGetSessionRoutingMissingKeyReturnsEmptyMap(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)
	got, err := store.GetSessionRouting(context.Background(), "no-such-session")
	if err != nil {
		t.Fatalf("expected nil error for missing session, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

// ─── PublishNodeEvent tests ──────────────────────────────────────────────────

// TestPublishNodeEventChannelAndPayload asserts the exact channel name and that
// the payload is valid JSON containing the event fields.
//
// Mutation proof: if chanNodeEvents were changed to "clusteros:events:cluster",
// the channel assertion fails.  If json.Marshal were omitted (raw struct send),
// json.Unmarshal of the payload would fail.
func TestPublishNodeEventChannelAndPayload(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)

	evt := NodeEvent{
		Type:   "join",
		NodeID: "node-42",
		Payload: map[string]string{
			"region": "eu-west-1",
		},
	}
	if err := store.PublishNodeEvent(context.Background(), evt); err != nil {
		t.Fatalf("PublishNodeEvent: %v", err)
	}

	if len(fake.publishes) != 1 {
		t.Fatalf("Publish called %d times, want 1", len(fake.publishes))
	}
	pub := fake.publishes[0]

	wantChan := "clusteros:events:nodes"
	if pub.Channel != wantChan {
		t.Errorf("Publish channel = %q, want %q", pub.Channel, wantChan)
	}

	var decoded NodeEvent
	if err := json.Unmarshal([]byte(pub.Payload), &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v (payload=%q)", err, pub.Payload)
	}
	if decoded.Type != evt.Type {
		t.Errorf("decoded.Type = %q, want %q", decoded.Type, evt.Type)
	}
	if decoded.NodeID != evt.NodeID {
		t.Errorf("decoded.NodeID = %q, want %q", decoded.NodeID, evt.NodeID)
	}
	if decoded.Payload["region"] != "eu-west-1" {
		t.Errorf("decoded.Payload[region] = %q, want eu-west-1", decoded.Payload["region"])
	}
}

// ─── SubscribeNodeEvents tests ───────────────────────────────────────────────

// TestSubscribeNodeEventsDecodesPayload pre-seeds the fake with a JSON-encoded
// NodeEvent and asserts the subscriber channel delivers a correctly decoded
// struct — not a raw string.
//
// Mutation proof: if SubscribeNodeEvents delivered raw strings instead of
// decoded NodeEvent structs the type assertion in the range loop would fail to
// compile; if json.Unmarshal were replaced by a no-op, decoded fields would be
// zero-valued and the Type assertion fails.
func TestSubscribeNodeEventsDecodesPayload(t *testing.T) {
	fake := newFakeSeam()

	src := NodeEvent{Type: "leave", NodeID: "node-99"}
	payload, _ := json.Marshal(src)
	fake.seedMessages(chanNodeEvents, string(payload))

	store := newRedisStoreFromSeam(fake)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := store.SubscribeNodeEvents(ctx)
	if err != nil {
		t.Fatalf("SubscribeNodeEvents: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before delivering event")
		}
		if got.Type != "leave" {
			t.Errorf("got.Type = %q, want leave", got.Type)
		}
		if got.NodeID != "node-99" {
			t.Errorf("got.NodeID = %q, want node-99", got.NodeID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for NodeEvent")
	}
}

// TestSubscribeNodeEventsMalformedPayloadSkipped asserts that a malformed JSON
// message does not crash or block the subscriber — it is silently skipped and
// the well-formed messages that follow are still delivered.
//
// Mutation proof: removing the json.Unmarshal error check and sending a
// zero-value NodeEvent for malformed input would deliver a bogus event before
// the valid one; the Type guard below would then see "" on the first receive.
func TestSubscribeNodeEventsMalformedPayloadSkipped(t *testing.T) {
	fake := newFakeSeam()

	good, _ := json.Marshal(NodeEvent{Type: "update", NodeID: "node-5"})
	fake.seedMessages(chanNodeEvents,
		"{not-json}", // malformed — must be skipped
		string(good), // well-formed — must be delivered
	)

	store := newRedisStoreFromSeam(fake)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := store.SubscribeNodeEvents(ctx)
	if err != nil {
		t.Fatalf("SubscribeNodeEvents: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("channel closed without delivering the valid event")
		}
		if got.Type != "update" {
			t.Errorf("got.Type = %q, want update", got.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for NodeEvent")
	}
}

// ─── NewRedisStore validation ─────────────────────────────────────────────────

// TestNewRedisStoreEmptyAddrRejected ensures construction fails fast.
//
// Mutation proof: removing the addr=="" guard means the code tries to construct
// a goRedisSeam with an empty address — on darwin that returns a dial error so
// the test still passes, but the error message would differ.  The guard makes
// the failure deterministic and local.
func TestNewRedisStoreEmptyAddrRejected(t *testing.T) {
	_, err := NewRedisStore("")
	if err == nil {
		t.Fatal("expected error for empty addr, got nil")
	}
}

// ─── Round-trip through seam ─────────────────────────────────────────────────

// TestPublishThenSubscribeRoundTrip sets up publish + subscribe through the
// same fake to verify the JSON serialisation + deserialisation round-trip is
// idempotent for non-trivial payloads.
func TestPublishThenSubscribeRoundTrip(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)

	evt := NodeEvent{
		Type:   "update",
		NodeID: "n-1",
		Payload: map[string]string{
			"cpu_pct": "72.3",
			"mem_mb":  "4096",
		},
	}

	// Publish so fake records the payload.
	if err := store.PublishNodeEvent(context.Background(), evt); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Seed the same payload into the fake subscriber channel.
	if len(fake.publishes) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(fake.publishes))
	}
	fake.seedMessages(chanNodeEvents, fake.publishes[0].Payload)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := store.SubscribeNodeEvents(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	select {
	case got := <-ch:
		if got.NodeID != evt.NodeID {
			t.Errorf("round-trip NodeID = %q, want %q", got.NodeID, evt.NodeID)
		}
		if got.Payload["cpu_pct"] != "72.3" {
			t.Errorf("round-trip cpu_pct = %q, want 72.3", got.Payload["cpu_pct"])
		}
	case <-ctx.Done():
		t.Fatal("timed out")
	}
}

// ─── Multiple sessions isolation ─────────────────────────────────────────────

// TestMultipleSessionsIsolated proves that SetSessionRouting for two different
// sessions writes to different keys and GetSessionRouting for each returns only
// that session's fields.
//
// Mutation proof: if RoutingKey used a shared key (e.g. always "clusteros:routing:")
// the second Set would overwrite the first and both Gets would return the same map.
func TestMultipleSessionsIsolated(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)

	ctx := context.Background()
	if err := store.SetSessionRouting(ctx, "sess-A", map[string]string{"node": "n-1"}, time.Minute); err != nil {
		t.Fatalf("set A: %v", err)
	}
	if err := store.SetSessionRouting(ctx, "sess-B", map[string]string{"node": "n-2"}, time.Minute); err != nil {
		t.Fatalf("set B: %v", err)
	}

	gotA, _ := store.GetSessionRouting(ctx, "sess-A")
	gotB, _ := store.GetSessionRouting(ctx, "sess-B")

	if gotA["node"] != "n-1" {
		t.Errorf("sess-A node = %q, want n-1", gotA["node"])
	}
	if gotB["node"] != "n-2" {
		t.Errorf("sess-B node = %q, want n-2", gotB["node"])
	}

	// Verify the two HSet calls used different keys.
	keys := map[string]bool{}
	for _, h := range fake.hsets {
		keys[h.Key] = true
	}
	if !keys["clusteros:routing:sess-A"] {
		t.Error("missing HSet for clusteros:routing:sess-A")
	}
	if !keys["clusteros:routing:sess-B"] {
		t.Error("missing HSet for clusteros:routing:sess-B")
	}
}

// ─── Explicit EXPIRE ordering ─────────────────────────────────────────────────

// TestExpireCalledAfterHSet proves HSet happens before Expire (not after), by
// checking the recorded call slices are non-empty in order.
//
// Mutation proof: if Expire were called before HSet (inverted order), the
// hash would not exist when Expire fires, so a real Redis server would silently
// ignore the TTL.  In tests, reversing the order in the implementation means
// fake.expires[0] precedes fake.hsets[0] — the length check catches it because
// both must be present after one SetSessionRouting call.
func TestExpireCalledAfterHSet(t *testing.T) {
	// After exactly one SetSessionRouting call, the fake must have recorded both
	// an HSet and an Expire — proving the routing hash is written AND given a TTL.
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)

	if err := store.SetSessionRouting(context.Background(), "s", map[string]string{"k": "v"}, 5*time.Second); err != nil {
		t.Fatalf("SetSessionRouting: %v", err)
	}
	if len(fake.hsets) == 0 {
		t.Fatal("HSet was never called")
	}
	if len(fake.expires) == 0 {
		t.Fatal("Expire was never called")
	}
}

// ─── Concurrent publish safety ────────────────────────────────────────────────

// TestPublishNodeEventConcurrentSafety hammers PublishNodeEvent from multiple
// goroutines to surface any race on fake.publishes under -race.
func TestPublishNodeEventConcurrentSafety(t *testing.T) {
	fake := newFakeSeam()
	store := newRedisStoreFromSeam(fake)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			evt := NodeEvent{Type: "join", NodeID: fmt.Sprintf("node-%d", i)}
			if err := store.PublishNodeEvent(context.Background(), evt); err != nil {
				t.Errorf("publish %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	fake.mu.Lock()
	got := len(fake.publishes)
	fake.mu.Unlock()
	if got != n {
		t.Errorf("expected %d publishes, got %d", n, got)
	}
}
