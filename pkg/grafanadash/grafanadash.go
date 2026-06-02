// Package grafanadash generates and validates Grafana dashboard JSON for the
// Helix Cluster OS global federation health view.
//
// The global health dashboard contains three panel groups:
//   - A cell-status grid (one "stat" panel per cell, driven by real PromQL
//     queries against the helix_cell_* federation metrics).
//   - A quorum health summary "stat" panel.
//   - A cross-cell latency "timeseries" panel.
//
// All generated JSON is schema-compatible with Grafana 8+
// (schemaVersion 36).  The package is self-contained: no live Grafana
// instance is required.  Round-trip fidelity is guaranteed by encoding/json.
package grafanadash

import (
	"encoding/json"
	"errors"
	"fmt"
)

// --------------------------------------------------------------------------
// Core types
// --------------------------------------------------------------------------

// GridPos describes the position and size of a panel on the Grafana canvas.
// X and Y are zero-based grid coordinates; W and H are width and height in
// grid units (Grafana's default canvas is 24 units wide).
type GridPos struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Target is a single Prometheus data-source query attached to a panel.
type Target struct {
	// Expr is the PromQL expression.  Must be non-empty for a valid dashboard.
	Expr string `json:"expr"`
	// LegendFormat is the Grafana legend template string (may be empty).
	LegendFormat string `json:"legendFormat"`
}

// Panel represents one Grafana panel (visualization cell) in the dashboard.
type Panel struct {
	// Title is the human-readable panel heading.
	Title string `json:"title"`
	// Type is the Grafana panel plugin identifier, e.g. "stat", "timeseries",
	// "table".
	Type string `json:"type"`
	// Targets holds one or more Prometheus queries for the panel.
	Targets []Target `json:"targets"`
	// GridPos defines the panel's position and dimensions on the canvas.
	GridPos GridPos `json:"gridPos"`
}

// Dashboard is the top-level Grafana dashboard model.
type Dashboard struct {
	// Title is the dashboard display name.
	Title string `json:"title"`
	// UID is the Grafana unique identifier for the dashboard (URL slug).
	UID string `json:"uid"`
	// Panels is the ordered list of visualization panels.
	Panels []Panel `json:"panels"`
	// SchemaVersion identifies the Grafana JSON schema revision.
	// BuildGlobalHealthDashboard uses 36 (Grafana 8+).
	SchemaVersion int `json:"schemaVersion"`
	// Tags are free-form labels shown in the Grafana UI.
	Tags []string `json:"tags"`
}

// --------------------------------------------------------------------------
// Builder
// --------------------------------------------------------------------------

// cellCount is the number of federation cells represented in the status grid.
const cellCount = 6

// buildCellStatusPanels returns one "stat" panel per cell, laid out in a
// two-row grid (3 cells per row, each 8×4 units).
//
// PromQL: helix_cell_up{cell="cellN"} — 1 when the cell is reachable, 0
// otherwise.
func buildCellStatusPanels() []Panel {
	panels := make([]Panel, 0, cellCount)
	for i := 0; i < cellCount; i++ {
		cellName := fmt.Sprintf("cell%d", i+1)
		col := i % 3
		row := i / 3
		panels = append(panels, Panel{
			Title: fmt.Sprintf("Cell %d Status", i+1),
			Type:  "stat",
			Targets: []Target{
				{
					Expr:         fmt.Sprintf(`helix_cell_up{cell=%q}`, cellName),
					LegendFormat: fmt.Sprintf("Cell %d", i+1),
				},
			},
			GridPos: GridPos{
				X: col * 8,
				Y: row * 4,
				W: 8,
				H: 4,
			},
		})
	}
	return panels
}

// buildQuorumPanel returns a single "stat" panel summarising Raft quorum
// health across all cells.
//
// PromQL: sum(helix_raft_quorum_healthy) / count(helix_raft_quorum_healthy)
// — fraction of cells that have quorum (1.0 = all healthy).
func buildQuorumPanel() Panel {
	return Panel{
		Title: "Quorum Health",
		Type:  "stat",
		Targets: []Target{
			{
				Expr:         `sum(helix_raft_quorum_healthy) / count(helix_raft_quorum_healthy)`,
				LegendFormat: "Quorum fraction",
			},
		},
		// Placed immediately below the two rows of cell-status panels (Y=8).
		GridPos: GridPos{X: 0, Y: 8, W: 8, H: 4},
	}
}

// buildLatencyPanel returns a "timeseries" panel showing P99 cross-cell RPC
// latency for all cell pairs.
//
// PromQL: histogram_quantile(0.99, sum by (le, src_cell, dst_cell)
//
//	(rate(helix_cross_cell_rpc_duration_seconds_bucket[5m])))
func buildLatencyPanel() Panel {
	return Panel{
		Title: "Cross-Cell Latency P99",
		Type:  "timeseries",
		Targets: []Target{
			{
				Expr: `histogram_quantile(0.99, sum by (le, src_cell, dst_cell) ` +
					`(rate(helix_cross_cell_rpc_duration_seconds_bucket[5m])))`,
				LegendFormat: "{{src_cell}} → {{dst_cell}}",
			},
		},
		// Spans the full 24-unit width below the quorum panel (Y=12).
		GridPos: GridPos{X: 0, Y: 12, W: 24, H: 8},
	}
}

// BuildGlobalHealthDashboard constructs the canonical Helix Cluster OS global
// federation health dashboard.  It contains:
//   - 6 cell-status "stat" panels (helix_cell_up per cell).
//   - 1 quorum health "stat" panel.
//   - 1 cross-cell latency "timeseries" panel.
//
// The returned Dashboard passes Validate() without error.
func BuildGlobalHealthDashboard() Dashboard {
	panels := buildCellStatusPanels()
	panels = append(panels, buildQuorumPanel())
	panels = append(panels, buildLatencyPanel())

	return Dashboard{
		Title:         "Helix Cluster — Global Federation Health",
		UID:           "helix-global-health-v1",
		Panels:        panels,
		SchemaVersion: 36,
		Tags:          []string{"helix", "federation", "health"},
	}
}

// --------------------------------------------------------------------------
// Render / Parse
// --------------------------------------------------------------------------

// Render serialises the Dashboard to Grafana-compatible, indented JSON.
// The JSON keys match the field tags exactly so the output can be imported
// directly into Grafana.
func (d Dashboard) Render() ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}

// Parse deserialises a Grafana dashboard JSON document produced by Render (or
// any compatible source) into a Dashboard value.  The result should be passed
// to Validate before use.
func Parse(doc []byte) (Dashboard, error) {
	var d Dashboard
	if err := json.Unmarshal(doc, &d); err != nil {
		return Dashboard{}, fmt.Errorf("grafanadash: parse: %w", err)
	}
	return d, nil
}

// --------------------------------------------------------------------------
// Validator
// --------------------------------------------------------------------------

// Validate performs a structural integrity check on the Dashboard.
//
// It returns a non-nil error for any of the following conditions:
//   - Title is empty.
//   - UID is empty.
//   - SchemaVersion is zero or negative.
//   - Panels slice is empty.
//   - Any panel has an empty Title or Type.
//   - Any panel has no Targets.
//   - Any Target within any panel has an empty Expr.
//   - Any two panels have overlapping GridPos rectangles.
func (d Dashboard) Validate() error {
	if d.Title == "" {
		return errors.New("grafanadash: dashboard Title must not be empty")
	}
	if d.UID == "" {
		return errors.New("grafanadash: dashboard UID must not be empty")
	}
	if d.SchemaVersion <= 0 {
		return errors.New("grafanadash: SchemaVersion must be > 0")
	}
	if len(d.Panels) == 0 {
		return errors.New("grafanadash: dashboard must contain at least one panel")
	}

	for i, p := range d.Panels {
		if p.Title == "" {
			return fmt.Errorf("grafanadash: panel[%d] has empty Title", i)
		}
		if p.Type == "" {
			return fmt.Errorf("grafanadash: panel[%d] %q has empty Type", i, p.Title)
		}
		if len(p.Targets) == 0 {
			return fmt.Errorf("grafanadash: panel[%d] %q has no Targets", i, p.Title)
		}
		for j, t := range p.Targets {
			if t.Expr == "" {
				return fmt.Errorf("grafanadash: panel[%d] %q target[%d] has empty Expr",
					i, p.Title, j)
			}
		}
	}

	if err := validateNoOverlap(d.Panels); err != nil {
		return err
	}

	return nil
}

// validateNoOverlap checks that no two panels occupy overlapping grid cells.
// Two rectangles overlap when their projections on both the X and Y axes
// overlap strictly (i.e. [x1, x1+w1) ∩ [x2, x2+w2) ≠ ∅ AND
// [y1, y1+h1) ∩ [y2, y2+h2) ≠ ∅).
//
// Panels with zero area (W==0 or H==0) are ignored.
// Error messages use the original slice indices so they are always
// deterministic: panels are compared in insertion order (a < b).
func validateNoOverlap(panels []Panel) error {
	for a := 0; a < len(panels); a++ {
		for b := a + 1; b < len(panels); b++ {
			pi, pj := panels[a], panels[b]

			// Skip zero-area panels.
			if pi.GridPos.W == 0 || pi.GridPos.H == 0 {
				continue
			}
			if pj.GridPos.W == 0 || pj.GridPos.H == 0 {
				continue
			}

			if rectsOverlap(pi.GridPos, pj.GridPos) {
				return fmt.Errorf(
					"grafanadash: panels[%d] %q and panels[%d] %q have overlapping GridPos",
					a, pi.Title, b, pj.Title,
				)
			}
		}
	}
	return nil
}

// rectsOverlap returns true when two GridPos rectangles share at least one
// grid cell.
func rectsOverlap(a, b GridPos) bool {
	// X-axis overlap: [a.X, a.X+a.W) ∩ [b.X, b.X+b.W) ≠ ∅
	xOverlap := a.X < b.X+b.W && b.X < a.X+a.W
	// Y-axis overlap: [a.Y, a.Y+a.H) ∩ [b.Y, b.Y+b.H) ≠ ∅
	yOverlap := a.Y < b.Y+b.H && b.Y < a.Y+a.H
	return xOverlap && yOverlap
}
