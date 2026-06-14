package edgeregistry

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// advRunUUID uniquely tags this adversarial suite's output so a silently
// skipped / short-circuited run (a PASS-bluff) is observable in captured logs.
const advRunUUID = "edgeregistry-adv-7c1f9b46-2d83-4e0a-9b6e-5a4c0e1d2f88"

// -----------------------------------------------------------------------------
// CENTERPIECE 1 — concurrent churn race + conservation under the FULL API.
//
// The HXC-1667 class is a DATA RACE on byID: Register WRITES the map while
// Get/Len/ListByTier/Dump READ it. This hammers ALL FIVE entry points from many
// goroutines simultaneously under -race, with a churning key space (each writer
// re-registers a small rotating set of ids -> in-place updates) plus a stable
// "anchor" set whose membership is invariant. We assert:
//   - no race / no panic (the race detector + runtime map-write guard),
//   - CONSERVATION: the anchor ids are NEVER lost or duplicated mid-flight and
//     are all present at the end with their exact stored attributes,
//   - the churning ids each resolve to a self-consistent (tier,mem) pair that a
//     writer actually wrote (no torn read across the in-place overwrite).
//
// WHY IT BITES: remove the lock around the map in any one method and the race
// detector reports "concurrent map read and map write" (or the runtime throws
// the unrecoverable fatal). The anchor-conservation assertions additionally bite
// a lost-update bug even if a race somehow went unreported.
// -----------------------------------------------------------------------------
func TestAdversarial_ConcurrentChurnConservation_FullAPI(t *testing.T) {
	r := New()

	// Anchor set: registered once, never re-registered, never overwritten by the
	// churn key space (disjoint "anchor-" prefix). Its membership is invariant.
	const anchors = 24
	anchorMem := make(map[string]int, anchors)
	for i := 0; i < anchors; i++ {
		id := fmt.Sprintf("anchor-%03d", i)
		tier := allTiers[i%len(allTiers)]
		mem := 5000 + i
		anchorMem[id] = mem
		if err := r.Register(Registration{
			NodeID: id, Tier: tier, Trust: TrustVerified,
			Caps: Capability{ComputeClass: "anchor", MemoryMB: mem, Backends: []string{"a"}},
		}); err != nil {
			t.Fatalf("seed anchor %q: %v", id, err)
		}
	}
	t.Logf("run=%s seeded anchors=%d len=%d", advRunUUID, anchors, r.Len())

	const writers = 8
	const readers = 8
	const iters = 600
	const churnKeys = 12 // rotating ids per writer

	// Each churning id maps tier deterministically from its memory so a reader
	// can detect a TORN read (tier inconsistent with mem) across the overwrite.
	memToTier := func(mem int) Tier { return allTiers[mem%len(allTiers)] }

	var wg sync.WaitGroup
	var tornReads int64
	var anchorMisses int64

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				mem := 100 + (w*131+i*17)%900
				id := fmt.Sprintf("churn-w%d-k%d", w, i%churnKeys)
				if err := r.Register(Registration{
					NodeID: id, Tier: memToTier(mem), Trust: TrustBasic,
					Caps: Capability{ComputeClass: "cpu", MemoryMB: mem, Backends: []string{"b"}},
				}); err != nil {
					t.Errorf("Register %q: %v", id, err)
					return
				}
			}
		}(w)
	}

	for rd := 0; rd < readers; rd++ {
		wg.Add(1)
		go func(rd int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Anchor lookups: must ALWAYS hit (conservation mid-flight) with
				// the exact seeded memory — a miss means a lost/clobbered entry.
				aid := fmt.Sprintf("anchor-%03d", i%anchors)
				if got, err := r.Get(aid); err != nil {
					atomic.AddInt64(&anchorMisses, 1)
				} else if got.Caps.MemoryMB != anchorMem[aid] {
					atomic.AddInt64(&anchorMisses, 1)
				}

				// Churn lookups: a hit must be internally consistent (tier derived
				// from mem) — proves Get returns a coherent committed snapshot, not
				// a half-overwritten struct.
				cid := fmt.Sprintf("churn-w%d-k%d", i%writers, i%churnKeys)
				if got, err := r.Get(cid); err == nil {
					if got.Tier != memToTier(got.Caps.MemoryMB) {
						atomic.AddInt64(&tornReads, 1)
					}
				} else if !errors.Is(err, ErrNotFound) {
					t.Errorf("Get(%q) unexpected error: %v", cid, err)
					return
				}

				_ = r.Len()
				_ = r.ListByTier(allTiers[i%len(allTiers)])
				if d := r.Dump(); len(d) < anchors {
					t.Errorf("Dump len %d < anchors %d (anchor lost mid-flight)", len(d), anchors)
					return
				}
			}
		}(rd)
	}

	wg.Wait()

	if n := atomic.LoadInt64(&anchorMisses); n != 0 {
		t.Fatalf("conservation: %d anchor lookups missed/clobbered mid-flight", n)
	}
	if n := atomic.LoadInt64(&tornReads); n != 0 {
		t.Fatalf("torn read: %d churn lookups returned tier inconsistent with mem", n)
	}

	// Final state: exactly anchors + (writers*churnKeys) distinct ids, every
	// anchor present with its exact seeded memory, Dump strictly sorted+unique.
	wantLen := anchors + writers*churnKeys
	if r.Len() != wantLen {
		t.Fatalf("final Len = %d, want %d (lost/ghost/duplicate)", r.Len(), wantLen)
	}
	for id, mem := range anchorMem {
		got, err := r.Get(id)
		if err != nil {
			t.Fatalf("final Get(%q): %v (anchor lost)", id, err)
		}
		if got.Caps.MemoryMB != mem {
			t.Errorf("anchor %q mem = %d, want %d (clobbered)", id, got.Caps.MemoryMB, mem)
		}
	}
	dump := r.Dump()
	if len(dump) != wantLen {
		t.Fatalf("final Dump len = %d, want %d", len(dump), wantLen)
	}
	for i := 1; i < len(dump); i++ {
		if dump[i-1].NodeID >= dump[i].NodeID {
			t.Fatalf("Dump not strictly sorted/unique: %q >= %q", dump[i-1].NodeID, dump[i].NodeID)
		}
	}
	t.Logf("run=%s done len=%d (anchors=%d churn=%d)", advRunUUID, r.Len(), anchors, writers*churnKeys)
}

// -----------------------------------------------------------------------------
// CENTERPIECE 2 — deep stored-copy isolation (ingress + egress, every path).
//
// Pins the copy contract: a registered entry shares NO memory with the caller's
// struct, and every read path (Get / ListByTier / Dump) hands back an
// independent deep copy. Probes the ONLY reference-typed deep field, Backends,
// on BOTH the register-side alias and the read-side alias, and proves two reads
// don't alias each other.
//
// WHY IT BITES: store/return the entry by reference instead of clone() (or drop
// the Backends deep-copy in Capability.clone), and a caller mutation corrupts
// registry state -> the "orig"/length assertions below fail.
// -----------------------------------------------------------------------------
func TestAdversarial_DeepStoredCopyIsolation_AllPaths(t *testing.T) {
	r := New()

	// (a) INGRESS alias: mutating the caller's backing array after Register must
	// not touch stored state — also mutate length-changing (append) to catch a
	// shared-cap aliasing variant.
	backends := []string{"orig0", "orig1"}
	in := Registration{
		NodeID: "n-ingress", Tier: T5, Trust: TrustBasic,
		Caps: Capability{ComputeClass: "cpu", MemoryMB: 100, Backends: backends},
	}
	if err := r.Register(in); err != nil {
		t.Fatalf("Register ingress: %v", err)
	}
	backends[0] = "TAMPERED"
	backends[1] = "TAMPERED"
	// Mutate the original struct's other fields too — stored copy must be immune.
	in.Tier = T8
	in.Caps.MemoryMB = 999999
	in.Caps.Backends = append(in.Caps.Backends, "EXTRA")

	got, err := r.Get("n-ingress")
	if err != nil {
		t.Fatalf("Get ingress: %v", err)
	}
	t.Logf("run=%s ingress stored=%+v", advRunUUID, got.Caps)
	if got.Tier != T5 || got.Caps.MemoryMB != 100 {
		t.Fatalf("ingress: stored scalar mutated via caller struct: tier=%v mem=%d", got.Tier, got.Caps.MemoryMB)
	}
	if len(got.Caps.Backends) != 2 || got.Caps.Backends[0] != "orig0" || got.Caps.Backends[1] != "orig1" {
		t.Fatalf("ingress: stored Backends corrupted via caller alias: %v", got.Caps.Backends)
	}

	// (b) EGRESS alias: a value returned by Get is the caller's to mutate and
	// must NOT write back into the registry.
	got.Caps.Backends[0] = "EGRESS_TAMPER"
	got.Caps.Backends = append(got.Caps.Backends, "EGRESS_EXTRA")
	got.Tier = T3
	again, err := r.Get("n-ingress")
	if err != nil {
		t.Fatalf("Get ingress again: %v", err)
	}
	if again.Tier != T5 || len(again.Caps.Backends) != 2 || again.Caps.Backends[0] != "orig0" {
		t.Fatalf("egress: caller mutation of Get result corrupted registry: tier=%v backends=%v", again.Tier, again.Caps.Backends)
	}

	// (c) TWO reads must not alias EACH OTHER (independent backing arrays).
	g1, _ := r.Get("n-ingress")
	g2, _ := r.Get("n-ingress")
	g1.Caps.Backends[0] = "G1_ONLY"
	if g2.Caps.Backends[0] == "G1_ONLY" {
		t.Fatalf("two Get results alias the same Backends backing array")
	}

	// (d) ListByTier and Dump egress isolation: mutate their results, registry
	// must stay pristine.
	if lst := r.ListByTier(T5); len(lst) == 1 {
		lst[0].Caps.Backends[0] = "LIST_TAMPER"
		lst[0].Tier = T8
	} else {
		t.Fatalf("ListByTier(T5) len = %d, want 1", len(lst))
	}
	for _, d := range r.Dump() {
		if d.NodeID == "n-ingress" {
			d.Caps.Backends[0] = "DUMP_TAMPER"
			d.Tier = T8
		}
	}
	final, _ := r.Get("n-ingress")
	if final.Tier != T5 || final.Caps.Backends[0] != "orig0" {
		t.Fatalf("ListByTier/Dump egress mutation corrupted registry: %+v", final)
	}

	// (e) nil / empty Backends survive clone as a zero-length, range-safe slice.
	// (clone() normalizes empty-non-nil to nil, which is semantically equivalent
	// for callers; we pin the contract that matters: no spurious elements and no
	// panic when ranging/indexing length.)
	r2 := New()
	mustReg(t, r2, Registration{NodeID: "nilcaps", Tier: T4, Caps: Capability{ComputeClass: "c", MemoryMB: 1, Backends: nil}})
	mustReg(t, r2, Registration{NodeID: "emptycaps", Tier: T4, Caps: Capability{ComputeClass: "c", MemoryMB: 1, Backends: []string{}}})
	gn, _ := r2.Get("nilcaps")
	ge, _ := r2.Get("emptycaps")
	if len(gn.Caps.Backends) != 0 {
		t.Errorf("nil Backends gained elements after clone: %#v", gn.Caps.Backends)
	}
	if len(ge.Caps.Backends) != 0 {
		t.Errorf("empty Backends gained elements after clone: %#v", ge.Caps.Backends)
	}
	n := 0 // ranging a cloned zero-length slice must be safe (no panic).
	for range gn.Caps.Backends {
		n++
	}
	for range ge.Caps.Backends {
		n++
	}
	if n != 0 {
		t.Errorf("zero-length Backends ranged %d elements", n)
	}
}

// -----------------------------------------------------------------------------
// CONSERVATION (deterministic) — register-then-overwrite leaves the exact set;
// re-registration updates IN PLACE (overwrite semantics, NOT reject); the final
// Dump/ListByTier partition is exact. Pins the "overwrite vs reject" decision.
// -----------------------------------------------------------------------------
func TestAdversarial_DeterministicOverwriteConservation(t *testing.T) {
	r := New()

	// Register the same id 5 times with escalating attributes; last write wins.
	const id = "same-id"
	for i := 0; i < 5; i++ {
		mustReg(t, r, Registration{
			NodeID: id, Tier: allTiers[i%len(allTiers)], Trust: TrustLevel(i % 4),
			Caps: Capability{ComputeClass: "c", MemoryMB: 1000 + i, Backends: []string{fmt.Sprintf("v%d", i)}},
		})
		if r.Len() != 1 {
			t.Fatalf("after overwrite #%d Len = %d, want 1 (overwrite must NOT duplicate)", i, r.Len())
		}
	}
	got, err := r.Get(id)
	if err != nil {
		t.Fatalf("Get(%q): %v", id, err)
	}
	// Last write: i=4.
	if got.Caps.MemoryMB != 1004 || got.Caps.Backends[0] != "v4" || got.Tier != allTiers[4%len(allTiers)] {
		t.Fatalf("overwrite did not keep last write: %+v", got)
	}

	// Build a known multi-tier set and verify ListByTier partitions exactly and
	// Dump is the exact union.
	r2 := New()
	want := map[Tier][]string{}
	total := 0
	for ti, tier := range allTiers {
		for k := 0; k <= ti; k++ { // tier T3 gets 1 node, T4 gets 2, ... T8 gets 6
			nid := fmt.Sprintf("%s-%d", tier, k)
			mustReg(t, r2, Registration{NodeID: nid, Tier: tier, Caps: Capability{ComputeClass: "c", MemoryMB: 1}})
			want[tier] = append(want[tier], nid)
			total++
		}
	}
	if r2.Len() != total || len(r2.Dump()) != total {
		t.Fatalf("set size mismatch: Len=%d Dump=%d want=%d", r2.Len(), len(r2.Dump()), total)
	}
	gotTotal := 0
	for _, tier := range allTiers {
		lst := r2.ListByTier(tier)
		gotTotal += len(lst)
		ids := make([]string, len(lst))
		for i, reg := range lst {
			if reg.Tier != tier {
				t.Errorf("ListByTier(%v) returned foreign tier %v for %q", tier, reg.Tier, reg.NodeID)
			}
			ids[i] = reg.NodeID
		}
		if !reflect.DeepEqual(ids, want[tier]) { // want is already insertion-sorted == id-sorted here
			t.Errorf("ListByTier(%v) = %v, want %v", tier, ids, want[tier])
		}
	}
	if gotTotal != total {
		t.Fatalf("partition lost entries: sum ListByTier = %d, want %d", gotTotal, total)
	}
}

// -----------------------------------------------------------------------------
// EDGES — get-missing, double-register, empty registry, invalid inputs do not
// leak, boundary tiers, and that a rejected Register leaves NO partial state.
// -----------------------------------------------------------------------------
func TestAdversarial_Edges(t *testing.T) {
	r := New()

	// Empty registry.
	if r.Len() != 0 {
		t.Fatalf("new registry Len = %d, want 0", r.Len())
	}
	if d := r.Dump(); d == nil || len(d) != 0 {
		t.Fatalf("empty Dump = %v, want non-nil empty", d)
	}
	for _, tier := range allTiers {
		if l := r.ListByTier(tier); l == nil || len(l) != 0 {
			t.Fatalf("empty ListByTier(%v) = %v, want non-nil empty", tier, l)
		}
	}
	if _, err := r.Get("anything"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on empty: err = %v, want ErrNotFound", err)
	}

	// Rejected registrations leave no partial state.
	if err := r.Register(Registration{NodeID: "", Tier: T5}); !errors.Is(err, ErrEmptyNodeID) {
		t.Fatalf("empty id: %v", err)
	}
	if err := r.Register(Registration{NodeID: "bad", Tier: 99}); !errors.Is(err, ErrTierOutOfRange) {
		t.Fatalf("bad tier: %v", err)
	}
	if _, err := r.Get("bad"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected bad-tier reg leaked: Get(bad) err = %v, want ErrNotFound", err)
	}
	if r.Len() != 0 {
		t.Fatalf("rejected regs leaked, Len = %d", r.Len())
	}

	// Boundary tiers admitted; just-outside rejected.
	for _, ok := range []Tier{T3, T8} {
		mustReg(t, r, Registration{NodeID: "ok-" + ok.String(), Tier: ok})
	}
	for _, bad := range []Tier{TierMin - 1, TierMax + 1} {
		if err := r.Register(Registration{NodeID: "x", Tier: bad}); !errors.Is(err, ErrTierOutOfRange) {
			t.Errorf("boundary-1 tier %d: err = %v, want ErrTierOutOfRange", int(bad), err)
		}
	}
}

func mustReg(t *testing.T, r *Registry, reg Registration) {
	t.Helper()
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register(%q): %v", reg.NodeID, err)
	}
}
