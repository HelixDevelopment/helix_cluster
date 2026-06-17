package edge

// Second-wave adversarial sink-side probes for pkg/edge.
//
// The FIRST adversarial wave fixed a fail-open in Allowed(): an invalid/unknown
// TrustLevel was admitted to AIInference/DataProcessing/SmallBatch
// unconditionally. The fix added `if !level.IsValid() { deny }`.
//
// This wave hunts for what that first-pass fix MISSED:
//
//   - A SYMMETRIC/SECOND fail-open axis. pkg/edge has TWO admission gates:
//     Allowed() (workload-restriction) and Evaluate() (schedule-rule). The fix
//     hardened only the first. Does the second still fail open on an
//     invalid/unknown tier?
//   - Workload-axis completeness inside Allowed(): is EVERY required condition
//     checked, or can an unvalidated WorkloadType admit?
//   - Trust-ordering totality + MONOTONICITY: a lower level must never out-admit
//     a higher one; >= vs > correctness; off-by-one at the FULL boundary.
//   - Sink-side "denied means denied": the returned decision's Allowed flag must
//     agree with its own PolicyReason; no field disagreement.
//
// Findings are classified inline: REAL BUG, DOCUMENTED CONTRACT (pinned), or
// LATENT RISK (surfaced, non-failing).

import (
	"strings"
	"testing"
)

// validLevels is the set of real, in-range trust levels (excludes Unknown/zero).
var validLevels = []TrustLevel{EDGE_DONOR, SEMI, STANDARD, FULL}

// allWorkloads2 is the full set of defined workload types.
var allWorkloads2 = []WorkloadType{
	InteractiveSession, SensitiveData, PersistentStorage, NetworkRelay,
	AIInference, DataProcessing, SmallBatch,
}

// ---------------------------------------------------------------------------
// (1) SYMMETRIC SECOND FAIL-OPEN AXIS — schedule-rule gate on an invalid tier.
//
// LATENT RISK (surfaced, non-failing).
//
// The Allowed() fix gated invalid TrustLevel on the WORKLOAD-restriction gate.
// The OTHER admission gate in this package is the schedule-rule gate, whose
// canonical use is:
//
//	Evaluate(DefaultTierRules()[tier], state)
//
// DefaultTierRules() returns a map keyed by DeviceTier (= TrustLevel). A Go map
// lookup for an invalid/unknown tier (TrustLevelUnknown, TrustLevel(-5),
// TrustLevel(99)) returns the ZERO value: a nil []ScheduleRule. Evaluate(nil)
// is documented as "vacuously eligible". Composing the two, an INVALID tier ->
// no rules -> ELIGIBLE for scheduling. That is the symmetric twin of the
// Allowed() hole the first fix closed.
//
// Why LATENT RISK and not REAL BUG: there is currently NO production caller of
// DefaultTierRules()/Evaluate() outside this package's own tests (verified via
// repo grep), and both DefaultTierRules() (a static catalogue) and Evaluate()
// (vacuous-on-empty) have INDIVIDUALLY documented, pinned contracts. The hazard
// lives at the *composition* / call site, which does not exist yet. We surface
// the OBSERVED behaviour so that whoever wires the scheduler must add a
// fail-closed tier guard (mirroring Allowed's IsValid gate) rather than feeding
// a raw map lookup into Evaluate. If the observed behaviour ever changes
// (e.g. DefaultTierRules grows a deny-all default), this test logs a notice so
// the risk note can be revisited — it never hard-fails.
func TestAdv2_ScheduleGate_InvalidTier_LatentFailOpen(t *testing.T) {
	rules := DefaultTierRules()
	invalid := []TrustLevel{TrustLevelUnknown, TrustLevel(-5), TrustLevel(99)}

	for _, tier := range invalid {
		if tier.IsValid() {
			t.Fatalf("test setup: %d should be invalid", int(tier))
		}
		got, ok := rules[tier]
		// A happy-path state that satisfies all *advisory* conditions and would
		// satisfy required ones too — so eligibility hinges purely on whether any
		// required rule exists for this tier.
		happy := DeviceState{
			IsCharging: true, WifiConnected: true, BatteryPct: 100,
			CPUTempC: 10, BackgroundRefreshEnabled: true, LowPowerModeOff: true,
		}
		eligible, trace := Evaluate(got, happy)

		if !ok && eligible && len(trace) == 0 {
			// Confirmed latent fail-open: missing tier -> nil rules -> eligible.
			t.Logf("LATENT RISK confirmed: DefaultTierRules()[%d(invalid)] absent -> "+
				"nil rules -> Evaluate eligible=true (vacuous). A future scheduler "+
				"caller MUST add a fail-closed tier guard before Evaluate; do NOT "+
				"feed a raw map lookup for an unvalidated tier.", int(tier))
			continue
		}
		// If the observed behaviour ever changes, surface it loudly (still no fail).
		t.Logf("NOTICE: behaviour changed for invalid tier %d: present=%v eligible=%v "+
			"trace=%d — revisit the LATENT RISK note.", int(tier), ok, eligible, len(trace))
	}

	// Pin the complementary FACT that grounds the risk: every VALID tier IS
	// present in the catalogue (so the only nil-rule path is an invalid tier).
	for _, tier := range validLevels {
		if _, ok := rules[tier]; !ok {
			t.Errorf("catalogue gap: valid tier %s missing from DefaultTierRules()", tier)
		}
	}
}

// ---------------------------------------------------------------------------
// (2) WORKLOAD-AXIS COMPLETENESS — the OTHER input to Allowed() must fail closed.
//
// DOCUMENTED CONTRACT (pinned).
//
// The first fix validated the TrustLevel axis. The symmetric question: is the
// WorkloadType axis also validated? An unknown/zero/garbage WorkloadType must
// DENY (the switch default), at EVERY valid trust level — including FULL, which
// otherwise admits everything. This proves Allowed() checks BOTH axes, not just
// the one the first fix touched.
func TestAdv2_Allowed_UnknownWorkload_DeniedAtEveryValidLevel(t *testing.T) {
	badWorkloads := []WorkloadType{
		WorkloadType(0),   // zero value
		WorkloadType(-1),  // negative
		WorkloadType(8),   // one past SmallBatch (the last defined value)
		WorkloadType(999), // far out of range
	}
	for _, level := range validLevels {
		for _, w := range badWorkloads {
			d := Allowed(level, w)
			if d.Allowed {
				t.Errorf("SINK VIOLATION: Allowed(%s, WorkloadType(%d)).Allowed=true; "+
					"an unknown workload must DENY at every level (reason=%q)",
					level, int(w), d.PolicyReason)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// (3) MONOTONICITY of the trust ordering — a higher level must never out-DENY a
// lower one for the same workload. (>=, not a broken/partial ordering.)
//
// DOCUMENTED CONTRACT (pinned). A correct trust gate is monotone in trust: if a
// device is admitted at level L, every strictly-higher VALID level must also be
// admitted. A violation would mean adding trust REMOVES access — an
// ordering/off-by-one bug. This is exactly the "lower level satisfying a higher
// requirement / >= vs >" class the brief calls out, asserted as a property over
// every (workload x level-pair).
func TestAdv2_Allowed_MonotoneInTrust(t *testing.T) {
	// validLevels is ascending by construction (iota order). Assert that.
	for i := 1; i < len(validLevels); i++ {
		if !(validLevels[i] > validLevels[i-1]) {
			t.Fatalf("test setup: validLevels not strictly ascending at %d", i)
		}
	}
	for _, w := range allWorkloads2 {
		for i := 0; i < len(validLevels); i++ {
			lowAllowed := Allowed(validLevels[i], w).Allowed
			if !lowAllowed {
				continue
			}
			// Every strictly-higher level must ALSO be allowed.
			for j := i + 1; j < len(validLevels); j++ {
				if !Allowed(validLevels[j], w).Allowed {
					t.Errorf("NON-MONOTONE: %s allowed at %s but DENIED at higher %s "+
						"(adding trust removed access — broken ordering / off-by-one)",
						w, validLevels[i], validLevels[j])
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// (4) FULL-boundary exactness — SensitiveData must require EXACTLY FULL.
//
// DOCUMENTED CONTRACT (pinned). SensitiveData uses `level >= FULL`. Because FULL
// is the maximum valid level, that is equivalent to `== FULL`. Pin that ONLY
// FULL admits SensitiveData and that no other valid level (the highest non-FULL
// being STANDARD) sneaks through — guarding against a future off-by-one that
// loosens the >= to admit STANDARD, or an ordering change that inserts a level
// above FULL.
func TestAdv2_Allowed_SensitiveData_RequiresExactlyFull(t *testing.T) {
	for _, level := range validLevels {
		want := level == FULL
		got := Allowed(level, SensitiveData).Allowed
		if got != want {
			t.Errorf("SensitiveData at %s: Allowed=%v want %v (must be FULL-only)", level, got, want)
		}
	}
	// And the invalid extreme just above FULL must NOT be treated as >= FULL.
	if Allowed(TrustLevel(int(FULL)+1), SensitiveData).Allowed {
		t.Errorf("SINK VIOLATION: a forged level above FULL admitted SensitiveData "+
			"via the >= FULL check; IsValid gate must reject it first")
	}
}

// ---------------------------------------------------------------------------
// (5) SINK-SIDE SELF-CONSISTENCY — the decision's bool must agree with its own
// PolicyReason, across the ENTIRE input space (valid + invalid levels and
// workloads). A "reported denied but admitted" (or vice-versa) split-brain
// between the Allowed flag and the human-readable reason is a CLAUDE-1
// PASS-bluff. DOCUMENTED CONTRACT (pinned).
func TestAdv2_Allowed_DecisionSelfConsistent(t *testing.T) {
	levels := []TrustLevel{
		TrustLevelUnknown, TrustLevel(-5), TrustLevel(99),
		EDGE_DONOR, SEMI, STANDARD, FULL,
	}
	workloads := append([]WorkloadType{WorkloadType(0), WorkloadType(999)}, allWorkloads2...)

	for _, level := range levels {
		for _, w := range workloads {
			d := Allowed(level, w)
			reason := strings.ToUpper(d.PolicyReason)
			deniedWord := strings.Contains(reason, "DENIED") || strings.Contains(reason, "DENY")
			// The decision must echo back the exact inputs it judged.
			if d.TrustLevel != level {
				t.Errorf("decision dropped its input level: got %d want %d", int(d.TrustLevel), int(level))
			}
			if d.WorkloadType != w {
				t.Errorf("decision dropped its input workload: got %d want %d", int(d.WorkloadType), int(w))
			}
			// If Allowed==false, the reason must say so. If Allowed==true, the
			// reason must NOT contain a denial verb (no split-brain).
			if !d.Allowed && !deniedWord {
				t.Errorf("split-brain: Allowed=false but reason lacks a denial verb "+
					"(level=%d w=%d reason=%q)", int(level), int(w), d.PolicyReason)
			}
			if d.Allowed && deniedWord {
				t.Errorf("split-brain: Allowed=true but reason contains a denial verb "+
					"(level=%d w=%d reason=%q)", int(level), int(w), d.PolicyReason)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// (6) Cross-gate parity ASSERTION the first fix established and must keep:
// an invalid level denies EVERY workload on the Allowed() gate. (Distinct from
// wave-1's test, this checks the post-fix invariant as a clean property and
// guards the fix from regressing — it is the anchor the LATENT-RISK note in (1)
// argues should also hold on the schedule gate once a caller exists.)
func TestAdv2_Allowed_InvalidLevel_DeniesAllWorkloads(t *testing.T) {
	for _, level := range []TrustLevel{TrustLevelUnknown, TrustLevel(-1), TrustLevel(100)} {
		if level.IsValid() {
			t.Fatalf("test setup: %d should be invalid", int(level))
		}
		for _, w := range allWorkloads2 {
			if Allowed(level, w).Allowed {
				t.Errorf("REGRESSION: invalid level %d admitted %s; the IsValid "+
					"fail-closed gate must deny every workload", int(level), w)
			}
		}
	}
}
