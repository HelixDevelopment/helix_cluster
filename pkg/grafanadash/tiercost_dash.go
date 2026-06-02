// Package grafanadash — this file implements HXC-1551: Phase-8B
// tier-utilization + cost dashboard generator.
//
// It adds BuildTierCostDashboard(), which composes a Grafana dashboard JSON
// model covering the metric series exposed by pkg/metrics:
//
//   - gpu_tier_utilization  (TierMetrics — GPU utilization per hardware tier)
//   - gpu_cost_per_hour     (TierMetrics — USD/hr cost per provider)
//   - provider_health       (TierMetrics — provider health score)
//   - tao_earnings_total    (EarningsMetrics — cumulative TAO per hotkey)
//   - token_throughput      (EarningsMetrics — scalar tokens/sec gauge)
//   - gpu_utilization       (EarningsMetrics — per-device GPU utilization)
//
// The function reuses the package-local Dashboard / Panel / Target / GridPos
// types and the Validate() method; it does not redefine any existing types.
//
// Out-of-host note (CLAUDE-1 / §11.4.3 honesty): a live Grafana instance is
// required to import the JSON.  This file generates and validates the JSON
// model in-process only; the unit tests prove correctness without network
// access.
package grafanadash

// --------------------------------------------------------------------------
// Layout constants
//
// The canvas is 24 grid units wide.  Panels are laid out in three rows:
//
//   Row 0 (Y=0,  H=8): GPU-tier utilization timeseries — full width (W=24).
//   Row 1 (Y=8,  H=8): two half-width panels side by side (W=12 each):
//                       left  = gpu_cost_per_hour timeseries
//                       right = provider_health stat
//   Row 2 (Y=16, H=8): three 8-unit panels:
//                       left   = tao_earnings_total timeseries
//                       centre = token_throughput stat
//                       right  = gpu_utilization timeseries
//
// Panels within each row are placed adjacently (no gap, no overlap), so
// Validate()'s overlap check passes and the layout is deterministic.
// --------------------------------------------------------------------------

const (
	tierDashSchemaVersion = 36

	// Row Y offsets
	tierDashRow0Y = 0
	tierDashRow1Y = 8
	tierDashRow2Y = 16

	// Row heights
	tierDashRowH = 8
)

// buildGPUTierUtilPanel returns a full-width timeseries panel showing GPU
// utilization aggregated over all hardware tiers.
//
// PromQL: gpu_tier_utilization (one series per tier label — Grafana fans out
// per label value automatically when no aggregation is applied).
func buildGPUTierUtilPanel() Panel {
	return Panel{
		Title: "GPU Tier Utilization",
		Type:  "timeseries",
		Targets: []Target{
			{
				// Rate of change is irrelevant for a gauge; the raw gauge value is
				// what operators care about (fraction busy per tier, 0–1).
				Expr:         `gpu_tier_utilization`,
				LegendFormat: "{{tier}}",
			},
		},
		// Full canvas width, 8 units tall; anchored at the top-left.
		GridPos: GridPos{X: 0, Y: tierDashRow0Y, W: 24, H: tierDashRowH},
	}
}

// buildCostPerHourPanel returns a timeseries panel for USD-per-hour cost per
// provider (left half of row 1).
//
// PromQL: gpu_cost_per_hour{provider="..."} — one series per provider.
func buildCostPerHourPanel() Panel {
	return Panel{
		Title: "GPU Cost Per Hour (USD)",
		Type:  "timeseries",
		Targets: []Target{
			{
				Expr:         `gpu_cost_per_hour`,
				LegendFormat: "{{provider}}",
			},
		},
		// Left 12 columns of row 1.
		GridPos: GridPos{X: 0, Y: tierDashRow1Y, W: 12, H: tierDashRowH},
	}
}

// buildProviderHealthPanel returns a stat panel for provider health scores
// (right half of row 1).
//
// PromQL: provider_health — one series per provider.
func buildProviderHealthPanel() Panel {
	return Panel{
		Title: "Provider Health",
		Type:  "stat",
		Targets: []Target{
			{
				Expr:         `provider_health`,
				LegendFormat: "{{provider}}",
			},
		},
		// Right 12 columns of row 1.
		GridPos: GridPos{X: 12, Y: tierDashRow1Y, W: 12, H: tierDashRowH},
	}
}

// buildTaoEarningsPanel returns a timeseries panel for cumulative TAO earned
// (left third of row 2).
//
// PromQL: tao_earnings_total — one series per hotkey label.
func buildTaoEarningsPanel() Panel {
	return Panel{
		Title: "TAO Earnings Total",
		Type:  "timeseries",
		Targets: []Target{
			{
				Expr:         `tao_earnings_total`,
				LegendFormat: "{{hotkey}}",
			},
		},
		// Left 8 columns of row 2.
		GridPos: GridPos{X: 0, Y: tierDashRow2Y, W: 8, H: tierDashRowH},
	}
}

// buildTokenThroughputPanel returns a scalar stat panel for current
// tokens-per-second (centre third of row 2).
//
// PromQL: token_throughput — scalar gauge with no labels.
func buildTokenThroughputPanel() Panel {
	return Panel{
		Title: "Token Throughput (tokens/sec)",
		Type:  "stat",
		Targets: []Target{
			{
				Expr:         `token_throughput`,
				LegendFormat: "tokens/sec",
			},
		},
		// Centre 8 columns of row 2.
		GridPos: GridPos{X: 8, Y: tierDashRow2Y, W: 8, H: tierDashRowH},
	}
}

// buildGPUUtilizationPanel returns a timeseries panel for per-device GPU
// utilization from the earnings metrics pipeline (right third of row 2).
//
// PromQL: gpu_utilization — one series per gpu label.
func buildGPUUtilizationPanel() Panel {
	return Panel{
		Title: "GPU Utilization (per device)",
		Type:  "timeseries",
		Targets: []Target{
			{
				Expr:         `gpu_utilization`,
				LegendFormat: "{{gpu}}",
			},
		},
		// Right 8 columns of row 2.
		GridPos: GridPos{X: 16, Y: tierDashRow2Y, W: 8, H: tierDashRowH},
	}
}

// BuildTierCostDashboard constructs the Helix Cluster OS Phase-8B tier
// utilization + cost dashboard.  It contains:
//
//   - "GPU Tier Utilization"          — timeseries, full width, row 0
//   - "GPU Cost Per Hour (USD)"       — timeseries, left half, row 1
//   - "Provider Health"               — stat,       right half, row 1
//   - "TAO Earnings Total"            — timeseries, left third, row 2
//   - "Token Throughput (tokens/sec)" — stat,       centre,     row 2
//   - "GPU Utilization (per device)"  — timeseries, right third, row 2
//
// The returned Dashboard passes Validate() without error.  Panel order and
// layout are fixed (deterministic): calling this function twice produces
// identical JSON when rendered via Render().
func BuildTierCostDashboard() Dashboard {
	// Panels are appended in a fixed, explicit order — not from a map — so the
	// slice and thus the rendered JSON are byte-stable across calls (satisfying
	// the determinism requirement from CLAUDE-1).
	panels := []Panel{
		buildGPUTierUtilPanel(),
		buildCostPerHourPanel(),
		buildProviderHealthPanel(),
		buildTaoEarningsPanel(),
		buildTokenThroughputPanel(),
		buildGPUUtilizationPanel(),
	}

	return Dashboard{
		Title:         "Helix Cluster — Phase-8B Tier Utilization + Cost",
		UID:           "helix-tier-cost-v1",
		Panels:        panels,
		SchemaVersion: tierDashSchemaVersion,
		Tags:          []string{"helix", "gpu", "tier", "cost", "earnings"},
	}
}
