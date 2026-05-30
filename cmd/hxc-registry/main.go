package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/hxcregistry"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	dbPath := os.Getenv("HXC_DB")
	if dbPath == "" {
		dbPath = "data/hxc_registry.db"
	}

	cmd := os.Args[1]
	switch cmd {
	case "init":
		cmdInit(dbPath)
	case "add":
		cmdAdd(dbPath, os.Args[2:])
	case "list":
		cmdList(dbPath, os.Args[2:])
	case "show":
		cmdShow(dbPath, os.Args[2:])
	case "update":
		cmdUpdate(dbPath, os.Args[2:])
	case "next":
		cmdNext(dbPath)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`HXC Registry CLI

Usage: hxc-registry <command> [options]

Commands:
  init                Initialize the registry database
  add                 Add a new HXC item
  list [status]       List items (optionally filter by status)
  show <HXC-XXX>      Show item details
  update <HXC-XXX>    Update an item
  next                Show next available HXC ID

Environment:
  HXC_DB              Path to SQLite database (default: data/hxc_registry.db)

Examples:
  hxc-registry init
  hxc-registry add --id HXC-904 --title "Build Service" --type Feature --priority P0 --phase 4
  hxc-registry list
  hxc-registry list --status "In progress"
  hxc-registry show HXC-904
  hxc-registry update HXC-904 --status Completed --commit abc123`)
}

func cmdInit(dbPath string) {
	reg, err := hxcregistry.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer reg.Close()
	fmt.Printf("Registry initialized at %s\n", dbPath)
}

func cmdAdd(dbPath string, args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	id := fs.String("id", "", "HXC ID (auto-generated if empty)")
	title := fs.String("title", "", "Item title")
	itemType := fs.String("type", "Task", "Item type: Bug, Feature, Task, Research, Docs")
	priority := fs.String("priority", "P1", "Priority: P0, P1, P2, P3")
	phase := fs.Int("phase", 0, "Phase number")
	desc := fs.String("description", "", "Description")
	fs.Parse(args)

	if *title == "" {
		fmt.Fprintln(os.Stderr, "Error: --title is required")
		os.Exit(1)
	}
	if *desc == "" {
		*desc = *title
	}

	reg, err := hxcregistry.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer reg.Close()

	if *id == "" {
		*id, err = reg.NextHXCID()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	item := &hxcregistry.HXCItem{
		HXCID:           *id,
		Type:            *itemType,
		Status:          "Queued",
		Priority:        *priority,
		Phase:           *phase,
		Title:           *title,
		Description:     *desc,
		CurrentLocation: "Issues",
	}
	if err := reg.CreateItem(item); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Created %s: %s\n", item.HXCID, item.Title)
}

func cmdList(dbPath string, args []string) {
	status := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		status = args[0]
	}

	reg, err := hxcregistry.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer reg.Close()

	items, err := reg.ListItems(status)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%-10s %-12s %-10s %-8s %-6s %s\n", "HXC-ID", "Status", "Type", "Priority", "Phase", "Title")
	fmt.Println(strings.Repeat("-", 80))
	for _, item := range items {
		fmt.Printf("%-10s %-12s %-10s %-8s %-6d %s\n",
			item.HXCID, item.Status, item.Type, item.Priority, item.Phase, item.Title)
	}
	fmt.Printf("\nTotal: %d items\n", len(items))
}

func cmdShow(dbPath string, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Error: HXC-ID required")
		os.Exit(1)
	}
	id := args[0]

	reg, err := hxcregistry.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer reg.Close()

	item, err := reg.GetItem(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("HXC ID:      %s\n", item.HXCID)
	fmt.Printf("Title:       %s\n", item.Title)
	fmt.Printf("Status:      %s\n", item.Status)
	fmt.Printf("Type:        %s\n", item.Type)
	fmt.Printf("Priority:    %s\n", item.Priority)
	fmt.Printf("Phase:       %d\n", item.Phase)
	fmt.Printf("Location:    %s\n", item.CurrentLocation)
	fmt.Printf("Description: %s\n", item.Description)
	if item.CommitSHA != "" {
		fmt.Printf("Commit:      %s\n", item.CommitSHA)
	}
	fmt.Printf("Created:     %s\n", item.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("Modified:    %s\n", item.LastModified.Format("2006-01-02 15:04:05"))
}

func cmdUpdate(dbPath string, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Error: HXC-ID required")
		os.Exit(1)
	}
	id := args[0]

	fs := flag.NewFlagSet("update", flag.ExitOnError)
	status := fs.String("status", "", "New status")
	commit := fs.String("commit", "", "Commit SHA")
	priority := fs.String("priority", "", "New priority")
	fs.Parse(args[1:])

	reg, err := hxcregistry.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer reg.Close()

	item, err := reg.GetItem(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *status != "" {
		item.Status = *status
	}
	if *commit != "" {
		item.CommitSHA = *commit
	}
	if *priority != "" {
		item.Priority = *priority
	}

	if err := reg.UpdateItem(item); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Updated %s\n", id)
}

func cmdNext(dbPath string) {
	reg, err := hxcregistry.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer reg.Close()

	next, err := reg.NextHXCID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(next)
}
