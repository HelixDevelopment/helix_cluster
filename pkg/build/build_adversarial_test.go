package build

// Adversarial sink-side probes for pkg/build.
//
// SCOPE NOTE (honest): The task brief targets a "dependency-graph build
// pipeline" with topological ordering and cycle detection. THIS pkg/build has
// NO such graph: there is no dependency map, no topological sort, and no cycle
// detector anywhere in build.go / cache / manifest / platform. It is a build
// *job* orchestration Service (queue + worker pool + state machine) plus a
// content-addressable cache and a multi-arch manifest. Bug classes (a) topo
// determinism and (b) cycle detection therefore have no sink to probe here and
// are documented as N/A below rather than faked.
//
// The two applicable classes ARE probed against real sinks:
//   (c) UNGUARDED SHARED STATE / lost result under concurrency (-race +
//       conservation), and
//   (d) CACHE / SNAPSHOT CORRECTNESS (a "deep copy" that aliases mutable
//       state -> a returned snapshot that mutates the live job, and a
//       content-addressable cache that returns content not matching its key).
//
// Determinism contracts that DO exist (Manifest.Platforms sorted ordering) are
// pinned so a regression to map-iteration order would bite.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
)

// ---------------------------------------------------------------------------
// (a)/(b) TOPO-ORDER + CYCLE DETECTION: documented N/A for this package.
// ---------------------------------------------------------------------------

// TestAdv_NoBuildGraphPresent documents (and locks) the fact that this package
// exposes no dependency graph / topological ordering / cycle detection API. If
// such an API is added later, this guard should be revisited and real
// determinism + cycle probes written. This is a contract-pin, not a bug.
func TestAdv_NoBuildGraphPresent(t *testing.T) {
	// The public surface is: Service (queue/worker/state machine), Builder,
	// Manifest, Platform, cache.Cache. None order nodes by dependency edges.
	// Nothing to assert beyond this documentation anchor; the build itself
	// failing to compile if these types vanish is the live signal.
	var _ = NewService
	var _ = NewManifest
	var _ = ParsePlatform
}

// ---------------------------------------------------------------------------
// Determinism contract that DOES exist: Manifest.Platforms() is sorted.
// ---------------------------------------------------------------------------

// TestAdv_ManifestPlatforms_Deterministic asserts that Platforms() returns a
// byte-identical, sorted order across MANY runs over a map (whose native
// iteration order Go deliberately randomizes). A regression that dropped the
// sort.Strings call (leaking map-iteration order) would make this flap.
func TestAdv_ManifestPlatforms_Deterministic(t *testing.T) {
	build := func() []string {
		m := NewManifest()
		// Insertion order intentionally unsorted; many keys to make map-order
		// nondeterminism overwhelmingly likely if the sort were dropped.
		specs := []string{
			"linux/amd64", "linux/arm64", "linux/arm64/v8", "linux/386",
			"windows/amd64", "darwin/arm64", "darwin/amd64", "linux/ppc64le",
			"linux/s390x", "linux/riscv64", "freebsd/amd64", "linux/mips64",
		}
		for i, s := range specs {
			p, err := ParsePlatform(s)
			if err != nil {
				t.Fatalf("parse %q: %v", s, err)
			}
			if err := m.Add(p, fmt.Sprintf("sha256:%064d", i)); err != nil {
				t.Fatalf("add %q: %v", s, err)
			}
		}
		return m.Platforms()
	}

	want := build()
	for iter := 0; iter < 2000; iter++ {
		got := build()
		if len(got) != len(want) {
			t.Fatalf("iter %d: len=%d, want %d", iter, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("iter %d: Platforms()[%d]=%q, want %q (nondeterministic order)", iter, i, got[i], want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// (c) UNGUARDED SHARED STATE / conservation under concurrency.
// ---------------------------------------------------------------------------

// TestAdv_ServiceConservation_Concurrent runs many concurrent Submits while
// workers drain, then asserts CONSERVATION: every successfully-submitted job is
// present in List(), reaches a terminal state, and no job is lost or
// duplicated. Run with -race this also exercises the jobs-map locking.
func TestAdv_ServiceConservation_Concurrent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService(8)
	svc.Start(ctx)
	defer svc.Stop()

	const n = 80
	var submitted sync.Map // id -> struct{}
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(k int) {
			defer wg.Done()
			id := fmt.Sprintf("cons-%d", k)
			if err := svc.Submit(&Job{ID: id, RepoURL: "url", Ref: "main"}); err != nil {
				t.Errorf("submit %s: %v", id, err)
				return
			}
			submitted.Store(id, struct{}{})
		}(i)
	}
	wg.Wait()

	// Count submitted.
	count := 0
	submitted.Range(func(_, _ any) bool { count++; return true })
	if count != n {
		t.Fatalf("submitted count = %d, want %d", count, n)
	}

	// Every submitted job must reach terminal AND be present exactly once.
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("cons-%d", i)
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := svc.WaitDone(dctx, id)
		dcancel()
		if err != nil {
			t.Fatalf("WaitDone(%s): %v", id, err)
		}
		if !got.IsTerminal() {
			t.Errorf("%s not terminal: %s", id, got.State)
		}
	}

	jobs := svc.List()
	if len(jobs) != n {
		t.Errorf("CONSERVATION VIOLATION: List() len = %d, want %d (lost/duplicated jobs)", len(jobs), n)
	}
	seen := map[string]int{}
	for _, j := range jobs {
		seen[j.ID]++
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("CONSERVATION VIOLATION: job %s appears %d times in List()", id, c)
		}
	}
}

// ---------------------------------------------------------------------------
// (d) SNAPSHOT CORRECTNESS: clone() claims "deep copy" but ALIASES BuildArgs.
// ---------------------------------------------------------------------------

// TestAdv_GetSnapshot_BuildArgsAliased proves the SINK: Service.Get is
// documented to return "a deep copy of a job", and clone() is documented "deep
// copy of the job". But clone() copies the BuildArgs map BY REFERENCE. So a
// caller who mutates the returned snapshot's BuildArgs silently corrupts the
// canonical job held inside the Service (and any concurrent reader of it).
//
// This asserts the WRONG observed result (the live job mutated through a
// supposedly-independent snapshot), not merely "no panic".
func TestAdv_GetSnapshot_BuildArgsAliased(t *testing.T) {
	svc := NewService(1) // not Start()ed; we only inspect snapshots

	orig := map[string]string{"VERSION": "1.0.0", "TARGET": "prod"}
	j := &Job{ID: "alias-1", RepoURL: "url", Ref: "main", BuildArgs: orig}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}

	snap1, err := svc.Get("alias-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Mutate the snapshot the caller believes is independent.
	snap1.BuildArgs["VERSION"] = "9.9.9-TAMPERED"

	// SINK: take a fresh snapshot from the Service. If clone() were a true deep
	// copy, the canonical job's BuildArgs would be untouched. It is not.
	snap2, err := svc.Get("alias-1")
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if snap2.BuildArgs["VERSION"] != "1.0.0" {
		t.Errorf("SNAPSHOT-ALIAS BUG: canonical job BuildArgs corrupted via supposedly-deep-copy snapshot: "+
			"Service now reports VERSION=%q, want %q (clone() aliases the BuildArgs map)",
			snap2.BuildArgs["VERSION"], "1.0.0")
	}
}

// TestAdv_CloneRace_BuildArgs proves the same aliasing manifests as a real
// DATA RACE under -race: a builder ranging over the live job's BuildArgs (as
// podmanBuilder.Build does, holding no lock) races a caller mutating a Get()
// snapshot's aliased map. With a true deep copy there is no shared memory and
// no race.
func TestAdv_CloneRace_BuildArgs(t *testing.T) {
	svc := NewService(1)
	j := &Job{ID: "alias-race", RepoURL: "url", Ref: "main", BuildArgs: map[string]string{"K": "v"}}
	if err := svc.Submit(j); err != nil {
		t.Fatalf("submit: %v", err)
	}
	snap, err := svc.Get("alias-race")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	// Reader: emulate podmanBuilder.Build ranging j.BuildArgs with no lock.
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			for range j.BuildArgs { //nolint
			}
		}
	}()
	// Writer: mutate the snapshot's aliased map.
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			snap.BuildArgs["K"] = fmt.Sprintf("v%d", i)
		}
	}()
	wg.Wait()
}

// ---------------------------------------------------------------------------
// (d) CACHE CORRECTNESS: content-addressable Get must match its key.
// ---------------------------------------------------------------------------

// TestAdv_MemoryCache_StoresIndependentCopy is a positive contract-pin: the
// in-memory cache copies on Put and on Get, so mutating caller buffers cannot
// poison a cached entry (no stale/aliased result). This is the GOOD behaviour
// the snapshot path above fails to provide — pinned so a regression bites.
func TestAdv_MemoryCache_StoresIndependentCopy(t *testing.T) {
	c := cache.NewMemoryCache()
	data := []byte("artifact-v1")
	d := cache.Digest(data)
	if err := c.Put(d, data); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Mutate the caller's buffer AFTER Put.
	data[0] = 'X'
	got, err := c.Get(d)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "artifact-v1" {
		t.Errorf("CACHE-ALIAS BUG: Get returned %q, want %q (Put did not copy)", got, "artifact-v1")
	}
	// Mutate the returned buffer; a second Get must be unaffected.
	got[0] = 'Y'
	got2, _ := c.Get(d)
	if string(got2) != "artifact-v1" {
		t.Errorf("CACHE-ALIAS BUG: second Get returned %q, want %q (Get did not copy)", got2, "artifact-v1")
	}
}
