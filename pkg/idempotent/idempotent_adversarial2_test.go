package idempotent

// Adversarial probe suite #2 for pkg/idempotent. This file is DISTINCT from the
// existing adversarial/concurrent suites: those hammer the (pid, seq=1) TOCTOU
// at the LOW end of the Seq space and assert exactly-once. This file attacks the
// parts of the contract those suites never reach:
//
//   (A) the NUMERIC OVERFLOW BOUNDARY at the watermark (lastSeq == math.MaxInt).
//       The package doc is explicit: "Seq <= lastSeq -> DUPLICATE/retry:
//       idempotent no-op. The message is NOT appended again and AlreadyApplied is
//       returned (nil error)." A naive `expected = lastSeq+1` overflows to
//       math.MinInt at MaxInt, which (1) misclassifies a RETRY of the
//       already-committed MaxInt message as an out-of-sequence GAP (wrong Result
//       AND a spurious non-nil error), and (2) ADMITS a brand-new math.MinInt
//       message as the "next expected" Seq — a phantom commit far below base.
//       Both are exactly-once / dedup violations of a correctness primitive.
//
//   (B) RESULT-COPY ISOLATION: Log()/LogFor() promise "a copy ... so callers
//       cannot mutate broker state". We mutate a returned Message's Payload and
//       prove a subsequent read is unaffected (no aliasing of the backing array).
//
//   (C) a concurrent storm at the overflow boundary, under the single -race run,
//       proving the retry-of-MaxInt is deduped exactly once with nil error even
//       when many racers submit it at once.
//
// ANTI-BLUFF: (A) and (C) assert the EXACT (Result, error) pair and the committed
// log shape, so a regression to the overflowing `expected()` fails loudly. These
// are mutation-proven: reverting Append to `exp := b.expected(...); switch{Seq==exp
// ...}` flips (A)/(C) to FAIL.

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// (A) OVERFLOW BOUNDARY — the headline correctness probe.
//
// Build a broker whose base is math.MaxInt, accept the MaxInt message (watermark
// := MaxInt), then:
//   - re-submit MaxInt (a RETRY of the committed message): MUST be
//     (AlreadyApplied, nil) — a duplicate, NOT a gap, NOT an error.
//   - submit math.MinInt (a never-seen Seq far below base): MUST NOT be accepted
//     and MUST NOT be committed. With the overflow bug it is wrongly Accepted
//     because expected() wrapped to MinInt.
//
// The committed log MUST hold exactly the one MaxInt message, zero duplicates.
func TestWatermarkOverflowRetryIsDuplicateNotGap(t *testing.T) {
	b := New(math.MaxInt)
	const pid = "P-overflow"

	// Accept the MaxInt first-use (expected == base == MaxInt).
	if res, err := b.Append(Message{ProducerID: pid, Seq: math.MaxInt, Payload: "x"}); err != nil || res != Accepted {
		t.Fatalf("first MaxInt: res=%v err=%v want Accepted,nil", res, err)
	}
	if last, ok := b.LastSeq(pid); !ok || last != math.MaxInt {
		t.Fatalf("LastSeq=(%d,%v) want (MaxInt,true)", last, ok)
	}

	// RETRY of the committed MaxInt: Seq == lastSeq -> per contract AlreadyApplied
	// with NIL error. The overflow bug instead returns ErrOutOfSequence here.
	res, err := b.Append(Message{ProducerID: pid, Seq: math.MaxInt, Payload: "retry"})
	t.Logf("run=%s clause=overflow-retry res=%v err=%v", runUUID, res, err)
	if res != AlreadyApplied || err != nil {
		t.Fatalf("retry of committed MaxInt: res=%v err=%v want AlreadyApplied,nil "+
			"(a retry of an already-committed Seq must be an idempotent no-op, NOT a gap)", res, err)
	}

	// A phantom math.MinInt message must NOT be admitted. With the overflow bug
	// expected() == MinInt so this is wrongly Accepted (a commit below base).
	resMin, errMin := b.Append(Message{ProducerID: pid, Seq: math.MinInt, Payload: "phantom"})
	t.Logf("run=%s clause=overflow-phantom-min res=%v err=%v", runUUID, resMin, errMin)
	if resMin == Accepted {
		t.Fatalf("math.MinInt phantom: res=Accepted — a never-seen Seq far below base "+
			"was admitted (watermark overflow). err=%v", errMin)
	}

	log := b.Log()
	if len(log) != 1 || countDuplicates(log) != 0 {
		t.Fatalf("overflow boundary VIOLATED: logLen=%d dups=%d want 1,0 (log=%v)",
			len(log), countDuplicates(log), log)
	}
	if log[0].Seq != math.MaxInt || log[0].Payload != "x" {
		t.Fatalf("committed wrong message: got {seq=%d payload=%q} want {seq=MaxInt payload=%q}",
			log[0].Seq, log[0].Payload, "x")
	}
}

// (A') The successor of a NON-MaxInt watermark must still be accepted — proves the
// overflow-safe `msg.Seq-1 == last` reformulation didn't break the ordinary
// in-order accept path near a large (but non-overflowing) watermark.
func TestLargeWatermarkSuccessorStillAccepted(t *testing.T) {
	const base = math.MaxInt - 2
	b := New(base)
	const pid = "P-near-max"

	for i, seq := range []int{base, base + 1, math.MaxInt} {
		if res, err := b.Append(Message{ProducerID: pid, Seq: seq, Payload: "y"}); err != nil || res != Accepted {
			t.Fatalf("step %d seq=%d: res=%v err=%v want Accepted,nil", i, seq, res, err)
		}
	}
	// Now watermark == MaxInt; a retry of MaxInt is a duplicate (nil err).
	if res, err := b.Append(Message{ProducerID: pid, Seq: math.MaxInt}); err != nil || res != AlreadyApplied {
		t.Fatalf("retry MaxInt after climb: res=%v err=%v want AlreadyApplied,nil", res, err)
	}
	log := b.Log()
	if len(log) != 3 || countDuplicates(log) != 0 {
		t.Fatalf("near-max climb VIOLATED: logLen=%d dups=%d want 3,0 (log=%v)",
			len(log), countDuplicates(log), log)
	}
	t.Logf("run=%s clause=near-max-climb base=%d logLen=%d", runUUID, base, len(log))
}

// (B) RESULT-COPY ISOLATION: mutating a Message returned by Log()/LogFor() must
// not bleed into broker state or into a later read. Proves Log() hands back an
// independent copy (the doc'd guarantee), not an alias of b.log.
func TestReturnedLogIsIsolatedCopy(t *testing.T) {
	b := New(1)
	const pid = "P-iso"
	if res, err := b.Append(Message{ProducerID: pid, Seq: 1, Payload: "original"}); err != nil || res != Accepted {
		t.Fatalf("seq=1: res=%v err=%v want Accepted,nil", res, err)
	}

	first := b.Log()
	if len(first) != 1 {
		t.Fatalf("logLen=%d want 1", len(first))
	}
	// Mutate the returned copy aggressively.
	first[0].Payload = "TAMPERED"
	first[0].Seq = 999
	first[0].ProducerID = "HIJACK"

	// A fresh read must reflect the ORIGINAL committed message, untouched.
	second := b.Log()
	if len(second) != 1 {
		t.Fatalf("second read logLen=%d want 1", len(second))
	}
	if second[0].Payload != "original" || second[0].Seq != 1 || second[0].ProducerID != pid {
		t.Fatalf("Log() aliasing VIOLATED: broker state mutated via returned slice: got %+v", second[0])
	}
	// LogFor likewise.
	lf := b.LogFor(pid)
	if len(lf) != 1 || lf[0].Payload != "original" || lf[0].Seq != 1 {
		t.Fatalf("LogFor() aliasing VIOLATED: got %+v", lf)
	}
	// And the watermark is unmoved by the tampering.
	if last, ok := b.LastSeq(pid); !ok || last != 1 {
		t.Fatalf("LastSeq=(%d,%v) want (1,true)", last, ok)
	}
	t.Logf("run=%s clause=log-copy-isolation tamperedRejected=true", runUUID)
}

// (C) CONCURRENT RETRY AT THE OVERFLOW BOUNDARY (single -race run).
//
// Accept MaxInt once, then fire N racers that ALL re-submit MaxInt at once. Every
// one MUST observe (AlreadyApplied, nil) — none may see Accepted (no second
// commit) and none may see an error (no spurious gap). The committed log stays at
// exactly one MaxInt message. -race also flags any unsynchronized access.
func TestConcurrentRetryAtOverflowBoundaryAllDeduped(t *testing.T) {
	const n = 128
	b := New(math.MaxInt)
	const pid = "P-overflow-conc"

	if res, err := b.Append(Message{ProducerID: pid, Seq: math.MaxInt, Payload: "seed"}); err != nil || res != Accepted {
		t.Fatalf("seed MaxInt: res=%v err=%v want Accepted,nil", res, err)
	}

	var (
		dup, acc, errs int64
		start          = make(chan struct{})
		wg             sync.WaitGroup
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			res, err := b.Append(Message{ProducerID: pid, Seq: math.MaxInt, Payload: "retry"})
			switch {
			case err != nil:
				atomic.AddInt64(&errs, 1)
			case res == Accepted:
				atomic.AddInt64(&acc, 1)
			case res == AlreadyApplied:
				atomic.AddInt64(&dup, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	log := b.Log()
	dups := countDuplicates(log)
	t.Logf("run=%s clause=overflow-concurrent-retry n=%d accepted=%d alreadyApplied=%d errs=%d logLen=%d dupCount=%d",
		runUUID, n, acc, dup, errs, len(log), dups)

	if acc != 0 {
		t.Fatalf("overflow concurrent retry VIOLATED: %d racers saw Accepted, want 0 (no second commit)", acc)
	}
	if errs != 0 {
		t.Fatalf("overflow concurrent retry VIOLATED: %d racers saw an error, want 0 (retry must not be a gap)", errs)
	}
	if dup != int64(n) {
		t.Fatalf("overflow concurrent retry VIOLATED: alreadyApplied=%d want %d", dup, n)
	}
	if len(log) != 1 || dups != 0 {
		t.Fatalf("overflow concurrent retry VIOLATED: logLen=%d dupCount=%d want 1,0 (log=%v)", len(log), dups, log)
	}
}
