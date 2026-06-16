package discovery

// Adversarial SECOND-PASS probes for pkg/discovery.
//
// This file is a deeper pass over discovery correctness, targeting bugs the
// first adversarial file (discovery_adversarial_test.go) does not exercise:
//
//   1. Lookup TTL-staleness: Lookup/HealthyInstances filter only on Healthy,
//      NOT on TTL. An instance whose lease has elapsed but has not yet been
//      swept (no Start()/SweepExpired() in between) is still returned as a
//      live, healthy node -> traffic routed to a dead node.
//   2. Pointer/map aliasing: Register aliases the caller's Metadata map into the
//      store; the map a later Lookup returns is the SAME map object, so a caller
//      mutation corrupts the registry (and every other reader).
//
// Probes are written to assert the CORRECT (isolated/non-stale) behavior so that
// a flip back to the buggy behavior fails sink-side.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (1) Lookup must not return TTL-expired instances as healthy.
//
// Register an instance with a short TTL, advance the injected clock PAST the
// TTL, and call Lookup WITHOUT running a sweep. A correct registry must not
// hand a caller a lease-expired node as a healthy endpoint.
// ---------------------------------------------------------------------------

func TestServiceRegistry_Lookup_DoesNotReturnExpired(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
	clk := &fixedClock{t: base}
	reg := NewStaticRegistry(WithClock(clk))
	ctx := context.Background()

	const ttl = 5 * time.Second
	if err := reg.Register(ctx, &Instance{
		ID: "dead-node", Service: "svc", Address: "10.0.0.9", Port: 1, LastSeen: base, TTL: ttl,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Sanity: visible while lease is live.
	if got, err := reg.Lookup(ctx, "svc"); err != nil || len(got) != 1 {
		t.Fatalf("pre-expiry Lookup: want 1 healthy, got %d (err=%v)", len(got), err)
	}

	// Advance well past TTL but DO NOT sweep. A correct Lookup must exclude the
	// lease-expired instance instead of routing traffic to a dead node.
	clk.set(base.Add(ttl + time.Hour))
	got, err := reg.Lookup(ctx, "svc")
	if err != nil {
		t.Fatalf("post-expiry Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("STALE READ: Lookup returned %d instance(s) whose TTL lease expired "+
			"%v ago without a sweep -> traffic would be routed to a dead node", len(got), time.Hour)
	}
}

// HealthyInstances is the documented "only healthy" accessor and delegates to
// Lookup; it must apply the same TTL eligibility.
func TestServiceRegistry_HealthyInstances_DoesNotReturnExpired(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 17, 1, 0, 0, 0, time.UTC)
	clk := &fixedClock{t: base}
	reg := NewStaticRegistry(WithClock(clk))
	ctx := context.Background()

	if err := reg.Register(ctx, &Instance{
		ID: "n1", Service: "svc", Port: 1, LastSeen: base, TTL: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	clk.set(base.Add(10 * time.Second))
	got, err := reg.HealthyInstances(ctx, "svc")
	if err != nil {
		t.Fatalf("HealthyInstances: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("STALE READ: HealthyInstances returned %d expired instance(s)", len(got))
	}
}

// Boundary parity with Select/SweepExpired: at elapsed==TTL (strict `>`),
// the instance is NOT yet expired and MUST still be returned by Lookup.
func TestServiceRegistry_Lookup_TTLBoundary_ExactlyAtTTLKept(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 6, 17, 2, 0, 0, 0, time.UTC)
	clk := &fixedClock{t: base}
	reg := NewStaticRegistry(WithClock(clk))
	ctx := context.Background()

	const ttl = 30 * time.Second
	if err := reg.Register(ctx, &Instance{
		ID: "n1", Service: "svc", Port: 1, LastSeen: base, TTL: ttl,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	clk.set(base.Add(ttl)) // elapsed == TTL -> kept
	got, err := reg.Lookup(ctx, "svc")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("boundary: at elapsed==TTL the instance must be KEPT (strict `>`), got %d want 1", len(got))
	}
}

// ---------------------------------------------------------------------------
// (2) Register must isolate the caller's Metadata map from the store, and
//     Lookup must return an isolated copy. A caller mutating its own map after
//     Register, or mutating the map returned by Lookup, must NOT corrupt the
//     registry's stored value.
// ---------------------------------------------------------------------------

func TestServiceRegistry_Register_MetadataIsCopied_CallerMutationIsolated(t *testing.T) {
	t.Parallel()
	reg := NewStaticRegistry()
	ctx := context.Background()

	meta := map[string]string{"zone": "us-east-1a", "role": "primary"}
	if err := reg.Register(ctx, &Instance{
		ID: "n1", Service: "svc", Port: 1, Metadata: meta,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Caller mutates ITS OWN map after registering. The store must be unaffected.
	meta["role"] = "TAMPERED"
	meta["injected"] = "evil"
	delete(meta, "zone")

	got, err := reg.Lookup(ctx, "svc")
	if err != nil || len(got) != 1 {
		t.Fatalf("Lookup: got %d err=%v", len(got), err)
	}
	gm := got[0].Metadata
	if gm["role"] != "primary" {
		t.Fatalf("ALIASING: caller mutation leaked into store: role=%q want \"primary\"", gm["role"])
	}
	if _, ok := gm["injected"]; ok {
		t.Fatalf("ALIASING: caller-injected key leaked into store metadata: %v", gm)
	}
	if gm["zone"] != "us-east-1a" {
		t.Fatalf("ALIASING: caller delete leaked into store: zone=%q want \"us-east-1a\"", gm["zone"])
	}
}

// A mutation through the slice returned by Lookup must not corrupt the store
// for the NEXT reader (returns must be isolated copies, incl. their map).
func TestServiceRegistry_Lookup_ReturnedMetadataIsolatedAcrossReads(t *testing.T) {
	t.Parallel()
	reg := NewStaticRegistry()
	ctx := context.Background()

	if err := reg.Register(ctx, &Instance{
		ID: "n1", Service: "svc", Port: 1, Metadata: map[string]string{"k": "good"},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	first, err := reg.Lookup(ctx, "svc")
	if err != nil || len(first) != 1 {
		t.Fatalf("Lookup#1: got %d err=%v", len(first), err)
	}
	// Hostile/buggy caller writes through the returned metadata map.
	first[0].Metadata["k"] = "CORRUPTED"
	first[0].Metadata["extra"] = "leak"

	second, err := reg.Lookup(ctx, "svc")
	if err != nil || len(second) != 1 {
		t.Fatalf("Lookup#2: got %d err=%v", len(second), err)
	}
	if second[0].Metadata["k"] != "good" {
		t.Fatalf("ALIASING: write through Lookup#1 metadata corrupted store for Lookup#2: k=%q want \"good\"",
			second[0].Metadata["k"])
	}
	if _, ok := second[0].Metadata["extra"]; ok {
		t.Fatalf("ALIASING: leaked key visible to subsequent reader: %v", second[0].Metadata)
	}
}

// Concurrent register/deregister/lookup WITH metadata maps under -race. If the
// store aliases caller maps, concurrent caller-side map writes + registry reads
// trip the race detector. (Run the whole package once with -race.)
func TestServiceRegistry_ConcurrentMetadata_NoAliasRace(t *testing.T) {
	t.Parallel()
	reg := NewStaticRegistry()
	ctx := context.Background()

	const workers = 32
	const iters = 100
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				m := map[string]string{"w": "x"}
				inst := &Instance{ID: "shared", Service: "svc", Port: 1, Metadata: m}
				_ = reg.Register(ctx, inst)
				// Mutate the caller-owned map AFTER register; must not race with
				// concurrent Lookup readers if the store copied the map.
				m["w"] = "y"
				got, _ := reg.Lookup(ctx, "svc")
				for _, g := range got {
					_ = g.Metadata["w"] // read store-side map
				}
			}
		}(w)
	}
	wg.Wait()
}
