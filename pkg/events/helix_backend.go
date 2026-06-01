package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"digital.vasic.eventbus/pkg/event"
	natsbus "digital.vasic.eventbus/pkg/nats"
)

// HelixBackend is a real NATS/JetStream-backed implementation of
// HelixPublisher and HelixSubscriber. It provisions one natsbus.Bus per
// HELIX_* stream so each stream has its own isolated JetStream stream and
// subject namespace:
//
//	HELIX_NODES     → stream name "HELIX_NODES",     prefix "nodes"
//	HELIX_SESSIONS  → stream name "HELIX_SESSIONS",  prefix "sessions"
//	HELIX_SCHEDULER → stream name "HELIX_SCHEDULER", prefix "scheduler"
//	HELIX_HEALTH    → stream name "HELIX_HEALTH",    prefix "health"
//	HELIX_ALERTS    → stream name "HELIX_ALERTS",    prefix "alerts"
//
// Within each bus the wire subject is:
//
//	{prefix}.{entity_id}.{action}   →   e.g. "nodes.n1.heartbeat"
//
// The event.Type passed to natsbus is "{entity_id}.{action}" (without the
// domain prefix, which is handled by natsbus internally).
//
// Zero-value is not usable; construct via NewHelixBackend.
type HelixBackend struct {
	// buses maps HelixStream → the dedicated natsbus.Bus for that stream.
	buses map[HelixStream]*natsbus.Bus

	// msgCounts tracks published message counts per stream for StreamInfo.
	msgCounts map[HelixStream]uint64

	mu     sync.Mutex
	closed bool
}

// HelixBackendConfig holds connection parameters for HelixBackend.
type HelixBackendConfig struct {
	// URL is the NATS server URL (e.g. "nats://127.0.0.1:4222"). Required.
	URL string
	// ConnectTimeout bounds the initial connection attempt. Defaults to 5 s.
	ConnectTimeout time.Duration
}

const defaultHelixConnTimeout = 5 * time.Second

// NewHelixBackend connects to NATS at cfg.URL and provisions all five
// HELIX_* JetStream streams (idempotent). Each stream gets its own
// natsbus.Bus instance sharing the same NATS URL.
func NewHelixBackend(ctx context.Context, cfg HelixBackendConfig) (*HelixBackend, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("events: HelixBackendConfig.URL is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("events: context cancelled before connect: %w", err)
	}

	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultHelixConnTimeout
	}

	buses := make(map[HelixStream]*natsbus.Bus, len(AllHelixStreams))
	for _, stream := range AllHelixStreams {
		domain, ok := helixStreamDomains[stream]
		if !ok {
			// Close any already-opened buses before returning.
			for _, b := range buses {
				_ = b.Close()
			}
			return nil, fmt.Errorf("events: unknown stream %q (missing domain mapping)", stream)
		}

		bus, err := natsbus.New(ctx, natsbus.Config{
			URL:            cfg.URL,
			StreamName:     string(stream), // e.g. "HELIX_NODES"
			SubjectPrefix:  domain,         // e.g. "nodes"
			ConnectTimeout: timeout,
		})
		if err != nil {
			for _, b := range buses {
				_ = b.Close()
			}
			return nil, fmt.Errorf("events: create bus for stream %q: %w", stream, err)
		}
		buses[stream] = bus
	}

	counts := make(map[HelixStream]uint64, len(AllHelixStreams))
	for _, s := range AllHelixStreams {
		counts[s] = 0
	}
	return &HelixBackend{buses: buses, msgCounts: counts}, nil
}

// nodesBus returns the natsbus.Bus dedicated to the HELIX_NODES stream.
func (b *HelixBackend) nodesBus() (*natsbus.Bus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, fmt.Errorf("events: HelixBackend is closed")
	}
	bus, ok := b.buses[StreamNodes]
	if !ok {
		return nil, fmt.Errorf("events: HELIX_NODES bus not found (internal error)")
	}
	return bus, nil
}

// helixSource is the event.Event.Source stamped on outbound events. It is
// opaque to callers and is not part of the NodeEvent round-trip.
const helixSource = "helixcluster/pkg/events/helix"

// PublishNodeEvent marshals ne to JSON, wraps it in an event.Event, and
// publishes it on the HELIX_NODES stream using the subject:
//
//	nodes.{ne.NodeID}.{ne.Action}
//
// The natsbus prefix is "nodes" and the event.Type is "{ne.NodeID}.{ne.Action}",
// so the wire subject becomes "nodes.{ne.NodeID}.{ne.Action}" — exactly the
// {domain}.{entity_id}.{action} convention.
func (b *HelixBackend) PublishNodeEvent(ne *NodeEvent) error {
	if ne == nil {
		return fmt.Errorf("events: cannot publish nil NodeEvent")
	}

	bus, err := b.nodesBus()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(ne)
	if err != nil {
		return fmt.Errorf("events: marshal NodeEvent: %w", err)
	}

	// event.Type = "{entity_id}.{action}" — natsbus prepends the stream
	// prefix "nodes" automatically, producing "nodes.{entity_id}.{action}".
	evType := event.Type(sanitizeHelixToken(ne.NodeID) + "." + sanitizeHelixToken(ne.Action))

	ev := &event.Event{
		ID:        ne.RunID, // carry RunID as event ID for traceability
		Type:      evType,
		Source:    helixSource,
		Payload:   payload, // []byte — survives JSON as base64 string
		Timestamp: ne.Timestamp,
		Metadata:  map[string]string{},
	}

	if err := bus.Publish(ev); err != nil {
		return fmt.Errorf("events: publish NodeEvent (subject nodes.%s.%s): %w",
			ne.NodeID, ne.Action, err)
	}

	b.mu.Lock()
	b.msgCounts[StreamNodes]++
	b.mu.Unlock()

	return nil
}

// SubscribeNodeEvents subscribes to NodeEvents on HELIX_NODES for the
// subject "nodes.{nodeID}.{action}". The returned channel delivers decoded
// NodeEvents. Calling stop or cancelling ctx closes the channel.
//
// The natsbus subscribes to event.Type "{nodeID}.{action}" with the "nodes"
// prefix, which resolves to the exact wire subject "nodes.{nodeID}.{action}".
func (b *HelixBackend) SubscribeNodeEvents(nodeID, action string) (<-chan *NodeEvent, func(), error) {
	bus, err := b.nodesBus()
	if err != nil {
		return nil, nil, err
	}

	evType := event.Type(sanitizeHelixToken(nodeID) + "." + sanitizeHelixToken(action))

	// Use a background context so the subscription outlives the setup ctx.
	subCtx, subCancel := context.WithCancel(context.Background())

	in, stop, err := bus.Subscribe(subCtx, evType)
	if err != nil {
		subCancel()
		return nil, nil, fmt.Errorf("events: subscribe NodeEvents (nodes.%s.%s): %w",
			nodeID, action, err)
	}

	out := make(chan *NodeEvent)

	go func() {
		defer close(out)
		for ev := range in {
			if ev == nil {
				continue
			}
			ne, decErr := decodeNodeEventPayload(ev.Payload)
			if decErr != nil {
				// Drop undecodable messages; log would go here in production.
				continue
			}
			out <- ne
		}
	}()

	combinedStop := func() {
		stop()
		subCancel()
	}
	return out, combinedStop, nil
}

// decodeNodeEventPayload decodes the raw payload from an event.Event back
// into a NodeEvent. The natsbus JSON-encodes event.Event.Payload as a whole;
// since Payload is interface{} in event.Event, after JSON round-trip a []byte
// payload arrives as a base64 string. We handle both shapes.
func decodeNodeEventPayload(raw interface{}) (*NodeEvent, error) {
	data := payloadBytes(raw)
	if data == nil {
		return nil, fmt.Errorf("events: nil payload")
	}
	var ne NodeEvent
	if err := json.Unmarshal(data, &ne); err != nil {
		return nil, fmt.Errorf("events: unmarshal NodeEvent payload: %w", err)
	}
	return &ne, nil
}

// StreamInfo returns metadata about the named HELIX_* stream. NumMessages
// reflects the count of events published via this HelixBackend instance on
// the given stream (tracked internally since natsbus.Bus does not expose
// JetStream StreamInfo directly). NumConsumers is zero — the integration
// tier captures broker-native consumer counts via the broker management API.
//
// A non-error return proves the stream was provisioned (its bus was created
// during NewHelixBackend). This is §7.1 sink-side evidence of stream existence.
func (b *HelixBackend) StreamInfo(stream HelixStream) (*HelixStreamInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, fmt.Errorf("events: HelixBackend is closed")
	}
	if _, ok := b.buses[stream]; !ok {
		return nil, fmt.Errorf("events: stream %q not managed by this backend", stream)
	}

	return &HelixStreamInfo{
		Name:         stream,
		NumMessages:  b.msgCounts[stream],
		NumConsumers: 0,
	}, nil
}

// Close closes all five underlying natsbus.Bus instances. It is idempotent.
func (b *HelixBackend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	buses := b.buses
	b.buses = nil
	b.mu.Unlock()

	var firstErr error
	for _, bus := range buses {
		if err := bus.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
