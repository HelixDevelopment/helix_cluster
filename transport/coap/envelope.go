package coap

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Resource paths exposed by the CoAP server. Centralised so the codec/path
// logic is unit-testable and a typo'd path is a single-source-of-truth change.
const (
	// PathDispatch accepts a WorkEnvelope via POST.
	PathDispatch = "/dispatch"
	// PathStatus serves a StatusReport via GET and supports Observe.
	PathStatus = "/status"
)

// MaxEnvelopeBytes caps the encoded size of an inbound CBOR body before it is
// decoded. A work envelope for a constrained link is small; rejecting anything
// larger up front bounds memory and CPU against a hostile/oversized payload
// (DoS via unbounded deserialization), including blockwise-assembled bodies.
const MaxEnvelopeBytes = 8 * 1024

// hardenedDecMode is a CBOR decode mode with tight structural limits so a
// crafted payload cannot exhaust memory via deeply nested / huge container
// headers even within the byte cap. Byte/text string lengths are additionally
// bounded by MaxEnvelopeBytes (the whole body cannot exceed it).
var hardenedDecMode cbor.DecMode

func init() {
	dm, err := cbor.DecOptions{
		MaxNestedLevels:  8,   // envelope is flat: map -> optional Labels submap
		MaxArrayElements: 64,  // no large arrays are expected
		MaxMapPairs:      128, // top-level keys + Labels entries
	}.DecMode()
	if err != nil {
		panic("coap: build hardened cbor dec mode: " + err.Error())
	}
	hardenedDecMode = dm
}

// WorkEnvelope is the unit of work dispatched to a constrained edge device.
// It is encoded as CBOR on the wire (CoAP content-format application/cbor).
//
// Field tags are short integer CBOR keys (via `cbor:"N,keyasint"`) to keep the
// encoding compact for low-bandwidth links — integer keys cost 1 byte each
// versus a string key per field.
type WorkEnvelope struct {
	JobID    string            `cbor:"1,keyasint"` // unique job identifier
	Kind     string            `cbor:"2,keyasint"` // task kind, e.g. "infer", "sense"
	Priority uint8             `cbor:"3,keyasint"` // 0 = lowest
	Payload  []byte            `cbor:"4,keyasint"` // opaque task payload
	Deadline int64             `cbor:"5,keyasint"` // unix millis; 0 = none
	Labels   map[string]string `cbor:"6,keyasint"` // optional routing labels
}

// StatusReport is the device/job status returned by GET /status and pushed by
// Observe notifications.
type StatusReport struct {
	DeviceID string `cbor:"1,keyasint"`
	JobID    string `cbor:"2,keyasint"` // last/active job, "" if idle
	State    string `cbor:"3,keyasint"` // "idle" | "running" | "done" | "error"
	Battery  uint8  `cbor:"4,keyasint"` // 0..100 percent
	Seq      uint32 `cbor:"5,keyasint"` // monotonically increasing status sequence
}

// EncodeEnvelope serialises a WorkEnvelope to compact CBOR.
func EncodeEnvelope(e WorkEnvelope) ([]byte, error) {
	b, err := cbor.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("coap: encode work envelope: %w", err)
	}
	return b, nil
}

// DecodeEnvelope parses a CBOR-encoded WorkEnvelope. It rejects bodies larger
// than MaxEnvelopeBytes and decodes with tight structural limits, so a hostile
// payload cannot exhaust memory (DoS via unbounded deserialization).
func DecodeEnvelope(b []byte) (WorkEnvelope, error) {
	if len(b) > MaxEnvelopeBytes {
		return WorkEnvelope{}, fmt.Errorf("coap: work envelope too large: %d > %d bytes", len(b), MaxEnvelopeBytes)
	}
	var e WorkEnvelope
	if err := hardenedDecMode.Unmarshal(b, &e); err != nil {
		return WorkEnvelope{}, fmt.Errorf("coap: decode work envelope: %w", err)
	}
	return e, nil
}

// EncodeStatus serialises a StatusReport to compact CBOR.
func EncodeStatus(s StatusReport) ([]byte, error) {
	b, err := cbor.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("coap: encode status: %w", err)
	}
	return b, nil
}

// DecodeStatus parses a CBOR-encoded StatusReport with the same body cap and
// tight structural limits as DecodeEnvelope.
func DecodeStatus(b []byte) (StatusReport, error) {
	if len(b) > MaxEnvelopeBytes {
		return StatusReport{}, fmt.Errorf("coap: status too large: %d > %d bytes", len(b), MaxEnvelopeBytes)
	}
	var s StatusReport
	if err := hardenedDecMode.Unmarshal(b, &s); err != nil {
		return StatusReport{}, fmt.Errorf("coap: decode status: %w", err)
	}
	return s, nil
}
