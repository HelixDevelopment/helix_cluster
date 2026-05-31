package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/HelixDevelopment/helix_cluster/pkg/infra"
)

// loadOrchestrator creates an Orchestrator from config loaded at configPath.
// An empty configPath uses built-in defaults. The returned Orchestrator is the
// simulation by default (see pkg/infra); callers MAY assert Simulated().
func loadOrchestrator(configPath string) (infra.Orchestrator, *infra.InfraConfig, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	return infra.NewOrchestrator(cfg), cfg, nil
}

// loadConfig loads the InfraConfig with defaults, optionally overlaying values
// from a JSON file at configPath. An empty configPath returns defaults. A
// non-empty path that does not exist or is malformed is a hard error (so a
// typo'd --config is never silently ignored), and the loaded config is
// validated before being returned.
func loadConfig(configPath string) (*infra.InfraConfig, error) {
	cfg := infra.DefaultConfig()
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("read config %q: %w", configPath, err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %q: %w", configPath, err)
		}
		cfg.ConfigPath = configPath
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateConfig enforces the invariants the orchestrator depends on.
func validateConfig(cfg *infra.InfraConfig) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	if strings.TrimSpace(cfg.ProjectName) == "" {
		return fmt.Errorf("project_name must not be empty")
	}
	if cfg.DefaultTimeout <= 0 {
		return fmt.Errorf("default_timeout must be positive, got %s", cfg.DefaultTimeout)
	}
	if len(cfg.Services) == 0 {
		return fmt.Errorf("services list must not be empty")
	}
	for _, s := range cfg.Services {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("services list contains an empty entry")
		}
	}
	return nil
}

// renderTable returns a formatted table with headers and rows as a string.
// Column widths fit the widest cell (header or body) plus two spaces of
// padding. Extracting this as a pure function makes the exact layout testable.
func renderTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var b strings.Builder
	for i, h := range headers {
		b.WriteString(padRight(h, widths[i]+2))
	}
	b.WriteByte('\n')
	for i := range headers {
		b.WriteString(strings.Repeat("-", widths[i]+2))
	}
	b.WriteByte('\n')
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				b.WriteString(padRight(cell, widths[i]+2))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// printTable writes a formatted table to w.
func printTable(w io.Writer, headers []string, rows [][]string) {
	fmt.Fprint(w, renderTable(headers, rows))
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// printJSON writes v as indented JSON to w.
func printJSON(w io.Writer, v interface{}) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// setupSignalHandler sets up a graceful shutdown on SIGINT/SIGTERM.
func setupSignalHandler(cancel context.CancelFunc) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		if cancel != nil {
			cancel()
		}
	}()
}
