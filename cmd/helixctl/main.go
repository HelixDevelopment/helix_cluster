// Command helixctl is the operator-facing CLI for the Helix Cluster OS build
// orchestration service. It is a thin gRPC client of helixv1.BuildService
// (served by cmd/helix-build): every subcommand performs a real RPC against a
// running build service — there is no canned/offline output.
//
// The CLI is split into a testable seam:
//   - dialClient(ctx, cfg): dials the build service and returns a connected
//     helixv1.BuildServiceClient plus a closer.
//   - runSubmit/runStatus/runCancel/runLogs(ctx, client, ...): the core actions,
//     which take an already-connected client and write to an io.Writer. Tests
//     drive these directly against an in-process build service.
//
// Cobra commands wire flags + connection and delegate to those action functions.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// defaultAddr is the build service's well-known gRPC address. It matches the
// helix-build service default port (50051) on localhost.
const defaultAddr = "localhost:50051"

// defaultTimeout bounds a single CLI invocation's RPC(s). Streaming logs honour
// this as the overall stream lifetime.
const defaultTimeout = 30 * time.Second

// Connection holds the resolved connection parameters shared by all build
// subcommands. addr is resolved from the --addr flag, falling back to the
// HELIX_BUILD_ADDR env var, then defaultAddr.
type Connection struct {
	Addr    string
	Timeout time.Duration
}

// dialClient opens a real gRPC connection to the build service and returns a
// connected client plus a closer the caller must invoke. Insecure transport
// matches the build service's plaintext listener (cmd/helix-build).
func dialClient(_ context.Context, conn Connection) (helixv1.BuildServiceClient, func() error, error) {
	cc, err := grpc.NewClient(conn.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial build service at %s: %w", conn.Addr, err)
	}
	return helixv1.NewBuildServiceClient(cc), cc.Close, nil
}

// resolveAddr applies the --addr flag / HELIX_BUILD_ADDR env / default
// precedence. An explicit non-default flag value always wins; otherwise the env
// var (if set) is used; otherwise the default.
func resolveAddr(flagVal string, getenv func(string) string) string {
	if flagVal != "" && flagVal != defaultAddr {
		return flagVal
	}
	if env := getenv("HELIX_BUILD_ADDR"); env != "" {
		return env
	}
	if flagVal != "" {
		return flagVal
	}
	return defaultAddr
}

var (
	addrFlag    string
	timeoutFlag time.Duration
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "helixctl",
		Short: "Helix Cluster OS operator CLI",
		Long: `helixctl is the operator-facing CLI for Helix Cluster OS.

The build command group is a thin gRPC client of the helix-build service
(BuildService): every subcommand performs a real RPC against a running build
service at --addr (default localhost:50051, overridable by HELIX_BUILD_ADDR).`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&addrFlag, "addr", defaultAddr,
		"build service gRPC address (overridable by HELIX_BUILD_ADDR)")
	root.PersistentFlags().DurationVar(&timeoutFlag, "timeout", defaultTimeout,
		"per-invocation RPC timeout")
	root.AddCommand(newBuildCmd())
	return root
}

// connection builds a Connection from the persistent flags + environment.
func connection() Connection {
	return Connection{Addr: resolveAddr(addrFlag, os.Getenv), Timeout: timeoutFlag}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
