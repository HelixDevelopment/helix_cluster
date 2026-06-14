package edge

// Adversarial sink-side probes for pkg/edge.
//
// This file is an untracked adversarial test artefact. It asserts SINK-SIDE
// behaviour (the actual admission decision / eligibility / conservation), not
// merely "no panic". Findings are classified inline as REAL BUG, DOCUMENTED
// CONTRACT (pinned), or LATENT RISK.

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (a) IDENTITY / BOUNDARY edge — TrustLevelUnknown admission.
//
// REAL BUG candidate. trust.go documents on TrustLevelUnknown (verbatim):
//
//	"A device with this level MUST NOT be admitted to any workload."
//
// restriction.go's Allowed() admits the workload classes AIInference,
// DataProcessing and SmallBatch "at all trust levels" UNCONDITIONALLY — it
// never gates on the trust level being a *valid* (non-zero, in-range) level.
// Therefore Allowed(TrustLevelUnknown, AIInference) returns Allowed==true,
// violating the explicit MUST-NOT promise on the zero value. The same code
// admits negative / out-of-range TrustLevel garbage (e.g. TrustLevel(-5)).
//
// SINK assertion: the returned AdmissionDecision.Allowed for an INVALID trust
// level must be false for EVERY workload type. This test FAILS on unfixed prod
// and is NOT gated behind an env var (per protocol step 6 — the coordinator
// applies the one-line fix that makes it pass by default).
//
// Minimal fix (apply in restriction.go Allowed(), at the top of the function):
//
//	if !level.IsValid() {
//	    return decision(false, fmt.Sprintf("invalid/unknown TrustLevel %d (DENIED by default)", int(level)))
//	}
func TestAdv_UnknownTrustLevel_MustNotBeAdmitted(t *testing.T) {
	allWorkloads := []WorkloadType{
		InteractiveSession, SensitiveData, PersistentStorage, NetworkRelay,
		AIInference, DataProcessing, SmallBatch,
	}
	// Invalid trust levels per TrustLevel.IsValid(): zero value and out-of-range.
	invalid := []TrustLevel{TrustLevelUnknown, TrustLevel(-5), TrustLevel(99)}

	for _, level := range invalid {
		if level.IsValid() {
			t.Fatalf("test setup error: %d should be invalid", int(level))
		}
		for _, w := range allWorkloads {
			d := Allowed(level, w)
			if d.Allowed {
				t.Errorf("SINK VIOLATION: Allowed(level=%d(%s), %v).Allowed = true; "+
					"trust.go contract says an invalid/unknown trust level MUST NOT be "+
					"admitted to ANY workload (reason=%q)",
					int(level), level, w, d.PolicyReason)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// CONTRACT PIN — valid-level matrix is unchanged by any fix.
//
// This locks the documented restriction matrix so a fix to the Unknown case
// cannot regress the legitimate four-level policy. Pure DOCUMENTED CONTRACT.
func TestAdv_ValidLevelMatrix_Pinned(t *testing.T) {
	type row struct {
		w        WorkloadType
		donor    bool
		semi     bool
		standard bool
		full     bool
	}
	matrix := []row{
		{InteractiveSession, false, false, true, true},
		{SensitiveData, false, false, false, true},
		{PersistentStorage, false, false, true, true},
		{NetworkRelay, false, false, true, true},
		{AIInference, true, true, true, true},
		{DataProcessing, true, true, true, true},
		{SmallBatch, true, true, true, true},
	}
	check := func(level TrustLevel, w WorkloadType, want bool) {
		if got := Allowed(level, w).Allowed; got != want {
			t.Errorf("Allowed(%s,%v).Allowed=%v want %v", level, w, got, want)
		}
	}
	for _, r := range matrix {
		check(EDGE_DONOR, r.w, r.donor)
		check(SEMI, r.w, r.semi)
		check(STANDARD, r.w, r.standard)
		check(FULL, r.w, r.full)
	}
}

// ---------------------------------------------------------------------------
// (b) NaN / TOTAL-ORDER — schedulerule numeric comparators.
//
// schedulerule.go evaluates CPUTempBelow as `state.CPUTempC < c.ThresholdA`.
// With a NaN metric value the comparison is false, so the condition is reported
// as FAILED. For a Required CPUTempBelow rule that means the device is ruled
// INELIGIBLE — the conservative / safe direction (a sensor returning NaN must
// not silently pass a thermal gate). This is the CORRECT, documented behaviour,
// so this is a CONTRACT PIN, not a bug. We pin it so a future "fix" can't make
// NaN pass a thermal limit.
func TestAdv_NaNTemperature_FailsThermalGate_Pinned(t *testing.T) {
	rules := []ScheduleRule{
		{Condition: Condition{Type: CPUTempBelow, ThresholdA: 80}, Required: true},
	}
	eligible, trace := Evaluate(rules, DeviceState{CPUTempC: math.NaN()})
	if eligible {
		t.Fatalf("SINK VIOLATION: NaN CPU temperature made device ELIGIBLE through a "+
			"Required CPUTempBelow(80) gate; NaN must fail the thermal limit. trace=%+v", trace)
	}
	if trace[0].Passed {
		t.Errorf("NaN temp: CPUTempBelow condition reported Passed=true, want false")
	}
	// +Inf temperature must also fail the gate.
	eligibleInf, _ := Evaluate(rules, DeviceState{CPUTempC: math.Inf(1)})
	if eligibleInf {
		t.Errorf("SINK VIOLATION: +Inf CPU temp passed CPUTempBelow(80) gate")
	}
}

// LATENT RISK pin: BatteryAbove truncates its threshold via int(c.ThresholdA).
// int(NaN) is implementation-defined-ish (Go yields 0 on amd64/arm64), so a NaN
// threshold silently degrades BatteryAbove(NaN) into BatteryAbove(0). Threshold
// values are package-controlled policy config (not untrusted runtime metrics)
// and the doc types them as integer percentages, so this is a LATENT RISK, not
// a promise-break. We pin the OBSERVED behaviour (a NaN threshold collapses to
// 0, so any non-zero battery passes) to surface it if the field is ever fed
// from untrusted input.
func TestAdv_NaNBatteryThreshold_LatentRisk_Pinned(t *testing.T) {
	rules := []ScheduleRule{
		{Condition: Condition{Type: BatteryAbove, ThresholdA: math.NaN()}, Required: true},
	}
	eligible, trace := Evaluate(rules, DeviceState{BatteryPct: 1})
	// Observed: int(NaN)==0, battery 1 > 0 → passes. Document the latent risk.
	if !eligible || !trace[0].Passed {
		t.Logf("NOTE: NaN battery threshold no longer collapses to 0 "+
			"(eligible=%v passed=%v) — behaviour changed; review LATENT RISK note",
			eligible, trace[0].Passed)
	}
}

// ---------------------------------------------------------------------------
// (c) IDENTITY — empty schedule-rule set.
//
// schedulerule.go documents: "eligible is true iff every Required rule passes."
// With zero rules that is vacuously true. CONTRACT PIN (documented), confirming
// an empty rule set does not accidentally deny.
func TestAdv_EmptyRules_VacuouslyEligible_Pinned(t *testing.T) {
	for _, rules := range [][]ScheduleRule{nil, {}} {
		eligible, trace := Evaluate(rules, DeviceState{})
		if !eligible {
			t.Errorf("empty rules: eligible=false, want true (vacuous)")
		}
		if len(trace) != 0 {
			t.Errorf("empty rules: trace len=%d, want 0", len(trace))
		}
	}
}

// ---------------------------------------------------------------------------
// (a') UNGUARDED SHARED MUTABLE STATE — concurrent Executor usage + conservation.
//
// Executor is a stateless concrete type; Run must be safe to call concurrently
// from many goroutines with no shared-map corruption and no lost results. We
// drive N concurrent units, each returning a unique payload, and assert
// CONSERVATION: every unit completes exactly once with its own correct output
// and a non-empty, distinct RunUUID. Run under -race this also proves no data
// race in the duration/memory bookkeeping. SINK = the set of observed outputs,
// not "no panic".
func TestAdv_Executor_ConcurrentConservation_Race(t *testing.T) {
	const n = 200
	exec := NewExecutor()

	var (
		mu       sync.Mutex
		outputs  = make(map[string]string) // unitID -> output
		uuids    = make(map[string]struct{})
		uuidDups int64
		wg       sync.WaitGroup
	)

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			id := "unit-" + itoa(i)
			want := "payload-" + itoa(i)
			res, err := exec.Run(context.Background(), EdgeWorkUnit{ID: id, MaxMemoryMB: 64},
				func(ctx context.Context) ([]byte, error) {
					return []byte(want), nil
				})
			if err != nil {
				t.Errorf("unit %s: unexpected err %v", id, err)
				return
			}
			mu.Lock()
			outputs[res.UnitID] = string(res.Output)
			if _, dup := uuids[res.RunUUID]; dup {
				atomic.AddInt64(&uuidDups, 1)
			}
			uuids[res.RunUUID] = struct{}{}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// CONSERVATION: exactly n units, each mapped to its own payload.
	if len(outputs) != n {
		t.Fatalf("conservation broken: got %d distinct unit results, want %d "+
			"(lost or clobbered results)", len(outputs), n)
	}
	for i := 0; i < n; i++ {
		id := "unit-" + itoa(i)
		want := "payload-" + itoa(i)
		if got := outputs[id]; got != want {
			t.Errorf("unit %s: output=%q want %q (cross-unit clobber)", id, got, want)
		}
	}
	// RunUUID is documented as a per-execution identifier; collisions would
	// break log correlation. (Reported, not hard-failed: the prod UUID is
	// time+goroutine based and could in principle collide — LATENT RISK.)
	if uuidDups > 0 {
		t.Logf("LATENT RISK: %d RunUUID collisions across %d concurrent runs "+
			"(newRunUUID uses time.UnixNano+NumGoroutine, not crypto/rand)", uuidDups, n)
	}
}

// (d) BOUNDARY — duration limit hard-enforces and reports the right sink.
func TestAdv_Executor_DurationBoundary(t *testing.T) {
	exec := NewExecutor()
	// 1s hard limit; work blocks until its context is cancelled.
	res, err := exec.Run(context.Background(), EdgeWorkUnit{ID: "slow", MaxDurationSeconds: 1},
		func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	if !res.IsLimitExceeded() || res.LimitHit != LimitDuration {
		t.Fatalf("duration boundary: LimitHit=%q want %q (err=%v)", res.LimitHit, LimitDuration, err)
	}
	if res.Output != nil {
		t.Errorf("duration boundary: Output=%q, want nil on limit", res.Output)
	}
	// Zero limit means no limit: a fast unit must complete cleanly.
	fast, ferr := exec.Run(context.Background(), EdgeWorkUnit{ID: "fast", MaxDurationSeconds: 0},
		func(ctx context.Context) ([]byte, error) { return []byte("ok"), nil })
	if ferr != nil || fast.IsLimitExceeded() || string(fast.Output) != "ok" {
		t.Errorf("zero-limit unit: out=%q hit=%q err=%v", fast.Output, fast.LimitHit, ferr)
	}
	_ = time.Now
}

// itoa is a tiny dependency-free int->string helper for unit IDs/payloads.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
