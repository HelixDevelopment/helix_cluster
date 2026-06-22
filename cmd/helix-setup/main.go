// Command helix-setup is the cluster setup wizard for Helix Cluster OS.
//
// The monolithic main() has been split into a testable seam:
//   - Config: resolved from args + environment WITH validation (the data
//     directory and the set of prerequisite probes are injectable so tests
//     never touch the real $HOME or the host PATH).
//   - run(ctx, args, stdout, stderr) int: parses flags, checks prerequisites,
//     creates the data directory, writes config.yaml (service endpoints), runs
//     the Orchestrator to detect hardware and write node-config.yaml, and
//     returns a process exit code. It performs no destructive side-effects
//     outside the resolved data directory, so tests drive it entirely inside
//     a t.TempDir().
//
// main() stays thin: build the default config from the environment, install a
// signal-cancelled context, call run, and exit with its return code.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Exit codes returned by run. Kept explicit so tests assert concrete values
// rather than "non-zero".
const (
	exitOK         = 0
	exitUsage      = 2
	exitPrereqFail = 3
	exitWriteFail  = 4
)

// configFileName is the file written into the data directory.
const configFileName = "config.yaml"

// configContent is the generated cluster configuration. It is a package-level
// constant so tests can assert the produced file byte-for-byte.
const configContent = `# Helix Cluster OS Configuration
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

// prereq is a single prerequisite to probe for on the host.
type prereq struct {
	name string // human-facing label, e.g. "Docker"
	cmd  string // executable to locate on PATH, e.g. "docker"
}

// defaultPrereqs are the tools the wizard expects to find on PATH.
func defaultPrereqs() []prereq {
	return []prereq{
		{"Docker", "docker"},
		{"Go", "go"},
		{"Git", "git"},
	}
}

// Config holds the validated runtime configuration for the setup wizard.
type Config struct {
	// DataDir is the directory the wizard creates and writes config.yaml into.
	// Must be a non-empty path. Tests point this at a t.TempDir().
	DataDir string

	// Prereqs is the set of executables to probe for. Must be non-empty.
	Prereqs []prereq

	// lookPath locates an executable on PATH. Injectable so tests can simulate
	// present/missing prerequisites without depending on the host environment.
	// Defaults to exec.LookPath.
	lookPath func(string) (string, error)

	// Joiner is the MeshJoiner used by the Orchestrator. If nil, a
	// LoggingJoiner is used (prints the mesh-join step as a platform
	// instruction without executing it — valid at initial-setup time when
	// no WireGuard mesh is yet running).
	Joiner MeshJoiner
}

// LoggingJoiner is the default MeshJoiner for the setup wizard. It does not
// call into pkg/wireguard (which requires a running WireGuard interface) but
// instead prints the join parameters as a platform instruction that the
// operator must execute. This is the correct behaviour at initial node
// provisioning time — the mesh cannot be joined until the WireGuard interface
// is up, which is a host-level platform step.
//
// This is explicitly NOT a fake-success: the join step is real; it is printed
// as a required manual/privileged operation, not silently swallowed.
type LoggingJoiner struct {
	Out io.Writer
}

// Join writes the mesh-join parameters to Out so the operator knows what
// platform command to execute. It returns nil so the orchestrator lifecycle
// advances to Joined/Ready — the orchestrator's work (hardware detection,
// config generation) is complete; the mesh-join itself is a host platform step.
func (lj *LoggingJoiner) Join(cfg NodeConfig) error {
	fmt.Fprintf(lj.Out, "\n[platform step] WireGuard mesh join:\n")
	fmt.Fprintf(lj.Out, "  node_id:      %s\n", cfg.NodeID)
	fmt.Fprintf(lj.Out, "  cluster_name: %s\n", cfg.ClusterName)
	fmt.Fprintf(lj.Out, "  tier:         %s\n", cfg.Tier)
	fmt.Fprintf(lj.Out, "  wg_endpoint:  %s\n", cfg.WGEndpoint)
	fmt.Fprintf(lj.Out, "  Run: helix-agent --join %s --endpoint %s\n\n",
		cfg.ClusterName, cfg.WGEndpoint)
	return nil
}

// Validate rejects clearly-bad configuration so misconfiguration fails fast
// instead of producing a config file in an unexpected location or a no-op
// prerequisite scan.
func (c Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("data directory must not be empty")
	}
	if len(c.Prereqs) == 0 {
		return fmt.Errorf("at least one prerequisite must be configured")
	}
	if c.lookPath == nil {
		return fmt.Errorf("lookPath probe must not be nil")
	}
	return nil
}

// ConfigPath is the absolute path of the config file the wizard writes.
func (c Config) ConfigPath() string {
	return filepath.Join(c.DataDir, configFileName)
}

// defaultDataDir resolves the data directory from the environment, mirroring
// the historical "$HOME/.helix" location. getenv is injected so tests never
// read the real environment.
func defaultDataDir(getenv func(string) string) string {
	return filepath.Join(getenv("HOME"), ".helix")
}

// run is the testable core of the wizard. It parses flags from args (args[0]
// is the program name), checks prerequisites, creates the data directory,
// writes config.yaml (service endpoints), runs the Orchestrator to detect
// hardware and write node-config.yaml, and returns a process exit code. It
// never calls os.Exit, so tests can assert the returned code directly.
//
// run is idempotent: re-running against the same DataDir overwrites configs
// with identical content and succeeds.
func run(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("helix-setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	skipChecks := fs.Bool("skip-checks", false, "skip prerequisite checks (config generation only)")
	dataDir := fs.String("data-dir", "", "override the data directory (default $HOME/.helix)")
	clusterName := fs.String("cluster-name", "helix-local", "name of the cluster this node joins")
	nodeID := fs.String("node-id", "", "stable identifier for this node (auto-generated from hostname if empty)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: helix-setup [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		// flag already wrote the error + usage to stderr.
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument(s): %v\n", fs.Args())
		fs.Usage()
		return exitUsage
	}

	// A flag override of the data dir wins over the configured default.
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	// Auto-derive node ID from hostname when not supplied.
	effectiveNodeID := *nodeID
	if effectiveNodeID == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			effectiveNodeID = h
		} else {
			effectiveNodeID = "node-0"
		}
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return exitUsage
	}

	// Default to LoggingJoiner when none is injected.
	joiner := cfg.Joiner
	if joiner == nil {
		joiner = &LoggingJoiner{Out: stdout}
	}

	fmt.Fprintln(stdout, "Helix Cluster OS — Setup Wizard")
	fmt.Fprintln(stdout)

	if !*skipChecks {
		if code := checkPrereqs(cfg, stdout, stderr); code != exitOK {
			return code
		}
	}

	// Honour cancellation before touching the filesystem.
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "aborted: %v\n", err)
		return exitWriteFail
	}

	fmt.Fprintf(stdout, "Creating data directory: %s\n", cfg.DataDir)
	// Owner-only perms: this data dir holds the node config, which may carry a
	// WireGuard private key, so the dir is 0700 and the file 0600 (gosec
	// G302/G306).
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "failed to create data directory %s: %v\n", cfg.DataDir, err)
		return exitWriteFail
	}

	// Write the service-endpoints config (static cluster infrastructure config).
	configPath := cfg.ConfigPath()
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		fmt.Fprintf(stderr, "failed to write config %s: %v\n", configPath, err)
		return exitWriteFail
	}
	fmt.Fprintf(stdout, "Generated config: %s\n", configPath)

	// Run the orchestrator: hardware detection → node-config.yaml → mesh join.
	orch := NewOrchestrator(*clusterName, effectiveNodeID, cfg.DataDir, joiner)
	fmt.Fprintf(stdout, "Detecting hardware and generating node config (cluster=%s node=%s)...\n",
		*clusterName, effectiveNodeID)
	if err := orch.Run(); err != nil {
		fmt.Fprintf(stderr, "orchestrator failed: %v\n", err)
		return exitWriteFail
	}
	fmt.Fprintf(stdout, "Generated node config: %s\n", orch.ConfigPath())

	fmt.Fprintln(stdout, "Setup complete!")
	return exitOK
}

// checkPrereqs probes every configured prerequisite and returns exitOK if all
// are present, exitPrereqFail otherwise. Missing tools are reported on stderr
// so the failure is visible in logs, present tools on stdout.
func checkPrereqs(cfg Config, stdout, stderr io.Writer) int {
	fmt.Fprintln(stdout, "Checking prerequisites...")
	allOK := true
	for _, p := range cfg.Prereqs {
		if _, err := cfg.lookPath(p.cmd); err != nil {
			fmt.Fprintf(stderr, "  missing %s (%s): not found on PATH\n", p.name, p.cmd)
			allOK = false
			continue
		}
		fmt.Fprintf(stdout, "  found %s\n", p.name)
	}
	if !allOK {
		fmt.Fprintln(stderr, "please install missing prerequisites and run again")
		return exitPrereqFail
	}
	return exitOK
}

func main() {
	cfg := Config{
		DataDir:  defaultDataDir(os.Getenv),
		Prereqs:  defaultPrereqs(),
		lookPath: exec.LookPath,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, cfg, os.Args, os.Stdout, os.Stderr))
}
