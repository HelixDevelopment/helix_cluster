// Command helix-build is the CLI entry point for the Helix Cluster OS build
// orchestration gRPC service. It wraps internal/build and serves the
// helixv1.BuildService API (SubmitBuild, GetBuildStatus, CancelBuild and the
// server-streaming StreamBuildLogs RPC).
//
// The monolithic main() has been split into a testable seam:
//   - Config: parsed from env WITH validation.
//   - run(ctx, cfg, ready): builds the orchestrator + gRPC server, binds,
//     serves, and shuts down gracefully on ctx cancel / SIGINT / SIGTERM with a
//     bounded GracefulStop timeout. The orchestrator's worker pool is started
//     and stopped around the serve loop.
//
// main() stays thin: load config, install a signal-cancelled context, call
// run, exit non-zero on error.
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

	"github.com/HelixDevelopment/helix_cluster/internal/build"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"google.golang.org/grpc"
)

// defaultPort is the build service's well-known gRPC port (matches the
// HELIX_BUILD_PORT default documented for the service).
const defaultPort = 50051

// defaultWorkers is the orchestrator worker-pool size when unset.
const defaultWorkers = 4

// defaultShutdownTimeout bounds how long run() waits for in-flight RPCs to
// drain during graceful shutdown before forcing a hard stop.
const defaultShutdownTimeout = 10 * time.Second

// Config holds the validated runtime configuration for the build service.
type Config struct {
	// Host is the bind host. Empty means all interfaces (":port").
	Host string
	// Port is the TCP port to listen on. Must be in [1, 65535].
	Port int
	// Workers is the orchestrator worker-pool size. Must be >= 1.
	Workers int
	// ShutdownTimeout bounds graceful shutdown. Must be > 0.
	ShutdownTimeout time.Duration
	// SimulatedBuilds, when true, injects the NON-PRODUCTION simulated builder
	// so the gRPC/orchestration plumbing can be exercised deterministically
	// without a real podman daemon. Production leaves this false (real podman).
	SimulatedBuilds bool
}

// Addr returns the listen address in host:port form suitable for net.Listen.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Validate rejects clearly-bad configuration so misconfiguration fails fast at
// startup instead of producing a half-broken listener or a no-op worker pool.
func (c Config) Validate() error {
	// Port 0 is the standard "let the OS choose an ephemeral port" sentinel for
	// net.Listen; it is accepted so callers (and tests) can bind 127.0.0.1:0.
	// Any other out-of-range value is a misconfiguration.
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d: must be in range 0-65535", c.Port)
	}
	if c.Workers < 1 {
		return fmt.Errorf("invalid workers %d: must be >= 1", c.Workers)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("invalid shutdown timeout %s: must be > 0", c.ShutdownTimeout)
	}
	return nil
}

// LoadConfig builds a Config from the environment, applying defaults. It
// returns an error (rather than calling log.Fatal deep in logic) so the caller
// controls the exit path. HELIX_BUILD_PORT / HELIX_BUILD_WORKERS, when set,
// must be valid integers; non-numeric or out-of-range values are rejected.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Host:            "",
		Port:            defaultPort,
		Workers:         defaultWorkers,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if raw := getenv("HELIX_BUILD_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_BUILD_PORT %q is not a valid integer: %w", raw, err)
		}
		cfg.Port = p
	}

	if raw := getenv("HELIX_BUILD_WORKERS"); raw != "" {
		w, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_BUILD_WORKERS %q is not a valid integer: %w", raw, err)
		}
		cfg.Workers = w
	}

	if host := getenv("HELIX_BUILD_HOST"); host != "" {
		cfg.Host = host
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// run builds and serves the build gRPC service until ctx is cancelled, then
// performs a bounded graceful shutdown. If ready is non-nil it is invoked with
// the actual bound address once the listener is open — this lets tests dial an
// ephemeral (":0") port without racing the bind. run returns nil on a clean
// ctx-triggered shutdown and a non-nil error only on a real failure (bad
// config, bind failure, or Serve error).
func run(ctx context.Context, cfg Config, ready func(addr string)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// Build the orchestrator and start its worker pool. The orchestrator's
	// goroutines are torn down on return so a failed bind does not leak workers.
	var orch *build.Orchestrator
	if cfg.SimulatedBuilds {
		orch = build.NewOrchestratorWithBuilder(cfg.Workers, cache.NewMemoryCache(), build.NewSimulatedBuilder())
	} else {
		orch = build.NewOrchestrator(cfg.Workers, cache.NewMemoryCache())
	}
	orch.Start(ctx)
	defer orch.Stop()

	pool := build.NewWorkerPool()
	srv := build.NewServer(orch, pool)

	lis, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr(), err)
	}

	gs := grpc.NewServer()
	srv.Register(gs)

	if ready != nil {
		ready(lis.Addr().String())
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("helix-build listening on %s", lis.Addr().String())
		// Serve returns ErrServerStopped after GracefulStop/Stop, which is a
		// normal shutdown signal, not a failure.
		if err := gs.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		// Serve failed before we were asked to stop.
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Println("helix-build shutting down...")
	}

	// Bounded graceful shutdown: drain in-flight RPCs, but do not hang forever.
	stopped := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(stopped)
	}()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	select {
	case <-stopped:
		log.Println("helix-build graceful shutdown complete")
	case <-shutdownCtx.Done():
		log.Println("helix-build graceful shutdown timed out; forcing stop")
		gs.Stop()
		<-stopped
	}

	// Surface any late Serve error observed during shutdown.
	if err := <-serveErr; err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func main() {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Printf("helix-build config error: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, nil); err != nil {
		log.Printf("helix-build error: %v", err)
		os.Exit(1)
	}
}
