package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/HelixDevelopment/helix_cluster/internal/build"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"google.golang.org/grpc"
)

func main() {
	port := os.Getenv("HELIX_BUILD_PORT")
	if port == "" {
		port = "50051"
	}

	ctx := context.Background()

	orch := build.NewOrchestrator(4, cache.NewMemoryCache())
	orch.Start(ctx)
	defer orch.Stop()

	pool := build.NewWorkerPool()

	srv := build.NewServer(orch, pool)

	s := grpc.NewServer()
	srv.Register(s)

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	srvErr := make(chan error, 1)
	go func() {
		log.Printf("helix-build listening on :%s", port)
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
