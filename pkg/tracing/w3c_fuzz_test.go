package tracing

import (
	"testing"
)

// FuzzParseTraceParent drives ParseTraceParent with arbitrary strings.
//
// Properties asserted on every successful parse (err == nil):
//  1. Round-trip stability: re-parsing tp.String() yields a TraceParent with
//     identical Version, TraceID, ParentID and Flags. A broken String()
//     renderer or parser would produce a different object on the second parse.
//  2. TraceID is exactly 32 lowercase hex characters and not all-zero.
//  3. ParentID is exactly 16 lowercase hex characters and not all-zero.
//
// On any input the function MUST NOT panic; it must return
// ErrInvalidTraceParent (or another error) instead of panicking.
//
// Seeds cover all interesting spec boundaries: valid v0, version 0xff (banned),
// all-zero IDs (banned), correct length but malformed hex, short/long inputs.
func FuzzParseTraceParent(f *testing.F) {
	// Seed 1: a fully valid version-0 traceparent (55 chars).
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	// Seed 2: sampled bit cleared.
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")

	// Seed 3: version 0xff — must be rejected.
	f.Add("ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	// Seed 4: all-zero trace_id — must be rejected.
	f.Add("00-00000000000000000000000000000000-00f067aa0ba902b7-01")

	// Seed 5: all-zero parent_id — must be rejected.
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01")

	// Seed 6: uppercase hex in trace_id — must be rejected (spec requires lowercase).
	f.Add("00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01")

	// Seed 7: too short (54 chars).
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01")

	// Seed 8: too long for version 0 (56 chars).
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-")

	// Seed 9: future version (01) with trailing data — must be accepted.
	f.Add("01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra")

	// Seed 10: empty string.
	f.Add("")

	// Seed 11: wrong separators.
	f.Add("00+4bf92f3577b34da6a3ce929d0e0e4736+00f067aa0ba902b7+01")

	f.Fuzz(func(t *testing.T, s string) {
		// MUST NOT panic on any input.
		tp, err := ParseTraceParent(s)
		if err != nil {
			// Any error is acceptable; the only requirement is no panic.
			return
		}

		// Property 1: round-trip stability.
		// Re-parse the canonical string produced by String() and confirm the
		// two TraceParents are field-identical.  A bug in String() (e.g. wrong
		// field order) or in ParseTraceParent (e.g. off-by-one slice) would
		// produce a different result on the second parse and trigger this failure.
		wire := tp.String()
		tp2, err2 := ParseTraceParent(wire)
		if err2 != nil {
			t.Fatalf("round-trip: tp.String()=%q re-parsed with error: %v (original input %q)", wire, err2, s)
		}
		if tp.Version != tp2.Version {
			t.Fatalf("round-trip Version mismatch: %v != %v (wire=%q)", tp.Version, tp2.Version, wire)
		}
		if tp.TraceID != tp2.TraceID {
			t.Fatalf("round-trip TraceID mismatch: %q != %q (wire=%q)", tp.TraceID, tp2.TraceID, wire)
		}
		if tp.ParentID != tp2.ParentID {
			t.Fatalf("round-trip ParentID mismatch: %q != %q (wire=%q)", tp.ParentID, tp2.ParentID, wire)
		}
		if tp.Flags != tp2.Flags {
			t.Fatalf("round-trip Flags mismatch: %v != %v (wire=%q)", tp.Flags, tp2.Flags, wire)
		}

		// Property 2: TraceID must be exactly 32 lowercase hex chars.
		// ParseTraceParent is supposed to enforce this; if the check were removed
		// we might accept a shorter or non-hex TraceID here.
		if len(tp.TraceID) != 32 {
			t.Fatalf("TraceID length=%d, want 32 (input=%q)", len(tp.TraceID), s)
		}
		if !isLowerHex(tp.TraceID) {
			t.Fatalf("TraceID %q is not lowercase hex (input=%q)", tp.TraceID, s)
		}
		if isAllZeroHex(tp.TraceID) {
			t.Fatalf("TraceID is all-zero, must be rejected by parser (input=%q)", s)
		}

		// Property 3: ParentID must be exactly 16 lowercase hex chars.
		if len(tp.ParentID) != 16 {
			t.Fatalf("ParentID length=%d, want 16 (input=%q)", len(tp.ParentID), s)
		}
		if !isLowerHex(tp.ParentID) {
			t.Fatalf("ParentID %q is not lowercase hex (input=%q)", tp.ParentID, s)
		}
		if isAllZeroHex(tp.ParentID) {
			t.Fatalf("ParentID is all-zero, must be rejected by parser (input=%q)", s)
		}
	})
}
