package inferenceproxy

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slowBackend is a Backend whose Infer is deliberately non-trivial: it bumps a
// shared atomic and yields the scheduler, widening the window during which the
// request-path goroutine is inside Proxy.Infer (and about to write the audit
// sink). The wider window makes any unguarded concurrent access to the shared
// audit slice far more likely to be caught by -race.
type slowBackend struct {
	hits atomic.Int64
}

func (b *slowBackend) Infer(_ context.Context, _ Request) (Response, error) {
	b.hits.Add(1)
	// Yield so the writer goroutine is mid-flight while readers scan the slice.
	runtime.Gosched()
	return Response{Model: "qwen", Text: "ok", Tokens: 1}, nil
}

// TestRace_ConcurrentAuditWriteAndRead is the adversarial -race test for the
// ONLY shared mutable state in this package: MemAudit.entries, guarded by
// MemAudit.mu. The proxy is concurrent-by-role — every request goroutine writes
// an audit record (the WRITE path, via Proxy.Infer -> MemAudit.Record) while
// observers concurrently read the audit history (the READ path, via
// MemAudit.Entries / MemAudit.Len). A slice append reallocates the backing
// array; a concurrent read of that slice header/backing array WITHOUT holding
// the lock is a data race that -race flags, and at minimum an unguarded read
// concurrent with a slice-grow append is a genuine torn read.
//
// WHY -race BITES A PARTIAL-LOCK BUG HERE: if MemAudit.Record dropped its
// mu.Lock (the partial-lock defect this audit hunts), this test runs a writer
// append concurrently with a reader len()/copy() over the same slice header and
// backing array -> the race detector reports a read/write data race at the
// MemAudit field access. Proven load-bearing below: removing the lock in
// Record (or in Entries/Len) makes THIS test fail under -race; restoring it
// makes it pass. That is the definition of a load-bearing concurrency guard.
//
// CORRECTNESS (logic, not race): independent of -race, the test asserts the
// in-flight accounting is exact — backend hits == number of admitted requests,
// and the final audit length == total requests (one entry per request, none
// lost to a racing append). A logic bug (a lost append, a double record, or
// forwarding a denied request) breaks these equality assertions even with the
// race detector off.
func TestRace_ConcurrentAuditWriteAndRead(t *testing.T) {
	backend := &slowBackend{}
	audit := NewMemAudit()
	secret := []byte("race-key")
	auth := NewTokenAuthorizer(secret, "infer")
	p := mustProxy(t, backend, auth, audit, fixedClock(time.Unix(7, 0)))

	const writers = 64 // admitted request goroutines (WRITE path)
	const readers = 16 // concurrent audit observers (READ path)
	good := auth.MintToken("alice")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// READ path: continuously snapshot/length the audit history while writes are
	// in flight. With a missing lock this reads the slice header concurrently
	// with an appending writer -> -race fires.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = audit.Len()
					_ = audit.Entries()
				}
			}
		}()
	}

	// WRITE path: each admitted request forwards to the backend and appends one
	// audit entry. These run concurrently with each other and with the readers.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Infer(context.Background(), Request{
				Subject: "alice", Model: "qwen", Prompt: "p",
				Token: good, Scope: "infer",
			})
		}()
	}

	// Wait for all writers, then stop readers.
	writersDone := make(chan struct{})
	go func() {
		// Re-wait only on writers by draining; simplest: spin until hits == writers.
		for backend.hits.Load() < int64(writers) {
			runtime.Gosched()
		}
		close(writersDone)
	}()
	<-writersDone
	close(stop)
	wg.Wait()

	// CORRECTNESS assertions (mutation-killing, race-detector-independent):
	// every admitted request reached the backend exactly once...
	if h := backend.hits.Load(); h != int64(writers) {
		t.Fatalf("backend hits = %d, want %d (each admitted request forwarded exactly once)", h, writers)
	}
	// ...and every request produced exactly one audit entry with no append lost
	// to a racing writer.
	if got := audit.Len(); got != writers {
		t.Fatalf("audit Len = %d, want %d (one entry per request, none lost)", got, writers)
	}
	entries := audit.Entries()
	if len(entries) != writers {
		t.Fatalf("audit Entries len = %d, want %d", len(entries), writers)
	}
	for i, e := range entries {
		if e.Decision != DecisionAllow {
			t.Fatalf("entry[%d].Decision = %q, want %q (all requests were admitted)", i, e.Decision, DecisionAllow)
		}
		if e.Subject != "alice" || e.Model != "qwen" || e.PromptBytes != 1 {
			t.Fatalf("entry[%d] = %+v, want who=alice model=qwen bytes=1", i, e)
		}
	}
}

// TestRace_MixedAdmitDeny_ConcurrentAuditObservers is a second adversarial
// scenario: writers split between ADMITTED and DENIED requests (deny path also
// writes an audit record, via a different branch of Proxy.Infer) while readers
// concurrently observe. This exercises BOTH write branches racing the readers.
//
// WHY -race BITES: same shared MemAudit.entries; both the allow-branch and
// deny-branch call MemAudit.Record. A dropped lock races either branch's append
// against the readers. WHY CORRECTNESS BITES: backend hits must equal the
// admitted count EXACTLY (a denied request leaking to the backend, or an
// admitted one dropped, breaks equality), and total audit entries must equal
// admitted+denied (one per request regardless of decision).
func TestRace_MixedAdmitDeny_ConcurrentAuditObservers(t *testing.T) {
	backend := &slowBackend{}
	audit := NewMemAudit()
	secret := []byte("race-key-2")
	auth := NewTokenAuthorizer(secret, "infer")
	p := mustProxy(t, backend, auth, audit, fixedClock(time.Unix(8, 0)))

	const admitted, denied = 48, 48
	const total = admitted + denied
	good := auth.MintToken("alice")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 12; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = audit.Entries()
				}
			}
		}()
	}

	var reqWG sync.WaitGroup
	for i := 0; i < admitted; i++ {
		reqWG.Add(1)
		go func() {
			defer reqWG.Done()
			_, _ = p.Infer(context.Background(), Request{Subject: "alice", Model: "qwen", Prompt: "p", Token: good, Scope: "infer"})
		}()
	}
	for i := 0; i < denied; i++ {
		reqWG.Add(1)
		go func() {
			defer reqWG.Done()
			// Bad token -> deny branch; backend must NOT be hit.
			_, _ = p.Infer(context.Background(), Request{Subject: "mallory", Model: "qwen", Prompt: "p", Token: "00", Scope: "infer"})
		}()
	}
	reqWG.Wait()
	close(stop)
	wg.Wait()

	if h := backend.hits.Load(); h != int64(admitted) {
		t.Fatalf("backend hits = %d, want %d (only admitted requests forwarded; no deny leaked)", h, admitted)
	}
	if got := audit.Len(); got != total {
		t.Fatalf("audit Len = %d, want %d (one entry per request, allow+deny)", got, total)
	}
	var allow, deny int
	for _, e := range audit.Entries() {
		switch e.Decision {
		case DecisionAllow:
			allow++
		case DecisionDeny:
			deny++
		}
	}
	if allow != admitted || deny != denied {
		t.Fatalf("audit decisions = %d allow / %d deny, want %d / %d", allow, deny, admitted, denied)
	}
}
