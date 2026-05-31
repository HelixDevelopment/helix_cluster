package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/node"
	"github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// syncBuffer is a goroutine-safe io.Writer for capturing output that one
// goroutine writes (via run) while another polls it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func testKey(t *testing.T) string {
	t.Helper()
	priv, _, err := wireguard.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv
}

// --- buildConfig: real argument validation -------------------------------

// TestBuildConfigValidArgs proves flags are parsed into the exact Config the
// agent is constructed from.
//
// Mutation: in buildConfig change `SwimBindPort: *bindPort` to
// `SwimBindPort: 0`. The assertion that SwimBindPort == 7001 then fails.
func TestBuildConfigValidArgs(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := buildConfig([]string{
		"--id", "node-A",
		"--region", "eu-west-2",
		"--bind-addr", "127.0.0.1",
		"--bind-port", "7001",
		"--wg-port", "51999",
		"--wg-key", testKey(t),
	}, &stderr)
	if err != nil {
		t.Fatalf("buildConfig: %v (stderr=%q)", err, stderr.String())
	}
	if cfg.ID != "node-A" {
		t.Errorf("ID = %q, want node-A", cfg.ID)
	}
	if cfg.Region != "eu-west-2" {
		t.Errorf("Region = %q, want eu-west-2", cfg.Region)
	}
	if cfg.SwimBindAddr != "127.0.0.1" {
		t.Errorf("SwimBindAddr = %q, want 127.0.0.1", cfg.SwimBindAddr)
	}
	if cfg.SwimBindPort != 7001 {
		t.Errorf("SwimBindPort = %d, want 7001", cfg.SwimBindPort)
	}
	if cfg.WgListenPort != 51999 {
		t.Errorf("WgListenPort = %d, want 51999", cfg.WgListenPort)
	}
	if !cfg.WgNoOp {
		t.Error("WgNoOp = false, want true (agent must not touch real WireGuard)")
	}
}

// TestBuildConfigGeneratesKeyWhenAbsent proves an empty --wg-key triggers real
// keypair generation rather than leaving the key blank.
//
// Mutation: delete the `if key == "" { ... GenerateKeyPair ... }` block so an
// empty key is passed through. WgPrivateKey stays "" and the assertion fails.
func TestBuildConfigGeneratesKeyWhenAbsent(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := buildConfig([]string{"--id", "node-B"}, &stderr)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.WgPrivateKey == "" {
		t.Fatal("WgPrivateKey is empty; expected a generated key")
	}
	// A generated WireGuard private key must be usable to actually construct an
	// agent (NewAgent feeds it to wireguard.NewManager). Prove it end-to-end by
	// building a real agent with the generated key and a host-safe ephemeral
	// SWIM port, then tearing it down.
	cfg.SwimBindPort = 0
	cfg.WgListenPort = 0
	agent, err := node.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent with generated key: %v", err)
	}
	if err := agent.Start(); err != nil {
		t.Fatalf("start with generated key: %v", err)
	}
	if err := agent.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// TestBuildConfigMissingID proves the required --id is enforced.
//
// Mutation: remove the `if *id == "" { return ... }` check. err becomes nil and
// the assertion fails.
func TestBuildConfigMissingID(t *testing.T) {
	var stderr bytes.Buffer
	_, err := buildConfig([]string{"--region", "us-east-1"}, &stderr)
	if err == nil {
		t.Fatal("expected error for missing --id")
	}
	if !strings.Contains(err.Error(), "--id") {
		t.Errorf("error = %q, want mention of --id", err.Error())
	}
}

// TestBuildConfigRejectsOutOfRangePort proves bound checking on ports.
//
// Mutation: remove the `*bindPort < 0 || *bindPort > 65535` check. err becomes
// nil and the assertion fails.
func TestBuildConfigRejectsOutOfRangePort(t *testing.T) {
	var stderr bytes.Buffer
	_, err := buildConfig([]string{"--id", "node-C", "--bind-port", "70000"}, &stderr)
	if err == nil {
		t.Fatal("expected error for out-of-range --bind-port")
	}
	if !strings.Contains(err.Error(), "bind-port") {
		t.Errorf("error = %q, want mention of bind-port", err.Error())
	}
}

// --- run with a real node.Agent: real lifecycle --------------------------

// TestRunRealAgentLifecycle exercises run() end-to-end against a REAL
// node.Agent bound to 127.0.0.1:0 (host-safe ephemeral UDP). It proves:
//   - the agent reaches StatusHealthy (which node.Agent sets ONLY after a
//     successful discovery registration), printed to stdout, and
//   - cancelling the context drives a clean graceful shutdown (exit code 0).
//
// Mutation: in run(), delete the `<-ctx.Done()` line so run returns before
// Start's effects are observed / before honoring cancellation. The test would
// then race the "started ... status=healthy" / "shutting down" output and the
// exit-code-0-after-cancel ordering, and the status assertion below fails
// because run returns 0 immediately without ever blocking on the context.
func TestRunRealAgentLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	factory := func(cfg *node.Config) (agentLike, error) {
		// Bind ephemeral ports so the test never collides with a real agent.
		cfg.SwimBindPort = 0
		cfg.WgListenPort = 0
		return node.NewAgent(cfg)
	}

	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--id", "node-real", "--region", "us-east-1", "--wg-key", testKey(t)},
			factory, &stdout, &stderr)
	}()

	// Wait until the agent reports it started healthy, then cancel.
	deadline := time.After(5 * time.Second)
	for !strings.Contains(stdout.String(), "status=healthy") {
		select {
		case <-deadline:
			t.Fatalf("agent never reported healthy; stdout=%q stderr=%q", stdout.String(), stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after context cancel (graceful shutdown hung)")
	}

	out := stdout.String()
	if !strings.Contains(out, "id=node-real") {
		t.Errorf("stdout missing id=node-real: %q", out)
	}
	if !strings.Contains(out, "status=healthy") {
		t.Errorf("stdout missing status=healthy: %q", out)
	}
	if !strings.Contains(out, "shutting down...") {
		t.Errorf("stdout missing shutdown line: %q", out)
	}
}

// --- run with a fake agent: ordering & error propagation -----------------

type fakeAgent struct {
	startErr  error
	stopErr   error
	started   atomic.Bool
	stopped   atomic.Bool
	stopAfter func() // recorded ordering hook

	mu     sync.Mutex
	status node.Status
}

func (f *fakeAgent) Start() error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started.Store(true)
	f.mu.Lock()
	f.status = node.StatusHealthy
	f.mu.Unlock()
	return nil
}

func (f *fakeAgent) Stop() error {
	f.stopped.Store(true)
	if f.stopAfter != nil {
		f.stopAfter()
	}
	return f.stopErr
}

func (f *fakeAgent) GetStatus() node.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

// TestRunStopsOnContextCancel proves Stop() is only invoked after the context
// is cancelled, never before — i.e. the agent keeps running until signalled.
//
// Mutation: in run(), move `agent.Stop()` to immediately after Start() (before
// `<-ctx.Done()`). Stop would then be observed already true while the context
// is still live, failing the "must not have stopped yet" assertion.
func TestRunStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fa := &fakeAgent{}
	factory := func(cfg *node.Config) (agentLike, error) { return fa, nil }

	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--id", "x", "--wg-key", testKey(t)}, factory, &stdout, &stderr)
	}()

	// Give run time to Start and block on ctx.Done.
	for i := 0; i < 200 && !fa.started.Load(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !fa.started.Load() {
		t.Fatal("agent never started")
	}
	if fa.stopped.Load() {
		t.Fatal("agent stopped before context cancellation")
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	if !fa.stopped.Load() {
		t.Fatal("agent was not stopped on shutdown")
	}
}

// TestRunStartErrorReturnsCode1 proves a Start() failure is propagated as a
// non-zero exit code and is NOT swallowed.
//
// Mutation: in run(), ignore the Start error (`_ = agent.Start()` and continue).
// run would block on ctx.Done instead of returning 1, so the code==1 assertion
// (with no cancel) times out / fails.
func TestRunStartErrorReturnsCode1(t *testing.T) {
	fa := &fakeAgent{startErr: errBoom{}}
	factory := func(cfg *node.Config) (agentLike, error) { return fa, nil }

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--id", "x", "--wg-key", testKey(t)},
		factory, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "start:") {
		t.Errorf("stderr missing start error: %q", stderr.String())
	}
	if fa.stopped.Load() {
		t.Error("Stop called even though Start failed")
	}
}

// TestRunBadArgsReturnsCode2 proves usage errors map to exit code 2 and never
// construct an agent.
//
// Mutation: in run(), return 0 instead of 2 in the buildConfig error branch.
// The code==2 assertion fails.
func TestRunBadArgsReturnsCode2(t *testing.T) {
	constructed := false
	factory := func(cfg *node.Config) (agentLike, error) {
		constructed = true
		return &fakeAgent{}, nil
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--region", "us-east-1"}, factory, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if constructed {
		t.Error("agent constructed despite invalid args")
	}
}

// TestRunStopErrorReturnsCode1 proves a Stop() failure during graceful shutdown
// surfaces as a non-zero exit code.
//
// Mutation: in run(), ignore the Stop error (`_ = agent.Stop(); return 0`). The
// code==1 assertion fails.
func TestRunStopErrorReturnsCode1(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: run starts, then immediately shuts down
	fa := &fakeAgent{stopErr: errBoom{}}
	factory := func(cfg *node.Config) (agentLike, error) { return fa, nil }

	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"--id", "x", "--wg-key", testKey(t)}, factory, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "stop error:") {
		t.Errorf("stderr missing stop error: %q", stderr.String())
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
