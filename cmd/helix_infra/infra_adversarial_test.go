package main

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/infra"
)

// capturingOrch is an Orchestrator double whose ONLY job is to record whether
// (and with what value) the unsafe sinks — Scale / Logs — were actually
// reached. The whole point of the CLI's parseReplicas / parseTail guards is to
// ensure a malformed or out-of-range arg NEVER reaches the sink (where, with a
// real containerOrchestrator wired, a negative replicas slices current[n:] and
// panics — the bug that fired tonight). We assert sink-side: "was Scale called
// at all, and if so with what int" — not merely that an error came back.
type capturingOrch struct {
	mu sync.Mutex

	scaleCalled   bool
	scaleReplicas int

	logsCalled bool
	logsTail   int
}

func (c *capturingOrch) Boot(context.Context, []string) ([]infra.ServiceStatus, error) {
	return nil, nil
}
func (c *capturingOrch) Stop(context.Context, []string, bool) ([]infra.ServiceStatus, error) {
	return nil, nil
}
func (c *capturingOrch) Status(context.Context, []string) ([]infra.ServiceStatus, error) {
	return nil, nil
}
func (c *capturingOrch) Health(context.Context, []string) ([]infra.ServiceStatus, error) {
	return nil, nil
}
func (c *capturingOrch) Logs(_ context.Context, _ string, _ bool, tail int, _ string, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logsCalled = true
	c.logsTail = tail
	return nil
}
func (c *capturingOrch) Scale(_ context.Context, _ string, replicas int) (infra.ServiceStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scaleCalled = true
	c.scaleReplicas = replicas
	return infra.ServiceStatus{Message: "scaled"}, nil
}
func (c *capturingOrch) VMSpawn(_ context.Context, count int) ([]infra.VMNode, error) {
	nodes := make([]infra.VMNode, 0, count)
	for i := 0; i < count; i++ {
		nodes = append(nodes, infra.VMNode{ID: fmt.Sprintf("vm-%d", i)})
	}
	return nodes, nil
}
func (c *capturingOrch) VMDestroy(context.Context, string) error { return nil }
func (c *capturingOrch) VMList(context.Context) ([]infra.VMNode, error) {
	return nil, nil
}
func (c *capturingOrch) VMStatus(context.Context, string) (infra.VMNode, error) {
	return infra.VMNode{}, nil
}
func (c *capturingOrch) VMSSH(context.Context, string) (string, error)   { return "", nil }
func (c *capturingOrch) VMSimulateFailure(context.Context, string) error { return nil }
func (c *capturingOrch) VMSimulatePartition(context.Context, string, time.Duration) error {
	return nil
}
func (c *capturingOrch) Simulated() bool { return true }

// ---------------------------------------------------------------------------
// CLASS (a): ARG / PARSE BYPASS — prove a hostile replica string never reaches
// Scale. The tonight-bug sink (containerOrchestrator.Scale) slices current[n:]
// with n=replicas; a negative n panics. parseReplicas is the upstream gate.
// ---------------------------------------------------------------------------

func TestAdversarial_ScaleHostileArgsNeverReachSink(t *testing.T) {
	// Each of these MUST be rejected by parseReplicas BEFORE Scale is invoked.
	// Includes the int64 overflow string: strconv.Atoi returns MaxInt64 *with*
	// an error, so if parseReplicas ever consulted n before err it would feed a
	// giant positive int (or, for the negative-overflow string, MinInt64 — a
	// catastrophic negative slice low-bound) straight into the sink.
	hostile := []string{
		"-1",                            // plain negative -> would panic real Scale
		"-2147483648",                   // 32-bit min
		"-9223372036854775808",          // int64 min, exact
		"-9223372036854775809",          // int64 min - 1: Atoi clamps to MinInt64 + ERR
		"99999999999999999999999999",    // overflow: Atoi clamps to MaxInt64 + ERR
		"3.5",                           // float
		"0x10",                          // hex
		" 5",                            // leading space
		"5 ",                            // trailing space
		"",                              // empty
		"abc",                           // garbage
		"--3",                           // double sign
		"3_000",                         // underscore literal (Atoi rejects)
	}
	for _, arg := range hostile {
		arg := arg
		t.Run(strconv.Quote(arg), func(t *testing.T) {
			c := &capturingOrch{}
			var out bytes.Buffer
			code, err := runScale(context.Background(), c, "redis", arg, false, &out)
			if err == nil || code != 1 {
				t.Fatalf("hostile replicas %q: got code=%d err=%v; want code=1 + non-nil error (rejected pre-sink)", arg, code, err)
			}
			// SINK-SIDE proof: Scale must NOT have been called at all.
			if c.scaleCalled {
				t.Fatalf("BYPASS: hostile replicas %q reached Scale with replicas=%d (would panic a real containerOrchestrator: current[%d:])", arg, c.scaleReplicas, c.scaleReplicas)
			}
		})
	}
}

// Positive control: a legitimate value DOES reach Scale with the exact int, so
// the test above proves a real gate, not a Scale that is simply never wired.
func TestAdversarial_ScaleValidArgReachesSinkExactly(t *testing.T) {
	c := &capturingOrch{}
	var out bytes.Buffer
	code, err := runScale(context.Background(), c, "redis", "0", false, &out)
	if err != nil || code != 0 {
		t.Fatalf("valid replicas=0: code=%d err=%v, want 0/nil", code, err)
	}
	if !c.scaleCalled || c.scaleReplicas != 0 {
		t.Fatalf("valid replicas=0 must reach Scale exactly; scaleCalled=%v replicas=%d", c.scaleCalled, c.scaleReplicas)
	}
}

// Mirror of (a) for the --tail parser feeding Logs. A non-numeric/negative tail
// must never reach Logs (where, with a real backend, a negative tail could be a
// bad ring-buffer index).
func TestAdversarial_LogsHostileTailNeverReachesSink(t *testing.T) {
	hostile := []string{"-1", "-9223372036854775809", "99999999999999999999999999", "abc", "", "1.5", " 3"}
	for _, tail := range hostile {
		tail := tail
		t.Run(strconv.Quote(tail), func(t *testing.T) {
			c := &capturingOrch{}
			var out bytes.Buffer
			code, err := runLogs(context.Background(), c, "postgres", false, tail, "", false, &out)
			if err == nil || code != 1 {
				t.Fatalf("hostile --tail %q: code=%d err=%v; want code=1 + error", tail, code, err)
			}
			if c.logsCalled {
				t.Fatalf("BYPASS: hostile --tail %q reached Logs with tail=%d", tail, c.logsTail)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CLASS (b): VALIDATION / FAIL-OPEN — the default --tail ("100") and a normal
// value must parse; the empty default must NOT silently coerce to 0.
// ---------------------------------------------------------------------------

func TestAdversarial_TailDefaultAndBoundary(t *testing.T) {
	// The registered default flag value is the literal string "100" (logs.go).
	// Prove it is a valid, non-zero tail that reaches Logs as 100 — i.e. the
	// default is not an unsafe one.
	c := &capturingOrch{}
	var out bytes.Buffer
	if code, err := runLogs(context.Background(), c, "pg", false, "100", "", false, &out); err != nil || code != 0 {
		t.Fatalf("default tail 100: code=%d err=%v", code, err)
	}
	if !c.logsCalled || c.logsTail != 100 {
		t.Fatalf("default tail must reach Logs as 100; called=%v tail=%d", c.logsCalled, c.logsTail)
	}
	// tail=0 is the documented lower boundary (>= 0): it must be accepted.
	if _, err := parseTail("0"); err != nil {
		t.Fatalf("tail=0 is the documented boundary and must be accepted, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CLASS (c): UNGUARDED SHARED STATE — the run* action funcs take all state as
// parameters (no package globals touched), and the default orchestrator guards
// its maps with a sync.RWMutex. Drive many concurrent commands against ONE
// shared real simulated orchestrator under -race. Capped at 8 goroutines.
// A data race here would fail the -race build; a missing lock in pkg/infra
// would surface as a CLI-visible defect.
// ---------------------------------------------------------------------------

func TestAdversarial_ConcurrentCommandsRaceFree(t *testing.T) {
	orch := infra.NewOrchestrator(infra.DefaultConfig())
	const workers = 8
	const iters = 40
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			svc := fmt.Sprintf("svc-%d", id) // disjoint + shared-map contention
			var out bytes.Buffer
			for i := 0; i < iters; i++ {
				out.Reset()
				_, _ = runScale(context.Background(), orch, svc, strconv.Itoa(i%4), false, &out)
				out.Reset()
				_, _ = runStatus(context.Background(), orch, nil, false, &out)
				out.Reset()
				_, _ = runHealth(context.Background(), orch, nil, false, &out)
				out.Reset()
				_, _ = runVMSpawn(context.Background(), orch, 1, &out)
			}
		}(w)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// CLASS (d): TOTAL-ORDER / DETERMINISM — the CLI rendering layer (statusRows /
// healthRows / vmListRows) is pure and MUST preserve the exact input order it
// is handed, so identical input yields byte-identical output. (The map-order
// nondeterminism that exists when the SIMULATED orchestrator expands an empty
// service list lives in pkg/infra, OUTSIDE this target; the CLI itself does not
// iterate a map, so there is no determinism defect to fix here — pinned below.)
// ---------------------------------------------------------------------------

func TestAdversarial_RenderingIsOrderDeterministic(t *testing.T) {
	in := []infra.ServiceStatus{
		{Name: "zeta", Status: "running", Healthy: true, Ports: []string{"1"}},
		{Name: "alpha", Status: "stopped", Healthy: false, Ports: []string{"2"}},
		{Name: "mu", Status: "running", Healthy: true, Ports: []string{"3"}},
	}
	first := renderTable(statusHeaders, statusRows(in))
	for i := 0; i < 50; i++ {
		if got := renderTable(statusHeaders, statusRows(in)); got != first {
			t.Fatalf("statusRows rendering is nondeterministic across runs:\nfirst:\n%s\ngot:\n%s", first, got)
		}
	}
	// Row order must equal input order (zeta before alpha before mu), proving
	// the CLI does not reorder via a map.
	zi := strings.Index(first, "zeta")
	ai := strings.Index(first, "alpha")
	mi := strings.Index(first, "mu")
	if !(zi < ai && ai < mi) {
		t.Fatalf("rendered row order %d/%d/%d does not match input order zeta<alpha<mu:\n%s", zi, ai, mi, first)
	}
}
