// Command helix-test is the unified test runner CLI for Helix Cluster OS.
//
// Subcommands:
//
//	dst       — run deterministic simulation tests
//	chaos     — run chaos experiments
//	device    — run device simulation tests
//	snapshot  — manage golden snapshots
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/chaos"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/device"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/snapshot"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "dst":
		cmdDST(os.Args[2:])
	case "chaos":
		cmdChaos(os.Args[2:])
	case "device":
		cmdDevice(os.Args[2:])
	case "snapshot":
		cmdSnapshot(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("helix-test — Helix Cluster OS test runner")
	fmt.Println()
	fmt.Println("Usage: helix-test <command> [args...]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  dst       Run deterministic simulation tests")
	fmt.Println("  chaos     Run chaos experiments")
	fmt.Println("  device    Run device simulation tests")
	fmt.Println("  snapshot  Manage golden snapshots")
}

// cmdDST runs a simple deterministic simulation test.
func cmdDST(args []string) {
	seed := int64(42)
	if len(args) > 0 {
		fmt.Sscanf(args[0], "%d", &seed)
	}

	eng := dst.NewEngine(seed)
	eng.RegisterNode("node-1", "10.0.0.1")
	eng.RegisterNode("node-2", "10.0.0.2")
	eng.RegisterNode("node-3", "10.0.0.3")

	msgCount := 0
	eng.RegisterHandler("message", func(e *dst.Event) {
		msgCount++
	})

	eng.ScheduleEvent("heartbeat", "ping", 10)
	eng.ScheduleEvent("heartbeat", "ping", 20)
	eng.ScheduleEvent("heartbeat", "ping", 30)

	processed := eng.Run(100)
	fmt.Printf("DST seed=%d nodes=%d events=%d messages=%d\n",
		eng.Seed(), eng.NodeCount(), processed, msgCount)
}

// cmdChaos runs a chaos experiment with sample faults.
func cmdChaos(args []string) {
	runner := chaos.NewChaosRunner()

	partition := &chaos.NetworkPartition{Nodes: []string{"node-1", "node-2"}}
	crash := &chaos.NodeCrash{NodeID: "node-3"}

	runner.AddFault(partition)
	runner.AddFault(crash)

	if err := runner.ApplyAll(); err != nil {
		fmt.Fprintf(os.Stderr, "apply faults: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Chaos faults applied: %v\n", runner.ListFaults())

	if err := runner.RestoreAll(); err != nil {
		fmt.Fprintf(os.Stderr, "restore faults: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Chaos faults restored")
}

// cmdDevice runs device simulation tests using default profiles.
func cmdDevice(args []string) {
	reg := device.NewRegistry()
	profiles := reg.List()
	fmt.Printf("Device simulation: %d profiles loaded\n", len(profiles))
	for _, p := range profiles {
		fmt.Printf("  %s: %d cores, %d GB RAM, %d GPU(s)\n",
			p.Tier, p.CPU.Cores, p.RAM.TotalGB, p.GPU.Count)
	}
}

// cmdSnapshot manages golden snapshots.
func cmdSnapshot(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: helix-test snapshot <create|compare|list|delete> [name] [file]")
		os.Exit(1)
	}

	sub := args[0]
	mgr := snapshot.NewManager("testdata/snapshots")

	switch sub {
	case "create":
		if len(args) < 3 {
			fmt.Println("Usage: helix-test snapshot create <name> <file>")
			os.Exit(1)
		}
		name, file := args[1], args[2]
		data, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read file: %v\n", err)
			os.Exit(1)
		}
		path, err := mgr.Create(name, data, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create snapshot: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Snapshot created: %s\n", path)

	case "compare":
		if len(args) < 3 {
			fmt.Println("Usage: helix-test snapshot compare <name> <file>")
			os.Exit(1)
		}
		name, file := args[1], args[2]
		data, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read file: %v\n", err)
			os.Exit(1)
		}
		if err := mgr.Compare(name, data); err != nil {
			fmt.Fprintf(os.Stderr, "compare failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Snapshot matches")

	case "list":
		names, err := mgr.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "list: %v\n", err)
			os.Exit(1)
		}
		if len(names) == 0 {
			fmt.Println("No snapshots")
			return
		}
		fmt.Println("Snapshots:")
		for _, n := range names {
			fmt.Printf("  %s\n", n)
		}

	case "delete":
		if len(args) < 2 {
			fmt.Println("Usage: helix-test snapshot delete <name>")
			os.Exit(1)
		}
		if err := mgr.Delete(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "delete: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Snapshot deleted")

	default:
		fmt.Fprintf(os.Stderr, "Unknown snapshot subcommand: %s\n", sub)
		os.Exit(1)
	}
}

// normalizeKey converts a snapshot name to a safe file path.
func normalizeKey(name string) string {
	return filepath.Clean(strings.ReplaceAll(name, "..", "_"))
}
