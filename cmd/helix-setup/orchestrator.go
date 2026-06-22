// Package main — orchestrator.go implements the single-command node onboarding
// orchestrator for HXC-1125. It detects real hardware, generates a typed node
// config, serialises it as YAML, and joins the mesh via an injectable seam.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

// ─── Hardware Detection ───────────────────────────────────────────────────────

// HardwareSummary carries the real hardware characteristics of this host.
type HardwareSummary struct {
	CPUCores int    // runtime.NumCPU() — logical cores visible to the process
	MemoryMB int    // total physical RAM in MiB, read from the OS
	Tier     string // derived capacity tier (T1–T4)
}

// DetectHardware reads real CPU and memory values from the host and derives a
// provisioning tier. It never fabricates values:
//   - CPUCores comes from runtime.NumCPU() — the OS scheduler's view.
//   - MemoryMB comes from a platform-specific read (platformMemoryMB):
//     Linux  → syscall.Sysinfo (kernel data, no shell-out)
//     Darwin → sysctl hw.memsize via os/exec (same path as pkg/resources)
//     other  → runtime.MemStats.Sys lower-bound
func DetectHardware() HardwareSummary {
	cores := runtime.NumCPU()
	memMB := platformMemoryMB() // defined in mem_linux.go / mem_darwin.go / mem_other.go
	tier := deriveTier(cores, memMB)
	return HardwareSummary{
		CPUCores: cores,
		MemoryMB: memMB,
		Tier:     tier,
	}
}

// deriveTier maps (cores, memMB) to one of the four provisioning tiers used
// throughout HelixCluster. The thresholds mirror the device-profile registry
// from pkg/deviceprofile.
//
//	T4 — high-end  : ≥32 cores OR ≥64 GiB RAM
//	T3 — mid-range : ≥16 cores OR ≥32 GiB RAM
//	T2 — entry     : ≥8  cores OR ≥16 GiB RAM
//	T1 — minimal   : everything else
func deriveTier(cores, memMB int) string {
	switch {
	case cores >= 32 || memMB >= 64*1024:
		return "T4"
	case cores >= 16 || memMB >= 32*1024:
		return "T3"
	case cores >= 8 || memMB >= 16*1024:
		return "T2"
	default:
		return "T1"
	}
}

// ─── Node Configuration ───────────────────────────────────────────────────────

// NodeConfig holds the provisioned identity and network parameters of a single
// Helix Cluster node. It is the primary data-transfer object between
// Orchestrator.Run() and the mesh joiner.
type NodeConfig struct {
	NodeID      string            `yaml:"node_id"`
	ClusterName string            `yaml:"cluster_name"`
	Tier        string            `yaml:"tier"`
	WGEndpoint  string            `yaml:"wg_endpoint"`
	Labels      map[string]string `yaml:"labels,omitempty"`
}

// GenerateConfig produces a NodeConfig populated from hw and the supplied
// identifiers. The tier comes from hw; a default WireGuard endpoint is derived
// from the OS hostname when one cannot be resolved an explicit fallback is used.
func GenerateConfig(hw HardwareSummary, clusterName, nodeID string) NodeConfig {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}
	return NodeConfig{
		NodeID:      nodeID,
		ClusterName: clusterName,
		Tier:        hw.Tier,
		WGEndpoint:  fmt.Sprintf("%s:51820", hostname),
		Labels: map[string]string{
			"tier":      hw.Tier,
			"cpu_cores": fmt.Sprintf("%d", hw.CPUCores),
		},
	}
}

// ToYAML serialises nc into canonical YAML bytes. A broken generator (dropping
// a field) causes the round-trip assertions in the tests to fail.
func (nc NodeConfig) ToYAML() ([]byte, error) {
	return yaml.Marshal(nc)
}

// ─── Mesh Joiner ─────────────────────────────────────────────────────────────

// MeshJoiner is the seam between the orchestrator and the WireGuard mesh.
// In production a WGMeshJoiner calls pkg/wireguard.MeshCoordinator.SyncNow();
// in tests a RecordingJoiner captures the call for assertions.
type MeshJoiner interface {
	Join(cfg NodeConfig) error
}

// ─── Orchestrator ─────────────────────────────────────────────────────────────

// Orchestrator drives the full single-command node onboarding flow:
//
//	DetectHardware → GenerateConfig → write config file → MeshJoiner.Join → advance lifecycle
//
// All side-effectful dependencies (data directory, joiner) are fields so tests
// can inject test doubles without subprocesses or real network calls.
type Orchestrator struct {
	// ClusterName is the name of the cluster this node joins.
	ClusterName string

	// NodeID is the stable identifier for this node.
	NodeID string

	// DataDir is the directory where node-config.yaml is written.
	// Defaults to $HOME/.helix if empty when Run is called.
	DataDir string

	// Joiner performs the mesh join step. If nil, Run returns an error.
	Joiner MeshJoiner

	// Lifecycle tracks the onboarding state machine. If nil, Run creates one.
	Lifecycle *Lifecycle

	// last holds the config produced in the most recent Run; set before Join.
	last NodeConfig
}

// NewOrchestrator creates an Orchestrator with the given parameters and a fresh
// Lifecycle.
func NewOrchestrator(clusterName, nodeID, dataDir string, joiner MeshJoiner) *Orchestrator {
	return &Orchestrator{
		ClusterName: clusterName,
		NodeID:      nodeID,
		DataDir:     dataDir,
		Joiner:      joiner,
		Lifecycle:   NewLifecycle(),
	}
}

// Run executes the full onboarding sequence. It is the sink-side entry point
// verified by unit and integration tests.
//
// Sequence:
//  1. lifecycle → Provisioning
//  2. DetectHardware
//  3. GenerateConfig
//  4. write node-config.yaml to DataDir
//  5. lifecycle → Configured
//  6. MeshJoiner.Join(cfg)
//  7. lifecycle → Joined → Ready
func (o *Orchestrator) Run() error {
	if o.Joiner == nil {
		return fmt.Errorf("orchestrator: MeshJoiner is nil")
	}
	if o.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("orchestrator: cannot resolve home dir: %w", err)
		}
		o.DataDir = filepath.Join(home, ".helix")
	}
	if o.Lifecycle == nil {
		o.Lifecycle = NewLifecycle()
	}

	// Step 1 – lifecycle: Provisioning.
	if err := o.Lifecycle.Advance(StateProvisioning); err != nil {
		return fmt.Errorf("orchestrator: lifecycle Provisioning: %w", err)
	}

	// Step 2 – detect real hardware.
	hw := DetectHardware()

	// Step 3 – generate typed config.
	cfg := GenerateConfig(hw, o.ClusterName, o.NodeID)
	o.last = cfg

	// Step 4 – persist config to data dir.
	// Owner-only perms: the node config may carry a WireGuard private key, so the
	// dir is 0700 and the file 0600 (gosec G302/G306).
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return fmt.Errorf("orchestrator: mkdir %s: %w", o.DataDir, err)
	}
	data, err := cfg.ToYAML()
	if err != nil {
		return fmt.Errorf("orchestrator: marshal config: %w", err)
	}
	cfgPath := o.ConfigPath()
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		return fmt.Errorf("orchestrator: write node config: %w", err)
	}

	// Step 5 – lifecycle: Configured.
	if err := o.Lifecycle.Advance(StateConfigured); err != nil {
		return fmt.Errorf("orchestrator: lifecycle Configured: %w", err)
	}

	// Step 6 – join the WireGuard mesh.
	if err := o.Joiner.Join(cfg); err != nil {
		return fmt.Errorf("orchestrator: mesh join: %w", err)
	}

	// Step 7 – lifecycle: Joined → Ready.
	if err := o.Lifecycle.Advance(StateJoined); err != nil {
		return fmt.Errorf("orchestrator: lifecycle Joined: %w", err)
	}
	if err := o.Lifecycle.Advance(StateReady); err != nil {
		return fmt.Errorf("orchestrator: lifecycle Ready: %w", err)
	}

	return nil
}

// ConfigPath returns the path of the node config file written by Run.
func (o *Orchestrator) ConfigPath() string {
	return filepath.Join(o.DataDir, "node-config.yaml")
}
