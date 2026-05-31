// Command helix-policy is the policy engine service for Helix Cluster OS.
//
// NOTE ON TRANSPORT: unlike the gRPC control-plane services (e.g.
// helix-scheduler), this service exposes its policy engine over HTTP/JSON.
// There is intentionally no PolicyService in api/v1 and internal/policy.Engine
// is a plain in-process engine, not a gRPC server. The refactor below keeps the
// real HTTP transport but introduces a testable seam (Config + run) so the
// service can be started on an ephemeral port, exercised by a real HTTP client,
// and shut down gracefully under test.
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

	"github.com/HelixDevelopment/helix_cluster/internal/policy"
)

// Config holds the validated runtime configuration for the policy service.
type Config struct {
	// Addr is the TCP listen address (host:port). A ":0" port binds an
	// ephemeral port, which is used by tests.
	Addr string

	// ShutdownTimeout bounds how long a graceful shutdown waits for in-flight
	// requests before the server is forced closed.
	ShutdownTimeout time.Duration
}

// DefaultConfig returns the baseline configuration before env overrides.
func DefaultConfig() Config {
	return Config{
		Addr:            ":50058",
		ShutdownTimeout: 5 * time.Second,
	}
}

// ConfigFromEnv builds a Config from process environment, applying defaults and
// validating the result. It returns an error (never log.Fatal) so callers and
// tests can react to bad input.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := DefaultConfig()
	if port := getenv("HELIX_POLICY_PORT"); port != "" {
		cfg.Addr = ":" + port
	}
	if to := getenv("HELIX_POLICY_SHUTDOWN_TIMEOUT"); to != "" {
		d, err := time.ParseDuration(to)
		if err != nil {
			return Config{}, fmt.Errorf("HELIX_POLICY_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects clearly-bad configuration. The port must be present and a
// number in the valid TCP range (0 is allowed and means "ephemeral").
func (c Config) Validate() error {
	if c.Addr == "" {
		return errors.New("listen address is empty")
	}
	_, portStr, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", c.Addr, err)
	}
	if portStr == "" {
		return fmt.Errorf("invalid listen address %q: missing port", c.Addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("invalid port %q: not a number", portStr)
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("invalid port %d: out of range 0-65535", port)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown timeout must be positive, got %s", c.ShutdownTimeout)
	}
	return nil
}

// newHandler builds the HTTP routes backed by the given policy engine. It is
// exported to the package so tests can mount it directly if desired, but run()
// uses it for the live server.
func newHandler(engine *policy.Engine) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/policies", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, engine.ListPolicies())
		case http.MethodPost:
			var req struct {
				Name string `json:"name"`
				Rego string `json:"rego"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := engine.LoadPolicy(req.Name, req.Rego); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/evaluate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Policy string                 `json:"policy"`
			Input  map[string]interface{} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		allowed, decisions, err := engine.Evaluate(req.Policy, req.Input)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"allowed":   allowed,
			"decisions": decisions,
		})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// run starts the policy HTTP server bound to cfg.Addr and serves until ctx is
// cancelled (or SIGINT/SIGTERM if main wires them into ctx), at which point it
// performs a bounded graceful shutdown. The optional ready callback is invoked
// once the listener is bound, with the resolved address (so tests can dial the
// real ephemeral port). run propagates errors instead of calling os.Exit.
func run(ctx context.Context, cfg Config, ready func(addr net.Addr)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	engine := policy.NewEngine()
	server := &http.Server{Handler: newHandler(engine)}

	lis, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	if ready != nil {
		ready(lis.Addr())
	}
	log.Printf("helix-policy listening on %s", lis.Addr())

	serveErr := make(chan error, 1)
	go func() {
		if err := server.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		log.Println("helix-policy shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			// Force-close on timeout so the listener is always released.
			_ = server.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		// Drain the serve goroutine result (ErrServerClosed maps to nil).
		<-serveErr
		return nil
	case err := <-serveErr:
		return err
	}
}

func main() {
	cfg, err := ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Printf("config error: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, nil); err != nil {
		log.Printf("helix-policy: %v", err)
		os.Exit(1)
	}
}
