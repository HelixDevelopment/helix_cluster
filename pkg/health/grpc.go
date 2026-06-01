// Package health — gRPC bridge for the standard grpc.health.v1 protocol.
//
// RegisterGRPC wires a grpc.health.v1 health server onto a *grpc.Server,
// seeding its overall ("") serving status from the supplied *Checker.
// The returned *grpchealth.Server lets callers update serving status at
// runtime (e.g. after a dependency becomes unavailable).
//
// SetServing is a thin helper that maps a bool to SERVING / NOT_SERVING so
// callers do not need to import grpc_health_v1 themselves.
package health

import (
	grpchealth "google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"google.golang.org/grpc"
)

// RegisterGRPC registers the standard grpc.health.v1 HealthServer on srv.
//
// It reads the current status of checker to seed the overall serving status
// (service name ""):
//   - Healthy  → SERVING
//   - Degraded → SERVING  (degraded is still serving, just impaired)
//   - Unhealthy → NOT_SERVING
//
// The returned *grpchealth.Server is live: callers can call
// hs.SetServingStatus(service, status) at any point after registration.
func RegisterGRPC(srv *grpc.Server, checker *Checker) *grpchealth.Server {
	hs := grpchealth.NewServer()

	// Seed the overall ("") slot from the checker's current status.
	if checker.GetStatus() == Unhealthy {
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	} else {
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	}

	grpc_health_v1.RegisterHealthServer(srv, hs)
	return hs
}

// SetServing maps a bool to SERVING (true) or NOT_SERVING (false) and updates
// the named service slot on the health server. Pass service="" to update the
// overall slot.
func SetServing(hs *grpchealth.Server, service string, healthy bool) {
	if healthy {
		hs.SetServingStatus(service, grpc_health_v1.HealthCheckResponse_SERVING)
	} else {
		hs.SetServingStatus(service, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	}
}
