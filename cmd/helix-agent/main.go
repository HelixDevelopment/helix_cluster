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
	"syscall"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/node"
	"github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

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
		id       = fs.String("id", "", "node unique identifier")
		region   = fs.String("region", "us-east-1", "node region")
		bindAddr = fs.String("bind-addr", "127.0.0.1", "SWIM bind address")
		bindPort = fs.Int("bind-port", 7946, "SWIM bind port")
		wgPort   = fs.Int("wg-port", 51820, "WireGuard listen port")
		wgKey    = fs.String("wg-key", "", "WireGuard private key (base64)")
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
	}, nil
}

// run is the testable core. It validates args, builds and starts the agent,
// blocks until ctx is cancelled, then performs a graceful shutdown. It returns
// the process exit code: 0 on a clean run, 2 on bad arguments, 1 on a runtime
// failure (start error or shutdown error).
func run(ctx context.Context, args []string, newAgent agentFactory, stdout, stderr io.Writer) int {
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
