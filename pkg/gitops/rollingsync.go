package gitops

import (
	"math"
	"sort"
)

// RollingSyncWave is one ordered step of a progressive rollout. Apps holds the
// Application names synced in this wave; Tier records which ring the wave
// belongs to; Step is the 0-based wave index.
type RollingSyncWave struct {
	Step int
	Tier Tier
	Apps []string
}

// RollingSyncPlan is the full ordered rollout: a sequence of waves executed in
// order (canary first, then tier-2 in 50% chunks, then tier-1 in 25% chunks).
type RollingSyncPlan struct {
	Waves []RollingSyncWave
}

// tierStepFraction is the per-wave fraction for a given tier: tier-2 advances in
// 50% chunks, tier-1 in 25% chunks, canary all-at-once (it is a single small
// ring). Returns 1.0 (one wave) for any unrecognized tier so an unknown tier is
// never silently skipped.
func tierStepFraction(t Tier) float64 {
	switch t {
	case TierTwo:
		return 0.50
	case TierOne:
		return 0.25
	default:
		return 1.0
	}
}

// chunkSize converts a per-wave fraction into an integer chunk over n apps,
// always at least 1 so a non-empty group makes progress (avoids a zero-size
// wave that would stall the rollout forever).
func chunkSize(n int, fraction float64) int {
	if n <= 0 {
		return 0
	}
	c := int(math.Ceil(float64(n) * fraction))
	if c < 1 {
		c = 1
	}
	return c
}

// PlanRollingSync decomposes the generated Applications into an ordered
// RollingSyncPlan implementing the federation rollout policy:
//
//	canary  -> one wave (all canary cells, the smallest ring)
//	tier-2  -> waves of 50% each
//	tier-1  -> waves of 25% each
//
// Tiers always execute canary -> tier-2 -> tier-1 regardless of input order, so
// the production ring (tier-1) is never touched before the canary has gone out.
// Within a tier, apps are sorted by name for deterministic, reproducible waves.
//
// This is the gradeable core of the closure criterion's "rolling out canary-first
// then tiered": the returned plan's wave order IS the rollout order ArgoCD's
// RollingSync strategy would execute.
func PlanRollingSync(apps []Application) RollingSyncPlan {
	// Group app names by tier.
	byTier := map[Tier][]string{}
	for _, a := range apps {
		t := Tier(a.Metadata.Labels[LabelTier])
		byTier[t] = append(byTier[t], a.Metadata.Name)
	}
	for t := range byTier {
		sort.Strings(byTier[t])
	}

	plan := RollingSyncPlan{}
	step := 0
	// Tier execution order: canary, tier-2, tier-1, then any UNKNOWN tiers
	// (sorted, emitted last). Iterating only the three known tiers would silently
	// DROP apps whose tier label is a typo, a future ring, or empty — they would
	// vanish from the rollout entirely. Appending the remaining tiers honors
	// tierStepFraction's documented "unknown tier is never silently skipped"
	// contract; unknown tiers roll out all-at-once (fraction 1.0) after the
	// production ring, never before the canary.
	order := []Tier{TierCanary, TierTwo, TierOne}
	known := map[Tier]bool{TierCanary: true, TierTwo: true, TierOne: true}
	var unknown []Tier
	for t := range byTier {
		if !known[t] {
			unknown = append(unknown, t)
		}
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	order = append(order, unknown...)
	for _, t := range order {
		names := byTier[t]
		if len(names) == 0 {
			continue
		}
		size := chunkSize(len(names), tierStepFraction(t))
		for i := 0; i < len(names); i += size {
			end := i + size
			if end > len(names) {
				end = len(names)
			}
			chunk := make([]string, end-i)
			copy(chunk, names[i:end])
			plan.Waves = append(plan.Waves, RollingSyncWave{
				Step: step,
				Tier: t,
				Apps: chunk,
			})
			step++
		}
	}
	return plan
}

// TierOrder returns the tier of each wave in execution order. It is a convenience
// for asserting/observing the canary->tier-2->tier-1 sequencing.
func (p RollingSyncPlan) TierOrder() []Tier {
	out := make([]Tier, len(p.Waves))
	for i, w := range p.Waves {
		out[i] = w.Tier
	}
	return out
}
