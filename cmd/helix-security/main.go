// Command helix-security is the CLI entry point for the Helix Cluster OS
// security manager gRPC service. It wraps internal/security and serves the
// helixv1.SecurityService API (Authenticate / Authorize / IssueToken /
// ValidateToken).
//
// The monolithic main() has been split into a testable seam:
//   - Config: parsed from env WITH validation.
//   - run(ctx, cfg, ready): builds the real internal/security gRPC server
//     (Orchestrator + PolicyEnforcer), binds, serves, and shuts down
//     gracefully on ctx cancel / SIGINT / SIGTERM with a bounded GracefulStop
//     timeout.
//
// main() stays thin: load config, install a signal-cancelled context, call
// run, exit non-zero on error.
//
// Anti-bluff note (CLAUDE-1): the previous revision of this command shipped an
// inline *stub* SecurityService that issued fabricated "stub-token-..." strings
// and authorized any token with that prefix — i.e. a PASS-bluff that "worked"
// in tests but did not run the real security logic. This revision wires the
// genuine internal/security implementation (real self-signed cert issuance,
// SPIFFE validation, and RBAC policy enforcement) so tests prove the feature
// works for end users, not that a placeholder returns a constant.
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
	"github.com/HelixDevelopment/helix_cluster/internal/security"
	"github.com/HelixDevelopment/helix_cluster/pkg/health"
	"github.com/HelixDevelopment/helix_cluster/pkg/metrics"
	"google.golang.org/grpc"
)

// defaultPort is the security manager's well-known gRPC port (matches the
// HELIX_SECURITY_PORT default documented for the service).
const defaultPort = 50056

// defaultShutdownTimeout bounds how long run() waits for in-flight RPCs to
// drain during graceful shutdown before forcing a hard stop.
const defaultShutdownTimeout = 10 * time.Second

// Config holds the validated runtime configuration for the security service.
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
// controls the exit path. HELIX_SECURITY_PORT, when set, must be a valid
// integer port; a non-numeric or out-of-range value is rejected.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Host:            "",
		Port:            defaultPort,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if raw := getenv("HELIX_SECURITY_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_SECURITY_PORT %q is not a valid integer: %w", raw, err)
		}
		cfg.Port = p
	}

	if host := getenv("HELIX_SECURITY_HOST"); host != "" {
		cfg.Host = host
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// newSecurityServer constructs the real internal/security gRPC service. The
// Orchestrator is created without a Vault client (nil), in which case it issues
// genuine self-signed certificates locally — real cert crypto, not stubs. The
// PolicyEnforcer starts empty: authorization is deny-by-default until roles are
// loaded, which is the correct safe posture for a fresh process.
func newSecurityServer() *security.GRPCServer {
	orch := security.NewOrchestrator(nil)
	enforcer := security.NewPolicyEnforcer()
	return security.NewGRPCServer(orch, enforcer)
}

// run builds and serves the security gRPC service until ctx is cancelled, then
// performs a bounded graceful shutdown. If ready is non-nil it is invoked with
// the actual bound address once the listener is open — this lets tests dial an
// ephemeral (":0") port without racing the bind. run returns nil on a clean
// ctx-triggered shutdown and a non-nil error only on a real failure (bad
// config, bind failure, or Serve error).
func run(ctx context.Context, cfg Config, ready func(addr string)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	lis, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr(), err)
	}

	gs := grpc.NewServer()
	helixv1.RegisterSecurityServiceServer(gs, newSecurityServer())
	hc := health.NewChecker()
	hc.SetStatus(health.Healthy)
	health.RegisterGRPC(gs, hc)

	if ready != nil {
		ready(lis.Addr().String())
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("helix-security listening on %s", lis.Addr().String())
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
		log.Println("helix-security shutting down...")
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
		log.Println("helix-security graceful shutdown complete")
	case <-shutdownCtx.Done():
		log.Println("helix-security graceful shutdown timed out; forcing stop")
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
		log.Printf("helix-security config error: %v", err)
		os.Exit(1)
	}

	reg := metrics.NewServiceRegistry("security")
	metricsSrv, metricsErr := metrics.StartSidecarFromEnv(reg)
	if metricsErr != nil {
		log.Printf("metrics sidecar: %v (continuing without /metrics)", metricsErr)
	}
	defer metrics.ShutdownSidecar(metricsSrv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, nil); err != nil {
		log.Printf("helix-security error: %v", err)
		os.Exit(1)
	}
}
