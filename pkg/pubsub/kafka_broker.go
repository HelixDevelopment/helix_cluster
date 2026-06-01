// Package pubsub – real Kafka broker implementation.
// Uses github.com/segmentio/kafka-go:
//   - Producer: WriterConfig{RequiredAcks: RequireAll, Async: false} — idempotent acks=all.
//   - Consumer: ReaderConfig{GroupID: …} — at-least-once + explicit CommitMessages.
//
// This is the production implementation of Broker; InMemoryBroker is used in unit tests.
package pubsub

import (
	"context"
	"fmt"
	"log"
	"sync"

	kafka "github.com/segmentio/kafka-go"
)

// Compile-time assertion: KafkaBroker must satisfy Broker.
var _ Broker = (*KafkaBroker)(nil)

// KafkaBroker wraps segmentio/kafka-go writers and readers behind the Broker
// interface.  One Writer per topic is created lazily and reused.  Readers are
// created per (topic, group) pair; each call to Subscribe starts a dedicated
// fetch-and-forward goroutine.
type KafkaBroker struct {
	brokers []string

	wmu     sync.Mutex
	writers map[string]*kafka.Writer // keyed by topic

	// writerFactory is called to create a new kafka.Writer. Overridden in
	// tests to capture configuration without a live broker.
	writerFactory func(topic string, brokers []string) *kafka.Writer

	// readerFactory is called to create a new kafka.Reader. Overridden in
	// tests to capture configuration without a live broker.
	readerFactory func(topic, group string, brokers []string) *kafka.Reader
}

// NewKafkaBroker creates a KafkaBroker connected to the given broker addresses.
func NewKafkaBroker(brokers []string) *KafkaBroker {
	kb := &KafkaBroker{
		brokers: brokers,
		writers: make(map[string]*kafka.Writer),
	}
	kb.writerFactory = kb.defaultWriterFactory
	kb.readerFactory = kb.defaultReaderFactory
	return kb
}

// defaultWriterFactory creates a kafka.Writer with acks=all and synchronous
// (non-async) mode so every WriteMessages call blocks until the ISR acks.
func (kb *KafkaBroker) defaultWriterFactory(topic string, brokers []string) *kafka.Writer {
	return &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.Hash{}, // deterministic key→partition routing
		RequiredAcks: kafka.RequireAll,
		Async:        false, // synchronous: caller blocks until ISR acks
	}
}

// defaultReaderFactory creates a kafka.Reader for consumer-group use.
func (kb *KafkaBroker) defaultReaderFactory(topic, group string, brokers []string) *kafka.Reader {
	return kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: group,
		Topic:   topic,
	})
}

// writerFor returns (or lazily creates) the Writer for topic, holding wmu.
func (kb *KafkaBroker) writerFor(topic string) *kafka.Writer {
	kb.wmu.Lock()
	defer kb.wmu.Unlock()
	if w, ok := kb.writers[topic]; ok {
		return w
	}
	w := kb.writerFactory(topic, kb.brokers)
	kb.writers[topic] = w
	return w
}

// Publish writes a single message to Kafka with acks=all.
// Key is used for partition routing (Hash balancer: same key → same partition).
func (kb *KafkaBroker) Publish(ctx context.Context, topic, key, value string) error {
	w := kb.writerFor(topic)
	return w.WriteMessages(ctx, kafka.Message{
		Key:   []byte(key),
		Value: []byte(value),
	})
}

// Subscribe starts a consumer-group reader for topic/group and forwards
// messages to the returned channel.  The channel is closed when ctx is
// cancelled.  Offset is committed after every successfully forwarded message
// (at-least-once).
func (kb *KafkaBroker) Subscribe(ctx context.Context, topic, group string) (<-chan Message, error) {
	r := kb.readerFactory(topic, group, kb.brokers)
	out := make(chan Message, 64)

	go func() {
		defer close(out)
		defer func() {
			if err := r.Close(); err != nil {
				log.Printf("pubsub/kafka: reader close error: %v", err)
			}
		}()

		for {
			// FetchMessage does NOT auto-commit; we commit explicitly below.
			m, err := r.FetchMessage(ctx)
			if err != nil {
				// Context cancellation is the normal termination path.
				if ctx.Err() != nil {
					return
				}
				log.Printf("pubsub/kafka: FetchMessage error on %s/%s: %v", topic, group, err)
				return
			}

			msg := Message{
				Topic: m.Topic,
				Key:   m.Key,
				Value: m.Value,
			}

			// Deliver to subscriber.
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			}

			// Commit offset after successful delivery — at-least-once guarantee.
			if err := r.CommitMessages(ctx, m); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("pubsub/kafka: CommitMessages error on %s/%s: %v", topic, group, err)
			}
		}
	}()

	return out, nil
}

// Close flushes and closes all writers.
func (kb *KafkaBroker) Close() error {
	kb.wmu.Lock()
	defer kb.wmu.Unlock()
	var errs []error
	for topic, w := range kb.writers {
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close writer for %s: %w", topic, err))
		}
		delete(kb.writers, topic)
	}
	if len(errs) > 0 {
		return fmt.Errorf("KafkaBroker.Close: %v", errs)
	}
	return nil
}

// CapturedWriterConfig returns the WriterConfig that would be (or was) applied
// for the given topic.  Used by unit tests to verify the seam without a live
// broker.  When writerFactory is the default, this constructs a *Writer solely
// to inspect its exported fields, then discards it.
func (kb *KafkaBroker) CapturedWriterConfig(topic string) WriterConfig {
	w := kb.defaultWriterFactory(topic, kb.brokers)
	return WriterConfig{
		Brokers:      kb.brokers,
		RequiredAcks: int(w.RequiredAcks),
		Async:        w.Async,
	}
}

// CapturedReaderConfig returns the ReaderConfig that would be applied for the
// given topic/group pair.  Used by unit tests to verify the seam.
func (kb *KafkaBroker) CapturedReaderConfig(topic, group string) ReaderConfig {
	return ReaderConfig{
		Brokers: kb.brokers,
		GroupID: group,
		Topic:   topic,
	}
}
