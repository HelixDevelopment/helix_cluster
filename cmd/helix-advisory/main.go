package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/internal/advisory"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

var (
	addr         string
	etcdEndpoints []string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "helix-advisory",
		Short: "Helix Cluster Advisory Lock gRPC service",
		RunE:  run,
	}

	rootCmd.Flags().StringVar(&addr, "addr", ":50059", "gRPC listen address")
	rootCmd.Flags().StringSliceVar(&etcdEndpoints, "etcd-endpoints", []string{}, "etcd endpoints (optional)")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func run(cmd *cobra.Command, args []string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	gs := grpc.NewServer()
	srv := advisory.NewServer()
	helixv1.RegisterAdvisoryServiceServer(gs, srv)

	go func() {
		log.Printf("advisory lock service listening on %s", addr)
		if err := gs.Serve(lis); err != nil {
			log.Printf("gRPC server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("shutting down advisory lock service...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		log.Println("graceful shutdown complete")
	case <-ctx.Done():
		log.Println("forcing shutdown")
		gs.Stop()
	}

	return nil
}
