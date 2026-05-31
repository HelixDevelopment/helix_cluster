package messaging

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Message represents a message on the bus.
type Message struct {
	ID        string            `json:"id"`
	Topic     string            `json:"topic"`
	Payload   []byte            `json:"payload"`
	Timestamp time.Time         `json:"timestamp"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// NewMessage creates a new Message with the given topic and payload.
func NewMessage(topic string, payload []byte) *Message {
	return &Message{
		ID:        uuid.New().String(),
		Topic:     topic,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
		Headers:   make(map[string]string),
	}
}

// Encode serializes the Message to JSON bytes.
func (m *Message) Encode() ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("failed to encode message: %w", err)
	}
	return b, nil
}

// DecodeMessage deserializes JSON bytes into a Message.
func DecodeMessage(data []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to decode message: %w", err)
	}
	if m.Headers == nil {
		m.Headers = make(map[string]string)
	}
	return &m, nil
}
