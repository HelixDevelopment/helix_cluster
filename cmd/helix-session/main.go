// Package main is the CLI entry point for the helix-session gRPC service.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/internal/session"
	"google.golang.org/grpc"
)

func main() {
	port := os.Getenv("HELIX_SESSION_PORT")
	if port == "" {
		port = "50053"
	}

	s := grpc.NewServer()
	helixv1.RegisterSessionServiceServer(s, session.NewServer())

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	go func() {
		log.Printf("helix-session listening on :%s", port)
		if err := s.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	s.GracefulStop()
}
