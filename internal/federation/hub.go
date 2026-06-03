package federation

import (
	"sort"
	"sync"
	"time"
)

// WorkStatus mirrors the per-cluster status Karmada reports for a propagated
// workload (via the Work / ResourceBinding objects). It records how many
// replicas are scheduled/ready in a given cell.
type WorkStatus struct {
	// Cluster is the cell name this status came from.
	Cluster string
	// Healthy is true when the cell's gateway reported the workload Healthy.
	Healthy bool
	// ReadyReplicas / DesiredReplicas mirror the aggregated status fields.
	ReadyReplicas   int
	DesiredReplicas int
	// LastTransition is when the cell last changed state.
	LastTransition time.Time
}

// AggregatedStatus is the federation-wide view of a workload, summed across all
// cells. It answers "is my workload Running somewhere, and how many replicas
// are up across the whole federation".
type AggregatedStatus struct {
	// Workload is the name of the propagated workload.
	Workload string
	// PerCluster is the per-cell status, sorted by cluster name for
	// determinism.
	PerCluster []WorkStatus
	// TotalReady / TotalDesired are the federation-wide replica sums.
	TotalReady   int
	TotalDesired int
	// ReadyClusters is the count of cells reporting at least one ready replica.
	ReadyClusters int
	// FullyScheduled is true when TotalReady >= TotalDesired and at least one
	// cluster is ready.
	FullyScheduled bool
}

// Aggregate folds a set of per-cell WorkStatus into a federation-wide summary.
// It is the model of Karmada's status aggregation controller.
func Aggregate(workload string, statuses []WorkStatus) AggregatedStatus {
	agg := AggregatedStatus{Workload: workload}
	agg.PerCluster = append(agg.PerCluster, statuses...)
	sort.SliceStable(agg.PerCluster, func(i, j int) bool {
		return agg.PerCluster[i].Cluster < agg.PerCluster[j].Cluster
	})
	for _, s := range agg.PerCluster {
		agg.TotalReady += s.ReadyReplicas
		agg.TotalDesired += s.DesiredReplicas
		if s.Healthy && s.ReadyReplicas > 0 {
			agg.ReadyClusters++
		}
	}
	agg.FullyScheduled = agg.ReadyClusters > 0 && agg.TotalReady >= agg.TotalDesired
	return agg
}

// FakeHub is an in-process model of a multi-cell Karmada hub used to drive
// deterministic failover tests without live infra. It owns the mutable cell
// liveness and per-cell workload status, and exposes the operations the
// closure-criteria scenario needs: place a workload, kill a cell's gateway,
// and read back aggregated status.
//
// It is intentionally simple and deterministic so a test can assert exactly
// what the global selector + status aggregation produce.
type FakeHub struct {
	mu sync.Mutex
	// cells maps cell name -> Cell (with mutable Health).
	cells map[string]*Cell
	// placement maps workload -> cell name currently hosting it.
	placement map[string]string
	// status maps workload -> per-cell WorkStatus.
	status map[string]map[string]*WorkStatus
	// desired maps workload -> desired replicas.
	desired map[string]int
}

// NewFakeHub builds a hub seeded with the supplied cells.
func NewFakeHub(cells []Cell) *FakeHub {
	h := &FakeHub{
		cells:     make(map[string]*Cell, len(cells)),
		placement: map[string]string{},
		status:    map[string]map[string]*WorkStatus{},
		desired:   map[string]int{},
	}
	for i := range cells {
		c := cells[i]
		if c.Health == "" {
			c.Health = CellReady
		}
		h.cells[c.Name] = &c
	}
	return h
}

// Cells returns a snapshot of the current cells (with live health), sorted by
// name, suitable to feed straight into Selector.Select/Reselect.
func (h *FakeHub) Cells() []Cell {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Cell, 0, len(h.cells))
	for _, c := range h.cells {
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Place records that workload is hosted on cell with the given desired
// replicas, and marks every replica ready on that cell (the happy path of a
// successful deployment). It returns the WorkStatus snapshot.
func (h *FakeHub) Place(workload, cell string, replicas int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.placement[workload] = cell
	h.desired[workload] = replicas
	if h.status[workload] == nil {
		h.status[workload] = map[string]*WorkStatus{}
	}
	// Karmada moves the ResourceBinding when a workload is rescheduled; the
	// Work objects on cells that no longer host it are garbage-collected. Drop
	// stale per-cell status so the aggregated DesiredReplicas reflects only the
	// current placement (otherwise a dead cell's desired count lingers).
	for other := range h.status[workload] {
		if other != cell {
			delete(h.status[workload], other)
		}
	}
	// A cell that hosts the workload reports it healthy with all replicas ready.
	now := time.Now()
	healthy := true
	if c, ok := h.cells[cell]; ok && c.Health == CellNotReady {
		healthy = false
	}
	ready := replicas
	if !healthy {
		ready = 0
	}
	h.status[workload][cell] = &WorkStatus{
		Cluster:         cell,
		Healthy:         healthy,
		ReadyReplicas:   ready,
		DesiredReplicas: replicas,
		LastTransition:  now,
	}
}

// KillCell marks a cell's gateway down (NotReady) and zeroes the ready
// replicas of any workload it was hosting, modeling a gateway/cell failure.
func (h *FakeHub) KillCell(cell string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.cells[cell]; ok {
		c.Health = CellNotReady
	}
	for _, byCell := range h.status {
		if ws, ok := byCell[cell]; ok {
			ws.Healthy = false
			ws.ReadyReplicas = 0
			ws.LastTransition = time.Now()
		}
	}
}

// HostingCell returns the cell currently recording the workload as placed.
func (h *FakeHub) HostingCell(workload string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.placement[workload]
}

// Status returns the aggregated federation status for a workload.
func (h *FakeHub) Status(workload string) AggregatedStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	var list []WorkStatus
	for _, ws := range h.status[workload] {
		list = append(list, *ws)
	}
	return Aggregate(workload, list)
}
