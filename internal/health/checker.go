// Package health provides the health check aggregation service for Helix Cluster OS.
package health

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Status represents the health status of a service or check.
type Status int

const (
	Unknown Status = iota
	Healthy
	Degraded
	Critical
)

// String returns the string representation of a Status.
func (s Status) String() string {
	switch s {
	case Healthy:
		return "healthy"
	case Degraded:
		return "degraded"
	case Critical:
		return "critical"
	default:
		return "unknown"
	}
}

// ParseStatus parses a status string into a Status value.
func ParseStatus(s string) Status {
	switch s {
	case "healthy":
		return Healthy
	case "degraded":
		return Degraded
	case "critical":
		return Critical
	default:
		return Unknown
	}
}

// HealthCheck is the interface for individual health checks.
type HealthCheck interface {
	Check(ctx context.Context) (Status, error)
}

// CheckFunc adapts a function to the HealthCheck interface.
type CheckFunc func(ctx context.Context) (Status, error)

// Check implements the HealthCheck interface.
func (f CheckFunc) Check(ctx context.Context) (Status, error) {
	return f(ctx)
}

// HTTPCheck performs an HTTP health check against an endpoint.
type HTTPCheck struct {
	URL     string
	Timeout time.Duration
	// StatusThreshold maps HTTP status codes to health Status.
	// If nil, 2xx = Healthy, 5xx = Critical, others = Degraded.
	StatusThreshold map[int]Status
}

// Check performs the HTTP health check.
func (c *HTTPCheck) Check(ctx context.Context) (Status, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Critical, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Critical, err
	}
	defer resp.Body.Close()

	if c.StatusThreshold != nil {
		if status, ok := c.StatusThreshold[resp.StatusCode]; ok {
			return status, nil
		}
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Healthy, nil
	case resp.StatusCode >= 500:
		return Critical, nil
	default:
		return Degraded, nil
	}
}

// GRPCCheck performs a gRPC health check against an endpoint.
type GRPCCheck struct {
	Address string
	Service string
	Timeout time.Duration
}

// Check performs the gRPC health check using the standard gRPC health protocol.
func (c *GRPCCheck) Check(ctx context.Context) (Status, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	conn, err := grpc.NewClient(c.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return Critical, err
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: c.Service})
	if err != nil {
		return Critical, err
	}

	switch resp.Status {
	case healthpb.HealthCheckResponse_SERVING:
		return Healthy, nil
	case healthpb.HealthCheckResponse_NOT_SERVING:
		return Critical, nil
	case healthpb.HealthCheckResponse_SERVICE_UNKNOWN:
		return Unknown, nil
	default:
		return Degraded, nil
	}
}

// DiskCheck checks disk usage against a threshold.
type DiskCheck struct {
	Path      string
	Threshold float64 // percentage 0.0 - 1.0 (e.g., 0.9 = 90%)
}

// Check performs the disk usage check.
func (c *DiskCheck) Check(ctx context.Context) (Status, error) {
	var statfs syscallStatfs
	err := statfsFunc(c.Path, &statfs)
	if err != nil {
		return Critical, err
	}

	total := statfs.Blocks * uint64(statfs.Bsize)
	free := statfs.Bavail * uint64(statfs.Bsize)
	if total == 0 {
		return Unknown, fmt.Errorf("unable to determine disk size for %s", c.Path)
	}
	used := 1.0 - float64(free)/float64(total)

	if used >= c.Threshold {
		return Critical, fmt.Errorf("disk usage %.2f%% exceeds threshold %.2f%%", used*100, c.Threshold*100)
	}
	if used >= c.Threshold*0.8 {
		return Degraded, fmt.Errorf("disk usage %.2f%% approaching threshold %.2f%%", used*100, c.Threshold*100)
	}
	return Healthy, nil
}

// MemoryCheck checks memory usage against a threshold.
type MemoryCheck struct {
	Threshold float64 // percentage 0.0 - 1.0 (e.g., 0.9 = 90%)
}

// Check performs the memory usage check.
func (c *MemoryCheck) Check(ctx context.Context) (Status, error) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Use Sys as total allocated from OS; if zero fall back to TotalAlloc approximation.
	total := m.Sys
	if total == 0 {
		return Unknown, fmt.Errorf("unable to determine memory size")
	}
	used := float64(m.HeapAlloc) / float64(total)

	if used >= c.Threshold {
		return Critical, fmt.Errorf("memory usage %.2f%% exceeds threshold %.2f%%", used*100, c.Threshold*100)
	}
	if used >= c.Threshold*0.8 {
		return Degraded, fmt.Errorf("memory usage %.2f%% approaching threshold %.2f%%", used*100, c.Threshold*100)
	}
	return Healthy, nil
}
