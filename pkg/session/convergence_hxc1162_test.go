package session

// convergence_hxc1162_test.go — HXC-1162: multi-node gossip CRDT convergence.
//
// Goes further than convergence_test.go (2-replica, single anti-entropy round)
// and crdt_hxc1085_test.go (6-replica, one global fold):
//
//   - N=5 INDEPENDENT replicas each hold a DISJOINT partition of the master op
//     sequence (so they genuinely diverge before gossip begins).
//   - GOSSIP: seeded-random pairwise merges continue until quiescent or bounded
//     max rounds — modelling real epidemic anti-entropy, not a single global fold.
//   - Two hard assertions every seed:
//       (a) ALL N replicas agree on the SAME Fingerprint after gossip.
//       (b) That fingerprint equals a canonical "truth" replica built by applying
//           the entire op sequence in its original order.
//
// ANTI-BLUFF: the test can only pass vacuously if both assertions are disabled.
//   - Assertion (a) catches a non-commutative / non-deterministic merge:
//     replicas reachable only via different gossip paths would diverge.
//   - Assertion (b) catches a merge that is internally consistent but drops data
//     (e.g. a broken mergePanes that silently ignores absent panes on the
//     receiving side) — the truth replica will contain those entries; gossip
//     result won't.
//
// MUTATION that would break the test (for orchestrator verification):
//   In paneWins (convergence.go), replace the deterministic tie-break
//     return paneContentKey(op) > paneContentKey(cp)
//   with a random / non-deterministic rule such as:
//     return rand.Intn(2) == 0
//   Replicas that received the same equal-version pane collision via different
//   gossip paths would then resolve it differently, producing divergent
//   Fingerprints.  Assertion (a) would FAIL on most seeds.

import (
	"fmt"
	"math/rand"
	"testing"
)

const (
	hxc1162Replicas  = 5   // N >= 3 as required
	hxc1162MasterOps = 100 // total ops in the master sequence
	// MaxRounds: upper bound on gossip SWEEPS (each sweep = N*(N-1) pairwise merges,
	// covering every ordered pair once in seeded-random order).  50 sweeps of 20
	// merges = 1000 individual merge calls — more than enough for N=5 to fully mix.
	hxc1162MaxSweeps = 50
	// QuiesceNeed: how many consecutive full sweeps with no fingerprint change
	// declare convergence.  2 is enough for a CvRDT; we use 3 for safety.
	hxc1162QuiesceNeed = 3
)

// TestHXC1162_MultiNodeGossipConvergence is the headline test for HXC-1162.
// It is table-driven over 20 deterministic seeds so failures are reproducible.
func TestHXC1162_MultiNodeGossipConvergence(t *testing.T) {
	seeds := []int64{
		1, 2, 3, 5, 7, 11, 13, 17, 19, 23,
		29, 31, 37, 41, 43, 47, 53, 59, 61, 67,
	}

	for _, seed := range seeds {
		seed := seed // capture for parallel sub-tests if ever enabled
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			testGossipConvergence(t, seed)
		})
	}
}

// testGossipConvergence runs a single gossip-convergence trial for the given seed.
func testGossipConvergence(t *testing.T, seed int64) {
	t.Helper()

	// -----------------------------------------------------------------------
	// 1. Generate the master op sequence.
	// -----------------------------------------------------------------------
	masterOps := genUpdates(seed, hxc1162MasterOps)

	// -----------------------------------------------------------------------
	// 2. Build a canonical "truth" replica: apply ALL ops in original order.
	//    Assertion (b) compares every replica's post-gossip fingerprint to this.
	// -----------------------------------------------------------------------
	truth := replicaFrom(masterOps)
	truthFP := truth.Fingerprint()

	// -----------------------------------------------------------------------
	// 3. Partition the master ops into N DISJOINT subsets (round-robin by index
	//    so each replica genuinely diverges from the others before gossip).
	// -----------------------------------------------------------------------
	partitions := make([][]buildUpdate, hxc1162Replicas)
	for i := range partitions {
		partitions[i] = make([]buildUpdate, 0, hxc1162MasterOps/hxc1162Replicas+1)
	}
	for i, op := range masterOps {
		bucket := i % hxc1162Replicas
		partitions[bucket] = append(partitions[bucket], op)
	}

	replicas := make([]*CRDTSessionState, hxc1162Replicas)
	for i := range replicas {
		replicas[i] = replicaFrom(partitions[i])
	}

	// -----------------------------------------------------------------------
	// 4. Gossip: seeded-random pairwise SWEEPS until quiescent or max-sweeps.
	//
	//    Each sweep: generate a seeded-random permutation of all N*(N-1)
	//    ordered pairs (src,dst) and execute each merge.  This guarantees every
	//    replica hears from every other within a single sweep — unlike purely
	//    random individual merges that can starve a replica for many rounds.
	//
	//    Quiescence: if fingerprints of ALL replicas are unchanged after a full
	//    sweep, increment the quiesce counter; stop after hxc1162QuiesceNeed
	//    consecutive unchanged sweeps.  This is the correct fixed-point check
	//    for a state-based CvRDT under full-graph gossip.
	// -----------------------------------------------------------------------
	gossipRand := rand.New(rand.NewSource(seed * 31337))

	// Build the ordered-pair list for one sweep.
	allPairs := make([][2]int, 0, hxc1162Replicas*(hxc1162Replicas-1))
	for i := 0; i < hxc1162Replicas; i++ {
		for j := 0; j < hxc1162Replicas; j++ {
			if i != j {
				allPairs = append(allPairs, [2]int{i, j})
			}
		}
	}

	quiesceCount := 0
	prevFPs := fingerprintsOf(replicas)

	for sweep := 0; sweep < hxc1162MaxSweeps; sweep++ {
		// Shuffle the ordered-pair list for this sweep (deterministic).
		gossipRand.Shuffle(len(allPairs), func(i, k int) {
			allPairs[i], allPairs[k] = allPairs[k], allPairs[i]
		})

		for _, pair := range allPairs {
			src, dst := pair[0], pair[1]
			// dst absorbs src via a clone (no aliasing between replicas).
			merged := replicas[dst].Clone()
			if err := merged.Merge(replicas[src]); err != nil {
				t.Fatalf("seed %d sweep %d: merge(%d->%d) error: %v", seed, sweep, src, dst, err)
			}
			replicas[dst] = merged
		}

		// Check quiescence: all fingerprints unchanged since last sweep.
		nowFPs := fingerprintsOf(replicas)
		if fpSlicesEqual(prevFPs, nowFPs) {
			quiesceCount++
			if quiesceCount >= hxc1162QuiesceNeed {
				break // stable for hxc1162QuiesceNeed consecutive sweeps
			}
		} else {
			quiesceCount = 0
			prevFPs = nowFPs
		}
	}

	// -----------------------------------------------------------------------
	// 5. Assertions.
	// -----------------------------------------------------------------------

	// (a) All N replicas must agree on the same fingerprint after gossip.
	fp0 := replicas[0].Fingerprint()
	for i := 1; i < hxc1162Replicas; i++ {
		fpi := replicas[i].Fingerprint()
		if fpi != fp0 {
			t.Fatalf(
				"seed %d: CONVERGENCE FAILED — replica 0 and replica %d diverged after gossip:\n  replica[0]=%s\n  replica[%d]=%s",
				seed, i, fp0, i, fpi,
			)
		}
	}

	// (b) The converged fingerprint must equal the canonical truth replica.
	if fp0 != truthFP {
		t.Fatalf(
			"seed %d: COMPLETENESS FAILED — gossip result differs from truth replica:\n  gossip=%s\n  truth =%s",
			seed, fp0, truthFP,
		)
	}
}

// fingerprintsOf returns a slice of Fingerprint() strings for each replica.
func fingerprintsOf(replicas []*CRDTSessionState) []string {
	fps := make([]string, len(replicas))
	for i, r := range replicas {
		fps[i] = r.Fingerprint()
	}
	return fps
}

// fpSlicesEqual returns true iff two fingerprint slices are element-wise equal.
func fpSlicesEqual(a, b []string) bool {
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
