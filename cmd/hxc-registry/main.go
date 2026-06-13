// Command hxc-registry is the CLI front end for the Helix Cluster OS HXC item
// registry (pkg/hxcregistry, backed by SQLite). It exposes the registry's CRUD
// operations as subcommands: init, add, list, show, update and next.
//
// The monolithic main() has been split into a testable seam:
//   - run(args, getenv, stdout, stderr) int does ALL the work: it parses the
//     subcommand and flags, validates input, opens the registry, performs the
//     operation and writes results to the supplied writers. It returns a process
//     exit code instead of calling os.Exit, so tests can drive it against a real
//     on-disk registry (t.TempDir) and assert exact stdout/stderr/exit-code.
//
// main() stays thin: it just forwards os.Args/os.Getenv/os.Stdout/os.Stderr to
// run and exits with the returned code.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/hxcregistry"
)

// defaultDBPath is used when HXC_DB is unset.
const defaultDBPath = "data/hxc_registry.db"

// exit codes returned by run; kept small and stable for scripts and tests.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run is the testable entry point. args is the argument list WITHOUT the program
// name (i.e. os.Args[1:]). It never calls os.Exit; it returns the intended
// process exit code. All human-readable output goes to stdout, all diagnostics
// to stderr.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stdout)
		return exitUsage
	}

	dbPath := getenv("HXC_DB")
	if dbPath == "" {
		dbPath = defaultDBPath
	}

	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "init":
		return cmdInit(dbPath, stdout, stderr)
	case "add":
		return cmdAdd(dbPath, rest, stdout, stderr)
	case "list":
		return cmdList(dbPath, rest, stdout, stderr)
	case "show":
		return cmdShow(dbPath, rest, stdout, stderr)
	case "update":
		return cmdUpdate(dbPath, rest, stdout, stderr)
	case "next":
		return cmdNext(dbPath, stdout, stderr)
	case "reconcile":
		return cmdReconcile(dbPath, stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "Unknown command: %s\n", cmd)
		usage(stdout)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `HXC Registry CLI

Usage: hxc-registry <command> [options]

Commands:
  init                Initialize the registry database
  add                 Add a new HXC item
  list [status]       List items (optionally filter by status)
  show <HXC-XXX>      Show item details
  update <HXC-XXX>    Update an item
  next                Show next available HXC ID
  reconcile           Set every item's location to the canonical one for its status

Environment:
  HXC_DB              Path to SQLite database (default: data/hxc_registry.db)

Examples:
  hxc-registry init
  hxc-registry add --id HXC-904 --title "Build Service" --type Feature --priority P0 --phase 4
  hxc-registry list
  hxc-registry list --status "In progress"
  hxc-registry show HXC-904
  hxc-registry update HXC-904 --status Completed --commit abc123
  hxc-registry update HXC-904 --location Issues
  hxc-registry reconcile`)
}

// openRegistry centralises the open/error path so every subcommand reports a
// consistent error and exit code on a bad DB path.
func openRegistry(dbPath string, stderr io.Writer) (*hxcregistry.Registry, int) {
	reg, err := hxcregistry.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return nil, exitError
	}
	return reg, exitOK
}

func cmdInit(dbPath string, stdout, stderr io.Writer) int {
	reg, code := openRegistry(dbPath, stderr)
	if reg == nil {
		return code
	}
	defer reg.Close()
	fmt.Fprintf(stdout, "Registry initialized at %s\n", dbPath)
	return exitOK
}

// parseFlags parses the given flag set against args using a continue-on-error
// policy so a bad flag yields a non-zero exit code instead of killing the whole
// process (the old flag.ExitOnError behaviour was untestable). Parse errors are
// written to stderr by the FlagSet, which we point at stderr.
func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(stderr)
	return fs.Parse(args)
}

func cmdAdd(dbPath string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	id := fs.String("id", "", "HXC ID (auto-generated if empty)")
	title := fs.String("title", "", "Item title")
	itemType := fs.String("type", "Task", "Item type: Bug, Feature, Task, Research, Docs")
	priority := fs.String("priority", "P1", "Priority: P0, P1, P2, P3")
	phase := fs.Int("phase", 0, "Phase number")
	desc := fs.String("description", "", "Description")
	if err := parseFlags(fs, args, stderr); err != nil {
		return exitUsage
	}

	if *title == "" {
		fmt.Fprintln(stderr, "Error: --title is required")
		return exitUsage
	}
	if *desc == "" {
		*desc = *title
	}

	reg, code := openRegistry(dbPath, stderr)
	if reg == nil {
		return code
	}
	defer reg.Close()

	itemID := *id
	if itemID == "" {
		next, err := reg.NextHXCID()
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return exitError
		}
		itemID = next
	}

	item := &hxcregistry.HXCItem{
		HXCID:           itemID,
		Type:            *itemType,
		Status:          "Queued",
		Priority:        *priority,
		Phase:           *phase,
		Title:           *title,
		Description:     *desc,
		CurrentLocation: "Issues",
	}
	if err := reg.CreateItem(item); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "Created %s: %s\n", item.HXCID, item.Title)
	return exitOK
}

func cmdList(dbPath string, args []string, stdout, stderr io.Writer) int {
	status := ""
	// Accept either a positional status ("list Queued") or a --status flag
	// ("list --status Queued") to match the documented usage examples.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		status = args[0]
	} else if len(args) > 0 {
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		statusFlag := fs.String("status", "", "Filter by status")
		if err := parseFlags(fs, args, stderr); err != nil {
			return exitUsage
		}
		status = *statusFlag
	}

	reg, code := openRegistry(dbPath, stderr)
	if reg == nil {
		return code
	}
	defer reg.Close()

	items, err := reg.ListItems(status)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitError
	}

	fmt.Fprintf(stdout, "%-10s %-12s %-10s %-8s %-6s %s\n", "HXC-ID", "Status", "Type", "Priority", "Phase", "Title")
	fmt.Fprintln(stdout, strings.Repeat("-", 80))
	for _, item := range items {
		fmt.Fprintf(stdout, "%-10s %-12s %-10s %-8s %-6d %s\n",
			item.HXCID, item.Status, item.Type, item.Priority, item.Phase, item.Title)
	}
	fmt.Fprintf(stdout, "\nTotal: %d items\n", len(items))
	return exitOK
}

func cmdShow(dbPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "Error: HXC-ID required")
		return exitUsage
	}
	id := args[0]

	reg, code := openRegistry(dbPath, stderr)
	if reg == nil {
		return code
	}
	defer reg.Close()

	item, err := reg.GetItem(id)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitError
	}

	fmt.Fprintf(stdout, "HXC ID:      %s\n", item.HXCID)
	fmt.Fprintf(stdout, "Title:       %s\n", item.Title)
	fmt.Fprintf(stdout, "Status:      %s\n", item.Status)
	fmt.Fprintf(stdout, "Type:        %s\n", item.Type)
	fmt.Fprintf(stdout, "Priority:    %s\n", item.Priority)
	fmt.Fprintf(stdout, "Phase:       %d\n", item.Phase)
	fmt.Fprintf(stdout, "Location:    %s\n", item.CurrentLocation)
	fmt.Fprintf(stdout, "Description: %s\n", item.Description)
	if item.CommitSHA != "" {
		fmt.Fprintf(stdout, "Commit:      %s\n", item.CommitSHA)
	}
	fmt.Fprintf(stdout, "Created:     %s\n", item.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(stdout, "Modified:    %s\n", item.LastModified.Format("2006-01-02 15:04:05"))
	return exitOK
}

func cmdUpdate(dbPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "Error: HXC-ID required")
		return exitUsage
	}
	id := args[0]

	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	status := fs.String("status", "", "New status")
	commit := fs.String("commit", "", "Commit SHA")
	priority := fs.String("priority", "", "New priority")
	location := fs.String("location", "", "Document location: Issues or Fixed (overrides the auto-derived location)")
	if err := parseFlags(fs, args[1:], stderr); err != nil {
		return exitUsage
	}

	// Validate -location up front so a bad value is a usage error, not a DB
	// CHECK-constraint error surfaced later. Empty means "not set".
	if *location != "" && *location != hxcregistry.LocationIssues && *location != hxcregistry.LocationFixed {
		fmt.Fprintf(stderr, "Error: invalid -location %q (want %s or %s)\n",
			*location, hxcregistry.LocationIssues, hxcregistry.LocationFixed)
		return exitUsage
	}

	reg, code := openRegistry(dbPath, stderr)
	if reg == nil {
		return code
	}
	defer reg.Close()

	item, err := reg.GetItem(id)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitError
	}

	if *status != "" {
		item.Status = *status
		// Auto-move on status change so the renderer (which splits issues.md vs
		// fixed.md by current_location, not status) files the item correctly: a
		// Completed item must land in Fixed, and a re-opened item back in Issues.
		// An explicit -location below wins, so this only sets the default.
		item.CurrentLocation = hxcregistry.CanonicalLocation(item.Status)
	}
	if *commit != "" {
		item.CommitSHA = *commit
	}
	if *priority != "" {
		item.Priority = *priority
	}
	// Explicit -location overrides the auto-derived location (operator escape
	// hatch). Applied last so it wins over the status-derived default above.
	if *location != "" {
		item.CurrentLocation = *location
	}

	if err := reg.UpdateItem(item); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "Updated %s\n", id)
	return exitOK
}

func cmdNext(dbPath string, stdout, stderr io.Writer) int {
	reg, code := openRegistry(dbPath, stderr)
	if reg == nil {
		return code
	}
	defer reg.Close()

	next, err := reg.NextHXCID()
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitError
	}
	fmt.Fprintln(stdout, next)
	return exitOK
}

// cmdReconcile sets every item's current_location to the canonical location for
// its status (terminal -> Fixed, active -> Issues), backfilling rows whose
// location drifted from their status. It reports how many rows were moved and is
// idempotent: a second run reports "moved 0 item(s)".
func cmdReconcile(dbPath string, stdout, stderr io.Writer) int {
	reg, code := openRegistry(dbPath, stderr)
	if reg == nil {
		return code
	}
	defer reg.Close()

	moved, err := reg.Reconcile()
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "Reconciled: moved %d item(s) to their canonical location\n", moved)
	return exitOK
}
