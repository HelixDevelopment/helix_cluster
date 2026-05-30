package main

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandsHaveRunE(t *testing.T) {
	commands := []*cobra.Command{
		upCmd,
		downCmd,
		statusCmd,
		logsCmd,
		healthCmd,
		vmSpawnCmd,
		vmDestroyCmd,
		vmListCmd,
		vmStatusCmd,
		vmSSHCmd,
		vmSimulateFailureCmd,
		vmSimulatePartitionCmd,
		scaleCmd,
		versionCmd,
	}
	for _, cmd := range commands {
		if cmd.RunE == nil && cmd.Run == nil {
			t.Errorf("command %q has no RunE or Run function", cmd.Name())
		}
	}
}

func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadConfig returned nil config")
	}
	if cfg.ProjectName == "" {
		t.Error("expected ProjectName to be set")
	}
}

func TestLoadOrchestrator(t *testing.T) {
	orch, err := loadOrchestrator()
	if err != nil {
		t.Fatalf("loadOrchestrator failed: %v", err)
	}
	if orch == nil {
		t.Fatal("loadOrchestrator returned nil orchestrator")
	}
}

func TestPrintTable(t *testing.T) {
	headers := []string{"A", "B", "C"}
	rows := [][]string{
		{"1", "2", "3"},
		{"4", "5", "6"},
	}
	// Just ensure it doesn't panic.
	printTable(headers, rows)
}

func TestPrintJSON(t *testing.T) {
	data := map[string]string{"key": "value"}
	if err := printJSON(data); err != nil {
		t.Fatalf("printJSON failed: %v", err)
	}
}

func TestSetupSignalHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	setupSignalHandler(cancel)
	cancel()
	<-ctx.Done()
	if ctx.Err() == nil {
		t.Fatal("expected context to be canceled")
	}
}
