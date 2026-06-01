// Command helix-node is the Node Service gRPC server for Helix Cluster OS.
//
// It wraps internal/node.Server (helixv1.NodeServiceServer) and exposes it over
// gRPC. The process is structured around a testable seam: Config carries the
// validated runtime configuration and run() owns the listen/serve/shutdown
// lifecycle so it can be driven directly from tests on an ephemeral port.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/internal/node"
	"github.com/HelixDevelopment/helix_cluster/pkg/health"
	"github.com/HelixDevelopment/helix_cluster/pkg/metrics"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

// Config holds the validated runtime configuration for the node service.
type Config struct {
	// Addr is the TCP listen address in host:port form (e.g. ":50054" or
	// "127.0.0.1:0"). A ":0" port asks the kernel for an ephemeral port.
	Addr string
	// ShutdownTimeout bounds how long GracefulStop is allowed to run before the
	// server is forcibly stopped.
	ShutdownTimeout time.Duration
}

// DefaultConfig returns the baseline configuration, overlaid with environment
// variables (HELIX_NODE_ADDR) when present.
func DefaultConfig() Config {
	cfg := Config{
		Addr:            ":50054",
		ShutdownTimeout: 10 * time.Second,
	}
	if v := strings.TrimSpace(os.Getenv("HELIX_NODE_ADDR")); v != "" {
		cfg.Addr = v
	}
	return cfg
}

// Validate rejects clearly-bad configuration before any listener is opened so
// that operator mistakes fail fast with an actionable message rather than a
// confusing runtime error.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("addr is required")
	}
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return fmt.Errorf("invalid addr %q: %w", c.Addr, err)
	}
	_ = host // empty host is valid (listen on all interfaces)

	p, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("invalid port %q in addr %q: not a number", port, c.Addr)
	}
	if p < 0 || p > 65535 {
		return fmt.Errorf("invalid port %d in addr %q: out of range 0-65535", p, c.Addr)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown timeout must be positive, got %s", c.ShutdownTimeout)
	}
	return nil
}

func main() {
	reg := metrics.NewServiceRegistry("node")
	metricsSrv, metricsErr := metrics.StartSidecarFromEnv(reg)
	if metricsErr != nil {
		log.Printf("metrics sidecar: %v (continuing without /metrics)", metricsErr)
	}
	defer metrics.ShutdownSidecar(metricsSrv)

	cfg := DefaultConfig()

	root := &cobra.Command{
		Use:           "helix-node",
		Short:         "Helix Node Service — cluster node registry and management",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return run(ctx, cfg, nil)
		},
	}

	root.Flags().StringVar(&cfg.Addr, "addr", cfg.Addr, "gRPC listen address (host:port)")
	root.Flags().DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "bound on graceful shutdown")

	if err := root.ExecuteContext(context.Background()); err != nil {
		log.Fatalf("helix-node: %v", err)
	}
}

// run builds the gRPC server, binds the listener, serves until ctx is cancelled
// (or a signal fires upstream), then performs a bounded graceful shutdown.
//
// If ready is non-nil it is invoked exactly once, after the listener is bound,
// with the resolved listen address. Tests use this to learn the ephemeral port
// chosen for a ":0" bind and to dial a real client.
func run(ctx context.Context, cfg Config, ready func(addr net.Addr)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	lis, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}

	gs := grpc.NewServer()
	helixv1.RegisterNodeServiceServer(gs, node.NewServer())
	hc := health.NewChecker()
	hc.SetStatus(health.Healthy)
	health.RegisterGRPC(gs, hc)

	if ready != nil {
		ready(lis.Addr())
	}
	log.Printf("helix-node listening on %s", lis.Addr())

	// Drive graceful shutdown from ctx cancellation. GracefulStop drains
	// in-flight RPCs; if it overruns ShutdownTimeout we fall back to a hard
	// Stop so the process never hangs on shutdown.
	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		log.Println("helix-node: shutting down...")
		stopped := make(chan struct{})
		go func() {
			gs.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(cfg.ShutdownTimeout):
			log.Printf("helix-node: graceful shutdown exceeded %s, forcing stop", cfg.ShutdownTimeout)
			gs.Stop()
		}
		close(shutdownDone)
	}()

	// Serve returns nil when the server is stopped via GracefulStop/Stop, so a
	// ctx-triggered shutdown is not an error.
	serveErr := gs.Serve(lis)
	<-shutdownDone
	if serveErr != nil && ctx.Err() == nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	return nil
}
