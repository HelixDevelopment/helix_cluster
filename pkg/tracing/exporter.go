package tracing

// Span exporting for the tracing package.
//
// This file adds a SpanExporter abstraction plus an OTLP/HTTP-JSON exporter
// that POSTs finished spans to an OpenTelemetry collector using the OTLP
// traces JSON encoding (resourceSpans -> scopeSpans -> spans). It is additive
// and backward-compatible: the default exporter is a no-op, so existing
// behaviour is preserved unless a real exporter is explicitly wired in.
//
// HONEST SCOPE NOTE: this implements the OTLP/HTTP *JSON* protocol only
// (Content-Type application/json, default path /v1/traces). That protocol is
// real and accepted by the OpenTelemetry Collector's OTLP receiver. Full OTLP
// over protobuf and/or gRPC is intentionally out of scope and is a documented
// future seam (see protobufSeam below). Only the Go standard library is used.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SpanExporter consumes a batch of finished spans and ships them to a backend.
//
// ExportSpans must be safe to call with zero spans (a no-op success) and must
// return a non-nil error if the batch could not be delivered.
type SpanExporter interface {
	ExportSpans(ctx context.Context, spans []*Span) error
}

// NoOpExporter discards all spans. It is the default so that wiring an exporter
// is opt-in and the package's historical behaviour is unchanged.
type NoOpExporter struct{}

// NewNoOpExporter returns an exporter that drops every span and never errors.
func NewNoOpExporter() *NoOpExporter { return &NoOpExporter{} }

// ExportSpans implements SpanExporter by doing nothing.
func (*NoOpExporter) ExportSpans(context.Context, []*Span) error { return nil }

// httpDoer is the minimal HTTP client surface the OTLP exporter needs. It is
// satisfied by *http.Client and lets tests substitute a client wired to an
// httptest.Server.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// OTLPHTTPExporter exports spans to an OpenTelemetry collector using the
// OTLP/HTTP JSON traces protocol.
type OTLPHTTPExporter struct {
	endpoint    string
	client      httpDoer
	serviceName string
	scopeName   string
}

// OTLPOption configures an OTLPHTTPExporter.
type OTLPOption func(*OTLPHTTPExporter)

// WithHTTPClient overrides the HTTP client used to POST batches. Pass a client
// whose transport targets a test collector to exercise the exporter offline.
func WithHTTPClient(c httpDoer) OTLPOption {
	return func(e *OTLPHTTPExporter) {
		if c != nil {
			e.client = c
		}
	}
}

// WithServiceName sets the service.name resource attribute on exported spans.
func WithServiceName(name string) OTLPOption {
	return func(e *OTLPHTTPExporter) { e.serviceName = name }
}

// WithScopeName sets the instrumentation scope name reported for spans.
func WithScopeName(name string) OTLPOption {
	return func(e *OTLPHTTPExporter) { e.scopeName = name }
}

// defaultTracesPath is the OTLP/HTTP path the collector listens on for traces.
const defaultTracesPath = "/v1/traces"

// NewOTLPHTTPExporter builds an exporter targeting collectorEndpoint. If the
// endpoint has no path, the standard /v1/traces path is appended.
func NewOTLPHTTPExporter(collectorEndpoint string, opts ...OTLPOption) *OTLPHTTPExporter {
	e := &OTLPHTTPExporter{
		endpoint:    normalizeTracesEndpoint(collectorEndpoint),
		client:      &http.Client{Timeout: 10 * time.Second},
		serviceName: "helix-cluster",
		scopeName:   "github.com/HelixDevelopment/helix_cluster/pkg/tracing",
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// normalizeTracesEndpoint appends the default traces path when the caller gives
// only a base URL (e.g. "http://collector:4318").
func normalizeTracesEndpoint(endpoint string) string {
	trimmed := strings.TrimRight(endpoint, "/")
	// If a path component beyond the host is already present, keep it.
	if i := strings.Index(trimmed, "://"); i >= 0 {
		rest := trimmed[i+3:]
		if strings.Contains(rest, "/") {
			return trimmed
		}
	} else if strings.Contains(trimmed, "/") {
		return trimmed
	}
	return trimmed + defaultTracesPath
}

// ExportSpans encodes spans as an OTLP/HTTP JSON traces payload and POSTs them
// to the collector. A zero-length batch is a no-op success. A non-2xx response
// (or transport failure) is surfaced as an error.
func (e *OTLPHTTPExporter) ExportSpans(ctx context.Context, spans []*Span) error {
	if len(spans) == 0 {
		return nil
	}
	payload := e.encode(spans)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("tracing: marshal OTLP payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("tracing: build OTLP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("tracing: POST OTLP spans: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("tracing: collector rejected spans: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// --- OTLP JSON wire types ---
//
// These mirror the OTLP traces JSON schema closely enough for the collector's
// OTLP receiver to accept them. Field names use the OTLP JSON casing.

type otlpExportRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	Kind              int            `json:"kind"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	Status            otlpStatus     `json:"status"`
}

type otlpStatus struct {
	Code int `json:"code"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string `json:"stringValue"`
}

// encode builds the OTLP request body for the given finished spans.
func (e *OTLPHTTPExporter) encode(spans []*Span) otlpExportRequest {
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		if s == nil {
			continue
		}
		out = append(out, e.encodeSpan(s))
	}
	return otlpExportRequest{
		ResourceSpans: []otlpResourceSpans{{
			Resource: otlpResource{
				Attributes: []otlpKeyValue{{
					Key:   "service.name",
					Value: otlpAnyValue{StringValue: e.serviceName},
				}},
			},
			ScopeSpans: []otlpScopeSpans{{
				Scope: otlpScope{Name: e.scopeName},
				Spans: out,
			}},
		}},
	}
}

// encodeSpan converts one Span into its OTLP JSON representation. Identifiers
// are normalized to the OTLP hex form: 32 hex chars for traceId, 16 for spanId.
func (e *OTLPHTTPExporter) encodeSpan(s *Span) otlpSpan {
	end := s.endUnixNano()
	os := otlpSpan{
		TraceID:           OTLPTraceID(s.TraceID),
		SpanID:            OTLPSpanID(s.SpanID),
		Name:              s.Name,
		Kind:              1, // SPAN_KIND_INTERNAL
		StartTimeUnixNano: fmt.Sprintf("%d", s.StartTime.UnixNano()),
		EndTimeUnixNano:   fmt.Sprintf("%d", end),
		Status:            otlpStatus{Code: spanStatusCode(s)},
	}
	if s.ParentID != "" {
		os.ParentSpanID = OTLPSpanID(s.ParentID)
	}
	for k, v := range s.Tags() {
		os.Attributes = append(os.Attributes, otlpKeyValue{
			Key:   k,
			Value: otlpAnyValue{StringValue: v},
		})
	}
	return os
}

// spanStatusCode maps span tags to an OTLP status code: an "error" tag set to a
// truthy value yields STATUS_CODE_ERROR (2); otherwise STATUS_CODE_UNSET (0).
func spanStatusCode(s *Span) int {
	switch strings.ToLower(s.GetTag("error")) {
	case "true", "1", "yes":
		return 2
	default:
		return 0
	}
}

// endUnixNano returns the span's recorded finish time in unix nanoseconds. If
// the span was never finished, the current time is used so the OTLP end is
// never before the start.
func (s *Span) endUnixNano() int64 {
	s.mu.RLock()
	end := s.endTime
	finished := s.finished
	s.mu.RUnlock()
	if !finished || end.IsZero() {
		return time.Now().UnixNano()
	}
	return end.UnixNano()
}

// OTLPTraceID normalizes an arbitrary trace identifier to a 32-char lowercase
// hex string as required by OTLP. UUID-form ids have their dashes removed; ids
// that are already valid hex are lowercased/padded; anything else is hashed
// deterministically so the mapping is stable and collision-resistant.
func OTLPTraceID(id string) string {
	return normalizeHexID(id, 32)
}

// OTLPSpanID normalizes an arbitrary span identifier to a 16-char lowercase hex
// string as required by OTLP, using the same rules as OTLPTraceID.
func OTLPSpanID(id string) string {
	return normalizeHexID(id, 16)
}

// normalizeHexID coerces id into exactly hexLen lowercase hex chars. Dashes are
// stripped first (so UUIDs map cleanly). If the de-dashed value is already pure
// hex it is truncated/zero-padded to length; otherwise the original id is
// FNV-1a hashed to fill the field deterministically.
func normalizeHexID(id string, hexLen int) string {
	stripped := strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if stripped != "" && isLowerHex(stripped) {
		if len(stripped) == hexLen {
			return stripped
		}
		if len(stripped) > hexLen {
			return stripped[:hexLen]
		}
		return stripped + strings.Repeat("0", hexLen-len(stripped))
	}
	return fnvHex(id, hexLen)
}

// fnvHex deterministically derives hexLen hex chars from s using FNV-1a 64-bit,
// chaining the hash to cover fields wider than 16 hex chars.
func fnvHex(s string, hexLen int) string {
	const (
		offset64 = 1469598103934665603
		prime64  = 1099511628211
	)
	var out []byte
	seed := s
	for len(out)*2 < hexLen {
		h := uint64(offset64)
		for i := 0; i < len(seed); i++ {
			h ^= uint64(seed[i])
			h *= prime64
		}
		var b [8]byte
		for i := 0; i < 8; i++ {
			b[i] = byte((h >> (8 * (7 - i))) & 0xff)
		}
		out = append(out, b[:]...)
		seed = hex.EncodeToString(b[:]) + s // chain for the next block
	}
	enc := hex.EncodeToString(out)
	return enc[:hexLen]
}

// --- batching processor ---

// BatchSpanProcessor queues finished spans and flushes them to an exporter in
// batches. It is the additive wiring point: code finishes spans as before and,
// if a processor is configured, also routes them here. Without a processor the
// tracer behaves exactly as it always has.
type BatchSpanProcessor struct {
	exporter SpanExporter

	mu    sync.Mutex
	queue []*Span
}

// NewBatchSpanProcessor returns a processor that flushes to exporter. A nil
// exporter is replaced with a NoOpExporter so the processor is always safe.
func NewBatchSpanProcessor(exporter SpanExporter) *BatchSpanProcessor {
	if exporter == nil {
		exporter = NewNoOpExporter()
	}
	return &BatchSpanProcessor{exporter: exporter}
}

// OnEnd enqueues a finished span for the next flush. Spans that are not yet
// finished are ignored so that only complete spans reach the collector.
func (p *BatchSpanProcessor) OnEnd(s *Span) {
	if p == nil || s == nil || !s.IsFinished() {
		return
	}
	p.mu.Lock()
	p.queue = append(p.queue, s)
	p.mu.Unlock()
}

// Queued reports how many spans are awaiting flush.
func (p *BatchSpanProcessor) Queued() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// Flush drains the queue and exports the whole batch in a single call. If the
// export fails the spans are re-queued so they are not lost, and the error is
// returned.
func (p *BatchSpanProcessor) Flush(ctx context.Context) error {
	p.mu.Lock()
	batch := p.queue
	p.queue = nil
	p.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}
	if err := p.exporter.ExportSpans(ctx, batch); err != nil {
		// Preserve unflushed spans for a later retry.
		p.mu.Lock()
		p.queue = append(batch, p.queue...)
		p.mu.Unlock()
		return err
	}
	return nil
}
