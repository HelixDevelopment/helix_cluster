package discovery

// Adversarial concurrency + total-order + TTL-boundary probes for pkg/discovery.
//
// CONTEXT: discovery is a read-by-many / written-by-few registry. The recurring
// bug class this file hunts is an UNGUARDED SHARED MUTABLE MAP racing under
// concurrency (data race + lost update), plus nondeterministic total-order on
// equal entries and TTL-boundary off-by-one.
//
// Each registry type here DOCUMENTS concurrency safety:
//   - ServiceRegistry / InMemoryBackend : mutex-guarded local cache + backend map
//   - TierRegistry                       : "thread-safe, in-memory registry"
//   - TrustRegistry                      : layered on ServiceRegistry
//   - FederatedDiscovery                 : "All exported methods are safe for concurrent use"
//   - WeightedSelector                   : stateful, mutex-guarded `current` map
//
// These are therefore CONTRACT-PINNING tests: they assert SINK-SIDE conservation
// (registered count == N, no lost update) and deterministic ordering, and they
// are written so that REMOVING the guarding lock in prod would make them FAIL
// under `go test -race` (data race) and/or fail the count==N conservation check.
// Run with: go test -race -count=2 ./pkg/discovery/
//
// If any of these FAIL on unmodified prod, that is a REAL BUG (promised-but-broke
// concurrency safety), not a documented-unsafe disclaimer.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/tierdef"
)

// fixedClock is a deterministic Clock for TTL-boundary probing.
type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// ---------------------------------------------------------------------------
// (a) UNGUARDED SHARED MUTABLE STATE: ServiceRegistry concurrent Register/Lookup.
//     Conservation: register N distinct instances concurrently; ALL N must be
//     present afterwards (a race silently drops map writes -> count < N).
// ---------------------------------------------------------------------------

func TestServiceRegistry_ConcurrentRegister_Conservation(t *testing.T) {
	t.Parallel()
	const N = 2000
	reg := NewStaticRegistry()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			err := reg.Register(ctx, &Instance{
				ID:      fmt.Sprintf("node-%d", i),
				Service: "svc",
				Address: "10.0.0.1",
				Port:    8000 + i,
			})
			if err != nil {
				t.Errorf("Register(node-%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := reg.Lookup(ctx, "svc")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	// SINK-SIDE conservation: every concurrently-registered node must survive.
	if len(got) != N {
		t.Fatalf("lost update: registered %d distinct instances concurrently, "+
			"Lookup returned %d (a data race on the registry map silently dropped %d)",
			N, len(got), N-len(got))
	}
	seen := make(map[string]bool, len(got))
	for _, inst := range got {
		if seen[inst.ID] {
			t.Errorf("duplicate id in Lookup result: %s", inst.ID)
		}
		seen[inst.ID] = true
	}
	if len(seen) != N {
		t.Fatalf("conservation: expected %d unique ids, got %d", N, len(seen))
	}
}

// Concurrent Register + Deregister + Lookup churn: pure -race detector. The net
// count is nondeterministic (interleaving), so we only assert no race / no panic
// and that the result is internally consistent (no duplicate ids).
func TestServiceRegistry_ConcurrentChurn_Race(t *testing.T) {
	t.Parallel()
	reg := NewStaticRegistry()
	ctx := context.Background()
	const workers = 64
	const iters = 200

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("w%d-n%d", w, i%8)
				_ = reg.Register(ctx, &Instance{ID: id, Service: "svc", Port: 9000})
				_, _ = reg.Lookup(ctx, "svc")
				_ = reg.Deregister(ctx, "svc", id)
			}
		}(w)
	}
	wg.Wait()

	got, err := reg.Lookup(ctx, "svc")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	seen := make(map[string]bool, len(got))
	for _, inst := range got {
		if seen[inst.ID] {
			t.Errorf("duplicate id after churn: %s", inst.ID)
		}
		seen[inst.ID] = true
	}
}

// ---------------------------------------------------------------------------
// (a') TierRegistry: documented "thread-safe". Concurrent Register conservation.
// ---------------------------------------------------------------------------

func testTierdef(t *testing.T) *tierdef.Registry {
	t.Helper()
	reg, err := tierdef.Default()
	if err != nil {
		t.Fatalf("tierdef.Default: %v", err)
	}
	// Discover the canonical tier names from the registry itself so this test
	// is robust to the exact ladder contents. We need at least one valid tier.
	if len(reg.Tiers) == 0 {
		t.Skip("tierdef.Registry has no tiers; cannot exercise TierRegistry")
	}
	return reg
}

func TestTierRegistry_ConcurrentRegister_Conservation(t *testing.T) {
	t.Parallel()
	td := testTierdef(t)
	tier := td.Tiers[0].Tier // first valid tier name
	tr := NewTierRegistry(td)

	const N = 2000
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			if err := tr.Register(&Instance{ID: fmt.Sprintf("n-%d", i), Service: "svc"}, tier); err != nil {
				t.Errorf("Register(n-%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := tr.SelectExact(tier)
	if err != nil {
		t.Fatalf("SelectExact: %v", err)
	}
	if len(got) != N {
		t.Fatalf("lost update: registered %d instances concurrently into TierRegistry, "+
			"SelectExact returned %d (race dropped %d)", N, len(got), N-len(got))
	}
}

// Concurrent Register + Deregister + SelectByTier churn on TierRegistry: -race.
func TestTierRegistry_ConcurrentChurn_Race(t *testing.T) {
	t.Parallel()
	td := testTierdef(t)
	tier := td.Tiers[0].Tier
	tr := NewTierRegistry(td)

	const workers = 64
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("w%d-n%d", w, i%8)
				_ = tr.Register(&Instance{ID: id, Service: "svc"}, tier)
				_, _ = tr.SelectByTier(tier)
				tr.Deregister(id)
			}
		}(w)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// (b) TOTAL-ORDER / IDENTITY: equal tier rank -> SelectByTier must order by ID
//     deterministically. Run many times; the result order must be identical
//     and strictly ascending by ID (no nondeterministic map-iteration order).
// ---------------------------------------------------------------------------

func TestTierRegistry_SelectByTier_TotalOrderOnEqualRank(t *testing.T) {
	t.Parallel()
	td := testTierdef(t)
	tier := td.Tiers[0].Tier
	tr := NewTierRegistry(td)

	// All same tier (== equal rank) so the tie-break is purely ID ordering.
	ids := []string{"id-07", "id-03", "id-99", "id-01", "id-55", "id-22", "id-04"}
	for _, id := range ids {
		if err := tr.Register(&Instance{ID: id, Service: "svc"}, tier); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}

	var first []string
	for trial := 0; trial < 50; trial++ {
		got, err := tr.SelectByTier(tier)
		if err != nil {
			t.Fatalf("SelectByTier: %v", err)
		}
		order := make([]string, len(got))
		for i, inst := range got {
			order[i] = inst.ID
		}
		// strictly ascending by ID
		for i := 1; i < len(order); i++ {
			if order[i-1] >= order[i] {
				t.Fatalf("trial %d: not strictly ascending by ID at %d: %v", trial, i, order)
			}
		}
		if trial == 0 {
			first = order
			continue
		}
		if len(order) != len(first) {
			t.Fatalf("trial %d: length changed %d -> %d", trial, len(first), len(order))
		}
		for i := range order {
			if order[i] != first[i] {
				t.Fatalf("nondeterministic order: trial %d differs from trial 0:\n %v\n vs %v",
					trial, order, first)
			}
		}
	}
}

// Duplicate / equal-ID re-registration must CLOBBER (replace), not double-insert.
func TestTierRegistry_DuplicateID_Clobbers(t *testing.T) {
	t.Parallel()
	td := testTierdef(t)
	tier := td.Tiers[0].Tier
	tr := NewTierRegistry(td)

	const dups = 500
	var wg sync.WaitGroup
	wg.Add(dups)
	for i := 0; i < dups; i++ {
		go func(i int) {
			defer wg.Done()
			// Same ID every time -> last writer wins, count must stay 1.
			_ = tr.Register(&Instance{ID: "same", Service: "svc", Port: 1000 + i}, tier)
		}(i)
	}
	wg.Wait()

	got, err := tr.SelectExact(tier)
	if err != nil {
		t.Fatalf("SelectExact: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("duplicate id %q double-registered: expected exactly 1 entry, got %d", "same", len(got))
	}
	if got[0].ID != "same" {
		t.Fatalf("unexpected id: %q", got[0].ID)
	}
}

// ---------------------------------------------------------------------------
// (b') FederatedDiscovery: documented "All exported methods are safe for
//      concurrent use". Concurrent AddRemoteCell + RemoteCells + Lookup -race,
//      plus a conservation check that all added remote cells are visible.
// ---------------------------------------------------------------------------

func TestFederatedDiscovery_ConcurrentAddRemote_Conservation(t *testing.T) {
	t.Parallel()
	local := NewStaticRegistry()
	fd := NewFederatedDiscovery("local-cell", local)

	const N = 1000
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			if err := fd.AddRemoteCell(fmt.Sprintf("cell-%04d", i), NewStaticRegistry()); err != nil {
				t.Errorf("AddRemoteCell(cell-%04d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	cells := fd.RemoteCells()
	if len(cells) != N {
		t.Fatalf("lost update: added %d remote cells concurrently, RemoteCells returned %d "+
			"(race dropped %d)", N, len(cells), N-len(cells))
	}
	// RemoteCells must be sorted ascending (documented deterministic order).
	for i := 1; i < len(cells); i++ {
		if cells[i-1] >= cells[i] {
			t.Fatalf("RemoteCells not strictly ascending at %d: %q >= %q", i, cells[i-1], cells[i])
		}
	}
}

func TestFederatedDiscovery_ConcurrentAddRemoveLookup_Race(t *testing.T) {
	t.Parallel()
	local := NewStaticRegistry()
	ctx := context.Background()
	_ = local.Register(ctx, &Instance{ID: "l1", Service: "svc", Port: 1})
	fd := NewFederatedDiscovery("local-cell", local)

	const workers = 48
	const iters = 150
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				cid := fmt.Sprintf("w%d-c%d", w, i%6)
				_ = fd.AddRemoteCell(cid, NewStaticRegistry())
				_ = fd.RemoteCells()
				_, _ = fd.Lookup(ctx, "svc", false)
				fd.RemoveRemoteCell(cid)
			}
		}(w)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// (a'') WeightedSelector: stateful, mutex-guarded `current` map shared across
//       concurrent Pick calls. Pure -race detector under concurrent Pick.
// ---------------------------------------------------------------------------

func TestWeightedSelector_ConcurrentPick_Race(t *testing.T) {
	t.Parallel()
	sel := NewWeightedSelector()
	insts := make([]*Instance, 0, 16)
	for i := 0; i < 16; i++ {
		insts = append(insts, &Instance{ID: fmt.Sprintf("n-%02d", i), Weight: 1 + i%4})
	}

	const workers = 64
	const iters = 500
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if got := sel.Pick(insts); got == nil {
					t.Errorf("Pick returned nil for non-empty slice")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// (d) STALE / EXPIRY boundary. package.go expiry test is `now.Sub(LastSeen) > TTL`.
//     Therefore elapsed == TTL is NOT expired (kept); elapsed == TTL+1ns IS expired.
//     Pin both sides of that boundary via the injected Clock so a flip of `>` to
//     `>=` (or vice-versa) is caught sink-side.
// ---------------------------------------------------------------------------

func TestServiceRegistry_TTLBoundary_ExactlyAtTTLNotExpired(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	clk := &fixedClock{t: base}
	reg := NewStaticRegistry(WithClock(clk))
	ctx := context.Background()

	const ttl = 30 * time.Second
	if err := reg.Register(ctx, &Instance{
		ID: "n1", Service: "svc", Port: 1, LastSeen: base, TTL: ttl,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Advance to EXACTLY the TTL boundary: elapsed == TTL. Per `> TTL`, NOT expired.
	clk.set(base.Add(ttl))
	reg.SweepExpired()
	got, err := reg.Lookup(ctx, "svc")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("TTL boundary: instance at elapsed==TTL must be KEPT (expiry is strict `>`), "+
			"but it was evicted (got %d, want 1)", len(got))
	}

	// One nanosecond past TTL: now elapsed > TTL, MUST be expired/evicted.
	clk.set(base.Add(ttl + time.Nanosecond))
	reg.SweepExpired()
	got, err = reg.Lookup(ctx, "svc")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("TTL boundary: instance at elapsed==TTL+1ns must be EVICTED, but %d survived", len(got))
	}
}

// Select() applies the same `> TTL` eligibility filter (selection.go). Pin the
// exact boundary on the read path too.
func TestServiceRegistry_Select_TTLBoundary(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	clk := &fixedClock{t: base}
	reg := NewStaticRegistry(WithClock(clk))
	ctx := context.Background()

	const ttl = 30 * time.Second
	if err := reg.Register(ctx, &Instance{
		ID: "n1", Service: "svc", Port: 1, LastSeen: base, TTL: ttl,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Exactly at TTL: eligible.
	clk.set(base.Add(ttl))
	if pick, err := reg.Select(ctx, "svc"); err != nil || pick == nil {
		t.Fatalf("Select at elapsed==TTL: want eligible pick, got pick=%v err=%v", pick, err)
	}

	// Past TTL: ErrNoInstances.
	clk.set(base.Add(ttl + time.Nanosecond))
	if pick, err := reg.Select(ctx, "svc"); err == nil {
		t.Fatalf("Select at elapsed==TTL+1ns: want ErrNoInstances, got pick=%v err=nil", pick)
	}
}
