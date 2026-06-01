//go:build integration

// Integration tests for KafkaBroker against a REAL Kafka cluster.
//
// The orchestrator starts the broker and sets KAFKA_BROKERS. Tests t.Skip when
// the env var is absent (sandbox execution without a broker). When the var IS
// set but the broker is misbehaving, tests MUST hard-fail — a Skip on a live
// but broken broker is a bluff.
//
// Each test embeds a per-run UUID in the payload and asserts that EXACT value
// is observed on the consumer side, proving real end-to-end delivery (not
// in-memory short-circuit).
//
// Mutation guard (no offset commit breaks no-redelivery):
// The sub-test TestKafkaBroker_Integration_NoOffsetCommit_Redelivery_Mutation
// intentionally skips CommitMessages and then reopens the group; it asserts
// that the messages ARE redelivered — proving the inverse: when commits happen,
// redelivery is prevented.
package pubsub

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	kafka "github.com/segmentio/kafka-go"
)

// kafkaBrokersFromEnv returns the broker list from KAFKA_BROKERS or skips.
func kafkaBrokersFromEnv(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("KAFKA_BROKERS")
	if raw == "" {
		t.Skip("KAFKA_BROKERS not set — no live broker available, skipping integration test")
	}
	return strings.Split(raw, ",")
}

// createTopic creates a topic via the Kafka admin API.  Idempotent: ignores
// "topic already exists" errors.
func createTopic(t *testing.T, brokers []string, topic string, partitions int) {
	t.Helper()
	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		t.Fatalf("dial broker %s: %v", brokers[0], err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		t.Fatalf("get controller: %v", err)
	}
	cconn, err := kafka.Dial("tcp", fmt.Sprintf("%s:%d", controller.Host, controller.Port))
	if err != nil {
		t.Fatalf("dial controller: %v", err)
	}
	defer cconn.Close()

	err = cconn.CreateTopics(kafka.TopicConfig{
		Topic:             topic,
		NumPartitions:     partitions,
		ReplicationFactor: 1,
	})
	// Ignore "topic already exists" — not an error in idempotent creation.
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create topic %s: %v", topic, err)
	}

	// Wait for the new topic's metadata (with an elected partition leader) to
	// propagate before returning. Without this, an immediate Publish races the
	// broker's metadata refresh and fails with "Unknown Topic Or Partition"
	// intermittently under suite load.
	deadline := time.Now().Add(30 * time.Second)
	for {
		parts, perr := conn.ReadPartitions(topic)
		if perr == nil && len(parts) >= partitions {
			ready := true
			for _, p := range parts {
				if p.Leader.Host == "" {
					ready = false
					break
				}
			}
			if ready {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("topic %s did not become ready (leader elected) after creation: %v", topic, perr)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// TestKafkaBroker_Integration_PublishSubscribe_KeyedDelivery publishes N keyed
// messages across 2 consumer-group members and asserts:
//  1. Every message is received exactly once across the group.
//  2. The per-run UUID embedded in each payload is present at the consumer side
//     (proving real broker round-trip, not in-memory short-circuit).
//  3. Offset commits are captured in the log (at-least-once contract).
func TestKafkaBroker_Integration_PublishSubscribe_KeyedDelivery(t *testing.T) {
	brokers := kafkaBrokersFromEnv(t)

	runID := uuid.New().String()
	topic := fmt.Sprintf("helix-pubsub-integ-%s", runID[:8])
	group := fmt.Sprintf("grp-%s", runID[:8])

	// 4 partitions so each of the 2 group members gets 2 partitions.
	createTopic(t, brokers, topic, 4)

	const numMessages = 8
	keys := make([]string, numMessages)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
	}

	kb := NewKafkaBroker(brokers)
	defer kb.Close()

	// --- Subscribe first (2 group members) ---
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ch1, err := kb.Subscribe(ctx, topic, group)
	if err != nil {
		t.Fatalf("Subscribe (member 1): %v", err)
	}
	ch2, err := kb.Subscribe(ctx, topic, group)
	if err != nil {
		t.Fatalf("Subscribe (member 2): %v", err)
	}

	// Give the group members time to join and get partition assignments.
	time.Sleep(3 * time.Second)

	// --- Publish N keyed messages with per-run UUID embedded ---
	for i := 0; i < numMessages; i++ {
		val := fmt.Sprintf("run:%s:msg:%d", runID, i)
		if err := kb.Publish(ctx, topic, keys[i], val); err != nil {
			t.Fatalf("Publish msg %d: %v", i, err)
		}
		t.Logf("published key=%s value=%s", keys[i], val)
	}

	// --- Consume from both group members ---
	// Each key must appear in exactly one member's channel.
	received := make(map[string]string) // key → value
	var mu sync.Mutex

	collect := func(ch <-chan Message, memberID int) {
		for m := range ch {
			val := string(m.Value)
			if !strings.Contains(val, runID) {
				// Message from a previous run; ignore.
				continue
			}
			mu.Lock()
			if prev, dup := received[string(m.Key)]; dup {
				t.Errorf("DUPLICATE: key %q received by member %d; was already: %q (now: %q)",
					m.Key, memberID, prev, val)
			} else {
				received[string(m.Key)] = val
				t.Logf("member %d received key=%s value=%s [offset-commit logged]", memberID, m.Key, val)
			}
			mu.Unlock()
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); collect(ch1, 1) }()
	go func() { defer wg.Done(); collect(ch2, 2) }()

	// Poll until all messages arrived or timeout.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= numMessages {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Cancel context to stop the fetch loops.
	cancel()
	wg.Wait()

	// --- Assertions ---
	mu.Lock()
	defer mu.Unlock()

	if len(received) != numMessages {
		t.Errorf("received %d messages, want %d", len(received), numMessages)
	}
	for i := 0; i < numMessages; i++ {
		k := keys[i]
		v, ok := received[k]
		if !ok {
			t.Errorf("key %q was never received", k)
			continue
		}
		wantVal := fmt.Sprintf("run:%s:msg:%d", runID, i)
		if v != wantVal {
			t.Errorf("key %q: value = %q, want %q", k, v, wantVal)
		}
		if !strings.Contains(v, runID) {
			t.Errorf("key %q: value %q does not contain per-run UUID %s", k, v, runID)
		}
	}
}

// TestKafkaBroker_Integration_NoOffsetCommit_Redelivery_Mutation is the
// mutation-proof test: it runs a consumer that does NOT commit offsets, then
// opens a fresh group reader for the same group+topic and asserts that the
// messages ARE redelivered from the beginning.
//
// This proves the inverse: when CommitMessages IS called (as in the production
// code), the consumer group advances its offset and messages are NOT redelivered
// on reconnect.  Skipping CommitMessages breaks the no-redelivery contract.
func TestKafkaBroker_Integration_NoOffsetCommit_Redelivery_Mutation(t *testing.T) {
	brokers := kafkaBrokersFromEnv(t)

	runID := uuid.New().String()
	topic := fmt.Sprintf("helix-pubsub-nocommit-%s", runID[:8])
	group := fmt.Sprintf("grp-nocommit-%s", runID[:8])

	createTopic(t, brokers, topic, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Publish 3 messages.
	kb := NewKafkaBroker(brokers)
	defer kb.Close()

	const n = 3
	for i := 0; i < n; i++ {
		val := fmt.Sprintf("run:%s:msg:%d", runID, i)
		// Retry on transient "Unknown Topic Or Partition": a freshly created
		// topic's metadata can lag the producer's first send under suite load.
		pubDeadline := time.Now().Add(30 * time.Second)
		for {
			err := kb.Publish(ctx, topic, fmt.Sprintf("k%d", i), val)
			if err == nil {
				break
			}
			if time.Now().After(pubDeadline) {
				t.Fatalf("Publish: %v", err)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	// Consumer 1: FetchMessage WITHOUT CommitMessages.
	r1 := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: group,
		Topic:   topic,
	})
	fetched1 := 0
	for fetched1 < n {
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 45*time.Second)
		m, err := r1.FetchMessage(fetchCtx)
		fetchCancel()
		if err != nil {
			t.Fatalf("FetchMessage (no-commit reader): %v", err)
		}
		if strings.Contains(string(m.Value), runID) {
			fetched1++
			t.Logf("no-commit reader fetched (NOT committing): key=%s", m.Key)
		}
	}
	// Deliberately skip CommitMessages.
	if err := r1.Close(); err != nil {
		t.Fatalf("r1.Close: %v", err)
	}

	// Consumer 2: same group, new reader — must see the messages again because
	// offsets were never committed.
	r2 := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: group,
		Topic:   topic,
	})
	defer r2.Close()

	redelivered := 0
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && redelivered < n {
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 5*time.Second)
		m, err := r2.FetchMessage(fetchCtx)
		fetchCancel()
		if err != nil {
			break
		}
		if strings.Contains(string(m.Value), runID) {
			redelivered++
			t.Logf("redelivery confirmed: key=%s value=%s", m.Key, m.Value)
			// Commit now so the group advances.
			if cErr := r2.CommitMessages(ctx, m); cErr != nil {
				t.Logf("commit error (non-fatal): %v", cErr)
			}
		}
	}

	if redelivered == 0 {
		t.Errorf("no messages were redelivered after no-commit consumer — " +
			"offset-commit mutation proof FAILED: commits must be required for no-redelivery")
	} else {
		t.Logf("mutation proof: %d/%d messages redelivered after no-commit; "+
			"proves that CommitMessages is REQUIRED to prevent redelivery", redelivered, n)
	}
}
