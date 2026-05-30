package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/HelixDevelopment/helix_cluster/pkg/infra"
)

// loadOrchestrator creates an InfraOrchestrator from config.
func loadOrchestrator() (*infra.InfraOrchestrator, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return infra.NewOrchestrator(cfg), nil
}

// loadConfig loads the InfraConfig with defaults, optionally from a file path.
func loadConfig() (*infra.InfraConfig, error) {
	cfg := infra.DefaultConfig()
	return cfg, nil
}

// printTable prints a formatted table with headers and rows.
func printTable(headers []string, rows [][]string) {
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

	// Print header
	for i, h := range headers {
		fmt.Printf("%s", padRight(h, widths[i]+2))
	}
	fmt.Println()
	for i := range headers {
		fmt.Printf("%s", strings.Repeat("-", widths[i]+2))
	}
	fmt.Println()

	// Print rows
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				fmt.Printf("%s", padRight(cell, widths[i]+2))
			}
		}
		fmt.Println()
	}
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// printJSON prints v as formatted JSON.
func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
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
