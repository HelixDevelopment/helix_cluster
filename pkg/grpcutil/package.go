// Package grpcutil provides gRPC utilities for Helix Cluster OS.
package grpcutil

import (
	"context"
	"google.golang.org/grpc"
)

// UnaryInterceptor is a simple unary interceptor.
func UnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	return handler(ctx, req)
}

// StreamInterceptor is a simple stream interceptor.
func StreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(srv, ss)
}
