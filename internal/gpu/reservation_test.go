package gpu

import (
	"bytes"
	"crypto/rand"
	"sync"
	"testing"
)

// newSplit70_30 builds a 10-unit pool split 70/30 interactive/batch with the
// default 80% high-water mark and the DEFAULT batch hard cap (== batch reserve,
// 3 units). Used by the "below threshold normal splitting" and "batch denied
// while interactive free" cases where the hard cap should equal the reserve.
func newSplit70_30(t *testing.T) *Reservation {
	t.Helper()
	r, err := NewReservation(ReservationConfig{
		Capacity:         10,
		InteractiveRatio: 0.7,
	})
	if err != nil {
		t.Fatalf("NewReservation failed: %v", err)
	}
	if got := r.Reserved(Interactive); got != 7 {
		t.Fatalf("expected interactive reserve 7, got %d", got)
	}
	if got := r.Reserved(Batch); got != 3 {
		t.Fatalf("expected batch reserve 3, got %d", got)
	}
	return r
}

// TestBatchDeniedWhileInteractiveFree is the core anti-bluff assertion: with a
// 70/30 split, batch is denied once it would exceed its 30% reserve EVEN WHILE
// the interactive 70% slice is entirely idle; and interactive can still claim
// its full 70%.
//
// Mutation: in admitLocked's Batch branch, set `batchCeiling := r.capacity`
// (ignore the class split) -> batch is admitted up to the whole pool while
// interactive is free, so the 4th batch unit is wrongly admitted and the
// "expected denial" assertion fails.
func TestBatchDeniedWhileInteractiveFree(t *testing.T) {
	r := newSplit70_30(t)

	// Batch may take exactly its 3-unit reserve.
	for i := 0; i < 3; i++ {
		if _, err := r.Reserve(Batch, 1); err != nil {
			t.Fatalf("batch unit %d should be admitted within reserve, got %v", i+1, err)
		}
	}
	// The 4th batch unit must be DENIED even though 7 interactive units are free.
	d := r.Admit(Batch, 1)
	if d.Admitted {
		t.Fatalf("4th batch unit must be denied (exceeds 30%% reserve); decision=%+v", d)
	}
	if _, err := r.Reserve(Batch, 1); err == nil {
		t.Fatal("Reserve of 4th batch unit must fail")
	}
	if used := r.Used(Batch); used != 3 {
		t.Fatalf("batch usage must stay at reserve after denial, got %d", used)
	}

	// Interactive can STILL use its full 70% (7 units) despite batch saturation.
	for i := 0; i < 7; i++ {
		if _, err := r.Reserve(Interactive, 1); err != nil {
			t.Fatalf("interactive unit %d should be admitted within its reserve, got %v", i+1, err)
		}
	}
	if used := r.Used(Interactive); used != 7 {
		t.Fatalf("interactive should hold its full 7-unit reserve, got %d", used)
	}
	// Pool is now full (3 batch + 7 interactive = 10): even interactive is denied.
	if r.Admit(Interactive, 1).Admitted {
		t.Fatal("pool is full; further interactive must be denied")
	}
}

// TestBelowThresholdNormalSplitting proves that below the high-water mark the
// plain class split governs admission: batch is bounded by its reserve and
// interactive by free pool capacity, with no hard-cap interference.
//
// Mutation: change the Batch reserve denial branch to always
// `return Decision{Admitted: true, ...}` -> batch would be admitted past its
// reserve below threshold, failing the denial assertion here.
func TestBelowThresholdNormalSplitting(t *testing.T) {
	r := newSplit70_30(t)

	// Two batch units (below the 3-unit reserve AND below the 8-unit high-water)
	// are admitted normally.
	if _, err := r.Reserve(Batch, 2); err != nil {
		t.Fatalf("2 batch units below threshold should be admitted, got %v", err)
	}
	// A 3rd is still within reserve.
	if !r.Admit(Batch, 1).Admitted {
		t.Fatal("3rd batch unit (within reserve, below high-water) must be admitted")
	}
	// But a 2-unit batch jump (would be 4 > reserve 3) is denied by the split.
	if r.Admit(Batch, 2).Admitted {
		t.Fatal("batch request exceeding 3-unit reserve must be denied below threshold")
	}
	// Interactive availability below threshold == free pool capacity (10-2=8).
	if got := r.AvailableFor(Interactive); got != 8 {
		t.Fatalf("interactive available below threshold expected 8, got %d", got)
	}
}

// TestStarvationHardCapAboveHighWater proves the Gepetto guarantee: with an
// EXPLICIT batch hard cap tighter than the batch reserve, once total load
// reaches the 80% high-water mark a batch request that would push past the hard
// cap is rejected, while interactive is STILL admitted.
//
// Config: capacity 10, 60/40 split (interactive reserve 6, batch reserve 4),
// high-water 0.8 (=8 units), batch hard cap 0.1 (=1 unit). We drive load to 8
// using interactive, then show batch is clamped to its 1-unit hard cap (not its
// 4-unit reserve) while interactive still gets in.
//
// Mutation: delete the hard-cap block (the `if loadAfter >= r.highWaterUnits
// ...` branch) in admitLocked's Batch case -> batch would be admitted up to its
// 4-unit reserve under load, so the post-high-water batch request is wrongly
// admitted and the rejection assertion fails.
func TestStarvationHardCapAboveHighWater(t *testing.T) {
	r, err := NewReservation(ReservationConfig{
		Capacity:          10,
		InteractiveRatio:  0.6,
		HighWaterRatio:    0.8,
		BatchHardCapRatio: 0.1, // 1 unit
	})
	if err != nil {
		t.Fatalf("NewReservation failed: %v", err)
	}
	if got := r.Reserved(Batch); got != 4 {
		t.Fatalf("expected batch reserve 4, got %d", got)
	}

	// Claim 1 batch unit while load is low (below high-water): admitted within
	// both reserve (4) and hard cap path not yet engaged.
	if _, err := r.Reserve(Batch, 1); err != nil {
		t.Fatalf("first batch unit (low load) should be admitted, got %v", err)
	}

	// Drive interactive up to push TOTAL load to the high-water mark (8 units).
	// 1 batch + 7 interactive = 8 == high-water.
	for i := 0; i < 7; i++ {
		if _, err := r.Reserve(Interactive, 1); err != nil {
			t.Fatalf("interactive unit %d should be admitted, got %v", i+1, err)
		}
	}
	if total := r.Used(Interactive) + r.Used(Batch); total != 8 {
		t.Fatalf("expected total load 8 at high-water, got %d", total)
	}

	// Now at/above high-water: batch is hard-capped at 1 unit. It already holds
	// 1, so ANY further batch is rejected -- even though its 4-unit reserve is
	// far from exhausted and the pool still has 2 free units.
	d := r.Admit(Batch, 1)
	if d.Admitted {
		t.Fatalf("batch must be hard-capped above high-water; decision=%+v", d)
	}
	if _, err := r.Reserve(Batch, 1); err == nil {
		t.Fatal("Reserve of batch above high-water past hard cap must fail")
	}

	// Interactive is STILL admitted into the remaining pool headroom.
	if !r.Admit(Interactive, 1).Admitted {
		t.Fatal("interactive must still be admitted above high-water (anti-starvation protects it)")
	}
	if _, err := r.Reserve(Interactive, 1); err != nil {
		t.Fatalf("interactive reservation above high-water should succeed, got %v", err)
	}
	if r.AvailableFor(Batch) != 0 {
		t.Fatalf("batch availability under hard cap must be 0, got %d", r.AvailableFor(Batch))
	}
}

// TestHardCapBlocksBatchButNotBelowHighWater proves the hard cap is gated on
// load: with the same tight hard cap, batch can grow to MORE than the hard cap
// while load is below high-water, then is frozen once high-water is reached.
//
// Mutation: make the high-water comparison always true (e.g. change
// `loadAfter >= r.highWaterUnits` to `loadAfter >= 0`) -> the hard cap engages
// immediately and the 2nd low-load batch unit is wrongly denied, failing the
// "admitted below high-water" assertion.
func TestHardCapBlocksBatchButNotBelowHighWater(t *testing.T) {
	r, err := NewReservation(ReservationConfig{
		Capacity:          10,
		InteractiveRatio:  0.5, // interactive reserve 5, batch reserve 5
		HighWaterRatio:    0.8, // 8 units
		BatchHardCapRatio: 0.1, // 1 unit
	})
	if err != nil {
		t.Fatalf("NewReservation failed: %v", err)
	}

	// Below high-water, batch grows past the 1-unit hard cap up to its 5 reserve.
	for i := 0; i < 3; i++ {
		if _, err := r.Reserve(Batch, 1); err != nil {
			t.Fatalf("batch unit %d below high-water should be admitted (hard cap not engaged), got %v", i+1, err)
		}
	}
	if r.Used(Batch) != 3 {
		t.Fatalf("expected 3 batch units below high-water, got %d", r.Used(Batch))
	}

	// Push total to high-water: 3 batch + 5 interactive = 8.
	for i := 0; i < 5; i++ {
		if _, err := r.Reserve(Interactive, 1); err != nil {
			t.Fatalf("interactive unit %d should be admitted, got %v", i+1, err)
		}
	}
	// Now batch is frozen by the hard cap (already 3 > cap 1): denied.
	if r.Admit(Batch, 1).Admitted {
		t.Fatal("batch must be frozen by hard cap at/above high-water")
	}
}

// TestReleaseReturnsCapacityToRightClass proves Release credits the class it is
// called with and the freed units become reservable again by that class (and
// not silently by the other).
//
// Mutation: in Release, change `r.used[class] -= amount` to
// `r.used[Interactive] -= amount` (always credit interactive) -> releasing
// batch would wrongly free interactive usage; the post-release batch
// availability assertion (which expects batch headroom restored) fails because
// batch usage is unchanged while interactive went negative-ish.
func TestReleaseReturnsCapacityToRightClass(t *testing.T) {
	r := newSplit70_30(t)

	// Saturate batch to its 3-unit reserve and take 2 interactive units.
	if _, err := r.Reserve(Batch, 3); err != nil {
		t.Fatalf("batch reserve fill failed: %v", err)
	}
	if _, err := r.Reserve(Interactive, 2); err != nil {
		t.Fatalf("interactive reserve failed: %v", err)
	}

	// Batch is at its reserve: no more batch admitted.
	if r.Admit(Batch, 1).Admitted {
		t.Fatal("batch at reserve must be denied before release")
	}

	// Release 2 batch units. Capacity must return to BATCH, not interactive.
	if err := r.Release(Batch, 2); err != nil {
		t.Fatalf("Release(Batch,2) failed: %v", err)
	}
	if r.Used(Batch) != 1 {
		t.Fatalf("expected batch usage 1 after releasing 2 of 3, got %d", r.Used(Batch))
	}
	if r.Used(Interactive) != 2 {
		t.Fatalf("interactive usage must be untouched by batch release, got %d", r.Used(Interactive))
	}
	// Batch can now reserve 2 more (back to its 3-unit reserve), proving the
	// freed units went to the batch class.
	if got := r.AvailableFor(Batch); got != 2 {
		t.Fatalf("expected 2 batch units available after release, got %d", got)
	}
	if _, err := r.Reserve(Batch, 2); err != nil {
		t.Fatalf("batch should re-reserve the freed 2 units, got %v", err)
	}
}

// TestReleaseGuards proves Release rejects unknown class, non-positive amount,
// and over-release (which would drive usage negative and leak phantom units).
//
// Mutation: drop the `amount > r.used[class]` guard in Release -> over-release
// makes usage negative and the subsequent availability inflates beyond the
// reserve, failing the assertion that usage cannot go below 0.
func TestReleaseGuards(t *testing.T) {
	r := newSplit70_30(t)
	if _, err := r.Reserve(Batch, 1); err != nil {
		t.Fatalf("setup reserve failed: %v", err)
	}
	if err := r.Release(Batch, 5); err == nil {
		t.Fatal("over-release must error")
	}
	if r.Used(Batch) != 1 {
		t.Fatalf("usage must be unchanged after failed over-release, got %d", r.Used(Batch))
	}
	if err := r.Release(Batch, 0); err == nil {
		t.Fatal("zero-amount release must error")
	}
	if err := r.Release(WorkloadClass(99), 1); err == nil {
		t.Fatal("unknown-class release must error")
	}
}

// TestAdmitRejectsUnknownClassAndNonPositive proves the admission guards.
// Mutation: remove the `!class.valid()` check in admitLocked -> an unknown
// class would be treated as the default (rejected) branch only by luck; with
// the guard removed the switch's default still rejects, BUT amount<=0 with a
// valid class would be admitted if the `amount <= 0` guard is also the target.
// Concretely: delete the `amount <= 0` guard -> Admit(Interactive, 0) is
// admitted, failing the assertion below.
func TestAdmitRejectsUnknownClassAndNonPositive(t *testing.T) {
	r := newSplit70_30(t)
	if r.Admit(WorkloadClass(42), 1).Admitted {
		t.Fatal("unknown class must not be admitted")
	}
	if r.Admit(Interactive, 0).Admitted {
		t.Fatal("zero amount must not be admitted")
	}
	if r.Admit(Batch, -3).Admitted {
		t.Fatal("negative amount must not be admitted")
	}
}

// TestNewReservationValidation proves config validation rejects bad inputs.
// Mutation: change the capacity guard to `cfg.Capacity < 0` -> a 0-capacity
// pool is accepted, but then divisions/derivations yield a degenerate pool;
// the zero-capacity assertion here fails because err becomes nil.
func TestNewReservationValidation(t *testing.T) {
	bad := []ReservationConfig{
		{Capacity: 0, InteractiveRatio: 0.5},
		{Capacity: 10, InteractiveRatio: 0},
		{Capacity: 10, InteractiveRatio: 1},
		{Capacity: 10, InteractiveRatio: 0.5, HighWaterRatio: 1.5},
		{Capacity: 10, InteractiveRatio: 0.5, BatchHardCapRatio: 1.0},
	}
	for i, cfg := range bad {
		if _, err := NewReservation(cfg); err == nil {
			t.Fatalf("config case %d (%+v) should be rejected", i, cfg)
		}
	}
}

// TestAvailableForConsistentWithAdmit proves AvailableFor(c)==n means Admit(c,n)
// is admitted but Admit(c,n+1) is not -- the contract documented on
// AvailableFor. Exercised for batch at the boundary of its reserve below
// high-water.
//
// Mutation: in AvailableFor's Batch path, change `batchCeiling := r.reserve[Batch]`
// to `r.capacity` -> AvailableFor reports the whole pool for batch, so
// Admit(Batch, available) (which exceeds the reserve) is denied, breaking the
// consistency assertion.
func TestAvailableForConsistentWithAdmit(t *testing.T) {
	r := newSplit70_30(t)
	n := r.AvailableFor(Batch)
	if n != 3 {
		t.Fatalf("expected batch available 3 on fresh 70/30 pool, got %d", n)
	}
	if !r.Admit(Batch, n).Admitted {
		t.Fatalf("Admit(Batch, %d) must be admitted", n)
	}
	if r.Admit(Batch, n+1).Admitted {
		t.Fatalf("Admit(Batch, %d) must be denied", n+1)
	}
}

// --- Crypto / attestation seam tests: REAL reference implementations ---

// TestAESGCMEncryptorRoundTrip proves the stdlib reference Encryptor genuinely
// encrypts and decrypts (not a no-op), produces ciphertext distinct from
// plaintext, and rejects tampering.
//
// Mutation: make Seal `return plaintext, nil` (no-op "encryption") -> the
// ciphertext != plaintext assertion fails immediately.
func TestAESGCMEncryptorRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewAESGCMEncryptor(key)
	if err != nil {
		t.Fatalf("NewAESGCMEncryptor failed: %v", err)
	}
	pt := []byte(`{"interactive":7,"batch":3}`)
	ct, err := enc.Seal(pt)
	if err != nil {
		t.Fatalf("Seal failed: %v", err)
	}
	if bytes.Equal(ct, pt) {
		t.Fatal("ciphertext must differ from plaintext (real encryption)")
	}
	got, err := enc.Open(ct)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, pt)
	}
	// Tamper: flip the last byte of the ciphertext; Open MUST fail (GCM auth).
	ct[len(ct)-1] ^= 0xFF
	if _, err := enc.Open(ct); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

// TestHMACAttestorVerifies proves the reference Attestor produces a proof that
// verifies for the right snapshot and identity, and rejects a forged proof or a
// mismatched snapshot.
//
// Mutation: make Verify `return true` unconditionally -> the forged-proof
// rejection assertion fails because a bad proof is accepted.
func TestHMACAttestorVerifies(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	att := NewHMACAttestor("node-7/gpu-0", key)
	snap := []byte("interactive=7;batch=3;cap=10")
	proof, err := att.Attest(snap)
	if err != nil {
		t.Fatalf("Attest failed: %v", err)
	}
	if !att.Verify(snap, proof) {
		t.Fatal("valid proof must verify")
	}
	// Forged proof.
	forged := make([]byte, len(proof))
	copy(forged, proof)
	forged[0] ^= 0xFF
	if att.Verify(snap, forged) {
		t.Fatal("forged proof must NOT verify")
	}
	// Snapshot mismatch.
	if att.Verify([]byte("interactive=9;batch=1;cap=10"), proof) {
		t.Fatal("proof must not verify for a different snapshot")
	}
	// Different identity must not verify the same snapshot+proof.
	other := NewHMACAttestor("node-9/gpu-0", key)
	if other.Verify(snap, proof) {
		t.Fatal("proof bound to a different identity must not verify")
	}
}

// TestSeamsSatisfyInterfaces is a compile-time + runtime check that the stdlib
// reference types implement the Encryptor/Attestor seams the submodule plugs
// into.
func TestSeamsSatisfyInterfaces(t *testing.T) {
	key := make([]byte, 16)
	enc, err := NewAESGCMEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	var _ Encryptor = enc
	var _ Attestor = NewHMACAttestor("id", key)
}

// TestConcurrentReserveRespectsCaps proves the Reservation is concurrency-safe
// and that, under a race of batch reservers, NO MORE than the batch reserve is
// ever committed (the split holds under -race).
//
// Mutation: drop the mutex in Reserve (remove r.mu.Lock/Unlock there) -> the
// race detector fires and/or batch over-commits beyond its 3-unit reserve,
// failing the final usage assertion.
func TestConcurrentReserveRespectsCaps(t *testing.T) {
	r := newSplit70_30(t)

	var wg sync.WaitGroup
	// 20 racing single-unit batch reservers; at most 3 may succeed (batch reserve).
	successes := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Reserve(Batch, 1); err == nil {
				successes <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(successes)

	count := 0
	for range successes {
		count++
	}
	if count != 3 {
		t.Fatalf("expected exactly 3 batch reservations to succeed (reserve), got %d", count)
	}
	if r.Used(Batch) != 3 {
		t.Fatalf("batch usage must equal 3 after concurrent fill, got %d", r.Used(Batch))
	}
}
