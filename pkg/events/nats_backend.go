package events

import (
	"context"
	"encoding/base64"

	"digital.vasic.eventbus/pkg/event"
	natsbus "digital.vasic.eventbus/pkg/nats"
)

// NATSBackend is a real NATS/JetStream-backed event bus. It delegates to the
// decoupled digital.vasic.eventbus/pkg/nats backend, mapping the root
// Event{ID,Type,Payload,Timestamp} type to/from eventbus's event.Event.
//
// It is an additive alternative to the in-memory Bus: callers that want durable,
// distributed delivery construct a NATSBackend; the default in-memory Bus is
// untouched. The mapping carries ID, Type, and Timestamp verbatim and round-
// trips the raw Payload bytes losslessly through event.Event.Payload.
type NATSBackend struct {
	bus    *natsbus.Bus
	source string
}

// natsBackendSource is the event.Event.Source stamped on outbound events. The
// eventbus backend requires a source; it is opaque to the round-trip and is not
// surfaced on the root Event type.
const natsBackendSource = "helixcluster/pkg/events"

// NewNATSBackend connects to the NATS server at url and returns a real-backed
// event bus. The provided context bounds the connection setup. Close the
// returned backend to release the connection.
func NewNATSBackend(ctx context.Context, url string) (*NATSBackend, error) {
	bus, err := natsbus.New(ctx, natsbus.Config{URL: url})
	if err != nil {
		return nil, err
	}
	return &NATSBackend{bus: bus, source: natsBackendSource}, nil
}

// Publish maps the root Event onto an eventbus event.Event and publishes it.
// The raw Payload bytes are carried through event.Event.Payload; ID, Type, and
// Timestamp are preserved so a subscriber observes the same logical event.
func (n *NATSBackend) Publish(e Event) error {
	ev := &event.Event{
		ID:        e.ID,
		Type:      event.Type(e.Type),
		Source:    n.source,
		Payload:   e.Payload,
		Timestamp: e.Timestamp,
		Metadata:  map[string]string{},
	}
	return n.bus.Publish(ev)
}

// Subscribe subscribes to events of eventType and returns a channel delivering
// decoded root Events, an unsubscribe function, and an error. The returned
// channel is closed when the unsubscribe func is called, ctx is cancelled, or
// the backend is closed.
func (n *NATSBackend) Subscribe(
	ctx context.Context, eventType string,
) (<-chan Event, func(), error) {
	in, stop, err := n.bus.Subscribe(ctx, event.Type(eventType))
	if err != nil {
		return nil, nil, err
	}

	out := make(chan Event)
	go func() {
		defer close(out)
		for ev := range in {
			if ev == nil {
				continue
			}
			select {
			case out <- fromEventbusEvent(ev):
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, stop, nil
}

// Close releases the underlying NATS connection and stops all subscriptions.
func (n *NATSBackend) Close() error {
	return n.bus.Close()
}

// fromEventbusEvent maps an eventbus event.Event back onto the root Event type.
//
// event.Event.Payload is interface{} and crosses the wire as JSON. A []byte
// payload published by Publish arrives JSON-decoded as a base64 string, so it is
// normalised back to []byte here; other shapes are best-effort coerced so the
// round-trip never loses the ID/Type/Timestamp identity even when the payload
// was not produced by this adapter.
func fromEventbusEvent(ev *event.Event) Event {
	return Event{
		ID:        ev.ID,
		Type:      string(ev.Type),
		Payload:   payloadBytes(ev.Payload),
		Timestamp: ev.Timestamp,
	}
}

// payloadBytes coerces an eventbus payload (post-JSON-decode) into the root
// Event's []byte payload. JSON marshals []byte as a base64 string, so a string
// is base64-decoded; a []byte (in-process delivery) is returned as-is; nil maps
// to nil.
func payloadBytes(p interface{}) []byte {
	switch v := p.(type) {
	case nil:
		return nil
	case []byte:
		return v
	case string:
		if b, err := base64.StdEncoding.DecodeString(v); err == nil {
			return b
		}
		return []byte(v)
	default:
		return nil
	}
}
