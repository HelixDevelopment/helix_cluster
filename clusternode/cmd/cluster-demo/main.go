// Command cluster-demo brings up a real 3-node Helix cluster IN-PROCESS and
// prints a readable, scripted account of what happens: the discovery overlay
// forming and a peer being discovered registry-free via the DHT, the raft leader
// election converging to exactly one leader, each node's real hardware
// capabilities, and a message routed end-to-end through a node's gateway.
//
// It composes the proven building blocks via the clusternode package; it adds no
// new mechanism. Run it with:
//
//	go run ./cmd/cluster-demo
//
// Everything here exercises the REAL components (libp2p DHT, embedded raft,
// hwinventory probe, the gateway router) — there are no mocks.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/HelixDevelopment/helix_cluster/clusternode"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cluster-demo: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ids := []string{"node-a", "node-b", "node-c"}
	fmt.Println("=== Helix cluster-demo: 3-node in-process bring-up ===")
	fmt.Printf("composing nodes: %v\n\n", ids)

	cluster, err := clusternode.NewNodeCluster(ids...)
	if err != nil {
		return err
	}
	defer func() { _ = cluster.Stop() }()

	// --- 1. Bring the cluster up: discovery hosts + gateways + raft group. ----
	fmt.Println("[1/4] starting agents (discovery hosts, gateways) and seeding the DHT...")
	if err := cluster.Start(ctx); err != nil {
		return err
	}
	for _, a := range cluster.Agents() {
		fmt.Printf("      %s discovery peer-id=%s\n", a.ID(), a.Discovery().ID())
	}
	if err := cluster.WaitForDiscovery(ctx, 30*time.Second); err != nil {
		return err
	}
	fmt.Println("      discovery overlay formed (every node's DHT routing table is populated)")

	// --- 2. Registry-free discovery: node-b finds node-c purely via the DHT. --
	// The overlay is a star (node-a is the anchor); node-b and node-c only ever
	// dialled the anchor, never each other, so node-b resolving node-c is genuine
	// registry-free Kademlia discovery.
	fmt.Println("\n[2/4] registry-free peer discovery via Kademlia DHT...")
	resolved, err := cluster.DiscoverPeer(ctx, "node-b", "node-c")
	if err != nil {
		return err
	}
	fmt.Printf("      node-b discovered node-c (peer-id=%s) with %d address(es) — purely via the DHT, no registry\n",
		resolved.ID, len(resolved.Addrs))

	// --- 3. Real raft leader election: exactly one leader. --------------------
	fmt.Println("\n[3/4] raft leader election (embedded hashicorp/raft consensus group)...")
	leader, err := cluster.WaitForLeader(30 * time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("      elected leader: %s (exactly %d leader across %d nodes)\n",
		leader.ID(), cluster.Election().CountLeaders(), len(cluster.Agents()))
	for _, a := range cluster.Agents() {
		role := "follower"
		if a.IsLeader() {
			role = "LEADER"
		}
		fmt.Printf("      %s -> %s (sees leader=%q)\n", a.ID(), role, a.Election().LeaderID())
	}

	// --- 4. Per-node capabilities + a routed message. -------------------------
	fmt.Println("\n[4/4] per-node hardware capabilities (real hwinventory probe)...")
	for _, a := range cluster.Agents() {
		inv, err := a.Capabilities(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("      %s host=%s os=%s/%s cpu=%q cores=%d/%d mem=%.1f GiB gpus=%d npus=%d fpgas=%d\n",
			a.ID(), inv.Host, inv.OS, inv.Arch, inv.CPU.Model,
			inv.CPU.PhysicalCores, inv.CPU.LogicalCores,
			float64(inv.MemoryBytes)/(1024*1024*1024),
			len(inv.GPUs), len(inv.NPUs), len(inv.FPGAs))
	}

	fmt.Println("\n      routing a message node-a -> node-c through the gateway...")
	payload := []byte(fmt.Sprintf("hello-from-node-a@%d", time.Now().UnixNano()))
	got, err := cluster.Route(ctx, "node-a", "node-c", payload)
	if err != nil {
		return err
	}
	fmt.Printf("      node-c received envelope id=%q kind=%s source=%s dest=%s\n",
		got.ID, got.Kind, got.Source, got.Dest)
	fmt.Printf("      payload delivered byte-exact: %v (%q)\n",
		string(got.Payload) == string(payload), string(got.Payload))

	fmt.Println("\n=== cluster-demo complete: discovery + leader election + capabilities + routing all composed ===")
	return nil
}
