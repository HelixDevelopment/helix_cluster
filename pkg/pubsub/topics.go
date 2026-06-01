// Package pubsub – Kafka topic configuration and idempotent topic provisioning.
//
// TopicCatalog is the authoritative table of Helix control-plane topics.
// EnsureTopics creates topics that do not yet exist via the kafka-go admin API,
// setting NumPartitions, ReplicationFactor and retention.ms for each entry.
// It is idempotent: calling it on an already-existing topic is a safe no-op.
package pubsub

import (
	"context"
	"fmt"
	"strconv"

	kafka "github.com/segmentio/kafka-go"
)

// TopicSpec is the authoritative configuration for a single Kafka topic.
type TopicSpec struct {
	// NumPartitions controls parallelism and key-routing fan-out.
	NumPartitions int
	// ReplicationFactor is 1 for single-node, ≥2 for HA clusters.
	ReplicationFactor int
	// RetentionMs is the retention.ms topic config value (milliseconds).
	// 0 means "use broker default" (not written as a ConfigEntry).
	RetentionMs int64
}

// topicCatalog is the authoritative map: topic name → TopicSpec.
// Exported via TopicCatalog() to prevent external mutation.
var topicCatalog = map[string]TopicSpec{
	"helix.nodes": {
		NumPartitions:     6,
		ReplicationFactor: 1,
		RetentionMs:       7 * 24 * 60 * 60 * 1000, // 7 days
	},
	"helix.sessions": {
		NumPartitions:     4,
		ReplicationFactor: 1,
		RetentionMs:       24 * 60 * 60 * 1000, // 1 day
	},
	"helix.scheduler": {
		NumPartitions:     8,
		ReplicationFactor: 1,
		RetentionMs:       3 * 24 * 60 * 60 * 1000, // 3 days
	},
	"helix.health": {
		NumPartitions:     4,
		ReplicationFactor: 1,
		RetentionMs:       6 * 60 * 60 * 1000, // 6 hours
	},
	"helix.alerts": {
		NumPartitions:     2,
		ReplicationFactor: 1,
		RetentionMs:       30 * 24 * 60 * 60 * 1000, // 30 days
	},
}

// TopicCatalog returns a snapshot copy of the authoritative topic table.
// Callers may iterate or assert individual entries without risk of mutation.
func TopicCatalog() map[string]TopicSpec {
	out := make(map[string]TopicSpec, len(topicCatalog))
	for k, v := range topicCatalog {
		out[k] = v
	}
	return out
}

// TopicNames returns the sorted list of control-plane topic names.
func TopicNames() []string {
	names := make([]string, 0, len(topicCatalog))
	for k := range topicCatalog {
		names = append(names, k)
	}
	return names
}

// buildTopicConfig converts a TopicSpec into the kafka-go TopicConfig that
// EnsureTopics (or an injected admin seam) must receive.
func buildTopicConfig(name string, spec TopicSpec) kafka.TopicConfig {
	tc := kafka.TopicConfig{
		Topic:             name,
		NumPartitions:     spec.NumPartitions,
		ReplicationFactor: spec.ReplicationFactor,
	}
	if spec.RetentionMs > 0 {
		tc.ConfigEntries = []kafka.ConfigEntry{
			{
				ConfigName:  "retention.ms",
				ConfigValue: strconv.FormatInt(spec.RetentionMs, 10),
			},
		}
	}
	return tc
}

// AdminSeam is the injection point used by unit tests.  The real
// implementation is kafkaConnAdmin (backed by kafka-go Conn); tests inject
// fakeAdmin to assert exact TopicConfig arguments without a live broker.
type AdminSeam interface {
	// CreateTopics sends one CreateTopics call per topic.  Implementations
	// MUST tolerate "topic already exists" as a non-error (idempotency).
	CreateTopics(topics ...kafka.TopicConfig) error
	// Close releases the underlying connection.
	Close() error
}

// kafkaConnAdmin adapts kafka.Conn to AdminSeam.
type kafkaConnAdmin struct{ conn *kafka.Conn }

func (a *kafkaConnAdmin) CreateTopics(topics ...kafka.TopicConfig) error {
	return a.conn.CreateTopics(topics...)
}
func (a *kafkaConnAdmin) Close() error { return a.conn.Close() }

// dialController dials the broker, resolves the controller, then dials the
// controller and returns a *kafkaConnAdmin ready for admin operations.
func dialController(ctx context.Context, broker string) (*kafkaConnAdmin, error) {
	conn, err := kafka.DialContext(ctx, "tcp", broker)
	if err != nil {
		return nil, fmt.Errorf("pubsub.dialController: dial %s: %w", broker, err)
	}

	controller, err := conn.Controller()
	conn.Close()
	if err != nil {
		return nil, fmt.Errorf("pubsub.dialController: get controller: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", controller.Host, controller.Port)
	cconn, err := kafka.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pubsub.dialController: dial controller %s: %w", addr, err)
	}
	return &kafkaConnAdmin{conn: cconn}, nil
}

// EnsureTopics creates all control-plane topics that do not yet exist.
// It is idempotent: "topic already exists" errors are silently ignored.
// The admin connection is acquired from brokers[0]; subsequent elements are
// used as fall-backs if the first is unreachable.
//
// EnsureTopicsWithSeam is the seam-injectable variant used by unit tests.
func EnsureTopics(ctx context.Context, brokers []string) error {
	if len(brokers) == 0 {
		return fmt.Errorf("pubsub.EnsureTopics: brokers must not be empty")
	}

	var admin *kafkaConnAdmin
	var lastErr error
	for _, b := range brokers {
		admin, lastErr = dialController(ctx, b)
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		return fmt.Errorf("pubsub.EnsureTopics: all brokers failed: %w", lastErr)
	}
	defer admin.Close()

	return EnsureTopicsWithSeam(ctx, admin)
}

// EnsureTopicsWithSeam is the seam-injectable entry point that accepts any
// AdminSeam.  Unit tests call this directly with a fakeAdmin; production code
// calls EnsureTopics which wires a kafkaConnAdmin.
func EnsureTopicsWithSeam(_ context.Context, admin AdminSeam) error {
	configs := make([]kafka.TopicConfig, 0, len(topicCatalog))
	for name, spec := range topicCatalog {
		configs = append(configs, buildTopicConfig(name, spec))
	}
	if err := admin.CreateTopics(configs...); err != nil {
		return fmt.Errorf("pubsub.EnsureTopicsWithSeam: CreateTopics: %w", err)
	}
	return nil
}
