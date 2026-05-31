package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/infra"
)

// fakeOrchestrator is a test double implementing infra.Orchestrator with
// fully controllable return values, so we can drive the CLI action functions
// down failure paths the in-memory simulation cannot reach (e.g. an unhealthy
// service, a Boot error, a Logs error). It records the arguments it was called
// with so tests can assert the wiring is correct, not just the output.
type fakeOrchestrator struct {
	bootResults  []infra.ServiceStatus
	bootErr      error
	bootServices []string // captured

	stopResults []infra.ServiceStatus
	stopErr     error
	stopVolumes bool // captured

	statusResults []infra.ServiceStatus
	healthResults []infra.ServiceStatus

	logsErr     error
	logsService string // captured
	logsTail    int    // captured

	scaleStatus   infra.ServiceStatus
	scaleErr      error
	scaleReplicas int // captured

	vmList     []infra.VMNode
	vmStatus   infra.VMNode
	vmStatErr  error
	vmSSH      string
	vmSSHErr   error
	vmSpawnCnt int // captured
}

func (f *fakeOrchestrator) Boot(_ context.Context, services []string) ([]infra.ServiceStatus, error) {
	f.bootServices = services
	return f.bootResults, f.bootErr
}
func (f *fakeOrchestrator) Stop(_ context.Context, _ []string, removeVolumes bool) ([]infra.ServiceStatus, error) {
	f.stopVolumes = removeVolumes
	return f.stopResults, f.stopErr
}
func (f *fakeOrchestrator) Status(_ context.Context, _ []string) ([]infra.ServiceStatus, error) {
	return f.statusResults, nil
}
func (f *fakeOrchestrator) Health(_ context.Context, _ []string) ([]infra.ServiceStatus, error) {
	return f.healthResults, nil
}
func (f *fakeOrchestrator) Logs(_ context.Context, service string, _ bool, tail int, _ string, _ bool) error {
	f.logsService = service
	f.logsTail = tail
	return f.logsErr
}
func (f *fakeOrchestrator) Scale(_ context.Context, _ string, replicas int) (infra.ServiceStatus, error) {
	f.scaleReplicas = replicas
	return f.scaleStatus, f.scaleErr
}
func (f *fakeOrchestrator) VMSpawn(_ context.Context, count int) ([]infra.VMNode, error) {
	f.vmSpawnCnt = count
	nodes := make([]infra.VMNode, 0, count)
	for i := 0; i < count; i++ {
		nodes = append(nodes, infra.VMNode{ID: "vm-x", IP: "10.0.0.9"})
	}
	return nodes, nil
}
func (f *fakeOrchestrator) VMDestroy(_ context.Context, _ string) error { return nil }
func (f *fakeOrchestrator) VMList(_ context.Context) ([]infra.VMNode, error) {
	return f.vmList, nil
}
func (f *fakeOrchestrator) VMStatus(_ context.Context, _ string) (infra.VMNode, error) {
	return f.vmStatus, f.vmStatErr
}
func (f *fakeOrchestrator) VMSSH(_ context.Context, _ string) (string, error) {
	return f.vmSSH, f.vmSSHErr
}
func (f *fakeOrchestrator) VMSimulateFailure(_ context.Context, _ string) error { return nil }
func (f *fakeOrchestrator) VMSimulatePartition(_ context.Context, _ string, _ time.Duration) error {
	return nil
}
func (f *fakeOrchestrator) Simulated() bool { return true }

var _ infra.Orchestrator = (*fakeOrchestrator)(nil)

// ---- config loading & validation ----

func TestLoadConfig_DefaultsAreValid(t *testing.T) {
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig(\"\"): %v", err)
	}
	if cfg.ProjectName != "helix-cluster" {
		// MUTATION: change DefaultConfig ProjectName -> this fails.
		t.Errorf("ProjectName = %q, want helix-cluster", cfg.ProjectName)
	}
	if len(cfg.Services) == 0 {
		t.Error("expected non-empty default services")
	}
}

func TestLoadConfig_MissingFileIsHardError(t *testing.T) {
	_, err := loadConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		// MUTATION: make loadConfig ignore a non-empty path -> this fails.
		t.Fatal("expected error for missing config file, got nil")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Errorf("error %q should mention read config", err)
	}
}

func TestLoadConfig_OverlayFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(p, []byte(`{"project_name":"custom-proj"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.ProjectName != "custom-proj" {
		// MUTATION: drop the json.Unmarshal overlay -> ProjectName stays default, fails.
		t.Errorf("ProjectName = %q, want custom-proj", cfg.ProjectName)
	}
	if cfg.ConfigPath != p {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, p)
	}
}

func TestLoadConfig_MalformedJSONRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(p); err == nil {
		// MUTATION: skip json.Unmarshal error check -> this fails.
		t.Fatal("expected parse error for malformed JSON")
	}
}

func TestValidateConfig_RejectsBadValues(t *testing.T) {
	base := func() *infra.InfraConfig {
		c := infra.DefaultConfig()
		return c
	}
	cases := map[string]func(*infra.InfraConfig){
		"empty project": func(c *infra.InfraConfig) { c.ProjectName = "" },
		"zero timeout":  func(c *infra.InfraConfig) { c.DefaultTimeout = 0 },
		"no services":   func(c *infra.InfraConfig) { c.Services = nil },
		"empty service": func(c *infra.InfraConfig) { c.Services = []string{"ok", ""} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := base()
			mutate(c)
			if err := validateConfig(c); err == nil {
				// MUTATION: delete the matching check in validateConfig -> fails.
				t.Errorf("expected validateConfig to reject %s", name)
			}
		})
	}
	// Sanity: an unmutated default must pass.
	if err := validateConfig(base()); err != nil {
		t.Errorf("default config should validate, got %v", err)
	}
}

func TestLoadOrchestrator_IsSimulated(t *testing.T) {
	orch, cfg, err := loadOrchestrator("")
	if err != nil {
		t.Fatalf("loadOrchestrator: %v", err)
	}
	if cfg == nil || orch == nil {
		t.Fatal("nil orch/cfg")
	}
	// Anti-bluff: the default orchestrator must self-identify as the fake, so a
	// green CLI test is never mistaken for proof real infra was provisioned.
	if !orch.Simulated() {
		t.Fatal("default orchestrator must report Simulated()==true")
	}
}

// ---- up ----

func TestRunUp_AllHealthy(t *testing.T) {
	f := &fakeOrchestrator{bootResults: []infra.ServiceStatus{
		{Name: "postgres", Healthy: true},
		{Name: "redis", Healthy: true},
	}}
	var out, errb bytes.Buffer
	code, err := runUp(context.Background(), f, []string{"postgres", "redis"}, upOptions{}, &out, &errb)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v, want 0/nil", code, err)
	}
	if !strings.Contains(out.String(), "postgres: healthy") {
		t.Errorf("missing per-service healthy line; got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Infrastructure stack is up.") {
		t.Errorf("missing final up line; got:\n%s", out.String())
	}
	if f.bootServices[0] != "postgres" {
		// MUTATION: pass nil instead of services to Boot -> capture fails.
		t.Errorf("Boot received %v, want [postgres redis]", f.bootServices)
	}
}

func TestRunUp_UnhealthyServiceExitsNonZero(t *testing.T) {
	f := &fakeOrchestrator{bootResults: []infra.ServiceStatus{
		{Name: "postgres", Healthy: true},
		{Name: "kafka", Healthy: false},
	}}
	var out, errb bytes.Buffer
	code, err := runUp(context.Background(), f, nil, upOptions{}, &out, &errb)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != 1 {
		// MUTATION: return 0 unconditionally in runUp -> fails.
		t.Fatalf("code=%d, want 1 for unhealthy service", code)
	}
	if !strings.Contains(errb.String(), "kafka") {
		t.Errorf("stderr should name failed service kafka; got: %q", errb.String())
	}
	if strings.Contains(out.String(), "Infrastructure stack is up.") {
		t.Error("must NOT print success when a service failed")
	}
}

func TestRunUp_BootErrorPropagates(t *testing.T) {
	f := &fakeOrchestrator{bootErr: errors.New("boom")}
	var out, errb bytes.Buffer
	code, err := runUp(context.Background(), f, nil, upOptions{}, &out, &errb)
	if err == nil || code != 1 {
		// MUTATION: swallow Boot's error -> fails.
		t.Fatalf("code=%d err=%v, want 1/non-nil", code, err)
	}
}

// ---- down ----

func TestRunDown_ReportsMessagesAndForwardsVolumesFlag(t *testing.T) {
	f := &fakeOrchestrator{stopResults: []infra.ServiceStatus{
		{Name: "postgres", Message: "stopped and volumes removed"},
	}}
	var out bytes.Buffer
	code, err := runDown(context.Background(), f, nil, true, &out)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if !f.stopVolumes {
		// MUTATION: hardcode removeVolumes=false in wrapper -> fails.
		t.Error("removeVolumes flag not forwarded to Stop")
	}
	if !strings.Contains(out.String(), "volumes removed") {
		t.Errorf("stop message missing; got:\n%s", out.String())
	}
}

// ---- status ----

func TestRunStatus_TableContainsServiceRow(t *testing.T) {
	f := &fakeOrchestrator{statusResults: []infra.ServiceStatus{
		{Name: "etcd-1", Status: "running", Healthy: true, Uptime: 90 * time.Second, Ports: []string{"2379"}},
	}}
	var out bytes.Buffer
	code, err := runStatus(context.Background(), f, nil, false, &out)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	s := out.String()
	for _, want := range []string{"SERVICE", "etcd-1", "running", "true", "1m30s", "2379"} {
		if !strings.Contains(s, want) {
			// MUTATION: drop a column from statusRows/headers -> fails.
			t.Errorf("status table missing %q; got:\n%s", want, s)
		}
	}
}

func TestRunStatus_JSONMode(t *testing.T) {
	f := &fakeOrchestrator{statusResults: []infra.ServiceStatus{{Name: "vault", Status: "running"}}}
	var out bytes.Buffer
	if _, err := runStatus(context.Background(), f, nil, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"name": "vault"`) {
		// MUTATION: ignore asJSON and always print table -> fails.
		t.Errorf("expected JSON output; got:\n%s", out.String())
	}
}

// ---- health ----

func TestRunHealth_AllHealthyExitZero(t *testing.T) {
	f := &fakeOrchestrator{healthResults: []infra.ServiceStatus{
		{Name: "nats", Healthy: true, Latency: 2 * time.Millisecond, Message: "ok"},
	}}
	var out bytes.Buffer
	code, err := runHealth(context.Background(), f, nil, false, &out)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v, want 0/nil", code, err)
	}
	if !strings.Contains(out.String(), "nats") || !strings.Contains(out.String(), "2ms") {
		t.Errorf("health table missing nats/latency; got:\n%s", out.String())
	}
}

func TestRunHealth_UnhealthyExitsNonZero(t *testing.T) {
	f := &fakeOrchestrator{healthResults: []infra.ServiceStatus{
		{Name: "nats", Healthy: true},
		{Name: "kafka", Healthy: false, Message: "down"},
	}}
	var out bytes.Buffer
	code, err := runHealth(context.Background(), f, nil, false, &out)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if code != 1 {
		// MUTATION: always return 0 in runHealth -> fails.
		t.Fatalf("code=%d, want 1 when a service is unhealthy", code)
	}
}

// ---- logs ----

func TestRunLogs_ValidTailStreams(t *testing.T) {
	f := &fakeOrchestrator{}
	var out bytes.Buffer
	code, err := runLogs(context.Background(), f, "postgres", true, "50", "", false, &out)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if f.logsTail != 50 {
		// MUTATION: pass a constant tail to Logs -> fails.
		t.Errorf("Logs got tail=%d, want 50", f.logsTail)
	}
	if f.logsService != "postgres" {
		t.Errorf("Logs got service=%q, want postgres", f.logsService)
	}
	if !strings.Contains(out.String(), "tail=50") {
		t.Errorf("log summary missing tail=50; got:\n%s", out.String())
	}
}

func TestRunLogs_NonNumericTailRejected(t *testing.T) {
	f := &fakeOrchestrator{}
	var out bytes.Buffer
	code, err := runLogs(context.Background(), f, "postgres", true, "abc", "", false, &out)
	if err == nil || code != 1 {
		// MUTATION: revert parseTail to strconv.Atoi-with-ignored-error -> fails.
		t.Fatalf("code=%d err=%v, want 1/non-nil for non-numeric tail", code, err)
	}
	if f.logsService != "" {
		t.Error("Logs must not be called when --tail is invalid")
	}
}

// ---- scale ----

func TestRunScale_ParsesReplicas(t *testing.T) {
	f := &fakeOrchestrator{scaleStatus: infra.ServiceStatus{Message: "scaled to 3 replicas"}}
	var out bytes.Buffer
	code, err := runScale(context.Background(), f, "redis-replica", "3", false, &out)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if f.scaleReplicas != 3 {
		// MUTATION: pass a constant to Scale -> fails.
		t.Errorf("Scale got replicas=%d, want 3", f.scaleReplicas)
	}
	if !strings.Contains(out.String(), "Scaling redis-replica to 3 replicas") {
		t.Errorf("missing scale line; got:\n%s", out.String())
	}
}

func TestRunScale_NegativeReplicasRejected(t *testing.T) {
	f := &fakeOrchestrator{}
	var out bytes.Buffer
	code, err := runScale(context.Background(), f, "redis", "-2", false, &out)
	if err == nil || code != 1 {
		// MUTATION: drop the n<0 check in parseReplicas -> fails.
		t.Fatalf("code=%d err=%v, want 1/non-nil for negative replicas", code, err)
	}
}

// ---- vm ----

func TestRunVMSpawn_RejectsZeroCount(t *testing.T) {
	f := &fakeOrchestrator{}
	var out bytes.Buffer
	code, err := runVMSpawn(context.Background(), f, 0, &out)
	if err == nil || code != 1 {
		// MUTATION: remove the count<1 guard -> fails (VMSpawn would return no nodes, code 0).
		t.Fatalf("code=%d err=%v, want 1/non-nil for count=0", code, err)
	}
}

func TestRunVMSpawn_PrintsEachNode(t *testing.T) {
	f := &fakeOrchestrator{}
	var out bytes.Buffer
	code, err := runVMSpawn(context.Background(), f, 2, &out)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if f.vmSpawnCnt != 2 {
		t.Errorf("VMSpawn got count=%d, want 2", f.vmSpawnCnt)
	}
	if strings.Count(out.String(), "Spawned VM node:") != 2 {
		// MUTATION: print only the first node -> fails.
		t.Errorf("expected 2 spawned lines; got:\n%s", out.String())
	}
}

func TestRunVMList_TableRows(t *testing.T) {
	f := &fakeOrchestrator{vmList: []infra.VMNode{
		{ID: "vm-1", Name: "vm-1", Status: "running", IP: "10.0.0.1", CPU: 2, Memory: 4096},
	}}
	var out bytes.Buffer
	if _, err := runVMList(context.Background(), f, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vm-1", "running", "10.0.0.1", "4096"} {
		if !strings.Contains(out.String(), want) {
			// MUTATION: drop a field from vmListRows -> fails.
			t.Errorf("vm list missing %q; got:\n%s", want, out.String())
		}
	}
}

func TestRunVMStatus_ErrorPropagates(t *testing.T) {
	f := &fakeOrchestrator{vmStatErr: errors.New("vm node nope not found")}
	var out bytes.Buffer
	code, err := runVMStatus(context.Background(), f, "nope", &out)
	if err == nil || code != 1 {
		// MUTATION: swallow VMStatus error -> fails.
		t.Fatalf("code=%d err=%v, want 1/non-nil", code, err)
	}
}

func TestRunVMSimulatePartition_RejectsNonPositiveDuration(t *testing.T) {
	f := &fakeOrchestrator{}
	var out bytes.Buffer
	code, err := runVMSimulatePartition(context.Background(), f, "vm-1", 0, &out)
	if err == nil || code != 1 {
		// MUTATION: remove the duration<=0 guard -> fails.
		t.Fatalf("code=%d err=%v, want 1/non-nil for zero duration", code, err)
	}
}

// ---- rendering ----

func TestRenderTable_AlignmentAndSeparator(t *testing.T) {
	got := renderTable([]string{"A", "BB"}, [][]string{{"xxxx", "y"}})
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header+sep+1 row = 3 lines, got %d:\n%s", len(lines), got)
	}
	// Column A width = max(len("A"),len("xxxx"))=4, +2 padding = 6.
	if lines[0] != "A     BB  " {
		// MUTATION: change padding width -> fails.
		t.Errorf("header = %q, want %q", lines[0], "A     BB  ")
	}
	if !strings.HasPrefix(lines[1], "------") {
		t.Errorf("separator row malformed: %q", lines[1])
	}
	if lines[2] != "xxxx  y   " {
		t.Errorf("data row = %q, want %q", lines[2], "xxxx  y   ")
	}
}

func TestPrintJSON_Indented(t *testing.T) {
	var out bytes.Buffer
	if err := printJSON(&out, map[string]int{"n": 7}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "  \"n\": 7") {
		// MUTATION: drop SetIndent -> fails.
		t.Errorf("expected indented JSON; got:\n%s", out.String())
	}
}

// ---- version ----

func TestResolveVersion_PrefersBuildVersion(t *testing.T) {
	if v := resolveVersion("v1.2.3", "VERSION"); v != "v1.2.3" {
		// MUTATION: always read the file -> fails.
		t.Errorf("resolveVersion = %q, want v1.2.3", v)
	}
}

func TestResolveVersion_FallsBackToFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "VERSION")
	if err := os.WriteFile(p, []byte("  v9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := resolveVersion("dev", p); v != "v9.9.9" {
		// MUTATION: don't TrimSpace, or don't read file -> fails.
		t.Errorf("resolveVersion = %q, want v9.9.9", v)
	}
}

func TestWriteVersion_AllFields(t *testing.T) {
	var out bytes.Buffer
	writeVersion(&out, "v1", "abc123", "2026-06-01", "go1.26")
	for _, want := range []string{"Version:   v1", "GitCommit: abc123", "BuildDate: 2026-06-01", "GoVersion: go1.26"} {
		if !strings.Contains(out.String(), want) {
			// MUTATION: drop a line from writeVersion -> fails.
			t.Errorf("version output missing %q; got:\n%s", want, out.String())
		}
	}
}

// ---- end-to-end via cobra (real simulated orchestrator + finish/exit wiring) ----

func TestCobraVersion_EndToEnd(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute version: %v", err)
	}
	if !strings.Contains(out.String(), "GoVersion:") {
		// MUTATION: writeVersion to os.Stdout instead of cmd.OutOrStdout -> fails (buffer empty).
		t.Errorf("version e2e output missing GoVersion; got:\n%s", out.String())
	}
}

func TestCobraScale_BadReplicasReturnsError(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"scale", "redis", "notanumber"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	err := rootCmd.Execute()
	if err == nil {
		// MUTATION: ignore parseReplicas error in runScale -> fails.
		t.Fatal("expected non-nil error from scale with bad replicas")
	}
	var ee *errExit
	if errors.As(err, &ee) {
		t.Fatalf("a validation error should be a real error, not an exit-code sentinel: %v", err)
	}
}

func TestFinish_ExitCodeBecomesSentinel(t *testing.T) {
	err := finish(1, nil)
	var ee *errExit
	if !errors.As(err, &ee) || ee.code != 1 {
		// MUTATION: have finish return nil for nonzero code -> fails.
		t.Fatalf("finish(1,nil) = %v, want *errExit{code:1}", err)
	}
	if finish(0, nil) != nil {
		t.Error("finish(0,nil) must be nil")
	}
	real := errors.New("x")
	if got := finish(1, real); !errors.Is(got, real) {
		t.Errorf("finish must return the real error verbatim")
	}
}
