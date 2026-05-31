package federation

import (
	"context"
	"errors"
	"sort"
	"testing"
)

// fakeCellClient is the software reference transport for tests. It answers each
// cell from an in-memory table: items[cellID] is that cell's payload, and
// failErr[cellID], if set, makes that cell error (simulating a down cell).
type fakeCellClient struct {
	items   map[string][]any
	failErr map[string]error
}

func (f *fakeCellClient) Query(ctx context.Context, cell Cell, _ any) (CellResult, error) {
	if err := f.failErr[cell.ID]; err != nil {
		return CellResult{}, err
	}
	return CellResult{CellID: cell.ID, Items: f.items[cell.ID]}, nil
}

func setupThreeCells(t *testing.T) *CellRegistry {
	t.Helper()
	r := NewCellRegistry()
	for _, id := range []string{"east", "north", "west"} {
		c := mustCell(t, id)
		_ = r.Register(c)
		_ = r.Advance(id, PhaseActive)
	}
	return r
}

// asStrings flattens an []any of strings for stable comparison.
func asStrings(items []any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.(string))
	}
	sort.Strings(out)
	return out
}

// TestAllCells_MergesThreeCells proves the aggregator concatenates every cell's
// items into one merged view, with per-cell breakdown, when all 3 succeed.
//
// MUTATION that kills this: in AllCells, replace `out.Items = append(out.Items,
// res.Items...)` with a no-op. The merged item set is empty and the
// want={e1,n1,n2,w1} assertion fails.
func TestAllCells_MergesThreeCells(t *testing.T) {
	r := setupThreeCells(t)
	client := &fakeCellClient{
		items: map[string][]any{
			"east":  {"e1"},
			"north": {"n1", "n2"},
			"west":  {"w1"},
		},
	}
	agg := NewAggregator(r, client).AllCells(context.Background(), nil)

	if !agg.OK() {
		t.Fatalf("expected OK aggregate, got errors %v", agg.Errors)
	}
	gotItems := asStrings(agg.Items)
	want := []string{"e1", "n1", "n2", "w1"}
	if !equalStrings(gotItems, want) {
		t.Fatalf("merged items = %v, want %v", gotItems, want)
	}
	if len(agg.PerCell) != 3 {
		t.Fatalf("PerCell len = %d, want 3", len(agg.PerCell))
	}
	if got := asStrings(agg.PerCell["north"].Items); !equalStrings(got, []string{"n1", "n2"}) {
		t.Fatalf("PerCell[north] = %v, want [n1 n2]", got)
	}
	if agg.PerCell["east"].CellID != "east" {
		t.Fatalf("PerCell[east].CellID = %q, want east", agg.PerCell["east"].CellID)
	}
}

// TestAllCells_PartialFailure is the central anti-bluff test: with 3 cells where
// ONE (north) errors, the aggregate MUST still contain the other two cells'
// data AND record a per-cell error for north.
//
// MUTATION that kills this: make AllCells abort on the first cell error, e.g.
//
//	if oc.err != nil { return out }   // returns early, dropping survivors
//
// Then survivors' items are lost: gotItems != {e1,w1} (it would be empty or
// partial depending on order) AND/OR the survivors-count assertion fails. The
// test's two-sided assertion (merged data PLUS recorded error) pins exactly the
// "partial failure still returns others" contract.
func TestAllCells_PartialFailure(t *testing.T) {
	r := setupThreeCells(t)
	boom := errors.New("north unreachable")
	client := &fakeCellClient{
		items: map[string][]any{
			"east":  {"e1"},
			"north": {"n1", "n2"}, // present but will be masked by failErr
			"west":  {"w1"},
		},
		failErr: map[string]error{"north": boom},
	}

	agg := NewAggregator(r, client).AllCells(context.Background(), nil)

	// (1) Survivors' data is present: east + west, NOT north's.
	gotItems := asStrings(agg.Items)
	want := []string{"e1", "w1"}
	if !equalStrings(gotItems, want) {
		t.Fatalf("partial-failure merged items = %v, want survivors %v", gotItems, want)
	}
	if _, ok := agg.PerCell["north"]; ok {
		t.Fatalf("PerCell must not contain failed cell north, got %+v", agg.PerCell["north"])
	}
	if len(agg.PerCell) != 2 {
		t.Fatalf("PerCell len = %d, want 2 survivors", len(agg.PerCell))
	}

	// (2) The failure is recorded as a per-cell error, not swallowed.
	if agg.OK() {
		t.Fatalf("aggregate reports OK despite a failed cell")
	}
	if len(agg.Errors) != 1 {
		t.Fatalf("Errors len = %d, want exactly 1", len(agg.Errors))
	}
	ce := agg.Errors[0]
	if ce.CellID != "north" {
		t.Fatalf("recorded error cell = %q, want north", ce.CellID)
	}
	if !errors.Is(ce, boom) {
		t.Fatalf("recorded error = %v, want to wrap boom", ce.Err)
	}
	if got := agg.ErrorByCell("north"); !errors.Is(got, boom) {
		t.Fatalf("ErrorByCell(north) = %v, want boom", got)
	}
	if got := agg.ErrorByCell("east"); got != nil {
		t.Fatalf("ErrorByCell(east) = %v, want nil (it succeeded)", got)
	}
}

// TestAllCells_AllFail proves that when every cell errors, the aggregate has no
// items but records one error per cell (nothing fabricated, nothing dropped).
//
// MUTATION that kills this: in AllCells, `continue` -> `break` on error. Only
// the first error is recorded; len(Errors)==3 assertion fails.
func TestAllCells_AllFail(t *testing.T) {
	r := setupThreeCells(t)
	client := &fakeCellClient{
		failErr: map[string]error{
			"east":  errors.New("e down"),
			"north": errors.New("n down"),
			"west":  errors.New("w down"),
		},
	}
	agg := NewAggregator(r, client).AllCells(context.Background(), nil)
	if len(agg.Items) != 0 {
		t.Fatalf("items = %v, want none", agg.Items)
	}
	if len(agg.Errors) != 3 {
		t.Fatalf("Errors len = %d, want 3", len(agg.Errors))
	}
	// Errors sorted by cell id.
	if agg.Errors[0].CellID != "east" || agg.Errors[1].CellID != "north" || agg.Errors[2].CellID != "west" {
		t.Fatalf("error order = %s,%s,%s, want east,north,west",
			agg.Errors[0].CellID, agg.Errors[1].CellID, agg.Errors[2].CellID)
	}
}

// TestAllCells_EmptyRegistry returns an empty-but-valid aggregate.
//
// MUTATION that kills this: have AllCells return a zero Aggregate{} (nil
// PerCell). The PerCell non-nil / OK() assertion fails.
func TestAllCells_EmptyRegistry(t *testing.T) {
	r := NewCellRegistry()
	agg := NewAggregator(r, &fakeCellClient{}).AllCells(context.Background(), nil)
	if !agg.OK() {
		t.Fatalf("empty aggregate should be OK")
	}
	if agg.PerCell == nil {
		t.Fatalf("PerCell must be non-nil")
	}
	if len(agg.Items) != 0 {
		t.Fatalf("items = %v, want none", agg.Items)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
