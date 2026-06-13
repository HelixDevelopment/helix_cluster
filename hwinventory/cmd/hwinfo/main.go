// Command hwinfo prints the unified host capability report for the current
// machine: real CPU, total memory, and the detected GPUs, NPUs and FPGAs.
//
// It is a thin, runnable front-end over hwinventory.Collect — running
//
//	go run ./cmd/hwinfo
//
// from the hwinventory module produces the real host inventory as indented
// JSON. No mocks: every field is measured on the host via the per-OS probes
// and detectors (see CLAUDE-1 / CLAUDE-2). The default (and only) output mode
// is JSON, selectable explicitly with -json.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/HelixDevelopment/helix_cluster/hwinventory"
)

func main() {
	// -json is the default output format; it is accepted explicitly so the
	// flag exists for callers/scripts, and leaves room for future formats.
	jsonOut := flag.Bool("json", true, "print the host capability report as indented JSON")
	timeout := flag.Duration("timeout", 30*time.Second, "maximum time to spend probing the host")
	flag.Parse()

	if err := run(*jsonOut, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "hwinfo: %v\n", err)
		os.Exit(1)
	}
}

func run(jsonOut bool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	inv, err := hwinventory.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect inventory: %w", err)
	}

	// jsonOut is the only supported mode today; it is always true by default.
	// Keeping the branch explicit documents intent and guards future formats.
	if !jsonOut {
		return fmt.Errorf("no non-JSON output format is implemented; pass -json")
	}

	raw, err := inv.JSON()
	if err != nil {
		return fmt.Errorf("render inventory JSON: %w", err)
	}
	fmt.Println(string(raw))
	return nil
}
