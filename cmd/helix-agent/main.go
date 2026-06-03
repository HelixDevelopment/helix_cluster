// Package main is the CLI entry point for the helix-agent node agent binary.
//
// helix-agent is a long-lived node agent: it parses flags into a node.Config,
// constructs a node.Agent, starts its subsystems (SWIM gossip over a real UDP
// socket, WireGuard, resource polling, discovery registration), and then blocks
// until its context is cancelled (SIGINT/SIGTERM in production) before
// performing a bounded graceful shutdown.
//
// The runnable core lives in run(), which is fully testable: it takes a
// context, argv, and output streams, and returns a process exit code. main() is
// a thin wrapper that wires real OS signals into run().
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/node"
	"github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// Build-stamped identity. These are overridden at link time by the
// reproducible cross-compile toolchain (scripts/cross-compile-agent.sh /
// `make cross-agent`) via:
//
//	-ldflags "-X main.Version=<v> -X main.BuildUUID=<uuid>"
//
// They default to "dev"/"unknown" for plain `go build`/`go run` so a developer
// build is still self-describing rather than empty. The --version flag prints
// both, which is the CLAUDE-1 sink-side proof that the stamping path works end
// to end (the native darwin/arm64 build must actually run and emit these).
var (
	// Version is the human-readable agent version (e.g. a git tag or semver).
	Version = "dev"
	// BuildUUID uniquely identifies one reproducible build invocation. The
	// cross-compile toolchain stamps the SAME BuildUUID into every per-arch
	// artifact produced by a single run, so the linux/arm64, linux/armv7 and
	// android/arm64 binaries from one build are correlatable.
	BuildUUID = "unknown"
)

// printVersion writes the build identity to w in a stable, greppable form.
// It is the single source of truth for --version so tests can assert on it.
func printVersion(w io.Writer) {
	fmt.Fprintf(w, "helix-agent version %s (build %s)\n", Version, BuildUUID)
}

// agentLike is the subset of *node.Agent behavior run() depends on. Defining it
// as an interface lets tests inject a fake to assert lifecycle ordering without
// real subsystems, while production uses the concrete *node.Agent.
type agentLike interface {
	Start() error
	Stop() error
	GetStatus() node.Status
}

// agentFactory builds a runnable agent from a validated config. Production wires
// this to newRealAgent; tests can substitute a fake.
type agentFactory func(cfg *node.Config) (agentLike, error)

// newRealAgent adapts node.NewAgent to the agentFactory signature.
func newRealAgent(cfg *node.Config) (agentLike, error) {
	return node.NewAgent(cfg)
}

// buildConfig parses argv into a validated node.Config. It performs all
// argument validation and key generation, so it can be unit-tested in isolation
// without standing up any subsystems. A non-nil error means the arguments were
// rejected; the returned config is only valid when err == nil.
func buildConfig(args []string, stderr io.Writer) (*node.Config, error) {
	fs := flag.NewFlagSet("helix-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		id            = fs.String("id", "", "node unique identifier")
		region        = fs.String("region", "us-east-1", "node region")
		bindAddr      = fs.String("bind-addr", "127.0.0.1", "SWIM bind address")
		bindPort      = fs.Int("bind-port", 7946, "SWIM bind port")
		wgPort        = fs.Int("wg-port", 51820, "WireGuard listen port")
		wgKey         = fs.String("wg-key", "", "WireGuard private key (base64)")
		etcdEndpoints = fs.String("etcd-endpoints", "", "comma-separated etcd client endpoints")
	)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if *id == "" {
		return nil, fmt.Errorf("--id is required")
	}
	if *bindPort < 0 || *bindPort > 65535 {
		return nil, fmt.Errorf("--bind-port must be in [0,65535], got %d", *bindPort)
	}
	if *wgPort < 0 || *wgPort > 65535 {
		return nil, fmt.Errorf("--wg-port must be in [0,65535], got %d", *wgPort)
	}

	key := *wgKey
	if key == "" {
		priv, _, err := wireguard.GenerateKeyPair()
		if err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
		key = priv
	}

	// Resolve etcd endpoints: flag takes precedence over env var
	// HELIX_ETCD_ENDPOINTS (mirrors helix-node's HELIX_NODE_ADDR env-overlay
	// style). When neither is set, EtcdEndpoints stays nil, selecting the
	// in-memory discovery backend.
	rawEndpoints := *etcdEndpoints
	if rawEndpoints == "" {
		rawEndpoints = strings.TrimSpace(os.Getenv("HELIX_ETCD_ENDPOINTS"))
	}
	var endpoints []string
	for _, ep := range strings.Split(rawEndpoints, ",") {
		ep = strings.TrimSpace(ep)
		if ep != "" {
			endpoints = append(endpoints, ep)
		}
	}

	return &node.Config{
		ID:                   *id,
		Region:               *region,
		SwimBindAddr:         *bindAddr,
		SwimBindPort:         *bindPort,
		WgListenPort:         *wgPort,
		WgPrivateKey:         key,
		WgNoOp:               true,
		DiscoveryTTL:         30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
		EtcdEndpoints:        endpoints,
	}, nil
}

// run is the testable core. It validates args, builds and starts the agent,
// blocks until ctx is cancelled, then performs a graceful shutdown. It returns
// the process exit code: 0 on a clean run, 2 on bad arguments, 1 on a runtime
// failure (start error or shutdown error).
func run(ctx context.Context, args []string, newAgent agentFactory, stdout, stderr io.Writer) int {
	// --version / -version is handled before any other flag parsing or
	// subsystem wiring: it must work with no other arguments (no --id) and
	// exit cleanly. This is the end-user-visible version path.
	for _, a := range args {
		switch a {
		case "--version", "-version":
			printVersion(stdout)
			return 0
		case "--":
			// Stop scanning at an explicit end-of-flags marker.
			goto parse
		}
	}
parse:
	cfg, err := buildConfig(args, stderr)
	if err != nil {
		// flag.ErrHelp is a clean help request, not a usage error.
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}

	agent, err := newAgent(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "new agent: %v\n", err)
		return 1
	}

	if err := agent.Start(); err != nil {
		fmt.Fprintf(stderr, "start: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "helix-agent started: id=%s region=%s status=%s\n",
		cfg.ID, cfg.Region, agent.GetStatus())

	// Block until the caller cancels (SIGINT/SIGTERM in production).
	<-ctx.Done()

	fmt.Fprintln(stdout, "shutting down...")
	if err := agent.Stop(); err != nil {
		fmt.Fprintf(stderr, "stop error: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], newRealAgent, os.Stdout, os.Stderr))
}
