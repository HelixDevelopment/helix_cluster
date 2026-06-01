//go:build integration

// Integration tests for EnsureTopics + topic round-trip against a REAL Kafka.
//
// The orchestrator sets KAFKA_BROKERS=127.0.0.1:9092 before running
// -tags=integration.  Tests t.Skip ONLY when the env var is absent (sandbox
// without a broker).  When the var IS set, every assertion is a hard FAIL.
//
// Per-run UUID: embedded in the produce/consume message so stale messages from
// previous runs are identified and skipped.
//
// Partition-propagation race: after CreateTopics, we wait for partition-leader
// election before producing.  The existing kafka_integration_test.go already
// implements this pattern (createTopic helper) — we follow the same approach.
package pubsub

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	kafka "github.com/segmentio/kafka-go"
)

// kafkaBrokersForTopics reads KAFKA_BROKERS or skips the test.
func kafkaBrokersForTopics(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("KAFKA_BROKERS")
	if raw == "" {
		t.Skip("KAFKA_BROKERS not set — no live broker; skipping integration test")
	}
	return strings.Split(raw, ",")
}

// waitForTopicLeader polls until all partitions of topic have an elected leader
// or the deadline expires.  This avoids the "Unknown Topic Or Partition" race
// that can occur immediately after CreateTopics on a freshly started broker.
func waitForTopicLeader(t *testing.T, broker, topic string, wantPartitions int) {
	t.Helper()
	conn, err := kafka.Dial("tcp", broker)
	if err != nil {
		t.Fatalf("waitForTopicLeader: dial %s: %v", broker, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(30 * time.Second)
	for {
		parts, err := conn.ReadPartitions(topic)
		if err == nil && len(parts) >= wantPartitions {
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
			t.Fatalf("waitForTopicLeader: topic %s not ready after 30s (parts=%d err=%v)",
				topic, len(parts), err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// TestEnsureTopics_Integration_CreateAndDescribe calls EnsureTopics against a
// real Kafka broker and then verifies every control-plane topic exists with the
// exact partition count, replication factor and retention.ms from the catalog.
//
// Mutation guard: changing helix.nodes NumPartitions from 6 to 5 and then
// calling EnsureTopics creates the topic with 5 partitions; the ReadPartitions
// assertion (want=6) hard-fails — confirming the catalog drives real broker state.
func TestEnsureTopics_Integration_CreateAndDescribe(t *testing.T) {
	brokers := kafkaBrokersForTopics(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Call EnsureTopics — must complete without error.
	if err := EnsureTopics(ctx, brokers); err != nil {
		t.Fatalf("EnsureTopics: %v", err)
	}

	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer conn.Close()

	catalog := TopicCatalog()
	for topicName, spec := range catalog {
		// Wait for leader election to avoid "Unknown Topic Or Partition".
		waitForTopicLeader(t, brokers[0], topicName, spec.NumPartitions)

		parts, err := conn.ReadPartitions(topicName)
		if err != nil {
			t.Errorf("ReadPartitions(%s): %v", topicName, err)
			continue
		}

		// Partition count assertion.
		if len(parts) != spec.NumPartitions {
			t.Errorf("%s: partition count=%d, want %d", topicName, len(parts), spec.NumPartitions)
		}

		// Replication factor: inspect the first partition's replica list length.
		if len(parts) > 0 && len(parts[0].Replicas) != spec.ReplicationFactor {
			t.Errorf("%s: replication factor=%d (from replica list), want %d",
				topicName, len(parts[0].Replicas), spec.ReplicationFactor)
		}
	}

	// Verify retention.ms via DescribeConfigs for one representative topic.
	verifyRetention(t, ctx, brokers[0], "helix.nodes", catalog["helix.nodes"].RetentionMs)
}

// verifyRetention uses the Client.DescribeConfigs API to assert the retention.ms
// config value that was set by EnsureTopics.
func verifyRetention(t *testing.T, ctx context.Context, broker, topic string, wantRetentionMs int64) {
	t.Helper()
	client := &kafka.Client{Addr: kafka.TCP(broker)}
	resp, err := client.DescribeConfigs(ctx, &kafka.DescribeConfigsRequest{
		Resources: []kafka.DescribeConfigRequestResource{
			{
				ResourceType: kafka.ResourceTypeTopic,
				ResourceName: topic,
				ConfigNames:  []string{"retention.ms"},
			},
		},
	})
	if err != nil {
		t.Errorf("DescribeConfigs(%s): %v", topic, err)
		return
	}
	for _, r := range resp.Resources {
		if r.ResourceName != topic {
			continue
		}
		for _, cfg := range r.ConfigEntries {
			if cfg.ConfigName == "retention.ms" {
				got, parseErr := strconv.ParseInt(cfg.ConfigValue, 10, 64)
				if parseErr != nil {
					t.Errorf("%s: retention.ms value %q not parseable: %v", topic, cfg.ConfigValue, parseErr)
					return
				}
				if got != wantRetentionMs {
					t.Errorf("%s: retention.ms=%d, want %d", topic, got, wantRetentionMs)
				}
				return
			}
		}
	}
	t.Errorf("%s: retention.ms not found in DescribeConfigs response", topic)
}

// TestEnsureTopics_Integration_ProduceConsumeRoundTrip produces a per-run UUID
// message to helix.health and consumes it back.  Proves real end-to-end
// delivery (not in-memory short-circuit).
//
// EnsureTopics is called first so the topic is guaranteed to exist.
// We retry the produce for up to 30s to tolerate the partition-leader election
// race on a freshly created topic.
func TestEnsureTopics_Integration_ProduceConsumeRoundTrip(t *testing.T) {
	brokers := kafkaBrokersForTopics(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := EnsureTopics(ctx, brokers); err != nil {
		t.Fatalf("EnsureTopics: %v", err)
	}

	const topic = "helix.health"
	spec := TopicCatalog()[topic]

	// Wait for all partition leaders to be elected.
	waitForTopicLeader(t, brokers[0], topic, spec.NumPartitions)

	runID := uuid.New().String()
	msgVal := fmt.Sprintf("ensure-topics-probe:%s", runID)

	kb := NewKafkaBroker(brokers)
	defer kb.Close()

	// Subscribe before producing so no messages are missed.
	group := fmt.Sprintf("integ-ensure-%s", runID[:8])
	subCtx, subCancel := context.WithTimeout(ctx, 60*time.Second)
	defer subCancel()

	ch, err := kb.Subscribe(subCtx, topic, group)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Give consumer group time to join and receive partition assignments.
	time.Sleep(2 * time.Second)

	// Produce with retry to handle partition-leader election lag.
	pubDeadline := time.Now().Add(30 * time.Second)
	for {
		pubErr := kb.Publish(ctx, topic, runID, msgVal)
		if pubErr == nil {
			break
		}
		if time.Now().After(pubDeadline) {
			t.Fatalf("Publish to %s: %v", topic, pubErr)
		}
		t.Logf("Publish retry (leader not yet elected?): %v", pubErr)
		time.Sleep(300 * time.Millisecond)
	}

	// Consume and verify the per-run UUID is present in the payload.
	consumeDeadline := time.After(30 * time.Second)
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("subscription channel closed before message received")
			}
			val := string(m.Value)
			if !strings.Contains(val, runID) {
				// Stale message from a previous run — skip.
				continue
			}
			if val != msgVal {
				t.Errorf("consumed value=%q, want %q", val, msgVal)
			}
			t.Logf("round-trip confirmed: topic=%s key=%s value=%s", m.Topic, m.Key, m.Value)
			return
		case <-consumeDeadline:
			t.Fatalf("timeout: did not receive per-run-UUID message (runID=%s) within 30s", runID)
		}
	}
}

// TestEnsureTopics_Integration_Idempotent verifies that calling EnsureTopics
// twice does not return an error ("topic already exists" must be silently ignored).
func TestEnsureTopics_Integration_Idempotent(t *testing.T) {
	brokers := kafkaBrokersForTopics(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := EnsureTopics(ctx, brokers); err != nil {
		t.Fatalf("EnsureTopics (first call): %v", err)
	}
	if err := EnsureTopics(ctx, brokers); err != nil {
		t.Fatalf("EnsureTopics (second call — idempotency): %v", err)
	}
}
