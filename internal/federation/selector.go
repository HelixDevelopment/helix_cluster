package federation

import (
	"errors"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

// CellHealth enumerates the liveness state of a candidate cell as reported by
// the federation hub.
type CellHealth string

const (
	// CellReady means the cell's Karmada gateway is reachable and the cell can
	// accept workloads.
	CellReady CellHealth = "Ready"
	// CellNotReady means the cell's gateway/control plane is down; the global
	// layer must not place (or must reschedule away from) this cell.
	CellNotReady CellHealth = "NotReady"
)

// Cell is a candidate member cluster the global layer can place a workload on.
// The fields model the constraint dimensions the closure criteria call out:
// data locality, latency, cost and compliance.
type Cell struct {
	// Name is the Karmada cluster name (used as clusterNames entry).
	Name string
	// Region is the geographic region, used for data-locality scoring.
	Region string
	// Zones lists the cell's failure domains, used for spread.
	Zones []string
	// LatencyMS is the measured network latency (ms) from the workload's
	// preferred ingress to this cell. Lower is better.
	LatencyMS float64
	// CostPerHour is the relative compute cost. Lower is better.
	CostPerHour float64
	// ComplianceTags are the compliance regimes the cell satisfies
	// (e.g. "gdpr", "hipaa", "soc2").
	ComplianceTags []string
	// Labels are the cluster labels (used when a policy selects cells by label).
	Labels map[string]string
	// Health is the current liveness of the cell.
	Health CellHealth
}

func (c Cell) hasCompliance(tag string) bool {
	for _, t := range c.ComplianceTags {
		if t == tag {
			return true
		}
	}
	return false
}

// Constraints describe what the workload needs from a cell. The selector first
// filters cells that fail any hard constraint, then scores the survivors.
type Constraints struct {
	// PreferredRegion expresses data locality: a cell in this region scores
	// the data-locality component fully; others score 0 for that component.
	PreferredRegion string
	// RequiredCompliance lists compliance tags a cell MUST satisfy (hard
	// filter). A cell missing any required tag is rejected outright.
	RequiredCompliance []string
	// MaxLatencyMS, when > 0, rejects cells slower than this (hard filter).
	MaxLatencyMS float64
	// MaxCostPerHour, when > 0, rejects cells more expensive than this.
	MaxCostPerHour float64
	// Weights tune the relative importance of each scoring component. Zero
	// weights fall back to DefaultWeights so callers need not set all of them.
	Weights Weights
}

// Weights tune the soft-constraint scoring. All components are normalised to
// [0,1] before weighting, so weights are directly comparable.
type Weights struct {
	DataLocality float64
	Latency      float64
	Cost         float64
	Compliance   float64
}

// DefaultWeights is used when a caller leaves Constraints.Weights zero.
var DefaultWeights = Weights{
	DataLocality: 0.40,
	Latency:      0.30,
	Cost:         0.20,
	Compliance:   0.10,
}

func (w Weights) orDefault() Weights {
	if w == (Weights{}) {
		return DefaultWeights
	}
	return w
}

// CellScore is a scored candidate cell, surfaced so callers (and tests) can
// see exactly why a cell won or lost.
type CellScore struct {
	Cell  Cell
	Score float64
	// Components records the per-dimension contribution (already weighted) so
	// the decision is explainable, not a black box.
	Components map[string]float64
	// Filtered is true when the cell failed a hard constraint; Reason explains
	// which one.
	Filtered bool
	Reason   string
}

// Decision is the outcome of a cell-selection (or re-selection) pass.
type Decision struct {
	// RunUUID is a unique id stamped on every decision so a federated run can
	// be correlated across logs/metrics and the live Karmada status capture.
	RunUUID string
	// Chosen is the winning cell name (empty when no cell is eligible).
	Chosen string
	// Ranked lists every candidate with its score, best-first; filtered cells
	// appear with Filtered=true and a Reason.
	Ranked []CellScore
	// Elapsed is how long the decision took, used to assert the failover
	// budget.
	Elapsed time.Duration
	// FailedOver is true when this decision excluded a previously-hosting cell
	// that went NotReady and picked a surviving cell instead.
	FailedOver bool
	// PreviousCell is the cell the workload was on before a failover (empty
	// for an initial placement).
	PreviousCell string
}

// ErrNoEligibleCell is returned when every candidate cell is filtered out.
var ErrNoEligibleCell = errors.New("federation: no eligible cell satisfies the constraints")

// Selector performs global cell selection. It is stateless and safe for
// concurrent use; the (mutable) hub state lives in the fake hub used by tests
// and, in production, in the real Karmada control plane.
type Selector struct {
	// now is the clock, overridable in tests.
	now func() time.Time
	// newUUID is the id generator, overridable in tests for determinism.
	newUUID func() string
}

// NewSelector returns a Selector wired to the real clock and UUID generator.
func NewSelector() *Selector {
	return &Selector{now: time.Now, newUUID: uuid.NewString}
}

// Select scores the candidate cells against the constraints and returns a
// Decision whose Chosen field names the best eligible cell. Cells failing a
// hard constraint are filtered (recorded with a reason) and never chosen.
func (s *Selector) Select(cells []Cell, c Constraints) (Decision, error) {
	return s.selectExcluding(cells, c, "", false)
}

// Reselect performs a failover decision: it excludes downCell (the cell whose
// gateway died) and any cell that is not Ready, then picks the best surviving
// cell. The returned Decision has FailedOver=true and PreviousCell=downCell.
//
// This is the in-process model of the closure-criteria failover: when the
// hosting cell's gateway is killed, the global layer must re-pick a surviving
// cell. Tests drive a fake hub state machine that marks a cell NotReady and
// assert Reselect lands on a different, Ready cell within the budget.
func (s *Selector) Reselect(cells []Cell, c Constraints, downCell string) (Decision, error) {
	d, err := s.selectExcluding(cells, c, downCell, true)
	if err != nil {
		return d, err
	}
	return d, nil
}

func (s *Selector) selectExcluding(cells []Cell, c Constraints, exclude string, failover bool) (Decision, error) {
	start := s.now()
	w := c.Weights.orDefault()

	ranked := make([]CellScore, 0, len(cells))
	for _, cell := range cells {
		cs := scoreCell(cell, c, w)
		// Hard exclusions that are not "constraint" failures: the explicitly
		// excluded (dead) cell and any NotReady cell.
		if exclude != "" && cell.Name == exclude {
			cs.Filtered = true
			cs.Reason = "excluded: hosting cell marked down"
			cs.Score = 0
		} else if cell.Health != CellReady {
			// Fail-closed admission: only a cell the hub has affirmatively
			// reported Ready may be chosen. CellNotReady, an empty/unreported
			// liveness ("") or any unrecognized state (e.g. "Draining",
			// "Unknown", or hostile garbage from a remote gateway) is NOT
			// trusted and must be filtered — admitting an unknown-liveness cell
			// is a default-allow trust gap (a workload could be placed on a
			// cell whose gateway state was never confirmed reachable).
			cs.Filtered = true
			if cell.Health == CellNotReady {
				cs.Reason = "filtered: cell not ready (gateway unreachable)"
			} else {
				cs.Reason = "filtered: cell liveness not affirmatively Ready (unknown/unreported state)"
			}
			cs.Score = 0
		}
		ranked = append(ranked, cs)
	}

	// Best score first; ties broken deterministically by cell name so the
	// decision is reproducible.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Filtered != ranked[j].Filtered {
			return !ranked[i].Filtered // unfiltered ahead of filtered
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Cell.Name < ranked[j].Cell.Name
	})

	d := Decision{
		RunUUID:    s.newUUID(),
		Ranked:     ranked,
		Elapsed:    s.now().Sub(start),
		FailedOver: failover,
	}
	if failover {
		d.PreviousCell = exclude
	}

	for _, cs := range ranked {
		if !cs.Filtered {
			d.Chosen = cs.Cell.Name
			return d, nil
		}
	}
	return d, ErrNoEligibleCell
}

// scoreCell applies hard filters then computes the weighted soft score.
func scoreCell(cell Cell, c Constraints, w Weights) CellScore {
	cs := CellScore{Cell: cell, Components: map[string]float64{}}

	// Hard compliance filter.
	for _, req := range c.RequiredCompliance {
		if !cell.hasCompliance(req) {
			cs.Filtered = true
			cs.Reason = "filtered: missing required compliance tag " + req
			return cs
		}
	}
	// Hard latency filter.
	if c.MaxLatencyMS > 0 && cell.LatencyMS > c.MaxLatencyMS {
		cs.Filtered = true
		cs.Reason = "filtered: latency exceeds MaxLatencyMS"
		return cs
	}
	// Hard cost filter.
	if c.MaxCostPerHour > 0 && cell.CostPerHour > c.MaxCostPerHour {
		cs.Filtered = true
		cs.Reason = "filtered: cost exceeds MaxCostPerHour"
		return cs
	}

	// Soft scoring. Each component is normalised to [0,1].
	dataLocality := 0.0
	if c.PreferredRegion != "" && cell.Region == c.PreferredRegion {
		dataLocality = 1.0
	} else if c.PreferredRegion == "" {
		// No locality preference -> neutral full credit so the dimension does
		// not penalise anyone.
		dataLocality = 1.0
	}

	// Latency: closer to 0ms => closer to 1.0. Use a smooth decay so a 0ms
	// cell scores 1 and latency degrades gracefully.
	latencyScore := 1.0 / (1.0 + cell.LatencyMS/50.0)
	if cell.LatencyMS <= 0 {
		latencyScore = 1.0
	}

	// Cost: cheaper => higher. Same smooth decay form.
	costScore := 1.0 / (1.0 + cell.CostPerHour)
	if cell.CostPerHour <= 0 {
		costScore = 1.0
	}

	// Compliance bonus: more satisfied required-beyond tags help, but with no
	// required set we give full credit.
	complianceScore := 1.0
	if len(c.RequiredCompliance) > 0 {
		got := 0
		for _, req := range c.RequiredCompliance {
			if cell.hasCompliance(req) {
				got++
			}
		}
		complianceScore = float64(got) / float64(len(c.RequiredCompliance))
	}

	cs.Components["dataLocality"] = round(w.DataLocality * dataLocality)
	cs.Components["latency"] = round(w.Latency * latencyScore)
	cs.Components["cost"] = round(w.Cost * costScore)
	cs.Components["compliance"] = round(w.Compliance * complianceScore)
	cs.Score = round(cs.Components["dataLocality"] + cs.Components["latency"] +
		cs.Components["cost"] + cs.Components["compliance"])
	// A cell whose score cannot be computed (NaN, from malformed NaN telemetry in
	// a measured field) must not be admitted: NaN comparisons are non-transitive
	// and would make the ranking order-dependent, letting an unmeasurable/hostile
	// cell win by input order. Filter it so the decision stays reproducible.
	if math.IsNaN(cs.Score) {
		cs.Filtered = true
		cs.Reason = "filtered: uncomputable score (NaN in a measured field)"
		cs.Score = 0
	}
	return cs
}

func round(f float64) float64 { return math.Round(f*1e6) / 1e6 }
