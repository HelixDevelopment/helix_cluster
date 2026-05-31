// Package main is the CLI entry point for the helix-scheduler gRPC service.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/internal/scheduler"
	"google.golang.org/grpc"
)

func main() {
	port := os.Getenv("HELIX_SCHEDULER_PORT")
	if port == "" {
		port = "50052"
	}

	s := grpc.NewServer()
	helixv1.RegisterSchedulerServiceServer(s, scheduler.NewServer())

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	srvErr := make(chan error, 1)
	go func() {
		log.Printf("helix-scheduler listening on :%s", port)
		if err := s.Serve(lis); err != nil {
			srvErr <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
	case err := <-srvErr:
		log.Printf("serve error: %v", err)
	}

	log.Println("shutting down...")
	s.GracefulStop()
}
