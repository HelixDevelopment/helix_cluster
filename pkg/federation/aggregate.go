package federation

import (
	"context"
	"sort"
	"sync"
)

// CellClient is the injectable cross-cluster transport seam. The real
// implementation issues a network RPC to a single cell's endpoint; the test
// fake answers from memory. Query returns this cell's contribution to an
// aggregated result, or an error if the cell is unreachable / errored.
//
// HONEST EXTERNAL SEAM: the actual wire protocol (gRPC/HTTP/mTLS, retries,
// timeouts) lives behind this interface and is deliberately NOT implemented in
// this package, so the federation core stays stdlib-only and unit-testable.
type CellClient interface {
	// Query asks one cell, identified by its descriptor, to answer the given
	// request. Implementations must honor ctx cancellation.
	Query(ctx context.Context, cell Cell, request any) (CellResult, error)
}

// CellResult is one cell's contribution to an --all-cells aggregation. Items is
// the opaque payload (e.g. rows, nodes, jobs); the aggregator concatenates Items
// across cells into the merged view. CellID is stamped by the aggregator.
type CellResult struct {
	CellID string
	Items  []any
}

// CellError records that one cell failed during an aggregation. Partial failures
// are reported here rather than aborting the whole fan-out.
type CellError struct {
	CellID string
	Err    error
}

// Error implements the error interface for a single cell failure.
func (e CellError) Error() string {
	return "federation: cell " + e.CellID + ": " + e.Err.Error()
}

// Unwrap exposes the underlying cause for errors.Is/As.
func (e CellError) Unwrap() error { return e.Err }

// Aggregate is the merged result of an --all-cells query. Items is the flattened
// union of every successful cell's Items. PerCell maps cell id -> that cell's
// result. Errors lists the cells that failed. An aggregation with one failed
// cell still returns the other cells' Items AND a recorded CellError — partial
// failure never discards good data.
type Aggregate struct {
	Items   []any
	PerCell map[string]CellResult
	Errors  []CellError
}

// OK reports whether every queried cell answered without error.
func (a Aggregate) OK() bool { return len(a.Errors) == 0 }

// ErrorByCell returns the recorded error for a cell id, or nil if that cell
// succeeded (or was not queried).
func (a Aggregate) ErrorByCell(cellID string) error {
	for _, ce := range a.Errors {
		if ce.CellID == cellID {
			return ce.Err
		}
	}
	return nil
}

// Aggregator fans an --all-cells query out across the cells in a CellRegistry
// using a CellClient, and merges the per-cell answers into one Aggregate,
// tolerating partial failures.
type Aggregator struct {
	registry *CellRegistry
	client   CellClient
}

// NewAggregator builds an Aggregator over the given registry and transport
// client. Both must be non-nil; passing nil yields an Aggregator whose AllCells
// returns an empty Aggregate.
func NewAggregator(registry *CellRegistry, client CellClient) *Aggregator {
	return &Aggregator{registry: registry, client: client}
}

// AllCells queries every registered cell concurrently and merges the results.
//
// Contract (the anti-bluff core):
//   - Each cell is queried exactly once via the injected CellClient.
//   - A cell that returns an error is recorded as a CellError and DOES NOT abort
//     the fan-out: the other cells' data is still merged and returned.
//   - The merged Items is the concatenation of every successful cell's Items,
//     ordered by cell id for determinism.
//   - PerCell contains an entry for every cell that succeeded.
//
// The mutation "abort on first cell error" (e.g. returning early on err) would
// drop the surviving cells' Items and fail TestAllCells_PartialFailure.
func (a *Aggregator) AllCells(ctx context.Context, request any) Aggregate {
	out := Aggregate{PerCell: make(map[string]CellResult)}
	if a == nil || a.registry == nil || a.client == nil {
		return out
	}

	cells := a.registry.List() // sorted by id

	type outcome struct {
		cell Cell
		res  CellResult
		err  error
	}
	results := make([]outcome, len(cells))

	var wg sync.WaitGroup
	for i, cell := range cells {
		wg.Add(1)
		go func(i int, cell Cell) {
			defer wg.Done()
			res, err := a.client.Query(ctx, cell, request)
			results[i] = outcome{cell: cell, res: res, err: err}
		}(i, cell)
	}
	wg.Wait()

	// Merge deterministically in cell-id order. Crucially this loop does NOT
	// stop at the first error: it records the failure and continues, so the
	// survivors' data always lands in the aggregate.
	for _, oc := range results {
		if oc.err != nil {
			out.Errors = append(out.Errors, CellError{CellID: oc.cell.ID, Err: oc.err})
			continue
		}
		res := oc.res
		res.CellID = oc.cell.ID // stamp the source cell id authoritatively
		out.PerCell[oc.cell.ID] = res
		out.Items = append(out.Items, res.Items...)
	}

	// Errors are gathered in cell-id order already (results follows cells which
	// is sorted), but sort defensively to keep the contract explicit.
	sort.Slice(out.Errors, func(i, j int) bool { return out.Errors[i].CellID < out.Errors[j].CellID })
	return out
}
