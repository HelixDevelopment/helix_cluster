package tieredcache

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// runID returns a random hex string to tag log lines so a failing run can be
// correlated across parallel test execution.
func runID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("run-id rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// ---- in-process fake tiers (not pretend services; plain map + mutex) --------

// fakeTier is a simple map-backed Tier used in tests.
// It uses the REAL sync.Mutex for concurrent safety and counts all Puts and
// Gets to provide observable sink-side evidence.
type fakeTier struct {
	mu      sync.Mutex
	data    map[string][]byte
	latency time.Duration
	absent  bool // when true, Get/Put return ErrCapabilityAbsent
	puts    int
	gets    int
}

func newFakeTier(latency time.Duration) *fakeTier {
	return &fakeTier{
		data:    make(map[string][]byte),
		latency: latency,
	}
}

func (f *fakeTier) Get(key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.absent {
		return nil, ErrCapabilityAbsent
	}
	v, ok := f.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	f.gets++
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (f *fakeTier) Put(key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.absent {
		return ErrCapabilityAbsent
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	f.data[key] = cp
	f.puts++
	return nil
}

func (f *fakeTier) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.absent {
		return ErrCapabilityAbsent
	}
	delete(f.data, key)
	return nil
}

func (f *fakeTier) AccessLatency() time.Duration { return f.latency }

func (f *fakeTier) GetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

func (f *fakeTier) PutCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

func (f *fakeTier) Has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[key]
	return ok
}

// ---- injected clock ---------------------------------------------------------

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock(base time.Time) *testClock { return &testClock{now: base} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ---- helpers ----------------------------------------------------------------

func newTestCache(t *testing.T, clk *testClock, idle time.Duration) (*Cache, *fakeTier, *fakeTier) {
	t.Helper()
	warm := newFakeTier(500 * time.Microsecond)
	cold := newFakeTier(2 * time.Millisecond)
	c, err := New(Config{
		Warm:          warm,
		Cold:          cold,
		IdleThreshold: idle,
		Now:           clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, warm, cold
}

// ============================================================================
// TestHotKeysServedFromMemory
//
// Asserts that a frequently-accessed key stays in the Hot tier and is served
// with Hot latency (the injected clock never advances past the idle threshold,
// so RunMaintenance must not demote the key).
//
// Mutation guard pinned: the rec.lastAccess.Before(deadline) comparison in
// RunMaintenance. If it were inverted, a "recently accessed" key would be
// treated as idle and evicted from Hot, causing the Hot-hit assertions below
// to FAIL.
// ============================================================================
func TestHotKeysServedFromMemory(t *testing.T) {
	id := runID(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := newClock(base)
	idle := 5 * time.Second

	c, warm, _ := newTestCache(t, clk, idle)

	key := "frequentKey"
	payload := []byte("payload-hot")

	if err := c.Put(key, payload); err != nil {
		t.Fatalf("run=%s Put: %v", id, err)
	}
	t.Logf("run=%s put key=%q at t=0", id, key)

	const accesses = 10
	for i := 0; i < accesses; i++ {
		// Advance clock only 100ms each access — well within 5 s idle threshold.
		clk.Advance(100 * time.Millisecond)
		val, tier, err := c.Get(key)
		if err != nil {
			t.Fatalf("run=%s Get[%d]: %v", id, i, err)
		}
		if tier != TierHot {
			t.Fatalf("run=%s Get[%d]: served from tier=%q, want %q", id, i, tier, TierHot)
		}
		if string(val) != string(payload) {
			t.Fatalf("run=%s Get[%d]: value=%q, want %q", id, i, val, payload)
		}
		t.Logf("run=%s access[%d] tier=%s val=%q", id, i, tier, val)
	}

	// Run maintenance: total elapsed = 10 * 100 ms = 1 s < 5 s threshold.
	// The key must NOT be demoted.
	c.RunMaintenance()

	if got := c.HotCount(); got != 1 {
		t.Fatalf("run=%s after maintenance HotCount=%d, want 1 (key must still be Hot)", id, got)
	}

	// Warm.Gets should be 0 because the key was served from Hot every time.
	if wg := warm.GetCount(); wg != 0 {
		t.Fatalf("run=%s warm.Gets=%d, want 0 (key must have been served from Hot)", id, wg)
	}

	// Hot hit counter must equal the number of accesses.
	stats := c.Stats()
	if stats.HotHits != accesses {
		t.Fatalf("run=%s HotHits=%d, want %d", id, stats.HotHits, accesses)
	}
	if stats.WarmHits != 0 {
		t.Fatalf("run=%s WarmHits=%d, want 0", id, stats.WarmHits)
	}
	if stats.ColdHits != 0 {
		t.Fatalf("run=%s ColdHits=%d, want 0", id, stats.ColdHits)
	}

	t.Logf("run=%s PASS: HotHits=%d WarmHits=%d ColdHits=%d",
		id, stats.HotHits, stats.WarmHits, stats.ColdHits)
}

// ============================================================================
// TestIdleKeyDemotesThroughTiers
//
// Asserts that a key left idle past the threshold demotes Hot->Warm on the
// first maintenance pass and Warm->Cold on the second, then can be retrieved
// from Cold (counting a ColdHit) and from Warm (counting a WarmHit).
//
// Mutation guard (MUST make this test FAIL if mutated):
//
//	rec.lastAccess.Before(deadline)   in RunMaintenance
//
// If this guard is inverted (Before -> After, or threshold negated), an idle
// key will NEVER be demoted: c.HotCount() will remain 1 after the first
// maintenance pass, and the assertions on WarmHits / ColdHits will FAIL.
// ============================================================================
func TestIdleKeyDemotesThroughTiers(t *testing.T) {
	id := runID(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := newClock(base)
	idle := 30 * time.Second

	c, warm, cold := newTestCache(t, clk, idle)

	key := "idleKey"
	payload := []byte("payload-idle")

	if err := c.Put(key, payload); err != nil {
		t.Fatalf("run=%s Put: %v", id, err)
	}
	t.Logf("run=%s put key=%q at t=0", id, key)

	// Confirm it is initially in Hot.
	if got := c.HotCount(); got != 1 {
		t.Fatalf("run=%s initial HotCount=%d, want 1", id, got)
	}

	// --- First maintenance pass: advance clock past idle threshold. -----------
	clk.Advance(31 * time.Second) // now = t+31s > idle threshold of 30s
	t.Logf("run=%s clock advanced to t+31s (idle threshold %s)", id, idle)

	c.RunMaintenance()

	// After first maintenance pass the key should have been demoted from Hot.
	if got := c.HotCount(); got != 0 {
		t.Fatalf("run=%s after 1st maintenance HotCount=%d, want 0; mutation guard violated", id, got)
	}
	// Warm must now contain the key (Hot value was written to Warm).
	if !warm.Has(key) {
		t.Fatalf("run=%s after 1st maintenance key not found in Warm", id)
	}
	t.Logf("run=%s after 1st maintenance: Hot=0, Warm has key", id)

	// --- Second maintenance pass: advance clock again. -----------------------
	// Put the key's lastAccess back in time: its entry in Warm was written when
	// it was evicted from Hot, but there is no lastAccess in Warm. We need to
	// simulate that the Warm entry was also idle long enough.
	// The demotion loop checks Hot keys only; so to demote Warm->Cold we need
	// to GET the key (which re-promotes to Hot with the CURRENT clock time), then
	// advance the clock past idle, and run maintenance again.
	//
	// Step 1: Get from Warm (promotes to Hot, records new lastAccess).
	clk.Advance(1 * time.Second) // t+32s
	val, tier, err := c.Get(key)
	if err != nil {
		t.Fatalf("run=%s Get after 1st demotion: %v", id, err)
	}
	if tier != TierWarm {
		t.Fatalf("run=%s Get after 1st demotion: tier=%q, want %q", id, tier, TierWarm)
	}
	if string(val) != string(payload) {
		t.Fatalf("run=%s Get after 1st demotion: val=%q, want %q", id, val, payload)
	}
	t.Logf("run=%s WarmHit confirmed at t+32s, val=%q", id, val)

	// WarmHit counter must now be 1.
	stats := c.Stats()
	if stats.WarmHits != 1 {
		t.Fatalf("run=%s WarmHits=%d after warm get, want 1", id, stats.WarmHits)
	}

	// Step 2: Advance clock past idle threshold (key was promoted to Hot at t+32s).
	clk.Advance(31 * time.Second) // t+63s; last access was t+32s so 31s idle > 30s threshold
	t.Logf("run=%s clock advanced to t+63s; key has been idle since t+32s", id)

	c.RunMaintenance() // demotes Hot->Warm; and since Warm already has the key it pushes to Cold

	if got := c.HotCount(); got != 0 {
		t.Fatalf("run=%s after 2nd maintenance HotCount=%d, want 0", id, got)
	}
	if !cold.Has(key) {
		t.Fatalf("run=%s after 2nd maintenance key not found in Cold", id)
	}
	t.Logf("run=%s after 2nd maintenance: Hot=0, Cold has key", id)

	// Step 3: Get from Cold (should count a ColdHit and promote to Hot).
	clk.Advance(1 * time.Second)
	val, tier, err = c.Get(key)
	if err != nil {
		t.Fatalf("run=%s Get after 2nd demotion: %v", id, err)
	}
	if tier != TierCold {
		t.Fatalf("run=%s Get after 2nd demotion: tier=%q, want %q", id, tier, TierCold)
	}
	if string(val) != string(payload) {
		t.Fatalf("run=%s Get after 2nd demotion: val=%q, want %q", id, val, payload)
	}
	t.Logf("run=%s ColdHit confirmed, val=%q", id, val)

	// Verify key is promoted back to Hot.
	if got := c.HotCount(); got != 1 {
		t.Fatalf("run=%s after cold promotion HotCount=%d, want 1", id, got)
	}

	// Verify per-tier hit counters: 0 HotHits, 1 WarmHit, 1 ColdHit.
	stats = c.Stats()
	if stats.HotHits != 0 {
		t.Fatalf("run=%s HotHits=%d, want 0", id, stats.HotHits)
	}
	if stats.WarmHits != 1 {
		t.Fatalf("run=%s WarmHits=%d, want 1", id, stats.WarmHits)
	}
	if stats.ColdHits != 1 {
		t.Fatalf("run=%s ColdHits=%d, want 1", id, stats.ColdHits)
	}

	t.Logf("run=%s PASS: HotHits=%d WarmHits=%d ColdHits=%d",
		id, stats.HotHits, stats.WarmHits, stats.ColdHits)
}

// ============================================================================
// TestLatencyOrderEnforced
// Verifies that New rejects configurations where AccessLatency is not strictly
// increasing Hot (0) < Warm < Cold.
// ============================================================================
func TestLatencyOrderEnforced(t *testing.T) {
	id := runID(t)
	clk := newClock(time.Now())
	idle := 10 * time.Second

	cases := []struct {
		name        string
		warmLatency time.Duration
		coldLatency time.Duration
		wantErr     bool
	}{
		{"valid", 500 * time.Microsecond, 2 * time.Millisecond, false},
		{"warm_zero", 0, 2 * time.Millisecond, true},
		{"cold_equal_warm", 500 * time.Microsecond, 500 * time.Microsecond, true},
		{"cold_less_than_warm", 2 * time.Millisecond, 500 * time.Microsecond, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			warm := newFakeTier(tc.warmLatency)
			cold := newFakeTier(tc.coldLatency)
			_, err := New(Config{
				Warm:          warm,
				Cold:          cold,
				IdleThreshold: idle,
				Now:           clk.Now,
			})
			if tc.wantErr && err == nil {
				t.Fatalf("run=%s case=%s: expected error for invalid latency order, got nil", id, tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("run=%s case=%s: unexpected error: %v", id, tc.name, err)
			}
			t.Logf("run=%s case=%s warm=%s cold=%s err=%v", id, tc.name, tc.warmLatency, tc.coldLatency, err)
		})
	}
}

// ============================================================================
// TestCapabilityAbsentError
// Verifies that when a Tier.Get returns ErrCapabilityAbsent the Cache.Get
// propagates it rather than treating it as ErrNotFound.
// ============================================================================
func TestCapabilityAbsentError(t *testing.T) {
	id := runID(t)
	clk := newClock(time.Now())
	warm := newFakeTier(500 * time.Microsecond)
	cold := newFakeTier(2 * time.Millisecond)
	warm.absent = true

	c, err := New(Config{
		Warm:          warm,
		Cold:          cold,
		IdleThreshold: 10 * time.Second,
		Now:           clk.Now,
	})
	if err != nil {
		t.Fatalf("run=%s New: %v", id, err)
	}

	// Put via warm will fail; we manually inject data into cold to test
	// that Warm absent error propagates before Cold is tried.
	// Simulate: key not in hot, warm.Get returns ErrCapabilityAbsent.
	_, _, err = c.Get("missing")
	if !errors.Is(err, ErrCapabilityAbsent) {
		t.Fatalf("run=%s expected ErrCapabilityAbsent propagation, got %v", id, err)
	}
	t.Logf("run=%s ErrCapabilityAbsent propagated correctly: %v", id, err)
}

// ============================================================================
// TestPromotionFromColdResetsTierHierarchy
// A cold-hit must promote the key to Hot so the NEXT Get is a Hot hit.
// ============================================================================
func TestPromotionFromColdResetsTierHierarchy(t *testing.T) {
	id := runID(t)
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	clk := newClock(base)
	idle := 10 * time.Second

	c, warm, cold := newTestCache(t, clk, idle)

	key := "promoKey"
	payload := []byte("promo-payload")

	// Inject directly into Cold (bypassing the cache's Put) to simulate a key
	// that was never in Hot/Warm and lives only in Cold.
	if err := cold.Put(key, payload); err != nil {
		t.Fatalf("run=%s cold.Put: %v", id, err)
	}
	// Warm must NOT have the key (confirm it reaches Cold).
	if warm.Has(key) {
		t.Fatalf("run=%s warm unexpectedly has key before test", id)
	}

	// First Get: should be a Cold hit.
	val, tier, err := c.Get(key)
	if err != nil {
		t.Fatalf("run=%s 1st Get: %v", id, err)
	}
	if tier != TierCold {
		t.Fatalf("run=%s 1st Get tier=%q, want cold", id, tier)
	}
	if string(val) != string(payload) {
		t.Fatalf("run=%s 1st Get val=%q, want %q", id, val, payload)
	}
	t.Logf("run=%s 1st Get: ColdHit val=%q", id, val)

	// Second Get (within idle threshold): should be a Hot hit now.
	clk.Advance(1 * time.Second)
	val2, tier2, err := c.Get(key)
	if err != nil {
		t.Fatalf("run=%s 2nd Get: %v", id, err)
	}
	if tier2 != TierHot {
		t.Fatalf("run=%s 2nd Get tier=%q, want hot (promoted after cold hit)", id, tier2)
	}
	if string(val2) != string(payload) {
		t.Fatalf("run=%s 2nd Get val=%q, want %q", id, val2, payload)
	}

	stats := c.Stats()
	if stats.ColdHits != 1 {
		t.Fatalf("run=%s ColdHits=%d, want 1", id, stats.ColdHits)
	}
	if stats.HotHits != 1 {
		t.Fatalf("run=%s HotHits=%d, want 1 (post-promotion)", id, stats.HotHits)
	}
	t.Logf("run=%s PASS: ColdHits=%d HotHits=%d", id, stats.ColdHits, stats.HotHits)
}

// ============================================================================
// TestStatsAccumulate
// Verifies that HotHits, WarmHits, and ColdHits accumulate correctly across
// multiple keys and multiple Gets.
// ============================================================================
func TestStatsAccumulate(t *testing.T) {
	id := runID(t)
	clk := newClock(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	c, _, cold := newTestCache(t, clk, 60*time.Second)

	// Put three keys → they all start Hot.
	for i := 0; i < 3; i++ {
		k := fmt.Sprintf("k%d", i)
		if err := c.Put(k, []byte(k)); err != nil {
			t.Fatalf("run=%s Put %s: %v", id, k, err)
		}
	}

	// Access k0 twice (Hot hits).
	for j := 0; j < 2; j++ {
		_, tier, err := c.Get("k0")
		if err != nil {
			t.Fatalf("run=%s Get k0[%d]: %v", id, j, err)
		}
		if tier != TierHot {
			t.Fatalf("run=%s k0[%d] tier=%q, want hot", id, j, tier)
		}
	}

	// Inject k3 directly into cold (never in Hot or Warm).
	if err := cold.Put("k3", []byte("k3")); err != nil {
		t.Fatalf("run=%s cold.Put k3: %v", id, err)
	}
	_, tier, err := c.Get("k3")
	if err != nil {
		t.Fatalf("run=%s Get k3: %v", id, err)
	}
	if tier != TierCold {
		t.Fatalf("run=%s k3 tier=%q, want cold", id, tier)
	}

	stats := c.Stats()
	t.Logf("run=%s stats: HotHits=%d WarmHits=%d ColdHits=%d",
		id, stats.HotHits, stats.WarmHits, stats.ColdHits)

	if stats.HotHits != 2 {
		t.Fatalf("run=%s HotHits=%d, want 2", id, stats.HotHits)
	}
	if stats.WarmHits != 0 {
		t.Fatalf("run=%s WarmHits=%d, want 0", id, stats.WarmHits)
	}
	if stats.ColdHits != 1 {
		t.Fatalf("run=%s ColdHits=%d, want 1", id, stats.ColdHits)
	}
}
