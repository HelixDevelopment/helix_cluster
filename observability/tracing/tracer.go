// Package tracing provides a real OpenTelemetry distributed-tracing helper
// for Helix Cluster services, built directly on the upstream OTel SDK
// (go.opentelemetry.io/otel + go.opentelemetry.io/otel/sdk/trace).
//
// It delivers the *Go* tracing + W3C trace-context propagation foundation:
//   - a configured TracerProvider carrying a resource (service.name="helix"),
//   - helpers to start spans with attributes, nested child spans, span events,
//     and status,
//   - W3C traceparent inject/extract so a single trace can span a simulated
//     controller -> dst -> device call chain *within* a Go process and across
//     a propagation boundary.
//
// Scope note (HXC-949): the cross-LANGUAGE polyglot trace (Rust controller ->
// Elixir dst -> Go device) remains DEFERRED. This package is the single-runtime
// Go foundation and the exact W3C traceparent mechanism that the deferred
// polyglot trace will build on. It does NOT itself cross a language boundary.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName is the resource service.name attribute value carried by every
// span emitted through a Tracer built by New.
const ServiceName = "helix"

// Tracer is a thin, real wrapper around an OpenTelemetry SDK TracerProvider.
// It owns the provider lifecycle (Shutdown), exposes an otel trace.Tracer for
// starting spans, and carries a W3C TraceContext propagator for cross-boundary
// inject/extract.
type Tracer struct {
	provider   *sdktrace.TracerProvider
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
}

// New constructs a Tracer backed by a real SDK TracerProvider. The provided
// span processors are registered on the provider (pass an exporter-backed
// processor, e.g. a SimpleSpanProcessor wrapping tracetest.NewInMemoryExporter,
// to capture spans for assertions).
//
// The resource always carries service.name=ServiceName ("helix") plus the
// given serviceVersion. instrumentationName names the trace.Tracer (the
// instrumentation scope) — e.g. "github.com/HelixDevelopment/helix_cluster".
func New(serviceVersion, instrumentationName string, processors ...sdktrace.SpanProcessor) (*Tracer, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(ServiceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("tracing: build resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		// AlwaysSample so tests deterministically observe every span — the
		// in-memory recorder must see the full controller->dst->device tree.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	}
	for _, p := range processors {
		opts = append(opts, sdktrace.WithSpanProcessor(p))
	}

	tp := sdktrace.NewTracerProvider(opts...)

	return &Tracer{
		provider:   tp,
		tracer:     tp.Tracer(instrumentationName),
		propagator: propagation.TraceContext{}, // W3C traceparent/tracestate
	}, nil
}

// Provider returns the underlying SDK TracerProvider.
func (t *Tracer) Provider() *sdktrace.TracerProvider { return t.provider }

// OTelTracer returns the underlying otel trace.Tracer.
func (t *Tracer) OTelTracer() trace.Tracer { return t.tracer }

// Propagator returns the W3C TraceContext propagator used by Inject/Extract.
func (t *Tracer) Propagator() propagation.TextMapPropagator { return t.propagator }

// Start begins a new span named name as a child of any span already present in
// ctx (or a root span if ctx has none). Supplied attributes are attached at
// start. It returns the child context (carrying the new span) and the span.
//
// The caller MUST End the returned span — typically `defer span.End()`.
func (t *Tracer) Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, name, trace.WithAttributes(attrs...))
}

// AddEvent records a timestamped span event with optional attributes on the
// span currently active in ctx. It is a no-op if ctx carries no recording span.
func AddEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	trace.SpanFromContext(ctx).AddEvent(name, trace.WithAttributes(attrs...))
}

// SetStatus sets the status of the span currently active in ctx. Use
// codes.Error with a message to mark a failed operation, codes.Ok for an
// explicit success.
func SetStatus(ctx context.Context, code codes.Code, description string) {
	trace.SpanFromContext(ctx).SetStatus(code, description)
}

// RecordError records err as an exception event on the span active in ctx and
// marks the span status as Error. It is a no-op when err is nil.
func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// Inject serializes the span context active in ctx into carrier as W3C
// trace-context headers (traceparent / tracestate). This is the exact
// over-the-wire mechanism a downstream hop (the deferred polyglot trace, or any
// HTTP/gRPC client) uses to continue the same trace.
func (t *Tracer) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	t.propagator.Inject(ctx, carrier)
}

// Extract reads W3C trace-context headers from carrier and returns a context
// carrying the remote span context as the parent. A span subsequently started
// from that context joins the SAME trace (same TraceID) as the injecting side.
func (t *Tracer) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return t.propagator.Extract(ctx, carrier)
}

// Shutdown flushes and stops the underlying TracerProvider, ensuring all
// pending spans are handed to the registered processors/exporters.
func (t *Tracer) Shutdown(ctx context.Context) error {
	return t.provider.Shutdown(ctx)
}
