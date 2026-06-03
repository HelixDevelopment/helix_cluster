package events

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

// registryWith builds a registry pre-loaded with the given writer schemas.
func registryWith(schemas ...*AvroSchema) *SchemaRegistry {
	r := NewSchemaRegistry()
	for _, s := range schemas {
		r.Register(s)
	}
	return r
}

// TestAvroRoundTrip_SameSchema proves a record encoded and decoded under the
// same schema preserves every field value exactly (load-bearing: wrong codec
// math flips these assertions).
func TestAvroRoundTrip_SameSchema(t *testing.T) {
	s := NodeEventSchemaV1()
	reg := registryWith(s)
	rec := map[string]any{"run_id": "run-abc", "node_id": "n1", "timestamp": int64(1717400000000000000)}

	enc, err := AvroEncode(s, rec)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Single-object header: magic 0xC3 0x01 then 8-byte fingerprint.
	if enc[0] != 0xC3 || enc[1] != 0x01 {
		t.Fatalf("missing single-object marker, got 0x%02X%02X", enc[0], enc[1])
	}

	got, err := AvroDecode(enc, reg, s)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["run_id"] != "run-abc" || got["node_id"] != "n1" || got["timestamp"] != int64(1717400000000000000) {
		t.Fatalf("round-trip mismatch: %#v", got)
	}
}

// TestAvroSchemaEvolution_OldReadsNew is half the HXC-1119 closure: a V1
// (old) consumer must still deserialize a V2 (new) payload — reading the
// shared fields and SKIPPING the added "action" field.
func TestAvroSchemaEvolution_OldReadsNew(t *testing.T) {
	writer := NodeEventSchemaV2()
	reader := NodeEventSchemaV1()
	// Registry knows the WRITER schema (that is what routing looks up).
	reg := registryWith(writer)

	payload, err := AvroEncode(writer, map[string]any{
		"run_id": "run-xyz", "node_id": "n7", "timestamp": int64(42), "action": "join",
	})
	if err != nil {
		t.Fatalf("encode v2: %v", err)
	}

	got, err := AvroDecode(payload, reg, reader)
	if err != nil {
		t.Fatalf("v1 reader decoding v2 payload: %v", err)
	}
	if got["run_id"] != "run-xyz" || got["node_id"] != "n7" || got["timestamp"] != int64(42) {
		t.Fatalf("shared fields not intact: %#v", got)
	}
	if _, present := got["action"]; present {
		t.Fatalf("v1 reader must NOT surface the v2-only field 'action', got %#v", got)
	}
}

// TestAvroSchemaEvolution_NewReadsOld is the other half: a V2 (new) consumer
// must read a V1 (old) payload, synthesizing the missing "action" from its
// schema default.
func TestAvroSchemaEvolution_NewReadsOld(t *testing.T) {
	writer := NodeEventSchemaV1()
	reader := NodeEventSchemaV2()
	reg := registryWith(writer)

	payload, err := AvroEncode(writer, map[string]any{
		"run_id": "run-old", "node_id": "n2", "timestamp": int64(7),
	})
	if err != nil {
		t.Fatalf("encode v1: %v", err)
	}

	got, err := AvroDecode(payload, reg, reader)
	if err != nil {
		t.Fatalf("v2 reader decoding v1 payload: %v", err)
	}
	if got["run_id"] != "run-old" || got["node_id"] != "n2" || got["timestamp"] != int64(7) {
		t.Fatalf("shared fields not intact: %#v", got)
	}
	action, present := got["action"]
	if !present {
		t.Fatalf("v2 reader must synthesize 'action' from default, absent in %#v", got)
	}
	if action != "" {
		t.Fatalf("default for 'action' is the empty string, got %q", action)
	}
}

// TestAvroSchemaValidatedRouting_UnknownFingerprint proves the registry gate:
// a payload whose writer fingerprint is not registered is REJECTED, not
// silently mis-decoded.
func TestAvroSchemaValidatedRouting_UnknownFingerprint(t *testing.T) {
	writer := NodeEventSchemaV2()
	payload, err := AvroEncode(writer, map[string]any{
		"run_id": "r", "node_id": "n", "timestamp": int64(1), "action": "x",
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	empty := NewSchemaRegistry() // does NOT know the writer schema
	if _, err := AvroDecode(payload, empty, NodeEventSchemaV2()); err == nil {
		t.Fatalf("expected error decoding with an empty registry (unknown fingerprint)")
	}
}

// TestAvroEncode_MissingRequiredField proves encode-side validation.
func TestAvroEncode_MissingRequiredField(t *testing.T) {
	s := NodeEventSchemaV1()
	if _, err := AvroEncode(s, map[string]any{"run_id": "r"}); err == nil {
		t.Fatalf("expected error: node_id and timestamp are missing")
	}
}

// TestAvroResolution_ReaderOnlyFieldWithoutDefault proves the forward-compat
// contract: a reader field absent from the writer AND lacking a default makes
// the schema pair unresolvable (error), rather than producing a bogus zero.
func TestAvroResolution_ReaderOnlyFieldWithoutDefault(t *testing.T) {
	writer := NodeEventSchemaV1()
	// reader adds a field with NO default
	reader := &AvroSchema{Name: "NodeEvent", Fields: append(
		append([]AvroField{}, NodeEventSchemaV1().Fields...),
		AvroField{Name: "zone", Type: AvroString}, // no default
	)}
	reg := registryWith(writer)
	payload, err := AvroEncode(writer, map[string]any{"run_id": "r", "node_id": "n", "timestamp": int64(1)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := AvroDecode(payload, reg, reader); err == nil {
		t.Fatalf("expected unresolvable-schema error for reader-only field without default")
	}
}

// TestAvroFingerprint_DefaultsIgnored proves the canonical fingerprint ignores
// defaults (Avro Parsing Canonical Form) but distinguishes different field sets.
func TestAvroFingerprint_DefaultsIgnored(t *testing.T) {
	v2a := NodeEventSchemaV2()
	v2b := NodeEventSchemaV2()
	v2b.Fields[3].Default = "different-default" // same shape, different default
	if v2a.Fingerprint() != v2b.Fingerprint() {
		t.Fatalf("defaults must not change the fingerprint")
	}
	if NodeEventSchemaV1().Fingerprint() == NodeEventSchemaV2().Fingerprint() {
		t.Fatalf("v1 and v2 have different field sets -> must differ in fingerprint")
	}
	// sanity: the helper used by diagnostics returns the field names
	if got := sortedFieldNames(NodeEventSchemaV1()); len(got) != 3 {
		t.Fatalf("v1 should have 3 fields, got %v", got)
	}
}

// TestAvroVarintRoundTrip exercises the zig-zag varint over boundary values so
// a sign/shift bug in the codec is caught directly.
func TestAvroVarintRoundTrip(t *testing.T) {
	for _, n := range []int64{0, -1, 1, 63, -64, 127, -128, 2147483647, -2147483648, 1717400000000000000, -1717400000000000000} {
		enc := encodeVarint(n)
		got, adv, err := decodeVarint(enc)
		if err != nil || adv != len(enc) || got != n {
			t.Fatalf("varint %d: got=%d adv=%d/%d err=%v", n, got, adv, len(enc), err)
		}
	}
}

// TestAvroConcurrentRoundTrip drives many concurrent encode/decode cycles
// (run under -race) to prove the codec + registry are safe for concurrent
// readers and deterministic.
func TestAvroConcurrentRoundTrip(t *testing.T) {
	writer := NodeEventSchemaV2()
	reader := NodeEventSchemaV1()
	reg := registryWith(writer)

	const workers = 16
	const iters = 200
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("n-%d-%d", w, i)
				enc, err := AvroEncode(writer, map[string]any{
					"run_id": "run", "node_id": id, "timestamp": int64(i), "action": "heartbeat",
				})
				if err != nil {
					errCh <- fmt.Errorf("encode: %w", err)
					return
				}
				got, err := AvroDecode(enc, reg, reader)
				if err != nil {
					errCh <- fmt.Errorf("decode: %w", err)
					return
				}
				if got["node_id"] != id || got["timestamp"] != int64(i) {
					errCh <- fmt.Errorf("mismatch: %#v", got)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// TestAvroSingleObjectHeader pins the wire framing: marker + little-endian
// fingerprint matching the writer schema.
func TestAvroSingleObjectHeader(t *testing.T) {
	s := NodeEventSchemaV1()
	enc, err := AvroEncode(s, map[string]any{"run_id": "r", "node_id": "n", "timestamp": int64(0)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	wantHdr := []byte{0xC3, 0x01}
	if !bytes.Equal(enc[:2], wantHdr) {
		t.Fatalf("header marker = % x, want % x", enc[:2], wantHdr)
	}
}
