// Command helix-gateway is the CLI entry point for the Helix Cluster OS
// HTTP reverse proxy. It wraps internal/gateway, which routes path-prefixed
// HTTP requests to backend HelixCluster gRPC-fronting services and stamps the
// X-Helix-Gateway marker header on every proxied response.
//
// The monolithic main() has been split into a testable seam:
//   - Config: parsed from env WITH validation.
//   - run(ctx, cfg, ready): builds the gateway, binds, serves, and shuts down
//     gracefully on ctx cancel / SIGINT / SIGTERM with a bounded
//     http.Server.Shutdown timeout.
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
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/gateway"
	"github.com/HelixDevelopment/helix_cluster/pkg/metrics"
)

// defaultPort is the gateway's well-known HTTP port (matches the
// HELIX_GATEWAY_PORT default documented for the service).
const defaultPort = 8080

// defaultShutdownTimeout bounds how long run() waits for in-flight HTTP
// requests to drain during graceful shutdown before returning.
const defaultShutdownTimeout = 10 * time.Second

// Config holds the validated runtime configuration for the gateway service.
type Config struct {
	// Host is the bind host. Empty means all interfaces (":port").
	Host string
	// Port is the TCP port to listen on. Must be in [1, 65535].
	Port int
	// ShutdownTimeout bounds graceful shutdown. Must be > 0.
	ShutdownTimeout time.Duration
}

// Addr returns the listen address in host:port form suitable for net.Listen.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Validate rejects clearly-bad configuration so misconfiguration fails fast at
// startup instead of producing a half-broken listener.
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d: must be in range 1-65535", c.Port)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("invalid shutdown timeout %s: must be > 0", c.ShutdownTimeout)
	}
	return nil
}

// LoadConfig builds a Config from the environment, applying defaults. It
// returns an error (rather than calling log.Fatal deep in logic) so the caller
// controls the exit path. HELIX_GATEWAY_PORT, when set, must be a valid integer
// port; a non-numeric or out-of-range value is rejected.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Host:            "",
		Port:            defaultPort,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if raw := getenv("HELIX_GATEWAY_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_GATEWAY_PORT %q is not a valid integer: %w", raw, err)
		}
		cfg.Port = p
	}

	if host := getenv("HELIX_GATEWAY_HOST"); host != "" {
		cfg.Host = host
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// run builds and serves the gateway HTTP service until ctx is cancelled, then
// performs a bounded graceful shutdown. If ready is non-nil it is invoked with
// the actual bound address once the listener is open — this lets tests dial an
// ephemeral (":0") port without racing the bind. run returns nil on a clean
// ctx-triggered shutdown and a non-nil error only on a real failure (bad
// config, gateway init failure, bind failure, or Serve error).
func run(ctx context.Context, cfg Config, ready func(addr string)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	g, err := gateway.NewGateway()
	if err != nil {
		return fmt.Errorf("gateway init: %w", err)
	}

	// Mount Prometheus /metrics alongside the gateway handler.
	// The gateway is a plain http.Handler (not a ServeMux), so we wrap it in a
	// new mux: /metrics is handled by the registry, everything else is forwarded
	// to the gateway's ServeHTTP.
	reg := metrics.NewServiceRegistry("gateway")
	mux := http.NewServeMux()
	metrics.Mount(mux, reg)
	mux.Handle("/", g)

	lis, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr(), err)
	}

	srv := &http.Server{Handler: mux}

	if ready != nil {
		ready(lis.Addr().String())
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("helix-gateway listening on %s", lis.Addr().String())
		// Serve returns ErrServerClosed after Shutdown/Close, which is a
		// normal shutdown signal, not a failure.
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
		log.Println("helix-gateway shutting down...")
	}

	// Bounded graceful shutdown: drain in-flight requests, but do not hang
	// forever. Shutdown stops the listener immediately, so the port refuses new
	// connections as soon as this returns.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	// Surface any late Serve error observed during shutdown.
	if err := <-serveErr; err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	log.Println("helix-gateway graceful shutdown complete")
	return nil
}

func main() {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Printf("helix-gateway config error: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, nil); err != nil {
		log.Printf("helix-gateway error: %v", err)
		os.Exit(1)
	}
}
