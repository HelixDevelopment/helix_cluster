// Package pubsub provides publish/subscribe abstractions for Helix Cluster OS.
// It ships two implementations behind a common interface:
//   - InMemoryBroker  – lock-free channel map, used in unit tests.
//   - KafkaBroker     – real segmentio/kafka-go producer+consumer, acks=all,
//     at-least-once delivery with explicit offset commit.
package pubsub

import "context"

// Message is a message delivered by a Broker subscription.
type Message struct {
	Topic string
	Key   []byte
	Value []byte
}

// Broker is the top-level publish/subscribe capability.
// Both the in-memory stub and the Kafka client implement this interface so that
// production code and unit tests share the same surface.
type Broker interface {
	// Publish sends a single message to the given topic.
	// Key is used for Kafka partition routing (hash-based).
	// Implementations MUST use idempotent, acks=all semantics where the
	// underlying transport supports it.
	Publish(ctx context.Context, topic, key, value string) error

	// Subscribe returns a channel that receives all messages for topic/group.
	// group is the consumer-group identifier; each group member competes for
	// messages (at-least-once, exactly-one-consumer-per-key within a group).
	// The returned channel is closed when ctx is cancelled.
	Subscribe(ctx context.Context, topic, group string) (<-chan Message, error)

	// Close releases all resources held by the broker.
	Close() error
}

// WriterConfig captures the configuration that was applied to the Kafka writer.
// Exposed so unit tests can assert the seam without touching a live broker.
type WriterConfig struct {
	Brokers      []string
	RequiredAcks int  // kafka-go: RequireAll == -1
	Async        bool // must be false for acks=all
}

// ReaderConfig captures configuration applied to each Kafka reader.
type ReaderConfig struct {
	Brokers []string
	GroupID string
	Topic   string
}
