// Command helixd is the umbrella control-plane daemon for Helix Cluster OS.
//
// helixd does not implement business logic itself; it composes the control
// plane by (a) probing the backing subservices the control plane depends on
// (etcd, postgres, redis) and (b) exposing an aggregated /status surface that
// reports the daemon version plus the reachability of each subservice. This is
// the process a supervisor/orchestrator scrapes to decide whether the control
// plane node is wired up correctly.
//
// The monolithic main() has been split into a testable seam:
//   - Config: parsed from env WITH validation (LoadConfig).
//   - Subservice wiring is injectable: a Dependency is {name, addr} and the
//     probe used to check it is a Prober. run() accepts the resolved set so a
//     test can inject fakes (reachable/unreachable) and assert the exact
//     aggregated /status payload without touching real services.
//   - run(ctx, cfg, deps, probe, ready): builds the HTTP server, binds a real
//     listener, serves /status, and shuts down gracefully on ctx cancel /
//     SIGINT / SIGTERM with a bounded timeout. The ready callback reports the
//     actual bound address so tests can dial an ephemeral (":0") port without
//     racing the bind.
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
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/metrics"
)

const version = "0.1.0"

// Reachability states reported per subservice in the /status payload.
const (
	stateReachable   = "reachable"
	stateUnreachable = "unreachable"
)

// defaultShutdownTimeout bounds how long run() waits for in-flight work to
// drain during graceful shutdown before forcing a hard stop.
const defaultShutdownTimeout = 10 * time.Second

// defaultProbeTimeout bounds each individual subservice connectivity probe.
const defaultProbeTimeout = 2 * time.Second

// Dependency is one subservice the control plane is wired to depend on.
type Dependency struct {
	// Name is the stable key reported in the /status "services" map.
	Name string
	// Addr is the host:port the subservice is expected to listen on.
	Addr string
}

// defaultDependencies returns the control-plane subservices helixd composes,
// resolved from the environment with sane localhost defaults. These are the
// backing stores the 14 control-plane microservices share.
func defaultDependencies(getenv func(string) string) []Dependency {
	addr := func(env, def string) string {
		if v := getenv(env); v != "" {
			return v
		}
		return def
	}
	return []Dependency{
		{Name: "etcd", Addr: addr("HELIXD_ETCD_ADDR", "localhost:2379")},
		{Name: "postgres", Addr: addr("HELIXD_POSTGRES_ADDR", "localhost:5432")},
		{Name: "redis", Addr: addr("HELIXD_REDIS_ADDR", "localhost:6379")},
	}
}

// status holds the current daemon health status surfaced at /status.
type status struct {
	Version   string            `json:"version"`
	Status    string            `json:"status"`
	Services  map[string]string `json:"services"`
	Timestamp int64             `json:"timestamp"`
}

// Config holds the validated runtime configuration for the daemon.
type Config struct {
	// Host is the bind host. Empty means all interfaces (":port").
	Host string
	// Port is the TCP port for the /status HTTP surface. Must be in [0,65535];
	// 0 selects an ephemeral port (used by tests).
	Port int
	// ProbeTimeout bounds each subservice connectivity probe. Must be > 0.
	ProbeTimeout time.Duration
	// ShutdownTimeout bounds graceful shutdown. Must be > 0.
	ShutdownTimeout time.Duration
}

// Addr returns the listen address in host:port form.
func (c Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Validate rejects clearly-bad configuration so misconfiguration fails fast at
// startup instead of producing a half-broken listener.
func (c Config) Validate() error {
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d: must be in range 0-65535", c.Port)
	}
	if c.ProbeTimeout <= 0 {
		return fmt.Errorf("invalid probe timeout %s: must be > 0", c.ProbeTimeout)
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
//   - HELIXD_PORT  /status HTTP port (default 8081)
//   - HELIXD_HOST  bind host (default all interfaces)
//
// Non-numeric or out-of-range port values are rejected.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Host:            "",
		Port:            8081,
		ProbeTimeout:    defaultProbeTimeout,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if raw := getenv("HELIXD_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HELIXD_PORT %q is not a valid integer: %w", raw, err)
		}
		cfg.Port = p
	}

	if host := getenv("HELIXD_HOST"); host != "" {
		cfg.Host = host
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Prober probes a single address and reports whether it is reachable. It is the
// injectable seam for subservice wiring: production uses tcpProber; tests inject
// a fake so the aggregated /status payload can be asserted deterministically.
type Prober func(ctx context.Context, addr string, timeout time.Duration) bool

// tcpProber is the production Prober: a bounded TCP dial. A successful dial
// (immediately closed) means the subservice is accepting connections.
func tcpProber(ctx context.Context, addr string, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := d.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// checkServices probes every dependency concurrently and returns a name->state
// map. The result always has exactly one entry per dependency so the /status
// surface is a faithful, complete picture of subservice wiring.
func checkServices(ctx context.Context, deps []Dependency, probe Prober, timeout time.Duration) map[string]string {
	results := make(map[string]string, len(deps))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, dep := range deps {
		wg.Add(1)
		go func(d Dependency) {
			defer wg.Done()
			state := stateUnreachable
			if probe(ctx, d.Addr, timeout) {
				state = stateReachable
			}
			mu.Lock()
			results[d.Name] = state
			mu.Unlock()
		}(dep)
	}
	wg.Wait()
	return results
}

// statusHandler builds the /status HTTP handler. It probes the wired
// subservices on every request so the payload reflects live reachability, and
// reports overall "healthy" (the daemon itself is serving) alongside the
// per-subservice map. The handler is constructed from the same deps/probe used
// by run(), so what a test asserts is exactly what production serves.
func statusHandler(deps []Dependency, probe Prober, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := status{
			Version:   version,
			Status:    "healthy",
			Services:  checkServices(r.Context(), deps, probe, timeout),
			Timestamp: time.Now().Unix(),
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("helixd: encode /status: %v", err)
		}
	}
}

func newMux(deps []Dependency, probe Prober, timeout time.Duration) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", statusHandler(deps, probe, timeout))
	return mux
}

// dependencyNames returns the sorted dependency names, used only for the
// startup banner so logs are deterministic.
func dependencyNames(deps []Dependency) []string {
	names := make([]string, 0, len(deps))
	for _, d := range deps {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

// run builds and serves the helixd /status surface until ctx is cancelled, then
// performs a bounded graceful shutdown. deps is the wired subservice set and
// probe is how each is checked (injected so tests are deterministic). If ready
// is non-nil it is invoked with the actual bound address once the listener is
// open — this lets tests dial an ephemeral (":0") port without racing the bind.
// run returns nil on a clean ctx-triggered shutdown and a non-nil error only on
// a real failure (bad config, bind failure, or a serve error).
func run(ctx context.Context, cfg Config, deps []Dependency, probe Prober, ready func(addr string)) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if probe == nil {
		probe = tcpProber
	}

	lis, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr(), err)
	}

	reg := metrics.NewServiceRegistry("helixd")
	mux := newMux(deps, probe, cfg.ProbeTimeout)
	metrics.Mount(mux, reg)
	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("helixd v%s composing subservices %v", version, dependencyNames(deps))

	if ready != nil {
		ready(lis.Addr().String())
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("helixd /status listening on %s", lis.Addr().String())
		if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("serve: %w", err)
		}
	}()

	select {
	case err := <-serveErr:
		_ = srv.Close()
		return err
	case <-ctx.Done():
		log.Println("helixd shutting down...")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("helixd shutdown error: %v", err)
		_ = srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}

	select {
	case err := <-serveErr:
		return err
	default:
		log.Println("helixd graceful shutdown complete")
		return nil
	}
}

func main() {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Printf("helixd config error: %v", err)
		os.Exit(1)
	}

	fmt.Printf("╔══════════════════════════════════════╗\n")
	fmt.Printf("║     HelixCluster Control Plane       ║\n")
	fmt.Printf("║         helixd v%-10s           ║\n", version)
	fmt.Printf("╚══════════════════════════════════════╝\n")

	deps := defaultDependencies(os.Getenv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, deps, tcpProber, nil); err != nil {
		log.Printf("helixd error: %v", err)
		os.Exit(1)
	}
}
