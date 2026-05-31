package tracing

import (
	"net/http"
	"strings"
	"testing"
)

// --- traceparent parsing: valid cases (W3C spec examples) ---

func TestParseTraceParentSpecExample(t *testing.T) {
	// Canonical example from the W3C Trace Context spec.
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tp, err := ParseTraceParent(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tp.Version != 0x00 {
		t.Errorf("expected version 0, got %d", tp.Version)
	}
	if tp.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("unexpected trace id %q", tp.TraceID)
	}
	if tp.ParentID != "00f067aa0ba902b7" {
		t.Errorf("unexpected parent id %q", tp.ParentID)
	}
	if tp.Flags != 0x01 {
		t.Errorf("expected flags 0x01, got %#x", tp.Flags)
	}
	if !tp.Sampled() {
		t.Error("expected sampled bit to be set")
	}
}

func TestParseTraceParentNotSampled(t *testing.T) {
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	tp, err := ParseTraceParent(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tp.Sampled() {
		t.Error("expected sampled bit to be clear")
	}
}

func TestParseTraceParentFutureVersionAccepted(t *testing.T) {
	// Per spec, unknown but valid (non-0xff) versions should be parseable; the
	// trace-id/parent-id/flags are still in the first 55 chars.
	const raw = "cc-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tp, err := ParseTraceParent(raw)
	if err != nil {
		t.Fatalf("unexpected error for future version: %v", err)
	}
	if tp.Version != 0xcc {
		t.Errorf("expected version 0xcc, got %#x", tp.Version)
	}
	if !tp.Sampled() {
		t.Error("expected sampled for future version example")
	}
}

func TestParseTraceParentFutureVersionWithTrailingData(t *testing.T) {
	// Future versions may append extra fields after flags; they must be ignored.
	const raw = "cc-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-what-the-future-brings"
	tp, err := ParseTraceParent(raw)
	if err != nil {
		t.Fatalf("unexpected error for future version with trailing data: %v", err)
	}
	if tp.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("unexpected trace id %q", tp.TraceID)
	}
	if tp.ParentID != "00f067aa0ba902b7" {
		t.Errorf("unexpected parent id %q", tp.ParentID)
	}
}

// --- traceparent parsing: invalid / mutation cases ---

func TestParseTraceParentRejectsAllZeroTraceID(t *testing.T) {
	const raw = "00-00000000000000000000000000000000-00f067aa0ba902b7-01"
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for all-zero trace id, got nil")
	}
}

func TestParseTraceParentRejectsAllZeroParentID(t *testing.T) {
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for all-zero parent id, got nil")
	}
}

func TestParseTraceParentRejectsVersionFF(t *testing.T) {
	const raw = "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for version 0xff, got nil")
	}
}

func TestParseTraceParentRejectsWrongTraceIDLength(t *testing.T) {
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e47-00f067aa0ba902b7-01" // 30 hex chars
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for short trace id, got nil")
	}
}

func TestParseTraceParentRejectsWrongParentIDLength(t *testing.T) {
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902-01" // 14 hex chars
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for short parent id, got nil")
	}
}

func TestParseTraceParentRejectsNonHexTraceID(t *testing.T) {
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e473g-00f067aa0ba902b7-01" // g is not hex
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for non-hex trace id, got nil")
	}
}

func TestParseTraceParentRejectsNonHexFlags(t *testing.T) {
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-xy"
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for non-hex flags, got nil")
	}
}

func TestParseTraceParentRejectsTooFewFields(t *testing.T) {
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7"
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for missing flags field, got nil")
	}
}

func TestParseTraceParentRejectsEmpty(t *testing.T) {
	if _, err := ParseTraceParent(""); err == nil {
		t.Fatal("expected error for empty traceparent, got nil")
	}
}

func TestParseTraceParentRejectsUppercaseHex(t *testing.T) {
	// Spec mandates lowercase hex on the wire.
	const raw = "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01"
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for uppercase hex, got nil")
	}
}

func TestParseTraceParentVersion0RejectsTrailingData(t *testing.T) {
	// For version 0 the string MUST be exactly 55 chars; trailing data is invalid.
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra"
	if _, err := ParseTraceParent(raw); err == nil {
		t.Fatal("expected error for version-0 trailing data, got nil")
	}
}

// --- traceparent formatting / round-trip ---

func TestTraceParentString(t *testing.T) {
	tp := TraceParent{
		Version:  0x00,
		TraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
		ParentID: "00f067aa0ba902b7",
		Flags:    0x01,
	}
	got := tp.String()
	const want = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestTraceParentRoundTrip(t *testing.T) {
	const raw = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tp, err := ParseTraceParent(raw)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if tp.String() != raw {
		t.Errorf("round-trip mismatch: %q != %q", tp.String(), raw)
	}
}

func TestTraceParentSampledFlagRoundTrip(t *testing.T) {
	// Mutation: sampled bit must survive a parse -> SetSampled(false) -> format cycle.
	tp, err := ParseTraceParent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !tp.Sampled() {
		t.Fatal("precondition: expected sampled")
	}
	tp.SetSampled(false)
	if tp.Sampled() {
		t.Error("expected sampled cleared after SetSampled(false)")
	}
	if !strings.HasSuffix(tp.String(), "-00") {
		t.Errorf("expected flags 00 suffix after clearing sampled, got %q", tp.String())
	}
	tp.SetSampled(true)
	if !strings.HasSuffix(tp.String(), "-01") {
		t.Errorf("expected flags 01 suffix after setting sampled, got %q", tp.String())
	}
}

// --- NewTraceParent (generation from an existing Span / fresh ids) ---

func TestNewTraceParentProducesValidHeader(t *testing.T) {
	tp := NewTraceParent(true)
	if len(tp.TraceID) != 32 {
		t.Errorf("expected 32-char trace id, got %d", len(tp.TraceID))
	}
	if len(tp.ParentID) != 16 {
		t.Errorf("expected 16-char parent id, got %d", len(tp.ParentID))
	}
	if !tp.Sampled() {
		t.Error("expected sampled true")
	}
	// Generated header must itself be parseable.
	if _, err := ParseTraceParent(tp.String()); err != nil {
		t.Errorf("generated traceparent not parseable: %v", err)
	}
}

func TestNewTraceParentNeverAllZero(t *testing.T) {
	// Mutation guard: generated ids must never be all-zero (would be rejected on parse).
	for i := 0; i < 50; i++ {
		tp := NewTraceParent(false)
		if tp.TraceID == strings.Repeat("0", 32) {
			t.Fatal("generated all-zero trace id")
		}
		if tp.ParentID == strings.Repeat("0", 16) {
			t.Fatal("generated all-zero parent id")
		}
	}
}

// --- tracestate parsing ---

func TestParseTraceStateSpecExample(t *testing.T) {
	ts, err := ParseTraceState("rojo=00f067aa0ba902b7,congo=t61rcWkgMzE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.Len() != 2 {
		t.Fatalf("expected 2 members, got %d", ts.Len())
	}
	if v := ts.Get("rojo"); v != "00f067aa0ba902b7" {
		t.Errorf("rojo = %q", v)
	}
	if v := ts.Get("congo"); v != "t61rcWkgMzE" {
		t.Errorf("congo = %q", v)
	}
}

func TestParseTraceStatePreservesOrder(t *testing.T) {
	ts, err := ParseTraceState("a=1,b=2,c=3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	keys := ts.Keys()
	want := []string{"a", "b", "c"}
	if len(keys) != len(want) {
		t.Fatalf("expected %d keys, got %d", len(want), len(keys))
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("order mismatch at %d: got %q want %q", i, keys[i], want[i])
		}
	}
}

func TestParseTraceStateTrimsOWS(t *testing.T) {
	// Optional whitespace around commas must be tolerated.
	ts, err := ParseTraceState("a=1, b=2,  c=3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.Get("b") != "2" || ts.Get("c") != "3" {
		t.Errorf("OWS not trimmed: %#v", ts.Keys())
	}
}

func TestParseTraceStateEmptyIsEmpty(t *testing.T) {
	ts, err := ParseTraceState("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.Len() != 0 {
		t.Errorf("expected empty tracestate, got %d members", ts.Len())
	}
}

func TestParseTraceStateRejectsDuplicateKeys(t *testing.T) {
	if _, err := ParseTraceState("a=1,a=2"); err == nil {
		t.Fatal("expected error for duplicate keys, got nil")
	}
}

func TestParseTraceStateRejectsMissingValue(t *testing.T) {
	if _, err := ParseTraceState("a=1,b"); err == nil {
		t.Fatal("expected error for member without '=', got nil")
	}
}

func TestParseTraceStateRejectsEmptyKey(t *testing.T) {
	if _, err := ParseTraceState("=1"); err == nil {
		t.Fatal("expected error for empty key, got nil")
	}
}

func TestParseTraceStateRejectsTooManyMembers(t *testing.T) {
	var parts []string
	for i := 0; i < 33; i++ {
		parts = append(parts, keyN(i)+"=v")
	}
	if _, err := ParseTraceState(strings.Join(parts, ",")); err == nil {
		t.Fatal("expected error for >32 members, got nil")
	}
}

func TestParseTraceStateAccepts32Members(t *testing.T) {
	var parts []string
	for i := 0; i < 32; i++ {
		parts = append(parts, keyN(i)+"=v")
	}
	ts, err := ParseTraceState(strings.Join(parts, ","))
	if err != nil {
		t.Fatalf("unexpected error for 32 members: %v", err)
	}
	if ts.Len() != 32 {
		t.Errorf("expected 32 members, got %d", ts.Len())
	}
}

// --- tracestate mutate-on-write (most-recent vendor first) ---

func TestTraceStateUpsertMovesToFront(t *testing.T) {
	ts, err := ParseTraceState("a=1,b=2,c=3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ts.Upsert("b", "99")
	keys := ts.Keys()
	if keys[0] != "b" {
		t.Errorf("expected mutated vendor 'b' first, got %q (keys=%v)", keys[0], keys)
	}
	if ts.Get("b") != "99" {
		t.Errorf("expected updated value 99, got %q", ts.Get("b"))
	}
	// No duplicate introduced.
	if ts.Len() != 3 {
		t.Errorf("expected 3 members after upsert, got %d", ts.Len())
	}
}

func TestTraceStateUpsertNewKeyPrepends(t *testing.T) {
	ts, err := ParseTraceState("a=1,b=2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ts.Upsert("z", "0")
	keys := ts.Keys()
	if keys[0] != "z" {
		t.Errorf("expected new vendor 'z' first, got %q", keys[0])
	}
	if ts.Len() != 3 {
		t.Errorf("expected 3 members, got %d", ts.Len())
	}
}

func TestTraceStateUpsertEvictsOldestBeyond32(t *testing.T) {
	var parts []string
	for i := 0; i < 32; i++ {
		parts = append(parts, keyN(i)+"=v")
	}
	ts, err := ParseTraceState(strings.Join(parts, ","))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	oldestKey := keyN(31) // last in list == oldest
	ts.Upsert("newvendor", "x")
	if ts.Len() != 32 {
		t.Errorf("expected length capped at 32, got %d", ts.Len())
	}
	if ts.Keys()[0] != "newvendor" {
		t.Errorf("expected newvendor at front, got %q", ts.Keys()[0])
	}
	if ts.Get(oldestKey) != "" {
		t.Errorf("expected oldest member %q evicted", oldestKey)
	}
}

func TestTraceStateStringRoundTrip(t *testing.T) {
	const raw = "rojo=00f067aa0ba902b7,congo=t61rcWkgMzE"
	ts, err := ParseTraceState(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts.String() != raw {
		t.Errorf("round-trip mismatch: %q != %q", ts.String(), raw)
	}
}

func TestTraceStateStringAfterUpsertOrdering(t *testing.T) {
	ts, err := ParseTraceState("a=1,b=2,c=3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ts.Upsert("a", "9")
	if got := ts.String(); got != "a=9,b=2,c=3" {
		t.Errorf("expected 'a=9,b=2,c=3', got %q", got)
	}
}

// --- propagation: inject / extract into http.Header and map ---

func TestInjectExtractHTTPHeader(t *testing.T) {
	tp := TraceParent{
		Version:  0x00,
		TraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
		ParentID: "00f067aa0ba902b7",
		Flags:    0x01,
	}
	ts, _ := ParseTraceState("rojo=00f067aa0ba902b7")

	h := http.Header{}
	InjectHTTP(h, tp, ts)

	if got := h.Get("traceparent"); got != tp.String() {
		t.Errorf("traceparent header = %q, want %q", got, tp.String())
	}
	if got := h.Get("tracestate"); got != "rojo=00f067aa0ba902b7" {
		t.Errorf("tracestate header = %q", got)
	}

	gotTP, gotTS, err := ExtractHTTP(h)
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	if gotTP.TraceID != tp.TraceID || gotTP.ParentID != tp.ParentID {
		t.Errorf("extracted traceparent mismatch: %+v", gotTP)
	}
	if gotTS.Get("rojo") != "00f067aa0ba902b7" {
		t.Error("extracted tracestate mismatch")
	}
}

func TestInjectExtractMap(t *testing.T) {
	tp := NewTraceParent(true)
	ts, _ := ParseTraceState("vendor=val")

	m := map[string]string{}
	Inject(m, tp, ts)

	if m["traceparent"] != tp.String() {
		t.Errorf("map traceparent = %q", m["traceparent"])
	}

	gotTP, gotTS, err := Extract(m)
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	if gotTP.String() != tp.String() {
		t.Errorf("round-trip traceparent mismatch: %q != %q", gotTP.String(), tp.String())
	}
	if gotTS.Get("vendor") != "val" {
		t.Error("round-trip tracestate mismatch")
	}
}

func TestExtractMissingTraceParentErrors(t *testing.T) {
	if _, _, err := Extract(map[string]string{}); err == nil {
		t.Fatal("expected error when traceparent absent, got nil")
	}
}

func TestExtractInvalidTraceParentErrors(t *testing.T) {
	m := map[string]string{"traceparent": "garbage"}
	if _, _, err := Extract(m); err == nil {
		t.Fatal("expected error for invalid traceparent, got nil")
	}
}

func TestExtractInvalidTraceStateIgnored(t *testing.T) {
	// Per spec, an invalid tracestate must NOT invalidate a valid traceparent;
	// it should be dropped, yielding an empty tracestate.
	m := map[string]string{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"tracestate":  "this is !!! not valid",
	}
	tp, ts, err := Extract(m)
	if err != nil {
		t.Fatalf("expected valid traceparent to survive invalid tracestate: %v", err)
	}
	if tp.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Error("traceparent not extracted")
	}
	if ts.Len() != 0 {
		t.Errorf("expected invalid tracestate dropped to empty, got %d members", ts.Len())
	}
}

// --- sampling decision helper ---

func TestSamplingDecisionFromFlags(t *testing.T) {
	if !SampledFromFlags(0x01) {
		t.Error("expected flags 0x01 => sampled")
	}
	if SampledFromFlags(0x00) {
		t.Error("expected flags 0x00 => not sampled")
	}
	// Only the low bit matters for the sampled decision.
	if !SampledFromFlags(0x03) {
		t.Error("expected flags 0x03 => sampled (low bit set)")
	}
	if SampledFromFlags(0x02) {
		t.Error("expected flags 0x02 => not sampled (low bit clear)")
	}
}

// --- helpers ---

func keyN(i int) string {
	// produce valid lowercase tracestate keys k0..k32
	return "k" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [4]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
