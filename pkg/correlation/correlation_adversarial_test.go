package correlation

import (
	"reflect"
	"strings"
	"testing"
)

// This file pins the audit-trail integrity contract of pkg/correlation with
// adversarial, mutation-verified probes. The centerpiece is the JSONL
// round-trip with tricky field values (newlines, quotes, commas, unicode,
// empty strings, control chars) which must reproduce byte-equal Rows, plus
// exact-attribution and ID-uniqueness/determinism checks.

// ---------------------------------------------------------------------------
// (a) ID UNIQUENESS / DETERMINISM at scale.
// ---------------------------------------------------------------------------

// TestIDSourceManyUniqueAndDeterministicSequence mints a large number of IDs
// and asserts (1) all are distinct and (2) the exact documented sequence
// prefix-1, prefix-2, ... is produced. A counter that fails to increment, that
// is reset, or that reuses a value collapses uniqueness and breaks the
// derived sequence.
func TestIDSourceManyUniqueAndDeterministicSequence(t *testing.T) {
	const n = 100000
	src := NewIDSource("req")
	seen := make(map[string]struct{}, n)
	for i := 1; i <= n; i++ {
		id := src.Next()
		want := "req-" + itoa(i)
		if id != want {
			t.Fatalf("Next() #%d = %q, want exact deterministic %q", i, id, want)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate correlation-id %q minted at call #%d", id, i)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct ids, got %d", n, len(seen))
	}
}

// TestIDSourceCounterIsStrictlyMonotonicAcrossPrefixes guards against an
// implementation that keys uniqueness on prefix alone or shares a counter in
// a way that lets two distinct sources collide. Two sources with the SAME
// prefix that are independent objects must each start at 1 (documented:
// "A fresh source restarts the counter").
func TestIDSourceFreshSourcesAreIndependent(t *testing.T) {
	a := NewIDSource("p")
	b := NewIDSource("p")
	if got := a.Next(); got != "p-1" {
		t.Fatalf("source a first id = %q, want p-1", got)
	}
	if got := a.Next(); got != "p-2" {
		t.Fatalf("source a second id = %q, want p-2", got)
	}
	// b is a distinct object: it must NOT inherit a's advanced counter.
	if got := b.Next(); got != "p-1" {
		t.Fatalf("independent source b first id = %q, want p-1 (counter leaked between sources)", got)
	}
}

// ---------------------------------------------------------------------------
// (b) EXACT ATTRIBUTION across many records, including tricky backends.
// ---------------------------------------------------------------------------

// TestRecordExactAttributionManyTrickyBackends records a batch of (id, backend,
// tick) tuples whose backends contain characters that a naive
// concatenation/format implementation might mangle or mis-pair, and asserts
// every stored Row matches its source tuple field-for-field. A wrong-
// attribution bug (off-by-one pairing, constant backend, swapped fields) is
// caught here.
func TestRecordExactAttributionManyTrickyBackends(t *testing.T) {
	a := NewAuditor()
	src := NewIDSource("cid")

	backends := []string{
		"alpha",
		"backend with spaces",
		`quote"inside`,
		"comma,separated",
		"new\nline",
		"tab\tchar",
		"unicode-😀-backend",
		"",             // empty backend
		"backend-1",    // looks like an ID
		"cid-1",        // collides lexically with a correlation-id form
		`{"json":"x"}`, // backend that is itself JSON
	}

	type want struct {
		id, backend string
		tick        int64
	}
	wants := make([]want, len(backends))
	for i, b := range backends {
		tick := int64(i * 7)
		id := proxyRequest(a, src, b, tick)
		wants[i] = want{id: id, backend: b, tick: tick}
	}

	got := a.Rows()
	if len(got) != len(wants) {
		t.Fatalf("recorded %d rows, want %d", len(got), len(wants))
	}
	for i, w := range wants {
		// Correlation-id must be the deterministic minted value cid-(i+1).
		expectID := "cid-" + itoa(i+1)
		if w.id != expectID {
			t.Fatalf("minted id[%d]=%q, want %q", i, w.id, expectID)
		}
		if got[i].CorrelationID != w.id {
			t.Errorf("row[%d] CorrelationID=%q, want exact %q", i, got[i].CorrelationID, w.id)
		}
		if got[i].Backend != w.backend {
			t.Errorf("row[%d] Backend=%q, want exact %q (wrong attribution)", i, got[i].Backend, w.backend)
		}
		if got[i].Tick != w.tick {
			t.Errorf("row[%d] Tick=%d, want %d", i, got[i].Tick, w.tick)
		}
	}
}

// ---------------------------------------------------------------------------
// (c) JSONL ROUND-TRIP ROBUSTNESS — the centerpiece.
// ---------------------------------------------------------------------------

// TestJSONLRoundTripTrickyFields is the load-bearing probe: it records Rows
// whose every field contains adversarial content (embedded newlines, double
// quotes, backslashes, commas, leading/trailing whitespace, unicode, HTML
// meta-chars, control characters, empty strings) then ExportJSONL +
// ParseJSONL and asserts the parsed Rows are byte-equal to the recorded ones.
//
// The newline case is the critical one: ExportJSONL is line-delimited on
// '\n', so if a field value's literal newline were NOT escaped by the encoder
// it would split a record across two physical lines and corrupt the parse.
// json.Marshal escapes it as "\\n", keeping one record per physical line.
func TestJSONLRoundTripTrickyFields(t *testing.T) {
	a := NewAuditor()

	rows := []Row{
		{CorrelationID: "id-1", Backend: "plain", Tick: 0},
		{CorrelationID: "id\nwith\nnewlines", Backend: "be\nnd", Tick: 1},
		{CorrelationID: `id"with"quotes`, Backend: `b\ack\slash`, Tick: -42},
		{CorrelationID: "id,with,commas", Backend: "a,b,c", Tick: 9223372036854775807},
		{CorrelationID: "  leading-trailing  ", Backend: "\ttabbed\t", Tick: -9223372036854775808},
		{CorrelationID: "unicode-😀-✓-Ω", Backend: "naïve café", Tick: 7},
		{CorrelationID: "<html>&amp;</html>", Backend: "a>b<c&d", Tick: 3},
		{CorrelationID: "ctrl\x01\x02\x1f", Backend: "bell\x07", Tick: 4},
		{CorrelationID: "", Backend: "", Tick: 0}, // all-empty strings
		{CorrelationID: `{"correlation_id":"spoof","backend":"evil","tick":99}`, Backend: "json-injection", Tick: 5},
		{CorrelationID: "carriage\rreturn", Backend: "cr\rlf\r\n", Tick: 6},
	}
	for _, r := range rows {
		a.Record(r.CorrelationID, r.Backend, r.Tick)
	}

	recorded := a.Rows()
	jsonl := a.ExportJSONL()

	// Structural invariant: N records => exactly N-1 physical lines split on
	// '\n'. If a field's newline leaked through unescaped, Split would yield
	// MORE than N segments and this would fail (or the round-trip below would).
	lines := strings.Split(jsonl, "\n")
	if len(lines) != len(recorded) {
		t.Fatalf("ExportJSONL produced %d physical lines for %d records — a field newline leaked into the line stream unescaped",
			len(lines), len(recorded))
	}

	parsed, err := ParseJSONL(jsonl)
	if err != nil {
		t.Fatalf("ParseJSONL on round-tripped tricky data: %v", err)
	}
	if !reflect.DeepEqual(parsed, recorded) {
		// Pinpoint the first divergence for diagnosis.
		for i := range recorded {
			if i >= len(parsed) {
				t.Errorf("row[%d] missing from parse", i)
				continue
			}
			if parsed[i] != recorded[i] {
				t.Errorf("row[%d] round-trip mismatch:\n recorded=%#v\n parsed  =%#v", i, recorded[i], parsed[i])
			}
		}
		t.Fatalf("JSONL round-trip not byte-equal for tricky fields (len parsed=%d recorded=%d)", len(parsed), len(recorded))
	}
}

// TestJSONLNoAmbiguityWithJSONBackend specifically defends against a delimiter/
// escaping bug where a field literally equal to a JSON object string could be
// mis-parsed as a separate record. The record count must be exactly preserved.
func TestJSONLRoundTripJSONLikeFieldDoesNotForkRecord(t *testing.T) {
	a := NewAuditor()
	a.Record("real-1", `{"correlation_id":"fake","backend":"fake","tick":1}`, 1)
	a.Record("real-2", "ordinary", 2)

	recorded := a.Rows()
	parsed, err := ParseJSONL(a.ExportJSONL())
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected exactly 2 records, got %d (JSON-shaped field forked into a phantom record)", len(parsed))
	}
	if !reflect.DeepEqual(parsed, recorded) {
		t.Fatalf("mismatch: recorded=%#v parsed=%#v", recorded, parsed)
	}
}

// ---------------------------------------------------------------------------
// Malformed / blank line handling: documented skip of blanks, ERROR (not a
// silent wrong-parse) for a truly malformed line.
// ---------------------------------------------------------------------------

// TestParseJSONLBlankLinesSkipped: blank and whitespace-only lines anywhere
// (leading, interior, trailing, runs) are skipped, not parsed into zero-value
// phantom rows.
func TestParseJSONLBlankLinesSkipped(t *testing.T) {
	doc := "\n" +
		`{"correlation_id":"a","backend":"x","tick":1}` + "\n" +
		"\n" +
		"   \n" +
		`{"correlation_id":"b","backend":"y","tick":2}` + "\n" +
		"\t\n"
	got, err := ParseJSONL(doc)
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	want := []Row{
		{CorrelationID: "a", Backend: "x", Tick: 1},
		{CorrelationID: "b", Backend: "y", Tick: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blank-line handling wrong:\n got=%#v\n want=%#v", got, want)
	}
}

// TestParseJSONLMalformedLineErrorsWithLineNumber: a genuinely malformed line
// must surface a typed error naming the 1-based line number — NEVER a silent
// drop or a zero-value row.
func TestParseJSONLMalformedLineErrors(t *testing.T) {
	doc := `{"correlation_id":"a","backend":"x","tick":1}` + "\n" +
		`{not json at all` + "\n" +
		`{"correlation_id":"c","backend":"z","tick":3}`
	got, err := ParseJSONL(doc)
	if err == nil {
		t.Fatalf("expected error on malformed line, got nil (silent wrong-parse); rows=%#v", got)
	}
	if got != nil {
		t.Fatalf("on error, rows should be nil, got %#v", got)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error should identify the 1-based offending line (line 2), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// (d) EDGES: empty export, single record, zero values, no panics.
// ---------------------------------------------------------------------------

// TestExportEmptyAuditorYieldsEmptyAndParsesToNothing pins the documented
// empty-log contract and its round-trip.
func TestExportEmptyAuditorRoundTrip(t *testing.T) {
	a := NewAuditor()
	if a.Len() != 0 {
		t.Fatalf("fresh auditor Len=%d, want 0", a.Len())
	}
	jsonl := a.ExportJSONL()
	if jsonl != "" {
		t.Fatalf("empty auditor ExportJSONL=%q, want empty string", jsonl)
	}
	parsed, err := ParseJSONL(jsonl)
	if err != nil {
		t.Fatalf("ParseJSONL of empty: %v", err)
	}
	if len(parsed) != 0 {
		t.Fatalf("empty round-trip yielded %d rows, want 0", len(parsed))
	}
}

// TestSingleRecordRoundTripNoSeparator: a single record must export with NO
// newline separator and round-trip exactly.
func TestSingleRecordRoundTrip(t *testing.T) {
	a := NewAuditor()
	a.Record("solo", "only-backend", 123)
	jsonl := a.ExportJSONL()
	if strings.Contains(jsonl, "\n") {
		t.Fatalf("single-record export must contain no newline, got %q", jsonl)
	}
	parsed, err := ParseJSONL(jsonl)
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	want := []Row{{CorrelationID: "solo", Backend: "only-backend", Tick: 123}}
	if !reflect.DeepEqual(parsed, want) {
		t.Fatalf("single round-trip mismatch: got=%#v want=%#v", parsed, want)
	}
}

// TestZeroValuesRoundTrip: a Row with all zero/empty fields must survive the
// round-trip and must NOT collide with a blank line on parse (it is a real
// "{...}" line, not whitespace).
func TestZeroValueRowRoundTrip(t *testing.T) {
	a := NewAuditor()
	a.Record("", "", 0)
	recorded := a.Rows()
	jsonl := a.ExportJSONL()
	if strings.TrimSpace(jsonl) == "" {
		t.Fatalf("zero-value row exported to blank line %q — would be silently dropped on parse", jsonl)
	}
	parsed, err := ParseJSONL(jsonl)
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	if !reflect.DeepEqual(parsed, recorded) {
		t.Fatalf("zero-value round-trip mismatch: parsed=%#v recorded=%#v", parsed, recorded)
	}
}

// TestRowsReturnsIndependentCopy: the slice returned by Rows must be a copy;
// mutating it must not corrupt the audit log (attribution integrity under
// caller misuse).
func TestRowsReturnsIndependentCopy(t *testing.T) {
	a := NewAuditor()
	a.Record("id-1", "b1", 1)
	a.Record("id-2", "b2", 2)
	snap := a.Rows()
	snap[0].Backend = "TAMPERED"
	snap[0].CorrelationID = "TAMPERED"
	again := a.Rows()
	if again[0].Backend != "b1" || again[0].CorrelationID != "id-1" {
		t.Fatalf("mutating Rows() output corrupted the log: %#v", again[0])
	}
}
