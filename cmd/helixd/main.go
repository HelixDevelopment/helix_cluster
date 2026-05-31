// Package main is the CLI entry point for the helixd control plane daemon.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const version = "0.1.0"

// status holds the current daemon health status.
type status struct {
	Version   string            `json:"version"`
	Status    string            `json:"status"`
	Services  map[string]string `json:"services"`
	Timestamp int64             `json:"timestamp"`
}

func main() {
	port := os.Getenv("HELIXD_PORT")
	if port == "" {
		port = "8081"
	}

	fmt.Printf("╔══════════════════════════════════════╗\n")
	fmt.Printf("║     HelixCluster Control Plane       ║\n")
	fmt.Printf("║         helixd v%-10s           ║\n", version)
	fmt.Printf("╚══════════════════════════════════════╝\n")

	svcStatus := checkServices()

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		resp := status{
			Version:   version,
			Status:    "healthy",
			Services:  svcStatus,
			Timestamp: time.Now().Unix(),
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("encode error: %v", err)
		}
	})

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	srvErr := make(chan error, 1)
	go func() {
		log.Printf("helixd status endpoint on :%s/status", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			srvErr <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
	case err := <-srvErr:
		log.Printf("listen error: %v", err)
	}

	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// checkServices performs simple connectivity checks to etcd, postgres, and redis.
func checkServices() map[string]string {
	results := make(map[string]string)

	results["etcd"] = pingTCP("localhost:2379", 2*time.Second)
	results["postgres"] = pingTCP("localhost:5432", 2*time.Second)
	results["redis"] = pingTCP("localhost:6379", 2*time.Second)

	return results
}

func pingTCP(addr string, timeout time.Duration) string {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "unreachable"
	}
	_ = conn.Close()
	return "reachable"
}
