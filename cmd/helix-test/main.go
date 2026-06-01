// Command helix-test is the unified test runner CLI for Helix Cluster OS.
//
// Subcommands:
//
//	dst       — run deterministic simulation tests
//	chaos     — run chaos experiments
//	device    — run device simulation tests
//	snapshot  — manage golden snapshots
//
// The monolithic main() has been split into a testable seam:
//   - run(args, stdout, stderr) int: dispatches a subcommand, writing all
//     output to the supplied writers and returning a process exit code. It
//     performs argument validation and propagates errors as non-zero codes
//     instead of calling os.Exit from deep inside command logic.
//   - each cmd* helper returns an int exit code and writes to injected
//     io.Writers, so behavior (stdout, exit code, errors) is fully assertable.
//
// main() stays thin: call run with the real args/stdio and exit with its code.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/chaos"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/device"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/snapshot"
)

// defaultSnapshotDir is the base directory used for golden snapshots when the
// HELIX_TEST_SNAPSHOT_DIR environment override is unset. It is read relative to
// the process working directory, matching the original CLI behavior.
const defaultSnapshotDir = "testdata/snapshots"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches the subcommand named by args[0] (if any) and returns the
// process exit code. All human-facing output is written to stdout; error
// diagnostics go to stderr. run never calls os.Exit, so it is unit-testable.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stdout)
		return 1
	}

	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "dst":
		return cmdDST(rest, stdout, stderr)
	case "chaos":
		return cmdChaos(rest, stdout, stderr)
	case "device":
		return cmdDevice(rest, stdout, stderr)
	case "snapshot":
		return cmdSnapshot(rest, stdout, stderr)
	case "challenge":
		return cmdChallenge(rest, stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "Unknown command: %s\n", cmd)
		usage(stdout)
		return 1
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "helix-test — Helix Cluster OS test runner")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: helix-test <command> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  dst        Run deterministic simulation tests")
	fmt.Fprintln(w, "  chaos      Run chaos experiments")
	fmt.Fprintln(w, "  device     Run device simulation tests")
	fmt.Fprintln(w, "  snapshot   Manage golden snapshots")
	fmt.Fprintln(w, "  challenge  Run a HelixQA challenge probe against a live endpoint")
}

// cmdDST runs a deterministic simulation test. The optional first arg is the
// PRNG seed; a non-integer seed is reported as an error (exit 1) rather than
// silently defaulting.
func cmdDST(args []string, stdout, stderr io.Writer) int {
	seed := int64(42)
	if len(args) > 0 {
		if _, err := fmt.Sscanf(args[0], "%d", &seed); err != nil {
			fmt.Fprintf(stderr, "invalid seed %q: %v\n", args[0], err)
			return 1
		}
	}

	eng := dst.NewEngine(seed)
	eng.RegisterNode("node-1", "10.0.0.1")
	eng.RegisterNode("node-2", "10.0.0.2")
	eng.RegisterNode("node-3", "10.0.0.3")

	msgCount := 0
	eng.RegisterHandler("heartbeat", func(e *dst.Event) {
		msgCount++
	})

	eng.ScheduleEvent("heartbeat", "ping", 10)
	eng.ScheduleEvent("heartbeat", "ping", 20)
	eng.ScheduleEvent("heartbeat", "ping", 30)

	processed := eng.Run(100)
	fmt.Fprintf(stdout, "DST seed=%d nodes=%d events=%d messages=%d\n",
		eng.Seed(), eng.NodeCount(), processed, msgCount)
	return 0
}

// cmdChaos runs a chaos experiment with sample faults, applying and then
// restoring them. Apply/restore failures are reported and exit non-zero.
func cmdChaos(args []string, stdout, stderr io.Writer) int {
	runner := chaos.NewChaosRunner()

	partition := &chaos.NetworkPartition{Nodes: []string{"node-1", "node-2"}}
	crash := &chaos.NodeCrash{NodeID: "node-3"}

	runner.AddFault(partition)
	runner.AddFault(crash)

	if err := runner.ApplyAll(); err != nil {
		fmt.Fprintf(stderr, "apply faults: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Chaos faults applied: %v\n", runner.ListFaults())

	if err := runner.RestoreAll(); err != nil {
		fmt.Fprintf(stderr, "restore faults: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "Chaos faults restored")
	return 0
}

// cmdDevice runs device simulation tests using default profiles, printing each
// loaded profile's headline hardware specs.
func cmdDevice(args []string, stdout, stderr io.Writer) int {
	reg := device.NewRegistry()
	profiles := reg.List()
	fmt.Fprintf(stdout, "Device simulation: %d profiles loaded\n", len(profiles))
	for _, p := range profiles {
		fmt.Fprintf(stdout, "  %s: %d cores, %d GB RAM, %d GPU(s)\n",
			p.Tier, p.CPU.Cores, p.RAM.TotalGB, p.GPU.Count)
	}
	return 0
}

// snapshotDir resolves the snapshot base directory, honoring the
// HELIX_TEST_SNAPSHOT_DIR override so callers (and tests) can redirect I/O away
// from the repo working tree.
func snapshotDir() string {
	if d := os.Getenv("HELIX_TEST_SNAPSHOT_DIR"); d != "" {
		return d
	}
	return defaultSnapshotDir
}

// cmdSnapshot manages golden snapshots via the snapshot.Manager rooted at
// snapshotDir(). It validates required positional args per subcommand and
// returns a non-zero exit code on usage errors or underlying I/O failures.
func cmdSnapshot(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stdout, "Usage: helix-test snapshot <create|compare|list|delete> [name] [file]")
		return 1
	}

	sub := args[0]
	mgr := snapshot.NewManager(snapshotDir())

	switch sub {
	case "create":
		if len(args) < 3 {
			fmt.Fprintln(stdout, "Usage: helix-test snapshot create <name> <file>")
			return 1
		}
		name, file := args[1], args[2]
		data, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(stderr, "read file: %v\n", err)
			return 1
		}
		path, err := mgr.Create(name, data, true)
		if err != nil {
			fmt.Fprintf(stderr, "create snapshot: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Snapshot created: %s\n", path)
		return 0

	case "compare":
		if len(args) < 3 {
			fmt.Fprintln(stdout, "Usage: helix-test snapshot compare <name> <file>")
			return 1
		}
		name, file := args[1], args[2]
		data, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(stderr, "read file: %v\n", err)
			return 1
		}
		if err := mgr.Compare(name, data); err != nil {
			fmt.Fprintf(stderr, "compare failed: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Snapshot matches")
		return 0

	case "list":
		names, err := mgr.List()
		if err != nil {
			fmt.Fprintf(stderr, "list: %v\n", err)
			return 1
		}
		if len(names) == 0 {
			fmt.Fprintln(stdout, "No snapshots")
			return 0
		}
		fmt.Fprintln(stdout, "Snapshots:")
		for _, n := range names {
			fmt.Fprintf(stdout, "  %s\n", n)
		}
		return 0

	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(stdout, "Usage: helix-test snapshot delete <name>")
			return 1
		}
		if err := mgr.Delete(args[1]); err != nil {
			fmt.Fprintf(stderr, "delete: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Snapshot deleted")
		return 0

	default:
		fmt.Fprintf(stderr, "Unknown snapshot subcommand: %s\n", sub)
		return 1
	}
}
