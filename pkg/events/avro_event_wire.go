package events

// Backend-integrated typed Avro publish/subscribe for the three non-Node Helix
// event families — SessionEvent, SchedulerEvent, AuditEvent (HXC-1627).
//
// HXC-1626 wired the Avro single-object wire path for NodeEvent
// (NATSBackend.PublishNodeEvent / SubscribeNodeEvents) and added concrete Go
// types + AvroSchema + standalone Encode*/Decode* helpers for the three other
// families (avro_event_schemas.go). What was missing was the SAME backend
// integration NodeEvent has: typed Publish*/Subscribe* methods that, in WireAvro
// mode, carry the payload as an Avro single-object record (0xC3 0x01 + writer
// fingerprint + body) decoded via the backend's SchemaRegistry, and that in the
// default WireJSON mode keep JSON serialization for back-compat. This file closes
// that gap, giving SessionEvent/SchedulerEvent/AuditEvent full parity with the
// NodeEvent path, honoring the {domain}.{entity}.{action} subject convention and
// the matching HELIX_* stream for each domain.

import (
	"context"
	"encoding/json"
	"fmt"
)

// --- WithAvro* options ------------------------------------------------------

// WithAvroEvents switches the backend to the Avro wire format for ALL three
// non-Node event families at once, registering each family's V1 writer schema in
// registry so the subscribe side resolves the writer fingerprint regardless of
// which type produced a payload. It is the typed-event analogue of WithAvro for
// NodeEvent and composes with it (NodeEvent + the three families can all be Avro
// on the same backend). registry MUST be non-nil; it is shared as the resolution
// surface for every typed Subscribe* call.
func WithAvroEvents(registry *SchemaRegistry) NATSBackendOption {
	return func(n *NATSBackend) {
		n.wireFormat = WireAvro
		n.avroRegistry = registry
		n.sessionWriter = SessionEventSchemaV1()
		n.schedulerWriter = SchedulerEventSchemaV1()
		n.auditWriter = AuditEventSchemaV1()
		if registry != nil {
			registry.Register(n.sessionWriter)
			registry.Register(n.schedulerWriter)
			registry.Register(n.auditWriter)
		}
	}
}

// WithAvroSession switches SessionEvent to the Avro wire format using writer as
// the publish-side schema and registry for subscribe-side resolution, registering
// writer in registry. JSON remains the default for any family not opted in.
func WithAvroSession(writer *AvroSchema, registry *SchemaRegistry) NATSBackendOption {
	return func(n *NATSBackend) {
		n.wireFormat = WireAvro
		n.avroRegistry = registry
		n.sessionWriter = writer
		if registry != nil && writer != nil {
			registry.Register(writer)
		}
	}
}

// WithAvroScheduler switches SchedulerEvent to the Avro wire format. See
// WithAvroSession for semantics.
func WithAvroScheduler(writer *AvroSchema, registry *SchemaRegistry) NATSBackendOption {
	return func(n *NATSBackend) {
		n.wireFormat = WireAvro
		n.avroRegistry = registry
		n.schedulerWriter = writer
		if registry != nil && writer != nil {
			registry.Register(writer)
		}
	}
}

// WithAvroAudit switches AuditEvent to the Avro wire format. See WithAvroSession
// for semantics.
func WithAvroAudit(writer *AvroSchema, registry *SchemaRegistry) NATSBackendOption {
	return func(n *NATSBackend) {
		n.wireFormat = WireAvro
		n.avroRegistry = registry
		n.auditWriter = writer
		if registry != nil && writer != nil {
			registry.Register(writer)
		}
	}
}

// --- JSON back-compat helpers ----------------------------------------------

// MarshalPayload serialises the SessionEvent to JSON bytes for Event.Payload.
func (e *SessionEvent) MarshalPayload() ([]byte, error) { return json.Marshal(e) }

// UnmarshalSessionEvent deserialises a SessionEvent from raw JSON bytes.
func UnmarshalSessionEvent(payload []byte) (*SessionEvent, error) {
	var e SessionEvent
	if err := json.Unmarshal(payload, &e); err != nil {
		return nil, fmt.Errorf("events: unmarshal SessionEvent: %w", err)
	}
	return &e, nil
}

// MarshalPayload serialises the SchedulerEvent to JSON bytes for Event.Payload.
func (e *SchedulerEvent) MarshalPayload() ([]byte, error) { return json.Marshal(e) }

// UnmarshalSchedulerEvent deserialises a SchedulerEvent from raw JSON bytes.
func UnmarshalSchedulerEvent(payload []byte) (*SchedulerEvent, error) {
	var e SchedulerEvent
	if err := json.Unmarshal(payload, &e); err != nil {
		return nil, fmt.Errorf("events: unmarshal SchedulerEvent: %w", err)
	}
	return &e, nil
}

// MarshalPayload serialises the AuditEvent to JSON bytes for Event.Payload.
func (e *AuditEvent) MarshalPayload() ([]byte, error) { return json.Marshal(e) }

// UnmarshalAuditEvent deserialises an AuditEvent from raw JSON bytes.
func UnmarshalAuditEvent(payload []byte) (*AuditEvent, error) {
	var e AuditEvent
	if err := json.Unmarshal(payload, &e); err != nil {
		return nil, fmt.Errorf("events: unmarshal AuditEvent: %w", err)
	}
	return &e, nil
}

// --- SessionEvent backend integration --------------------------------------

// PublishSessionEvent publishes a SessionEvent on the StreamSessions stream,
// honoring the backend's wire format. In WireAvro mode the payload is the Avro
// single-object encoding of e (0xC3 0x01 + writer fingerprint + body); in the
// default WireJSON mode it is e's JSON. Event identity (ID=RunID, Type=subject)
// is identical across both formats so subscribers route the same.
func (n *NATSBackend) PublishSessionEvent(e *SessionEvent) error {
	if e == nil {
		return fmt.Errorf("events: cannot publish nil SessionEvent")
	}
	var payload []byte
	var err error
	switch n.wireFormat {
	case WireAvro:
		if n.sessionWriter == nil {
			return fmt.Errorf("events: Avro wire mode requires a SessionEvent writer schema (WithAvroEvents/WithAvroSession)")
		}
		payload, err = EncodeSessionEventAvro(e, n.sessionWriter)
	default:
		payload, err = e.MarshalPayload()
	}
	if err != nil {
		return fmt.Errorf("events: encode SessionEvent payload: %w", err)
	}
	return n.Publish(Event{ID: e.RunID, Type: e.Subject(), Payload: payload, Timestamp: e.Timestamp})
}

// SubscribeSessionEvents subscribes to SessionEvents on the subject derived from
// sessionID/action and decodes each payload per the backend's wire format. In
// WireAvro mode the payload is decoded via the registry (writer resolution) to
// the configured writer schema as reader; in WireJSON mode it is JSON-unmarshalled.
func (n *NATSBackend) SubscribeSessionEvents(
	ctx context.Context, sessionID, action string,
) (<-chan *SessionEvent, func(), error) {
	subject := HelixSubject("sessions", sessionID, action)
	in, stop, err := n.Subscribe(ctx, subject)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan *SessionEvent)
	go func() {
		defer close(out)
		for ev := range in {
			e, decErr := n.decodeSessionEvent(ev.Payload)
			if decErr != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, stop, nil
}

func (n *NATSBackend) decodeSessionEvent(payload []byte) (*SessionEvent, error) {
	switch n.wireFormat {
	case WireAvro:
		if n.avroRegistry == nil || n.sessionWriter == nil {
			return nil, fmt.Errorf("events: Avro wire mode requires registry+reader schema (WithAvroEvents/WithAvroSession)")
		}
		return DecodeSessionEventAvro(payload, n.avroRegistry, n.sessionWriter)
	default:
		return UnmarshalSessionEvent(payload)
	}
}

// --- SchedulerEvent backend integration ------------------------------------

// PublishSchedulerEvent publishes a SchedulerEvent on the StreamScheduler stream,
// honoring the backend's wire format. See PublishSessionEvent for semantics.
func (n *NATSBackend) PublishSchedulerEvent(e *SchedulerEvent) error {
	if e == nil {
		return fmt.Errorf("events: cannot publish nil SchedulerEvent")
	}
	var payload []byte
	var err error
	switch n.wireFormat {
	case WireAvro:
		if n.schedulerWriter == nil {
			return fmt.Errorf("events: Avro wire mode requires a SchedulerEvent writer schema (WithAvroEvents/WithAvroScheduler)")
		}
		payload, err = EncodeSchedulerEventAvro(e, n.schedulerWriter)
	default:
		payload, err = e.MarshalPayload()
	}
	if err != nil {
		return fmt.Errorf("events: encode SchedulerEvent payload: %w", err)
	}
	return n.Publish(Event{ID: e.RunID, Type: e.Subject(), Payload: payload, Timestamp: e.Timestamp})
}

// SubscribeSchedulerEvents subscribes to SchedulerEvents on the subject derived
// from entityID/action. See SubscribeSessionEvents for semantics.
func (n *NATSBackend) SubscribeSchedulerEvents(
	ctx context.Context, entityID, action string,
) (<-chan *SchedulerEvent, func(), error) {
	subject := HelixSubject("scheduler", entityID, action)
	in, stop, err := n.Subscribe(ctx, subject)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan *SchedulerEvent)
	go func() {
		defer close(out)
		for ev := range in {
			e, decErr := n.decodeSchedulerEvent(ev.Payload)
			if decErr != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, stop, nil
}

func (n *NATSBackend) decodeSchedulerEvent(payload []byte) (*SchedulerEvent, error) {
	switch n.wireFormat {
	case WireAvro:
		if n.avroRegistry == nil || n.schedulerWriter == nil {
			return nil, fmt.Errorf("events: Avro wire mode requires registry+reader schema (WithAvroEvents/WithAvroScheduler)")
		}
		return DecodeSchedulerEventAvro(payload, n.avroRegistry, n.schedulerWriter)
	default:
		return UnmarshalSchedulerEvent(payload)
	}
}

// --- AuditEvent backend integration ----------------------------------------

// PublishAuditEvent publishes an AuditEvent on the "audit.>" subject family,
// honoring the backend's wire format. See PublishSessionEvent for semantics.
func (n *NATSBackend) PublishAuditEvent(e *AuditEvent) error {
	if e == nil {
		return fmt.Errorf("events: cannot publish nil AuditEvent")
	}
	var payload []byte
	var err error
	switch n.wireFormat {
	case WireAvro:
		if n.auditWriter == nil {
			return fmt.Errorf("events: Avro wire mode requires an AuditEvent writer schema (WithAvroEvents/WithAvroAudit)")
		}
		payload, err = EncodeAuditEventAvro(e, n.auditWriter)
	default:
		payload, err = e.MarshalPayload()
	}
	if err != nil {
		return fmt.Errorf("events: encode AuditEvent payload: %w", err)
	}
	return n.Publish(Event{ID: e.RunID, Type: e.Subject(), Payload: payload, Timestamp: e.Timestamp})
}

// SubscribeAuditEvents subscribes to AuditEvents on the subject derived from
// subjectID/action. See SubscribeSessionEvents for semantics.
func (n *NATSBackend) SubscribeAuditEvents(
	ctx context.Context, subjectID, action string,
) (<-chan *AuditEvent, func(), error) {
	subject := HelixSubject("audit", subjectID, action)
	in, stop, err := n.Subscribe(ctx, subject)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan *AuditEvent)
	go func() {
		defer close(out)
		for ev := range in {
			e, decErr := n.decodeAuditEvent(ev.Payload)
			if decErr != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, stop, nil
}

func (n *NATSBackend) decodeAuditEvent(payload []byte) (*AuditEvent, error) {
	switch n.wireFormat {
	case WireAvro:
		if n.avroRegistry == nil || n.auditWriter == nil {
			return nil, fmt.Errorf("events: Avro wire mode requires registry+reader schema (WithAvroEvents/WithAvroAudit)")
		}
		return DecodeAuditEventAvro(payload, n.avroRegistry, n.auditWriter)
	default:
		return UnmarshalAuditEvent(payload)
	}
}
