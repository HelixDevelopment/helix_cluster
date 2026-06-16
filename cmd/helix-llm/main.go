// Command helix-llm is the CLI entry point for the Helix Cluster OS LLM brain
// service. It wraps internal/llm and serves a small HTTP/JSON API for model
// registration, listing, and (stubbed) inference.
//
// The monolithic main() has been split into a testable seam:
//   - Config: parsed from env WITH validation.
//   - run(ctx, cfg, ready): builds the HTTP server, binds, serves, and shuts
//     down gracefully on ctx cancel / SIGINT / SIGTERM with a bounded
//     shutdown timeout.
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
	"syscall"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/llm"
	"github.com/HelixDevelopment/helix_cluster/pkg/metrics"
)

// defaultPort is the LLM service's well-known HTTP port (matches the
// HELIX_LLM_PORT default documented for the service).
const defaultPort = 50057

// defaultShutdownTimeout bounds how long run() waits for in-flight requests to
// drain during graceful shutdown before the deadline elapses.
const defaultShutdownTimeout = 10 * time.Second

// Config holds the validated runtime configuration for the LLM service.
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
// controls the exit path. HELIX_LLM_PORT, when set, must be a valid integer
// port; a non-numeric or out-of-range value is rejected.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Host:            "",
		Port:            defaultPort,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if raw := getenv("HELIX_LLM_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_LLM_PORT %q is not a valid integer: %w", raw, err)
		}
		cfg.Port = p
	}

	if host := getenv("HELIX_LLM_HOST"); host != "" {
		cfg.Host = host
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

type server struct {
	manager *llm.Manager
}

// newMux wires the HTTP routes for the LLM service.
func (s *server) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/models", s.handleList)
	mux.HandleFunc("/models/register", s.handleRegister)
	mux.HandleFunc("/inference", s.handleInference)
	mux.HandleFunc("/health", s.handleHealth)
	return mux
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Format string `json:"format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.manager.RegisterModel(req.Name, req.Path, req.Format); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"success": true})
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	models := s.manager.ListModels()
	resp := make([]map[string]interface{}, 0, len(models))
	for _, m := range models {
		resp = append(resp, map[string]interface{}{
			"name":   m.Name,
			"path":   m.Path,
			"format": m.Format,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleInference(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.manager.Inference(req.Model, req.Prompt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}

// writeJSON centralises status-code + JSON body writing so handlers stay terse
// and the Content-Type is always set before the body.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// run builds and serves the LLM HTTP service until ctx is cancelled, then
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

	s := &server{manager: llm.NewManager()}
	reg := metrics.NewServiceRegistry("llm")
	llmMux := s.newMux()
	metrics.Mount(llmMux, reg)
	httpServer := &http.Server{
		Handler:           llmMux,
		ReadHeaderTimeout: 10 * time.Second, // G112: bound slow-header (Slowloris) reads
	}

	if ready != nil {
		ready(lis.Addr().String())
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("helix-llm listening on %s", lis.Addr().String())
		// Serve returns ErrServerClosed after Shutdown/Close, which is a normal
		// shutdown signal, not a failure.
		if err := httpServer.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
		log.Println("helix-llm shutting down...")
	}

	// Bounded graceful shutdown: drain in-flight requests, but do not hang
	// forever. On timeout Shutdown returns context.DeadlineExceeded, so we force
	// remaining connections shut with Close.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("helix-llm graceful shutdown timed out (%v); forcing close", err)
		_ = httpServer.Close()
	} else {
		log.Println("helix-llm graceful shutdown complete")
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
		log.Printf("helix-llm config error: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, nil); err != nil {
		log.Printf("helix-llm error: %v", err)
		os.Exit(1)
	}
}
