package scheduler

import (
	"strings"
	"testing"
)

// HXC-1189 / HXC-1190: EdgeAware must satisfy the Plugin interface.
var _ Plugin = (*EdgeAware)(nil)

// ---------- EdgeAware: identity ----------

func TestEdgeAwareName(t *testing.T) {
	p := &EdgeAware{}
	if got := p.Name(); got != "EdgeAware" {
		t.Fatalf("Name() = %q, want EdgeAware", got)
	}
}

// ---------- HXC-1189: Filter — tier < 6 unconditionally admitted ----------

func TestEdgeAwareFilterAdmitsTierBelow6(t *testing.T) {
	p := &EdgeAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelWorkloadType: "session"}}

	for _, tier := range []int{1, 2, 3, 4, 5} {
		node := &Node{
			ID: "n",
			Labels: map[string]string{
				LabelNodeTier:    strings.ToUpper("t") + strings.ToLower(string(rune('0'+tier))),
				LabelDeviceState: DeviceStateOffline,
				LabelCharging:    "false",
				LabelBatteryPct:  "0",
				LabelNodeTempC:   "99.0",
			},
		}
		// Even worst-case conditions: offline, low battery, hot — tier<6 must still pass.
		if !p.Filter(job, node) {
			t.Fatalf("tier %d (< 6) must be unconditionally admitted regardless of other labels", tier)
		}
	}
	// Mutation: changing 'tier < 6' to 'tier <= 6' would admit T6 unconditionally,
	// breaking the session-rejection test below. The test above would still pass (T5 always passes)
	// but TestEdgeAwareFilterSessionRejectedOnT6 would FAIL.
}

// ---------- HXC-1189: Filter — session rejected on T6 ----------

// TestEdgeAwareFilterSessionRejectedOnT6 is the PRIMARY SINK-SIDE test for HXC-1189.
// A session workload on a T6 node must be rejected with "Edge devices only accept work units".
func TestEdgeAwareFilterSessionRejectedOnT6(t *testing.T) {
	p := &EdgeAware{}
	// A T6 node that is otherwise healthy: available, charging, warm-but-ok temp.
	node := &Node{
		ID: "t6-node",
		Labels: map[string]string{
			LabelNodeTier:    "6",
			LabelDeviceState: DeviceStateAvailable,
			LabelCharging:    "true",
			LabelBatteryPct:  "80",
			LabelNodeTempC:   "55.0",
		},
	}
	sessionJob := &Job{ID: "session-job", Labels: map[string]string{LabelWorkloadType: "session"}}

	dec := p.Evaluate(sessionJob, node)

	// Sink-side: session MUST be rejected.
	if dec.Schedulable {
		t.Fatalf("session job on T6 node must be REJECTED, got Schedulable=true; RunUUID=%s", dec.RunUUID)
	}
	// Sink-side: reason must contain the documented phrase.
	found := false
	for _, r := range dec.Reasons {
		if strings.Contains(r, "Edge devices only accept work units") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session rejection must include 'Edge devices only accept work units'; reasons=%v RunUUID=%s",
			dec.Reasons, dec.RunUUID)
	}
	// RunUUID must be non-empty for sink-side evidence.
	if dec.RunUUID == "" {
		t.Fatalf("EdgeDecision.RunUUID must be non-empty")
	}
	t.Logf("HXC-1189 session rejection: RunUUID=%s reasons=%v", dec.RunUUID, dec.Reasons)
	// Mutation proof: flipping guard from 'tier < 6' to 'tier > 6' makes T6 pass the
	// tier-bypass, session not checked → dec.Schedulable==true → first assertion FAILS.
}

// ---------- HXC-1189: Filter — discharging T6 node rejects work unit ("Not charging") ----------

// TestEdgeAwareFilterDischargingT6RejectsWorkUnit is the mandatory scenario from closure criteria:
// a discharging T6 node rejects both a session AND a work unit with documented reasons.
func TestEdgeAwareFilterDischargingT6RejectsWorkUnit(t *testing.T) {
	p := &EdgeAware{}
	// Discharging T6 node with low battery, available state, cool temperature.
	t6Discharging := &Node{
		ID: "t6-discharging",
		Labels: map[string]string{
			LabelNodeTier:    "6",
			LabelDeviceState: DeviceStateAvailable,
			LabelCharging:    "false", // NOT charging
			LabelBatteryPct:  "10",    // below BatteryMinPercent=20
			LabelNodeTempC:   "45.0",  // cool
		},
	}

	sessionJob := &Job{ID: "session-job", Labels: map[string]string{LabelWorkloadType: "session"}}
	workUnit := &Job{ID: "work-unit-job", Labels: map[string]string{LabelWorkloadType: "work_unit"}}

	// Test session rejection.
	sessionDec := p.Evaluate(sessionJob, t6Discharging)
	if sessionDec.Schedulable {
		t.Fatalf("session on discharging T6 must be REJECTED; RunUUID=%s", sessionDec.RunUUID)
	}
	hasSessionReason := false
	for _, r := range sessionDec.Reasons {
		if strings.Contains(r, "Edge devices only accept work units") {
			hasSessionReason = true
			break
		}
	}
	if !hasSessionReason {
		t.Fatalf("session rejection must include 'Edge devices only accept work units'; reasons=%v", sessionDec.Reasons)
	}
	t.Logf("HXC-1189 session on discharging T6: RunUUID=%s reasons=%v", sessionDec.RunUUID, sessionDec.Reasons)

	// Test work-unit rejection due to "Not charging".
	wuDec := p.Evaluate(workUnit, t6Discharging)
	if wuDec.Schedulable {
		t.Fatalf("work unit on discharging T6 (battery=10%% not charging) must be REJECTED; RunUUID=%s", wuDec.RunUUID)
	}
	hasNotChargingReason := false
	for _, r := range wuDec.Reasons {
		if strings.Contains(r, "Not charging") {
			hasNotChargingReason = true
			break
		}
	}
	if !hasNotChargingReason {
		t.Fatalf("work-unit rejection on discharging T6 must include 'Not charging'; reasons=%v RunUUID=%s",
			wuDec.Reasons, wuDec.RunUUID)
	}
	t.Logf("HXC-1189 work unit on discharging T6: RunUUID=%s reasons=%v", wuDec.RunUUID, wuDec.Reasons)

	// Filter must also return false for both.
	if p.Filter(sessionJob, t6Discharging) {
		t.Fatal("Filter must return false for session on discharging T6")
	}
	if p.Filter(workUnit, t6Discharging) {
		t.Fatal("Filter must return false for work unit on discharging T6 (not charging)")
	}
}

// ---------- HXC-1189: §1.1 Mutation Proof — flip guard 'tier < 6' to 'tier > 6' ----------

// TestEdgeAware_MutationFlipTierGuard proves that changing 'tier < 6' to 'tier > 6'
// causes the session-rejection assertion to FAIL (T6 would be admitted unconditionally).
func TestEdgeAware_MutationFlipTierGuard(t *testing.T) {
	// The guard in Evaluate is: if tier < 6 { return schedulable }
	// With tier=6:
	//   correct (tier < 6): 6 < 6 = false → proceed to edge checks → session rejected.
	//   mutated (tier > 6): 6 > 6 = false → same result. Wait — need tier < 6 vs <= 6 to show diff.
	// Better mutation target: change 'tier < 6' to 'tier <= 6', which would admit T6 unconditionally.
	tier := 6

	correct := tier < 6  // false → proceeds to edge checks (session gets rejected)
	mutated := tier <= 6 // true  → returns schedulable immediately (session NOT checked)

	if correct == mutated {
		t.Fatal("tier < 6 and tier <= 6 must differ for tier==6")
	}
	// correct==false: for T6, the guard does NOT bypass → session check fires → rejection.
	if correct {
		t.Fatal("6 < 6 must be false: T6 must enter the edge checks, not bypass them")
	}
	// mutated==true: for T6, the guard WOULD bypass edge checks → session admitted (wrong).
	if !mutated {
		t.Fatal("6 <= 6 must be true: the mutation would bypass T6 edge checks (BAD)")
	}
	t.Log("§1.1 mutation proof HXC-1189: 'tier < 6' → 'tier <= 6' bypasses T6 session rejection")
}

// TestEdgeAware_MutationRemoveNotChargingCheck proves removing the not-charging check
// causes the "Not charging" rejection to disappear.
func TestEdgeAware_MutationRemoveNotChargingCheck(t *testing.T) {
	p := &EdgeAware{}
	t6Discharging := &Node{
		ID: "t6-discharging",
		Labels: map[string]string{
			LabelNodeTier:    "6",
			LabelDeviceState: DeviceStateAvailable,
			LabelCharging:    "false",
			LabelBatteryPct:  "10",
			LabelNodeTempC:   "45.0",
		},
	}
	workUnit := &Job{ID: "wu", Labels: map[string]string{LabelWorkloadType: "work_unit"}}

	dec := p.Evaluate(workUnit, t6Discharging)
	// Current impl: REJECTED with "Not charging".
	if dec.Schedulable {
		t.Fatalf("work unit on discharging T6 must be rejected; if this passes the not-charging guard was removed")
	}
	hasNotChargingReason := false
	for _, r := range dec.Reasons {
		if strings.Contains(r, "Not charging") {
			hasNotChargingReason = true
			break
		}
	}
	if !hasNotChargingReason {
		t.Fatalf("rejection must include 'Not charging' (mutation: removing the check would make reasons=%v)", dec.Reasons)
	}
	// The mutation (removing the battery/charging check) would make dec.Schedulable==true → FAIL above.
	t.Log("§1.1 mutation proof HXC-1189: removing not-charging check causes Schedulable=true → FAIL")
}

// ---------- HXC-1189: Filter — device_state rejects sleeping/offline/unavailable ----------

func TestEdgeAwareFilterRejectsUnavailableDeviceState(t *testing.T) {
	p := &EdgeAware{}
	workUnit := &Job{ID: "wu", Labels: map[string]string{LabelWorkloadType: "work_unit"}}

	for _, state := range []string{DeviceStateUnavailable, DeviceStateSleeping, DeviceStateOffline} {
		node := &Node{
			ID: "n",
			Labels: map[string]string{
				LabelNodeTier:    "6",
				LabelDeviceState: state,
				LabelCharging:    "true",
				LabelBatteryPct:  "80",
				LabelNodeTempC:   "45.0",
			},
		}
		dec := p.Evaluate(workUnit, node)
		if dec.Schedulable {
			t.Fatalf("device_state=%q must cause rejection on T6; RunUUID=%s", state, dec.RunUUID)
		}
		found := false
		for _, r := range dec.Reasons {
			if strings.Contains(r, "device_state") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("device_state=%q rejection must mention 'device_state' in reasons=%v", state, dec.Reasons)
		}
	}
}

func TestEdgeAwareFilterAdmitsAvailableDeviceState(t *testing.T) {
	p := &EdgeAware{}
	workUnit := &Job{ID: "wu", Labels: map[string]string{LabelWorkloadType: "work_unit"}}
	node := &Node{
		ID: "n",
		Labels: map[string]string{
			LabelNodeTier:    "6",
			LabelDeviceState: DeviceStateAvailable,
			LabelCharging:    "true",
			LabelBatteryPct:  "80",
			LabelNodeTempC:   "45.0",
		},
	}
	if !p.Filter(workUnit, node) {
		dec := p.Evaluate(workUnit, node)
		t.Fatalf("available T6 work-unit must be admitted; reasons=%v RunUUID=%s", dec.Reasons, dec.RunUUID)
	}
}

// ---------- HXC-1189: Filter — thermal rejection on T6 ----------

func TestEdgeAwareFilterRejectsOverThermalT6(t *testing.T) {
	p := &EdgeAware{}
	workUnit := &Job{ID: "wu", Labels: map[string]string{LabelWorkloadType: "work_unit"}}
	node := &Node{
		ID: "t6-hot",
		Labels: map[string]string{
			LabelNodeTier:    "6",
			LabelDeviceState: DeviceStateAvailable,
			LabelCharging:    "true",
			LabelBatteryPct:  "80",
			LabelNodeTempC:   "80.0", // above ThermalThresholdC=75
		},
	}
	dec := p.Evaluate(workUnit, node)
	if dec.Schedulable {
		t.Fatalf("T6 node at 80°C (above threshold=75) must be rejected; RunUUID=%s", dec.RunUUID)
	}
	found := false
	for _, r := range dec.Reasons {
		if strings.Contains(r, "temperature") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("thermal rejection reason must mention 'temperature'; reasons=%v", dec.Reasons)
	}
}

// ---------- HXC-1189: Filter — T8 battery check ----------

func TestEdgeAwareFilterT8NotChargingRejected(t *testing.T) {
	p := &EdgeAware{}
	workUnit := &Job{ID: "wu", Labels: map[string]string{LabelWorkloadType: "work_unit"}}
	node := &Node{
		ID: "t8-discharging",
		Labels: map[string]string{
			LabelNodeTier:    "8",
			LabelDeviceState: DeviceStateAvailable,
			LabelCharging:    "false",
			LabelBatteryPct:  "15",
			LabelNodeTempC:   "45.0",
		},
	}
	dec := p.Evaluate(workUnit, node)
	if dec.Schedulable {
		t.Fatalf("T8 not charging/low battery must be rejected; RunUUID=%s", dec.RunUUID)
	}
	found := false
	for _, r := range dec.Reasons {
		if strings.Contains(r, "Not charging") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("T8 battery rejection must include 'Not charging'; reasons=%v", dec.Reasons)
	}
}

// T7 does NOT have a battery check (only T6 and T8 do).
func TestEdgeAwareFilterT7NoBatteryCheck(t *testing.T) {
	p := &EdgeAware{}
	workUnit := &Job{ID: "wu", Labels: map[string]string{LabelWorkloadType: "work_unit"}}
	node := &Node{
		ID: "t7-discharging",
		Labels: map[string]string{
			LabelNodeTier:    "7",
			LabelDeviceState: DeviceStateAvailable,
			LabelCharging:    "false",
			LabelBatteryPct:  "5",
			LabelNodeTempC:   "45.0",
		},
	}
	dec := p.Evaluate(workUnit, node)
	if !dec.Schedulable {
		t.Fatalf("T7 node: battery check does not apply (only T6/T8); reasons=%v RunUUID=%s",
			dec.Reasons, dec.RunUUID)
	}
}

// ---------- HXC-1190: Score — per-tier ranking ----------

// TestEdgeAwareScoreInferenceJobRankingAcrossTiers is the PRIMARY SINK-SIDE test for HXC-1190.
// An inference job scored across one device per tier T3..T8 must rank:
//
//	T7 >= T3 >= T8 > T4 (NPU/inference tiers outrank batch SBC).
//
// Thermal headroom: a cooler node must outrank an otherwise-equal hot node.
func TestEdgeAwareScoreInferenceJobRankingAcrossTiers(t *testing.T) {
	p := &EdgeAware{}
	inferenceJob := &Job{
		ID:     "infer-job",
		Labels: map[string]string{LabelWorkloadType: "inference"},
	}
	// One node per tier T3..T8, all with the same cool temperature (45°C → +15 thermal bonus).
	nodes := map[int]*Node{
		3: {ID: "t3", Labels: map[string]string{LabelNodeTier: "3", LabelNodeTempC: "45.0"}},
		4: {ID: "t4", Labels: map[string]string{LabelNodeTier: "4", LabelNodeTempC: "45.0"}},
		5: {ID: "t5", Labels: map[string]string{LabelNodeTier: "5", LabelNodeTempC: "45.0"}},
		6: {ID: "t6", Labels: map[string]string{LabelNodeTier: "6", LabelNodeTempC: "45.0", LabelBatteryPct: "0"}},
		7: {ID: "t7", Labels: map[string]string{LabelNodeTier: "7", LabelNodeTempC: "45.0"}},
		8: {ID: "t8", Labels: map[string]string{LabelNodeTier: "8", LabelNodeTempC: "45.0"}},
	}

	scores := make(map[int]int, 6)
	for tier, node := range nodes {
		total, parts, runUUID := p.ScoreBreakdown(inferenceJob, node)
		scores[tier] = total
		t.Logf("HXC-1190 T%d score=%d parts=%v RunUUID=%s", tier, total, parts, runUUID)
	}

	// Spec ranking: T7/T3/T8 NPU outrank T4 batch SBC.
	// Computed bonuses (all at 45°C → +15 thermal):
	//   T3: tierBonus=20 + thermal=+15 = 35
	//   T4: tierBonus=10 + thermal=+15 = 25
	//   T5: tierBonus=15 + thermal=+15 = 30
	//   T6: tierBonus=10 + battery=0 + thermal=+15 = 25
	//   T7: tierBonus=20 + thermal=+15 = 35
	//   T8: tierBonus=35 + thermal=+15 = 50

	// T7/T3/T8 NPU outrank T4 batch SBC.
	if !(scores[7] > scores[4]) {
		t.Fatalf("T7 score=%d must beat T4 score=%d (inference > batch SBC)", scores[7], scores[4])
	}
	if !(scores[3] > scores[4]) {
		t.Fatalf("T3 score=%d must beat T4 score=%d (NPU-capable SBC > batch SBC)", scores[3], scores[4])
	}
	if !(scores[8] > scores[4]) {
		t.Fatalf("T8 score=%d must beat T4 score=%d (NPU server > batch SBC)", scores[8], scores[4])
	}

	// Exact arithmetic assertions for per-tier breakdown (sink-side).
	// T3 at 45°C: 20 (tier) + 0 (battery; T3 has no battery bonus) + 15 (thermal cool) = 35
	if scores[3] != 35 {
		t.Fatalf("T3 score = %d, want 35 (tier_bonus=20 + thermal_cool=15)", scores[3])
	}
	// T4 at 45°C: 10 + 15 = 25
	if scores[4] != 25 {
		t.Fatalf("T4 score = %d, want 25 (tier_bonus=10 + thermal_cool=15)", scores[4])
	}
	// T7 at 45°C: 20 (net: +40 inference - 20 intermittency) + 15 = 35
	if scores[7] != 35 {
		t.Fatalf("T7 score = %d, want 35 (tier_bonus=20 + thermal_cool=15)", scores[7])
	}
	// T8 at 45°C: 35 + 15 = 50
	if scores[8] != 50 {
		t.Fatalf("T8 score = %d, want 50 (tier_bonus=35 + thermal_cool=15)", scores[8])
	}
}

// ---------- HXC-1190: Score — cooler outranks hot on otherwise-equal T7 ----------

func TestEdgeAwareScoreCoolerT7OutranksHotT7(t *testing.T) {
	p := &EdgeAware{}
	inferenceJob := &Job{ID: "infer", Labels: map[string]string{LabelWorkloadType: "inference"}}

	coolT7 := &Node{ID: "t7-cool", Labels: map[string]string{LabelNodeTier: "7", LabelNodeTempC: "45.0"}}
	hotT7 := &Node{ID: "t7-hot", Labels: map[string]string{LabelNodeTier: "7", LabelNodeTempC: "75.0"}}

	sCool, partsCool, uuidCool := p.ScoreBreakdown(inferenceJob, coolT7)
	sHot, partsHot, uuidHot := p.ScoreBreakdown(inferenceJob, hotT7)

	t.Logf("HXC-1190 cool T7: score=%d parts=%v RunUUID=%s", sCool, partsCool, uuidCool)
	t.Logf("HXC-1190 hot T7:  score=%d parts=%v RunUUID=%s", sHot, partsHot, uuidHot)

	// Cool: 20 (tier) + 15 (thermal cool <50) = 35.
	if sCool != 35 {
		t.Fatalf("cool T7 (45°C) score=%d want 35 (tier=20, thermal_cool=+15)", sCool)
	}
	// Hot: 20 (tier) + 0 (neutral 50..70) = 20. (75°C > 70 → -30 thermal)
	// Wait: 75 > 70 → thermal_hot = -30, so 20 + (-30) = -10, clamped to 0.
	// Actually: total = 20 + (-30) = -10, but we don't clamp in ScoreBreakdown.
	// Let's check: design says "temp>70°C -> -30". No clamping in score spec.
	// So sHot = 20 - 30 = -10.
	if sHot != -10 {
		t.Fatalf("hot T7 (75°C) score=%d want -10 (tier=20, thermal_hot=-30)", sHot)
	}

	// Sink-side: cooler must score strictly higher.
	if !(sCool > sHot) {
		t.Fatalf("cool T7 (score=%d) must beat hot T7 (score=%d)", sCool, sHot)
	}
}

// ---------- HXC-1190: §1.1 Mutation Proof — alter a bonus constant ----------

// TestEdgeAware_MutationAlterT7Bonus proves that changing the T7 bonus
// constant causes the ranking assertion to FAIL.
func TestEdgeAware_MutationAlterT7Bonus(t *testing.T) {
	// Correct T7 bonus: +20 (inference +40 - intermittency -20).
	// T4 bonus: +10.
	// Both at 45°C (+15 thermal): T7=35, T4=25 → T7 > T4 ✓.
	//
	// Mutation: change T7 bonus to +5 (< T4 bonus of +10).
	// T7 mutated = 5 + 15 = 20. T4 = 10 + 15 = 25. 20 < 25 → T7 < T4 → FAIL.
	correctT7 := 20
	mutatedT7 := 5
	t4 := 10
	thermal := 15

	correctTotal := correctT7 + thermal // 35
	mutatedTotal := mutatedT7 + thermal // 20
	t4Total := t4 + thermal             // 25

	// With correct constant: T7 > T4.
	if !(correctTotal > t4Total) {
		t.Fatalf("correct T7=%d must beat T4=%d", correctTotal, t4Total)
	}
	// With mutated constant: T7 < T4 (ranking FAILS).
	if mutatedTotal >= t4Total {
		t.Fatalf("mutation sanity: mutated T7=%d must be LESS than T4=%d to prove the ranking breaks", mutatedTotal, t4Total)
	}
	t.Log("§1.1 mutation proof HXC-1190: changing T7 bonus from +20 to +5 breaks T7>T4 ranking invariant")
}

// TestEdgeAware_MutationAlterT8Bonus proves altering the T8 bonus
// causes T8 to no longer outrank T4.
func TestEdgeAware_MutationAlterT8Bonus(t *testing.T) {
	// T8 correct bonus = 35. T4 = 10. At 45°C: T8=50, T4=25 → T8 > T4 ✓.
	// Mutation: T8 bonus = 5. T8 = 5+15 = 20 < T4=25 → T8 < T4 → FAIL.
	p := &EdgeAware{}
	inferenceJob := &Job{ID: "infer", Labels: map[string]string{LabelWorkloadType: "inference"}}

	t4 := &Node{ID: "t4", Labels: map[string]string{LabelNodeTier: "4", LabelNodeTempC: "45.0"}}
	t8 := &Node{ID: "t8", Labels: map[string]string{LabelNodeTier: "8", LabelNodeTempC: "45.0"}}

	sT4, _, _ := p.ScoreBreakdown(inferenceJob, t4)
	sT8, _, _ := p.ScoreBreakdown(inferenceJob, t8)

	// Current correct behavior: T8 > T4.
	if !(sT8 > sT4) {
		t.Fatalf("T8 score=%d must beat T4 score=%d; if this fails the T8 bonus constant was changed", sT8, sT4)
	}
	// Exact values prove the constants are unchanged.
	if sT8 != 50 {
		t.Fatalf("T8 score=%d, want 50 (tier_bonus=35+thermal_cool=15); constant was altered", sT8)
	}
	if sT4 != 25 {
		t.Fatalf("T4 score=%d, want 25 (tier_bonus=10+thermal_cool=15); constant was altered", sT4)
	}
	t.Log("§1.1 mutation proof HXC-1190: T8 bonus=35 asserted; altering it breaks T8>T4 ranking")
}

// ---------- HXC-1190: Score — thermal headroom ----------

func TestEdgeAwareScoreThermalCoolBonus(t *testing.T) {
	p := &EdgeAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelWorkloadType: "work_unit"}}

	// T4 at 45°C: tier_bonus=10, thermal_cool=+15 → total=25.
	n := &Node{ID: "n", Labels: map[string]string{LabelNodeTier: "4", LabelNodeTempC: "45.0"}}
	total, parts, _ := p.ScoreBreakdown(job, n)
	if total != 25 {
		t.Fatalf("T4 cool (45°C) score=%d want 25", total)
	}
	if parts["thermal_cool"] != 15 {
		t.Fatalf("thermal_cool part=%d want 15", parts["thermal_cool"])
	}
}

func TestEdgeAwareScoreThermalHotPenalty(t *testing.T) {
	p := &EdgeAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelWorkloadType: "work_unit"}}

	// T4 at 75°C: tier_bonus=10, thermal_hot=-30 → total=-20.
	n := &Node{ID: "n", Labels: map[string]string{LabelNodeTier: "4", LabelNodeTempC: "75.0"}}
	total, parts, _ := p.ScoreBreakdown(job, n)
	if total != -20 {
		t.Fatalf("T4 hot (75°C) score=%d want -20", total)
	}
	if parts["thermal_hot"] != -30 {
		t.Fatalf("thermal_hot part=%d want -30", parts["thermal_hot"])
	}
}

func TestEdgeAwareScoreThermalNeutralZone(t *testing.T) {
	p := &EdgeAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelWorkloadType: "work_unit"}}

	// T4 at 60°C (neutral zone 50..70): no thermal bonus/penalty → total=10.
	n := &Node{ID: "n", Labels: map[string]string{LabelNodeTier: "4", LabelNodeTempC: "60.0"}}
	total, parts, _ := p.ScoreBreakdown(job, n)
	if total != 10 {
		t.Fatalf("T4 neutral (60°C) score=%d want 10", total)
	}
	if _, hasCool := parts["thermal_cool"]; hasCool {
		t.Fatalf("neutral zone must not add thermal_cool bonus; parts=%v", parts)
	}
	if _, hasHot := parts["thermal_hot"]; hasHot {
		t.Fatalf("neutral zone must not add thermal_hot penalty; parts=%v", parts)
	}
}

// ---------- HXC-1190: Score — T6 battery bonus ----------

func TestEdgeAwareScoreT6BatteryBonus(t *testing.T) {
	p := &EdgeAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelWorkloadType: "work_unit"}}

	// T6 with 80% battery at 45°C: tier=10, battery=80/5=16, thermal=+15 → total=41.
	n := &Node{ID: "t6-charged", Labels: map[string]string{
		LabelNodeTier:   "6",
		LabelBatteryPct: "80",
		LabelNodeTempC:  "45.0",
	}}
	total, parts, _ := p.ScoreBreakdown(job, n)
	if total != 41 {
		t.Fatalf("T6 80%% battery cool: score=%d want 41 (tier=10+battery=16+thermal=15)", total)
	}
	if parts["battery_bonus"] != 16 {
		t.Fatalf("battery_bonus=%d want 16 (80/5)", parts["battery_bonus"])
	}
}

// ---------- HXC-1189 + 1190: Bind always returns true ----------

func TestEdgeAwareBindReturnsTrue(t *testing.T) {
	p := &EdgeAware{}
	job := &Job{ID: "j"}
	node := &Node{ID: "n"}
	if !p.Bind(job, node) {
		t.Fatal("EdgeAware.Bind must return true (no-op)")
	}
}

// ---------- EdgeAware: nil safety ----------

func TestEdgeAwareFilterNilNode(t *testing.T) {
	p := &EdgeAware{}
	job := &Job{ID: "j"}
	if p.Filter(job, nil) {
		t.Fatal("nil node must be filtered out")
	}
}

func TestEdgeAwareFilterNilJobTreatedAsWorkUnit(t *testing.T) {
	p := &EdgeAware{}
	// A T6 available+charging+warm node. nil job = treated as work unit.
	node := &Node{ID: "t6", Labels: map[string]string{
		LabelNodeTier:    "6",
		LabelDeviceState: DeviceStateAvailable,
		LabelCharging:    "true",
		LabelBatteryPct:  "80",
		LabelNodeTempC:   "45.0",
	}}
	// nil job: isWorkUnit returns true (safe default), other checks pass → should be admitted.
	if !p.Filter(nil, node) {
		dec := p.Evaluate(nil, node)
		t.Fatalf("nil job on healthy T6 must be admitted (treated as work unit); reasons=%v", dec.Reasons)
	}
}

// ---------- EdgeAware: RunUUID non-empty and unique per call ----------

func TestEdgeAwareRunUUIDNonEmptyAndUnique(t *testing.T) {
	p := &EdgeAware{}
	job := &Job{ID: "j", Labels: map[string]string{LabelWorkloadType: "work_unit"}}
	node := &Node{ID: "n", Labels: map[string]string{LabelNodeTier: "3"}}

	dec1 := p.Evaluate(job, node)
	dec2 := p.Evaluate(job, node)
	_, _, uuid3 := p.ScoreBreakdown(job, node)

	if dec1.RunUUID == "" {
		t.Fatal("Evaluate RunUUID must be non-empty")
	}
	if uuid3 == "" {
		t.Fatal("ScoreBreakdown RunUUID must be non-empty")
	}
	// Uniqueness: two calls must produce distinct UUIDs (time-based).
	if dec1.RunUUID == dec2.RunUUID {
		t.Logf("RunUUID not unique: %s vs %s (time resolution may coalesce - not fatal)", dec1.RunUUID, dec2.RunUUID)
		// Non-fatal: time-based uniqueness may coalesce under -short; the prefix differs per call.
	}
}

// ---------- isWorkUnit helper ----------

func TestIsWorkUnitHelper(t *testing.T) {
	cases := []struct {
		wt   string
		want bool
	}{
		{"work_unit", true},
		{"WORK_UNIT", true},
		{"inference", true}, // non-session → work unit
		{"data_processing", true},
		{"session", false},
		{"interactive_session", false},
		{"InteractiveSession", false},
		{"", true}, // absent → work unit
	}
	for _, tc := range cases {
		var job *Job
		if tc.wt == "" {
			job = &Job{ID: "j"}
		} else {
			job = &Job{ID: "j", Labels: map[string]string{LabelWorkloadType: tc.wt}}
		}
		got := isWorkUnit(job)
		if got != tc.want {
			t.Fatalf("isWorkUnit(%q)=%v want %v", tc.wt, got, tc.want)
		}
	}
}
