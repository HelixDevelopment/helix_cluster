package tracing

// W3C Trace Context interoperability for the tracing package.
//
// This file implements parsing, validation, formatting and propagation of the
// W3C `traceparent` and `tracestate` HTTP headers, plus a sampling decision
// derived from the trace-flags. It is additive to the existing UUID-based
// trace-ID API and depends only on the Go standard library.
//
// References: https://www.w3.org/TR/trace-context/

import (
	"errors"
	"io"
	"strings"
)

const (
	// traceFlagSampled is the low-order bit of the trace-flags field.
	traceFlagSampled byte = 0x01

	// traceIDHexLen is the required length of the trace-id field in hex chars.
	traceIDHexLen = 32
	// parentIDHexLen is the required length of the parent-id field in hex chars.
	parentIDHexLen = 16
	// flagsHexLen is the required length of the trace-flags field in hex chars.
	flagsHexLen = 2

	// version0Len is the exact length of a version-0 traceparent header.
	// 2 + 1 + 32 + 1 + 16 + 1 + 2 = 55
	version0Len = 55

	// maxTraceStateMembers is the maximum number of list members allowed in a
	// tracestate header per the W3C spec.
	maxTraceStateMembers = 32
)

var (
	// ErrInvalidTraceParent indicates a malformed or out-of-spec traceparent.
	ErrInvalidTraceParent = errors.New("tracing: invalid traceparent header")
	// ErrInvalidTraceState indicates a malformed or out-of-spec tracestate.
	ErrInvalidTraceState = errors.New("tracing: invalid tracestate header")
	// ErrTraceParentMissing indicates no traceparent header was present.
	ErrTraceParentMissing = errors.New("tracing: traceparent header missing")
)

// TraceParent is a parsed W3C `traceparent` header value.
//
// Wire format: version(2 hex)-trace_id(32 hex)-parent_id(16 hex)-flags(2 hex).
type TraceParent struct {
	Version  byte
	TraceID  string // 32 lowercase hex chars, never all-zero
	ParentID string // 16 lowercase hex chars, never all-zero
	Flags    byte
}

// SampledFromFlags reports whether the sampled bit is set in trace-flags.
func SampledFromFlags(flags byte) bool {
	return flags&traceFlagSampled != 0
}

// Sampled reports whether this traceparent requests sampling.
func (tp TraceParent) Sampled() bool {
	return SampledFromFlags(tp.Flags)
}

// SetSampled sets or clears the sampled bit of the trace-flags.
func (tp *TraceParent) SetSampled(sampled bool) {
	if sampled {
		tp.Flags |= traceFlagSampled
	} else {
		tp.Flags &^= traceFlagSampled
	}
}

// String renders the TraceParent in its canonical wire form. The version byte
// is always rendered using two lowercase hex digits.
func (tp TraceParent) String() string {
	var sb strings.Builder
	sb.Grow(version0Len)
	sb.WriteString(hexByte(tp.Version))
	sb.WriteByte('-')
	sb.WriteString(tp.TraceID)
	sb.WriteByte('-')
	sb.WriteString(tp.ParentID)
	sb.WriteByte('-')
	sb.WriteString(hexByte(tp.Flags))
	return sb.String()
}

// ParseTraceParent parses and strictly validates a W3C traceparent header.
//
// Validation rules (per spec):
//   - version is 2 lowercase hex chars and MUST NOT be 0xff.
//   - trace_id is 32 lowercase hex chars and MUST NOT be all zero.
//   - parent_id is 16 lowercase hex chars and MUST NOT be all zero.
//   - flags is 2 lowercase hex chars.
//   - for version 0 the header MUST be exactly 55 chars (no trailing data);
//     for higher (future, non-0xff) versions trailing fields are ignored.
func ParseTraceParent(s string) (TraceParent, error) {
	// The version, trace_id, parent_id and flags occupy a fixed 55-char prefix.
	if len(s) < version0Len {
		return TraceParent{}, ErrInvalidTraceParent
	}

	// Field separators must sit at fixed positions.
	if s[2] != '-' || s[35] != '-' || s[52] != '-' {
		return TraceParent{}, ErrInvalidTraceParent
	}

	versionHex := s[0:2]
	traceID := s[3:35]
	parentID := s[36:52]
	flagsHex := s[52+1 : 55]

	version, ok := parseHexByte(versionHex)
	if !ok {
		return TraceParent{}, ErrInvalidTraceParent
	}
	// version 0xff is explicitly forbidden.
	if version == 0xff {
		return TraceParent{}, ErrInvalidTraceParent
	}

	// For version 0, the header must be exactly 55 chars; for future versions
	// trailing data (preceded by '-') is permitted and ignored, but a 56th char
	// that is not a separator is invalid.
	if len(s) > version0Len {
		if version == 0x00 {
			return TraceParent{}, ErrInvalidTraceParent
		}
		if s[version0Len] != '-' {
			return TraceParent{}, ErrInvalidTraceParent
		}
	}

	if len(traceID) != traceIDHexLen || !isLowerHex(traceID) || isAllZeroHex(traceID) {
		return TraceParent{}, ErrInvalidTraceParent
	}
	if len(parentID) != parentIDHexLen || !isLowerHex(parentID) || isAllZeroHex(parentID) {
		return TraceParent{}, ErrInvalidTraceParent
	}

	flags, ok := parseHexByte(flagsHex)
	if !ok {
		return TraceParent{}, ErrInvalidTraceParent
	}

	return TraceParent{
		Version:  version,
		TraceID:  traceID,
		ParentID: parentID,
		Flags:    flags,
	}, nil
}

// NewTraceParent generates a fresh version-0 TraceParent with random,
// spec-valid (never all-zero) trace_id and parent_id. The sampled flag is set
// according to the argument.
func NewTraceParent(sampled bool) TraceParent {
	tp := TraceParent{
		Version:  0x00,
		TraceID:  randomHex(traceIDHexLen / 2),
		ParentID: randomHex(parentIDHexLen / 2),
	}
	tp.SetSampled(sampled)
	return tp
}

// TraceStateMember is a single key=value list member of a tracestate header.
type TraceStateMember struct {
	Key   string
	Value string
}

// TraceState is an ordered, parsed W3C `tracestate` header.
//
// Order is significant: the left-most member is the most-recently-mutating
// vendor. Upsert implements the spec's mutate-on-write semantics by moving the
// touched vendor to the front.
type TraceState struct {
	members []TraceStateMember
}

// ParseTraceState parses and validates a W3C tracestate header. An empty string
// yields an empty TraceState with no error.
func ParseTraceState(s string) (*TraceState, error) {
	ts := &TraceState{}
	s = strings.TrimSpace(s)
	if s == "" {
		return ts, nil
	}

	seen := make(map[string]struct{})
	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.Trim(part, " \t")
		if part == "" {
			// Empty list members are not permitted.
			return nil, ErrInvalidTraceState
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			return nil, ErrInvalidTraceState
		}
		key := part[:eq]
		value := part[eq+1:]
		if !validTraceStateKey(key) || !validTraceStateValue(value) {
			return nil, ErrInvalidTraceState
		}
		if _, dup := seen[key]; dup {
			return nil, ErrInvalidTraceState
		}
		seen[key] = struct{}{}
		ts.members = append(ts.members, TraceStateMember{Key: key, Value: value})
		if len(ts.members) > maxTraceStateMembers {
			return nil, ErrInvalidTraceState
		}
	}
	return ts, nil
}

// Len returns the number of members.
func (ts *TraceState) Len() int {
	if ts == nil {
		return 0
	}
	return len(ts.members)
}

// Get returns the value for key, or empty string if absent.
func (ts *TraceState) Get(key string) string {
	if ts == nil {
		return ""
	}
	for _, m := range ts.members {
		if m.Key == key {
			return m.Value
		}
	}
	return ""
}

// Keys returns the member keys in order (most-recent vendor first).
func (ts *TraceState) Keys() []string {
	if ts == nil {
		return nil
	}
	out := make([]string, len(ts.members))
	for i, m := range ts.members {
		out[i] = m.Key
	}
	return out
}

// Upsert applies mutate-on-write semantics: it sets key=value and moves that
// member to the front of the list (most-recent vendor first). If adding a new
// member would exceed the 32-member limit, the oldest (last) member is evicted.
// Invalid keys/values are ignored (no-op) to keep the state spec-valid.
func (ts *TraceState) Upsert(key, value string) {
	if ts == nil {
		return
	}
	if !validTraceStateKey(key) || !validTraceStateValue(value) {
		return
	}

	// Remove any existing member with this key.
	filtered := ts.members[:0:0]
	for _, m := range ts.members {
		if m.Key != key {
			filtered = append(filtered, m)
		}
	}
	ts.members = filtered

	// Prepend the (updated) member.
	ts.members = append([]TraceStateMember{{Key: key, Value: value}}, ts.members...)

	// Enforce the maximum member count by evicting oldest entries.
	if len(ts.members) > maxTraceStateMembers {
		ts.members = ts.members[:maxTraceStateMembers]
	}
}

// String renders the TraceState in canonical comma-separated form.
func (ts *TraceState) String() string {
	if ts == nil || len(ts.members) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, m := range ts.members {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(m.Key)
		sb.WriteByte('=')
		sb.WriteString(m.Value)
	}
	return sb.String()
}

// --- propagation helpers ---

// Inject writes the traceparent and (if non-empty) tracestate into a
// map[string]string carrier using lowercase header names.
func Inject(carrier map[string]string, tp TraceParent, ts *TraceState) {
	if carrier == nil {
		return
	}
	carrier["traceparent"] = tp.String()
	if ts != nil && ts.Len() > 0 {
		carrier["tracestate"] = ts.String()
	}
}

// Extract reads the traceparent and tracestate from a map[string]string
// carrier. A missing or invalid traceparent is an error. An invalid tracestate
// is dropped (yielding an empty TraceState) and does NOT fail extraction, per
// the W3C spec's resilience requirement.
func Extract(carrier map[string]string) (TraceParent, *TraceState, error) {
	if carrier == nil {
		return TraceParent{}, &TraceState{}, ErrTraceParentMissing
	}
	raw, ok := carrier["traceparent"]
	if !ok || raw == "" {
		return TraceParent{}, &TraceState{}, ErrTraceParentMissing
	}
	tp, err := ParseTraceParent(raw)
	if err != nil {
		return TraceParent{}, &TraceState{}, err
	}
	ts, tsErr := ParseTraceState(carrier["tracestate"])
	if tsErr != nil {
		ts = &TraceState{}
	}
	return tp, ts, nil
}

// InjectHTTP writes the headers into anything providing Set(key, value), such
// as net/http.Header.
func InjectHTTP(h interface{ Set(string, string) }, tp TraceParent, ts *TraceState) {
	if h == nil {
		return
	}
	h.Set("traceparent", tp.String())
	if ts != nil && ts.Len() > 0 {
		h.Set("tracestate", ts.String())
	}
}

// ExtractHTTP reads the headers from anything providing Get(key) string, such
// as net/http.Header. Semantics match Extract.
func ExtractHTTP(h interface{ Get(string) string }) (TraceParent, *TraceState, error) {
	if h == nil {
		return TraceParent{}, &TraceState{}, ErrTraceParentMissing
	}
	raw := h.Get("traceparent")
	if raw == "" {
		return TraceParent{}, &TraceState{}, ErrTraceParentMissing
	}
	tp, err := ParseTraceParent(raw)
	if err != nil {
		return TraceParent{}, &TraceState{}, err
	}
	ts, tsErr := ParseTraceState(h.Get("tracestate"))
	if tsErr != nil {
		ts = &TraceState{}
	}
	return tp, ts, nil
}

// --- low-level hex / validation helpers ---

const lowerHexDigits = "0123456789abcdef"

func hexByte(b byte) string {
	return string([]byte{lowerHexDigits[b>>4], lowerHexDigits[b&0x0f]})
}

// parseHexByte parses exactly two lowercase hex chars into a byte.
func parseHexByte(s string) (byte, bool) {
	if len(s) != 2 {
		return 0, false
	}
	hi, ok := hexVal(s[0])
	if !ok {
		return 0, false
	}
	lo, ok := hexVal(s[1])
	if !ok {
		return 0, false
	}
	return hi<<4 | lo, true
}

// hexVal returns the value of a lowercase hex digit. Uppercase is rejected to
// honour the spec's lowercase-on-the-wire requirement.
func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	default:
		return 0, false
	}
}

// isLowerHex reports whether s is non-empty and all lowercase hex.
func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if _, ok := hexVal(s[i]); !ok {
			return false
		}
	}
	return true
}

// isAllZeroHex reports whether every char in s is '0'.
func isAllZeroHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

// validTraceStateKey validates a tracestate key per the simple-key grammar:
// lcalpha (0*255 lcalpha / DIGIT / "_" / "-" / "*" / "/"), optionally with a
// "@vendor" tenant suffix. We implement a pragmatic subset: first char must be
// lowercase alpha or a digit, remaining chars from the allowed set, length
// 1..256, and at most one '@'.
func validTraceStateKey(k string) bool {
	if k == "" || len(k) > 256 {
		return false
	}
	atCount := 0
	for i := 0; i < len(k); i++ {
		c := k[i]
		if i == 0 {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') {
				return false
			}
			continue
		}
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '*' || c == '/':
		case c == '@':
			atCount++
			if atCount > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// validTraceStateValue validates a tracestate value. Per spec it is 0..256
// chars of printable ASCII (0x20..0x7E) excluding comma and equals, and must
// not end with whitespace. We require at least one char.
func validTraceStateValue(v string) bool {
	if v == "" || len(v) > 256 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x20 || c > 0x7e || c == ',' || c == '=' {
			return false
		}
	}
	// Must not end with a space.
	if v[len(v)-1] == ' ' || v[len(v)-1] == '\t' {
		return false
	}
	return true
}

// randomHex returns n random bytes encoded as 2n lowercase hex chars, retrying
// in the astronomically unlikely event of an all-zero draw so the result is
// always a spec-valid (never all-zero) identifier.
func randomHex(n int) string {
	for {
		b := make([]byte, n)
		if _, err := io.ReadFull(randReader, b); err != nil {
			panic("tracing: failed to read random bytes: " + err.Error())
		}
		allZero := true
		for _, x := range b {
			if x != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			continue
		}
		out := make([]byte, 2*n)
		for i, x := range b {
			out[2*i] = lowerHexDigits[x>>4]
			out[2*i+1] = lowerHexDigits[x&0x0f]
		}
		return string(out)
	}
}
