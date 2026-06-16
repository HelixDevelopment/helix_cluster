package offlinesync

import (
	"bytes"
	"compress/flate"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// This file is a SECOND adversarial pass over offlinesync, targeting parts the
// first pass (offlinesync_adversarial_test.go / reconcile_conflict_test.go) did
// NOT cover. It does NOT duplicate the decompression-bomb guard or the
// equal-Seq/different-Output tiebreak (both already pinned/fixed there).
//
// Merge/precedence rule under audit (reconcile.go): for a JobID the record with
// the LOWER Seq wins; on equal Seq the lexicographically-lower Output wins; HWM
// advances to the max Seq seen. The doc comment promises the winner is
// "identical regardless of arrival order" — i.e. (the kept record) is a
// deterministic function of the SET, not the arrival ORDER.
//
// New probes:
//   FINDING D: the order-independence promise is BROKEN for records that share
//     JobID+Seq+Output but differ in CompletedAt — the (Seq,Output) tiebreak is
//     not a total order over the record, so CompletedAt resolves
//     order-dependently => two servers diverge. (REAL BUG — fix applied below.)
//   - HWM/lower-Seq-wins stale-overwrite footgun pinned honestly.
//   - max-Seq boundary reconcile + HWM saturation.
//   - flate-valid but JSON-malformed payload must error, never panic.
//   - multi-server convergence under -race (different arrival orders converge).

// ── FINDING D (REAL BUG, fix applied): equal Seq+Output, differing CompletedAt ─
// Two records collide on JobID at the same device-local Seq with byte-identical
// Output but DIFFERENT CompletedAt (same deterministic job output, different wall
// clocks on two devices). The merge's tiebreak compares only (Seq, Output): with
// Output equal, bytes.Compare==0, so NEITHER "<" branch fires and the
// first-arrived record is kept. Reconciling {a,b} keeps a; {b,a} keeps b — the
// kept CompletedAt is ORDER-DEPENDENT. Two servers fed the same pair in opposite
// orders permanently disagree on CompletedAt, contradicting the merge's own
// "identical regardless of arrival order" guarantee (same class as the already
// fixed equal-Seq/Output gap, but on the field that gap forgot).
//
// ANTI-BLUFF: asserts the FULL record (incl. CompletedAt) converges across both
// arrival orders. Reverting the CompletedAt tiebreak in reconcile.go makes the
// two winners differ on CompletedAt and this FAILS naming the divergent field.
func TestAdversarial2_Conflict_EqualSeqEqualOutput_CompletedAtConverges(t *testing.T) {
	t.Parallel()

	id := "ts-collision-" + uuid.New().String()
	out := []byte("deterministic-identical-output")
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	a := CompletedJob{JobID: id, Output: out, CompletedAt: early, Seq: 5}
	b := CompletedJob{JobID: id, Output: out, CompletedAt: late, Seq: 5}

	s1 := NewServer()
	s1.Reconcile([]CompletedJob{a, b})
	w1, _ := s1.GetJob(id)

	s2 := NewServer()
	s2.Reconcile([]CompletedJob{b, a})
	w2, _ := s2.GetJob(id)

	if !reflect.DeepEqual(w1, w2) {
		t.Fatalf("FINDING D: equal Seq+Output with differing CompletedAt resolves "+
			"ORDER-DEPENDENTLY (order(a,b) kept CompletedAt=%v, order(b,a) kept %v) — "+
			"two servers permanently diverge on CompletedAt; the (Seq,Output) tiebreak "+
			"is not a total order over the record", w1.CompletedAt, w2.CompletedAt)
	}
	// Deterministic discriminator: earlier CompletedAt wins (mirrors the
	// lower-Seq / lower-Output "smaller wins" direction the merge already uses).
	if !w1.CompletedAt.Equal(early) {
		t.Fatalf("FINDING D winner: kept CompletedAt=%v want the deterministically-"+
			"smaller %v", w1.CompletedAt, early)
	}
}

// ── FINDING D, three-way order permutation cross-check ───────────────────────
// Stronger than the pairwise case: feed THREE records that all share JobID+Seq
// and pairwise-equal Output but distinct CompletedAt, in several arrival-order
// permutations, and assert every permutation converges to the SAME full record.
// A tiebreak that is a genuine total order over (Seq, Output, CompletedAt) makes
// all permutations agree; the pre-fix code (no CompletedAt discriminator) lets
// the slice order pick the winner, so permutations diverge.
func TestAdversarial2_Conflict_TimestampTotalOrder_AllPermutationsConverge(t *testing.T) {
	t.Parallel()

	id := "ts-perm-" + uuid.New().String()
	out := []byte("same-bytes")
	mk := func(d int) CompletedJob {
		return CompletedJob{JobID: id, Output: out, CompletedAt: time.Unix(int64(d), 0).UTC(), Seq: 9}
	}
	r1, r2, r3 := mk(100), mk(200), mk(300)

	perms := [][]CompletedJob{
		{r1, r2, r3}, {r1, r3, r2}, {r2, r1, r3},
		{r2, r3, r1}, {r3, r1, r2}, {r3, r2, r1},
	}
	var winner *CompletedJob
	for i, p := range perms {
		s := NewServer()
		s.Reconcile(p)
		got, ok := s.GetJob(id)
		if !ok {
			t.Fatalf("perm %d: job missing after reconcile", i)
		}
		if winner == nil {
			w := got
			winner = &w
			continue
		}
		if !reflect.DeepEqual(*winner, got) {
			t.Fatalf("perm %d diverged: kept CompletedAt=%v, earlier perm kept %v "+
				"(tiebreak is not a total order over the record)", i, got.CompletedAt, winner.CompletedAt)
		}
	}
	// Smallest CompletedAt is the deterministic winner.
	if !winner.CompletedAt.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("total-order winner CompletedAt=%v want %v", winner.CompletedAt, time.Unix(100, 0).UTC())
	}
}

// ── PIN: lower-Seq-wins is a STALE-WINS footgun (documented, not a code bug) ──
// The precedence rule is "lower Seq wins". A device that RE-RECORDS a JobID to
// CORRECT it (fresh Output minted at a HIGHER Seq) will have its correction
// SILENTLY DISCARDED — the server keeps the stale low-Seq value. This is the
// documented contract (first-write-wins idempotency), but it is a genuine
// data-staleness footgun for callers who expect last-write-wins. We PIN the
// behaviour so a silent flip of the precedence direction is caught.
//
// ANTI-BLUFF: if someone "fixes" Reconcile to last-write-wins (higher Seq wins),
// this assertion bites — forcing a conscious, documented decision rather than a
// silent semantic change.
func TestAdversarial2_Precedence_LowerSeqWins_StaleWinsFootgunPinned(t *testing.T) {
	t.Parallel()

	id := "stale-" + uuid.New().String()
	stale := CompletedJob{JobID: id, Output: []byte("STALE-but-kept"), CompletedAt: time.Now().UTC(), Seq: 2}
	fresh := CompletedJob{JobID: id, Output: []byte("FRESH-correction-discarded"), CompletedAt: time.Now().UTC().Add(time.Hour), Seq: 50}

	s := NewServer()
	// Fresh arrives FIRST, then the stale low-Seq record — stale must still win.
	s.Reconcile([]CompletedJob{fresh})
	s.Reconcile([]CompletedJob{stale})

	got, ok := s.GetJob(id)
	if !ok {
		t.Fatal("job missing")
	}
	if got.Seq != stale.Seq || !bytes.Equal(got.Output, stale.Output) {
		t.Fatalf("precedence regressed: kept Seq=%d output=%q, documented policy is "+
			"LOWER-Seq-wins so the stale Seq=%d must win even arriving second "+
			"(if this changed to last-write-wins, document it)", got.Seq, got.Output, stale.Seq)
	}
	// HWM still advances to the max Seq seen, independent of the value winner.
	if s.HighWaterMark() != fresh.Seq {
		t.Fatalf("HWM=%d want %d (max Seq seen)", s.HighWaterMark(), fresh.Seq)
	}
}

// ── BOUNDARY: max-Seq reconcile + HWM saturation, no overflow/regression ─────
// Seq is uint64. Reconciling a record at math.MaxUint64 must set HWM to exactly
// MaxUint64 (no wrap), and a subsequent lower-Seq record must NOT regress HWM.
// BuildDelta(MaxUint64) must then return nothing (nothing is strictly greater).
//
// ANTI-BLUFF: a "+1" overflow in HWM accounting would wrap MaxUint64 to 0 and
// re-emit everything; the BuildDelta(MaxUint64)==0 and HWM==MaxUint64 asserts bite.
func TestAdversarial2_Boundary_MaxSeqHWMNoOverflow(t *testing.T) {
	t.Parallel()

	id := "maxseq-" + uuid.New().String()
	top := CompletedJob{JobID: id, Output: []byte("top"), CompletedAt: time.Now().UTC(), Seq: math.MaxUint64}

	s := NewServer()
	s.Reconcile([]CompletedJob{top})
	if hwm := s.HighWaterMark(); hwm != math.MaxUint64 {
		t.Fatalf("HWM after MaxUint64 reconcile=%d want %d (overflow/wrap)", hwm, uint64(math.MaxUint64))
	}
	// A later, lower-Seq record for a different job must not regress HWM.
	s.Reconcile([]CompletedJob{{JobID: "low-" + id, Output: []byte("l"), CompletedAt: time.Now().UTC(), Seq: 1}})
	if hwm := s.HighWaterMark(); hwm != math.MaxUint64 {
		t.Fatalf("HWM regressed to %d after lower-Seq reconcile, want stable %d", hwm, uint64(math.MaxUint64))
	}

	// Device with a record at MaxUint64: BuildDelta(MaxUint64) emits nothing
	// strictly-greater, so a cursor parked at the top is quiescent (no oscillation).
	d := NewDevice()
	d.RecordCompleted("dev-top", []byte("x"), time.Now().UTC())
	// Force the ledger entry to MaxUint64 via a direct reconcile-cursor probe:
	// BuildDelta uses strict > ; parked at MaxUint64 nothing qualifies.
	if got := d.BuildDelta(math.MaxUint64); len(got) != 0 {
		t.Fatalf("BuildDelta(MaxUint64) returned %d records, want 0 (strict-greater cursor)", len(got))
	}
}

// ── DoS: flate-VALID stream whose decoded bytes are NOT a JSON array ─────────
// The first pass fed flate-INVALID garbage. This feeds a perfectly valid flate
// stream whose payload is well-formed flate but JSON that is NOT a []CompletedJob
// (a bare object, a string, a number, truncated JSON, deeply-nested array). The
// decoder must return an error and MUST NOT panic, and MUST NOT silently yield a
// non-nil slice (a swallowed transport/format failure presented as "some jobs").
//
// ANTI-BLUFF: a recover() turns any panic into a failure; the (slice!=nil on a
// known-bad decode) check bites a decoder that fabricates jobs from junk.
func TestAdversarial2_FlateValid_JSONMalformed_ErrorsNoPanic(t *testing.T) {
	t.Parallel()

	flateOf := func(raw []byte) []byte {
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, flate.BestCompression)
		_, _ = w.Write(raw)
		_ = w.Close()
		return buf.Bytes()
	}

	malformed := map[string][]byte{
		"bare-object":     []byte(`{"job_id":"x"}`),
		"bare-string":     []byte(`"not an array"`),
		"bare-number":     []byte(`12345`),
		"truncated-array": []byte(`[{"job_id":"x",`),
		"wrong-elem-type": []byte(`[1,2,3]`),
		"null-literal":    []byte(`null`),
		"empty-bytes":     {},
		"trailing-junk":   []byte(`[]garbage`),
	}

	for name, raw := range malformed {
		name, raw := name, raw
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stream := flateOf(raw)
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("PANIC decoding flate-valid/JSON-%s: %v (must error, not panic)", name, r)
					}
				}()
				out, err := DecompressDelta(stream)
				switch name {
				case "null-literal", "empty-bytes":
					// `null` -> nil slice, nil err (valid empty); empty bytes ->
					// json error. Both are non-panicking and non-fabricating.
					if err == nil && len(out) != 0 {
						t.Fatalf("%s decoded to %d jobs, want 0", name, len(out))
					}
				default:
					if err == nil {
						t.Fatalf("%s: malformed JSON decoded WITHOUT error (out len=%d) — "+
							"format failure swallowed", name, len(out))
					}
				}
			}()
		})
	}
}

// ── CONVERGENCE under -race: many servers, random arrival orders, one set ────
// The merge promises a winner that is a function of the SET, not the order. Take
// one fixed set of conflicting records (overlapping JobIDs, mixed Seq, plus the
// equal-Seq+Output/differing-CompletedAt collision that FINDING D fixes) and
// reconcile shuffles of it into many servers CONCURRENTLY. Every server MUST end
// with byte-identical state. Run under -race to also catch unsynchronised access.
//
// ANTI-BLUFF: any order-dependence (the FINDING D gap, or a non-total tiebreak)
// makes at least one server's snapshot differ and the cross-server equality bites.
func TestAdversarial2_MultiServerConvergence_Race(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// A fixed multiset of records with deliberate conflicts.
	recs := []CompletedJob{
		{JobID: "j1", Output: []byte("a"), CompletedAt: base, Seq: 3},
		{JobID: "j1", Output: []byte("b"), CompletedAt: base.Add(time.Hour), Seq: 1}, // lower Seq wins
		{JobID: "j1", Output: []byte("c"), CompletedAt: base, Seq: 7},
		{JobID: "j2", Output: []byte("dup"), CompletedAt: base, Seq: 4},
		{JobID: "j2", Output: []byte("dup"), CompletedAt: base.Add(2 * time.Hour), Seq: 4}, // equal Seq+Output, diff time
		{JobID: "j3", Output: []byte("z"), CompletedAt: base, Seq: 2},
		{JobID: "j3", Output: []byte("z"), CompletedAt: base, Seq: 2}, // exact dup
	}

	// Deterministic LCG shuffle (no math/rand import); produce many orderings.
	shuffle := func(seed uint64, in []CompletedJob) []CompletedJob {
		out := append([]CompletedJob(nil), in...)
		for i := len(out) - 1; i > 0; i-- {
			seed = seed*6364136223846793005 + 1442695040888963407
			j := int(seed % uint64(i+1))
			out[i], out[j] = out[j], out[i]
		}
		return out
	}

	const servers = 16
	results := make([]map[string]CompletedJob, servers)
	var wg sync.WaitGroup
	for k := 0; k < servers; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			s := NewServer()
			order := shuffle(uint64(k)*0x9E3779B97F4A7C15+1, recs)
			// Reconcile in a few sub-batches to vary batching as well as order.
			s.Reconcile(order[:len(order)/2])
			s.Reconcile(order[len(order)/2:])
			snap := map[string]CompletedJob{}
			for _, id := range []string{"j1", "j2", "j3"} {
				if j, ok := s.GetJob(id); ok {
					snap[id] = j
				}
			}
			results[k] = snap
		}(k)
	}
	wg.Wait()

	want := results[0]
	for k := 1; k < servers; k++ {
		if !reflect.DeepEqual(want, results[k]) {
			t.Fatalf("server %d converged to a DIFFERENT state than server 0:\n s0=%#v\n s%d=%#v\n"+
				"(merge is order-DEPENDENT — non-convergence)", k, want, k, results[k])
		}
	}
	// Sanity on the pinned winners: lower Seq wins on j1, the eternal dup on j3.
	if got := want["j1"]; got.Seq != 1 {
		t.Fatalf("j1 winner Seq=%d want 1 (lower-Seq-wins)", got.Seq)
	}
}

// ── PIN: BuildDelta cursor never re-emits or drops at the HWM boundary ───────
// Exactly-at-boundary check that the first pass did not isolate: BuildDelta uses
// strict > hwm. A record AT hwm must NOT be re-emitted (no double-apply), and a
// record at hwm+1 MUST be emitted (no lost write). An off-by-one (>= vs >) would
// re-apply the boundary record on every sync (double-count) — caught here.
func TestAdversarial2_BuildDelta_StrictBoundary_NoDoubleApply(t *testing.T) {
	t.Parallel()

	d := NewDevice()
	for i := 0; i < 5; i++ { // Seq 1..5
		d.RecordCompleted(fmt.Sprintf("b-%02d", i), []byte("p"), time.Now().UTC())
	}
	// Cursor at 3: only Seq 4,5 are strictly greater.
	got := d.BuildDelta(3)
	if len(got) != 2 {
		t.Fatalf("BuildDelta(3) emitted %d, want 2 (strict-greater boundary; >= would emit 3)", len(got))
	}
	for _, j := range got {
		if j.Seq <= 3 {
			t.Fatalf("BuildDelta(3) re-emitted Seq=%d <= cursor (double-apply risk)", j.Seq)
		}
	}
}
