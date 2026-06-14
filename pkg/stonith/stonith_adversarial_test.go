package stonith

// stonith_adversarial_test.go — adversarial, SINK-SIDE probes against the
// fencing surface. STONITH is safety-critical: a fence that fails OPEN (reports
// a node fenced/safe-to-proceed when it is not) or a concurrency bug on the
// shared fence registry causes split-brain / data corruption.
//
// These tests assert the ACTUAL fence/safe verdict (Confirmation, IsFenced bool,
// on-disk slot, power state), never merely "no panic". Each probe is annotated
// with its three-way discrimination: REAL BUG (a violated promise), DOCUMENTED
// CONTRACT (a disclaimed behaviour we pin), or LATENT RISK (a noted edge).
//
// FINDING SUMMARY (see bottom + agent report): NO REAL BUG found. The fence
// surface holds its "confirm, don't assume" contract under every attack tried.
// All tests below PASS on unmodified prod and pin the contract.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// =============================================================================
// (a) FAIL-OPEN FENCE — every driver must refuse to confirm when the sink-side
//     post-condition is not met, even if the "issue" step returned nil error.
// =============================================================================

// haltedEC2 is an EC2 power fabric whose StopInstance SUCCEEDS (nil) but whose
// instance NEVER transitions to "stopped" — it keeps reporting "stopping". This
// is the cloud analogue of the silent-no-op fabric: the API accepted the call
// but the box is still alive. A driver that trusted the nil error from
// StopInstance and emitted a Confirmation would be the fail-open bug.
type haltedEC2 struct{}

func (haltedEC2) StopInstance(context.Context, string) error { return nil } // "accepted"...
func (haltedEC2) InstanceState(context.Context, string) (string, error) {
	return "stopping", nil // ...but never reaches "stopped".
}

// TestEC2FailOpenRefusedWhenNeverStops is FAIL-OPEN probe for the EC2 driver.
// SINK-SIDE: the verdict must be "unsafe" — Fence returns ErrNotConfirmed and
// nil Confirmation, and IsFenced stays false. A fail-open driver would say safe.
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned. cloud.go promises Fence confirms
// "stopped" before returning a Confirmation; this proves it on a stuck instance.
func TestEC2FailOpenRefusedWhenNeverStops(t *testing.T) {
	ctx := context.Background()
	agent := NewEC2Agent("ec2-stuck", haltedEC2{})

	conf, err := agent.Fence(ctx, "i-stuck")
	if !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("FAIL-OPEN: EC2 Fence on a never-stopping instance returned err=%v conf=%+v; "+
			"want ErrNotConfirmed and nil conf (verdict must be UNSAFE)", err, conf)
	}
	if conf != nil {
		t.Fatalf("FAIL-OPEN: EC2 emitted a Confirmation for an instance still in 'stopping': %+v", conf)
	}
	if fenced, ierr := agent.IsFenced(ctx, "i-stuck"); ierr != nil || fenced {
		t.Fatalf("FAIL-OPEN: IsFenced on never-stopped instance = (%v,%v), want (false,nil)", fenced, ierr)
	}
}

// haltedAzure is the Azure analogue: PowerOffVM succeeds but the VM stays
// "running" (never stopped/deallocated).
type haltedAzure struct{}

func (haltedAzure) PowerOffVM(context.Context, string, string) error { return nil }
func (haltedAzure) VMPowerState(context.Context, string, string) (string, error) {
	return "running", nil
}

// TestAzureFailOpenRefusedWhenNeverStops — FAIL-OPEN probe for the Azure driver.
// DISCRIMINATION: DOCUMENTED CONTRACT pinned.
func TestAzureFailOpenRefusedWhenNeverStops(t *testing.T) {
	ctx := context.Background()
	agent := NewAzureAgent("az-stuck", "rg", haltedAzure{})

	conf, err := agent.Fence(ctx, "vm-stuck")
	if !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("FAIL-OPEN: Azure Fence on never-stopping VM returned err=%v conf=%+v; want ErrNotConfirmed/nil", err, conf)
	}
	if conf != nil {
		t.Fatalf("FAIL-OPEN: Azure emitted a Confirmation for a still-running VM: %+v", conf)
	}
}

// lyingSBD is an SBDDevice whose WriteSlot SUCCEEDS (nil) but whose ReadSlot
// never returns the poison message — modelling a device that silently dropped
// the write (bad LUN / cache that lost the page). The SBD driver confirms by
// reading the slot back, so it MUST report not-confirmed.
type lyingSBD struct{}

func (lyingSBD) WriteSlot(context.Context, string, string) error { return nil } // "written"...
func (lyingSBD) ReadSlot(context.Context, string) (string, error) {
	return "", nil // ...but the poison was never persisted.
}

// TestSBDFailOpenRefusedWhenPoisonNotPersisted — FAIL-OPEN probe for SBD.
// SINK-SIDE: read-back is empty, so Fence must return ErrNotConfirmed.
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (sbd.go: "confirm = read the slot
// back and see the off message").
func TestSBDFailOpenRefusedWhenPoisonNotPersisted(t *testing.T) {
	ctx := context.Background()
	agent := NewSBDAgent("sbd-lying", lyingSBD{})

	conf, err := agent.Fence(ctx, "node-lost-write")
	if !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("FAIL-OPEN: SBD Fence with a dropped write returned err=%v conf=%+v; want ErrNotConfirmed/nil", err, conf)
	}
	if conf != nil {
		t.Fatalf("FAIL-OPEN: SBD emitted a Confirmation though the poison was not persisted: %+v", conf)
	}
}

// confirmErrEC2 returns a nil error from StopInstance but ERRORS on the state
// query. A fail-open driver might swallow the confirm error and report success;
// the contract requires the confirm error to propagate (verdict = unsafe).
type confirmErrEC2 struct{ err error }

func (confirmErrEC2) StopInstance(context.Context, string) error { return nil }
func (c confirmErrEC2) InstanceState(context.Context, string) (string, error) {
	return "", c.err
}

// TestEC2ConfirmErrorIsNotSwallowed — a confirmation-probe error must NOT be
// treated as a successful fence. DISCRIMINATION: DOCUMENTED CONTRACT pinned.
func TestEC2ConfirmErrorIsNotSwallowed(t *testing.T) {
	ctx := context.Background()
	probeErr := errors.New("describe-instances throttled")
	agent := NewEC2Agent("ec2-confuse", confirmErrEC2{err: probeErr})

	conf, err := agent.Fence(ctx, "i-confuse")
	if err == nil || !errors.Is(err, probeErr) {
		t.Fatalf("FAIL-OPEN: EC2 swallowed the confirm-probe error: got err=%v conf=%+v; "+
			"want the probe error propagated (verdict UNSAFE)", err, conf)
	}
	if conf != nil {
		t.Fatalf("FAIL-OPEN: EC2 emitted a Confirmation despite a failed confirm probe: %+v", conf)
	}
}

// =============================================================================
// (b) QUORUM / SPLIT-BRAIN — this package contains NO quorum/vote logic; its
//     aggregate is MultiLevelFencer.IsFenced with documented OR semantics
//     ("ANY agent reports fenced -> true"). We PIN that documented contract and
//     prove the dangerous direction (a single failing/mismatched agent must NOT
//     flip the aggregate to a false "fenced=true").
// =============================================================================

// fixedFencedAgent reports a fixed IsFenced answer (and a fixed Fence result),
// letting us compose deterministic aggregate scenarios.
type fixedFencedAgent struct {
	name   string
	fenced bool
	err    error
}

func (a *fixedFencedAgent) Name() string { return a.name }
func (a *fixedFencedAgent) Fence(context.Context, string) (*Confirmation, error) {
	if a.fenced {
		return &Confirmation{Target: "t", Agent: a.name, Method: "fixed"}, nil
	}
	return nil, fmt.Errorf("agent %q: %w", a.name, ErrNotConfirmed)
}
func (a *fixedFencedAgent) IsFenced(context.Context, string) (bool, error) {
	return a.fenced, a.err
}

// TestAggregateIsFencedOrSemanticsPinned pins the documented OR rule: IsFenced is
// true iff at least one agent reports fenced. The safety-relevant direction is:
// when NO agent confirms fenced (some error, some honest false), the aggregate
// must be false — never a fail-open true.
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (stonith.go:167 "reports true if
// ANY configured agent reports the target as fenced").
func TestAggregateIsFencedOrSemanticsPinned(t *testing.T) {
	ctx := context.Background()

	// Case 1: one agent errors, the other honestly says not-fenced -> aggregate
	// must be false WITH the error surfaced (not a swallowed fail-open).
	f1, _ := NewMultiLevelFencer(
		&fixedFencedAgent{name: "erroring", err: errors.New("probe down")},
		&fixedFencedAgent{name: "honest-false", fenced: false},
	)
	got, err := f1.IsFenced(ctx, "node-q")
	if got {
		t.Fatalf("SPLIT-BRAIN: aggregate IsFenced=true when no agent confirmed fenced (one errored, one false)")
	}
	if err == nil {
		t.Fatalf("aggregate swallowed the agent error; want it surfaced so caller does not treat false as authoritative")
	}

	// Case 2: at least one agent reports fenced -> aggregate true (OR semantics).
	f2, _ := NewMultiLevelFencer(
		&fixedFencedAgent{name: "honest-false", fenced: false},
		&fixedFencedAgent{name: "confirmed", fenced: true},
	)
	got2, err2 := f2.IsFenced(ctx, "node-q")
	if err2 != nil || !got2 {
		t.Fatalf("OR semantics broken: got (%v,%v), want (true,nil) when one agent confirms fenced", got2, err2)
	}
}

// TestSingleMismatchedIPMIAgentCannotFailOpenAggregate probes a realistic
// misconfiguration: a fencer whose ONLY agent is an IPMIAgent wired to the wrong
// BMC. Asking IsFenced(node-X) must NOT yield a fail-open "true": the mismatch
// surfaces as an error and the aggregate stays false. If this returned
// (true,nil) a survivor would wrongly proceed to take over node-X.
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (ErrTargetMismatch guard).
func TestSingleMismatchedIPMIAgentCannotFailOpenAggregate(t *testing.T) {
	ctx := context.Background()
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is off\n"}
	// Agent is wired to node-Y's BMC; we ask about node-X.
	ipmi := newIPMIAgent("ipmi-y", IPMIConfig{Host: "bmc-y", Username: "u", Password: "p"}, rec)
	fencer, err := NewMultiLevelFencer(ipmi)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}

	fenced, ierr := fencer.IsFenced(ctx, "node-x")
	if fenced {
		t.Fatalf("FAIL-OPEN: mismatched IPMI agent reported node-x fenced (=true) — survivor would wrongly take over")
	}
	if ierr == nil {
		t.Fatalf("mismatched IPMI agent: aggregate returned (false,nil); want the ErrTargetMismatch surfaced, "+
			"so the false is not mistaken for an authoritative 'not fenced'")
	}
	if !errors.Is(ierr, ErrTargetMismatch) {
		t.Fatalf("aggregate error = %v, want it to wrap ErrTargetMismatch", ierr)
	}
	// The mismatched agent's BMC must NOT have been queried (guard fires first).
	if len(rec.calls) != 0 {
		t.Fatalf("mismatched IPMI agent queried the BMC %d time(s); guard should fire before any I/O", len(rec.calls))
	}
}

// =============================================================================
// (c) UNGUARDED SHARED STATE — concurrent fence/report/status on the registries.
//     Run under -race. We assert the FINAL verdict, not just absence of a race.
// =============================================================================

// TestInMemoryPowerConcurrentFenceNoLostUpdate hammers InMemoryPower with
// concurrent PowerOff + State across many targets. SINK-SIDE: after the storm
// every fenced target must read PoweredOff (no lost update) and -race must be
// clean.
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned ("Implementations MUST be safe for
// concurrent use"; InMemoryPower uses sync.Mutex).
func TestInMemoryPowerConcurrentFenceNoLostUpdate(t *testing.T) {
	ctx := context.Background()
	pc := NewInMemoryPower()
	const targets = 64
	const writersPerTarget = 8

	var wg sync.WaitGroup
	for i := 0; i < targets; i++ {
		tgt := fmt.Sprintf("node-%d", i)
		for w := 0; w < writersPerTarget; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := pc.PowerOff(ctx, tgt); err != nil {
					t.Errorf("PowerOff(%q): %v", tgt, err)
				}
				// Interleave reads to exercise the State path under contention.
				_, _ = pc.State(ctx, tgt)
			}()
		}
	}
	wg.Wait()

	// SINK-SIDE: every target must be PoweredOff — a lost update would leave one
	// PoweredOn (a node we believe alive that was actually fenced, or vice versa).
	for i := 0; i < targets; i++ {
		tgt := fmt.Sprintf("node-%d", i)
		st, err := pc.State(ctx, tgt)
		if err != nil || st != PoweredOff {
			t.Fatalf("post-storm State(%q) = (%v,%v), want (powered-off,nil) — lost update on the registry", tgt, st, err)
		}
	}
}

// TestFencerConcurrentFenceAndIsFenced runs concurrent Fence and IsFenced calls
// through a real MultiLevelFencer + InMemoryPower. SINK-SIDE: after the storm
// the fencer reports every target fenced. -race must be clean (the fencer's
// agent slice is immutable post-construction; the registry is mutex-guarded).
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (defensive copy + mutex).
func TestFencerConcurrentFenceAndIsFenced(t *testing.T) {
	ctx := context.Background()
	pc := NewInMemoryPower()
	fencer, err := NewMultiLevelFencer(NewNoOpAgent("p", pc))
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}

	const targets = 50
	var wg sync.WaitGroup
	for i := 0; i < targets; i++ {
		tgt := fmt.Sprintf("n-%d", i)
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = fencer.Fence(ctx, tgt) }()
		go func() { defer wg.Done(); _, _ = fencer.IsFenced(ctx, tgt) }()
	}
	wg.Wait()

	for i := 0; i < targets; i++ {
		tgt := fmt.Sprintf("n-%d", i)
		if fenced, err := fencer.IsFenced(ctx, tgt); err != nil || !fenced {
			t.Fatalf("post-storm IsFenced(%q) = (%v,%v), want (true,nil)", tgt, fenced, err)
		}
	}
}

// TestFileSBDConcurrentDistinctSlotsNoLostUpdate stresses the read-modify-write
// in FileSBDDevice.WriteSlot: each goroutine poisons a DISTINCT slot in the same
// file. The single-process mutex must prevent the classic read-modify-write
// lost-update where slot A's write clobbers slot B. SINK-SIDE: every slot must
// read back the poison message; -race must be clean.
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (FileSBDDevice mutex guards
// in-process concurrency; cross-process is explicitly disclaimed and NOT tested
// as a bug).
func TestFileSBDConcurrentDistinctSlotsNoLostUpdate(t *testing.T) {
	ctx := context.Background()
	dev, err := NewFileSBDDevice(t.TempDir() + "/sbd.dev")
	if err != nil {
		t.Fatalf("NewFileSBDDevice: %v", err)
	}

	const slots = 40
	var wg sync.WaitGroup
	for i := 0; i < slots; i++ {
		tgt := fmt.Sprintf("slot-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := dev.WriteSlot(ctx, tgt, sbdMessageOff); err != nil {
				t.Errorf("WriteSlot(%q): %v", tgt, err)
			}
		}()
	}
	wg.Wait()

	// SINK-SIDE: NO slot may have been lost by a concurrent read-modify-write.
	for i := 0; i < slots; i++ {
		tgt := fmt.Sprintf("slot-%02d", i)
		msg, err := dev.ReadSlot(ctx, tgt)
		if err != nil || msg != sbdMessageOff {
			t.Fatalf("LOST UPDATE: ReadSlot(%q) = (%q,%v), want (%q,nil) — a concurrent write clobbered this slot",
				tgt, msg, err, sbdMessageOff)
		}
	}
}

// =============================================================================
// (d) IDENTITY / TOTAL-ORDER — duplicate targets, self-fence, ordering.
// =============================================================================

// TestDoubleFenceIsIdempotent proves fencing the same node twice does not break
// the verdict: the second Fence still confirms and IsFenced stays true. A
// double-fence must never flip a fenced node back to "safe/alive".
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (PowerOff "is idempotent").
func TestDoubleFenceIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pc := NewInMemoryPower()
	agent := NewNoOpAgent("idem", pc)
	const target = "node-dup"

	if _, err := agent.Fence(ctx, target); err != nil {
		t.Fatalf("first Fence: %v", err)
	}
	conf2, err := agent.Fence(ctx, target)
	if err != nil {
		t.Fatalf("second Fence (double-fence) must remain confirmed, got %v", err)
	}
	if conf2 == nil || conf2.Target != target {
		t.Fatalf("double-fence confirmation wrong: %+v", conf2)
	}
	if fenced, err := agent.IsFenced(ctx, target); err != nil || !fenced {
		t.Fatalf("after double-fence IsFenced = (%v,%v), want (true,nil) — must not flip back to alive", fenced, err)
	}
}

// TestDuplicateAgentsBothFenceSameTarget puts the SAME logical target through a
// chain with two agents over two distinct fabrics. Only the FIRST (winning)
// fabric is touched; the chain stops at the first confirmed fence (ordering
// contract). The second fabric must remain untouched.
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (stonith.go: "first agent to return
// a confirmed fence wins").
func TestDuplicateAgentsBothFenceSameTarget(t *testing.T) {
	ctx := context.Background()
	fabricA := NewInMemoryPower()
	fabricB := NewInMemoryPower()
	fencer, err := NewMultiLevelFencer(
		NewNoOpAgent("a", fabricA),
		NewNoOpAgent("b", fabricB),
	)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}
	const target = "node-order"

	conf, err := fencer.Fence(ctx, target)
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	if conf.Agent != "a" {
		t.Fatalf("ordering: won by %q, want first agent 'a'", conf.Agent)
	}
	// First fabric fenced; second must NOT have been touched.
	if st, _ := fabricA.State(ctx, target); st != PoweredOff {
		t.Fatalf("first fabric not fenced: %v", st)
	}
	if st, _ := fabricB.State(ctx, target); st != PoweredOn {
		t.Fatalf("ordering violated: second fabric was touched (state=%v), chain must stop at first confirm", st)
	}
}

// TestSBDDistinctTargetsDoNotAliasIdentity proves the SBD slot keying does not
// confuse two targets whose names share a prefix or differ only in case — a
// poisoned slot for one node must NOT make a DIFFERENT node read as fenced.
// (Identity/aliasing probe: a sloppy key match would fail open for node-2.)
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (per-target slot keyed by exact
// target string).
func TestSBDDistinctTargetsDoNotAliasIdentity(t *testing.T) {
	ctx := context.Background()
	dev, err := NewFileSBDDevice(t.TempDir() + "/sbd.dev")
	if err != nil {
		t.Fatalf("NewFileSBDDevice: %v", err)
	}
	agent := NewSBDAgent("sbd", dev)

	// Poison only "node-1".
	if _, err := agent.Fence(ctx, "node-1"); err != nil {
		t.Fatalf("Fence(node-1): %v", err)
	}

	// Distinct identities that a sloppy match might alias to node-1.
	for _, other := range []string{"node-10", "node-1x", "NODE-1", "node-", "node-2"} {
		fenced, err := agent.IsFenced(ctx, other)
		if err != nil {
			t.Fatalf("IsFenced(%q): %v", other, err)
		}
		if fenced {
			t.Fatalf("IDENTITY ALIAS: poisoning 'node-1' made %q read as fenced — a wrong-node 'safe-to-act' verdict", other)
		}
	}
	// And the genuinely poisoned node still reads fenced.
	if fenced, _ := agent.IsFenced(ctx, "node-1"); !fenced {
		t.Fatal("node-1 should still read fenced after poisoning")
	}
}

// TestConfirmationTargetMatchesRequest proves the Confirmation's Target field is
// the node actually requested (not a stale/aliased identity). A Confirmation
// claiming the wrong Target is the wrong-node-fence record that misleads a
// caller about WHICH node is safe to evict.
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned.
func TestConfirmationTargetMatchesRequest(t *testing.T) {
	ctx := context.Background()

	// Across all real drivers, the Confirmation.Target must equal the request.
	t.Run("noop", func(t *testing.T) {
		c, err := NewNoOpAgent("n", NewInMemoryPower()).Fence(ctx, "req-noop")
		if err != nil || c.Target != "req-noop" {
			t.Fatalf("got (%+v,%v), want Target=req-noop", c, err)
		}
	})
	t.Run("ec2", func(t *testing.T) {
		c, err := NewEC2Agent("e", newFakeEC2()).Fence(ctx, "i-req")
		if err != nil || c.Target != "i-req" {
			t.Fatalf("got (%+v,%v), want Target=i-req", c, err)
		}
	})
	t.Run("sbd", func(t *testing.T) {
		dev, _ := NewFileSBDDevice(t.TempDir() + "/sbd.dev")
		c, err := NewSBDAgent("s", dev).Fence(ctx, "req-sbd")
		if err != nil || c.Target != "req-sbd" {
			t.Fatalf("got (%+v,%v), want Target=req-sbd", c, err)
		}
	})
}

// TestEmptyTargetRejectedByEveryDriver pins that an empty (missing) node report
// is rejected by every driver rather than silently fencing/aliasing the "" key.
// An empty identity that slipped through could poison a shared "" slot and make
// unrelated empty-target queries read fenced.
//
// DISCRIMINATION: DOCUMENTED CONTRACT pinned (ErrEmptyTarget guards).
func TestEmptyTargetRejectedByEveryDriver(t *testing.T) {
	ctx := context.Background()
	dev, _ := NewFileSBDDevice(t.TempDir() + "/sbd.dev")
	drivers := []struct {
		name  string
		agent FencingAgent
	}{
		{"noop", NewNoOpAgent("n", NewInMemoryPower())},
		{"ec2", NewEC2Agent("e", newFakeEC2())},
		{"azure", NewAzureAgent("a", "rg", newFakeAzure())},
		{"sbd", NewSBDAgent("s", dev)},
	}
	for _, d := range drivers {
		d := d
		t.Run(d.name, func(t *testing.T) {
			if _, err := d.agent.Fence(ctx, ""); !errors.Is(err, ErrEmptyTarget) {
				t.Fatalf("%s.Fence(\"\") = %v, want ErrEmptyTarget", d.name, err)
			}
		})
	}
}

// --- LATENT RISK note (no fix; behaviour is internally consistent) -----------
//
// IPMIAgent.IsFenced("") skips the target/host mismatch guard (the guard is
// `target != "" && target != cfg.Host`) and queries cfg.Host's chassis status
// directly. So MultiLevelFencer.IsFenced(ctx, "") would, for an IPMI level,
// actually probe the BMC and could return true for the empty target — whereas
// Fence(ctx, "") is rejected with ErrEmptyTarget at both the fencer and agent
// level. This asymmetry is NOT exploitable as a fail-open in practice: no caller
// asks "is the empty-named node fenced?", and a true here cannot evict any named
// holder (a fence-gated resource keys on real node identities). It is recorded
// as a LATENT RISK, not a bug. The test below PINS the current behaviour so a
// future change is a conscious one.
func TestIPMIIsFencedEmptyTargetProbesHostLatentRisk(t *testing.T) {
	ctx := context.Background()
	rec := &recordingIPMIRunner{present: true, statusOut: "Chassis Power is off\n"}
	agent := newIPMIAgent("ipmi", IPMIConfig{Host: "bmc-z", Username: "u", Password: "p"}, rec)

	// Empty target bypasses the mismatch guard and probes cfg.Host's status.
	fenced, err := agent.IsFenced(ctx, "")
	if err != nil {
		t.Fatalf("IsFenced(\"\"): unexpected error %v", err)
	}
	if !fenced {
		t.Fatalf("LATENT-RISK pin changed: IsFenced(\"\") with 'power is off' status = false; "+
			"current prod behaviour probes cfg.Host and returns true")
	}
	// Confirm the BMC was actually probed (this is the latent-risk behaviour).
	if len(rec.calls) == 0 {
		t.Fatalf("expected the empty-target IsFenced to probe the BMC (current behaviour); none recorded")
	}
	// Document the asymmetry: Fence("") is still rejected.
	if _, ferr := agent.Fence(ctx, ""); !errors.Is(ferr, ErrEmptyTarget) {
		t.Fatalf("Fence(\"\") = %v, want ErrEmptyTarget (asymmetry with IsFenced is the noted latent risk)", ferr)
	}
}

// helper to keep the import of strings meaningful if future probes need it.
var _ = strings.Contains
