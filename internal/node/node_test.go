package node

import (
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

func testKey() string {
	priv, _, err := wireguard.GenerateKeyPair()
	if err != nil {
		panic(err)
	}
	return priv
}

func TestNewAgent(t *testing.T) {
	cfg := &Config{
		ID:       "node-1",
		Region:   "us-east-1",
		Labels:   map[string]string{"tier": "T1"},
		SwimBindAddr: "127.0.0.1",
		SwimBindPort: 7946,
		WgListenPort: 51820,
		WgPrivateKey: testKey(),
		WgNoOp:       true,
		DiscoveryTTL: 30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
	}

	agent, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	defer agent.Stop()

	if agent.ID != "node-1" {
		t.Errorf("id = %s, want node-1", agent.ID)
	}
	if agent.GetStatus() != StatusStarting {
		t.Errorf("status = %s, want starting", agent.GetStatus())
	}
}

func TestNewAgentMissingID(t *testing.T) {
	_, err := NewAgent(&Config{})
	if err == nil {
		t.Error("expected error for missing ID")
	}
}

func TestAgentStartStop(t *testing.T) {
	cfg := &Config{
		ID:       "node-2",
		Region:   "us-west-1",
		SwimBindAddr: "127.0.0.1",
		SwimBindPort: 7947,
		WgListenPort: 51821,
		WgPrivateKey: testKey(),
		WgNoOp:       true,
		DiscoveryTTL: 30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
	}

	agent, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	if err := agent.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	if agent.GetStatus() != StatusHealthy {
		t.Errorf("status = %s, want healthy", agent.GetStatus())
	}

	if err := agent.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestAgentDoubleStart(t *testing.T) {
	cfg := &Config{
		ID:       "node-3",
		SwimBindAddr: "127.0.0.1",
		SwimBindPort: 7948,
		WgListenPort: 51822,
		WgPrivateKey: testKey(),
		WgNoOp:       true,
		DiscoveryTTL: 30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
	}

	agent, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	defer agent.Stop()

	if err := agent.Start(); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := agent.Start(); err == nil {
		t.Error("double start should fail")
	}
}
