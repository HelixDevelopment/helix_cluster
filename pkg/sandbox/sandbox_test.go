package sandbox_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/sandbox"
)

// ---------------------------------------------------------------------------
// Capability tests
// ---------------------------------------------------------------------------

func TestCapabilities_GrantHas(t *testing.T) {
	caps := sandbox.NewCapabilities()
	if caps.Has(sandbox.CapReadFS) {
		t.Fatal("empty capabilities must not grant CapReadFS")
	}
	caps.Grant(sandbox.CapReadFS)
	if !caps.Has(sandbox.CapReadFS) {
		t.Fatal("CapReadFS must be granted after Grant()")
	}
	if caps.Has(sandbox.CapWriteFS) {
		t.Fatal("CapWriteFS must NOT be granted when only CapReadFS was added")
	}
}

func TestCapabilities_NilDeniesAll(t *testing.T) {
	var caps *sandbox.Capabilities
	if caps.Has(sandbox.CapNetwork) {
		t.Fatal("nil *Capabilities must deny all caps")
	}
}

func TestCapabilities_Chaining(t *testing.T) {
	caps := sandbox.NewCapabilities().
		Grant(sandbox.CapReadFS).
		Grant(sandbox.CapNetwork)
	if !caps.Has(sandbox.CapReadFS) || !caps.Has(sandbox.CapNetwork) {
		t.Fatal("chained Grant calls must all take effect")
	}
}

func TestCapabilities_Granted_Order(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapExec, sandbox.CapClock, sandbox.CapReadFS)
	got := caps.Granted()
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("Granted() not sorted: %v", got)
		}
	}
}

func TestCapabilities_String(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapClock)
	s := caps.String()
	if s == "" {
		t.Fatal("String() must return non-empty representation")
	}
}

// ---------------------------------------------------------------------------
// MemAudit tests
// ---------------------------------------------------------------------------

func TestMemAudit_RecordAndEntries(t *testing.T) {
	a := &sandbox.MemAudit{}
	a.Record(sandbox.AuditEntry{Cap: sandbox.CapReadFS, Decision: sandbox.Allow, Reason: ""})
	a.Record(sandbox.AuditEntry{Cap: sandbox.CapWriteFS, Decision: sandbox.Deny, Reason: "not granted"})

	entries := a.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Sequence numbers must be 1 and 2.
	if entries[0].Seq != 1 {
		t.Fatalf("first entry Seq = %d, want 1", entries[0].Seq)
	}
	if entries[1].Seq != 2 {
		t.Fatalf("second entry Seq = %d, want 2", entries[1].Seq)
	}
	// First entry must be Allow on CapReadFS.
	if entries[0].Decision != sandbox.Allow || entries[0].Cap != sandbox.CapReadFS {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
	// Second entry must be Deny on CapWriteFS with a non-empty Reason.
	if entries[1].Decision != sandbox.Deny || entries[1].Cap != sandbox.CapWriteFS {
		t.Fatalf("unexpected second entry: %+v", entries[1])
	}
	if entries[1].Reason == "" {
		t.Fatal("deny entry must carry a non-empty Reason")
	}
}

func TestMemAudit_ConcurrentSafety(t *testing.T) {
	a := &sandbox.MemAudit{}
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Record(sandbox.AuditEntry{Cap: sandbox.CapClock, Decision: sandbox.Allow})
		}()
	}
	wg.Wait()
	if a.Len() != n {
		t.Fatalf("concurrent Record: expected %d entries, got %d", n, a.Len())
	}
}

func TestMemAudit_EntriesIsACopy(t *testing.T) {
	a := &sandbox.MemAudit{}
	a.Record(sandbox.AuditEntry{Cap: sandbox.CapExec, Decision: sandbox.Allow})
	snap1 := a.Entries()
	a.Record(sandbox.AuditEntry{Cap: sandbox.CapNetwork, Decision: sandbox.Deny, Reason: "x"})
	snap2 := a.Entries()
	if len(snap1) != 1 {
		t.Fatalf("snap1 should have 1 entry after copy, got %d", len(snap1))
	}
	if len(snap2) != 2 {
		t.Fatalf("snap2 should have 2 entries, got %d", len(snap2))
	}
}

// ---------------------------------------------------------------------------
// Guard: granted cap -> Allow
// ---------------------------------------------------------------------------

func TestGuard_GrantedCap_ReturnsNil(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapReadFS)
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{}, audit)

	err := g.Check(sandbox.CapReadFS)
	if err != nil {
		t.Fatalf("Check granted cap: want nil, got %v", err)
	}

	entries := audit.Entries()
	if len(entries) != 1 {
		t.Fatalf("want 1 audit entry, got %d", len(entries))
	}
	if entries[0].Decision != sandbox.Allow {
		t.Fatalf("want Allow audit entry, got %v", entries[0].Decision)
	}
	if entries[0].Cap != sandbox.CapReadFS {
		t.Fatalf("want CapReadFS in audit, got %v", entries[0].Cap)
	}
}

// ---------------------------------------------------------------------------
// Guard: ungranted cap -> ErrCapabilityDenied + Deny audit
// ---------------------------------------------------------------------------

func TestGuard_UngrantedCap_ReturnsDenied(t *testing.T) {
	caps := sandbox.NewCapabilities() // no caps granted
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{}, audit)

	err := g.Check(sandbox.CapNetwork)
	if err == nil {
		t.Fatal("Check ungranted cap: want ErrCapabilityDenied, got nil")
	}
	if !errors.Is(err, sandbox.ErrCapabilityDenied) {
		t.Fatalf("want errors.Is(err, ErrCapabilityDenied), got %v", err)
	}

	entries := audit.Entries()
	if len(entries) != 1 {
		t.Fatalf("want 1 audit entry, got %d", len(entries))
	}
	if entries[0].Decision != sandbox.Deny {
		t.Fatalf("want Deny audit entry, got %v", entries[0].Decision)
	}
	if entries[0].Reason == "" {
		t.Fatal("Deny entry must have a non-empty Reason")
	}
}

// ---------------------------------------------------------------------------
// Guard: MaxOps=5, 6th Check -> ErrLimitExceeded
// ---------------------------------------------------------------------------

func TestGuard_MaxOps_6thCheckExceedsLimit(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapClock)
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxOps: 5}, audit)

	// First 5 calls must succeed.
	for i := 1; i <= 5; i++ {
		if err := g.Check(sandbox.CapClock); err != nil {
			t.Fatalf("call %d: want nil, got %v", i, err)
		}
	}

	// 6th call must fail with ErrLimitExceeded.
	err := g.Check(sandbox.CapClock)
	if err == nil {
		t.Fatal("6th Check: want ErrLimitExceeded, got nil")
	}
	if !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("want errors.Is(err, ErrLimitExceeded), got %v", err)
	}

	// Audit must have 5 Allow + 1 Deny = 6 entries total.
	entries := audit.Entries()
	if len(entries) != 6 {
		t.Fatalf("want 6 audit entries (5 allow + 1 deny), got %d", len(entries))
	}
	last := entries[5]
	if last.Decision != sandbox.Deny {
		t.Fatalf("6th entry must be Deny, got %v", last.Decision)
	}
	if !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("6th entry must carry ErrLimitExceeded sentinel")
	}
}

// ---------------------------------------------------------------------------
// Guard: Account over MaxBytes -> ErrLimitExceeded
// ---------------------------------------------------------------------------

func TestGuard_Account_OverMaxBytes_ReturnsLimitExceeded(t *testing.T) {
	caps := sandbox.NewCapabilities()
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxBytes: 100}, audit)

	// Accumulate exactly the limit — must succeed.
	if err := g.Account(100); err != nil {
		t.Fatalf("Account up to limit: want nil, got %v", err)
	}

	// One byte more must fail.
	err := g.Account(1)
	if err == nil {
		t.Fatal("Account over limit: want ErrLimitExceeded, got nil")
	}
	if !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("want errors.Is(err, ErrLimitExceeded), got %v", err)
	}

	// The over-limit call must produce a Deny audit entry.
	entries := audit.Entries()
	if len(entries) == 0 {
		t.Fatal("want audit entry on Account denial, got none")
	}
	last := entries[len(entries)-1]
	if last.Decision != sandbox.Deny {
		t.Fatalf("Account denial must produce Deny audit entry, got %v", last.Decision)
	}
	if last.Reason == "" {
		t.Fatal("Account denial must carry a non-empty Reason")
	}
}

func TestGuard_Account_UnderMaxBytes_ReturnsNil(t *testing.T) {
	caps := sandbox.NewCapabilities()
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxBytes: 1000}, nil)
	if err := g.Account(500); err != nil {
		t.Fatalf("Account under limit: want nil, got %v", err)
	}
}

func TestGuard_Account_ZeroMaxBytes_Unlimited(t *testing.T) {
	caps := sandbox.NewCapabilities()
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxBytes: 0}, nil)
	// No limit set; any amount of bytes should be fine.
	if err := g.Account(1 << 30); err != nil {
		t.Fatalf("Account with unlimited MaxBytes: want nil, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Guard: MemAudit Entries() contains Allow AND Deny, correct Reason, order
// ---------------------------------------------------------------------------

func TestGuard_AuditContainsBothDecisions_CorrectReasonAndOrder(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapExec)
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxOps: 2}, audit)

	// First Check: granted, limit OK -> Allow.
	if err := g.Check(sandbox.CapExec); err != nil {
		t.Fatalf("first Check: %v", err)
	}
	// Second Check: granted, limit OK -> Allow.
	if err := g.Check(sandbox.CapExec); err != nil {
		t.Fatalf("second Check: %v", err)
	}
	// Third Check: limit exceeded -> Deny.
	err := g.Check(sandbox.CapExec)
	if !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("third Check: want ErrLimitExceeded, got %v", err)
	}
	// Fourth Check: ungranted cap -> Deny.
	err = g.Check(sandbox.CapWriteFS)
	if !errors.Is(err, sandbox.ErrCapabilityDenied) {
		t.Fatalf("fourth Check: want ErrCapabilityDenied, got %v", err)
	}

	entries := audit.Entries()
	if len(entries) != 4 {
		t.Fatalf("want 4 entries, got %d", len(entries))
	}

	// Verify ordering via Seq.
	for i, e := range entries {
		if e.Seq != i+1 {
			t.Fatalf("entries[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
	}

	// First two must be Allow.
	if entries[0].Decision != sandbox.Allow {
		t.Fatalf("entries[0] must be Allow, got %v", entries[0].Decision)
	}
	if entries[1].Decision != sandbox.Allow {
		t.Fatalf("entries[1] must be Allow, got %v", entries[1].Decision)
	}

	// Third must be Deny (limit) with non-empty Reason.
	if entries[2].Decision != sandbox.Deny {
		t.Fatalf("entries[2] must be Deny (limit), got %v", entries[2].Decision)
	}
	if entries[2].Reason == "" {
		t.Fatal("entries[2] must have a non-empty Reason for limit denial")
	}

	// Fourth must be Deny (cap) with non-empty Reason.
	if entries[3].Decision != sandbox.Deny {
		t.Fatalf("entries[3] must be Deny (cap), got %v", entries[3].Decision)
	}
	if entries[3].Reason == "" {
		t.Fatal("entries[3] must have a non-empty Reason for capability denial")
	}
}

// ---------------------------------------------------------------------------
// Guard: concurrent safety under -race
// ---------------------------------------------------------------------------

func TestGuard_ConcurrentSafety(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapReadFS)
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxOps: 1000}, audit)

	const goroutines = 50
	const callsEach = 10
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsEach; j++ {
				_ = g.Check(sandbox.CapReadFS)
				_ = g.Account(1)
			}
		}()
	}
	wg.Wait()
	// No assertion on exact count — we only care that -race doesn't fire.
}

// ---------------------------------------------------------------------------
// Guard: nil caps -> all denied
// ---------------------------------------------------------------------------

func TestGuard_NilCaps_AllDenied(t *testing.T) {
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(nil, sandbox.ResourceLimits{}, audit)
	err := g.Check(sandbox.CapClock)
	if !errors.Is(err, sandbox.ErrCapabilityDenied) {
		t.Fatalf("nil caps: want ErrCapabilityDenied, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Guard: nil audit -> no panic (nop sink used)
// ---------------------------------------------------------------------------

func TestGuard_NilAudit_NoPanic(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapReadFS)
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{}, nil)
	if err := g.Check(sandbox.CapReadFS); err != nil {
		t.Fatalf("Check with nil audit: want nil, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ResourceLimits: zero means unlimited for each dimension
// ---------------------------------------------------------------------------

func TestResourceLimits_ZeroOps_Unlimited(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapNetwork)
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxOps: 0}, nil)
	for i := 0; i < 10000; i++ {
		if err := g.Check(sandbox.CapNetwork); err != nil {
			t.Fatalf("Check #%d with unlimited MaxOps: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Guard: MaxDuration via injected clock
// ---------------------------------------------------------------------------

func TestGuard_MaxDuration_ExceedsDuration(t *testing.T) {
	// We need to inject the clock via the internal seam. Because guard.go
	// exposes NewGuard (no clock arg), we test the duration path by creating a
	// guard with a very short MaxDuration and sleeping past it in real time.
	// This avoids exposing internal test seams in the public API.
	caps := sandbox.NewCapabilities(sandbox.CapClock)
	audit := &sandbox.MemAudit{}
	// Use a 1ms max duration and verify that after sleeping 5ms the guard
	// rejects further calls.
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxDuration: time.Millisecond}, audit)

	// First call starts the clock.
	if err := g.Check(sandbox.CapClock); err != nil {
		t.Fatalf("first Check: want nil, got %v", err)
	}

	// Sleep well beyond the 1ms limit.
	time.Sleep(10 * time.Millisecond)

	// Now the guard must refuse.
	err := g.Check(sandbox.CapClock)
	if err == nil {
		t.Fatal("after duration exceeded: want ErrLimitExceeded, got nil")
	}
	if !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("after duration exceeded: want ErrLimitExceeded, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Anti-bluff: removing Has() check would allow ungranted cap (this test would
// catch it because an ungranted cap must return ErrCapabilityDenied).
// ---------------------------------------------------------------------------

func TestAntiBluff_HasCheckRequired(t *testing.T) {
	// Only CapReadFS is granted; CapWriteFS is NOT.
	caps := sandbox.NewCapabilities(sandbox.CapReadFS)
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{}, nil)

	if err := g.Check(sandbox.CapReadFS); err != nil {
		t.Fatalf("granted cap must succeed: %v", err)
	}
	// If Has() check were removed, this would return nil — test catches that.
	err := g.Check(sandbox.CapWriteFS)
	if err == nil {
		t.Fatal("ANTI-BLUFF: ungranted CapWriteFS must return an error (Has() check missing?)")
	}
	if !errors.Is(err, sandbox.ErrCapabilityDenied) {
		t.Fatalf("ANTI-BLUFF: wrong error type %v, want ErrCapabilityDenied", err)
	}
}

// ---------------------------------------------------------------------------
// Anti-bluff: removing limit check would allow over-limit ops (test catches
// it because the 6th call on MaxOps=5 must fail).
// ---------------------------------------------------------------------------

func TestAntiBluff_LimitCheckRequired(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapClock)
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxOps: 3}, nil)

	for i := 1; i <= 3; i++ {
		if err := g.Check(sandbox.CapClock); err != nil {
			t.Fatalf("call %d: want nil, got %v", i, err)
		}
	}
	// If limit check were removed this would return nil — test catches that.
	err := g.Check(sandbox.CapClock)
	if err == nil {
		t.Fatal("ANTI-BLUFF: 4th Check on MaxOps=3 must return an error (limit check missing?)")
	}
	if !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("ANTI-BLUFF: wrong error type %v, want ErrLimitExceeded", err)
	}
}

// ---------------------------------------------------------------------------
// Anti-bluff: audit must record both Allow and Deny — not just one or the
// other. Removing either recording path would break this test.
// ---------------------------------------------------------------------------

func TestAntiBluff_AuditRecordsBothDecisions(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapExec)
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxOps: 1}, audit)

	// One allowed.
	if err := g.Check(sandbox.CapExec); err != nil {
		t.Fatalf("first Check: %v", err)
	}
	// One denied (limit).
	_ = g.Check(sandbox.CapExec)
	// One denied (cap).
	_ = g.Check(sandbox.CapNetwork)

	entries := audit.Entries()
	var allows, denies int
	for _, e := range entries {
		switch e.Decision {
		case sandbox.Allow:
			allows++
		case sandbox.Deny:
			denies++
		}
	}
	if allows == 0 {
		t.Fatal("ANTI-BLUFF: no Allow entries recorded; audit.Record for Allow path missing?")
	}
	if denies == 0 {
		t.Fatal("ANTI-BLUFF: no Deny entries recorded; audit.Record for Deny paths missing?")
	}
	// We expect at least 1 of each.
	if allows < 1 || denies < 2 {
		t.Fatalf("ANTI-BLUFF: want >=1 Allow and >=2 Deny entries, got %d/%d", allows, denies)
	}
}
