package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
)

const topicPrefix = "/helix/messaging/topics/"

// TopicRegistry provides etcd-backed topic discovery.
type TopicRegistry struct {
	client *etcd.Client
}

// NewTopicRegistry creates a new TopicRegistry backed by the given etcd client.
func NewTopicRegistry(client *etcd.Client) *TopicRegistry {
	return &TopicRegistry{client: client}
}

// RegisterTopic registers a topic in etcd.
func (r *TopicRegistry) RegisterTopic(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("topic name cannot be empty")
	}
	key := topicPrefix + name
	if err := r.client.Put(ctx, key, name); err != nil {
		return fmt.Errorf("failed to register topic %q: %w", name, err)
	}
	return nil
}

// DiscoverTopics returns a list of all registered topics from etcd.
func (r *TopicRegistry) DiscoverTopics(ctx context.Context) ([]string, error) {
	kvs, err := r.client.GetPrefix(ctx, topicPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to discover topics: %w", err)
	}
	topics := make([]string, 0, len(kvs))
	for key := range kvs {
		name := strings.TrimPrefix(key, topicPrefix)
		if name != "" {
			topics = append(topics, name)
		}
	}
	return topics, nil
}

// DeleteTopic removes a topic from etcd.
func (r *TopicRegistry) DeleteTopic(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("topic name cannot be empty")
	}
	key := topicPrefix + name
	if err := r.client.Delete(ctx, key); err != nil {
		return fmt.Errorf("failed to delete topic %q: %w", name, err)
	}
	return nil
}
