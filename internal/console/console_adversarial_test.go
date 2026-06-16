package console

// Adversarial concurrency / identity / fail-open probes for internal/console.
//
// Hunted classes (mirroring the internal/node shared-pointer-mutation bug):
//   (a) SHARED-POINTER MUTATION RACE: a registration path that hands a shared
//       map pointer to a concurrent reader so a writer mutates it in place.
//   (b) UNGUARDED SHARED STATE / lost update on the registry: concurrent
//       Register of N distinct IDs must conserve all N (no torn map writes).
//   (c) IDENTITY / TOTAL-ORDER: duplicate IDs must collapse deterministically
//       to a single entry (last-write-wins), not duplicate or corrupt.
//   (d) FAIL-OPEN / VALIDATION: empty ID must be rejected; BootCoordinator's
//       documented "safe for concurrent use" must hold under -race.
//
// All probes are in-process, capped at <=8 concurrent goroutines, and complete
// well within `go test -timeout 90s`.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
)

const advConcurrency = 8

// ----------------------------------------------------------------------------
// (b) CONSERVATION: concurrent Register of N distinct IDs -> all N discoverable.
// Sink-side: we look every node back up through the registry and assert the set
// is complete. A torn map write or lost update in the registry path surfaces as
// a missing ID here, and -race surfaces any unsynchronized access.
// ----------------------------------------------------------------------------
func TestAdversarial_ConcurrentRegister_Conservation(t *testing.T) {
	t.Parallel()
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	const perWorker = 25
	total := advConcurrency * perWorker

	var wg sync.WaitGroup
	errs := make(chan error, total)
	for w := 0; w < advConcurrency; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("node-%d-%d", w, i)
				cap := &resources.NodeResources{CPU: resources.CPUInfo{Cores: w + 1}}
				if _, err := r.Register(id, TypePS5, "us-east-1", map[string]string{"w": fmt.Sprint(w)}, cap); err != nil {
					errs <- fmt.Errorf("register %s: %w", id, err)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent register error: %v", err)
	}

	insts, err := registry.Lookup(context.Background(), "helix-console-node")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(insts) != total {
		t.Fatalf("CONSERVATION VIOLATED: registered %d nodes, registry has %d", total, len(insts))
	}
	seen := make(map[string]bool, total)
	for _, in := range insts {
		seen[in.ID] = true
	}
	for w := 0; w < advConcurrency; w++ {
		for i := 0; i < perWorker; i++ {
			id := fmt.Sprintf("node-%d-%d", w, i)
			if !seen[id] {
				t.Fatalf("CONSERVATION VIOLATED: %s missing from registry after concurrent register", id)
			}
		}
	}
}

// ----------------------------------------------------------------------------
// (a) SHARED-POINTER MUTATION RACE: concurrent Register + Lookup of the SAME
// node id. If the registration path published a Metadata map pointer that a
// later writer mutates in place (instead of replacing it under lock), the
// concurrent Lookup reader would race the writer. We hammer both sides under
// -race; the assertion is "-race clean", not "no panic".
// ----------------------------------------------------------------------------
func TestAdversarial_RegisterLookupSameID_NoSharedPointerRace(t *testing.T) {
	t.Parallel()
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	const iters = 300
	var wg sync.WaitGroup

	// Writers: re-register the same id with fresh metadata each time.
	for w := 0; w < advConcurrency/2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_, _ = r.Register("hot-node", TypePS5, "r",
					map[string]string{"gen": fmt.Sprintf("%d-%d", w, i)}, nil)
			}
		}(w)
	}
	// Readers: look up + touch the published Metadata map of the hot node.
	for rd := 0; rd < advConcurrency/2; rd++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				insts, err := registry.Lookup(context.Background(), "helix-console-node")
				if err != nil {
					continue
				}
				for _, in := range insts {
					// Read every key of the published map — this is the read side
					// that would race an in-place writer mutating the same map.
					for k := range in.Metadata {
						_ = in.Metadata[k]
					}
				}
			}
		}()
	}
	wg.Wait()

	// Sink-side: identity collapses to exactly one hot-node entry.
	insts, err := registry.Lookup(context.Background(), "helix-console-node")
	if err != nil {
		t.Fatalf("final lookup: %v", err)
	}
	count := 0
	for _, in := range insts {
		if in.ID == "hot-node" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("IDENTITY VIOLATED: expected exactly 1 hot-node entry, got %d", count)
	}
}

// ----------------------------------------------------------------------------
// (c) IDENTITY / TOTAL-ORDER: duplicate IDs must collapse to a single entry
// (last-write-wins on service/id), deterministically — never duplicate rows
// nor a nondeterministic count.
// ----------------------------------------------------------------------------
func TestAdversarial_DuplicateID_CollapsesDeterministically(t *testing.T) {
	t.Parallel()
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	for i := 0; i < 50; i++ {
		if _, err := r.Register("dup", TypePS5, "r", map[string]string{"i": fmt.Sprint(i)}, nil); err != nil {
			t.Fatalf("register dup #%d: %v", i, err)
		}
	}
	insts, err := registry.Lookup(context.Background(), "helix-console-node")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("IDENTITY VIOLATED: 50 same-id registrations -> %d entries, want 1", len(insts))
	}
	if insts[0].ID != "dup" {
		t.Fatalf("expected id dup, got %s", insts[0].ID)
	}
}

// ----------------------------------------------------------------------------
// (d) FAIL-OPEN / VALIDATION: empty ID must be rejected and nothing published.
// ----------------------------------------------------------------------------
func TestAdversarial_EmptyID_RejectedNotFailOpen(t *testing.T) {
	t.Parallel()
	backend := discovery.NewInMemoryBackend()
	registry := discovery.NewServiceRegistry(backend)
	r := NewRegistrar(registry)

	rec, err := r.Register("", TypePS5, "r", map[string]string{"x": "y"}, nil)
	if err == nil {
		t.Fatal("FAIL-OPEN: empty ID was accepted")
	}
	if rec != nil {
		t.Fatalf("FAIL-OPEN: empty ID returned a record %+v", rec)
	}
	insts, lerr := registry.Lookup(context.Background(), "helix-console-node")
	if lerr != nil {
		t.Fatalf("lookup: %v", lerr)
	}
	if len(insts) != 0 {
		t.Fatalf("FAIL-OPEN: empty-ID registration published %d instances", len(insts))
	}
}

// ----------------------------------------------------------------------------
// (d) BootCoordinator documents "safe for concurrent use". Probe Probe(),
// CurrentPhase(), and WaitReady() concurrently under -race. Assertion is
// -race clean AND a coherent terminal phase, not "no panic".
// ----------------------------------------------------------------------------
func TestAdversarial_BootCoordinator_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vp := dir + "/version"
	cl := dir + "/cmdline"
	lg := dir + "/boot.log"
	writeFile(t, vp, "Linux version 6.6.0")
	writeFile(t, cl, "ro quiet helix.userspace=ready helix.cluster=ready")

	bc := NewBootCoordinator(BootMarkers{VersionPath: vp, CmdlinePath: cl, BootLogPath: lg})
	bc.PollInterval = 2 * time.Millisecond

	var wg sync.WaitGroup
	for g := 0; g < advConcurrency; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			switch g % 3 {
			case 0:
				for i := 0; i < 100; i++ {
					if _, err := bc.Probe(); err != nil {
						t.Errorf("probe: %v", err)
						return
					}
				}
			case 1:
				for i := 0; i < 100; i++ {
					_ = bc.CurrentPhase()
				}
			default:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := bc.WaitReady(ctx); err != nil {
					t.Errorf("waitready: %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	if ph := bc.CurrentPhase(); ph != PhaseClusterReady {
		t.Fatalf("expected terminal phase ClusterReady, got %s", ph)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
