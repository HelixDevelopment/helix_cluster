// Package correlation provides deterministic correlation-ID assignment and a
// backend audit log with JSONL export/parse round-trip. It is a self-contained,
// pure-Go, deterministic model of how a request proxy tags each request with a
// correlation-ID and records which backend ultimately served it.
//
// There is no randomness, clock, network, or host access in the logic under
// test: correlation-IDs are minted by a counter-based source (a value injected
// by the caller), and audit ticks are caller-supplied. This makes every
// observable fully derivable from inputs.
package correlation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Row is one immutable audit record: a request bearing CorrelationID was served
// by Backend at logical time Tick.
type Row struct {
	CorrelationID string `json:"correlation_id"`
	Backend       string `json:"backend"`
	Tick          int64  `json:"tick"`
}

// IDSource is a deterministic, counter-based correlation-ID source. Each call to
// Next yields a unique ID derived solely from the prefix and a monotonically
// increasing counter — no randomness, no clock. The zero value is NOT ready;
// use NewIDSource.
type IDSource struct {
	prefix  string
	counter uint64
}

// NewIDSource returns a counter-based ID source. The first ID minted is
// prefix-1, the next prefix-2, and so on. An empty prefix is allowed and yields
// IDs like "1", "2", ...
func NewIDSource(prefix string) *IDSource {
	return &IDSource{prefix: prefix, counter: 0}
}

// Next returns the next deterministic correlation-ID. IDs are unique across the
// lifetime of this source because the counter strictly increases.
func (s *IDSource) Next() string {
	s.counter++
	if s.prefix == "" {
		return fmt.Sprintf("%d", s.counter)
	}
	return fmt.Sprintf("%s-%d", s.prefix, s.counter)
}

// Auditor accumulates audit Rows in insertion order.
type Auditor struct {
	rows []Row
}

// NewAuditor returns an empty Auditor.
func NewAuditor() *Auditor {
	return &Auditor{}
}

// Record appends an audit Row capturing that the request with correlationID was
// served by backend at tick.
func (a *Auditor) Record(correlationID, backend string, tick int64) {
	a.rows = append(a.rows, Row{
		CorrelationID: correlationID,
		Backend:       backend,
		Tick:          tick,
	})
}

// Rows returns a copy of the recorded rows in insertion order. The returned
// slice is independent of internal state, so callers cannot mutate the log.
func (a *Auditor) Rows() []Row {
	out := make([]Row, len(a.rows))
	copy(out, a.rows)
	return out
}

// Len reports how many rows have been recorded.
func (a *Auditor) Len() int {
	return len(a.rows)
}

// ExportJSONL emits the audit log as JSON Lines: one JSON object per row,
// separated by '\n'. An empty log yields the empty string. The output is
// deterministic given the recorded rows.
func (a *Auditor) ExportJSONL() string {
	if len(a.rows) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range a.rows {
		enc, err := json.Marshal(r)
		if err != nil {
			// Row contains only strings and an int64; json.Marshal cannot fail.
			// Returning what we have keeps the function total.
			continue
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.Write(enc)
	}
	return b.String()
}

// ParseJSONL parses a JSONL document (as produced by ExportJSONL) back into
// Rows. Blank lines are skipped. A malformed line yields an error identifying
// the 1-based line number.
func ParseJSONL(s string) ([]Row, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	lines := strings.Split(s, "\n")
	out := make([]Row, 0, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("ParseJSONL: line %d: %w", i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}
