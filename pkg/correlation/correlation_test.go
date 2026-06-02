package correlation

import (
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"testing"
)

// runUUID returns a random run identifier for log correlation across the test.
// It is used ONLY for logging observability, never in assertions, so it does
// not introduce nondeterminism into the logic under test.
func runUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// proxyRequest models the proxy serving a single request: it mints a
// correlation-ID from the injected deterministic source, "serves" it via the
// named backend, and records the audit row. It returns the ID it minted so the
// test can assert the exact stored value.
func proxyRequest(a *Auditor, src *IDSource, backend string, tick int64) string {
	cid := src.Next()
	a.Record(cid, backend, tick)
	return cid
}

// Clause 1: each stored audit Row has the EXACT correlation-ID and the EXACT
// backend it was served by. before = no rows, after = rows.
//
// Mutation: if Record stores a constant/wrong backend (e.g. always "b0"), the
// per-request backend assertion below fails because expected backends differ
// per request and are derived from the request inputs.
func TestRecordExactCorrelationIDAndBackend(t *testing.T) {
	uuid := runUUID(t)
	a := NewAuditor()
	src := NewIDSource("req")

	t.Logf("run=%s before: rows=%d", uuid, a.Len())
	if a.Len() != 0 {
		t.Fatalf("expected no rows before proxying, got %d", a.Len())
	}

	// Distinct backends per request so a constant-backend mutation is caught.
	type req struct {
		backend string
		tick    int64
	}
	reqs := []req{
		{"alpha", 10},
		{"beta", 20},
		{"alpha", 30},
		{"gamma", 40},
	}

	wantIDs := make([]string, len(reqs))
	for i, r := range reqs {
		wantIDs[i] = proxyRequest(a, src, r.backend, r.tick)
	}

	got := a.Rows()
	t.Logf("run=%s after: rows=%d", uuid, len(got))

	if len(got) != len(reqs) {
		t.Fatalf("expected %d rows, got %d", len(reqs), len(got))
	}

	for i, r := range reqs {
		// Correlation-ID derived from the counter source: req-1, req-2, ...
		wantID := wantIDs[i]
		expectID := "req-" + itoa(i+1)
		if wantID != expectID {
			t.Fatalf("minted id[%d]=%q, expected derived %q", i, wantID, expectID)
		}
		if got[i].CorrelationID != wantID {
			t.Errorf("row[%d] CorrelationID=%q, want exact %q", i, got[i].CorrelationID, wantID)
		}
		if got[i].Backend != r.backend {
			t.Errorf("row[%d] Backend=%q, want exact %q", i, got[i].Backend, r.backend)
		}
		if got[i].Tick != r.tick {
			t.Errorf("row[%d] Tick=%d, want %d", i, got[i].Tick, r.tick)
		}
	}
}

// Clause 2: ExportJSONL emits valid JSONL whose parsed rows equal the recorded
// rows (round-trip).
//
// Mutation: a JSONL export that drops the correlation_id field (e.g. struct tag
// "-" or omitting the field) makes the parsed rows' CorrelationID empty, so the
// reflect.DeepEqual against the recorded rows fails.
func TestExportParseJSONLRoundTrip(t *testing.T) {
	uuid := runUUID(t)
	a := NewAuditor()
	src := NewIDSource("trace")

	proxyRequest(a, src, "backend-x", 100)
	proxyRequest(a, src, "backend-y", 200)
	proxyRequest(a, src, "backend-x", 300)

	recorded := a.Rows()
	t.Logf("run=%s before export: rows=%d", uuid, len(recorded))

	jsonl := a.ExportJSONL()
	t.Logf("run=%s jsonl=%q", uuid, jsonl)

	// Three rows => exactly two newline separators, no trailing newline.
	if got := countNewlines(jsonl); got != len(recorded)-1 {
		t.Fatalf("expected %d newline separators, got %d", len(recorded)-1, got)
	}

	parsed, err := ParseJSONL(jsonl)
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	t.Logf("run=%s after parse: rows=%d", uuid, len(parsed))

	if !reflect.DeepEqual(parsed, recorded) {
		t.Fatalf("round-trip mismatch:\n parsed=%+v\n recorded=%+v", parsed, recorded)
	}

	// Explicitly assert the correlation-ID survived export (kills the
	// drop-correlation-id mutation even if some other field coincided).
	for i := range recorded {
		if parsed[i].CorrelationID != recorded[i].CorrelationID || parsed[i].CorrelationID == "" {
			t.Errorf("row[%d] correlation-id lost in round-trip: parsed=%q recorded=%q",
				i, parsed[i].CorrelationID, recorded[i].CorrelationID)
		}
	}
}

// Clause 3: correlation-IDs are unique per request via the deterministic
// counter source.
//
// Mutation: if Next fails to increment the counter (returns a constant), the
// uniqueness check below finds a duplicate and fails; the derived-sequence
// check also fails.
func TestCorrelationIDsUniqueDeterministicCounter(t *testing.T) {
	uuid := runUUID(t)
	src := NewIDSource("id")

	const n = 6
	seen := make(map[string]bool, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = src.Next()
		if seen[ids[i]] {
			t.Fatalf("duplicate correlation-id %q at index %d", ids[i], i)
		}
		seen[ids[i]] = true
		// Derived expectation: id-1, id-2, ...
		want := "id-" + itoa(i+1)
		if ids[i] != want {
			t.Errorf("id[%d]=%q, want derived %q", i, ids[i], want)
		}
	}
	t.Logf("run=%s minted %d unique ids: %v", uuid, len(seen), ids)
	if len(seen) != n {
		t.Fatalf("expected %d unique ids, got %d", n, len(seen))
	}

	// A fresh source restarts the counter deterministically.
	src2 := NewIDSource("id")
	if first := src2.Next(); first != "id-1" {
		t.Errorf("fresh source first id=%q, want %q", first, "id-1")
	}

	// Empty-prefix source yields bare counter values.
	bare := NewIDSource("")
	if v := bare.Next(); v != "1" {
		t.Errorf("empty-prefix first id=%q, want %q", v, "1")
	}
}

// --- small local helpers (no stdlib strconv dependency needed, keep explicit) ---

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func countNewlines(s string) int {
	c := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			c++
		}
	}
	return c
}
