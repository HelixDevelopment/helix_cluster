package marketplace

import (
	"bytes"
	"crypto/rand"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// marketplace_adversarial_test.go is an adversarial, mutation-oriented audit of
// the two real-state primitives in this package: the finite per-class
// GPUReservationTable (a true reservation pool, not an advisory function) and the
// AES-256-GCM AESGCMEncryptor. Unlike chaos_test.go these tests carry NO build
// tag, so they run in the default `go test -race ./pkg/marketplace/...` suite and
// gate every build.
//
// Centerpieces:
//   - NO OVER-COMMIT UNDER CONCURRENCY: N goroutines race to Reserve a fixed
//     capacity C; the count of SUCCESSFUL reservations is EXACTLY min(N,C) and an
//     atomic in-flight peak never exceeds C. A non-atomic check-then-reserve
//     (lock removed) both trips -race AND grants > C here.
//   - CROSS-CLASS ISOLATION: saturating class A leaves class B's full capacity
//     intact; A's pressure never bleeds into B.
//   - AES-GCM NONCE FRESHNESS + TAMPER REJECTION: every Seal of identical
//     plaintext yields a distinct nonce/ciphertext; any single-bit flip, wrong
//     key, or truncation makes Open fail (no panic, no silent accept).

// -----------------------------------------------------------------------------
// CENTERPIECE 1: no over-commit under concurrency (default -race suite).
// -----------------------------------------------------------------------------

// TestAdversarial_NoOverCommitUnderRace launches far more concurrent reservers
// than there is capacity and holds every grant LIVE until all goroutines have
// finished racing the capacity check (no early Release). This makes the test
// observe the true peak concurrent occupancy: successes MUST be exactly
// min(N, C), and an atomically-tracked in-flight peak MUST never exceed C.
//
// Why it bites the canonical mutation: if the mutex around the check-then-commit
// in Reserve is removed (or the capacity guard dropped), two goroutines both read
// used < total and both commit, granting more than C. Because grants are held
// live, `granted` exceeds C and `peak` exceeds C — the assertions fail.
// Independently, the unsynchronised map read/write trips the -race detector.
func TestAdversarial_NoOverCommitUnderRace(t *testing.T) {
	const capacity = 16
	const workers = 256 // N >> C: heavy oversubscription

	for iter := 0; iter < 50; iter++ {
		tbl := NewGPUReservationTable(map[string]int{"H100": capacity, "A100": 1000})

		var live int64
		var peak int64
		var granted int64

		// A gate so all workers hit Reserve as simultaneously as possible, then a
		// release barrier so grants are all held LIVE during the race window.
		start := make(chan struct{})
		var releaseWG sync.WaitGroup
		var doneWG sync.WaitGroup
		held := make([]*Reservation, 0, capacity)
		var heldMu sync.Mutex
		releaseWG.Add(1)

		for w := 0; w < workers; w++ {
			doneWG.Add(1)
			go func() {
				defer doneWG.Done()
				<-start
				r, err := tbl.Reserve("H100", 1)
				if err != nil {
					return // denial under contention is correct
				}
				atomic.AddInt64(&granted, 1)
				now := atomic.AddInt64(&live, 1)
				for {
					p := atomic.LoadInt64(&peak)
					if now <= p || atomic.CompareAndSwapInt64(&peak, p, now) {
						break
					}
				}
				if now > capacity {
					t.Errorf("over-commit: live H100 = %d > capacity %d", now, capacity)
				}
				heldMu.Lock()
				held = append(held, r)
				heldMu.Unlock()
				// Hold the unit live until every worker has finished racing.
				releaseWG.Wait()
				atomic.AddInt64(&live, -1)
			}()
		}

		close(start)
		// Spin until `capacity` units are simultaneously LIVE, then release the
		// barrier. We wait on `live` (not `granted`) deliberately: a worker bumps
		// `granted` BEFORE `live`, so waiting on `granted` could fire the barrier
		// before all winners have recorded their live count, letting freed workers
		// decrement `live` and leaving `peak < capacity` (a flaky false failure).
		// Waiting on `live` is deadlock-free and deterministic: exactly `capacity`
		// Reserves succeed (the pool caps there) and each winner holds its unit
		// (blocked on releaseWG) until the barrier, so no winner can decrement
		// before the barrier — `live` rises monotonically to exactly `capacity`.
		for atomic.LoadInt64(&live) < capacity {
		}
		releaseWG.Done()
		doneWG.Wait()

		if granted != capacity {
			t.Fatalf("iter %d: granted = %d, want exactly %d (min(N,C))", iter, granted, capacity)
		}
		if peak > capacity {
			t.Fatalf("iter %d: peak live = %d exceeded capacity %d (over-commit)", iter, peak, capacity)
		}
		if peak != capacity {
			t.Fatalf("iter %d: peak live = %d, want %d (pool never fully saturated)", iter, peak, capacity)
		}

		// Release everything; class must return to full free capacity (no leak).
		for _, r := range held {
			r.Release()
		}
		if free, _ := tbl.Available("H100"); free != capacity {
			t.Fatalf("iter %d: accounting leak, H100 free=%d after release, want %d", iter, free, capacity)
		}
	}
}

// TestAdversarial_ConcurrentReserveReleaseChurn mixes concurrent Reserve and
// Release so the pool oscillates. The invariant: used never exceeds capacity
// (Available stays in [0, capacity]) and after the storm the class is fully free.
// This catches a lock dropped only on the Release path as well as on Reserve.
func TestAdversarial_ConcurrentReserveReleaseChurn(t *testing.T) {
	const capacity = 12
	tbl := NewGPUReservationTable(map[string]int{"H100": capacity, "A100": 500})

	const workers = 96
	const iters = 400
	var wg sync.WaitGroup
	var overcommit int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				count := (i % 3) + 1
				r, err := tbl.Reserve("H100", count)
				if err != nil {
					continue
				}
				if free, _ := tbl.Available("H100"); free < 0 {
					atomic.AddInt64(&overcommit, 1)
				}
				r.Release()
			}
		}()
	}
	wg.Wait()

	if overcommit != 0 {
		t.Fatalf("observed %d moments where H100 free < 0 (over-commit)", overcommit)
	}
	if free, _ := tbl.Available("H100"); free != capacity {
		t.Fatalf("after churn H100 free=%d, want %d", free, capacity)
	}
}

// -----------------------------------------------------------------------------
// CENTERPIECE 2: cross-class isolation under concurrency.
// -----------------------------------------------------------------------------

// TestAdversarial_CrossClassIsolationConcurrent saturates class A and then hammers
// it with concurrent requests while concurrently reserving/releasing in class B.
// A's saturation must NEVER let a request succeed against A, and B's full capacity
// must remain fully usable throughout (its pool is independent). This bites a
// mutation that pools capacity globally (A's pressure would either wrongly grant
// from B or B's grants would wrongly count against A).
func TestAdversarial_CrossClassIsolationConcurrent(t *testing.T) {
	const aCap = 4
	const bCap = 32
	tbl := NewGPUReservationTable(map[string]int{"A": aCap, "B": bCap})

	// Saturate A.
	saturator, err := tbl.Reserve("A", aCap)
	if err != nil {
		t.Fatalf("saturate A: %v", err)
	}
	defer saturator.Release()
	if free, _ := tbl.Available("A"); free != 0 {
		t.Fatalf("A free after saturate = %d, want 0", free)
	}

	var aBled int64    // A reservations that wrongly succeeded while A is full
	var bMaxSeen int64 // max concurrent B units we ever successfully held

	var wg sync.WaitGroup
	// Aggressors against the saturated A class.
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if r, err := tbl.Reserve("A", 1); err == nil {
					atomic.AddInt64(&aBled, 1)
					r.Release()
				}
			}
		}()
	}
	// Independent workers fully exercising B; B must be unaffected by A's state.
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				r, err := tbl.Reserve("B", 2)
				if err != nil {
					continue
				}
				used := int64(bCap) - func() int64 { f, _ := tbl.Available("B"); return int64(f) }()
				for {
					m := atomic.LoadInt64(&bMaxSeen)
					if used <= m || atomic.CompareAndSwapInt64(&bMaxSeen, m, used) {
						break
					}
				}
				r.Release()
			}
		}()
	}
	wg.Wait()

	if aBled != 0 {
		t.Fatalf("cross-class bleed: %d A reservations succeeded while A was saturated", aBled)
	}
	// A must still be exactly saturated (only our held saturator), proving B churn
	// never leaked into A's accounting.
	if free, _ := tbl.Available("A"); free != 0 {
		t.Fatalf("A free after concurrent B churn = %d, want 0 (B bled into A)", free)
	}
	// B must be fully free again and must have been genuinely usable (we saw real
	// concurrent B occupancy), proving B's pool is independent and live.
	if free, _ := tbl.Available("B"); free != bCap {
		t.Fatalf("B free after churn = %d, want %d", free, bCap)
	}
	if bMaxSeen == 0 {
		t.Fatal("B was never successfully reserved — isolation test exercised nothing in B")
	}
	if bMaxSeen > int64(bCap) {
		t.Fatalf("B over-committed: saw %d used > capacity %d", bMaxSeen, bCap)
	}
}

// TestAdversarial_SaturatedClassDoesNotDrainSibling is the deterministic sibling
// of the concurrent isolation test: it pins the exact accounting. Fill A to the
// unit; B's free count must be untouched and a further A request is denied with
// ErrInsufficientCapacity naming A, never served from B.
func TestAdversarial_SaturatedClassDoesNotDrainSibling(t *testing.T) {
	tbl := NewGPUReservationTable(map[string]int{"A": 3, "B": 7})
	rA, err := tbl.Reserve("A", 3)
	if err != nil {
		t.Fatalf("fill A: %v", err)
	}
	defer rA.Release()

	if free, _ := tbl.Available("B"); free != 7 {
		t.Fatalf("B free after filling A = %d, want 7 (no bleed)", free)
	}
	_, err = tbl.Reserve("A", 1)
	if !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("over-fill A err = %v, want ErrInsufficientCapacity", err)
	}
	if free, _ := tbl.Available("B"); free != 7 {
		t.Fatalf("B free after denied A request = %d, want 7 (A did not borrow from B)", free)
	}
	// B remains fully reservable on its own.
	rB, err := tbl.Reserve("B", 7)
	if err != nil {
		t.Fatalf("reserve all of B = %v, want success (B independent)", err)
	}
	rB.Release()
}

// -----------------------------------------------------------------------------
// CENTERPIECE 3: AES-GCM nonce freshness + tamper rejection.
// -----------------------------------------------------------------------------

// TestAdversarial_NonceFreshnessAcrossManySeals proves every Seal of the SAME
// plaintext uses a fresh nonce (and therefore a distinct ciphertext). Nonce reuse
// is catastrophic for GCM. We Seal identical plaintext many times and assert all
// nonces (the first NonceSize bytes) and all full ciphertexts are unique.
//
// Why it bites a fixed-nonce mutation: replacing the random nonce with a constant
// makes every ciphertext of the same plaintext identical, so the uniqueness set
// collapses and the assertion fails.
func TestAdversarial_NonceFreshnessAcrossManySeals(t *testing.T) {
	enc, err := NewAESGCMEncryptor(key32)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	const n = 4096
	const nonceSize = 12 // AES-GCM standard nonce size
	pt := []byte("identical plaintext sealed many times")

	nonces := make(map[string]struct{}, n)
	cts := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		ct, err := enc.Seal(pt)
		if err != nil {
			t.Fatalf("seal %d: %v", i, err)
		}
		if len(ct) < nonceSize {
			t.Fatalf("ciphertext shorter than nonce: %d", len(ct))
		}
		nonce := string(ct[:nonceSize])
		if _, dup := nonces[nonce]; dup {
			t.Fatalf("NONCE REUSE at seal %d — GCM nonce repeated across encryptions", i)
		}
		nonces[nonce] = struct{}{}
		full := string(ct)
		if _, dup := cts[full]; dup {
			t.Fatalf("identical ciphertext at seal %d — encryption is deterministic (nonce reuse)", i)
		}
		cts[full] = struct{}{}

		// Each must still round-trip.
		got, err := enc.Open(ct)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("round-trip %d mismatch", i)
		}
	}
	if len(nonces) != n {
		t.Fatalf("unique nonces = %d, want %d", len(nonces), n)
	}
}

// TestAdversarial_NonceFreshnessConcurrent stresses nonce generation from many
// goroutines at once: concurrent Seals must still all produce unique nonces (the
// RNG path is shared) and must round-trip. Run under -race it also flags any
// unsynchronised state in Seal.
func TestAdversarial_NonceFreshnessConcurrent(t *testing.T) {
	enc, _ := NewAESGCMEncryptor(key32)
	const nonceSize = 12
	const workers = 32
	const per = 256
	pt := []byte("concurrent seal")

	var mu sync.Mutex
	seen := make(map[string]struct{}, workers*per)
	var dup int64

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				ct, err := enc.Seal(pt)
				if err != nil {
					t.Errorf("seal: %v", err)
					return
				}
				if got, err := enc.Open(ct); err != nil || !bytes.Equal(got, pt) {
					t.Errorf("round-trip failed: err=%v", err)
					return
				}
				nonce := string(ct[:nonceSize])
				mu.Lock()
				if _, ok := seen[nonce]; ok {
					atomic.AddInt64(&dup, 1)
				} else {
					seen[nonce] = struct{}{}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if dup != 0 {
		t.Fatalf("nonce reuse across concurrent seals: %d duplicates", dup)
	}
}

// TestAdversarial_RoundTripVariedPlaintext round-trips edge-case plaintexts:
// empty, single byte, exact-block-multiple, and large random binary. All must
// Open back byte-identical, and ciphertext must differ from plaintext (for the
// non-empty cases).
func TestAdversarial_RoundTripVariedPlaintext(t *testing.T) {
	enc, _ := NewAESGCMEncryptor(key32)

	large := make([]byte, 1<<16)
	if _, err := rand.Read(large); err != nil {
		t.Fatalf("rand: %v", err)
	}
	cases := map[string][]byte{
		"empty":        {},
		"single":       {0x00},
		"block16":      bytes.Repeat([]byte{0xAB}, 16),
		"binary":       {0x00, 0xFF, 0x10, 0x80, 0x7F, 0x00},
		"large-random": large,
	}
	for name, pt := range cases {
		ct, err := enc.Seal(pt)
		if err != nil {
			t.Fatalf("%s seal: %v", name, err)
		}
		got, err := enc.Open(ct)
		if err != nil {
			t.Fatalf("%s open: %v", name, err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("%s round-trip mismatch", name)
		}
		if len(pt) > 0 && bytes.Equal(ct, pt) {
			t.Fatalf("%s ciphertext equals plaintext — no-op encryption", name)
		}
	}
}

// TestAdversarial_TamperEveryBytePositionRejected flips each byte in turn across
// the WHOLE ciphertext (nonce region, encrypted body, and the trailing GCM tag)
// and asserts Open rejects every single mutation with an error and never a panic.
// A GCM auth bypass (or dropping the tag) would let some position decrypt
// "successfully" and fail this test.
func TestAdversarial_TamperEveryBytePositionRejected(t *testing.T) {
	enc, _ := NewAESGCMEncryptor(key32)
	pt := []byte("authentic confidential payload")
	base, err := enc.Seal(pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	for i := 0; i < len(base); i++ {
		corrupt := make([]byte, len(base))
		copy(corrupt, base)
		corrupt[i] ^= 0xFF
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("Open panicked on tampered byte %d: %v", i, rec)
				}
			}()
			out, err := enc.Open(corrupt)
			if err == nil {
				// A flip in the nonce region yields a valid-length but wrong nonce;
				// GCM auth must still reject it because the tag won't verify.
				t.Fatalf("Open ACCEPTED tampered ciphertext at byte %d (got %d bytes) — auth broken", i, len(out))
			}
		}()
	}
}

// TestAdversarial_WrongKeyDecryptFails proves ciphertext sealed under one key
// cannot be opened under a different key (the GCM tag won't verify). A wrong-key
// success would mean authentication/keying is broken.
func TestAdversarial_WrongKeyDecryptFails(t *testing.T) {
	encA, _ := NewAESGCMEncryptor([]byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")) // 32 bytes
	encB, _ := NewAESGCMEncryptor([]byte("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")) // 32 bytes

	ct, err := encA.Seal([]byte("secret for A only"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := encB.Open(ct); err == nil {
		t.Fatal("wrong-key Open succeeded — ciphertext decrypted under a different key")
	}
	// Sanity: the right key still works.
	if _, err := encA.Open(ct); err != nil {
		t.Fatalf("right-key Open failed: %v", err)
	}
}

// TestAdversarial_TruncatedCiphertextRejected proves every truncation length —
// from empty up to one byte short of a valid minimum — is rejected without panic.
// Lengths below the nonce size must return ErrCiphertextTooShort; lengths at/above
// the nonce size but with no/!invalid tag must fail GCM auth. No length may panic
// or silently "succeed".
func TestAdversarial_TruncatedCiphertextRejected(t *testing.T) {
	enc, _ := NewAESGCMEncryptor(key32)
	full, err := enc.Seal([]byte("payload to be truncated"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	const nonceSize = 12

	for n := 0; n < len(full); n++ {
		trunc := full[:n]
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("Open panicked on truncation length %d: %v", n, rec)
				}
			}()
			_, err := enc.Open(trunc)
			if err == nil {
				t.Fatalf("Open ACCEPTED truncated ciphertext of length %d — must fail", n)
			}
			if n < nonceSize && !errors.Is(err, ErrCiphertextTooShort) {
				t.Fatalf("truncation %d: err = %v, want ErrCiphertextTooShort", n, err)
			}
		}()
	}
}

// -----------------------------------------------------------------------------
// EDGES: zero capacity, single unit.
// -----------------------------------------------------------------------------

// TestAdversarial_ZeroCapacityClass proves a class configured with zero capacity
// is known but immediately denies any positive reservation, and never bleeds from
// a sibling.
func TestAdversarial_ZeroCapacityClass(t *testing.T) {
	tbl := NewGPUReservationTable(map[string]int{"zero": 0, "other": 5})
	free, ok := tbl.Available("zero")
	if !ok {
		t.Fatal("zero-capacity class reported unknown, want known")
	}
	if free != 0 {
		t.Fatalf("zero class free = %d, want 0", free)
	}
	if _, err := tbl.Reserve("zero", 1); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("reserve on zero-capacity err = %v, want ErrInsufficientCapacity", err)
	}
	if free, _ := tbl.Available("other"); free != 5 {
		t.Fatalf("sibling 'other' free = %d, want 5 (zero class did not borrow)", free)
	}
}

// TestAdversarial_SingleUnitClassExclusive proves a 1-unit class grants exactly
// one live reservation at a time: a second concurrent request is denied until the
// first releases.
func TestAdversarial_SingleUnitClassExclusive(t *testing.T) {
	tbl := NewGPUReservationTable(map[string]int{"solo": 1})
	r, err := tbl.Reserve("solo", 1)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if _, err := tbl.Reserve("solo", 1); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("second reserve err = %v, want ErrInsufficientCapacity", err)
	}
	r.Release()
	r2, err := tbl.Reserve("solo", 1)
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	r2.Release()
}
