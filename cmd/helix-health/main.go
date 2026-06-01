// Command helix-health is the health aggregation daemon for Helix Cluster OS.
// It serves the helix.v1.HealthService gRPC API (backed by internal/health)
// and a legacy/dual-stack HTTP surface exposing /health, /livez, /readyz, and
// /check/<service>.
//
// The monolithic main() has been split into a testable seam:
//   - Config: parsed from env WITH validation (LoadConfig).
//   - run(ctx, cfg, ready): builds the gRPC + HTTP servers, binds both real
//     listeners, serves, and shuts down gracefully on ctx cancel / SIGINT /
//     SIGTERM with a bounded timeout. The ready callback reports the actual
//     bound addresses so tests can dial ephemeral (":0") ports without racing
//     the bind.
//
// main() stays thin: load config, install a signal-cancelled context, call
// run, exit non-zero on error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	ihealth "github.com/HelixDevelopment/helix_cluster/internal/health"
	pkghealth "github.com/HelixDevelopment/helix_cluster/pkg/health"
	"github.com/HelixDevelopment/helix_cluster/pkg/metrics"
	"google.golang.org/grpc"
)

// defaultGRPCPort is the health service's well-known gRPC port.
const defaultGRPCPort = 50055

// defaultHTTPPort is the well-known HTTP port for the legacy/probe surface.
// It defaults to the gRPC port + 1 so the two listeners do not collide when
// only HELIX_HEALTH_PORT is set.
const defaultHTTPPort = 50056

// defaultShutdownTimeout bounds how long run() waits for in-flight work to
// drain during graceful shutdown before forcing a hard stop.
const defaultShutdownTimeout = 10 * time.Second

// Config holds the validated runtime configuration for the health service.
type Config struct {
	// Host is the bind host. Empty means all interfaces (":port").
	Host string
	// GRPCPort is the TCP port for the gRPC HealthService. Must be in [1,65535].
	GRPCPort int
	// HTTPPort is the TCP port for the HTTP probe surface. Must be in [1,65535]
	// and must differ from GRPCPort (two distinct TCP listeners).
	HTTPPort int
	// ShutdownTimeout bounds graceful shutdown. Must be > 0.
	ShutdownTimeout time.Duration
}

// GRPCAddr returns the gRPC listen address in host:port form.
func (c Config) GRPCAddr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.GRPCPort))
}

// HTTPAddr returns the HTTP listen address in host:port form.
func (c Config) HTTPAddr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.HTTPPort))
}

// Validate rejects clearly-bad configuration so misconfiguration fails fast at
// startup instead of producing a half-broken listener.
func (c Config) Validate() error {
	if c.GRPCPort < 1 || c.GRPCPort > 65535 {
		return fmt.Errorf("invalid gRPC port %d: must be in range 1-65535", c.GRPCPort)
	}
	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		return fmt.Errorf("invalid HTTP port %d: must be in range 1-65535", c.HTTPPort)
	}
	// Two fixed non-zero ports that are equal would make the second bind fail.
	// Port 0 (ephemeral) is allowed to repeat because the kernel assigns a
	// distinct port to each listener.
	if c.GRPCPort == c.HTTPPort && c.GRPCPort != 0 {
		return fmt.Errorf("gRPC and HTTP ports must differ (both %d)", c.GRPCPort)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("invalid shutdown timeout %s: must be > 0", c.ShutdownTimeout)
	}
	return nil
}

// LoadConfig builds a Config from the environment, applying defaults. It
// returns an error (rather than calling log.Fatal deep in logic) so the caller
// controls the exit path.
//
//   - HELIX_HEALTH_PORT      gRPC port (default 50055)
//   - HELIX_HEALTH_HTTP_PORT HTTP port (default 50056)
//   - HELIX_HEALTH_HOST      bind host (default all interfaces)
//
// Non-numeric or out-of-range port values are rejected.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Host:            "",
		GRPCPort:        defaultGRPCPort,
		HTTPPort:        defaultHTTPPort,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if raw := getenv("HELIX_HEALTH_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_HEALTH_PORT %q is not a valid integer: %w", raw, err)
		}
		cfg.GRPCPort = p
		// Keep the HTTP port adjacent to the gRPC port unless overridden below,
		// so a single HELIX_HEALTH_PORT override does not collide.
		cfg.HTTPPort = p + 1
	}

	if raw := getenv("HELIX_HEALTH_HTTP_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_HEALTH_HTTP_PORT %q is not a valid integer: %w", raw, err)
		}
		cfg.HTTPPort = p
	}

	if host := getenv("HELIX_HEALTH_HOST"); host != "" {
		cfg.Host = host
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// httpServer holds the aggregator-backed HTTP probe surface plus a legacy
// composite checker used by the /check/<service> path.
type httpServer struct {
	aggregator *ihealth.Aggregator
	checker    *pkghealth.CompositeChecker
}

func newHTTPServer(aggregator *ihealth.Aggregator) *httpServer {
	return &httpServer{
		aggregator: aggregator,
		checker:    pkghealth.NewCompositeChecker(),
	}
}

func (s *httpServer) registerDefaults() {
	s.checker.AddCheck("helix-health", func() pkghealth.Status { return pkghealth.Healthy })
}

// clusterHealth aggregates all registered checks into a JSON response.
func (s *httpServer) clusterHealth(w http.ResponseWriter, r *http.Request) {
	status, checks := s.checker.Check()
	code := http.StatusOK
	if status == pkghealth.Unhealthy {
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := struct {
		Status  string                  `json:"status"`
		Checks  []pkghealth.CheckResult `json:"checks,omitempty"`
		Message string                  `json:"message,omitempty"`
	}{
		Status:  string(status),
		Checks:  checks,
		Message: "",
	}
	if status != pkghealth.Healthy {
		resp.Message = fmt.Sprintf("cluster is %s", status)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// livez is the liveness probe: it reports the aggregator's liveness rollup.
// A non-healthy liveness status means the process is wedged and should be
// restarted by its supervisor, so it returns 503.
func (s *httpServer) livez(w http.ResponseWriter, r *http.Request) {
	st := s.aggregator.Liveness()
	code := http.StatusOK
	if st != ihealth.Healthy {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
		Probe  string `json:"probe"`
	}{Status: st.String(), Probe: "liveness"})
}

// readyz is the readiness probe: it reports whether the node should currently
// receive traffic. Not-ready returns 503 so load balancers drain the node.
func (s *httpServer) readyz(w http.ResponseWriter, r *http.Request) {
	ready := s.aggregator.Ready()
	st := s.aggregator.Readiness()
	code := http.StatusOK
	if !ready {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
		Ready  bool   `json:"ready"`
		Probe  string `json:"probe"`
	}{Status: st.String(), Ready: ready, Probe: "readiness"})
}

// serviceHealth returns health for a specific named service.
func (s *httpServer) serviceHealth(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/check/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"service name required"}`, http.StatusBadRequest)
		return
	}
	name := parts[0]

	status, checks := s.checker.Check()
	for _, c := range checks {
		if c.Name == name {
			code := http.StatusOK
			if c.Status == pkghealth.Unhealthy {
				code = http.StatusServiceUnavailable
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(struct {
				Status  string `json:"status"`
				Service string `json:"service"`
			}{
				Status:  string(c.Status),
				Service: name,
			})
			return
		}
	}

	// Service not found — still report based on overall if it's the default self-check
	if name == "helix-health" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Service string `json:"service"`
		}{
			Status:  string(status),
			Service: name,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(struct {
		Error   string `json:"error"`
		Service string `json:"service"`
	}{
		Error:   "service not registered",
		Service: name,
	})
}

func (s *httpServer) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.clusterHealth)
	mux.HandleFunc("/livez", s.livez)
	mux.HandleFunc("/readyz", s.readyz)
	mux.HandleFunc("/check/", s.serviceHealth)
	return mux
}

// ReadyAddrs reports the actually-bound listen addresses to a caller (e.g. a
// test) once both listeners are open.
type ReadyAddrs struct {
	GRPC string
	HTTP string
}

// run builds and serves the health gRPC + HTTP services until ctx is
// cancelled, then performs a bounded graceful shutdown. If ready is non-nil it
// is invoked with the actual bound addresses once both listeners are open —
// this lets tests dial ephemeral (":0") ports without racing the bind. run
// returns nil on a clean ctx-triggered shutdown and a non-nil error only on a
// real failure (bad config, bind failure, or a serve error).
func run(ctx context.Context, cfg Config, ready func(ReadyAddrs)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// ---- aggregator + gRPC health service (internal/health) ----
	aggregator := ihealth.NewAggregator()
	// Register a self liveness check so the readiness/liveness rollups and the
	// gRPC Check RPC return a concrete Healthy status out of the box instead of
	// Unknown. This is the process's own "I am alive and serving" signal.
	aggregator.RegisterCheckKind("helix-health", ihealth.CheckFunc(
		func(ctx context.Context) (ihealth.Status, error) {
			return ihealth.Healthy, nil
		}), ihealth.KindBoth)
	aggregator.RunChecks(ctx)

	grpcSrv := ihealth.NewServer(aggregator)
	gs := grpc.NewServer()
	helixv1.RegisterHealthServiceServer(gs, grpcSrv)

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr())
	if err != nil {
		return fmt.Errorf("grpc listen %s: %w", cfg.GRPCAddr(), err)
	}

	// ---- HTTP probe surface ----
	httpSrv := newHTTPServer(aggregator)
	httpSrv.registerDefaults()

	httpLis, err := net.Listen("tcp", cfg.HTTPAddr())
	if err != nil {
		_ = grpcLis.Close()
		return fmt.Errorf("http listen %s: %w", cfg.HTTPAddr(), err)
	}

	reg := metrics.NewServiceRegistry("health")
	httpMux := httpSrv.mux()
	metrics.Mount(httpMux, reg)
	httpServerInst := &http.Server{
		Handler:      httpMux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if ready != nil {
		ready(ReadyAddrs{
			GRPC: grpcLis.Addr().String(),
			HTTP: httpLis.Addr().String(),
		})
	}

	serveErr := make(chan error, 2)

	go func() {
		log.Printf("helix-health gRPC listening on %s", grpcLis.Addr().String())
		if err := gs.Serve(grpcLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- fmt.Errorf("grpc serve: %w", err)
		}
	}()

	go func() {
		log.Printf("helix-health HTTP listening on %s", httpLis.Addr().String())
		if err := httpServerInst.Serve(httpLis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("http serve: %w", err)
		}
	}()

	select {
	case err := <-serveErr:
		// A listener failed before we were asked to stop.
		gs.Stop()
		_ = httpServerInst.Close()
		return err
	case <-ctx.Done():
		log.Println("helix-health shutting down...")
	}

	// Bounded graceful shutdown: drain in-flight work, but do not hang forever.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(stopped)
	}()

	if err := httpServerInst.Shutdown(shutdownCtx); err != nil {
		log.Printf("helix-health http shutdown error: %v", err)
		_ = httpServerInst.Close()
	}

	select {
	case <-stopped:
		log.Println("helix-health graceful shutdown complete")
	case <-shutdownCtx.Done():
		log.Println("helix-health graceful shutdown timed out; forcing stop")
		gs.Stop()
		<-stopped
	}

	// Surface any serve error observed during shutdown (non-blocking).
	select {
	case err := <-serveErr:
		return err
	default:
		return nil
	}
}

func main() {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Printf("helix-health config error: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, nil); err != nil {
		log.Printf("helix-health error: %v", err)
		os.Exit(1)
	}
}
