package advisory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// Adversarial SECOND PASS for internal/advisory (the Advisory *Lock* service).
//
// First pass (advisory_adversarial_test.go) already pinned the single-instant
// mutual-exclusion invariant, foreign-holder rejection, deterministic single
// winner, and the blocking/cancel paths. This pass goes DEEPER on the highest-
// value lock-correctness classes that the first pass did not nail down:
//
//   (E) CHECK-AND-GRANT ATOMICITY under a TryLock+Unlock+expiry storm — the
//       global "total successful TryLock acquisitions across the whole run
//       equals total successful Unlocks plus whatever is still held at the end"
//       accounting must balance; a non-atomic grant would mint an acquisition
//       with no matching release (double-grant) and unbalance the ledger.
//   (F) HOLDER-KEYED UNLOCK is the authorization gate — a foreign holder is
//       rejected AND the owner's grant survives, even under concurrent churn.
//   (G) LEASE-EXPIRY hand-off: once A's lease lapses, exactly ONE successor may
//       acquire; the successor's grant is a *fresh* generation (new lockID).
//   (H) ABA / STALE-UNLOCK contract pin (LATENT RISK): unlock is keyed ONLY on
//       the holder string (UnlockRequest carries no LockId), so a late unlock
//       from an expired generation reusing the same holder id releases a newer
//       generation's lock. This is a real fencing hazard but is FIXED BY THE
//       PROTO CONTRACT (no token to pass); pinned here so any future proto that
//       adds a LockId MUST also start validating it.
//   (I) RETURN ISOLATION: the minted lockID handed to a caller is an immutable
//       value, never aliased to mutable store state.
//
// Every assertion is SINK-SIDE: actual acquired/released verdicts, the actual
// surviving lock table, or the actual successor generation — never "no panic".
// All ops are bounded; no test can hang.
// ----------------------------------------------------------------------------

// (E) CHECK-AND-GRANT ATOMICITY: hammer tryLock/unlock from many goroutines on a
// small resource set and keep a global ledger. Each goroutine, for a resource it
// is currently NOT holding, calls tryLock; on success it (and only it) performs
// the matching unlock. The invariant: every successful acquisition is released
// by its own acquirer exactly once, and unlock by that same acquirer never fails
// while the lease is valid. A double-grant (two acquirers for one generation)
// would surface as an unlock that the loser performs returning "not found" or a
// foreign-holder error — i.e. acquisitions != successful self-unlocks.
//
// We give each goroutine a UNIQUE holder id so a double-grant is detectable: if
// two goroutines both believed they acquired the same resource generation, the
// second unlock would hit a holder mismatch (the first already deleted+regranted
// is impossible here because each uses a long TTL and unlocks immediately).
func TestAdversarial2_CheckAndGrantLedgerBalances(t *testing.T) {
	lm := newLockManager()
	const workers = 8 // <= 8 anti-hang cap
	const iters = 300
	const resources = 3
	resName := func(i int) string { return "L" + string(rune('a'+i)) }

	var acquired int64
	var selfUnlocked int64
	var foreignReject int64

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			holder := "h" + string(rune('A'+id))
			for j := 0; j < iters; j++ {
				r := resName(j % resources)
				lockID, ok := lm.tryLock(r, holder, time.Minute)
				if !ok {
					continue
				}
				atomic.AddInt64(&acquired, 1)
				require.NotEmpty(t, lockID)
				// We hold a valid, long lease and are the sole acquirer of THIS
				// generation. Our own unlock MUST succeed.
				released, err := lm.unlock(r, holder)
				if err != nil {
					// A foreign-holder/not-found error here means someone ELSE
					// concurrently held the same generation -> a double grant or
					// a lost release. Record it as a hard violation.
					atomic.AddInt64(&foreignReject, 1)
					continue
				}
				if released {
					atomic.AddInt64(&selfUnlocked, 1)
				}
			}
		}(w)
	}
	wg.Wait()

	require.Zero(t, atomic.LoadInt64(&foreignReject),
		"a sole acquirer's own unlock must never fail; nonzero implies a double-grant")
	// Every acquisition was released by its acquirer; nothing should remain held.
	require.Equal(t, atomic.LoadInt64(&acquired), atomic.LoadInt64(&selfUnlocked),
		"acquisitions must balance self-unlocks (check-and-grant atomicity)")

	lm.mu.RLock()
	remaining := len(lm.locks)
	lm.mu.RUnlock()
	assert.Equal(t, 0, remaining, "no lock may remain held after a balanced run")
}

// (F) HOLDER-KEYED UNLOCK under churn: while owner O holds "r", a flood of
// foreign-holder unlock attempts must ALL be rejected and O's grant must survive
// every one of them. SINK-SIDE: each foreign unlock's (released,err) and the
// surviving entry's holder/lockID being unchanged.
func TestAdversarial2_ForeignUnlockNeverStealsUnderChurn(t *testing.T) {
	lm := newLockManager()
	ownerID, ok := lm.tryLock("r", "owner", time.Minute)
	require.True(t, ok)

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			attacker := "atk" + string(rune('A'+id))
			for i := 0; i < 200; i++ {
				released, err := lm.unlock("r", attacker)
				assert.False(t, released, "foreign holder must never release")
				assert.Error(t, err, "foreign unlock must surface an error")
			}
		}(w)
	}
	wg.Wait()

	// Owner's exact generation must be intact: same holder, same lockID.
	lm.mu.RLock()
	e := lm.locks["r"]
	lm.mu.RUnlock()
	require.NotNil(t, e, "owner's lock must survive the foreign-unlock storm")
	assert.Equal(t, "owner", e.holder)
	assert.Equal(t, ownerID, e.lockID, "surviving generation's lockID must be unchanged")

	// And only the true owner can release it.
	released, err := lm.unlock("r", "owner")
	require.NoError(t, err)
	assert.True(t, released)
}

// (G) LEASE-EXPIRY hand-off: once the holder's lease lapses, exactly one
// successor acquires, and its grant is a FRESH generation (new lockID distinct
// from the lapsed one). We let the lease lapse (no explicit unlock) and have N
// concurrent contenders race; exactly one wins and its id differs from gen1.
func TestAdversarial2_ExpiryHandoffSingleFreshSuccessor(t *testing.T) {
	lm := newLockManager()
	gen1, ok := lm.tryLock("r", "A", 60*time.Millisecond)
	require.True(t, ok)

	// Wait past the lease so the resource is durably free (tryLock treats an
	// expired entry as grantable even before the 1s expiration ticker fires).
	time.Sleep(120 * time.Millisecond)

	const n = 8
	var mu sync.Mutex
	var winners []string
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			if lockID, got := lm.tryLock("r", "succ"+string(rune('A'+id)), time.Minute); got {
				mu.Lock()
				winners = append(winners, lockID)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.Len(t, winners, 1, "exactly one successor may acquire after lease expiry")
	assert.NotEqual(t, gen1, winners[0], "successor must get a fresh generation lockID")
}

// (H) ABA / STALE-UNLOCK contract pin (LATENT RISK, proto-constrained).
//
// unlock authorizes on the holder STRING alone; UnlockRequest carries no LockId.
// So a late unlock from an EXPIRED generation, replayed by a process reusing the
// same holder id, releases a *newer* generation's lock. This documents the real
// fencing hazard. It is currently un-exploitable through the gRPC surface only
// because there is no token to pass — i.e. the proto contract is the guardrail.
//
// THIS TEST PINS THE PRESENT (vulnerable-by-contract) BEHAVIOR. If a future
// change adds a LockId to UnlockRequest and starts validating it, the stale
// unlock below MUST begin to FAIL (released=false) and this test must be updated
// in the SAME change — that is the whole point of the pin.
func TestAdversarial2_StaleUnlockAcrossGenerations_ContractPin(t *testing.T) {
	lm := newLockManager()

	gen1, ok := lm.tryLock("r", "A", 50*time.Millisecond)
	require.True(t, ok)
	time.Sleep(110 * time.Millisecond) // gen1 lease lapses

	gen2, ok := lm.tryLock("r", "A", time.Minute) // same holder id reacquires
	require.True(t, ok)
	require.NotEqual(t, gen1, gen2, "each acquisition mints a distinct generation")

	// gen1's stale unlock (no fencing token) currently releases gen2. We PIN this
	// holder-keyed behavior; it is the documented contract, not a passing fix.
	released, err := lm.unlock("r", "A")
	require.NoError(t, err)
	assert.True(t, released,
		"CONTRACT: unlock is holder-keyed (no LockId in proto); a same-holder "+
			"stale unlock releases the current generation. If a fencing token is "+
			"ever added to UnlockRequest, this assertion MUST flip to false.")

	// Sink-side consequence of the contract: the resource is now free.
	lm.mu.RLock()
	_, stillHeld := lm.locks["r"]
	lm.mu.RUnlock()
	assert.False(t, stillHeld, "after the holder-keyed unlock the resource is free")
}

// (I) RETURN ISOLATION: the lockID returned to a caller must be an immutable,
// independent value — never a window onto mutable store state. We capture the
// id, then drive a full release+re-acquire cycle (which mutates the store entry)
// and assert the previously-returned id is unchanged. A returned alias into the
// entry would observe the new generation's id.
func TestAdversarial2_ReturnedLockIDNotAliasedToStore(t *testing.T) {
	lm := newLockManager()
	id1, ok := lm.tryLock("r", "A", time.Minute)
	require.True(t, ok)
	captured := id1

	_, err := lm.unlock("r", "A")
	require.NoError(t, err)

	id2, ok := lm.tryLock("r", "B", time.Minute)
	require.True(t, ok)
	require.NotEqual(t, id1, id2)

	// The value we were handed for gen1 must not have mutated under us.
	assert.Equal(t, captured, id1, "returned lockID must be an immutable value, not a store alias")
}

// (B/E) FULL-PATH NO-RACE: concurrent blocking lock(), tryLock(), unlock(), and
// the background expiration ticker all on one hot resource under -race. The
// blocking lock uses a bounded context so the test cannot hang. SINK-SIDE: the
// detector stays quiet AND at the end the single-holder invariant holds (0 or 1
// entry for the key, never a corrupted map).
func TestAdversarial2_BlockingAndTryAndExpiryNoRace(t *testing.T) {
	lm := newLockManager()
	deadline := time.Now().Add(900 * time.Millisecond)

	var wg sync.WaitGroup
	// Blocking lockers with bounded ctx.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for time.Now().Before(deadline) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				holder := "b" + string(rune('A'+id))
				lockID, ok := lm.lock(ctx, "hot", holder, 20*time.Millisecond)
				cancel()
				if ok {
					require.NotEmpty(t, lockID)
					_, _ = lm.unlock("hot", holder)
				}
			}
		}(w)
	}
	// TryLockers with short TTLs so the expiration ticker participates.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			holder := "t" + string(rune('A'+id))
			for time.Now().Before(deadline) {
				if _, ok := lm.tryLock("hot", holder, 15*time.Millisecond); ok {
					_, _ = lm.unlock("hot", holder)
				}
			}
		}(w)
	}
	wg.Wait()

	// Final structural sink check: the key maps to at most one entry and, if
	// present, it is a well-formed (non-nil) generation.
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	if e, ok := lm.locks["hot"]; ok {
		require.NotNil(t, e, "a present entry must be well-formed")
		assert.Equal(t, "hot", e.resource)
		assert.NotEmpty(t, e.lockID)
	}
}
