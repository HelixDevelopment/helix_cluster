package events

// Avro single-object wire path for the live NATS HelixBackend (HXC-1626).
//
// HXC-1119 delivered the Avro-subset codec (avro.go): AvroEncode/AvroDecode with
// single-object framing (0xC3 0x01 + CRC-64-AVRO writer fingerprint), a
// SchemaRegistry for validated routing, and writer->reader RESOLUTION for
// backward/forward-compatible schema evolution. This file WIRES that codec onto
// the real NATS wire path used by NATSBackend, as an explicit, opt-in alternative
// to the default JSON serialization.
//
// Design (decoupling):
//   - The on-wire bytes are carried verbatim through Event.Payload, which
//     NATSBackend already round-trips losslessly (base64 over the eventbus JSON
//     envelope). So an Avro-encoded NodeEvent travels as the SAME Event{} shape a
//     JSON payload would — only the Payload bytes differ, and they are
//     self-describing via their single-object header.
//   - Callers select Avro by constructing a NATSBackend with WithAvro(registry)
//     OR by using the per-publish PublishNodeEventAvro / decode helpers directly.
//     JSON remains the default so existing publishers/subscribers are unchanged
//     (back-compat).
//
// The Avro projection of a NodeEvent is the flat primitive record described by
// NodeEventSchemaV1/V2: run_id, node_id, timestamp(unix nanos as long), and (V2)
// action. Metadata (a map) is intentionally outside the Avro subset and is not
// carried on the Avro path; callers needing metadata use the JSON path.

import (
	"context"
	"fmt"
	"time"
)

// nodeEventToRecord projects a NodeEvent onto the name->value map the Avro codec
// encodes, in terms of the supplied schema's fields. Only fields the schema
// declares are populated; timestamp is carried as unix nanoseconds (Avro long).
func nodeEventToRecord(ne *NodeEvent, schema *AvroSchema) map[string]any {
	rec := make(map[string]any, len(schema.Fields))
	for _, f := range schema.Fields {
		switch f.Name {
		case "run_id":
			rec[f.Name] = ne.RunID
		case "node_id":
			rec[f.Name] = ne.NodeID
		case "action":
			rec[f.Name] = ne.Action
		case "timestamp":
			rec[f.Name] = ne.Timestamp.UTC().UnixNano()
		}
	}
	return rec
}

// recordToNodeEvent rebuilds a NodeEvent from a decoded Avro record (already
// resolved to the reader schema). Missing optional fields (e.g. a V1 reader has
// no "action") simply leave the zero value, matching the resolution contract.
func recordToNodeEvent(rec map[string]any) (*NodeEvent, error) {
	ne := &NodeEvent{}
	if v, ok := rec["run_id"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("events/avro-wire: run_id is %T, want string", v)
		}
		ne.RunID = s
	}
	if v, ok := rec["node_id"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("events/avro-wire: node_id is %T, want string", v)
		}
		ne.NodeID = s
	}
	if v, ok := rec["action"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("events/avro-wire: action is %T, want string", v)
		}
		ne.Action = s
	}
	if v, ok := rec["timestamp"]; ok {
		n, ok := v.(int64)
		if !ok {
			return nil, fmt.Errorf("events/avro-wire: timestamp is %T, want int64", v)
		}
		ne.Timestamp = time.Unix(0, n).UTC()
	}
	return ne, nil
}

// EncodeNodeEventAvro encodes a NodeEvent into Avro single-object bytes under the
// given writer schema. The returned bytes begin with the 0xC3 0x01 marker and the
// writer-schema fingerprint, so they are self-describing on the wire.
func EncodeNodeEventAvro(ne *NodeEvent, writer *AvroSchema) ([]byte, error) {
	if ne == nil {
		return nil, fmt.Errorf("events/avro-wire: cannot encode nil NodeEvent")
	}
	if writer == nil {
		return nil, fmt.Errorf("events/avro-wire: nil writer schema")
	}
	return AvroEncode(writer, nodeEventToRecord(ne, writer))
}

// DecodeNodeEventAvro decodes Avro single-object bytes into a NodeEvent. It reads
// the writer fingerprint from the header, resolves the writer schema via the
// registry (rejecting an unregistered fingerprint), and resolves the result to
// readerSchema — so a V1 reader can decode a V2 payload (skips action) and a V2
// reader can decode a V1 payload (action defaulted).
func DecodeNodeEventAvro(data []byte, registry *SchemaRegistry, readerSchema *AvroSchema) (*NodeEvent, error) {
	rec, err := AvroDecode(data, registry, readerSchema)
	if err != nil {
		return nil, err
	}
	return recordToNodeEvent(rec)
}

// WireFormat selects the serialization used on the NATS wire path.
type WireFormat int

const (
	// WireJSON is the default, back-compatible JSON serialization.
	WireJSON WireFormat = iota
	// WireAvro carries the payload as an Avro single-object-encoded record
	// (0xC3 0x01 + writer fingerprint + body), decoded via a SchemaRegistry.
	WireAvro
)

// NATSBackendOption configures a NATSBackend at construction time.
type NATSBackendOption func(*NATSBackend)

// WithAvro switches the backend's NodeEvent helpers to the Avro wire format,
// using writer as the publish-side schema and registry for subscribe-side
// writer-schema resolution. registry MUST have the writer schema (and any other
// acceptable writer schemas, e.g. both V1 and V2) registered. Passing this option
// does NOT change the generic Publish/Subscribe byte round-trip — it changes how
// PublishNodeEvent / SubscribeNodeEvents project a NodeEvent on/off the wire.
func WithAvro(writer *AvroSchema, registry *SchemaRegistry) NATSBackendOption {
	return func(n *NATSBackend) {
		n.wireFormat = WireAvro
		n.avroWriter = writer
		n.avroRegistry = registry
	}
}

// PublishNodeEvent publishes a NodeEvent over the backend, honoring the backend's
// configured wire format. In WireAvro mode the payload bytes are the Avro
// single-object encoding of ne (0xC3 0x01 marker + writer fingerprint + body); in
// the default WireJSON mode they are ne's JSON. The Event identity (ID=RunID,
// Type=subject) is identical across both formats so subscribers route the same.
func (n *NATSBackend) PublishNodeEvent(ne *NodeEvent) error {
	if ne == nil {
		return fmt.Errorf("events: cannot publish nil NodeEvent")
	}
	var payload []byte
	var err error
	switch n.wireFormat {
	case WireAvro:
		if n.avroWriter == nil {
			return fmt.Errorf("events: Avro wire mode requires a writer schema (WithAvro)")
		}
		payload, err = EncodeNodeEventAvro(ne, n.avroWriter)
	default:
		payload, err = ne.MarshalPayload()
	}
	if err != nil {
		return fmt.Errorf("events: encode NodeEvent payload: %w", err)
	}
	return n.Publish(Event{
		ID:        ne.RunID,
		Type:      ne.Subject(),
		Payload:   payload,
		Timestamp: ne.Timestamp,
	})
}

// SubscribeNodeEvents subscribes to NodeEvents on the subject derived from
// nodeID/action and decodes each delivered payload per the backend's wire format.
// In WireAvro mode the payload is decoded via the registry (writer resolution) to
// the configured reader schema; in WireJSON mode it is JSON-unmarshalled.
func (n *NATSBackend) SubscribeNodeEvents(
	ctx context.Context, nodeID, action string,
) (<-chan *NodeEvent, func(), error) {
	subject := HelixSubject("nodes", nodeID, action)
	in, stop, err := n.Subscribe(ctx, subject)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan *NodeEvent)
	go func() {
		defer close(out)
		for ev := range in {
			ne, decErr := n.decodeNodeEvent(ev.Payload)
			if decErr != nil {
				// Drop undecodable messages rather than crash the consumer.
				continue
			}
			select {
			case out <- ne:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, stop, nil
}

// decodeNodeEvent decodes a payload per the backend's configured wire format.
func (n *NATSBackend) decodeNodeEvent(payload []byte) (*NodeEvent, error) {
	switch n.wireFormat {
	case WireAvro:
		if n.avroRegistry == nil || n.avroWriter == nil {
			return nil, fmt.Errorf("events: Avro wire mode requires registry+reader schema (WithAvro)")
		}
		return DecodeNodeEventAvro(payload, n.avroRegistry, n.avroWriter)
	default:
		return UnmarshalNodeEvent(payload)
	}
}
