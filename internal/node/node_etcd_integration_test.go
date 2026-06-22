//go:build integration

// Package node — HXC-1210 integration test.
//
// Proves that two Agent instances pointing at the same real etcd endpoint can
// see each other via Lookup("helix-node"), and that B's resource metadata
// (containing a per-run UUID) is visible in A's lookup result. Contrast: the
// in-memory path returns only the registering agent itself (proven in the
// hermetic unit tier).
//
// Uses digital.vasic.containers/pkg/brokertest.StartEtcd to boot an ephemeral
// real etcd container. HARD-FAILS when etcd is available but assertions are
// wrong. t.Skip is used only when the container runtime is genuinely absent.
package node

import (
	"context"
	"fmt"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
)

// TestIntegration_HXC1210_TwoAgents_EtcdBackend_CrossVisibility_WithResourceMetadata
// proves the closure criteria for HXC-1210:
//
//  1. Agent A and Agent B are started with EtcdEndpoints pointing at real etcd.
//  2. After B starts, A's Lookup("helix-node") returns both A and B.
//  3. B's discovery Instance carries its resource metadata, including a per-run
//     UUID embedded in b_run_uuid — a value a stale or fabricated response
//     cannot reproduce.
//  4. The in-memory path would return only A (proven in unit tests), so
//     seeing B proves the etcd backend is genuinely in use.
//
// §1.1 mutation anchor: forcing NewInMemoryBackend even when EtcdEndpoints is
// set causes A's Lookup to return only A. The foundB assertion then fails
// with "agent B not found in A's Lookup".
func TestIntegration_HXC1210_TwoAgents_EtcdBackend_CrossVisibility_WithResourceMetadata(t *testing.T) {
	// ── Boot real etcd ────────────────────────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	etcdEndpoint, stop, err := brokertest.StartEtcd(ctx)
	if err != nil {
		t.Skipf("no container runtime available, skipping HXC-1210 integration test: %v", err)
		return
	}
	defer stop()
	t.Logf("HXC-1210 integration: real etcd at %s", etcdEndpoint)

	// ── Per-run UUID embedded in B's resource metadata ────────────────────────
	// This value is unique per test execution; a stale cache or fabricated
	// response cannot reproduce it, satisfying the CLAUDE-1 anti-bluff mandate.
	runUUID := fmt.Sprintf("hxc1210-b-uuid-%d", time.Now().UnixNano())

	// ── Start Agent A ─────────────────────────────────────────────────────────
	cfgA := &Config{
		ID:                   "integ-node-a",
		Region:               "us-east-1",
		Labels:               map[string]string{"role": "agent-a"},
		SwimBindAddr:         "127.0.0.1",
		SwimBindPort:         8100,
		WgListenPort:         52100,
		WgPrivateKey:         testKey(),
		WgNoOp:               true,
		DiscoveryTTL:         30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
		EtcdEndpoints:        []string{etcdEndpoint},
	}
	agentA, err := NewAgent(cfgA)
	if err != nil {
		t.Fatalf("NewAgent(A): %v", err)
	}
	defer agentA.Stop()

	if bt := agentA.BackendType(); bt != "etcd" {
		t.Fatalf("agent A BackendType = %q, want \"etcd\"", bt)
	}

	if err := agentA.Start(); err != nil {
		t.Fatalf("agentA.Start: %v", err)
	}

	// ── Start Agent B with resource capacity carrying the per-run UUID ────────
	cfgB := &Config{
		ID:                   "integ-node-b",
		Region:               "us-east-1",
		Labels:               map[string]string{"role": "agent-b", "b_run_uuid": runUUID},
		SwimBindAddr:         "127.0.0.1",
		SwimBindPort:         8101,
		WgListenPort:         52101,
		WgPrivateKey:         testKey(),
		WgNoOp:               true,
		DiscoveryTTL:         30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
		EtcdEndpoints:        []string{etcdEndpoint},
	}
	agentB, err := NewAgent(cfgB)
	if err != nil {
		t.Fatalf("NewAgent(B): %v", err)
	}
	defer agentB.Stop()

	// Attach capacity so B's metadata carries resource fields.
	agentB.Capacity = &resources.NodeResources{
		NodeID: cfgB.ID,
		CPU:    resources.CPUInfo{Cores: 16},
		Memory: resources.MemoryInfo{TotalKB: 32_000_000},
		GPU:    resources.GPUInfo{Count: 4, Model: "TestGPU-HXC1210"},
	}

	if bt := agentB.BackendType(); bt != "etcd" {
		t.Fatalf("agent B BackendType = %q, want \"etcd\"", bt)
	}

	if err := agentB.Start(); err != nil {
		t.Fatalf("agentB.Start: %v", err)
	}

	// ── Poll: A's Lookup must return both A and B ─────────────────────────────
	lookupCtx, lookupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer lookupCancel()

	var (
		foundA       bool
		foundB       bool
		bMetaUUID    string
		bMetaCores   string
	)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		insts, err := agentA.registry.Lookup(lookupCtx, discoveryNodeService)
		if err != nil {
			t.Logf("Lookup transient error: %v", err)
			time.Sleep(300 * time.Millisecond)
			continue
		}
		foundA = false
		foundB = false
		for _, inst := range insts {
			switch inst.ID {
			case cfgA.ID:
				foundA = true
			case cfgB.ID:
				foundB = true
				bMetaUUID = inst.Metadata["b_run_uuid"]
				bMetaCores = inst.Metadata["cpu_cores"]
			}
		}
		if foundA && foundB {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	// ── Sink-side assertions ──────────────────────────────────────────────────

	if !foundA {
		t.Error("HXC-1210: agent A not found in its own Lookup result")
	}
	if !foundB {
		t.Errorf("HXC-1210: agent B (id=%s) not found in A's Lookup — mutation: "+
			"forcing NewInMemoryBackend even with EtcdEndpoints set would cause this failure",
			cfgB.ID)
	}

	// B's per-run UUID must survive the etcd round-trip.
	if bMetaUUID != runUUID {
		t.Errorf("HXC-1210: B's b_run_uuid in metadata = %q, want %q (per-run UUID not preserved)", bMetaUUID, runUUID)
	}

	// B's resource metadata (cpu_cores) must be present.
	if bMetaCores == "" {
		t.Error("HXC-1210: B's cpu_cores metadata key absent — resource capacity not attached to Instance")
	}

	t.Logf("HXC-1210 integration PASS: foundA=%v foundB=%v b_run_uuid=%q cpu_cores=%q",
		foundA, foundB, bMetaUUID, bMetaCores)
}

// TestIntegration_HXC1210_InMemory_Returns_OnlySelf_Contrast proves that the
// same Lookup call on an in-memory-backed agent returns only the agent itself
// (not the other agent), contrasting the etcd path above.
func TestIntegration_HXC1210_InMemory_Returns_OnlySelf_Contrast(t *testing.T) {
	// No etcd needed — this is a pure in-memory contrast test.
	cfgA := &Config{
		ID:                   "contrast-inmem-a",
		Region:               "eu-west-1",
		SwimBindAddr:         "127.0.0.1",
		SwimBindPort:         8102,
		WgListenPort:         52102,
		WgPrivateKey:         testKey(),
		WgNoOp:               true,
		DiscoveryTTL:         30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
		// No EtcdEndpoints → in-memory
	}
	cfgB := &Config{
		ID:                   "contrast-inmem-b",
		Region:               "eu-west-1",
		SwimBindAddr:         "127.0.0.1",
		SwimBindPort:         8103,
		WgListenPort:         52103,
		WgPrivateKey:         testKey(),
		WgNoOp:               true,
		DiscoveryTTL:         30 * time.Second,
		ResourcePollInterval: 30 * time.Second,
		// No EtcdEndpoints → in-memory
	}

	agentA, err := NewAgent(cfgA)
	if err != nil {
		t.Fatalf("NewAgent(A): %v", err)
	}
	defer agentA.Stop()

	agentB, err := NewAgent(cfgB)
	if err != nil {
		t.Fatalf("NewAgent(B): %v", err)
	}
	defer agentB.Stop()

	if err := agentA.Start(); err != nil {
		t.Fatalf("agentA.Start: %v", err)
	}
	if err := agentB.Start(); err != nil {
		t.Fatalf("agentB.Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	instsA, err := agentA.registry.Lookup(ctx, discoveryNodeService)
	if err != nil {
		t.Fatalf("agentA Lookup: %v", err)
	}

	// In-memory: A can only see itself.
	for _, inst := range instsA {
		if inst.ID == cfgB.ID {
			t.Errorf("in-memory contrast: A's Lookup unexpectedly found B (id=%s) — "+
				"in-memory backends are per-agent, not shared", cfgB.ID)
		}
	}
	t.Logf("HXC-1210 in-memory contrast PASS: A sees %d instance(s), B is absent", len(instsA))
}
