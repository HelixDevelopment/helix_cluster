// Package main is the CLI entry point for the helix-session gRPC service.
//
// The runtime is split into a thin main() and a testable run(ctx, cfg, ready)
// seam so the service can be exercised end-to-end (real listener, real gRPC
// client, real RPC) without spawning a process. See main_test.go.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/internal/session"
	"github.com/HelixDevelopment/helix_cluster/pkg/health"
	"github.com/HelixDevelopment/helix_cluster/pkg/metrics"
	"google.golang.org/grpc"
)

// defaultPort is used when HELIX_SESSION_PORT is unset.
const defaultPort = 50053

// shutdownTimeout bounds the graceful drain before the server is forced to stop.
const shutdownTimeout = 10 * time.Second

// Config is the validated runtime configuration for the helix-session service.
type Config struct {
	// Host is the bind interface. Empty means all interfaces (":port").
	Host string
	// Port is the TCP port to listen on. Must be in [1, 65535].
	Port int
}

// LoadConfig reads configuration from the environment and validates it.
// It returns a descriptive error rather than terminating, so callers (and
// tests) can assert on rejection of bad input.
func LoadConfig() (Config, error) {
	cfg := Config{Host: os.Getenv("HELIX_SESSION_HOST"), Port: defaultPort}

	if raw := os.Getenv("HELIX_SESSION_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_SESSION_PORT %q is not an integer: %w", raw, err)
		}
		cfg.Port = p
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects clearly-bad configuration values supplied by an end user.
// Port 0 is rejected here because a user must name a concrete port; the
// ephemeral-port sentinel is a bind concern handled by validateBind.
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range [1, 65535]", c.Port)
	}
	return nil
}

// validateBind is the gate run() applies before net.Listen. It is laxer than
// Validate by exactly one value: port 0, which instructs the OS to choose an
// ephemeral port (host-safe, used by tests). Out-of-range ports are rejected
// so we never hand net.Listen a clearly-bad value.
func (c Config) validateBind() error {
	if c.Port == 0 {
		return nil
	}
	return c.Validate()
}

// Addr returns the host:port string suitable for net.Listen.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// run builds the gRPC server, binds the listener, serves, and shuts down
// gracefully when ctx is cancelled (driven by SIGINT/SIGTERM in main, or by
// the caller in tests). The optional ready callback is invoked with the
// concrete bound address once the listener is accepting connections; this lets
// tests bind on port 0 and learn the ephemeral address to dial.
func run(ctx context.Context, cfg Config, ready func(addr net.Addr)) error {
	if err := cfg.validateBind(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	lis, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr(), err)
	}

	s := grpc.NewServer()
	helixv1.RegisterSessionServiceServer(s, session.NewServer())
	hc := health.NewChecker()
	hc.SetStatus(health.Healthy)
	health.RegisterGRPC(s, hc)

	srvErr := make(chan error, 1)
	go func() {
		log.Printf("helix-session listening on %s", lis.Addr())
		if serveErr := s.Serve(lis); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			srvErr <- serveErr
		}
		close(srvErr)
	}()

	if ready != nil {
		ready(lis.Addr())
	}

	select {
	case <-ctx.Done():
		log.Println("shutdown signal received, draining...")
	case err := <-srvErr:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	}

	// Bounded graceful stop: drain in-flight RPCs, but do not hang forever.
	stopped := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
		log.Println("graceful shutdown complete")
	case <-time.After(shutdownTimeout):
		log.Printf("graceful shutdown exceeded %s, forcing stop", shutdownTimeout)
		s.Stop()
		<-stopped
	}

	// Surface any late serve error captured during the drain.
	if err := <-srvErr; err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	reg := metrics.NewServiceRegistry("session")
	metricsSrv, metricsErr := metrics.StartSidecarFromEnv(reg)
	if metricsErr != nil {
		log.Printf("metrics sidecar: %v (continuing without /metrics)", metricsErr)
	}
	defer metrics.ShutdownSidecar(metricsSrv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, nil); err != nil {
		log.Fatalf("helix-session: %v", err)
	}
}
