// Command helix-snapshot is a standalone CLI for managing golden snapshots.
//
// Usage:
//
//	helix-snapshot [-dir <dir>] <subcommand> [args...]
//
// Subcommands:
//
//	create <name> <file>   Read file, write golden snapshot, print path to stdout.
//	restore <name>         Write snapshot bytes to stdout.
//	compare <name> <file>  Compare file against snapshot; exit 1 + stderr on mismatch.
//	list                   Print snapshot names one per line, or "No snapshots".
//	delete <name>          Delete snapshot; exit 1 on error.
//
// The snapshot base directory is resolved (in order of precedence):
//  1. -dir flag value
//  2. HELIX_SNAPSHOT_DIR environment variable
//  3. "testdata/snapshots" (default)
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/snapshot"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches the subcommand in args, writes output to stdout and errors to
// stderr, and returns the process exit code. It never calls os.Exit so it is
// fully testable.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("helix-snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)

	defaultDir := os.Getenv("HELIX_SNAPSHOT_DIR")
	if defaultDir == "" {
		defaultDir = "testdata/snapshots"
	}
	dir := fs.String("dir", defaultDir, "snapshot base directory")

	if err := fs.Parse(args); err != nil {
		// flag already wrote the error to stderr
		usageTo(stderr)
		return 1
	}

	rest := fs.Args()
	if len(rest) < 1 {
		usageTo(stderr)
		return 1
	}

	sub := rest[0]
	subArgs := rest[1:]
	mgr := snapshot.NewManager(*dir)

	switch sub {
	case "create":
		return cmdCreate(subArgs, mgr, stdout, stderr)
	case "restore":
		return cmdRestore(subArgs, mgr, stdout, stderr)
	case "compare":
		return cmdCompare(subArgs, mgr, stdout, stderr)
	case "list":
		return cmdList(mgr, stdout, stderr)
	case "delete":
		return cmdDelete(subArgs, mgr, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand: %q\n", sub)
		usageTo(stderr)
		return 1
	}
}

func usageTo(w io.Writer) {
	fmt.Fprintln(w, "helix-snapshot — golden snapshot manager")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: helix-snapshot [-dir <dir>] <subcommand> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  create  <name> <file>   Create (or update) a golden snapshot from a file")
	fmt.Fprintln(w, "  restore <name>          Write snapshot bytes to stdout")
	fmt.Fprintln(w, "  compare <name> <file>   Compare file against snapshot; exit 1 on mismatch")
	fmt.Fprintln(w, "  list                    List all snapshots (one per line)")
	fmt.Fprintln(w, "  delete  <name>          Delete a snapshot")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -dir <dir>   Snapshot base directory (default: HELIX_SNAPSHOT_DIR or testdata/snapshots)")
}

// cmdCreate reads <file>, calls Manager.Create with update=true, and prints the
// stored path. Exits 0 on success.
func cmdCreate(args []string, mgr *snapshot.Manager, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: helix-snapshot create <name> <file>")
		return 1
	}
	name, file := args[0], args[1]

	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "read file %q: %v\n", file, err)
		return 1
	}

	path, err := mgr.Create(name, data, true)
	if err != nil {
		fmt.Fprintf(stderr, "create snapshot: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, path)
	return 0
}

// cmdRestore reads the named snapshot and writes its bytes verbatim to stdout.
// Exits 0 on success.
func cmdRestore(args []string, mgr *snapshot.Manager, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: helix-snapshot restore <name>")
		return 1
	}
	name := args[0]

	data, err := mgr.Restore(name)
	if err != nil {
		fmt.Fprintf(stderr, "restore snapshot: %v\n", err)
		return 1
	}

	if _, err := stdout.Write(data); err != nil {
		fmt.Fprintf(stderr, "write stdout: %v\n", err)
		return 1
	}
	return 0
}

// cmdCompare reads <file> and calls Manager.Compare. Exits 0 if they match,
// exits 1 and writes the mismatch description to stderr otherwise.
func cmdCompare(args []string, mgr *snapshot.Manager, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: helix-snapshot compare <name> <file>")
		return 1
	}
	name, file := args[0], args[1]

	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "read file %q: %v\n", file, err)
		return 1
	}

	if err := mgr.Compare(name, data); err != nil {
		fmt.Fprintf(stderr, "mismatch: %v\n", err)
		return 1
	}

	return 0
}

// cmdList prints one snapshot name per line, or "No snapshots" when empty.
// Exits 0 on success.
func cmdList(mgr *snapshot.Manager, stdout, stderr io.Writer) int {
	names, err := mgr.List()
	if err != nil {
		fmt.Fprintf(stderr, "list: %v\n", err)
		return 1
	}

	if len(names) == 0 {
		fmt.Fprintln(stdout, "No snapshots")
		return 0
	}

	for _, n := range names {
		fmt.Fprintln(stdout, n)
	}
	return 0
}

// cmdDelete removes the named snapshot. Exits 0 on success, 1 on error.
func cmdDelete(args []string, mgr *snapshot.Manager, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: helix-snapshot delete <name>")
		return 1
	}
	name := args[0]

	if err := mgr.Delete(name); err != nil {
		fmt.Fprintf(stderr, "delete snapshot: %v\n", err)
		return 1
	}

	return 0
}
