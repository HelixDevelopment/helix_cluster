package tracing_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/HelixDevelopment/helix_cluster/observability/tracing"
)

const instrumentationName = "github.com/HelixDevelopment/helix_cluster/observability/tracing"

// newRecordingTracer wires the real SDK Tracer to an in-memory exporter via a
// SimpleSpanProcessor so emitted spans can be asserted on directly.
func newRecordingTracer(t *testing.T) (*tracing.Tracer, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tr, err := tracing.New("test", instrumentationName, sdktrace.NewSimpleSpanProcessor(exp))
	if err != nil {
		t.Fatalf("tracing.New: %v", err)
	}
	t.Cleanup(func() {
		if err := tr.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return tr, exp
}

// findSpan returns the first recorded span with the given name.
func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("span %q not found among recorded spans %v", name, spanNames(spans))
	return tracetest.SpanStub{}
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name
	}
	return names
}

func hasAttr(s tracetest.SpanStub, key string, want attribute.Value) bool {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value == want
		}
	}
	return false
}

// TestThreeHopSpanTree runs a simulated controller -> dst -> device flow using
// the REAL OTel SDK with an in-memory recorder, then asserts the recorded spans
// form the correct parent->child tree (same TraceID, correct parent SpanIDs),
// carry the expected attributes/events, and the right status.
func TestThreeHopSpanTree(t *testing.T) {
	tr, exp := newRecordingTracer(t)

	runID := uuid.NewString()
	t.Logf("otel-tracing 3-hop run trace-uuid=%s", runID)

	// Hop 1: controller (root span).
	ctx, controller := tr.Start(context.Background(), "controller.dispatch",
		attribute.String("helix.run_id", runID),
		attribute.String("helix.hop", "controller"),
	)
	tracing.AddEvent(ctx, "controller.received_request",
		attribute.String("rpc.method", "Dispatch"))

	// Hop 2: dst (child of controller).
	dstCtx, dst := tr.Start(ctx, "dst.route",
		attribute.String("helix.hop", "dst"),
		attribute.String("dst.target", "device-7"),
	)
	tracing.AddEvent(dstCtx, "dst.selected_device",
		attribute.String("device.id", "device-7"))

	// Hop 3: device (child of dst).
	devCtx, device := tr.Start(dstCtx, "device.exec",
		attribute.String("helix.hop", "device"),
		attribute.Int("device.queue_depth", 3),
	)
	tracing.AddEvent(devCtx, "device.executed")
	tracing.SetStatus(devCtx, codes.Ok, "")

	// End in reverse order, as a real call stack unwinds.
	device.End()
	dst.End()
	controller.End()

	spans := exp.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("expected 3 recorded spans, got %d: %v", len(spans), spanNames(spans))
	}

	ctrlSpan := findSpan(t, spans, "controller.dispatch")
	dstSpan := findSpan(t, spans, "dst.route")
	devSpan := findSpan(t, spans, "device.exec")

	// --- Same trace: all three spans share one TraceID ---
	traceID := ctrlSpan.SpanContext.TraceID()
	if !traceID.IsValid() {
		t.Fatalf("controller TraceID invalid")
	}
	if dstSpan.SpanContext.TraceID() != traceID {
		t.Errorf("dst TraceID %s != controller TraceID %s", dstSpan.SpanContext.TraceID(), traceID)
	}
	if devSpan.SpanContext.TraceID() != traceID {
		t.Errorf("device TraceID %s != controller TraceID %s", devSpan.SpanContext.TraceID(), traceID)
	}

	// --- Correct parenting: controller is root; dst->controller; device->dst ---
	if ctrlSpan.Parent.IsValid() {
		t.Errorf("controller should be root, but has valid parent %s", ctrlSpan.Parent.SpanID())
	}
	if dstSpan.Parent.SpanID() != ctrlSpan.SpanContext.SpanID() {
		t.Errorf("dst parent SpanID %s != controller SpanID %s",
			dstSpan.Parent.SpanID(), ctrlSpan.SpanContext.SpanID())
	}
	if devSpan.Parent.SpanID() != dstSpan.SpanContext.SpanID() {
		t.Errorf("device parent SpanID %s != dst SpanID %s",
			devSpan.Parent.SpanID(), dstSpan.SpanContext.SpanID())
	}

	// --- Attributes ---
	if !hasAttr(ctrlSpan, "helix.run_id", attribute.StringValue(runID)) {
		t.Errorf("controller missing helix.run_id=%s", runID)
	}
	if !hasAttr(dstSpan, "dst.target", attribute.StringValue("device-7")) {
		t.Errorf("dst missing dst.target=device-7")
	}
	if !hasAttr(devSpan, "device.queue_depth", attribute.IntValue(3)) {
		t.Errorf("device missing device.queue_depth=3")
	}

	// --- Events ---
	if len(ctrlSpan.Events) != 1 || ctrlSpan.Events[0].Name != "controller.received_request" {
		t.Errorf("controller events = %+v, want one controller.received_request", ctrlSpan.Events)
	}
	if len(devSpan.Events) != 1 || devSpan.Events[0].Name != "device.executed" {
		t.Errorf("device events = %+v, want one device.executed", devSpan.Events)
	}

	// --- Status ---
	if devSpan.Status.Code != codes.Ok {
		t.Errorf("device status = %v, want Ok", devSpan.Status.Code)
	}

	// --- Resource: service.name=helix ---
	var gotService string
	for _, kv := range devSpan.Resource.Attributes() {
		if string(kv.Key) == "service.name" {
			gotService = kv.Value.AsString()
		}
	}
	if gotService != tracing.ServiceName {
		t.Errorf("resource service.name = %q, want %q", gotService, tracing.ServiceName)
	}

	t.Logf("span-tree OK: trace=%s controller=%s dst=%s(parent=%s) device=%s(parent=%s)",
		traceID,
		ctrlSpan.SpanContext.SpanID(),
		dstSpan.SpanContext.SpanID(), dstSpan.Parent.SpanID(),
		devSpan.SpanContext.SpanID(), devSpan.Parent.SpanID())
}

// TestW3CPropagationJoinsSameTrace injects the controller span context into a
// W3C traceparent carrier, extracts it into a SEPARATE context, starts the
// device span from it, and asserts the device span joins the SAME trace. This
// proves the cross-boundary propagation mechanism the polyglot trace (HXC-949,
// DEFERRED) will use.
func TestW3CPropagationJoinsSameTrace(t *testing.T) {
	tr, exp := newRecordingTracer(t)

	runID := uuid.NewString()
	t.Logf("otel-tracing propagation run trace-uuid=%s", runID)

	// Controller side: start a span and inject its context into a carrier.
	ctx, controller := tr.Start(context.Background(), "controller.dispatch",
		attribute.String("helix.run_id", runID))
	carrier := propagation.MapCarrier{}
	tr.Inject(ctx, carrier)
	controller.End()

	// Assert a real W3C traceparent header was produced.
	traceparent := carrier.Get("traceparent")
	if traceparent == "" {
		t.Fatalf("no traceparent header injected; carrier=%v", carrier)
	}
	t.Logf("injected W3C traceparent=%q", traceparent)

	controllerTraceID := controller.SpanContext().TraceID()

	// Device side: extract into a FRESH context (simulating a separate
	// process/boundary that only received headers), then start the device span.
	remoteCtx := tr.Extract(context.Background(), carrier)
	_, device := tr.Start(remoteCtx, "device.exec")
	device.End()

	deviceTraceID := device.SpanContext().TraceID()

	if deviceTraceID != controllerTraceID {
		t.Fatalf("propagation FAILED: device TraceID %s != controller TraceID %s",
			deviceTraceID, controllerTraceID)
	}

	// The extracted parent must be the controller's remote span context.
	devSpan := findSpan(t, exp.GetSpans(), "device.exec")
	if devSpan.Parent.SpanID() != controller.SpanContext().SpanID() {
		t.Errorf("device parent SpanID %s != controller SpanID %s (parent not propagated)",
			devSpan.Parent.SpanID(), controller.SpanContext().SpanID())
	}
	if !devSpan.Parent.IsRemote() {
		t.Errorf("device parent should be remote (came over a propagation boundary)")
	}

	t.Logf("propagation OK: device joined trace %s via W3C traceparent (remote parent=%s)",
		deviceTraceID, devSpan.Parent.SpanID())
}

// startDeviceSpan is the propagation-aware seam exercised by the anti-bluff
// test. When useRoot is false it starts the device span from the propagated
// remote context (correct); when true it starts from a fresh root context
// (the injected mutation that MUST break the same-trace guarantee).
func startDeviceSpan(tr *tracing.Tracer, remoteCtx context.Context, useRoot bool) oteltrace.Span {
	ctx := remoteCtx
	if useRoot {
		ctx = context.Background() // MUTATION: ignore propagated parent
	}
	_, span := tr.Start(ctx, "device.exec")
	span.End()
	return span
}

// TestAntiBluffPropagationMustFailWhenRooted is the anti-bluff guard: it proves
// the same-trace assertion is load-bearing. With correct propagation the device
// joins the trace; flipping the seam to start from a fresh root makes the SAME
// assertion fail — so a broken-parenting regression cannot pass silently.
func TestAntiBluffPropagationMustFailWhenRooted(t *testing.T) {
	tr, _ := newRecordingTracer(t)

	ctx, controller := tr.Start(context.Background(), "controller.dispatch")
	carrier := propagation.MapCarrier{}
	tr.Inject(ctx, carrier)
	controller.End()
	controllerTraceID := controller.SpanContext().TraceID()

	remoteCtx := tr.Extract(context.Background(), carrier)

	// Correct path: propagated context -> SAME trace.
	good := startDeviceSpan(tr, remoteCtx, false)
	if good.SpanContext().TraceID() != controllerTraceID {
		t.Fatalf("propagated device should share controller trace, but did not")
	}

	// Mutated path: fresh root -> DIFFERENT trace. We assert the mutation
	// genuinely breaks the same-trace property (i.e. the real assertion would
	// fail), which is exactly what anti-bluff.txt captures.
	bad := startDeviceSpan(tr, remoteCtx, true)
	if bad.SpanContext().TraceID() == controllerTraceID {
		t.Fatalf("anti-bluff broken: rooted device unexpectedly shared the trace; "+
			"the same-trace assertion is NOT load-bearing (controller=%s bad=%s)",
			controllerTraceID, bad.SpanContext().TraceID())
	}

	t.Logf("anti-bluff OK: propagated device trace=%s == controller; "+
		"rooted device trace=%s != controller (mutation correctly breaks same-trace)",
		good.SpanContext().TraceID(), bad.SpanContext().TraceID())
}
