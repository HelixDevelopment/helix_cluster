// Command helix-setup is the cluster setup wizard for Helix Cluster OS.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║        Helix Cluster OS — Setup Wizard                       ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Check prerequisites
	fmt.Println("Checking prerequisites...")
	checks := []struct {
		name string
		cmd  string
		args []string
	}{
		{"Docker", "docker", []string{"--version"}},
		{"Go", "go", []string{"version"}},
		{"Git", "git", []string{"--version"}},
	}

	allOK := true
	for _, check := range checks {
		_, err := exec.LookPath(check.cmd)
		if err != nil {
			fmt.Printf("  ❌ %s: not found\n", check.name)
			allOK = false
		} else {
			fmt.Printf("  ✓ %s: found\n", check.name)
		}
	}

	if !allOK {
		fmt.Println("\nPlease install missing prerequisites and run again.")
		os.Exit(1)
	}

	// Generate config
	dataDir := filepath.Join(os.Getenv("HOME"), ".helix")
	fmt.Printf("\nCreating data directory: %s\n", dataDir)
	_ = os.MkdirAll(dataDir, 0755)

	configPath := filepath.Join(dataDir, "config.yaml")
	configContent := `# Helix Cluster OS Configuration
cluster:
  name: helix-local
  id: ""

services:
  etcd:
    endpoints:
      - localhost:2379
  postgres:
    host: localhost
    port: 5432
    database: helix
  redis:
    host: localhost
    port: 6379

node:
  id: ""
  region: local
  labels:
    tier: T1

log:
  level: info
  format: json
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		fmt.Printf("Failed to write config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Generated config: %s\n", configPath)

	fmt.Println("\n✓ Setup complete!")
	fmt.Println("\nNext steps:")
	fmt.Println("  1. Start infrastructure:  helix_infra up")
	fmt.Println("  2. Start control plane:   helixd")
	fmt.Println("  3. Start node agent:      helix-agent --id node-1")
	fmt.Println("  4. Check status:          curl http://localhost:8081/status")
}
