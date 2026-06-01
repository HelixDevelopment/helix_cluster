// Package main is the CLI entry point for the helix-advisory gRPC service.
//
// It wraps the internal/advisory Advisory Lock service. The process is split
// into a thin main() (config + signal context + exit code) and a testable
// run(ctx, cfg, ready) seam that builds the gRPC server, binds a listener,
// serves, and shuts down gracefully with a bounded GracefulStop timeout.
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
	"strings"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/internal/advisory"
	"github.com/HelixDevelopment/helix_cluster/pkg/health"
	"google.golang.org/grpc"
)

const (
	defaultAddr            = ":50059"
	defaultGracefulTimeout = 10 * time.Second
)

// Config holds the validated runtime configuration for the advisory service.
type Config struct {
	// Addr is the gRPC listen address in host:port form (host optional).
	Addr string
	// GracefulTimeout bounds how long GracefulStop is allowed to drain before
	// the server is forcibly stopped.
	GracefulTimeout time.Duration
}

// LoadConfig builds a Config from environment variables and validates it.
//
//	HELIX_ADVISORY_ADDR             — listen address (default ":50059")
//	HELIX_ADVISORY_GRACEFUL_TIMEOUT — graceful-stop budget, Go duration (default "10s")
//
// Validation rejects clearly-bad input (out-of-range ports, non-numeric ports,
// non-positive graceful timeouts) instead of letting net.Listen fail opaquely.
func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:            defaultAddr,
		GracefulTimeout: defaultGracefulTimeout,
	}
	if v := os.Getenv("HELIX_ADVISORY_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("HELIX_ADVISORY_GRACEFUL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid HELIX_ADVISORY_GRACEFUL_TIMEOUT %q: %w", v, err)
		}
		cfg.GracefulTimeout = d
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks that the configuration is usable. It rejects malformed
// addresses and out-of-range ports so the failure is reported up front with a
// clear message rather than as a late, opaque bind error.
func (c Config) Validate() error {
	if c.Addr == "" {
		return errors.New("addr must not be empty")
	}
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return fmt.Errorf("invalid addr %q: %w", c.Addr, err)
	}
	if port == "" {
		return fmt.Errorf("invalid addr %q: port is required", c.Addr)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("invalid addr %q: port %q is not numeric", c.Addr, port)
	}
	// Port 0 (kernel-chosen ephemeral) is permitted; that is how tests bind
	// 127.0.0.1:0. Reject anything outside the valid TCP port range.
	if p < 0 || p > 65535 {
		return fmt.Errorf("invalid addr %q: port %d out of range [0,65535]", c.Addr, p)
	}
	if host != "" {
		if ip := net.ParseIP(host); ip == nil && !isHostname(host) {
			return fmt.Errorf("invalid addr %q: host %q is not a valid IP or hostname", c.Addr, host)
		}
	}
	if c.GracefulTimeout <= 0 {
		return fmt.Errorf("graceful timeout must be positive, got %s", c.GracefulTimeout)
	}
	return nil
}

func isHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" {
			return false
		}
	}
	return true
}

// run builds the gRPC server, binds the listener, serves until ctx is
// cancelled or Serve fails, then performs a bounded graceful shutdown.
//
// The optional ready callback is invoked with the actually-bound address once
// the listener is open (before serving). This lets tests dial the real port
// even when cfg.Addr requests an ephemeral ":0" port.
func run(ctx context.Context, cfg Config, ready func(addr net.Addr)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	lis, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	gs := grpc.NewServer()
	helixv1.RegisterAdvisoryServiceServer(gs, advisory.NewServer())
	hc := health.NewChecker()
	hc.SetStatus(health.Healthy)
	health.RegisterGRPC(gs, hc)

	if ready != nil {
		ready(lis.Addr())
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("helix-advisory listening on %s", lis.Addr())
		if err := gs.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Println("shutting down advisory lock service...")
	}

	stopped := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(stopped)
	}()

	timer := time.NewTimer(cfg.GracefulTimeout)
	defer timer.Stop()
	select {
	case <-stopped:
		log.Println("graceful shutdown complete")
	case <-timer.C:
		log.Println("graceful timeout exceeded, forcing shutdown")
		gs.Stop()
		<-stopped
	}

	// Drain the serve goroutine's terminal value so it does not leak.
	if err := <-serveErr; err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Printf("config error: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, nil); err != nil {
		log.Printf("helix-advisory: %v", err)
		os.Exit(1)
	}
}
