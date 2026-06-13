package epochresolve

import (
	"fmt"
	"sort"
	"sync"
	"testing"
)

// ============================================================================
// ADVERSARIAL FENCING / EPOCH-RESOLUTION SAFETY TESTS
// ============================================================================
//
// HONEST SCOPE NOTE (read first — no bluff about what this package is):
//
// epochresolve is a STATELESS, pure resolution function. It does NOT mint
// monotonically-increasing epochs, and it does NOT hold a "current epoch" that
// a Validate(token) call checks a write against. There is no Advance()/Issue()
// and no in-package stale-token rejector to drive.
//
// What it DOES implement is the EPOCH-COMPARISON CORE of fencing: given a set
// of competing Claims (each stamped with an Epoch — a fencing token), Resolve
// deterministically elects exactly ONE winner per Slot using:
//     1. highest Epoch wins   (the fencing rule: newer epoch fences older)
//     2. lex-smaller Owner breaks an Epoch tie
//
// The strongest REAL safety properties this code supports — and which these
// tests prove with anti-bluff teeth — are:
//
//   (A) STALE-EPOCH FENCING: when a stale claim (E_old) competes with a newer
//       claim (E_new > E_old) for the same Slot, Resolve MUST elect E_new and
//       FENCE OUT E_old. This is exactly the fencing guarantee that stops a
//       partitioned old leader (carrying an old epoch) from winning ownership.
//
//   (B) AT-MOST-ONE authoritative owner per Slot (split-epoch / split-brain
//       analogue): never two ResolutionRecords for one Slot, never two
//       authoritative owners.
//
//   (C) ORDER-INDEPENDENCE / OBSERVER CONVERGENCE: every observer, regardless
//       of the order in which Claims arrive (and under concurrent Resolve with
//       -race), converges to the SAME winner. This is the real determinism /
//       "no observer disagrees on the committed epoch" guarantee here, and it
//       stands in for monotonicity: the resolved owner is the max-epoch claim,
//       deterministically, for every permutation.
//
// ANTI-BLUFF CORE: a fake that does NOT really fence — e.g. a beats() that
// always returns true (degenerates to last-claim-wins) or always returns false
// (first-claim-wins) — would let a STALE E_old win whenever it arrives in the
// right slot of the input order. TestStaleEpochIsFenced presents the stale
// claim in BOTH orderings and across many interleavings and asserts the stale
// owner is ALWAYS rejected and the current (newest) epoch ALWAYS accepted. A
// no-fencing bluff cannot pass it.
// ============================================================================

// bruteForceWinner is an INDEPENDENT oracle: it computes the expected winning
// Claim for a single slot by the documented rule (max Epoch; lex-smaller Owner
// on tie) WITHOUT calling into the package's beats()/Resolve(). Using a
// separate implementation as the oracle is what gives the assertions teeth — a
// bug in beats() cannot also corrupt the oracle.
func bruteForceWinner(claims []Claim) Claim {
	win := claims[0]
	for _, c := range claims[1:] {
		if c.Epoch > win.Epoch {
			win = c
			continue
		}
		if c.Epoch == win.Epoch && c.Owner < win.Owner {
			win = c
		}
	}
	return win
}

// TestStaleEpochIsFenced is THE core anti-bluff fencing test.
//
// Scenario: a "current"/newest epoch E_new is established for a slot, and a
// recovering/partitioned old leader presents a token stamped with a STALE epoch
// E_old < E_new for the SAME slot. The fencing guarantee requires that the
// stale claim is REJECTED (fenced) and the current claim is ACCEPTED (wins).
//
// Why this bites a no-fencing fake: the stale claim is placed both BEFORE and
// AFTER the current claim in the input. A bluff that ignores epochs and just
// returns the first claim (or the last claim) would, in one of the two
// orderings, let the STALE owner win — failing the assertion. Only real
// epoch-aware fencing passes BOTH orderings.
func TestStaleEpochIsFenced(t *testing.T) {
	const runID = "run-fence-stale-001"
	const slot = "lease-A"

	staleOwner := "old-leader-partitioned"
	currentOwner := "new-leader"

	staleClaim := Claim{Slot: slot, Owner: staleOwner, Epoch: 4}     // E_old
	currentClaim := Claim{Slot: slot, Owner: currentOwner, Epoch: 9} // E_new > E_old

	orderings := map[string][]Claim{
		"stale-first":   {staleClaim, currentClaim},
		"current-first": {currentClaim, staleClaim},
	}

	for name, input := range orderings {
		records, err := Resolve(runID, input)
		if err != nil {
			t.Fatalf("[%s] unexpected error: %v", name, err)
		}
		rec := findRecord(t, records, slot)

		// FENCING ASSERTION 1: the stale (older-epoch) owner is REJECTED.
		if rec.Winner.Owner == staleOwner {
			t.Fatalf("[%s] FENCING VIOLATION: stale owner %q (epoch %d) was "+
				"ACCEPTED over current owner %q (epoch %d) — a stale leader could "+
				"corrupt state", name, staleOwner, staleClaim.Epoch,
				currentOwner, currentClaim.Epoch)
		}
		// FENCING ASSERTION 2: the current (newest-epoch) owner is ACCEPTED.
		if rec.Winner.Owner != currentOwner || rec.Winner.Epoch != currentClaim.Epoch {
			t.Fatalf("[%s] current epoch not accepted: got owner=%q epoch=%d, "+
				"want owner=%q epoch=%d", name, rec.Winner.Owner, rec.Winner.Epoch,
				currentOwner, currentClaim.Epoch)
		}

		t.Logf("[%s] FENCED: stale=%q(E%d) REJECTED, current=%q(E%d) ACCEPTED",
			name, staleOwner, staleClaim.Epoch, currentOwner, currentClaim.Epoch)
	}
}

// TestStaleEpochFencedAcrossManyInterleavings hardens the fencing guarantee
// against ALL permutations of a stale claim hidden among many competitors of
// varying epochs. The newest epoch must win in every permutation; every stale
// (lower-epoch) claim must be fenced. A position-dependent bluff (first/last
// wins) collapses on at least one permutation.
func TestStaleEpochFencedAcrossManyInterleavings(t *testing.T) {
	const runID = "run-fence-perm-002"
	const slot = "lease-B"

	claims := []Claim{
		{Slot: slot, Owner: "n-stale-1", Epoch: 1},
		{Slot: slot, Owner: "n-stale-2", Epoch: 3},
		{Slot: slot, Owner: "n-stale-3", Epoch: 5},
		{Slot: slot, Owner: "n-current", Epoch: 11}, // the only authoritative epoch
	}
	want := bruteForceWinner(claims) // oracle: n-current @ E11

	perms := permute(claims)
	t.Logf("checking %d permutations; expected winner = %q (E%d)",
		len(perms), want.Owner, want.Epoch)

	for i, p := range perms {
		records, err := Resolve(runID, p)
		if err != nil {
			t.Fatalf("perm %d: unexpected error: %v", i, err)
		}
		rec := findRecord(t, records, slot)
		if rec.Winner != want {
			t.Fatalf("perm %d %v: FENCING VIOLATION: got winner %q(E%d), want %q(E%d) "+
				"— a stale epoch was accepted", i, ownersOf(p),
				rec.Winner.Owner, rec.Winner.Epoch, want.Owner, want.Epoch)
		}
		// Every non-winner is, by construction, a lower epoch and must be fenced.
		if rec.Winner.Epoch != want.Epoch {
			t.Fatalf("perm %d: winner epoch %d != newest %d", i, rec.Winner.Epoch, want.Epoch)
		}
	}
	t.Logf("all %d interleavings fenced every stale claim; newest epoch %q(E%d) always won",
		len(perms), want.Owner, want.Epoch)
}

// TestAtMostOneAuthoritativeOwner proves the split-epoch safety property: for a
// single contested slot with MANY simultaneous claimants (a partition storm),
// Resolve emits EXACTLY ONE record — never two authoritative owners. This is
// the epoch-based analogue of the split-brain at-most-one-leader guarantee.
func TestAtMostOneAuthoritativeOwner(t *testing.T) {
	const runID = "run-at-most-one-003"
	const slot = "contested-slot"

	claims := make([]Claim, 0, 50)
	for i := 0; i < 50; i++ {
		claims = append(claims, Claim{
			Slot:  slot,
			Owner: fmt.Sprintf("claimant-%02d", i),
			Epoch: uint64(i % 7), // many ties + many epochs to stress the rule
		})
	}
	want := bruteForceWinner(claims)

	records, err := Resolve(runID, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// AT-MOST-ONE: exactly one record for the contested slot.
	count := 0
	for _, r := range records {
		if r.Slot == slot {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("AT-MOST-ONE VIOLATION: %d authoritative records for slot %q, want 1",
			count, slot)
	}
	rec := findRecord(t, records, slot)
	if rec.Winner != want {
		t.Fatalf("winner mismatch: got %q(E%d), want %q(E%d)",
			rec.Winner.Owner, rec.Winner.Epoch, want.Owner, want.Epoch)
	}
	t.Logf("50 simultaneous claimants -> exactly 1 authoritative owner %q(E%d)",
		rec.Winner.Owner, rec.Winner.Epoch)
}

// TestConcurrentResolveObserverConvergence is the -race adversarial test. Many
// goroutines ("observers") independently resolve the SAME claim set, each with
// the claims SHUFFLED into a different arrival order. The safety property: no
// two observers ever disagree on the committed winner per slot, regardless of
// order, and no slot is lost or duplicated. The shared input slice is read
// concurrently; -race proves Resolve introduces no data race.
//
// This stands in for the "concurrent advance / no two observers disagree on a
// committed epoch ordering" requirement: with a stateless resolver, the
// committed ordering IS the deterministic max-epoch election, and every
// concurrent observer must land on the identical result.
func TestConcurrentResolveObserverConvergence(t *testing.T) {
	const runID = "run-concurrent-004"
	const observers = 64

	// Build a multi-slot claim set with stale + current epochs per slot.
	base := []Claim{
		{Slot: "s0", Owner: "s0-stale", Epoch: 2},
		{Slot: "s0", Owner: "s0-current", Epoch: 8},
		{Slot: "s1", Owner: "s1-a", Epoch: 5},
		{Slot: "s1", Owner: "s1-b", Epoch: 5}, // tie -> lex smaller "s1-a"
		{Slot: "s2", Owner: "s2-stale", Epoch: 1},
		{Slot: "s2", Owner: "s2-current", Epoch: 99},
		{Slot: "s3", Owner: "z-owner", Epoch: 7},
		{Slot: "s3", Owner: "a-owner", Epoch: 7}, // tie -> "a-owner"
	}

	// Independent per-slot oracle.
	bySlot := map[string][]Claim{}
	for _, c := range base {
		bySlot[c.Slot] = append(bySlot[c.Slot], c)
	}
	wantWinner := map[string]Claim{}
	for slot, cs := range bySlot {
		wantWinner[slot] = bruteForceWinner(cs)
	}

	type result struct {
		winners map[string]Claim // slot -> winner
	}
	results := make([]result, observers)

	var wg sync.WaitGroup
	for g := 0; g < observers; g++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Each observer sees a distinct, deterministic arrival order.
			shuffled := shuffleDeterministic(base, uint64(idx)*2654435761+1)
			records, err := Resolve(runID, shuffled)
			if err != nil {
				t.Errorf("observer %d: unexpected error: %v", idx, err)
				return
			}
			w := make(map[string]Claim, len(records))
			seen := map[string]bool{}
			for _, r := range records {
				if seen[r.Slot] {
					t.Errorf("observer %d: DUPLICATE record for slot %q", idx, r.Slot)
				}
				seen[r.Slot] = true
				w[r.Slot] = r.Winner
			}
			results[idx] = result{winners: w}
		}(g)
	}
	wg.Wait()

	// CONVERGENCE: every observer agrees with the oracle (hence with each other),
	// and no slot is lost or duplicated.
	for idx, res := range results {
		if len(res.winners) != len(wantWinner) {
			t.Fatalf("observer %d: got %d slots, want %d (slot lost or duplicated)",
				idx, len(res.winners), len(wantWinner))
		}
		for slot, want := range wantWinner {
			got, ok := res.winners[slot]
			if !ok {
				t.Fatalf("observer %d: missing slot %q", idx, slot)
			}
			if got != want {
				t.Fatalf("observer DISAGREEMENT: observer %d slot %q got %q(E%d), "+
					"want %q(E%d) — observers must converge on the committed epoch",
					idx, slot, got.Owner, got.Epoch, want.Owner, want.Epoch)
			}
		}
	}

	t.Logf("%d concurrent observers converged identically on %d slots (no race, no disagreement):",
		observers, len(wantWinner))
	slots := make([]string, 0, len(wantWinner))
	for s := range wantWinner {
		slots = append(slots, s)
	}
	sort.Strings(slots)
	for _, s := range slots {
		w := wantWinner[s]
		t.Logf("  slot %q -> %q (E%d)", s, w.Owner, w.Epoch)
	}
}

// ---------------------------------------------------------------------------
// helpers (test-only)
// ---------------------------------------------------------------------------

// ownersOf renders the owners of a claim slice for diagnostic output.
func ownersOf(cs []Claim) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = fmt.Sprintf("%s(E%d)", c.Owner, c.Epoch)
	}
	return out
}

// permute returns all permutations of cs (factorial — keep input small).
func permute(cs []Claim) [][]Claim {
	var out [][]Claim
	var rec func(prefix, rest []Claim)
	rec = func(prefix, rest []Claim) {
		if len(rest) == 0 {
			cp := make([]Claim, len(prefix))
			copy(cp, prefix)
			out = append(out, cp)
			return
		}
		for i := range rest {
			next := make([]Claim, 0, len(rest)-1)
			next = append(next, rest[:i]...)
			next = append(next, rest[i+1:]...)
			rec(append(prefix, rest[i]), next)
		}
	}
	rec(nil, cs)
	return out
}

// shuffleDeterministic returns a copy of cs reordered by a seeded xorshift PRNG
// (no shared global rand state -> safe under -race, and reproducible).
func shuffleDeterministic(cs []Claim, seed uint64) []Claim {
	out := make([]Claim, len(cs))
	copy(out, cs)
	x := seed | 1
	for i := len(out) - 1; i > 0; i-- {
		// xorshift64
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		j := int(x % uint64(i+1))
		out[i], out[j] = out[j], out[i]
	}
	return out
}
