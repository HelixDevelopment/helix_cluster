// Package main is the CLI entry point for the helix-agent node agent binary.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/node"
	"github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

func main() {
	var (
		id       = flag.String("id", "", "node unique identifier")
		region   = flag.String("region", "us-east-1", "node region")
		bindAddr = flag.String("bind-addr", "127.0.0.1", "SWIM bind address")
		bindPort = flag.Int("bind-port", 7946, "SWIM bind port")
		wgPort   = flag.Int("wg-port", 51820, "WireGuard listen port")
		wgKey    = flag.String("wg-key", "", "WireGuard private key (base64)")
	)
	flag.Parse()

	if *id == "" {
		fmt.Fprintln(os.Stderr, "--id is required")
		flag.Usage()
		os.Exit(1)
	}

	key := *wgKey
	if key == "" {
		priv, _, err := wireguard.GenerateKeyPair()
		if err != nil {
			log.Fatalf("generate key: %v", err)
		}
		key = priv
	}

	cfg := &node.Config{
		ID:                   *id,
		Region:               *region,
		SwimBindAddr:         *bindAddr,
		SwimBindPort:         *bindPort,
		WgListenPort:         *wgPort,
		WgPrivateKey:         key,
		WgNoOp:               true,
		DiscoveryTTL:         30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
	}

	agent, err := node.NewAgent(cfg)
	if err != nil {
		log.Fatalf("new agent: %v", err)
	}

	if err := agent.Start(); err != nil {
		log.Fatalf("start: %v", err)
	}

	log.Printf("helix-agent started: id=%s region=%s status=%s", agent.ID, agent.Region, agent.GetStatus())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	if err := agent.Stop(); err != nil {
		log.Printf("stop error: %v", err)
	}
}
