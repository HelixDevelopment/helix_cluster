package events

// Avro-subset serialization for Helix domain events (HXC-1119).
//
// This is a deliberately small, standard-library-only implementation of the
// slice of Apache Avro that the Helix event bus needs: records of primitive
// fields, binary encoding, single-object framing (so each payload is
// self-describing via its writer-schema fingerprint), a schema registry for
// validated routing, and writer->reader schema RESOLUTION so schemas can
// evolve backward- AND forward-compatibly by adding defaulted fields.
//
// It is NOT a full Avro implementation: unions, enums, arrays, maps, fixed,
// and nested records are intentionally out of scope (the Helix event payloads
// are flat primitive records). What IS implemented follows the Avro spec
// faithfully so the wire bytes are interoperable with real Avro readers for
// the supported subset:
//   - int/long  : zig-zag varint
//   - string    : long(len) prefix + UTF-8 bytes
//   - bytes      : long(len) prefix + raw bytes
//   - boolean    : single byte 0x00 / 0x01
//   - double     : 8-byte little-endian IEEE-754
//   - record     : concatenation of fields in writer-schema order
//   - single-object encoding header: 0xC3 0x01 + 8-byte little-endian
//     CRC-64-AVRO (Rabin) fingerprint of the writer schema's canonical form.
//
// Schema resolution (decode side) implements the Avro rules for the supported
// subset:
//   - a field present in BOTH writer and reader is decoded and kept;
//   - a field present ONLY in the writer is decoded then discarded (skipped);
//   - a field present ONLY in the reader is filled from its default (and it is
//     an error for such a field to have no default — that schema pair is not
//     resolvable, which is the forward-compat contract).

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
)

// AvroType enumerates the primitive Avro types supported by this subset.
type AvroType string

const (
	AvroString  AvroType = "string"
	AvroLong    AvroType = "long"
	AvroInt     AvroType = "int"
	AvroBoolean AvroType = "boolean"
	AvroBytes   AvroType = "bytes"
	AvroDouble  AvroType = "double"
)

// avroSingleObjectMagic is the two-byte marker that prefixes an Avro
// single-object-encoded payload (Avro spec, "Single object encoding").
var avroSingleObjectMagic = [2]byte{0xC3, 0x01}

// AvroField is one field of a record schema.
type AvroField struct {
	Name string
	Type AvroType
	// HasDefault marks that Default is meaningful. A reader-only field MUST
	// have a default for the writer/reader schema pair to be resolvable.
	HasDefault bool
	Default    any
}

// AvroSchema is a flat record schema: an ordered list of primitive fields.
type AvroSchema struct {
	Name   string
	Fields []AvroField
}

// Fingerprint returns the CRC-64-AVRO (Rabin) fingerprint of the schema's
// canonical form. Two schemas with the same canonical form (same record name,
// same ordered field names+types) share a fingerprint; defaults do NOT affect
// the fingerprint, matching Avro's Parsing Canonical Form which strips them.
func (s *AvroSchema) Fingerprint() uint64 {
	return crc64Avro([]byte(s.canonicalForm()))
}

// canonicalForm builds a deterministic Avro-style Parsing Canonical Form for
// the supported subset: defaults, ordering noise, and whitespace are removed;
// fields appear in schema order.
func (s *AvroSchema) canonicalForm() string {
	var b strings.Builder
	b.WriteString(`{"name":"`)
	b.WriteString(s.Name)
	b.WriteString(`","type":"record","fields":[`)
	for i, f := range s.Fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":"`)
		b.WriteString(f.Name)
		b.WriteString(`","type":"`)
		b.WriteString(string(f.Type))
		b.WriteString(`"}`)
	}
	b.WriteString("]}")
	return b.String()
}

// SchemaRegistry maps writer-schema fingerprints to their schemas so a decoder
// can look up the schema a payload was written with (validated routing). It is
// the "schema-validated routing" surface: a payload whose fingerprint is not
// registered is rejected rather than mis-decoded.
type SchemaRegistry struct {
	byFingerprint map[uint64]*AvroSchema
}

// NewSchemaRegistry returns an empty registry.
func NewSchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{byFingerprint: make(map[uint64]*AvroSchema)}
}

// Register adds a schema to the registry keyed by its fingerprint. Registering
// the same schema twice is idempotent.
func (r *SchemaRegistry) Register(s *AvroSchema) {
	r.byFingerprint[s.Fingerprint()] = s
}

// Lookup returns the registered schema for a fingerprint, or (nil,false).
func (r *SchemaRegistry) Lookup(fp uint64) (*AvroSchema, bool) {
	s, ok := r.byFingerprint[fp]
	return s, ok
}

// AvroEncode encodes record (a name->value map) under the writer schema into a
// single-object-encoded byte slice (magic + fingerprint + body). Every field
// declared by the schema MUST be present in record with a type-compatible Go
// value, otherwise an error is returned (encode-side validation).
func AvroEncode(schema *AvroSchema, record map[string]any) ([]byte, error) {
	body, err := encodeBody(schema, record)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 10+len(body))
	out = append(out, avroSingleObjectMagic[0], avroSingleObjectMagic[1])
	var fp [8]byte
	binary.LittleEndian.PutUint64(fp[:], schema.Fingerprint())
	out = append(out, fp[:]...)
	out = append(out, body...)
	return out, nil
}

func encodeBody(schema *AvroSchema, record map[string]any) ([]byte, error) {
	var body []byte
	for _, f := range schema.Fields {
		v, ok := record[f.Name]
		if !ok {
			return nil, fmt.Errorf("events/avro: encode %q: missing required field %q", schema.Name, f.Name)
		}
		enc, err := encodeValue(f.Type, v)
		if err != nil {
			return nil, fmt.Errorf("events/avro: encode %q field %q: %w", schema.Name, f.Name, err)
		}
		body = append(body, enc...)
	}
	return body, nil
}

// AvroDecode decodes a single-object-encoded payload. It reads the writer
// fingerprint from the header, resolves the writer schema via the registry
// (returning an error for an unknown/unregistered fingerprint), decodes the
// body under the writer schema, and then RESOLVES the result to readerSchema:
// reader-only fields are filled from their defaults, writer-only fields are
// discarded. The returned map contains exactly readerSchema's fields.
func AvroDecode(data []byte, registry *SchemaRegistry, readerSchema *AvroSchema) (map[string]any, error) {
	if len(data) < 10 {
		return nil, fmt.Errorf("events/avro: payload too short (%d bytes) for single-object header", len(data))
	}
	if data[0] != avroSingleObjectMagic[0] || data[1] != avroSingleObjectMagic[1] {
		return nil, fmt.Errorf("events/avro: bad single-object marker 0x%02X%02X", data[0], data[1])
	}
	fp := binary.LittleEndian.Uint64(data[2:10])
	writer, ok := registry.Lookup(fp)
	if !ok {
		return nil, fmt.Errorf("events/avro: unknown writer-schema fingerprint %016x (not registered)", fp)
	}

	// Decode every writer field in order; keep the values by name.
	pos := 10
	written := make(map[string]any, len(writer.Fields))
	for _, f := range writer.Fields {
		v, n, err := decodeValue(f.Type, data[pos:])
		if err != nil {
			return nil, fmt.Errorf("events/avro: decode writer field %q: %w", f.Name, err)
		}
		written[f.Name] = v
		pos += n
	}
	if pos != len(data) {
		return nil, fmt.Errorf("events/avro: %d trailing byte(s) after record body", len(data)-pos)
	}

	// Resolve to the reader schema.
	out := make(map[string]any, len(readerSchema.Fields))
	for _, rf := range readerSchema.Fields {
		if v, present := written[rf.Name]; present {
			out[rf.Name] = v
			continue
		}
		if !rf.HasDefault {
			return nil, fmt.Errorf("events/avro: reader field %q absent from writer schema and has no default (schemas not resolvable)", rf.Name)
		}
		out[rf.Name] = rf.Default
	}
	return out, nil
}

// --- primitive value codec ------------------------------------------------

func encodeValue(t AvroType, v any) ([]byte, error) {
	switch t {
	case AvroString:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("want string, got %T", v)
		}
		return encodeBytesLike([]byte(s)), nil
	case AvroBytes:
		b, ok := v.([]byte)
		if !ok {
			return nil, fmt.Errorf("want []byte, got %T", v)
		}
		return encodeBytesLike(b), nil
	case AvroLong:
		n, ok := toInt64(v)
		if !ok {
			return nil, fmt.Errorf("want integer, got %T", v)
		}
		return encodeVarint(n), nil
	case AvroInt:
		n, ok := toInt64(v)
		if !ok {
			return nil, fmt.Errorf("want integer, got %T", v)
		}
		if n > math.MaxInt32 || n < math.MinInt32 {
			return nil, fmt.Errorf("int out of 32-bit range: %d", n)
		}
		return encodeVarint(n), nil
	case AvroBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("want bool, got %T", v)
		}
		if b {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case AvroDouble:
		f, ok := toFloat64(v)
		if !ok {
			return nil, fmt.Errorf("want float64, got %T", v)
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(f))
		return buf[:], nil
	default:
		return nil, fmt.Errorf("unsupported type %q", t)
	}
}

func decodeValue(t AvroType, data []byte) (any, int, error) {
	switch t {
	case AvroString:
		b, n, err := decodeBytesLike(data)
		if err != nil {
			return nil, 0, err
		}
		return string(b), n, nil
	case AvroBytes:
		b, n, err := decodeBytesLike(data)
		if err != nil {
			return nil, 0, err
		}
		return b, n, nil
	case AvroLong, AvroInt:
		n, adv, err := decodeVarint(data)
		if err != nil {
			return nil, 0, err
		}
		return n, adv, nil
	case AvroBoolean:
		if len(data) < 1 {
			return nil, 0, fmt.Errorf("truncated boolean")
		}
		switch data[0] {
		case 0:
			return false, 1, nil
		case 1:
			return true, 1, nil
		default:
			return nil, 0, fmt.Errorf("invalid boolean byte 0x%02X", data[0])
		}
	case AvroDouble:
		if len(data) < 8 {
			return nil, 0, fmt.Errorf("truncated double")
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(data[:8])), 8, nil
	default:
		return nil, 0, fmt.Errorf("unsupported type %q", t)
	}
}

func encodeBytesLike(b []byte) []byte {
	out := encodeVarint(int64(len(b)))
	return append(out, b...)
}

func decodeBytesLike(data []byte) ([]byte, int, error) {
	n, adv, err := decodeVarint(data)
	if err != nil {
		return nil, 0, err
	}
	if n < 0 {
		return nil, 0, fmt.Errorf("negative length %d", n)
	}
	end := adv + int(n)
	if end > len(data) || int(n) > len(data) {
		return nil, 0, fmt.Errorf("length %d exceeds %d remaining bytes", n, len(data)-adv)
	}
	out := make([]byte, n)
	copy(out, data[adv:end])
	return out, end, nil
}

// encodeVarint writes a zig-zag varint (Avro long/int wire form).
func encodeVarint(n int64) []byte {
	u := uint64(n<<1) ^ uint64(n>>63) // zig-zag
	var buf [10]byte
	i := 0
	for u >= 0x80 {
		buf[i] = byte(u) | 0x80
		u >>= 7
		i++
	}
	buf[i] = byte(u)
	i++
	return buf[:i]
}

// decodeVarint reads a zig-zag varint and returns the value + bytes consumed.
func decodeVarint(data []byte) (int64, int, error) {
	var u uint64
	var shift uint
	for i := 0; i < len(data); i++ {
		b := data[i]
		if shift >= 64 {
			return 0, 0, fmt.Errorf("varint overflow")
		}
		u |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			// zig-zag decode
			return int64(u>>1) ^ -int64(u&1), i + 1, nil
		}
		shift += 7
	}
	return 0, 0, fmt.Errorf("truncated varint")
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint32:
		return int64(x), true
	default:
		return 0, false
	}
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	default:
		return 0, false
	}
}

// --- CRC-64-AVRO (Rabin) fingerprint --------------------------------------

var crc64AvroTable [256]uint64

func init() {
	const empty = uint64(0xc15d213aa4d7a795)
	for i := 0; i < 256; i++ {
		fp := uint64(i)
		for j := 0; j < 8; j++ {
			fp = (fp >> 1) ^ (empty & (-(fp & 1)))
		}
		crc64AvroTable[i] = fp
	}
}

// crc64Avro computes the CRC-64-AVRO Rabin fingerprint of buf (Avro spec
// "Schema Fingerprints").
func crc64Avro(buf []byte) uint64 {
	fp := uint64(0xc15d213aa4d7a795)
	for _, b := range buf {
		fp = (fp >> 8) ^ crc64AvroTable[byte(fp)^b]
	}
	return fp
}

// --- Helix event schemas ---------------------------------------------------

// NodeEventSchemaV1 is the original Avro schema for NodeEvent: the fields that
// shipped before schema evolution. Timestamp is carried as a long (unix nanos)
// and Metadata is intentionally excluded from the v1 Avro projection (maps are
// out of this subset); the Avro path serializes the scalar identity fields.
func NodeEventSchemaV1() *AvroSchema {
	return &AvroSchema{
		Name: "NodeEvent",
		Fields: []AvroField{
			{Name: "run_id", Type: AvroString},
			{Name: "node_id", Type: AvroString},
			{Name: "timestamp", Type: AvroLong},
		},
	}
}

// NodeEventSchemaV2 evolves V1 by adding the "action" field WITH a default, so
// a V1 reader can read a V2 payload (skips action) and a V2 reader can read a
// V1 payload (synthesizes action from the default). This is the backward- AND
// forward-compatible evolution Avro is chosen for.
func NodeEventSchemaV2() *AvroSchema {
	return &AvroSchema{
		Name: "NodeEvent",
		Fields: []AvroField{
			{Name: "run_id", Type: AvroString},
			{Name: "node_id", Type: AvroString},
			{Name: "timestamp", Type: AvroLong},
			{Name: "action", Type: AvroString, HasDefault: true, Default: ""},
		},
	}
}

// sortedFieldNames is a tiny helper used by tests/diagnostics to compare field
// sets independent of order.
func sortedFieldNames(s *AvroSchema) []string {
	names := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		names[i] = f.Name
	}
	sort.Strings(names)
	return names
}
