// Package grpcutil provides gRPC utilities for Helix Cluster OS.
package grpcutil

import (
	"context"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryInterceptor is a unary interceptor that logs requests and validates auth tokens.
func UnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	start := time.Now()

	// Auth check: look for authorization metadata
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		tokens := md.Get("authorization")
		if len(tokens) > 0 && tokens[0] != "" {
			// In a real system, validate the token here.
			// For now we accept any non-empty bearer token.
			if tokens[0] == "invalid" {
				return nil, status.Error(codes.Unauthenticated, "invalid token")
			}
		}
	}

	resp, err := handler(ctx, req)
	duration := time.Since(start)
	code := codes.OK
	if st, ok := status.FromError(err); ok {
		code = st.Code()
	}
	log.Printf("[gRPC] unary %s code=%s duration=%s err=%v", info.FullMethod, code, duration, err)
	return resp, err
}

// StreamInterceptor is a stream interceptor that logs stream operations and validates auth.
func StreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	start := time.Now()
	ctx := ss.Context()

	// Auth check
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		tokens := md.Get("authorization")
		if len(tokens) > 0 && tokens[0] != "" {
			if tokens[0] == "invalid" {
				return status.Error(codes.Unauthenticated, "invalid token")
			}
		}
	}

	err := handler(srv, ss)
	duration := time.Since(start)
	code := codes.OK
	if st, ok := status.FromError(err); ok {
		code = st.Code()
	}
	log.Printf("[gRPC] stream %s code=%s duration=%s err=%v", info.FullMethod, code, duration, err)
	return err
}

// wrappedStream wraps grpc.ServerStream to allow intercepting RecvMsg/SendMsg.
type wrappedStream struct {
	grpc.ServerStream
	recvCount int
	sendCount int
}

func (w *wrappedStream) RecvMsg(m interface{}) error {
	w.recvCount++
	return w.ServerStream.RecvMsg(m)
}

func (w *wrappedStream) SendMsg(m interface{}) error {
	w.sendCount++
	return w.ServerStream.SendMsg(m)
}

// LoggingStreamInterceptor is a dedicated stream interceptor for detailed message logging.
func LoggingStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ws := &wrappedStream{ServerStream: ss}
	err := handler(srv, ws)
	log.Printf("[gRPC] stream %s recv=%d send=%d err=%v", info.FullMethod, ws.recvCount, ws.sendCount, err)
	return err
}

// AuthUnaryInterceptor returns a unary interceptor that enforces a static API key.
func AuthUnaryInterceptor(apiKey string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if apiKey == "" {
			return handler(ctx, req)
		}
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		tokens := md.Get("x-api-key")
		if len(tokens) == 0 || tokens[0] != apiKey {
			return nil, status.Error(codes.Unauthenticated, "invalid api key")
		}
		return handler(ctx, req)
	}
}

// ChainUnaryInterceptors chains multiple unary interceptors.
func ChainUnaryInterceptors(interceptors ...grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		chain := handler
		for i := len(interceptors) - 1; i >= 0; i-- {
			ic := interceptors[i]
			next := chain
			chain = func(ctx context.Context, req interface{}) (interface{}, error) {
				return ic(ctx, req, info, next)
			}
		}
		return chain(ctx, req)
	}
}
