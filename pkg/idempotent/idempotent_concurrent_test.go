package idempotent

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// This file adds ADVERSARIAL tests for the exactly-once-EFFECT property under
// duplicate and CONCURRENT submission, with anti-bluff teeth and -race.
//
// API note (read from idempotent.go, NOT assumed): this package does not expose
// a Do(key, fn) closure wrapper. The idempotency KEY is the (ProducerID, Seq)
// pair; the EFFECT is "the message is committed to b.log". "Effect ran exactly
// once" is therefore proven by:
//   - exactly ONE Append for a given (ProducerID, Seq) reports Accepted, and
//   - the committed log contains that (ProducerID, Seq) exactly ONCE
//     (countDuplicates == 0), no matter how many duplicate submissions race.
//
// All other duplicate submitters MUST observe AlreadyApplied (the cached/first
// outcome — the effect is NOT re-run for them). This is the concurrent analogue
// of "a retry returns the first result rather than re-executing".

// runConcurrentDuplicates fires N goroutines that each submit the SAME
// (pid, seq) message into b at the same time, released together by a barrier.
// It returns how many submissions observed Accepted, how many AlreadyApplied,
// and how many returned an unexpected error. The atomic effectRuns counter is
// incremented only when Append reports Accepted — i.e. it counts how many times
// the commit "effect" was actually taken.
func runConcurrentDuplicates(b *Broker, pid string, seq, n int) (accepted, alreadyApplied, errs int64, effectRuns int64) {
	var (
		acc, dup, er int64
		runs         int64
		start        = make(chan struct{})
		wg           sync.WaitGroup
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start // barrier: maximize the window where all goroutines race
			res, err := b.Append(Message{ProducerID: pid, Seq: seq, Payload: "effect"})
			switch {
			case err != nil:
				atomic.AddInt64(&er, 1)
			case res == Accepted:
				atomic.AddInt64(&acc, 1)
				atomic.AddInt64(&runs, 1) // the commit effect was taken
			case res == AlreadyApplied:
				atomic.AddInt64(&dup, 1)
			}
		}()
	}
	close(start) // release the barrier — all N race on the same key now
	wg.Wait()
	return acc, dup, er, runs
}

// TestExactlyOnceEffectUnderConcurrentDuplicates is the HEADLINE anti-bluff test.
//
// N goroutines submit the IDENTICAL idempotency key (pid, seq=1) simultaneously.
// The exactly-once-effect property demands:
//   - the committed log contains (pid, 1) EXACTLY ONCE (countDuplicates == 0),
//   - exactly ONE submitter saw Accepted (the effect ran once, not N times),
//   - every other submitter saw AlreadyApplied (got the first/cached outcome).
//
// WHY THIS BITES: a broker that does not truly serialize/dedup the in-flight
// effect will let two or more racers pass the `Seq == expected` branch before
// either advances lastSeq, committing the SAME (pid, seq) two-plus times ->
// countDuplicates > 0 and acceptedCount > 1. A fake/stub that returned
// AlreadyApplied for everyone would commit zero times -> acceptedCount == 0,
// logLen == 0; also caught. The only passing shape is exactly one commit.
//
// -race additionally flags the unsynchronized read-modify-write on b.log /
// b.lastSeq / b.seen even on runs that happen not to double-commit.
func TestExactlyOnceEffectUnderConcurrentDuplicates(t *testing.T) {
	const n = 64
	b := New(1)
	pid := "P-concurrent"

	t.Logf("run=%s clause=exactly-once-effect-concurrent pid=%s n=%d logLenBefore=%d",
		runUUID, pid, n, len(b.Log()))

	accepted, alreadyApplied, errs, effectRuns := runConcurrentDuplicates(b, pid, 1, n)

	log := b.Log()
	dups := countDuplicates(log)

	t.Logf("run=%s accepted=%d alreadyApplied=%d errs=%d effectRuns=%d logLen=%d duplicateCount=%d",
		runUUID, accepted, alreadyApplied, errs, effectRuns, len(log), dups)

	// (1) The effect must have run EXACTLY ONCE.
	if effectRuns != 1 {
		t.Fatalf("exactly-once-effect VIOLATED: effectRuns=%d want 1 (n=%d racers on same key)", effectRuns, n)
	}
	if accepted != 1 {
		t.Fatalf("exactly-once-effect VIOLATED: acceptedCount=%d want 1 (effect ran more than once under concurrency)", accepted)
	}
	// (2) The committed log holds the key exactly once — zero duplicates.
	if dups != 0 {
		t.Fatalf("exactly-once-effect VIOLATED: committed duplicateCount=%d want 0 (log=%v)", dups, log)
	}
	if len(log) != 1 {
		t.Fatalf("exactly-once-effect VIOLATED: logLen=%d want 1 (one commit for one key)", len(log))
	}
	// (3) Every other submitter observed the cached/first outcome, no errors.
	if errs != 0 {
		t.Fatalf("unexpected errors from concurrent duplicates: errs=%d want 0", errs)
	}
	if want := int64(n - 1); alreadyApplied != want {
		t.Fatalf("alreadyApplied=%d want %d (every non-winner must see the first outcome)", alreadyApplied, want)
	}
	// (4) The single committed message is exactly the key we raced on.
	if log[0].ProducerID != pid || log[0].Seq != 1 {
		t.Fatalf("committed wrong key: got {pid=%s seq=%d} want {pid=%s seq=1}", log[0].ProducerID, log[0].Seq, pid)
	}
	if last, ok := b.LastSeq(pid); !ok || last != 1 {
		t.Fatalf("LastSeq(%s)=(%d,%v) want (1,true)", pid, last, ok)
	}
}

// TestExactlyOnceEffectConcurrentRepeated loops the headline race many times on
// a FRESH broker each iteration. Concurrency bugs are probabilistic; a single
// lucky pass proves nothing. If the effect ever runs != 1 time, or a duplicate
// is ever committed, on ANY iteration, the property is broken — fail loudly with
// the iteration index so it is reproducible.
func TestExactlyOnceEffectConcurrentRepeated(t *testing.T) {
	const (
		iters = 200
		n     = 32
	)
	for it := 0; it < iters; it++ {
		b := New(1)
		pid := "P-loop"
		accepted, _, errs, effectRuns := runConcurrentDuplicates(b, pid, 1, n)
		log := b.Log()
		dups := countDuplicates(log)
		if effectRuns != 1 || accepted != 1 || dups != 0 || len(log) != 1 || errs != 0 {
			t.Fatalf("iter=%d exactly-once-effect VIOLATED: effectRuns=%d accepted=%d errs=%d logLen=%d duplicateCount=%d (n=%d)",
				it, effectRuns, accepted, errs, len(log), dups, n)
		}
	}
	t.Logf("run=%s clause=exactly-once-effect-concurrent-repeated iters=%d n=%d allHeldEffectRuns==1", runUUID, iters, n)
}

// TestSequentialDuplicateEffectRunsOnce is the simpler sequential anti-bluff:
// submit the SAME key several times in a row; the effect (commit) must happen
// exactly once and every resubmission must observe AlreadyApplied (the cached
// first outcome), never re-running the effect.
//
// This mirrors a Do(key,fn)-style "fn runs once per key" check, expressed in
// this package's (ProducerID, Seq) model.
func TestSequentialDuplicateEffectRunsOnce(t *testing.T) {
	b := New(1)
	pid := "P-seq-dup"

	const submissions = 8
	var effectRuns int
	for i := 0; i < submissions; i++ {
		res, err := b.Append(Message{ProducerID: pid, Seq: 1, Payload: "effect"})
		if err != nil {
			t.Fatalf("submission %d: unexpected err=%v", i, err)
		}
		switch {
		case i == 0:
			if res != Accepted {
				t.Fatalf("first submission: res=%v want Accepted (effect must run once)", res)
			}
			effectRuns++
		default:
			if res != AlreadyApplied {
				t.Fatalf("submission %d (duplicate): res=%v want AlreadyApplied (effect must NOT re-run)", i, res)
			}
		}
	}

	log := b.Log()
	t.Logf("run=%s clause=sequential-duplicate-effect-once pid=%s submissions=%d effectRuns=%d logLen=%d",
		runUUID, pid, submissions, effectRuns, len(log))

	if effectRuns != 1 {
		t.Fatalf("effectRuns=%d want 1", effectRuns)
	}
	if len(log) != 1 || countDuplicates(log) != 0 {
		t.Fatalf("log must hold the key exactly once: logLen=%d dups=%d (log=%v)", len(log), countDuplicates(log), log)
	}
}

// TestDistinctKeysRunIndependentlyConcurrent proves that DIFFERENT idempotency
// keys each run their effect exactly once and independently, even when all
// submitted concurrently. K distinct producers, each fired by D concurrent
// duplicate submitters of that producer's seq=1: total committed effects MUST
// equal K (one per distinct key), with zero duplicates.
//
// WHY THIS BITES: if dedup were keyed by a constant (or by seq alone, ignoring
// ProducerID), distinct producers' seq=1 messages would collide -> fewer than K
// commits. If dedup failed under concurrency, some producer would commit twice
// -> duplicateCount > 0 and total > K. Only true per-key, exactly-once behavior
// yields exactly K commits and zero duplicates.
func TestDistinctKeysRunIndependentlyConcurrent(t *testing.T) {
	const (
		k = 24 // distinct keys (producers)
		d = 8  // concurrent duplicate submitters per key
	)
	b := New(1)

	pids := make([]string, k)
	for i := range pids {
		pids[i] = "P-distinct-" + string(rune('A'+i%26)) + "-" + itoa(i)
	}

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		gate  = make(chan struct{})
	)
	start.Add(k * d)
	done.Add(k * d)
	var totalEffectRuns int64
	perKeyAccepted := make([]int64, k)

	for ki := 0; ki < k; ki++ {
		for di := 0; di < d; di++ {
			ki := ki
			go func() {
				defer done.Done()
				start.Done()
				<-gate
				res, err := b.Append(Message{ProducerID: pids[ki], Seq: 1, Payload: "effect"})
				if err == nil && res == Accepted {
					atomic.AddInt64(&totalEffectRuns, 1)
					atomic.AddInt64(&perKeyAccepted[ki], 1)
				}
			}()
		}
	}
	start.Wait() // all goroutines parked on the gate
	close(gate)  // release everyone at once
	done.Wait()

	log := b.Log()
	dups := countDuplicates(log)

	t.Logf("run=%s clause=distinct-keys-concurrent k=%d d=%d totalEffectRuns=%d logLen=%d duplicateCount=%d",
		runUUID, k, d, totalEffectRuns, len(log), dups)

	if totalEffectRuns != int64(k) {
		t.Fatalf("distinct-keys exactly-once VIOLATED: totalEffectRuns=%d want %d", totalEffectRuns, k)
	}
	if len(log) != k {
		t.Fatalf("distinct-keys exactly-once VIOLATED: logLen=%d want %d", len(log), k)
	}
	if dups != 0 {
		t.Fatalf("distinct-keys exactly-once VIOLATED: duplicateCount=%d want 0", dups)
	}
	for ki := 0; ki < k; ki++ {
		if perKeyAccepted[ki] != 1 {
			t.Fatalf("key %q ran its effect %d times, want exactly 1", pids[ki], perKeyAccepted[ki])
		}
	}
}

// itoa is a tiny dependency-free int->string for building distinct pids.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// TestErrorOutcomeNotCachedAsCommit documents the package's ACTUAL error
// behavior (read from idempotent.go, not assumed): an out-of-sequence GAP
// returns ErrOutOfSequence and does NOT commit; crucially it does NOT poison the
// key — re-submitting the SAME seq in order later still runs the effect exactly
// once. I.e. an errored (rejected) submission is NOT cached as "already done".
//
// There is no fn-error path in this model (the "effect" is a deterministic
// commit, not a user closure that can fail), so the closest real error-handling
// property is gap-rejection-without-poisoning, asserted here.
func TestErrorOutcomeNotCachedAsCommit(t *testing.T) {
	b := New(1)
	pid := "P-err"

	// Gap: seq 2 before seq 1 -> rejected, nothing committed, watermark unmoved.
	res, err := b.Append(Message{ProducerID: pid, Seq: 2, Payload: "gap"})
	if !errors.Is(err, ErrOutOfSequence) {
		t.Fatalf("gap seq=2: err=%v want ErrOutOfSequence", err)
	}
	if res == Accepted {
		t.Fatalf("gap seq=2: result=Accepted, must not commit")
	}
	if len(b.Log()) != 0 {
		t.Fatalf("gap committed a message: log=%v", b.Log())
	}
	if _, ok := b.LastSeq(pid); ok {
		t.Fatalf("gap advanced/created watermark; LastSeq must report ok=false")
	}

	// The rejected seq=2 must NOT be remembered as done: deliver 1 then 2 in
	// order, both run their effect exactly once.
	if res, err := b.Append(Message{ProducerID: pid, Seq: 1, Payload: "a"}); err != nil || res != Accepted {
		t.Fatalf("seq 1: res=%v err=%v want Accepted,nil", res, err)
	}
	if res, err := b.Append(Message{ProducerID: pid, Seq: 2, Payload: "b"}); err != nil || res != Accepted {
		t.Fatalf("seq 2 (after fill): res=%v err=%v want Accepted,nil (rejected gap must not poison the key)", res, err)
	}

	log := b.Log()
	if len(log) != 2 || countDuplicates(log) != 0 {
		t.Fatalf("final log must be seqs 1,2 once each: logLen=%d dups=%d log=%v", len(log), countDuplicates(log), log)
	}
	t.Logf("run=%s clause=error-not-cached-as-commit gapErr=%v finalLogLen=%d", runUUID, err, len(log))
}
