package discovery

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// newReg returns a *ServiceRegistry backed by an in-memory backend ready for
// use in tests (no TTL background ticker).
func newReg() *ServiceRegistry {
	return NewStaticRegistry()
}

// regWith registers the given instances into reg and returns reg.  Panics on
// error so tests stay concise.
func regWith(reg *ServiceRegistry, insts ...*Instance) *ServiceRegistry {
	ctx := context.Background()
	for _, inst := range insts {
		if err := reg.Register(ctx, inst); err != nil {
			panic("regWith: " + err.Error())
		}
	}
	return reg
}

// inst constructs a minimal healthy Instance suitable for registration.
func inst(id, service string) *Instance {
	return &Instance{
		ID:      id,
		Service: service,
		Healthy: true,
		Weight:  1,
	}
}

// idSet extracts the sorted set of instance IDs from a []FederatedInstance.
func idSet(fi []FederatedInstance) []string {
	ids := make([]string, len(fi))
	for i, f := range fi {
		ids[i] = f.Instance.ID
	}
	sort.Strings(ids)
	return ids
}

// cellsOf returns the ordered list of CellID values from fi (preserving the
// result order so determinism tests can assert the exact sequence).
func cellsOf(fi []FederatedInstance) []string {
	out := make([]string, len(fi))
	for i, f := range fi {
		out[i] = f.CellID
	}
	return out
}

// -----------------------------------------------------------------------------
// Test: AddRemoteCell rejects duplicates and cellID==local
// Mutation that makes it fail: remove the dup-guard block in AddRemoteCell.
// -----------------------------------------------------------------------------

func TestAddRemoteCell_RejectsDuplicateAndLocal(t *testing.T) {
	fd := NewFederatedDiscovery("cell-A", newReg())

	remoteReg := newReg()

	// First add must succeed.
	if err := fd.AddRemoteCell("cell-B", remoteReg); err != nil {
		t.Fatalf("first AddRemoteCell: unexpected error: %v", err)
	}

	// Adding the same cellID again must fail with ErrDuplicateCell.
	err := fd.AddRemoteCell("cell-B", newReg())
	if err == nil {
		t.Fatal("duplicate cellID: expected error, got nil")
	}
	if !errors.Is(err, ErrDuplicateCell) {
		t.Fatalf("duplicate cellID: want ErrDuplicateCell, got %v", err)
	}

	// Adding cellID == local must also fail with ErrDuplicateCell.
	err = fd.AddRemoteCell("cell-A", newReg())
	if err == nil {
		t.Fatal("cellID==local: expected error, got nil")
	}
	if !errors.Is(err, ErrDuplicateCell) {
		t.Fatalf("cellID==local: want ErrDuplicateCell, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Test: local-preferred short-circuit
//
// Setup: service "svc" exists in local (id="L1") AND in remote "cell-X"
// (id="X1").  Lookup(svc, preferLocal=true) must return ONLY L1 tagged with
// the local cell ID.
//
// Mutation: remove the `if preferLocal && len(localInsts) > 0` block in Lookup
// so it always aggregates → the result would include X1 → test fails.
// -----------------------------------------------------------------------------

func TestLookup_LocalPreferred_ShortCircuit(t *testing.T) {
	localReg := regWith(newReg(), inst("L1", "svc"))
	fd := NewFederatedDiscovery("cell-local", localReg)

	remoteReg := regWith(newReg(), inst("X1", "svc"))
	if err := fd.AddRemoteCell("cell-X", remoteReg); err != nil {
		t.Fatalf("AddRemoteCell: %v", err)
	}

	ctx := context.Background()
	result, err := fd.Lookup(ctx, "svc", true)
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}

	// Must contain EXACTLY the local instance.
	got := idSet(result)
	want := []string{"L1"}
	if !equalSlices(got, want) {
		t.Errorf("local-preferred: got IDs %v, want %v", got, want)
	}

	// All returned instances must be tagged with the local cell.
	for _, fi := range result {
		if fi.CellID != "cell-local" {
			t.Errorf("local-preferred: instance %q tagged cellID %q, want %q",
				fi.Instance.ID, fi.CellID, "cell-local")
		}
	}
}

// -----------------------------------------------------------------------------
// Test: local-preferred short-circuit is deterministic with multiple local
// instances.
//
// Setup: service "svc" exists in local (ids="L1","L2") AND in remote "cell-X"
// (id="X1").  Lookup(svc, preferLocal=true) called twice must return ONLY L1
// then L2 — sorted ascending — and NEVER X1.
//
// Mutation: remove the sortByID call on the preferLocal branch in Lookup →
// map-iteration order can swap L1/L2 across calls → the position assertions
// fail non-deterministically; the determinism re-run loop makes it reliable.
// -----------------------------------------------------------------------------

func TestLookup_LocalPreferred_ShortCircuit_MultipleInstances(t *testing.T) {
	// Register two local instances to expose any map-order non-determinism.
	localReg := regWith(newReg(), inst("L2", "svc"), inst("L1", "svc"))
	fd := NewFederatedDiscovery("cell-local", localReg)

	remoteReg := regWith(newReg(), inst("X1", "svc"))
	if err := fd.AddRemoteCell("cell-X", remoteReg); err != nil {
		t.Fatalf("AddRemoteCell: %v", err)
	}

	ctx := context.Background()

	// Call multiple times; if sortByID is missing on the short-circuit path
	// the order will differ across iterations when the backend map randomises.
	const runs = 20
	for run := 0; run < runs; run++ {
		result, err := fd.Lookup(ctx, "svc", true)
		if err != nil {
			t.Fatalf("run %d: Lookup: unexpected error: %v", run, err)
		}
		if len(result) != 2 {
			t.Fatalf("run %d: got %d results, want 2", run, len(result))
		}
		// Remote instance must never appear.
		for _, fi := range result {
			if fi.Instance.ID == "X1" {
				t.Errorf("run %d: remote instance X1 leaked into preferLocal result", run)
			}
			if fi.CellID != "cell-local" {
				t.Errorf("run %d: CellID=%q, want %q", run, fi.CellID, "cell-local")
			}
		}
		// L1 < L2 lexicographically — must be in this order every run.
		if result[0].Instance.ID != "L1" || result[1].Instance.ID != "L2" {
			t.Errorf("run %d: order = [%s, %s], want [L1, L2]",
				run, result[0].Instance.ID, result[1].Instance.ID)
		}
	}
}

// -----------------------------------------------------------------------------
// Test: fallback when service absent locally
//
// Setup: "svc" absent locally; present in remote "cell-Y" (id="Y1").
// Lookup(svc, preferLocal=true) must fall back and return Y1 tagged "cell-Y".
//
// Mechanically-killable mutation: on the `if preferLocal && len(localInsts) > 0`
// guard in Lookup, change `len(localInsts) > 0` to `len(localInsts) >= 0`
// (always true) → the short-circuit fires even when local is empty → the
// aggregate loop is skipped → result is empty → ErrServiceNotFound is returned
// → the `if err != nil` check below fires → test fails.
// -----------------------------------------------------------------------------

func TestLookup_LocalPreferred_Fallback(t *testing.T) {
	fd := NewFederatedDiscovery("cell-local", newReg()) // local has no "svc"

	remoteReg := regWith(newReg(), inst("Y1", "svc"))
	if err := fd.AddRemoteCell("cell-Y", remoteReg); err != nil {
		t.Fatalf("AddRemoteCell: %v", err)
	}

	ctx := context.Background()
	result, err := fd.Lookup(ctx, "svc", true)
	if err != nil {
		t.Fatalf("fallback Lookup: unexpected error: %v", err)
	}

	got := idSet(result)
	want := []string{"Y1"}
	if !equalSlices(got, want) {
		t.Errorf("fallback: got IDs %v, want %v", got, want)
	}

	if result[0].CellID != "cell-Y" {
		t.Errorf("fallback: CellID=%q, want %q", result[0].CellID, "cell-Y")
	}
}

// -----------------------------------------------------------------------------
// Test: aggregate tagging — same service in local + 2 remotes
//
// Lookup(svc, preferLocal=false) must return the union with correct per-cell
// origin tags in deterministic order: local first, then remotes in sorted
// cellID order.
//
// Mutation: hardcode CellID="" in wrapInstances → all tags become "" → the
// per-cell assertions fail.
// -----------------------------------------------------------------------------

func TestLookup_Aggregate_DeterministicOrderAndTags(t *testing.T) {
	localReg := regWith(newReg(), inst("L1", "svc"))
	fd := NewFederatedDiscovery("cell-A", localReg)

	reg1 := regWith(newReg(), inst("R1", "svc"))
	reg2 := regWith(newReg(), inst("R2", "svc"))

	// Add remotes out of alphabetical order to verify sort.
	if err := fd.AddRemoteCell("cell-C", reg2); err != nil {
		t.Fatalf("AddRemoteCell cell-C: %v", err)
	}
	if err := fd.AddRemoteCell("cell-B", reg1); err != nil {
		t.Fatalf("AddRemoteCell cell-B: %v", err)
	}

	ctx := context.Background()
	result, err := fd.Lookup(ctx, "svc", false)
	if err != nil {
		t.Fatalf("aggregate Lookup: unexpected error: %v", err)
	}

	// Expect exactly 3 results.
	if len(result) != 3 {
		t.Fatalf("aggregate: got %d results, want 3", len(result))
	}

	// Verify deterministic order: cell-A (L1), cell-B (R1), cell-C (R2).
	type want struct {
		cellID     string
		instanceID string
	}
	wantOrder := []want{
		{"cell-A", "L1"},
		{"cell-B", "R1"},
		{"cell-C", "R2"},
	}
	for i, w := range wantOrder {
		if result[i].CellID != w.cellID {
			t.Errorf("result[%d].CellID = %q, want %q", i, result[i].CellID, w.cellID)
		}
		if result[i].Instance.ID != w.instanceID {
			t.Errorf("result[%d].Instance.ID = %q, want %q", i, result[i].Instance.ID, w.instanceID)
		}
	}
}

// -----------------------------------------------------------------------------
// Test: RemoveRemoteCell drops the cell from aggregation
//
// Setup: local + remote "cell-R" each have one instance.
// After RemoveRemoteCell("cell-R"), aggregation must return ONLY the local
// instance.
//
// Mutation: make RemoveRemoteCell a no-op (comment out the delete) → the
// remote instance is still returned → count and ID assertions fail.
// -----------------------------------------------------------------------------

func TestRemoveRemoteCell_DropsFromAggregation(t *testing.T) {
	localReg := regWith(newReg(), inst("L1", "svc"))
	fd := NewFederatedDiscovery("cell-local", localReg)

	remoteReg := regWith(newReg(), inst("R1", "svc"))
	if err := fd.AddRemoteCell("cell-R", remoteReg); err != nil {
		t.Fatalf("AddRemoteCell: %v", err)
	}

	ctx := context.Background()

	// Before removal: both instances present.
	before, err := fd.Lookup(ctx, "svc", false)
	if err != nil {
		t.Fatalf("Lookup before remove: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("before remove: got %d results, want 2", len(before))
	}

	fd.RemoveRemoteCell("cell-R")

	// After removal: only local instance.
	after, err := fd.Lookup(ctx, "svc", false)
	if err != nil {
		t.Fatalf("Lookup after remove: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("after remove: got %d results, want 1", len(after))
	}
	if after[0].Instance.ID != "L1" {
		t.Errorf("after remove: instance ID = %q, want %q", after[0].Instance.ID, "L1")
	}
	// Verify "cell-R" is gone from RemoteCells.
	cells := fd.RemoteCells()
	for _, c := range cells {
		if c == "cell-R" {
			t.Errorf("RemoteCells still contains cell-R after remove: %v", cells)
		}
	}
}

// -----------------------------------------------------------------------------
// Test: RemoteCells returns sorted, deterministic list.
// Mutation: remove sort.Strings in RemoteCells → order becomes map-iteration
// order (non-deterministic) → the equality check with sorted wantCells fails.
// -----------------------------------------------------------------------------

func TestRemoteCells_SortedDeterministic(t *testing.T) {
	fd := NewFederatedDiscovery("cell-local", newReg())

	for _, id := range []string{"cell-Z", "cell-A", "cell-M"} {
		if err := fd.AddRemoteCell(id, newReg()); err != nil {
			t.Fatalf("AddRemoteCell %q: %v", id, err)
		}
	}

	got := fd.RemoteCells()
	want := []string{"cell-A", "cell-M", "cell-Z"}
	if !equalSlices(got, want) {
		t.Errorf("RemoteCells = %v, want %v", got, want)
	}
}

// -----------------------------------------------------------------------------
// Test: RegisterLocal delegates to local registry
// Mutation: make RegisterLocal return nil without registering → the subsequent
// Lookup returns ErrServiceNotFound → test fails.
// -----------------------------------------------------------------------------

func TestRegisterLocal_DelegatesCorrectly(t *testing.T) {
	fd := NewFederatedDiscovery("cell-local", newReg())

	ctx := context.Background()
	if err := fd.RegisterLocal(ctx, inst("L1", "svc")); err != nil {
		t.Fatalf("RegisterLocal: %v", err)
	}

	result, err := fd.Lookup(ctx, "svc", true)
	if err != nil {
		t.Fatalf("Lookup after RegisterLocal: %v", err)
	}
	if len(result) != 1 || result[0].Instance.ID != "L1" {
		t.Errorf("after RegisterLocal: got %v, want [{L1}]", idSet(result))
	}
}

// -----------------------------------------------------------------------------
// Test: Lookup returns ErrServiceNotFound when no cell carries the service.
// Mutation: return nil error when result is empty → this sentinel is not
// observed → the errors.Is check fails.
// -----------------------------------------------------------------------------

func TestLookup_ServiceNotFound(t *testing.T) {
	fd := NewFederatedDiscovery("cell-local", newReg())

	ctx := context.Background()
	_, err := fd.Lookup(ctx, "ghost-svc", false)
	if err == nil {
		t.Fatal("expected ErrServiceNotFound, got nil")
	}
	if !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("want ErrServiceNotFound, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Test: concurrent AddRemoteCell + Lookup proves the RWMutex is real.
//
// We run 50 goroutines adding remotes while 50 goroutines continuously perform
// Lookups.  The Go race detector (-race flag) will report a DATA RACE if the
// RWMutex is absent or incorrectly scoped.
//
// This test is deliberately NOT skipped under -short because its value is the
// race-detector result, not wall-clock time (it completes in < 100 ms).
// -----------------------------------------------------------------------------

func TestFederatedDiscovery_ConcurrentSafety(t *testing.T) {
	localReg := regWith(newReg(), inst("L1", "svc"))
	fd := NewFederatedDiscovery("cell-local", localReg)

	ctx := context.Background()
	var wg sync.WaitGroup

	const n = 50

	// Writers: add remote cells.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cellID := cellName(i)
			reg := regWith(newReg(), inst("R"+cellID, "svc"))
			// Ignore errors: duplicates can race.
			_ = fd.AddRemoteCell(cellID, reg)
		}(i)
	}

	// Readers: concurrent Lookup.
	// We capture and validate the results so the test proves correctness at the
	// sink side, not just absence of a race-detector report.
	// Mutation: remove the RWMutex from FederatedDiscovery → race detector fires
	// AND/OR the integrity assertions below trigger on corrupted results.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := fd.Lookup(ctx, "svc", false)
			// err must be nil (instances exist from startup) or ErrServiceNotFound
			// (theoretically impossible here but tolerated for generality).
			if err != nil && !errors.Is(err, ErrServiceNotFound) {
				t.Errorf("ConcurrentSafety: Lookup returned unexpected error: %v", err)
				return
			}
			// Every returned FederatedInstance must have a non-empty CellID and
			// a non-nil Instance pointer.  A data race or map corruption can
			// produce zero-value structs that violate this invariant.
			for idx, fi := range result {
				if fi.CellID == "" {
					t.Errorf("ConcurrentSafety: result[%d].CellID is empty", idx)
				}
				if fi.Instance == nil {
					t.Errorf("ConcurrentSafety: result[%d].Instance is nil", idx)
				}
			}
		}()
	}

	wg.Wait()
}

// -----------------------------------------------------------------------------
// Test: backend errors are propagated by Lookup.
//
// When the backend of the LOCAL cell returns an error, Lookup must return a
// non-nil error wrapping the backend failure.
// When the backend of a REMOTE cell returns an error, Lookup must also return
// a non-nil error.
//
// Mutation: remove the `return nil, fmt.Errorf(...)` on the localReg.Lookup
// error-return path (lines ~144-146 in federated.go) → err becomes nil →
// the `if err == nil` guard below fires → test fails.
// Mutation for remote: remove the `return nil, fmt.Errorf(...)` on the remote
// reg.Lookup error-return path (lines ~162-164 in federated.go) → same.
// -----------------------------------------------------------------------------

func TestLookup_PropagatesBackendError(t *testing.T) {
	ctx := context.Background()

	t.Run("local_backend_error", func(t *testing.T) {
		// Wire a ServiceRegistry whose backend always fails List.
		failReg := NewServiceRegistry(&errBackend{msg: "backend-failure"})
		fd := NewFederatedDiscovery("cell-fail", failReg)

		_, err := fd.Lookup(ctx, "svc", false)
		if err == nil {
			t.Fatal("expected error from failing local backend, got nil")
		}
		// The error message must include the backend failure text so callers
		// can diagnose which backend failed.
		if !containsStr(err.Error(), "backend-failure") {
			t.Errorf("error %q does not mention backend failure text", err.Error())
		}
	})

	t.Run("remote_backend_error", func(t *testing.T) {
		// Healthy local cell has NO instances for "svc" so the aggregate path
		// is followed and the remote backend error is reached.
		localReg := newReg() // empty — no instances registered
		fd := NewFederatedDiscovery("cell-local", localReg)

		failReg := NewServiceRegistry(&errBackend{msg: "remote-backend-failure"})
		if err := fd.AddRemoteCell("cell-remote", failReg); err != nil {
			t.Fatalf("AddRemoteCell: %v", err)
		}

		_, err := fd.Lookup(ctx, "svc", false)
		if err == nil {
			t.Fatal("expected error from failing remote backend, got nil")
		}
		if !containsStr(err.Error(), "remote-backend-failure") {
			t.Errorf("error %q does not mention remote backend failure text", err.Error())
		}
	})
}

// errBackend is a Backend whose List method always returns an error.  All other
// methods are no-ops that succeed, so a ServiceRegistry wrapping errBackend can
// be constructed without panic but will fail on every Lookup.
type errBackend struct {
	msg string
}

func (e *errBackend) Put(_ context.Context, _ string, _ *Instance) error { return nil }
func (e *errBackend) Delete(_ context.Context, _ string) error           { return nil }
func (e *errBackend) List(_ context.Context, _ string) ([]*Instance, error) {
	return nil, errors.New(e.msg)
}
func (e *errBackend) Watch(_ context.Context, _ string) (<-chan BackendEvent, error) {
	ch := make(chan BackendEvent)
	close(ch)
	return ch, nil
}

// containsStr reports whether substr appears in s (avoids importing strings in
// a package that otherwise does not need it).
func containsStr(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// cellName converts an int to a unique cell identifier string.
func cellName(i int) string {
	return "remote-" + itoa(i)
}

// itoa converts an integer to its decimal string representation without
// importing strconv (keeps the test file dependency-free).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// equalSlices returns true if a and b contain the same elements in the same
// order.
func equalSlices(a, b []string) bool {
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
