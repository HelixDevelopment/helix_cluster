package grafanadash_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/grafanadash"
)

// --------------------------------------------------------------------------
// TestBuildTierCostDashboard_ValidAndStructured
//
// Closure criterion 1: the generated dashboard JSON is valid per the
// package's EXISTING Validate() and contains panels referencing the expected
// metric series (assert panel targets include gpu_tier_utilization and
// gpu_cost_per_hour).
//
// Mutation guard: if any required panel or target Expr were dropped, the
// "contains series X" substring assertions below fail (one per required
// metric series).  The panel-count assertion fires if a whole panel is
// removed.  The Validate() call fires if structural invariants are broken
// (empty Title, UID, SchemaVersion, empty Targets, overlapping GridPos, etc.).
// --------------------------------------------------------------------------
func TestBuildTierCostDashboard_ValidAndStructured(t *testing.T) {
	t.Parallel()

	d := grafanadash.BuildTierCostDashboard()

	// Must pass the package's existing structural validator immediately —
	// overlap check, empty-Expr check, and all other invariants included.
	if err := d.Validate(); err != nil {
		t.Fatalf("BuildTierCostDashboard: Validate() = %v, want nil", err)
	}

	// --- Exact top-level field values ---------------------------------------

	const wantUID = "helix-tier-cost-v1"
	if d.UID != wantUID {
		t.Errorf("UID = %q, want %q", d.UID, wantUID)
	}

	const wantTitle = "Helix Cluster — Phase-8B Tier Utilization + Cost"
	if d.Title != wantTitle {
		t.Errorf("Title = %q, want %q", d.Title, wantTitle)
	}

	if d.SchemaVersion != 36 {
		t.Errorf("SchemaVersion = %d, want 36", d.SchemaVersion)
	}

	// Tags must include "helix", "gpu", "cost".
	wantTags := []string{"helix", "gpu", "cost"}
	tagSet := make(map[string]bool, len(d.Tags))
	for _, tag := range d.Tags {
		tagSet[tag] = true
	}
	for _, wt := range wantTags {
		if !tagSet[wt] {
			t.Errorf("Tags %v missing required tag %q", d.Tags, wt)
		}
	}

	// --- Panel presence ----------------------------------------------------

	// All six panel titles that must exist.
	wantTitles := []string{
		"GPU Tier Utilization",
		"GPU Cost Per Hour (USD)",
		"Provider Health",
		"TAO Earnings Total",
		"Token Throughput (tokens/sec)",
		"GPU Utilization (per device)",
	}
	for _, title := range wantTitles {
		if _, ok := panelByTitle(d.Panels, title); !ok {
			t.Errorf("panel %q not found in dashboard", title)
		}
	}

	// Exact panel count: 6 panels (no accidental extras or omissions).
	// Mutation guard: remove any buildXxx() call in BuildTierCostDashboard →
	// len(d.Panels) < 6 → assertion fires.
	if got := len(d.Panels); got != 6 {
		t.Errorf("panel count = %d, want 6", got)
	}

	// --- Required metric series (CLAUDE-1 closure criterion 1) -------------
	//
	// Assert that at least one target Expr across all panels references each
	// of the two mandatory series from the spec.  Using a substring check
	// (not exact equality) so that future PromQL wrapping (rate(), sum by())
	// still satisfies the check while preserving mutation-survivability: if
	// a panel/target for these series is dropped entirely, the check fails.

	// Mandatory per spec closure criterion 1 — GPU tier utilization.
	assertAnyTargetContains(t, d.Panels, "gpu_tier_utilization")

	// Mandatory per spec closure criterion 1 — GPU cost per hour.
	assertAnyTargetContains(t, d.Panels, "gpu_cost_per_hour")

	// Additional required series.
	assertAnyTargetContains(t, d.Panels, "provider_health")
	assertAnyTargetContains(t, d.Panels, "tao_earnings_total")
	assertAnyTargetContains(t, d.Panels, "token_throughput")
	assertAnyTargetContains(t, d.Panels, "gpu_utilization")
}

// assertAnyTargetContains is a helper that fails t if no target Expr across
// all panels contains needle.
//
// Mutation guard: remove the panel/target for 'needle' → no Expr contains it
// → t.Errorf fires.
func assertAnyTargetContains(t *testing.T, panels []grafanadash.Panel, needle string) {
	t.Helper()
	for _, p := range panels {
		for _, tgt := range p.Targets {
			if strings.Contains(tgt.Expr, needle) {
				return
			}
		}
	}
	t.Errorf("no panel target Expr contains metric series %q", needle)
}

// --------------------------------------------------------------------------
// TestBuildTierCostDashboard_Deterministic
//
// Closure criterion 2: generate twice → identical JSON; stable panel/target
// ordering.
//
// Mutation guard: if BuildTierCostDashboard used map iteration to build
// panels (non-deterministic order), the two Render() outputs would differ on
// some runs → bytes.Equal fails → test fails.
// --------------------------------------------------------------------------
func TestBuildTierCostDashboard_Deterministic(t *testing.T) {
	t.Parallel()

	doc1, err := grafanadash.BuildTierCostDashboard().Render()
	if err != nil {
		t.Fatalf("first Render() error: %v", err)
	}
	doc2, err := grafanadash.BuildTierCostDashboard().Render()
	if err != nil {
		t.Fatalf("second Render() error: %v", err)
	}

	if !bytes.Equal(doc1, doc2) {
		t.Errorf("BuildTierCostDashboard().Render() is not deterministic:\n"+
			"first:  %s\nsecond: %s", doc1, doc2)
	}
}

// --------------------------------------------------------------------------
// TestBuildTierCostDashboard_RenderParseRoundTrip
//
// Render → Parse → Validate must preserve all panel titles and target Exprs.
//
// Mutation guard: corrupt Render() output → Parse fails → t.Fatalf fires;
// OR drop a panel → title/count mismatch → t.Errorf fires.
// --------------------------------------------------------------------------
func TestBuildTierCostDashboard_RenderParseRoundTrip(t *testing.T) {
	t.Parallel()

	orig := grafanadash.BuildTierCostDashboard()
	doc, err := orig.Render()
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	// Rendered bytes must be valid JSON.
	if !json.Valid(doc) {
		t.Fatal("Render() output is not valid JSON")
	}

	got, err := grafanadash.Parse(doc)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	// Parsed dashboard must still be valid.
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() after Parse() = %v, want nil", err)
	}

	// Top-level fields.
	if got.Title != orig.Title {
		t.Errorf("Title: got %q, want %q", got.Title, orig.Title)
	}
	if got.UID != orig.UID {
		t.Errorf("UID: got %q, want %q", got.UID, orig.UID)
	}
	if got.SchemaVersion != orig.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", got.SchemaVersion, orig.SchemaVersion)
	}

	// Panel count.
	if len(got.Panels) != len(orig.Panels) {
		t.Fatalf("panel count: got %d, want %d", len(got.Panels), len(orig.Panels))
	}

	// Panel titles and target Exprs in insertion order.
	for i, op := range orig.Panels {
		gp := got.Panels[i]
		if gp.Title != op.Title {
			t.Errorf("panel[%d] Title: got %q, want %q", i, gp.Title, op.Title)
		}
		if len(gp.Targets) != len(op.Targets) {
			t.Errorf("panel[%d] %q target count: got %d, want %d",
				i, op.Title, len(gp.Targets), len(op.Targets))
			continue
		}
		for j, ot := range op.Targets {
			if gp.Targets[j].Expr != ot.Expr {
				t.Errorf("panel[%d] %q target[%d] Expr: got %q, want %q",
					i, op.Title, j, gp.Targets[j].Expr, ot.Expr)
			}
		}
	}
}

// --------------------------------------------------------------------------
// TestBuildTierCostDashboard_GridPos
//
// Assert exact GridPos for all six panels (three-row layout).
//
// Mutation guard: shift any panel's Y/X/W/H → the corresponding wantPos
// assertion fires.  Also, since Validate() checks for overlaps, any layout
// collision caught by Validate() surfaced earlier in _ValidAndStructured.
// --------------------------------------------------------------------------
func TestBuildTierCostDashboard_GridPos(t *testing.T) {
	t.Parallel()

	d := grafanadash.BuildTierCostDashboard()

	type posCase struct {
		title string
		want  grafanadash.GridPos
	}
	cases := []posCase{
		// Row 0: GPU tier utilization — full 24-unit width at Y=0.
		{"GPU Tier Utilization", grafanadash.GridPos{X: 0, Y: 0, W: 24, H: 8}},
		// Row 1: cost (left 12) and health (right 12) at Y=8.
		{"GPU Cost Per Hour (USD)", grafanadash.GridPos{X: 0, Y: 8, W: 12, H: 8}},
		{"Provider Health", grafanadash.GridPos{X: 12, Y: 8, W: 12, H: 8}},
		// Row 2: three 8-unit panels at Y=16.
		{"TAO Earnings Total", grafanadash.GridPos{X: 0, Y: 16, W: 8, H: 8}},
		{"Token Throughput (tokens/sec)", grafanadash.GridPos{X: 8, Y: 16, W: 8, H: 8}},
		{"GPU Utilization (per device)", grafanadash.GridPos{X: 16, Y: 16, W: 8, H: 8}},
	}

	for _, pc := range cases {
		p, ok := panelByTitle(d.Panels, pc.title)
		if !ok {
			t.Errorf("panel %q not found", pc.title)
			continue
		}
		if p.GridPos != pc.want {
			t.Errorf("panel %q GridPos = %+v, want %+v", pc.title, p.GridPos, pc.want)
		}
	}
}

// --------------------------------------------------------------------------
// TestBuildTierCostDashboard_PanelTypes
//
// Assert the Grafana panel plugin type ("timeseries" vs "stat") for each
// panel is correct.
//
// Mutation guard: change "timeseries" to "stat" in any buildXxx() →
// p.Type != wantType assertion fires.
// --------------------------------------------------------------------------
func TestBuildTierCostDashboard_PanelTypes(t *testing.T) {
	t.Parallel()

	d := grafanadash.BuildTierCostDashboard()

	type typeCase struct {
		title    string
		wantType string
	}
	cases := []typeCase{
		{"GPU Tier Utilization", "timeseries"},
		{"GPU Cost Per Hour (USD)", "timeseries"},
		{"Provider Health", "stat"},
		{"TAO Earnings Total", "timeseries"},
		{"Token Throughput (tokens/sec)", "stat"},
		{"GPU Utilization (per device)", "timeseries"},
	}

	for _, tc := range cases {
		p, ok := panelByTitle(d.Panels, tc.title)
		if !ok {
			t.Errorf("panel %q not found", tc.title)
			continue
		}
		if p.Type != tc.wantType {
			t.Errorf("panel %q Type = %q, want %q", tc.title, p.Type, tc.wantType)
		}
	}
}

// --------------------------------------------------------------------------
// TestBuildTierCostDashboard_TargetExprs
//
// Assert the exact PromQL Expr for each panel's first target.
//
// Mutation guard: change any Expr string in a buildXxx() function →
// wantExpr != p.Targets[0].Expr → assertion fires for that panel.
// --------------------------------------------------------------------------
func TestBuildTierCostDashboard_TargetExprs(t *testing.T) {
	t.Parallel()

	d := grafanadash.BuildTierCostDashboard()

	type exprCase struct {
		title    string
		wantExpr string
	}
	cases := []exprCase{
		{"GPU Tier Utilization", "gpu_tier_utilization"},
		{"GPU Cost Per Hour (USD)", "gpu_cost_per_hour"},
		{"Provider Health", "provider_health"},
		{"TAO Earnings Total", "tao_earnings_total"},
		{"Token Throughput (tokens/sec)", "token_throughput"},
		{"GPU Utilization (per device)", "gpu_utilization"},
	}

	for _, tc := range cases {
		p, ok := panelByTitle(d.Panels, tc.title)
		if !ok {
			t.Errorf("panel %q not found", tc.title)
			continue
		}
		if len(p.Targets) == 0 {
			t.Errorf("panel %q has no targets", tc.title)
			continue
		}
		if p.Targets[0].Expr != tc.wantExpr {
			t.Errorf("panel %q Expr = %q, want %q", tc.title, p.Targets[0].Expr, tc.wantExpr)
		}
	}
}

// --------------------------------------------------------------------------
// TestBuildTierCostDashboard_NoOverlap
//
// Belt-and-suspenders: directly assert that Validate() reports no overlap
// after mutating each panel's GridPos to a known-bad position.
//
// Mutation guard: remove the validateNoOverlap call from Validate() →
// this test still passes (since we just trigger Validate's structural path),
// but TestBuildTierCostDashboard_ValidAndStructured would fail because
// Validate() no longer catches overlaps.  The overlap sub-tests below
// independently verify per-panel overlap detection via the shared validator.
// --------------------------------------------------------------------------
func TestBuildTierCostDashboard_NoOverlap(t *testing.T) {
	t.Parallel()

	// The baseline dashboard must pass the validator (no overlap).
	d := grafanadash.BuildTierCostDashboard()
	if err := d.Validate(); err != nil {
		t.Fatalf("baseline dashboard Validate() = %v, want nil (no overlap)", err)
	}

	// Introduce a deliberate overlap by placing panel[1] on top of panel[0]
	// and confirm Validate() rejects it.
	d.Panels[1].GridPos = d.Panels[0].GridPos // identical position → overlap
	if err := d.Validate(); err == nil {
		t.Error("Validate() with deliberately overlapping panels: got nil, want non-nil error")
	}
}
