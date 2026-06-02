package grafanadash_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/grafanadash"
)

// --------------------------------------------------------------------------
// Helper
// --------------------------------------------------------------------------

// panelByTitle returns the first panel whose Title matches t, or the zero
// value and false if none found.
func panelByTitle(panels []grafanadash.Panel, title string) (grafanadash.Panel, bool) {
	for _, p := range panels {
		if p.Title == title {
			return p, true
		}
	}
	return grafanadash.Panel{}, false
}

// --------------------------------------------------------------------------
// TestBuildGlobalHealthDashboard
//
// Mutation proof (panel count): change BuildGlobalHealthDashboard to return a
// Dashboard with an empty Panels slice → Validate() returns non-nil → test
// fails.
// Mutation proof (UID): change UID to "wrong-uid" → d.UID != wantUID check
// fires.
// Mutation proof (Title): change Title to "x" → d.Title != wantTitle check
// fires.
// Mutation proof (SchemaVersion): change SchemaVersion to 1 →
// d.SchemaVersion != 36 check fires.
// Mutation proof (Tags): remove "helix" from Tags → wantTags membership check
// fires.
// Mutation proof (Type): change "stat" to "table" in buildCellStatusPanels →
// p.Type != "stat" check fires.
// Mutation proof (GridPos): shift cell panels by Y+100 → GridPos assertion
// fires.
// --------------------------------------------------------------------------
func TestBuildGlobalHealthDashboard(t *testing.T) {
	t.Parallel()
	d := grafanadash.BuildGlobalHealthDashboard()

	// Must pass structural validation immediately.
	if err := d.Validate(); err != nil {
		t.Fatalf("BuildGlobalHealthDashboard: Validate() = %v, want nil", err)
	}

	// --- Domain contract: exact field values --------------------------------

	const wantUID = "helix-global-health-v1"
	if d.UID != wantUID {
		t.Errorf("UID = %q, want %q", d.UID, wantUID)
	}

	const wantTitle = "Helix Cluster — Global Federation Health"
	if d.Title != wantTitle {
		t.Errorf("Title = %q, want %q", d.Title, wantTitle)
	}

	const wantSchema = 36
	if d.SchemaVersion != wantSchema {
		t.Errorf("SchemaVersion = %d, want %d", d.SchemaVersion, wantSchema)
	}

	// Tags must contain exactly "helix", "federation", "health" (order-independent).
	// Mutation proof: remove any one tag → the membership check below fires.
	wantTags := []string{"helix", "federation", "health"}
	tagSet := make(map[string]bool, len(d.Tags))
	for _, tag := range d.Tags {
		tagSet[tag] = true
	}
	for _, wt := range wantTags {
		if !tagSet[wt] {
			t.Errorf("Tags %v missing required tag %q", d.Tags, wt)
		}
	}

	// --- Panel presence, Targets, and Type ---------------------------------

	// Expected panel titles that must be present (exact strings).
	wantTitles := []string{
		"Cell 1 Status",
		"Cell 2 Status",
		"Cell 3 Status",
		"Cell 4 Status",
		"Cell 5 Status",
		"Cell 6 Status",
		"Quorum Health",
		"Cross-Cell Latency P99",
	}

	for _, title := range wantTitles {
		p, ok := panelByTitle(d.Panels, title)
		if !ok {
			t.Errorf("panel %q not found", title)
			continue
		}
		// Each panel must have at least one target with a non-empty Expr.
		// Mutation proof for cell panels: change Expr to "" in
		// buildCellStatusPanels → first assert below fails.
		if len(p.Targets) == 0 {
			t.Errorf("panel %q: no targets", title)
			continue
		}
		for j, tgt := range p.Targets {
			if tgt.Expr == "" {
				t.Errorf("panel %q target[%d]: empty Expr", title, j)
			}
		}
	}

	// Cell and quorum panels must have Type "stat"; latency panel "timeseries".
	// Mutation proof: change "stat" to "table" in buildCellStatusPanels →
	// p.Type != "stat" fires for each cell panel.
	statPanels := []string{
		"Cell 1 Status", "Cell 2 Status", "Cell 3 Status",
		"Cell 4 Status", "Cell 5 Status", "Cell 6 Status",
		"Quorum Health",
	}
	for _, title := range statPanels {
		p, ok := panelByTitle(d.Panels, title)
		if !ok {
			continue // already reported above
		}
		if p.Type != "stat" {
			t.Errorf("panel %q: Type = %q, want \"stat\"", title, p.Type)
		}
	}

	p, ok := panelByTitle(d.Panels, "Cross-Cell Latency P99")
	if ok && p.Type != "timeseries" {
		t.Errorf("panel %q: Type = %q, want \"timeseries\"", "Cross-Cell Latency P99", p.Type)
	}

	// --- GridPos layout assertions -----------------------------------------
	// Cell panels occupy a 3×2 grid of 8×4 slots starting at (X,Y)=(0,0).
	// Row 0: cells 1-3 at Y=0, X=0,8,16.  Row 1: cells 4-6 at Y=4, X=0,8,16.
	// Quorum panel: X=0, Y=8, W=8, H=4.
	// Latency panel: X=0, Y=12, W=24, H=8.
	//
	// Mutation proof: shift all cell panels by Y+100 → wantPos.Y != p.GridPos.Y
	// assertion fires.
	type posCase struct {
		title string
		want  grafanadash.GridPos
	}
	posCases := []posCase{
		{"Cell 1 Status", grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4}},
		{"Cell 2 Status", grafanadash.GridPos{X: 8, Y: 0, W: 8, H: 4}},
		{"Cell 3 Status", grafanadash.GridPos{X: 16, Y: 0, W: 8, H: 4}},
		{"Cell 4 Status", grafanadash.GridPos{X: 0, Y: 4, W: 8, H: 4}},
		{"Cell 5 Status", grafanadash.GridPos{X: 8, Y: 4, W: 8, H: 4}},
		{"Cell 6 Status", grafanadash.GridPos{X: 16, Y: 4, W: 8, H: 4}},
		{"Quorum Health", grafanadash.GridPos{X: 0, Y: 8, W: 8, H: 4}},
		{"Cross-Cell Latency P99", grafanadash.GridPos{X: 0, Y: 12, W: 24, H: 8}},
	}
	for _, pc := range posCases {
		panel, ok := panelByTitle(d.Panels, pc.title)
		if !ok {
			continue // already reported above
		}
		if panel.GridPos != pc.want {
			t.Errorf("panel %q: GridPos = %+v, want %+v", pc.title, panel.GridPos, pc.want)
		}
	}

	// Total panel count must equal exactly 8 (6 cell + 1 quorum + 1 latency).
	// Mutation proof: drop buildQuorumPanel() from BuildGlobalHealthDashboard →
	// len(d.Panels) == 7 → assertion fails.
	if got := len(d.Panels); got != 8 {
		t.Errorf("panel count = %d, want 8", got)
	}
}

// --------------------------------------------------------------------------
// TestBuildGlobalHealthDashboard_CellPanelExprs
//
// Assert the exact PromQL for each cell-status panel: must reference
// helix_cell_up with the correct per-cell label value.
//
// Mutation proof (label present): change the Expr template in
// buildCellStatusPanels from `helix_cell_up{cell=%q}` to `helix_cell_up`
// (drop label) → wantExpr != expr check fires.
//
// Mutation proof (per-cell correctness): hard-code cellName to always be
// "cell1" in buildCellStatusPanels → for i>=2, expr contains "cell1" but
// wantExpr is `helix_cell_up{cell="cell2"}` etc. → the exact equality check
// fires for panels 2-6.
//
// Mutation proof (bounds): empty Targets on a cell panel → len(p.Targets)==0
// guard fires a clean t.Errorf instead of a panic.
// --------------------------------------------------------------------------
func TestBuildGlobalHealthDashboard_CellPanelExprs(t *testing.T) {
	t.Parallel()
	d := grafanadash.BuildGlobalHealthDashboard()

	for i := 1; i <= 6; i++ {
		titleStr := fmt.Sprintf("Cell %d Status", i)
		p, ok := panelByTitle(d.Panels, titleStr)
		if !ok {
			t.Errorf("cell panel %q not found", titleStr)
			continue
		}
		// Bounds guard: report cleanly instead of panic on empty Targets.
		if len(p.Targets) == 0 {
			t.Errorf("panel %q has no targets", titleStr)
			continue
		}
		expr := p.Targets[0].Expr
		// Exact expression: helix_cell_up{cell="cellN"} where N == i.
		wantExpr := fmt.Sprintf(`helix_cell_up{cell="cell%d"}`, i)
		if expr != wantExpr {
			t.Errorf("panel %q: Expr = %q, want %q", titleStr, expr, wantExpr)
		}
	}
}

// --------------------------------------------------------------------------
// TestBuildGlobalHealthDashboard_QuorumExpr
//
// Assert the quorum panel carries the correct aggregate PromQL.
// Mutation proof: change the Expr in buildQuorumPanel to a different string →
// Contains check fails.
// --------------------------------------------------------------------------
func TestBuildGlobalHealthDashboard_QuorumExpr(t *testing.T) {
	t.Parallel()
	d := grafanadash.BuildGlobalHealthDashboard()

	p, ok := panelByTitle(d.Panels, "Quorum Health")
	if !ok {
		t.Fatal("panel 'Quorum Health' not found")
	}
	expr := p.Targets[0].Expr
	wantSubstr := "helix_raft_quorum_healthy"
	if !strings.Contains(expr, wantSubstr) {
		t.Errorf("Quorum Health Expr = %q, want substring %q", expr, wantSubstr)
	}
}

// --------------------------------------------------------------------------
// TestBuildGlobalHealthDashboard_LatencyExpr
//
// Assert the latency panel carries a histogram_quantile(0.99, ...) PromQL.
// Mutation proof: change the Expr in buildLatencyPanel to drop
// histogram_quantile → Contains("histogram_quantile(0.99") fails.
// --------------------------------------------------------------------------
func TestBuildGlobalHealthDashboard_LatencyExpr(t *testing.T) {
	t.Parallel()
	d := grafanadash.BuildGlobalHealthDashboard()

	p, ok := panelByTitle(d.Panels, "Cross-Cell Latency P99")
	if !ok {
		t.Fatal("panel 'Cross-Cell Latency P99' not found")
	}
	expr := p.Targets[0].Expr
	if !strings.Contains(expr, "histogram_quantile(0.99") {
		t.Errorf("Latency Expr = %q, missing histogram_quantile(0.99", expr)
	}
	if !strings.Contains(expr, "helix_cross_cell_rpc_duration_seconds_bucket") {
		t.Errorf("Latency Expr = %q, missing helix_cross_cell_rpc_duration_seconds_bucket", expr)
	}
}

// --------------------------------------------------------------------------
// TestRenderParseRoundTrip
//
// Proves that Render→Parse reproduces Title, UID, panel count, all panel
// titles, and all target Exprs EXACTLY.
// Mutation proof: in Render(), replace json.MarshalIndent(d, …) with
// json.MarshalIndent(Dashboard{}, …) → Parse produces a zero-value Dashboard
// → Title == "" check fires.
// --------------------------------------------------------------------------
func TestRenderParseRoundTrip(t *testing.T) {
	t.Parallel()
	orig := grafanadash.BuildGlobalHealthDashboard()

	doc, err := orig.Render()
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	got, err := grafanadash.Parse(doc)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	// Title
	if got.Title != orig.Title {
		t.Errorf("Title: got %q, want %q", got.Title, orig.Title)
	}
	// UID
	if got.UID != orig.UID {
		t.Errorf("UID: got %q, want %q", got.UID, orig.UID)
	}
	// SchemaVersion
	if got.SchemaVersion != orig.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", got.SchemaVersion, orig.SchemaVersion)
	}
	// Panel count
	if len(got.Panels) != len(orig.Panels) {
		t.Fatalf("panel count: got %d, want %d", len(got.Panels), len(orig.Panels))
	}
	// Panel titles and target exprs — exact match in order.
	for i, op := range orig.Panels {
		gp := got.Panels[i]
		if gp.Title != op.Title {
			t.Errorf("panel[%d] Title: got %q, want %q", i, gp.Title, op.Title)
		}
		if gp.Type != op.Type {
			t.Errorf("panel[%d] %q Type: got %q, want %q", i, op.Title, gp.Type, op.Type)
		}
		if len(gp.Targets) != len(op.Targets) {
			t.Errorf("panel[%d] %q: target count got %d, want %d",
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
// TestRenderProducesValidGrafanaJSON
//
// Asserts:
//  1. json.Valid(doc) is true.
//  2. Unmarshalling into a generic map contains "panels", "schemaVersion",
//     "title" keys.
//  3. "schemaVersion" is > 0.
//
// Mutation proof: change json.MarshalIndent to return []byte("not-json") →
// json.Valid fails immediately.
// --------------------------------------------------------------------------
func TestRenderProducesValidGrafanaJSON(t *testing.T) {
	t.Parallel()
	d := grafanadash.BuildGlobalHealthDashboard()

	doc, err := d.Render()
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	// 1. Valid JSON.
	if !json.Valid(doc) {
		t.Fatalf("Render() output is not valid JSON")
	}

	// 2. Generic map must contain required Grafana top-level keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		t.Fatalf("unmarshal to generic map: %v", err)
	}
	for _, key := range []string{"panels", "schemaVersion", "title"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("rendered JSON missing key %q", key)
		}
	}

	// 3. schemaVersion must decode to a positive integer.
	var sv int
	if err := json.Unmarshal(raw["schemaVersion"], &sv); err != nil {
		t.Fatalf("schemaVersion decode: %v", err)
	}
	if sv <= 0 {
		t.Errorf("schemaVersion = %d, want > 0", sv)
	}
}

// --------------------------------------------------------------------------
// TestValidate_EmptyUID
//
// Mutation proof: remove the UID-empty check from Validate() → this test
// receives nil error → t.Error fires.
// --------------------------------------------------------------------------
func TestValidate_EmptyUID(t *testing.T) {
	t.Parallel()
	d := grafanadash.BuildGlobalHealthDashboard()
	d.UID = ""
	if err := d.Validate(); err == nil {
		t.Error("Validate() with empty UID: got nil error, want non-nil")
	}
}

// --------------------------------------------------------------------------
// TestValidate_ZeroSchemaVersion
//
// Mutation proof: change the SchemaVersion guard from `<= 0` to `< 0` →
// SchemaVersion==0 passes → test receives nil error → t.Error fires.
// --------------------------------------------------------------------------
func TestValidate_ZeroSchemaVersion(t *testing.T) {
	t.Parallel()
	d := grafanadash.BuildGlobalHealthDashboard()
	d.SchemaVersion = 0
	if err := d.Validate(); err == nil {
		t.Error("Validate() with SchemaVersion=0: got nil error, want non-nil")
	}
}

// --------------------------------------------------------------------------
// TestValidate_PanelWithNoTargets
//
// Mutation proof: remove the `len(p.Targets) == 0` guard from Validate() →
// this test receives nil error → t.Error fires.
// --------------------------------------------------------------------------
func TestValidate_PanelWithNoTargets(t *testing.T) {
	t.Parallel()
	d := grafanadash.Dashboard{
		Title:         "Test",
		UID:           "test-uid",
		SchemaVersion: 36,
		Panels: []grafanadash.Panel{
			{
				Title:   "Empty Panel",
				Type:    "stat",
				Targets: nil,
				GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			},
		},
	}
	if err := d.Validate(); err == nil {
		t.Error("Validate() with panel having no Targets: got nil error, want non-nil")
	}
}

// --------------------------------------------------------------------------
// TestValidate_PanelWithEmptyExpr
//
// Mutation proof: remove the `t.Expr == ""` check from Validate() → this
// test receives nil error → t.Error fires.
// --------------------------------------------------------------------------
func TestValidate_PanelWithEmptyExpr(t *testing.T) {
	t.Parallel()
	d := grafanadash.Dashboard{
		Title:         "Test",
		UID:           "test-uid",
		SchemaVersion: 36,
		Panels: []grafanadash.Panel{
			{
				Title: "Bad Expr Panel",
				Type:  "stat",
				Targets: []grafanadash.Target{
					{Expr: "", LegendFormat: "x"},
				},
				GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			},
		},
	}
	if err := d.Validate(); err == nil {
		t.Error("Validate() with empty Expr: got nil error, want non-nil")
	}
}

// --------------------------------------------------------------------------
// TestValidate_OverlappingGridPos
//
// Two panels that share at least one grid cell must be rejected.
// Mutation proof: delete the `validateNoOverlap(d.Panels)` call from
// Validate() → this test receives nil error → t.Error fires.
// --------------------------------------------------------------------------
func TestValidate_OverlappingGridPos(t *testing.T) {
	t.Parallel()
	d := grafanadash.Dashboard{
		Title:         "Test",
		UID:           "test-uid",
		SchemaVersion: 36,
		Panels: []grafanadash.Panel{
			{
				Title:   "Panel A",
				Type:    "stat",
				Targets: []grafanadash.Target{{Expr: `up`, LegendFormat: "up"}},
				// Occupies columns 0-7, rows 0-3.
				GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			},
			{
				Title:   "Panel B",
				Type:    "stat",
				Targets: []grafanadash.Target{{Expr: `up`, LegendFormat: "up"}},
				// Starts at (4,2): overlaps with Panel A on both axes.
				GridPos: grafanadash.GridPos{X: 4, Y: 2, W: 8, H: 4},
			},
		},
	}
	if err := d.Validate(); err == nil {
		t.Error("Validate() with overlapping panels: got nil error, want non-nil")
	}
}

// --------------------------------------------------------------------------
// TestValidate_NonOverlappingGridPos_OK
//
// Panels that are adjacent (share an edge but not interior cells) must be
// accepted.
// Mutation proof: change rectsOverlap to use `<=` instead of `<` for the
// overlap condition → adjacent panels appear to overlap → Validate() returns
// non-nil → t.Fatal fires.
// --------------------------------------------------------------------------
func TestValidate_NonOverlappingGridPos_OK(t *testing.T) {
	t.Parallel()
	d := grafanadash.Dashboard{
		Title:         "Test",
		UID:           "test-uid",
		SchemaVersion: 36,
		Panels: []grafanadash.Panel{
			{
				Title:   "Left Panel",
				Type:    "stat",
				Targets: []grafanadash.Target{{Expr: `up`, LegendFormat: "up"}},
				// Occupies columns 0-7, rows 0-3.
				GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			},
			{
				Title:   "Right Panel",
				Type:    "stat",
				Targets: []grafanadash.Target{{Expr: `up`, LegendFormat: "up"}},
				// Starts at column 8 — exactly adjacent, no overlap.
				GridPos: grafanadash.GridPos{X: 8, Y: 0, W: 8, H: 4},
			},
		},
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() with adjacent non-overlapping panels: unexpected error: %v", err)
	}
}

// --------------------------------------------------------------------------
// TestValidate_DistinctErrors
//
// Each validation path must return a distinct non-nil error.  This ensures
// the validator does not short-circuit all paths to a single generic error.
// Mutation proof: unify all validation errors to a single "invalid dashboard"
// string → the strings.Contains checks still pass → but each error is no
// longer identifiable by its specific substring → the assertions on each
// specific substring would fail.
// --------------------------------------------------------------------------
func TestValidate_DistinctErrors(t *testing.T) {
	t.Parallel()

	validPanel := grafanadash.Panel{
		Title:   "OK",
		Type:    "stat",
		Targets: []grafanadash.Target{{Expr: `up`, LegendFormat: "x"}},
		GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 4, H: 4},
	}

	cases := []struct {
		name    string
		mutate  func(*grafanadash.Dashboard)
		wantSub string
	}{
		{
			name:    "empty_title",
			mutate:  func(d *grafanadash.Dashboard) { d.Title = "" },
			wantSub: "Title",
		},
		{
			name:    "empty_uid",
			mutate:  func(d *grafanadash.Dashboard) { d.UID = "" },
			wantSub: "UID",
		},
		{
			name:    "zero_schema_version",
			mutate:  func(d *grafanadash.Dashboard) { d.SchemaVersion = 0 },
			wantSub: "SchemaVersion",
		},
		{
			name: "no_targets",
			mutate: func(d *grafanadash.Dashboard) {
				d.Panels = []grafanadash.Panel{
					{Title: "P", Type: "stat", Targets: nil,
						GridPos: grafanadash.GridPos{W: 4, H: 4}},
				}
			},
			wantSub: "no Targets",
		},
		{
			name: "empty_expr",
			mutate: func(d *grafanadash.Dashboard) {
				d.Panels = []grafanadash.Panel{
					{Title: "P", Type: "stat",
						Targets: []grafanadash.Target{{Expr: ""}},
						GridPos: grafanadash.GridPos{W: 4, H: 4}},
				}
			},
			wantSub: "empty Expr",
		},
		{
			name: "overlapping_panels",
			mutate: func(d *grafanadash.Dashboard) {
				d.Panels = []grafanadash.Panel{
					{Title: "A", Type: "stat",
						Targets: []grafanadash.Target{{Expr: `up`}},
						GridPos: grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4}},
					{Title: "B", Type: "stat",
						Targets: []grafanadash.Target{{Expr: `up`}},
						GridPos: grafanadash.GridPos{X: 4, Y: 2, W: 8, H: 4}},
				}
			},
			wantSub: "overlapping GridPos",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := grafanadash.Dashboard{
				Title:         "OK Dashboard",
				UID:           "ok-uid",
				SchemaVersion: 36,
				Panels:        []grafanadash.Panel{validPanel},
			}
			tc.mutate(&base)
			err := base.Validate()
			if err == nil {
				t.Fatalf("Validate() with %s: got nil, want non-nil error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Validate() with %s: error %q does not contain %q",
					tc.name, err.Error(), tc.wantSub)
			}
		})
	}
}

// --------------------------------------------------------------------------
// TestRenderJSON_PanelsKey
//
// Verify that "panels" in the rendered JSON is an array with the correct
// element count, not e.g. a null or object.
// Mutation proof: change Panels field JSON tag from `json:"panels"` to
// `json:"panel"` → raw["panels"] is absent → t.Fatal fires.
// --------------------------------------------------------------------------
func TestRenderJSON_PanelsKey(t *testing.T) {
	t.Parallel()
	d := grafanadash.BuildGlobalHealthDashboard()
	doc, err := d.Render()
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	panelsRaw, ok := raw["panels"]
	if !ok {
		t.Fatal("rendered JSON has no 'panels' key")
	}

	var panels []json.RawMessage
	if err := json.Unmarshal(panelsRaw, &panels); err != nil {
		t.Fatalf("panels is not a JSON array: %v", err)
	}
	// 6 cell + 1 quorum + 1 latency.
	if len(panels) != 8 {
		t.Errorf("JSON panels count = %d, want 8", len(panels))
	}
}

// --------------------------------------------------------------------------
// TestRectsOverlap_VariousCases
//
// Directly exercises the overlap logic through the Validate path using a
// table of panel-pair configurations with expected outcomes.
// Mutation proof: replace `a.X < b.X+b.W` with `a.X <= b.X+b.W` in
// rectsOverlap → adjacent-column case incorrectly overlaps → "adj_col" case
// sees unexpected error.
// --------------------------------------------------------------------------
func TestRectsOverlap_VariousCases(t *testing.T) {
	t.Parallel()

	goodTarget := []grafanadash.Target{{Expr: `up`}}

	cases := []struct {
		name    string
		a, b    grafanadash.GridPos
		wantErr bool
	}{
		{
			name:    "identical_positions",
			a:       grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			b:       grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			wantErr: true,
		},
		{
			name:    "partial_x_overlap_same_row",
			a:       grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			b:       grafanadash.GridPos{X: 4, Y: 0, W: 8, H: 4},
			wantErr: true,
		},
		{
			name:    "partial_y_overlap_same_col",
			a:       grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			b:       grafanadash.GridPos{X: 0, Y: 2, W: 8, H: 4},
			wantErr: true,
		},
		{
			name:    "adj_col",
			a:       grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			b:       grafanadash.GridPos{X: 8, Y: 0, W: 8, H: 4},
			wantErr: false,
		},
		{
			name:    "adj_row",
			a:       grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			b:       grafanadash.GridPos{X: 0, Y: 4, W: 8, H: 4},
			wantErr: false,
		},
		{
			name:    "diag_adjacent",
			a:       grafanadash.GridPos{X: 0, Y: 0, W: 8, H: 4},
			b:       grafanadash.GridPos{X: 8, Y: 4, W: 8, H: 4},
			wantErr: false,
		},
		{
			name:    "b_contained_in_a",
			a:       grafanadash.GridPos{X: 0, Y: 0, W: 12, H: 8},
			b:       grafanadash.GridPos{X: 2, Y: 2, W: 4, H: 4},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := grafanadash.Dashboard{
				Title:         "T",
				UID:           "u",
				SchemaVersion: 36,
				Panels: []grafanadash.Panel{
					{Title: "A", Type: "stat", Targets: goodTarget, GridPos: tc.a},
					{Title: "B", Type: "stat", Targets: goodTarget, GridPos: tc.b},
				},
			}
			err := d.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("case %q: expected overlap error, got nil", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("case %q: unexpected error: %v", tc.name, err)
			}
		})
	}
}
