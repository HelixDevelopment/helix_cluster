package idempotent

import (
	"errors"
	"testing"
)

const runUUID = "HXC-1415-idempotent-9f3c1a2e-7b41-4d0e-8c6a-1e2f3a4b5c6d"

// countDuplicates returns the number of (ProducerID, Seq) pairs that appear
// more than once in the committed log — i.e. exactly-once violations.
func countDuplicates(log []Message) int {
	type key struct {
		pid string
		seq int
	}
	counts := make(map[key]int, len(log))
	for _, m := range log {
		counts[key{m.ProducerID, m.Seq}]++
	}
	dups := 0
	for _, c := range counts {
		if c > 1 {
			dups += c - 1
		}
	}
	return dups
}

// Closure clause 1: Exactly-once under retries.
//
// We send seqs 1..5 for producer P, but re-send (simulate lost acks) some of
// the already-accepted seqs interleaved with fresh ones. The committed log MUST
// contain each distinct seq EXACTLY ONCE, in order, with zero duplicates.
//
// Mutation: if the `msg.Seq < exp` duplicate guard in Append is removed (so a
// re-sent Seq is appended again), duplicateCount becomes > 0 and the log length
// no longer equals the number of distinct seqs — this test fails.
func TestExactlyOnceUnderRetries(t *testing.T) {
	b := New(1)
	pid := "P-orders"

	// Each step: (seq, expectAccepted). Re-sends target already-accepted seqs.
	steps := []struct {
		seq          int
		wantAccepted bool
		wantResult   Result
	}{
		{1, true, Accepted},        // fresh
		{1, false, AlreadyApplied}, // ack lost -> retry, duplicate
		{2, true, Accepted},        // fresh
		{2, false, AlreadyApplied}, // retry
		{1, false, AlreadyApplied}, // stale retry of older seq
		{3, true, Accepted},        // fresh
		{4, true, Accepted},        // fresh
		{3, false, AlreadyApplied}, // retry
		{5, true, Accepted},        // fresh
		{5, false, AlreadyApplied}, // retry
	}

	t.Logf("run=%s clause=exactly-once-under-retries pid=%s logLenBefore=%d", runUUID, pid, len(b.Log()))

	accepted := 0
	for i, s := range steps {
		res, err := b.Append(Message{ProducerID: pid, Seq: s.seq, Payload: "p"})
		if err != nil {
			t.Fatalf("step %d seq=%d: unexpected error: %v", i, s.seq, err)
		}
		if res != s.wantResult {
			t.Fatalf("step %d seq=%d: result=%v want=%v", i, s.seq, res, s.wantResult)
		}
		if s.wantAccepted {
			accepted++
		}
	}

	log := b.Log()
	distinctSeqs := 5 // seqs 1..5
	dups := countDuplicates(log)

	t.Logf("run=%s logLenAfter=%d acceptedCount=%d distinctSeqs=%d duplicateCount=%d",
		runUUID, len(log), accepted, distinctSeqs, dups)

	if dups != 0 {
		t.Fatalf("exactly-once violated: duplicateCount=%d want 0 (log=%v)", dups, log)
	}
	if len(log) != distinctSeqs {
		t.Fatalf("log length=%d want %d (one per distinct seq)", len(log), distinctSeqs)
	}
	// Ordering preserved: committed seqs must be strictly 1,2,3,4,5.
	for i, m := range log {
		wantSeq := i + 1
		if m.ProducerID != pid || m.Seq != wantSeq {
			t.Fatalf("log[%d]={pid=%s seq=%d} want {pid=%s seq=%d}", i, m.ProducerID, m.Seq, pid, wantSeq)
		}
	}
	if last, ok := b.LastSeq(pid); !ok || last != 5 {
		t.Fatalf("LastSeq(%s)=(%d,%v) want (5,true)", pid, last, ok)
	}
}

// Closure clause 2: Gap rejected, then the in-order seq is accepted.
//
// Mutation: if Append accepts Seq > lastSeq+1 (gap guard removed / wrong
// comparison), the skip-ahead seq would be appended and ErrOutOfSequence would
// not be returned — this test fails on both the error check and the log length.
func TestGapRejectedThenFilled(t *testing.T) {
	b := New(1)
	pid := "P-gap"

	t.Logf("run=%s clause=gap-rejected pid=%s lastSeqBefore=none", runUUID, pid)

	// Accept seq 1.
	if res, err := b.Append(Message{ProducerID: pid, Seq: 1, Payload: "a"}); err != nil || res != Accepted {
		t.Fatalf("seq 1: res=%v err=%v want Accepted,nil", res, err)
	}

	// Skip seq 2, send seq 3 -> GAP, must be rejected and NOT appended.
	logLenBeforeGap := len(b.Log())
	res, err := b.Append(Message{ProducerID: pid, Seq: 3, Payload: "c"})
	if !errors.Is(err, ErrOutOfSequence) {
		t.Fatalf("seq 3 (gap): err=%v want ErrOutOfSequence", err)
	}
	if res == Accepted {
		t.Fatalf("seq 3 (gap): result=Accepted, must not be accepted")
	}
	logLenAfterGap := len(b.Log())
	if logLenAfterGap != logLenBeforeGap {
		t.Fatalf("gap appended a message: logLen %d -> %d", logLenBeforeGap, logLenAfterGap)
	}
	if last, ok := b.LastSeq(pid); !ok || last != 1 {
		t.Fatalf("after gap LastSeq=(%d,%v) want (1,true) — watermark must not advance", last, ok)
	}

	t.Logf("run=%s gapSeq=3 gapErr=%v logLenUnchanged=%d lastSeqAfterGap=1", runUUID, err, logLenAfterGap)

	// Now deliver the missing seq 2 in order -> accepted.
	if res, err := b.Append(Message{ProducerID: pid, Seq: 2, Payload: "b"}); err != nil || res != Accepted {
		t.Fatalf("seq 2 (fill): res=%v err=%v want Accepted,nil", res, err)
	}
	// And seq 3 now fits.
	if res, err := b.Append(Message{ProducerID: pid, Seq: 3, Payload: "c"}); err != nil || res != Accepted {
		t.Fatalf("seq 3 (refill): res=%v err=%v want Accepted,nil", res, err)
	}

	log := b.Log()
	wantSeqs := []int{1, 2, 3}
	if len(log) != len(wantSeqs) {
		t.Fatalf("final log length=%d want %d (log=%v)", len(log), len(wantSeqs), log)
	}
	for i, m := range log {
		if m.Seq != wantSeqs[i] {
			t.Fatalf("log[%d].Seq=%d want %d", i, m.Seq, wantSeqs[i])
		}
	}
	t.Logf("run=%s clause=gap-filled finalSeqs=%v lastSeqAfter=3", runUUID, wantSeqs)
}

// Closure clause 3: Multiple producers are independent.
//
// Interleave appends across two producers. Each maintains its OWN lastSeq and
// committed sub-log; one producer's progress (or duplicate) must not affect the
// other.
//
// Mutation: if lastSeq were tracked globally instead of per-producer (e.g.
// keyed by a constant rather than msg.ProducerID), the interleaved seqs would
// be seen as duplicates/gaps and acceptance would diverge from per-producer
// expectations — this test fails.
func TestMultipleProducersIndependent(t *testing.T) {
	b := New(1)
	pa, pb := "P-a", "P-b"

	t.Logf("run=%s clause=multi-producer-independence pa=%s pb=%s", runUUID, pa, pb)

	// Interleaved stream: note both producers independently start at seq 1.
	stream := []struct {
		pid        string
		seq        int
		wantResult Result
		wantErrGap bool
	}{
		{pa, 1, Accepted, false},
		{pb, 1, Accepted, false}, // pb's seq 1 is fresh, NOT a dup of pa's seq 1
		{pa, 2, Accepted, false},
		{pb, 1, AlreadyApplied, false}, // pb retry
		{pb, 2, Accepted, false},
		{pa, 1, AlreadyApplied, false}, // pa retry
		{pa, 4, AlreadyApplied, true},  // pa gap (skipped 3)
		{pa, 3, Accepted, false},       // pa fills 3
		{pb, 3, Accepted, false},
	}

	for i, s := range stream {
		res, err := b.Append(Message{ProducerID: s.pid, Seq: s.seq, Payload: "x"})
		if s.wantErrGap {
			if !errors.Is(err, ErrOutOfSequence) {
				t.Fatalf("step %d %s seq=%d: err=%v want ErrOutOfSequence", i, s.pid, s.seq, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("step %d %s seq=%d: unexpected err=%v", i, s.pid, s.seq, err)
		}
		if res != s.wantResult {
			t.Fatalf("step %d %s seq=%d: res=%v want=%v", i, s.pid, s.seq, res, s.wantResult)
		}
	}

	logA := b.LogFor(pa)
	logB := b.LogFor(pb)
	lastA, okA := b.LastSeq(pa)
	lastB, okB := b.LastSeq(pb)

	t.Logf("run=%s pa.logLen=%d pa.lastSeq=%d pb.logLen=%d pb.lastSeq=%d totalLog=%d",
		runUUID, len(logA), lastA, len(logB), lastB, len(b.Log()))

	// Each producer committed seqs 1,2,3 exactly once, independently.
	for name, lg := range map[string][]Message{pa: logA, pb: logB} {
		if len(lg) != 3 {
			t.Fatalf("producer %s log length=%d want 3 (log=%v)", name, len(lg), lg)
		}
		for i, m := range lg {
			if m.ProducerID != name {
				t.Fatalf("producer %s log[%d] has foreign pid=%s", name, i, m.ProducerID)
			}
			if m.Seq != i+1 {
				t.Fatalf("producer %s log[%d].Seq=%d want %d", name, i, m.Seq, i+1)
			}
		}
	}
	if !okA || lastA != 3 {
		t.Fatalf("LastSeq(%s)=(%d,%v) want (3,true)", pa, lastA, okA)
	}
	if !okB || lastB != 3 {
		t.Fatalf("LastSeq(%s)=(%d,%v) want (3,true)", pb, lastB, okB)
	}
	// Global log has all 6 committed messages with zero duplicates.
	if dups := countDuplicates(b.Log()); dups != 0 {
		t.Fatalf("global duplicateCount=%d want 0", dups)
	}
	if got := len(b.Log()); got != 6 {
		t.Fatalf("global log length=%d want 6", got)
	}
}

// Sanity: a non-default base shifts the first valid Seq.
// Mutation: if expected() ignored base for fresh producers, seq=base would be a
// gap (rejected) — this asserts base is honored.
func TestNonDefaultBase(t *testing.T) {
	b := New(100)
	pid := "P-base"

	if _, ok := b.LastSeq(pid); ok {
		t.Fatalf("fresh producer should report ok=false from LastSeq")
	}
	// seq 99 < base -> treated as already-applied (pre-base), not accepted.
	if res, err := b.Append(Message{ProducerID: pid, Seq: 99}); err != nil || res != AlreadyApplied {
		t.Fatalf("seq 99 (<base): res=%v err=%v want AlreadyApplied,nil", res, err)
	}
	if len(b.Log()) != 0 {
		t.Fatalf("pre-base seq must not be committed, log=%v", b.Log())
	}
	// seq 100 == base -> accepted.
	if res, err := b.Append(Message{ProducerID: pid, Seq: 100}); err != nil || res != Accepted {
		t.Fatalf("seq 100 (==base): res=%v err=%v want Accepted,nil", res, err)
	}
	t.Logf("run=%s clause=base base=100 firstAcceptedSeq=100 logLen=%d", runUUID, len(b.Log()))
}
