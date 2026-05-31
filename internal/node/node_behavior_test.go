package node

import (
	"testing"
	"time"
)

func baseCfg(id string, swimPort, wgPort int) *Config {
	return &Config{
		ID:           id,
		SwimBindAddr: "127.0.0.1",
		SwimBindPort: swimPort,
		WgListenPort: wgPort,
		WgPrivateKey: testKey(),
		WgNoOp:       true,
		DiscoveryTTL: 30 * time.Second,
	}
}

// TestAgentUsesConfiguredPollInterval proves the resource poll interval from
// Config is actually wired into the agent instead of being ignored.
//
// Mutation that breaks this: in NewAgent set `a.pollInterval = 30 * time.Second`
// unconditionally (the original behavior, which hardcoded the ticker period and
// discarded cfg.ResourcePollInterval). The assertion on 5s then fails.
func TestAgentUsesConfiguredPollInterval(t *testing.T) {
	cfg := baseCfg("poll-1", 7960, 51840)
	cfg.ResourcePollInterval = 5 * time.Second

	agent, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	defer agent.Stop()

	if agent.pollInterval != 5*time.Second {
		t.Errorf("pollInterval = %v, want 5s", agent.pollInterval)
	}
}

// TestAgentPollIntervalDefault proves an unset (zero) interval falls back to a
// safe non-zero default, so time.NewTicker never panics on a zero duration.
//
// Mutation that breaks this: remove the `if a.pollInterval <= 0` fallback in
// NewAgent. pollInterval stays 0 and the assertion fails (and a real ticker
// would panic).
func TestAgentPollIntervalDefault(t *testing.T) {
	cfg := baseCfg("poll-2", 7961, 51841)
	cfg.ResourcePollInterval = 0

	agent, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	defer agent.Stop()

	if agent.pollInterval <= 0 {
		t.Errorf("pollInterval = %v, want a positive default", agent.pollInterval)
	}
}
