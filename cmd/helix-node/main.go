// Command helix-node is the Node Service gRPC server for Helix Cluster OS.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/internal/node"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

func main() {
	var addr string

	root := &cobra.Command{
		Use:   "helix-node",
		Short: "Helix Node Service — cluster node registry and management",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(addr)
		},
	}

	root.Flags().StringVar(&addr, "addr", ":50054", "gRPC listen address")

	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}

func run(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	gs := grpc.NewServer()
	srv := node.NewServer()
	helixv1.RegisterNodeServiceServer(gs, srv)

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Println("shutting down node service...")
		gs.GracefulStop()
	}()

	log.Printf("node service listening on %s", addr)
	return gs.Serve(lis)
}
