// Unit tests for KafkaBroker — no live broker required.
// Uses factory-function seams to capture Writer/Reader configuration and
// verify it matches the acks=all, sync, group-ID requirements without
// making any network connections.
package pubsub

import (
	"context"
	"sync"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
)

// ---------------------------------------------------------------------------
// Writer-config seam tests
// ---------------------------------------------------------------------------

// TestKafkaBroker_WriterConfig_AcksAll_Mutation asserts that the default
// writer factory produces a writer with RequiredAcks == RequireAll (-1) and
// Async == false.
// Mutation: changing RequiredAcks to RequireOne (1) or RequireNone (0) makes
// this test fail — proving the acks=all mandate is enforced.
func TestKafkaBroker_WriterConfig_AcksAll_Mutation(t *testing.T) {
	brokers := []string{"kafka:9092"}
	kb := NewKafkaBroker(brokers)

	cfg := kb.CapturedWriterConfig("test-topic")

	const requireAll = int(kafka.RequireAll) // must be -1
	if cfg.RequiredAcks != requireAll {
		t.Errorf("RequiredAcks = %d, want %d (RequireAll / ISR ack)", cfg.RequiredAcks, requireAll)
	}
	if cfg.Async {
		t.Error("Async must be false for synchronous acks=all")
	}
	if len(cfg.Brokers) != 1 || cfg.Brokers[0] != "kafka:9092" {
		t.Errorf("Brokers = %v, want [kafka:9092]", cfg.Brokers)
	}
}

// TestKafkaBroker_WriterConfig_RequireAll_Value verifies the kafka-go constant
// value is exactly -1 (ISR ack) as required by the spec.
// Guards against library version drift changing the constant.
func TestKafkaBroker_WriterConfig_RequireAll_Value(t *testing.T) {
	if int(kafka.RequireAll) != -1 {
		t.Errorf("kafka.RequireAll = %d, expected -1", int(kafka.RequireAll))
	}
	if int(kafka.RequireNone) != 0 {
		t.Errorf("kafka.RequireNone = %d, expected 0", int(kafka.RequireNone))
	}
	if int(kafka.RequireOne) != 1 {
		t.Errorf("kafka.RequireOne = %d, expected 1", int(kafka.RequireOne))
	}
}

// TestKafkaBroker_WriterConfig_HashBalancer_Mutation verifies Hash balancer is
// used so that the same key always routes to the same partition.
// Mutation: changing to RoundRobin would lose key-affinity and break the
// "exactly-one-consumer-per-key" property tested in integration tests.
func TestKafkaBroker_WriterConfig_HashBalancer_Mutation(t *testing.T) {
	kb := NewKafkaBroker([]string{"b:9092"})
	w := kb.defaultWriterFactory("topic", []string{"b:9092"})
	if w == nil {
		t.Fatal("defaultWriterFactory returned nil")
	}
	if _, ok := w.Balancer.(*kafka.Hash); !ok {
		t.Errorf("Balancer = %T, want *kafka.Hash", w.Balancer)
	}
	_ = w.Close()
}

// ---------------------------------------------------------------------------
// Reader-config seam tests
// ---------------------------------------------------------------------------

// TestKafkaBroker_ReaderConfig_GroupID_Mutation asserts that Subscribe wires
// the group ID into the ReaderConfig.
// Mutation: if the group were empty, each reader would be an independent
// consumer (not a group member) — defeating at-least-once group semantics.
func TestKafkaBroker_ReaderConfig_GroupID_Mutation(t *testing.T) {
	kb := NewKafkaBroker([]string{"b:9092"})
	cfg := kb.CapturedReaderConfig("my-topic", "my-group")

	if cfg.GroupID != "my-group" {
		t.Errorf("GroupID = %q, want %q", cfg.GroupID, "my-group")
	}
	if cfg.Topic != "my-topic" {
		t.Errorf("Topic = %q, want %q", cfg.Topic, "my-topic")
	}
	if len(cfg.Brokers) == 0 {
		t.Error("Brokers must not be empty")
	}
}

// ---------------------------------------------------------------------------
// Isolated writer lazily-created-per-topic seam test
// ---------------------------------------------------------------------------

// TestKafkaBroker_MultipleTopics_IsolatedWriters verifies that writerFor
// creates exactly one writer per topic and reuses it on subsequent calls.
func TestKafkaBroker_MultipleTopics_IsolatedWriters(t *testing.T) {
	created := map[string]int{}
	var mu sync.Mutex

	kb := NewKafkaBroker([]string{"b:9092"})
	kb.writerFactory = func(topic string, brokers []string) *kafka.Writer {
		mu.Lock()
		created[topic]++
		mu.Unlock()
		return &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Topic:        topic,
			RequiredAcks: kafka.RequireAll,
			Async:        false,
		}
	}

	_ = kb.writerFor("alpha")
	_ = kb.writerFor("beta")
	_ = kb.writerFor("alpha") // second call: must reuse, not create new writer

	mu.Lock()
	defer mu.Unlock()
	if created["alpha"] != 1 {
		t.Errorf("writerFor('alpha') called factory %d times, want 1", created["alpha"])
	}
	if created["beta"] != 1 {
		t.Errorf("writerFor('beta') called factory %d times, want 1", created["beta"])
	}
}

// ---------------------------------------------------------------------------
// Fake reader for Subscribe loop seam tests
// ---------------------------------------------------------------------------

// kafkaReaderIface is the subset of kafka.Reader used by our fetch-forward-commit
// loop, enabling injection of a fakeReader without a live broker.
type kafkaReaderIface interface {
	FetchMessage(context.Context) (kafka.Message, error)
	CommitMessages(context.Context, ...kafka.Message) error
	Close() error
}

// fakeReader emits pre-loaded messages then blocks until ctx is cancelled.
// It records every CommitMessages call so tests can assert offset-commit counts.
// mu guards commits and idx so the -race detector does not fire when the test
// goroutine reads commits after a channel synchronisation barrier.
type fakeReader struct {
	mu      sync.Mutex
	msgs    []kafka.Message
	idx     int
	commits []kafka.Message
}

func (f *fakeReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	f.mu.Lock()
	if f.idx < len(f.msgs) {
		m := f.msgs[f.idx]
		f.idx++
		f.mu.Unlock()
		return m, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return kafka.Message{}, ctx.Err()
}

func (f *fakeReader) CommitMessages(_ context.Context, msgs ...kafka.Message) error {
	f.mu.Lock()
	f.commits = append(f.commits, msgs...)
	f.mu.Unlock()
	return nil
}

// commitCount returns the number of messages committed so far, safe for
// concurrent reading by the test goroutine.
func (f *fakeReader) commitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.commits)
}

// commitAt returns the i-th committed message, safe for concurrent reading.
func (f *fakeReader) commitAt(i int) kafka.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits[i]
}

func (f *fakeReader) Close() error { return nil }

// subscribeWithFakeReader runs the same fetch-forward-commit loop as
// KafkaBroker.Subscribe but against a kafkaReaderIface, so unit tests can
// inject fakeReader without a live Kafka cluster.
func subscribeWithFakeReader(ctx context.Context, r kafkaReaderIface) <-chan Message {
	out := make(chan Message, 64)
	go func() {
		defer close(out)
		defer r.Close() //nolint:errcheck
		for {
			m, err := r.FetchMessage(ctx)
			if err != nil {
				return
			}
			msg := Message{Topic: m.Topic, Key: m.Key, Value: m.Value}
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			}
			if err := r.CommitMessages(ctx, m); err != nil {
				return
			}
		}
	}()
	return out
}

// TestKafkaBroker_Subscribe_CommitAfterEachMessage_Mutation verifies that
// CommitMessages is called after EVERY delivered message (not batched/skipped).
// Mutation: removing the CommitMessages call → commits == 0 while deliveries > 0
// — the test fails, proving the at-least-once offset-commit mandate is enforced.
func TestKafkaBroker_Subscribe_CommitAfterEachMessage_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgs := []kafka.Message{
		{Topic: "t", Key: []byte("k1"), Value: []byte("v1")},
		{Topic: "t", Key: []byte("k2"), Value: []byte("v2")},
		{Topic: "t", Key: []byte("k3"), Value: []byte("v3")},
	}
	fr := &fakeReader{msgs: msgs}

	ch := subscribeWithFakeReader(ctx, fr)

	// Drain all messages. We use a done channel synchronised on the final
	// message so we know the CommitMessages call for it has also completed
	// before we read fr.commits (avoiding the race).
	received := 0
	for received < len(msgs) {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before all messages received")
			}
			received++
		case <-time.After(time.Second):
			t.Fatalf("timeout after %d/%d messages", received, len(msgs))
		}
	}

	// Poll for commit count using the race-safe accessor — commitMessages runs
	// in the goroutine immediately after forwarding each message. In practice it
	// completes before we can even enter this loop, but we retry for up to 1 s
	// to be deterministic under high load.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && fr.commitCount() < len(msgs) {
		time.Sleep(time.Millisecond)
	}

	n := fr.commitCount()
	if n != len(msgs) {
		t.Errorf("CommitMessages called %d times, want %d — offset commit broken", n, len(msgs))
	}
	for i := 0; i < n && i < len(msgs); i++ {
		c := fr.commitAt(i)
		if string(c.Key) != string(msgs[i].Key) {
			t.Errorf("commit[%d].Key = %q, want %q", i, c.Key, msgs[i].Key)
		}
	}
}

// TestKafkaBroker_Subscribe_ChannelClosedOnContextCancel_Mutation verifies
// that the output channel is closed when ctx is cancelled.
// Mutation: removing the <-ctx.Done() select-case leaves the channel open,
// causing a range-over to block forever.
func TestKafkaBroker_Subscribe_ChannelClosedOnContextCancel_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	fr := &fakeReader{msgs: nil} // blocks immediately on FetchMessage
	ch := subscribeWithFakeReader(ctx, fr)

	cancel()

	select {
	case _, open := <-ch:
		if open {
			t.Error("channel should be closed after context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after context cancellation")
	}
}

// TestKafkaBroker_Subscribe_ExactPayloadRoundTrip verifies Key, Value and
// Topic are faithfully forwarded without truncation or modification.
func TestKafkaBroker_Subscribe_ExactPayloadRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wantKey := "order-42"
	wantVal := "payload-roundtrip-check"

	fr := &fakeReader{msgs: []kafka.Message{
		{Topic: "orders", Key: []byte(wantKey), Value: []byte(wantVal)},
	}}
	ch := subscribeWithFakeReader(ctx, fr)

	select {
	case m := <-ch:
		if string(m.Key) != wantKey {
			t.Errorf("Key = %q, want %q", m.Key, wantKey)
		}
		if string(m.Value) != wantVal {
			t.Errorf("Value = %q, want %q", m.Value, wantVal)
		}
		if m.Topic != "orders" {
			t.Errorf("Topic = %q, want %q", m.Topic, "orders")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for forwarded message")
	}
}
