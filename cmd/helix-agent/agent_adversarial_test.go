package main

// Adversarial / sink-side probes for the cmd/helix-agent runtime surface
// (run / buildConfig / printVersion). These complement the existing happy-path
// lifecycle tests by attacking the contract under concurrency, teardown, and
// malformed input.
//
// ANTI-HANG: no real network beyond loopback ephemeral UDP sockets (SwimBindPort
// = 0, WgListenPort = 0, WgNoOp = true via buildConfig), goroutine fan-out is
// capped at <= 8, and every test is bounded by `go test -timeout 90s`. No real
// SWIM/WireGuard cluster is ever stood up.
//
// Honest class mapping (cmd/helix-agent surface, NOT internal/node):
//
//   (a) UNGUARDED SHARED STATE — at the cmd level the only mutable state shared
//       across goroutines is the stdout/stderr io.Writer that run() writes to. We
//       drive several real ephemeral agents through run() concurrently against a
//       single shared writer while a reader polls it, and assert -race clean +
//       no lost/torn output. (The deeper agent-state-map / shared-pointer races
//       live in internal/node and are covered by internal/node's own
//       node_adversarial_test.go; they have no independent cmd-level surface, so
//       they are honestly scoped out here.)
//
//   (b) TEARDOWN RACE — run() blocks on <-ctx.Done() then calls agent.Stop().
//       We race ctx cancellation against the just-started real agent across the
//       full goroutine cap and assert: no panic (no send-on-closed / double-close
//       / use-after-free), and every run() returns exit code 0 (clean shutdown).
//
//   (c) FAIL-OPEN / VALIDATION — buildConfig is the sole arg gate. We pin BOTH
//       the rejections it DOES enforce (missing --id, out-of-range / negative
//       ports) AND a fail-open it does NOT (a whitespace-only --id is accepted).
//       The whitespace case is a LATENT RISK characterization, not a forced
//       failure: it documents current behavior so a future "trim + reject" fix
//       has a pin to flip.
//
//   (d) TOTAL-ORDER / DETERMINISM — buildConfig parsing identical argv must yield
//       a byte-identical Config (notably a stably-ordered EtcdEndpoints slice,
//       which is built by splitting a string, not by ranging a map), and
//       printVersion must be deterministic.

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/node"
)

// ephemeralFactory builds a REAL node.Agent but forces host-safe ephemeral
// ports so concurrent agents never collide and never touch a real cluster.
func ephemeralFactory(cfg *node.Config) (agentLike, error) {
	cfg.SwimBindPort = 0
	cfg.WgListenPort = 0
	return node.NewAgent(cfg)
}

// --- (a) UNGUARDED SHARED STATE: shared writer under concurrent run() --------

// TestAdversarial_SharedWriter_ConcurrentRun_RaceClean drives the cmd-level
// shared mutable state (the io.Writer passed to run) from multiple goroutines.
// run() is documented as a "fully testable" core that the existing lifecycle
// test already exercises from a goroutine via syncBuffer; here we push 6 real
// ephemeral agents through it simultaneously, all writing to ONE shared writer,
// while the test goroutine polls that writer. Under -race this proves the
// cmd-level write path has no data race and produces no torn output.
//
// SINK-SIDE assertion: -race clean (run with -race) AND every "started" line is
// intact (id=... appears exactly once per agent that announced), proving writes
// are not interleaved-corrupted.
//
// Mutation (illustrative — in test infra, not prod): replace syncBuffer's
// mutex-guarded Write with an unguarded *bytes.Buffer and -race reports a write
// race here. We rely on the real syncBuffer so this stays a clean contract pin.
func TestAdversarial_SharedWriter_ConcurrentRun_RaceClean(t *testing.T) {
	const n = 6 // <= 8 cap
	var shared syncBuffer
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("shared-%d", i)
			codes[i] = run(ctx, []string{"--id", id, "--wg-key", testKey(t)},
				ephemeralFactory, &shared, &shared)
		}(i)
	}

	// Poll the shared writer until all n agents have announced they started,
	// proving concurrent reads race concurrent writes on the same writer.
	deadline := time.After(20 * time.Second)
	for {
		out := shared.String()
		if strings.Count(out, "helix-agent started:") >= n {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d/%d agents announced; out=%q",
				strings.Count(out, "helix-agent started:"), n, out)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	wg.Wait()

	for i, c := range codes {
		if c != 0 {
			t.Errorf("agent %d exit code = %d, want 0", i, c)
		}
	}
	// Each agent's id must appear intact exactly once in a started line — no
	// torn writes mixing two agents' ids on one line.
	out := shared.String()
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("id=shared-%d ", i)
		if strings.Count(out, want) != 1 {
			t.Errorf("expected exactly one intact started line for %q, got %d; out=%q",
				want, strings.Count(out, want), out)
		}
	}
}

// --- (b) TEARDOWN RACE: ctx cancel vs just-started real agent ----------------

// TestAdversarial_TeardownRace_NoPanic races context cancellation against the
// real agent's start/poller/stop lifecycle across the full goroutine cap. The
// agent spawns a background resourcePoller goroutine and tears it down in Stop()
// via ctx.cancel + WaitGroup; cancelling the run ctx at an arbitrary instant
// after start must drive a clean Stop with no panic (no send-on-closed-channel,
// no double-close, no use-after-free) and exit code 0.
//
// SINK-SIDE assertion: no panic propagates (test would crash), and all runs
// return 0. We cancel at staggered tiny delays to widen the window where cancel
// overlaps Start's tail / the poller spin-up.
//
// Mutation (prod, illustrative): in node.Agent.Stop, removing `a.wgProc.Wait()`
// before the registry/protocol teardown would let the poller touch torn-down
// state; -race would then flag it. That mutation is in internal/node and proven
// by its own adversarial suite — here we assert the cmd-level invariant that a
// cancel-driven Stop never panics and always exits 0.
func TestAdversarial_TeardownRace_NoPanic(t *testing.T) {
	const n = 8 // cap
	var wg sync.WaitGroup
	results := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			// Cancel shortly after launch so cancellation overlaps the agent's
			// start tail / poller spin-up — the teardown-race window.
			go func() {
				time.Sleep(time.Duration(i) * time.Millisecond)
				cancel()
			}()
			var out, errb syncBuffer
			results[i] = run(ctx, []string{
				"--id", fmt.Sprintf("teardown-%d", i),
				"--wg-key", testKey(t),
			}, ephemeralFactory, &out, &errb)
			if results[i] != 0 {
				t.Errorf("agent %d: exit=%d stderr=%q", i, results[i], errb.String())
			}
		}(i)
	}
	// Bound the whole fan-out well under the 90s test timeout.
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(45 * time.Second):
		t.Fatal("teardown-race fan-out hung (graceful shutdown deadlock?)")
	}
}

// --- (c) FAIL-OPEN / VALIDATION ----------------------------------------------

// TestAdversarial_Validation_RejectsBadArgs pins the rejections buildConfig DOES
// enforce. These are the fail-CLOSED guarantees; flipping any one to accept is a
// CLAUDE-1 fail-open.
//
// Mutation: removing the `*id == ""` guard makes the missing-id case return
// err==nil and this subtest fails; removing the `*bindPort > 65535` guard makes
// the over-range case pass through and fails; removing `*wgPort < 0` likewise.
func TestAdversarial_Validation_RejectsBadArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // substring expected in the rejection error
	}{
		{"missing id", []string{"--region", "us-east-1"}, "--id"},
		{"bind-port over range", []string{"--id", "x", "--bind-port", "70000"}, "bind-port"},
		{"bind-port negative", []string{"--id", "x", "--bind-port", "-1"}, "bind-port"},
		{"wg-port over range", []string{"--id", "x", "--wg-port", "99999"}, "wg-port"},
		{"wg-port negative", []string{"--id", "x", "--wg-port", "-5"}, "wg-port"},
		{"unknown flag", []string{"--id", "x", "--nope", "1"}, "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := buildConfig(tc.args, &stderr)
			if err == nil {
				t.Fatalf("buildConfig(%v) accepted; want rejection", tc.args)
			}
			combined := err.Error() + stderr.String()
			if !strings.Contains(combined, tc.want) {
				t.Errorf("rejection for %v = %q, want substring %q", tc.args, combined, tc.want)
			}
		})
	}
}

// TestAdversarial_Validation_WhitespaceID_FailOpen is a LATENT-RISK
// characterization: buildConfig guards only `*id == ""`, so an --id made
// entirely of whitespace ("   ", "\t") is ACCEPTED and flows through to
// node.NewAgent (which also only checks == ""), registering the node under a
// blank-looking identifier.
//
// This test PINS current behavior (accepted) rather than forcing a failure: it
// is an honest, un-gated reproducer of the fail-open. If/when a "trim + reject"
// fix lands in buildConfig, flip the expectation in this test to require an
// error mentioning --id. Until then it documents the gap without breaking CI.
//
// Sink-side proof of the risk: the accepted config carries the raw whitespace
// ID verbatim (cfg.ID == the whitespace), i.e. nothing downstream sanitizes it.
func TestAdversarial_Validation_WhitespaceID_FailOpen(t *testing.T) {
	for _, ws := range []string{"   ", "\t", " \t "} {
		var stderr bytes.Buffer
		cfg, err := buildConfig([]string{"--id", ws}, &stderr)
		if err != nil {
			// A future hardening fix legitimately makes this an error; treat
			// that as the GOOD outcome and record it, don't fail.
			if strings.Contains(err.Error(), "--id") {
				t.Logf("HARDENED: whitespace id %q now rejected: %v", ws, err)
				continue
			}
			t.Fatalf("unexpected non-id error for %q: %v", ws, err)
		}
		// Current (unhardened) behavior: accepted verbatim — the fail-open.
		if cfg.ID != ws {
			t.Errorf("accepted whitespace id mutated: cfg.ID=%q, want verbatim %q", cfg.ID, ws)
		}
		if strings.TrimSpace(cfg.ID) != "" {
			t.Errorf("expected an effectively-blank id; got %q", cfg.ID)
		}
		t.Logf("FAIL-OPEN pinned: whitespace --id %q accepted (cfg.ID=%q, len=%d)", ws, cfg.ID, len(cfg.ID))
	}
}

// --- (d) TOTAL-ORDER / DETERMINISM -------------------------------------------

// TestAdversarial_BuildConfig_Deterministic proves buildConfig is a pure
// function of its argv+env: parsing the SAME argv repeatedly yields a Config
// that is identical in every field that argv controls, with a stably-ordered
// EtcdEndpoints slice. (EtcdEndpoints is built by Split on a string, so order is
// total and input-driven, never map-iteration-dependent.)
//
// We compare everything except WgPrivateKey, which is intentionally random when
// --wg-key is absent; here we PIN --wg-key so even that field is deterministic.
//
// Mutation: if buildConfig ever built EtcdEndpoints by ranging a map (nondet
// order), the endpoint-order assertion across iterations would flake/fail.
func TestAdversarial_BuildConfig_Deterministic(t *testing.T) {
	key := testKey(t)
	args := []string{
		"--id", "det-node",
		"--region", "eu-central-1",
		"--bind-addr", "127.0.0.1",
		"--bind-port", "7700",
		"--wg-port", "51820",
		"--wg-key", key,
		"--etcd-endpoints", "a:2379,b:2379,c:2379",
	}

	render := func() string {
		var stderr bytes.Buffer
		cfg, err := buildConfig(args, &stderr)
		if err != nil {
			t.Fatalf("buildConfig: %v (stderr=%q)", err, stderr.String())
		}
		return fmt.Sprintf("id=%s region=%s addr=%s bp=%d wgp=%d wgkey=%s noop=%v ttl=%s poll=%s etcd=%v",
			cfg.ID, cfg.Region, cfg.SwimBindAddr, cfg.SwimBindPort, cfg.WgListenPort,
			cfg.WgPrivateKey, cfg.WgNoOp, cfg.DiscoveryTTL, cfg.ResourcePollInterval, cfg.EtcdEndpoints)
	}

	first := render()
	for i := 0; i < 50; i++ {
		if got := render(); got != first {
			t.Fatalf("buildConfig nondeterministic on iter %d:\n first=%q\n   got=%q", i, first, got)
		}
	}
	// Endpoint order must match input order exactly (total order, input-driven).
	if !strings.Contains(first, "etcd=[a:2379 b:2379 c:2379]") {
		t.Errorf("EtcdEndpoints not in stable input order; rendered=%q", first)
	}
}

// TestAdversarial_PrintVersion_Deterministic proves printVersion emits a stable,
// greppable single line for fixed build vars — the CLAUDE-1 sink-side version
// proof must not vary run to run.
//
// Mutation: changing the format string drops the "version"/"build" tokens and
// the substring assertions fail.
func TestAdversarial_PrintVersion_Deterministic(t *testing.T) {
	var a, b bytes.Buffer
	printVersion(&a)
	printVersion(&b)
	if a.String() != b.String() {
		t.Fatalf("printVersion nondeterministic: %q vs %q", a.String(), b.String())
	}
	s := a.String()
	if !strings.Contains(s, "helix-agent version ") || !strings.Contains(s, "(build ") {
		t.Errorf("version line missing stable tokens: %q", s)
	}
	if !strings.HasSuffix(s, "\n") {
		t.Errorf("version line must be newline-terminated: %q", s)
	}
}
