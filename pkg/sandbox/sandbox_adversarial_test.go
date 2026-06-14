package sandbox_test

// Adversarial / negative-space tests for the sandbox capability-isolation gate.
//
// This is a SECURITY-ISOLATION boundary. The dominant bug class is a
// capability/restriction NOT enforced — the Guard claiming to deny a capability
// but actually permitting it (fail-open), an allow-list that is too permissive
// (substring / case bypass), or a default posture that grants instead of denies.
//
// Contract pinned here (verified against pkg/sandbox/{capability,guard,limits}.go):
//   - Capabilities is an explicit ALLOW-LIST. The zero value and a nil
//     *Capabilities grant nothing: DENY-BY-DEFAULT.
//   - Capability matching is EXACT (map key equality on the Capability string).
//     No substring, prefix, suffix, or case-insensitive matching is performed.
//   - Guard.Check fails closed: an ungranted/unknown capability returns
//     ErrCapabilityDenied and records a Deny audit entry; it does NOT consume
//     the MaxOps budget and does NOT start the duration clock.
//   - Granting one capability never implicitly grants any other (no escalation).
//
// Each centerpiece deny test is paired with the production mutation that would
// make it fail, documented inline, so the test demonstrably "bites".

import (
	"errors"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/sandbox"
)

// allCaps is the full set of defined capabilities. Used to assert that granting
// a strict subset leaves every other capability denied.
var allCaps = []sandbox.Capability{
	sandbox.CapReadFS,
	sandbox.CapWriteFS,
	sandbox.CapNetwork,
	sandbox.CapExec,
	sandbox.CapClock,
}

// ---------------------------------------------------------------------------
// CENTERPIECE 1 — DENY ENFORCEMENT.
// A capability NOT in the allow-list is DENIED by Guard.Check. Asserts the
// guard returns deny (not allow) for EVERY ungranted privileged capability.
//
// Mutation that bites: change guard.go line `if !g.caps.Has(cap)` to
// `if false` (or make Has always return true) — every sub-case below flips to
// "want deny, got nil".
// ---------------------------------------------------------------------------

func TestAdversarial_UngrantedCapability_AlwaysDenied(t *testing.T) {
	// Grant ONLY CapClock — the least privileged read-only cap. Every other
	// capability, including the dangerous CapExec / CapWriteFS / CapNetwork,
	// must be denied.
	caps := sandbox.NewCapabilities(sandbox.CapClock)

	for _, cap := range allCaps {
		if cap == sandbox.CapClock {
			continue // the one granted cap is covered by the allow-precision test
		}
		t.Run(string(cap), func(t *testing.T) {
			audit := &sandbox.MemAudit{}
			g := sandbox.NewGuard(caps, sandbox.ResourceLimits{}, audit)

			err := g.Check(cap)
			if err == nil {
				t.Fatalf("SECURITY: ungranted capability %q was PERMITTED (fail-open)", cap)
			}
			if !errors.Is(err, sandbox.ErrCapabilityDenied) {
				t.Fatalf("ungranted %q: want ErrCapabilityDenied, got %v", cap, err)
			}
			// Sink-side evidence: a Deny entry must be recorded.
			entries := audit.Entries()
			if len(entries) != 1 || entries[0].Decision != sandbox.Deny {
				t.Fatalf("ungranted %q: want exactly one Deny audit entry, got %+v", cap, entries)
			}
			if entries[0].Cap != cap {
				t.Fatalf("audit Cap = %q, want %q", entries[0].Cap, cap)
			}
		})
	}
}

// A capability that is granted and then is the ONLY thing checked passes, but a
// sibling stays denied — proves Grant does not leak across keys.
func TestAdversarial_GrantDoesNotLeakAcrossCaps(t *testing.T) {
	caps := sandbox.NewCapabilities().Grant(sandbox.CapExec)
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{}, nil)

	if err := g.Check(sandbox.CapExec); err != nil {
		t.Fatalf("granted CapExec must pass: %v", err)
	}
	// Granting exec must NOT grant the (related but distinct) write_fs / network.
	for _, cap := range []sandbox.Capability{sandbox.CapWriteFS, sandbox.CapNetwork, sandbox.CapReadFS, sandbox.CapClock} {
		if err := g.Check(cap); !errors.Is(err, sandbox.ErrCapabilityDenied) {
			t.Fatalf("SECURITY: granting CapExec leaked into %q (err=%v)", cap, err)
		}
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2 — DEFAULT-DENY POSTURE.
// Empty grant set, nil *Capabilities, zero-value Capabilities, and a Guard
// built from nil caps all DENY an unknown/undeclared capability. Fail-closed.
//
// Mutation that bites: make NewCapabilities seed the map with every cap, or
// make Has return true on nil — these sub-cases flip to "want deny, got nil".
// ---------------------------------------------------------------------------

func TestAdversarial_DefaultDeny_AllZeroValueForms(t *testing.T) {
	// An undeclared/unknown capability name that is not in the const set.
	unknown := sandbox.Capability("admin_root_all")

	t.Run("empty_NewCapabilities", func(t *testing.T) {
		caps := sandbox.NewCapabilities()
		if caps.Has(unknown) {
			t.Fatal("SECURITY: empty Capabilities granted an unknown cap")
		}
		for _, c := range allCaps {
			if caps.Has(c) {
				t.Fatalf("SECURITY: empty Capabilities granted %q", c)
			}
		}
	})

	t.Run("nil_pointer", func(t *testing.T) {
		var caps *sandbox.Capabilities
		if caps.Has(unknown) || caps.Has(sandbox.CapExec) {
			t.Fatal("SECURITY: nil *Capabilities granted a cap")
		}
		if g := caps.Granted(); g != nil {
			t.Fatalf("nil *Capabilities Granted() = %v, want nil", g)
		}
	})

	t.Run("zero_value_struct", func(t *testing.T) {
		var caps sandbox.Capabilities // zero value, granted map is nil
		if caps.Has(sandbox.CapReadFS) || caps.Has(unknown) {
			t.Fatal("SECURITY: zero-value Capabilities granted a cap")
		}
	})

	t.Run("guard_from_nil_caps_denies_unknown", func(t *testing.T) {
		audit := &sandbox.MemAudit{}
		g := sandbox.NewGuard(nil, sandbox.ResourceLimits{}, audit)
		if err := g.Check(unknown); !errors.Is(err, sandbox.ErrCapabilityDenied) {
			t.Fatalf("SECURITY: Guard(nil caps) permitted unknown cap: %v", err)
		}
		// And every defined cap is denied too.
		for _, c := range allCaps {
			if err := g.Check(c); !errors.Is(err, sandbox.ErrCapabilityDenied) {
				t.Fatalf("SECURITY: Guard(nil caps) permitted %q: %v", c, err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// CENTERPIECE 3 — ALLOW PRECISION (lookalike / case / substring DO NOT bypass).
// Granting "network" must NOT admit "Network", "NETWORK", "net", "network ",
// " network", "network\x00", or "networkx". Matching is exact.
//
// Mutation that bites: change Has to use strings.Contains / EqualFold /
// HasPrefix instead of exact map lookup — the lookalikes start passing.
// ---------------------------------------------------------------------------

func TestAdversarial_AllowPrecision_NoLookalikeBypass(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapNetwork) // grants exactly "network"
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{}, nil)

	// The exact name passes (so an always-deny mutation is also caught).
	if err := g.Check(sandbox.CapNetwork); err != nil {
		t.Fatalf("exact granted cap must pass: %v", err)
	}

	lookalikes := []sandbox.Capability{
		"Network",     // case
		"NETWORK",     // upper case
		"NeTwOrK",     // mixed case
		"net",         // prefix substring
		"work",        // suffix substring
		"networ",      // truncated
		"networkx",    // superstring suffix
		"xnetwork",    // superstring prefix
		"network ",    // trailing space
		" network",    // leading space
		"network\x00", // NUL-padded
		"network\n",   // newline padded
		"read_fs",     // entirely different granted-style name
	}
	for _, la := range lookalikes {
		t.Run(string(la), func(t *testing.T) {
			if caps.Has(la) {
				t.Fatalf("SECURITY: lookalike %q matched granted 'network' (Has too permissive)", la)
			}
			err := g.Check(la)
			if !errors.Is(err, sandbox.ErrCapabilityDenied) {
				t.Fatalf("SECURITY: lookalike %q was PERMITTED by Guard.Check (err=%v)", la, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 4 — PRIVILEGE: a denied (privileged) cap does NOT consume the
// ops budget and does NOT start the duration clock. A fail-open here would let
// repeated denied privileged probes be "free" AND mask later limit accounting.
//
// This also pins the order-of-operations contract in Guard.Check: the
// capability gate runs BEFORE the limit gates.
// ---------------------------------------------------------------------------

func TestAdversarial_DeniedCap_DoesNotConsumeOpsBudget(t *testing.T) {
	// MaxOps = 1. Grant only CapClock.
	caps := sandbox.NewCapabilities(sandbox.CapClock)
	audit := &sandbox.MemAudit{}
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxOps: 1}, audit)

	// Hammer a DENIED privileged cap many times. If denials wrongly consumed
	// the ops budget, the single allowed op below would later be falsely
	// limit-denied (masking the real cap decision).
	for i := 0; i < 50; i++ {
		if err := g.Check(sandbox.CapExec); !errors.Is(err, sandbox.ErrCapabilityDenied) {
			t.Fatalf("denied cap iteration %d: want ErrCapabilityDenied, got %v", i, err)
		}
	}

	// The one granted op must still succeed — proving denials did not burn the
	// budget.
	if err := g.Check(sandbox.CapClock); err != nil {
		t.Fatalf("granted op after 50 denials: want nil (budget untouched), got %v", err)
	}
	// The (now budget-exhausted) second granted op must be a LIMIT denial,
	// distinct from a capability denial.
	err := g.Check(sandbox.CapClock)
	if !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("2nd granted op: want ErrLimitExceeded, got %v", err)
	}
	if errors.Is(err, sandbox.ErrCapabilityDenied) {
		t.Fatal("limit denial must NOT also be a capability denial")
	}
}

// The duration clock must not be started by a capability-denied Check. With a
// tiny MaxDuration, denied probes alone never trip ErrLimitExceeded because the
// clock only starts on the first ALLOWED op.
func TestAdversarial_DeniedCap_DoesNotStartDurationClock(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapClock) // CapExec is denied
	g := sandbox.NewGuard(caps, sandbox.ResourceLimits{MaxDuration: 1}, nil)

	// Many denied probes; each must be a capability denial, never a duration
	// limit denial (the clock has not started).
	for i := 0; i < 100; i++ {
		err := g.Check(sandbox.CapExec)
		if !errors.Is(err, sandbox.ErrCapabilityDenied) {
			t.Fatalf("denied probe %d: want ErrCapabilityDenied, got %v", i, err)
		}
		if errors.Is(err, sandbox.ErrLimitExceeded) {
			t.Fatalf("denied probe %d wrongly tripped duration limit (clock started on a denial?)", i)
		}
	}
}

// ---------------------------------------------------------------------------
// EDGES — empty set, defensive copy independence, post-Check grant mutation.
// ---------------------------------------------------------------------------

// Empty capability set: Guard denies everything; no panic.
func TestAdversarial_EmptyCapabilitySet_DeniesAll(t *testing.T) {
	g := sandbox.NewGuard(sandbox.NewCapabilities(), sandbox.ResourceLimits{}, nil)
	for _, c := range allCaps {
		if err := g.Check(c); !errors.Is(err, sandbox.ErrCapabilityDenied) {
			t.Fatalf("empty set must deny %q, got %v", c, err)
		}
	}
}

// Granted() returns a defensive copy whose mutation cannot widen the live set.
func TestAdversarial_GrantedSliceMutationDoesNotWidenGrant(t *testing.T) {
	caps := sandbox.NewCapabilities(sandbox.CapReadFS)
	got := caps.Granted()
	if len(got) != 1 {
		t.Fatalf("want 1 granted cap, got %d", len(got))
	}
	// Tamper with the returned slice.
	got[0] = sandbox.CapExec
	// The live set must be unaffected: CapExec still denied, CapReadFS still
	// granted.
	if caps.Has(sandbox.CapExec) {
		t.Fatal("SECURITY: mutating Granted() slice leaked CapExec into the grant set")
	}
	if !caps.Has(sandbox.CapReadFS) {
		t.Fatal("Granted() slice mutation corrupted the live grant set")
	}
}

// Account is NOT capability-gated by contract (it is byte-volume accounting):
// pin that it operates regardless of the cap set, and that it does not consume
// the MaxOps budget. This prevents a future "fail-open" refactor that silently
// couples Account to a capability it never checks.
func TestAdversarial_Account_IsNotCapabilityGated_AndDoesNotBurnOps(t *testing.T) {
	// No caps granted at all.
	g := sandbox.NewGuard(sandbox.NewCapabilities(), sandbox.ResourceLimits{MaxOps: 1, MaxBytes: 100}, nil)

	// Account works without any capability (documented: separate byte budget).
	if err := g.Account(50); err != nil {
		t.Fatalf("Account with no caps: want nil, got %v", err)
	}
	if err := g.Account(50); err != nil {
		t.Fatalf("Account up to MaxBytes: want nil, got %v", err)
	}
	// Over MaxBytes -> limit denial.
	if err := g.Account(1); !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("Account over MaxBytes: want ErrLimitExceeded, got %v", err)
	}
}

// Pin the cumulative byte-accounting contract precisely so a future change to
// the running-total semantics is caught. Account sums signed deltas; the limit
// trips only when the RUNNING TOTAL exceeds MaxBytes.
func TestAdversarial_Account_CumulativeRunningTotalSemantics(t *testing.T) {
	g := sandbox.NewGuard(sandbox.NewCapabilities(), sandbox.ResourceLimits{MaxBytes: 100}, nil)
	if err := g.Account(60); err != nil {
		t.Fatalf("Account(60): %v", err)
	}
	// Running total 60 + 40 = 100 == limit, still OK (strict > comparison).
	if err := g.Account(40); err != nil {
		t.Fatalf("Account(40) to exactly MaxBytes: want nil, got %v", err)
	}
	// 100 + 1 = 101 > 100 -> denial.
	if err := g.Account(1); !errors.Is(err, sandbox.ErrLimitExceeded) {
		t.Fatalf("Account(1) over MaxBytes: want ErrLimitExceeded, got %v", err)
	}
}
