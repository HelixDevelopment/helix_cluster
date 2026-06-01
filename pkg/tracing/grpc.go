package tracing

// grpc.go — gRPC unary interceptors that propagate a W3C traceparent across
// every hop (gateway -> scheduler -> session) via gRPC metadata.
//
// Design notes:
//   - The W3C TraceParent / Inject / Extract helpers in w3c.go are reused
//     without modification; this file only adds the gRPC transport layer.
//   - A private context key (grpcTraceIDKey) carries the active trace-ID so
//     consecutive client interceptors on the same goroutine pick it up.
//   - Each server hop generates a fresh parent-span-id (via NewTraceParent)
//     while preserving the original trace-id, exactly as the W3C spec requires.

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	metadataTraceparent = "traceparent"
	metadataTracestate  = "tracestate"
)

// grpcTraceIDKey is the private context key used to carry the active trace-ID
// across gRPC interceptor hops on the same call chain.
type grpcTraceIDKey struct{}

// ContextWithTraceID returns a derived context that stores id as the active
// trace-ID for gRPC propagation. It is exported so callers can seed a context
// before the first outbound hop.
func ContextWithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, grpcTraceIDKey{}, id)
}

// TraceIDFromContext returns the trace-ID stored by ContextWithTraceID (or by
// UnaryServerInterceptor). ok is false when the context carries no trace-ID.
func TraceIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(grpcTraceIDKey{}).(string)
	return v, ok && v != ""
}

// UnaryClientInterceptor returns a grpc.UnaryClientInterceptor that reads the
// active trace-ID from ctx, builds a W3C traceparent (fresh span-id, same
// trace-id), and injects it into the outgoing gRPC metadata before invoking
// the next handler. If no trace-ID is present in ctx a new trace is started.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		// Determine which trace to continue or start.
		var tp TraceParent
		if traceID, ok := TraceIDFromContext(ctx); ok {
			// Continue existing trace with a fresh span-id for this hop.
			tp = NewTraceParent(true)
			tp.TraceID = traceID
		} else {
			// No upstream trace — start a brand-new one.
			tp = NewTraceParent(true)
		}

		// Encode into a map[string]string carrier and lift onto MD.
		carrier := make(map[string]string, 2)
		Inject(carrier, tp, nil) // tracestate not tracked at the gRPC layer

		ctx = metadata.AppendToOutgoingContext(ctx,
			metadataTraceparent, carrier[metadataTraceparent],
		)
		if ts, ok := carrier[metadataTracestate]; ok && ts != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, metadataTracestate, ts)
		}

		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// UnaryServerInterceptor returns a grpc.UnaryServerInterceptor that extracts
// the W3C traceparent from incoming gRPC metadata and stores the trace-ID in
// the request context via ContextWithTraceID. Downstream client calls on that
// context (via UnaryClientInterceptor) will reuse the same trace-ID and
// generate a fresh span-id, satisfying the one-trace-across-N-hops contract.
// If no traceparent is present a new trace is started.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		var traceID string

		if md, ok := metadata.FromIncomingContext(ctx); ok {
			// Build a map[string]string carrier from the first value of each key.
			carrier := make(map[string]string, 2)
			if vals := md.Get(metadataTraceparent); len(vals) > 0 {
				carrier[metadataTraceparent] = vals[0]
			}
			if vals := md.Get(metadataTracestate); len(vals) > 0 {
				carrier[metadataTracestate] = vals[0]
			}

			if tp, _, err := Extract(carrier); err == nil {
				traceID = tp.TraceID
			}
		}

		if traceID == "" {
			// No incoming trace — mint a new one.
			traceID = NewTraceParent(true).TraceID
		}

		ctx = ContextWithTraceID(ctx, traceID)
		return handler(ctx, req)
	}
}
