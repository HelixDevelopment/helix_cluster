package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// allFound is a lookPath stub that reports every probed command as present.
func allFound(string) (string, error) { return "/usr/bin/stub", nil }

// missing returns a lookPath stub that reports the named commands as absent and
// everything else as present.
func missing(absent ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, a := range absent {
		set[a] = true
	}
	return func(cmd string) (string, error) {
		if set[cmd] {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + cmd, nil
	}
}

func newConfig(dataDir string, lp func(string) (string, error)) Config {
	return Config{
		DataDir:  dataDir,
		Prereqs:  defaultPrereqs(),
		lookPath: lp,
	}
}

// TestRunHappyPathWritesConfig proves run produces a config.yaml with the exact
// expected content inside the data dir, reports success on stdout, and exits 0.
// Mutation: change exitOK return in run to exitWriteFail -> code assert fails.
// Mutation: drop the os.WriteFile call -> file-content assert fails.
func TestRunHappyPathWritesConfig(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, ".helix")
	var out, errOut strings.Builder

	code := run(context.Background(), newConfig(dataDir, allFound),
		[]string{"helix-setup"}, &out, &errOut)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", code, exitOK, errOut.String())
	}

	got, err := os.ReadFile(filepath.Join(dataDir, configFileName))
	if err != nil {
		t.Fatalf("reading generated config: %v", err)
	}
	if string(got) != configContent {
		t.Fatalf("config content mismatch:\n got=%q\nwant=%q", got, configContent)
	}
	// Sink-side, end-user-visible evidence: the generated YAML actually carries
	// the cluster identity an operator would consume downstream.
	if !strings.Contains(string(got), "name: helix-local") {
		t.Fatalf("config missing cluster name; got:\n%s", got)
	}
	if !strings.Contains(out.String(), "Generated config:") {
		t.Fatalf("stdout did not report generated config; got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Setup complete!") {
		t.Fatalf("stdout missing completion line; got:\n%s", out.String())
	}
}

// TestRunIdempotent proves a second run over the same data dir succeeds and
// leaves identical content (no duplication, no error on pre-existing file).
// Mutation: make run fail when configPath already exists -> second run code != 0.
func TestRunIdempotent(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".helix")
	cfg := newConfig(dataDir, allFound)
	var out, errOut strings.Builder

	if code := run(context.Background(), cfg, []string{"helix-setup"}, &out, &errOut); code != exitOK {
		t.Fatalf("first run exit = %d, want 0; stderr=%q", code, errOut.String())
	}
	first, err := os.ReadFile(cfg.ConfigPath())
	if err != nil {
		t.Fatalf("read after first run: %v", err)
	}

	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), cfg, []string{"helix-setup"}, &out, &errOut); code != exitOK {
		t.Fatalf("second run exit = %d, want 0; stderr=%q", code, errOut.String())
	}
	second, err := os.ReadFile(cfg.ConfigPath())
	if err != nil {
		t.Fatalf("read after second run: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("config changed between idempotent runs:\nfirst=%q\nsecond=%q", first, second)
	}
}

// TestRunMissingPrereqFails proves a missing prerequisite yields exitPrereqFail,
// names the tool on stderr, and writes NO config (fail-before-side-effect).
// Mutation: have checkPrereqs return exitOK on failure -> code assert fails.
// Mutation: move the prereq check after WriteFile -> config-absent assert fails.
func TestRunMissingPrereqFails(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".helix")
	cfg := newConfig(dataDir, missing("docker"))
	var out, errOut strings.Builder

	code := run(context.Background(), cfg, []string{"helix-setup"}, &out, &errOut)

	if code != exitPrereqFail {
		t.Fatalf("exit code = %d, want %d", code, exitPrereqFail)
	}
	if !strings.Contains(errOut.String(), "Docker") {
		t.Fatalf("stderr did not name missing Docker prereq; got:\n%s", errOut.String())
	}
	if _, err := os.Stat(cfg.ConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("config should NOT be written when prereq fails; stat err=%v", err)
	}
}

// TestRunSkipChecks proves --skip-checks bypasses prerequisite probing entirely
// (a deliberately failing lookPath is never consulted) and still writes config.
// Mutation: ignore the skipChecks flag in run -> code becomes exitPrereqFail.
func TestRunSkipChecks(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".helix")
	probed := false
	lp := func(cmd string) (string, error) {
		probed = true
		return "", errors.New("should not be called")
	}
	cfg := newConfig(dataDir, lp)
	var out, errOut strings.Builder

	code := run(context.Background(), cfg, []string{"helix-setup", "--skip-checks"}, &out, &errOut)

	if code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errOut.String())
	}
	if probed {
		t.Fatalf("--skip-checks must not probe prerequisites, but lookPath was called")
	}
	if _, err := os.Stat(cfg.ConfigPath()); err != nil {
		t.Fatalf("config not written under --skip-checks: %v", err)
	}
}

// TestRunDataDirFlagOverride proves --data-dir redirects output and beats the
// configured default, so nothing is written to the original DataDir.
// Mutation: stop applying *dataDir override in run -> file lands in cfg.DataDir.
func TestRunDataDirFlagOverride(t *testing.T) {
	defaultDir := filepath.Join(t.TempDir(), "default")
	overrideDir := filepath.Join(t.TempDir(), "override")
	cfg := newConfig(defaultDir, allFound)
	var out, errOut strings.Builder

	code := run(context.Background(), cfg,
		[]string{"helix-setup", "--data-dir", overrideDir}, &out, &errOut)

	if code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(overrideDir, configFileName)); err != nil {
		t.Fatalf("config not written to override dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultDir, configFileName)); !os.IsNotExist(err) {
		t.Fatalf("config wrongly written to default dir; stat err=%v", err)
	}
}

// TestRunBadFlagUsage proves an unknown flag produces exitUsage and writes no
// config (argument validation before any side-effect).
// Mutation: return exitOK instead of exitUsage on parse error -> assert fails.
func TestRunBadFlag(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".helix")
	cfg := newConfig(dataDir, allFound)
	var out, errOut strings.Builder

	code := run(context.Background(), cfg, []string{"helix-setup", "--nope"}, &out, &errOut)

	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if _, err := os.Stat(cfg.ConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("config should not be written on bad flag; stat err=%v", err)
	}
}

// TestRunUnexpectedArg proves a stray positional arg is rejected with exitUsage.
// Mutation: drop the fs.NArg()>0 guard in run -> code becomes exitOK.
func TestRunUnexpectedArg(t *testing.T) {
	cfg := newConfig(filepath.Join(t.TempDir(), ".helix"), allFound)
	var out, errOut strings.Builder

	code := run(context.Background(), cfg, []string{"helix-setup", "extra"}, &out, &errOut)

	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "unexpected argument") {
		t.Fatalf("stderr did not flag the unexpected arg; got:\n%s", errOut.String())
	}
}

// TestRunCancelledContext proves a cancelled context aborts before writing the
// config, returning exitWriteFail and leaving the filesystem untouched.
// Mutation: remove the ctx.Err() guard in run -> config gets written, assert fails.
func TestRunCancelledContext(t *testing.T) {
	cfg := newConfig(filepath.Join(t.TempDir(), ".helix"), allFound)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut strings.Builder

	code := run(ctx, cfg, []string{"helix-setup"}, &out, &errOut)

	if code != exitWriteFail {
		t.Fatalf("exit = %d, want %d", code, exitWriteFail)
	}
	if _, err := os.Stat(cfg.ConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("config should not be written when ctx cancelled; stat err=%v", err)
	}
}

// TestRunInvalidConfigRejected proves run validates injected Config and refuses
// an empty DataDir with exitUsage rather than writing somewhere unexpected.
// Mutation: drop cfg.Validate() call in run -> empty DataDir path is used.
func TestRunInvalidConfigRejected(t *testing.T) {
	cfg := Config{DataDir: "", Prereqs: defaultPrereqs(), lookPath: allFound}
	var out, errOut strings.Builder

	code := run(context.Background(), cfg, []string{"helix-setup", "--skip-checks"}, &out, &errOut)

	if code != exitUsage {
		t.Fatalf("exit = %d, want %d; stderr=%q", code, exitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), "configuration error") {
		t.Fatalf("stderr did not report configuration error; got:\n%s", errOut.String())
	}
}

// TestConfigValidate exercises the Config.Validate guards directly.
// Mutation: make Validate always return nil -> the nil-lookPath case fails.
func TestConfigValidate(t *testing.T) {
	good := newConfig("/tmp/x", allFound)
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]Config{
		"empty dir":    {DataDir: "", Prereqs: defaultPrereqs(), lookPath: allFound},
		"no prereqs":   {DataDir: "/tmp/x", Prereqs: nil, lookPath: allFound},
		"nil lookPath": {DataDir: "/tmp/x", Prereqs: defaultPrereqs(), lookPath: nil},
	}
	for name, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

// TestDefaultDataDir proves the default location derives from $HOME via the
// injected getenv, matching the documented $HOME/.helix layout.
// Mutation: change ".helix" literal in defaultDataDir -> assert fails.
func TestDefaultDataDir(t *testing.T) {
	got := defaultDataDir(func(string) string { return "/home/tester" })
	want := filepath.Join("/home/tester", ".helix")
	if got != want {
		t.Fatalf("defaultDataDir = %q, want %q", got, want)
	}
}

// ─── Integration: run() wires the Orchestrator ───────────────────────────────

// TestRunWiresOrchestrator is the primary anti-PASS-bluff integration test.
// It proves that run() actually invokes the Orchestrator path by asserting:
//
//  1. node-config.yaml is written to the data dir (orchestrator artifact).
//  2. node-config.yaml contains a real tier value (T1–T4), proving hardware
//     detection ran — the static configContent constant has "tier: T1" in the
//     labels map but not as a top-level field; only the orchestrator writes the
//     top-level "tier:" key in node-config.yaml.
//  3. The injected joiner was called (Join received the expected node ID),
//     proving the orchestrator mesh-join step was not skipped.
//  4. config.yaml (static endpoints) is ALSO written (existing behaviour
//     preserved — both files must exist).
//
// Bite: remove orch.Run() from run() → node-config.yaml absent, test fails.
// Bite: skip joiner.Join in orchestrator.Run() → joiner.called==false, fails.
// Bite: use static configContent for node-config → YAML has no "tier:" key.
func TestRunWiresOrchestrator(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".helix")
	joiner := &recordingJoiner{}

	cfg := Config{
		DataDir:  dataDir,
		Prereqs:  defaultPrereqs(),
		lookPath: allFound,
		Joiner:   joiner,
	}
	var out, errOut strings.Builder

	code := run(context.Background(), cfg,
		[]string{"helix-setup", "--skip-checks", "--cluster-name", "integration-cluster", "--node-id", "inode-1"},
		&out, &errOut)

	if code != exitOK {
		t.Fatalf("run() exit = %d, want 0; stderr=%q stdout=%q", code, errOut.String(), out.String())
	}

	// 1. node-config.yaml must exist.
	nodeConfigPath := filepath.Join(dataDir, "node-config.yaml")
	raw, err := os.ReadFile(nodeConfigPath)
	if err != nil {
		t.Fatalf("node-config.yaml not found at %s: %v\nstdout=%s\nstderr=%s",
			nodeConfigPath, err, out.String(), errOut.String())
	}
	if len(raw) == 0 {
		t.Fatal("node-config.yaml is empty")
	}

	// 2. node-config.yaml must contain a real tier.
	var decoded NodeConfig
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("node-config.yaml is not valid YAML: %v\ncontent:\n%s", err, raw)
	}
	validTiers := map[string]bool{"T1": true, "T2": true, "T3": true, "T4": true}
	if !validTiers[decoded.Tier] {
		t.Errorf("node-config.yaml tier = %q, want one of T1–T4\ncontent:\n%s", decoded.Tier, raw)
	}

	// 3. Joiner must have been called with correct identity.
	if !joiner.called {
		t.Fatal("MeshJoiner.Join was never called — orchestrator not wired into run()")
	}
	if joiner.receivedCfg.NodeID != "inode-1" {
		t.Errorf("joiner received NodeID %q, want %q", joiner.receivedCfg.NodeID, "inode-1")
	}
	if joiner.receivedCfg.ClusterName != "integration-cluster" {
		t.Errorf("joiner received ClusterName %q, want %q", joiner.receivedCfg.ClusterName, "integration-cluster")
	}

	// 4. config.yaml (static service endpoints) must also exist.
	if _, err := os.Stat(filepath.Join(dataDir, configFileName)); err != nil {
		t.Fatalf("config.yaml not found: %v", err)
	}
}

// TestRunNodeIDAutoFromHostname proves --node-id omitted causes run() to derive
// the node ID from the host's hostname (non-empty). The written node-config.yaml
// must carry a non-empty node_id field.
//
// Bite: leave effectiveNodeID as "" when flag absent → decoded.NodeID == "".
func TestRunNodeIDAutoFromHostname(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".helix")
	joiner := &recordingJoiner{}

	cfg := Config{
		DataDir:  dataDir,
		Prereqs:  defaultPrereqs(),
		lookPath: allFound,
		Joiner:   joiner,
	}
	var out, errOut strings.Builder

	// No --node-id flag; hostname fallback must be used.
	code := run(context.Background(), cfg,
		[]string{"helix-setup", "--skip-checks"}, &out, &errOut)

	if code != exitOK {
		t.Fatalf("run() exit = %d, want 0; stderr=%q", code, errOut.String())
	}

	nodeConfigPath := filepath.Join(dataDir, "node-config.yaml")
	raw, err := os.ReadFile(nodeConfigPath)
	if err != nil {
		t.Fatalf("node-config.yaml not found: %v", err)
	}
	var decoded NodeConfig
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("node-config.yaml not valid YAML: %v", err)
	}
	if decoded.NodeID == "" {
		t.Errorf("node_id is empty in node-config.yaml — hostname fallback not wired\ncontent:\n%s", raw)
	}
}
