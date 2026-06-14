package idempotent

// Adversarial probe suite for pkg/idempotent (HXC tonight: unguarded shared
// mutable state + dedup correctness). These tests are DISTINCT from
// idempotent_concurrent_test.go: they bind the "effect" to an EXTERNAL guarded
// side-effect (a per-key counter mutated INSIDE the accepted branch's caller)
// so a double-commit is observable sink-side even if the in-broker log were
// somehow deduped after the fact; they push goroutine counts much higher; and
// they cover identity edges (empty key, cross-pid same-Seq collision) and the
// numeric base/watermark boundary that an off-by-one in expected() would breach.
//
// Contract under test (read verbatim from idempotent.go, NOT assumed):
//   - Broker is documented "safe for concurrent use" and the exactly-once EFFECT
//     ("the message is committed to b.log exactly once") is the WHOLE point.
//   - The KEY is (ProducerID, Seq). Seq == lastSeq+1 -> Accepted (commit once);
//     Seq <= lastSeq -> AlreadyApplied (no re-commit); Seq > lastSeq+1 -> gap err.
//   - There is NO time/TTL expiry in this model; the only "expiry boundary"
//     analogue is the numeric base / lastSeq+1 watermark edge, probed in (c).
//
// ANTI-BLUFF: every concurrent test asserts an EXACT execution count == 1 (or a
// conservation equality), never merely "no panic". -race is required to catch
// the unsynchronized map/slice RMW even on runs that don't happen to double-apply.

import (
	"sync"
	"sync/atomic"
	"testing"
)

// keyedEffect models a real end-user side-effect guarded by the broker's
// exactly-once decision: the caller increments the per-key counter ONLY when the
// broker reports Accepted. Under a correct broker each (pid,seq) drives exactly
// one increment no matter how many racers submit it. Under a TOCTOU gap (two
// racers both pass Seq==expected before either advances lastSeq) the counter
// would reach 2+, AND the committed log would carry the key 2+ times.
type keyedEffect struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newKeyedEffect() *keyedEffect { return &keyedEffect{counts: make(map[string]int64)} }

func (k *keyedEffect) fire(key string) {
	k.mu.Lock()
	k.counts[key]++
	k.mu.Unlock()
}

func (k *keyedEffect) get(key string) int64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.counts[key]
}

// (a) CONCURRENT FIRST-USE OF ONE KEY — the headline TOCTOU probe, 256 racers.
//
// 256 goroutines present the SAME, never-before-seen (pid, seq=1) at once,
// released by a single barrier. The guarded effect MUST fire EXACTLY ONCE.
//
// WHY IT BITES: with a check-then-store that is not atomic under one lock, two
// goroutines both read expected==1, both take the Accepted branch, both append
// and both increment -> effectCount==2 and committed duplicateCount>0. A stub
// that AlreadyApplied'd everyone -> effectCount==0, logLen==0. Only a real
// atomic check-and-set yields exactly one.
func TestConcurrentFirstUseSingleKeyFiresEffectExactlyOnce(t *testing.T) {
	const n = 256
	b := New(1)
	eff := newKeyedEffect()
	const pid = "P-first-use"

	var (
		accepted int64
		start    = make(chan struct{})
		wg       sync.WaitGroup
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			res, err := b.Append(Message{ProducerID: pid, Seq: 1, Payload: "effect"})
			if err == nil && res == Accepted {
				atomic.AddInt64(&accepted, 1)
				eff.fire(pid) // external sink-side effect, guarded by Accepted
			}
		}()
	}
	close(start)
	wg.Wait()

	log := b.Log()
	dups := countDuplicates(log)
	got := eff.get(pid)
	t.Logf("run=%s clause=concurrent-first-use n=%d acceptedBranch=%d externalEffect=%d logLen=%d duplicateCount=%d",
		runUUID, n, accepted, got, len(log), dups)

	if got != 1 {
		t.Fatalf("exactly-once VIOLATED (sink-side): external effect fired %d times, want 1 (n=%d racers on one fresh key)", got, n)
	}
	if accepted != 1 {
		t.Fatalf("exactly-once VIOLATED: Accepted branch taken %d times, want 1", accepted)
	}
	if len(log) != 1 || dups != 0 {
		t.Fatalf("exactly-once VIOLATED: logLen=%d duplicateCount=%d, want logLen=1 dups=0 (log=%v)", len(log), dups, log)
	}
}

// (b) CONSERVATION under mixed concurrent load: many distinct keys, each pounded
// by many duplicate racers, all at once. The conserved quantity is:
//
//	sum(externalEffectPerKey) == numberOfDistinctKeys == len(committedLog)
//
// and every per-key effect count is EXACTLY 1. A lost update (unguarded map
// write dropping an Accepted) breaks the lower bound; a double-apply breaks the
// upper bound. -race catches the raw map RMW regardless.
func TestConservationUnderMixedConcurrentLoad(t *testing.T) {
	const (
		keys      = 64
		dupRacers = 12
	)
	b := New(1)
	eff := newKeyedEffect()

	pids := make([]string, keys)
	for i := range pids {
		pids[i] = "P-cons-" + itoa(i)
	}

	var (
		gate  = make(chan struct{})
		wg    sync.WaitGroup
		total int64
	)
	wg.Add(keys * dupRacers)
	for ki := 0; ki < keys; ki++ {
		for r := 0; r < dupRacers; r++ {
			ki := ki
			go func() {
				defer wg.Done()
				<-gate
				res, err := b.Append(Message{ProducerID: pids[ki], Seq: 1, Payload: "effect"})
				if err == nil && res == Accepted {
					atomic.AddInt64(&total, 1)
					eff.fire(pids[ki])
				}
			}()
		}
	}
	close(gate)
	wg.Wait()

	log := b.Log()
	dups := countDuplicates(log)
	t.Logf("run=%s clause=conservation keys=%d dupRacers=%d totalAccepted=%d logLen=%d duplicateCount=%d",
		runUUID, keys, dupRacers, total, len(log), dups)

	if total != int64(keys) {
		t.Fatalf("conservation VIOLATED: totalAccepted=%d want %d (lost update or double-apply)", total, keys)
	}
	if len(log) != keys {
		t.Fatalf("conservation VIOLATED: logLen=%d want %d", len(log), keys)
	}
	if dups != 0 {
		t.Fatalf("conservation VIOLATED: committed duplicateCount=%d want 0", dups)
	}
	for ki := 0; ki < keys; ki++ {
		if c := eff.get(pids[ki]); c != 1 {
			t.Fatalf("key %q external effect fired %d times, want exactly 1", pids[ki], c)
		}
	}
}

// (c) NUMERIC EDGE on the base / lastSeq+1 watermark — the closest analogue to a
// TTL/expiry boundary in this clockless model. We pin the EXACT comparison
// boundaries so an off-by-one (e.g. `Seq < exp` mistyped as `Seq <= exp`, or
// `Seq == exp` drifting) would admit a duplicate or reject a valid first-use.
//
//   - At a fresh producer, expected == base. base-1 must be AlreadyApplied (pre-
//     base, no commit); base must be Accepted; base again must be AlreadyApplied.
//   - After committing base, expected == base+1. Re-presenting base (== lastSeq,
//     the boundary value) must be AlreadyApplied, NOT a second commit.
func TestWatermarkBoundaryNoDuplicateAdmission(t *testing.T) {
	const base = 5
	b := New(base)
	const pid = "P-edge"

	// base-1: strictly below base -> pre-base duplicate, no commit, no watermark.
	if res, err := b.Append(Message{ProducerID: pid, Seq: base - 1}); err != nil || res != AlreadyApplied {
		t.Fatalf("seq=base-1=%d: res=%v err=%v want AlreadyApplied,nil", base-1, res, err)
	}
	if _, ok := b.LastSeq(pid); ok {
		t.Fatalf("pre-base submission must not create a watermark")
	}

	// base: exactly expected -> Accepted, commit once.
	if res, err := b.Append(Message{ProducerID: pid, Seq: base}); err != nil || res != Accepted {
		t.Fatalf("seq=base=%d: res=%v err=%v want Accepted,nil", base, res, err)
	}

	// base again: now == lastSeq (the boundary). Must be AlreadyApplied, NOT a
	// second commit. This is the exact value an off-by-one in the `<` guard would
	// wrongly re-admit.
	if res, err := b.Append(Message{ProducerID: pid, Seq: base}); err != nil || res != AlreadyApplied {
		t.Fatalf("seq=base=%d (replay at watermark): res=%v err=%v want AlreadyApplied,nil (must not double-commit)", base, res, err)
	}

	log := b.Log()
	if len(log) != 1 || countDuplicates(log) != 0 {
		t.Fatalf("watermark boundary VIOLATED: logLen=%d dups=%d want 1,0 (log=%v)", len(log), countDuplicates(log), log)
	}
	if last, ok := b.LastSeq(pid); !ok || last != base {
		t.Fatalf("LastSeq=(%d,%v) want (%d,true)", last, ok, base)
	}
	t.Logf("run=%s clause=watermark-boundary base=%d firstCommit=%d replayAtWatermark=AlreadyApplied logLen=%d",
		runUUID, base, base, len(log))
}

// (d-1) IDENTITY: distinct keys that an over-eager dedup might COLLIDE. Two
// different producers each present Seq=1; the SAME numeric Seq must NOT be
// deduped across producers. Both commit; total == 2; neither sees the other's.
// (If dedup keyed on Seq alone, the second would be a false AlreadyApplied.)
func TestSameSeqDistinctProducersDoNotCollide(t *testing.T) {
	b := New(1)
	const pa, pb = "P-collide-a", "P-collide-b"

	if res, err := b.Append(Message{ProducerID: pa, Seq: 1, Payload: "a"}); err != nil || res != Accepted {
		t.Fatalf("pa seq=1: res=%v err=%v want Accepted,nil", res, err)
	}
	if res, err := b.Append(Message{ProducerID: pb, Seq: 1, Payload: "b"}); err != nil || res != Accepted {
		t.Fatalf("pb seq=1: res=%v err=%v want Accepted,nil (same Seq, different producer must NOT collide)", res, err)
	}
	log := b.Log()
	if len(log) != 2 || countDuplicates(log) != 0 {
		t.Fatalf("identity VIOLATED: logLen=%d dups=%d want 2,0 (cross-pid same-Seq collision) log=%v", len(log), countDuplicates(log), log)
	}
	t.Logf("run=%s clause=identity-no-cross-pid-collision logLen=%d", runUUID, len(log))
}

// (d-2) IDENTITY: the EMPTY ProducerID is a legitimate, distinct key (Go map
// keys "" are first-class). It must dedup against itself like any other key and
// must NOT be conflated with a non-empty producer. Concurrent first-use of the
// empty key must still fire its effect exactly once.
func TestEmptyProducerIDIsADistinctDedupedKey(t *testing.T) {
	const n = 64
	b := New(1)
	eff := newKeyedEffect()

	// Also run a non-empty producer's seq=1 concurrently to prove "" is not
	// conflated with it: both keys must commit exactly once.
	const other = "P-nonempty"

	var (
		gate            = make(chan struct{})
		wg              sync.WaitGroup
		emptyAcc, othAc int64
	)
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-gate
			if res, err := b.Append(Message{ProducerID: "", Seq: 1, Payload: "empty"}); err == nil && res == Accepted {
				atomic.AddInt64(&emptyAcc, 1)
				eff.fire("")
			}
		}()
		go func() {
			defer wg.Done()
			<-gate
			if res, err := b.Append(Message{ProducerID: other, Seq: 1, Payload: "other"}); err == nil && res == Accepted {
				atomic.AddInt64(&othAc, 1)
				eff.fire(other)
			}
		}()
	}
	close(gate)
	wg.Wait()

	log := b.Log()
	dups := countDuplicates(log)
	t.Logf("run=%s clause=empty-key-distinct n=%d emptyAccepted=%d otherAccepted=%d emptyEffect=%d otherEffect=%d logLen=%d dups=%d",
		runUUID, n, emptyAcc, othAc, eff.get(""), eff.get(other), len(log), dups)

	if emptyAcc != 1 || eff.get("") != 1 {
		t.Fatalf("empty key exactly-once VIOLATED: acceptedBranch=%d externalEffect=%d want 1,1", emptyAcc, eff.get(""))
	}
	if othAc != 1 || eff.get(other) != 1 {
		t.Fatalf("non-empty key exactly-once VIOLATED: acceptedBranch=%d externalEffect=%d want 1,1", othAc, eff.get(other))
	}
	if len(log) != 2 || dups != 0 {
		t.Fatalf("identity VIOLATED: logLen=%d dups=%d want 2,0 (empty and non-empty are distinct keys) log=%v", len(log), dups, log)
	}
	// Confirm both watermarks exist independently.
	if last, ok := b.LastSeq(""); !ok || last != 1 {
		t.Fatalf("LastSeq(\"\")=(%d,%v) want (1,true)", last, ok)
	}
	if last, ok := b.LastSeq(other); !ok || last != 1 {
		t.Fatalf("LastSeq(%q)=(%d,%v) want (1,true)", other, last, ok)
	}
}
