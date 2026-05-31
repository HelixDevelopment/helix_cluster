// Package main is the CLI entry point for the helix-gateway HTTP reverse proxy.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/gateway"
)

func main() {
	port := os.Getenv("HELIX_GATEWAY_PORT")
	if port == "" {
		port = "8080"
	}

	g, err := gateway.NewGateway()
	if err != nil {
		log.Fatalf("gateway init: %v", err)
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: g,
	}

	go func() {
		log.Printf("helix-gateway listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
