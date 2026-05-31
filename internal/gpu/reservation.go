package gpu

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"
)

// WorkloadClass partitions GPU demand into two service tiers that share one
// pool. Interactive (latency-sensitive) work is the high-priority class whose
// reserved slice batch work may never consume; Batch (best-effort) is the
// lower-priority class that is hard-capped under load so it cannot starve
// interactive work — the "Gepetto" anti-starvation guarantee.
type WorkloadClass int

const (
	// Interactive is the high-priority, latency-sensitive class.
	Interactive WorkloadClass = iota
	// Batch is the lower-priority, best-effort class.
	Batch
)

var workloadClassNames = map[WorkloadClass]string{
	Interactive: "interactive",
	Batch:       "batch",
}

func (c WorkloadClass) String() string {
	if name, ok := workloadClassNames[c]; ok {
		return name
	}
	return fmt.Sprintf("WorkloadClass(%d)", c)
}

// valid reports whether c is a known class. Reserve/Release/AvailableFor reject
// unknown classes so a caller cannot mutate phantom accounting buckets.
func (c WorkloadClass) valid() bool {
	_, ok := workloadClassNames[c]
	return ok
}

// Decision is the outcome of an admission check for a prospective allocation.
type Decision struct {
	// Admitted reports whether the allocation is allowed.
	Admitted bool
	// Reason is a human-readable explanation, always set (even when admitted)
	// so audit logs and tests can assert on the exact branch taken.
	Reason string
}

// Encryptor seals and opens reservation-ledger bytes. The stdlib reference
// implementation (AESGCMEncryptor) provides authenticated AES-256-GCM and
// actually round-trips in tests. The digital.vasic.security submodule's
// post-quantum end-to-end encryption implements this SAME interface, so a
// PQ-E2EE Encryptor can be swapped in without touching reservation logic.
type Encryptor interface {
	// Seal encrypts plaintext, returning an authenticated ciphertext.
	Seal(plaintext []byte) ([]byte, error)
	// Open authenticates and decrypts a ciphertext produced by Seal.
	Open(ciphertext []byte) ([]byte, error)
}

// Attestor produces and verifies a proof binding a reservation snapshot to a
// node/GPU identity. The stdlib reference implementation (HMACAttestor) uses
// HMAC-SHA256 and genuinely verifies in tests. The submodule's
// proof-of-GPU attestation implements this SAME interface, so a real
// hardware-rooted Attestor plugs in without changing the reservation seam.
type Attestor interface {
	// Attest returns a proof over snapshot bound to the configured identity.
	Attest(snapshot []byte) ([]byte, error)
	// Verify reports whether proof is a valid attestation over snapshot.
	Verify(snapshot, proof []byte) bool
}

// AESGCMEncryptor is the stdlib reference Encryptor using AES-256-GCM. Each
// Seal uses a fresh random nonce prepended to the ciphertext.
type AESGCMEncryptor struct {
	aead cipher.AEAD
}

// NewAESGCMEncryptor builds an AESGCMEncryptor from a 16/24/32-byte key.
func NewAESGCMEncryptor(key []byte) (*AESGCMEncryptor, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("reservation: bad AES key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("reservation: gcm init: %w", err)
	}
	return &AESGCMEncryptor{aead: aead}, nil
}

// Seal implements Encryptor.
func (e *AESGCMEncryptor) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("reservation: nonce: %w", err)
	}
	// Prepend nonce; Seal appends ciphertext+tag onto the nonce slice.
	return e.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open implements Encryptor.
func (e *AESGCMEncryptor) Open(ciphertext []byte) ([]byte, error) {
	ns := e.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("reservation: ciphertext too short")
	}
	nonce, ct := ciphertext[:ns], ciphertext[ns:]
	pt, err := e.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("reservation: open: %w", err)
	}
	return pt, nil
}

// HMACAttestor is the stdlib reference Attestor using HMAC-SHA256 over the
// identity and snapshot. Verify is constant-time.
type HMACAttestor struct {
	key      []byte
	identity []byte
}

// NewHMACAttestor builds an HMACAttestor for the given identity and key.
func NewHMACAttestor(identity string, key []byte) *HMACAttestor {
	return &HMACAttestor{key: key, identity: []byte(identity)}
}

func (a *HMACAttestor) mac(snapshot []byte) []byte {
	h := hmac.New(sha256.New, a.key)
	h.Write(a.identity)
	h.Write([]byte{0}) // domain separator between identity and snapshot
	h.Write(snapshot)
	return h.Sum(nil)
}

// Attest implements Attestor.
func (a *HMACAttestor) Attest(snapshot []byte) ([]byte, error) {
	return a.mac(snapshot), nil
}

// Verify implements Attestor.
func (a *HMACAttestor) Verify(snapshot, proof []byte) bool {
	return hmac.Equal(a.mac(snapshot), proof)
}

// ReservationConfig parameterizes a Reservation.
type ReservationConfig struct {
	// Capacity is the total number of allocatable units in the pool (e.g. the
	// number of GPUs, or finer-grained slices). Must be > 0.
	Capacity int
	// InteractiveRatio is the fraction of Capacity reserved for the Interactive
	// class, in (0,1). With 0.7, batch may use at most 30% of capacity even
	// while interactive capacity sits idle.
	InteractiveRatio float64
	// HighWaterRatio is the total-load fraction in (0,1] at or above which the
	// batch hard cap engages. Defaults to 0.8 when zero.
	HighWaterRatio float64
	// BatchHardCapRatio is the fraction of Capacity to which Batch is clamped
	// once load is at/above the high-water mark. Defaults to InteractiveRatio's
	// complement (the batch reserve) when zero, i.e. batch may not grow beyond
	// its own reserve under pressure.
	BatchHardCapRatio float64
}

// Reservation enforces a dual-class capacity split plus a starvation hard-cap.
// It is safe for concurrent use.
type Reservation struct {
	mu       sync.Mutex
	capacity int
	// reserve[class] is the per-class reserved unit count derived from ratios.
	reserve map[WorkloadClass]int
	// used[class] is the currently reserved (in-use) units for that class.
	used map[WorkloadClass]int

	highWaterUnits  int // load at/above which the batch hard cap engages
	batchHardCapUse int // max Batch units allowed while load >= highWaterUnits
}

// NewReservation builds a Reservation from cfg, validating the parameters.
func NewReservation(cfg ReservationConfig) (*Reservation, error) {
	if cfg.Capacity <= 0 {
		return nil, fmt.Errorf("reservation: capacity must be > 0, got %d", cfg.Capacity)
	}
	if cfg.InteractiveRatio <= 0 || cfg.InteractiveRatio >= 1 {
		return nil, fmt.Errorf("reservation: interactive ratio must be in (0,1), got %v", cfg.InteractiveRatio)
	}
	highWater := cfg.HighWaterRatio
	if highWater == 0 {
		highWater = 0.8
	}
	if highWater <= 0 || highWater > 1 {
		return nil, fmt.Errorf("reservation: high-water ratio must be in (0,1], got %v", highWater)
	}

	// Interactive reserve rounds down; batch reserve is the remainder so the two
	// reserves always sum to exactly capacity (no unit is double-counted or
	// orphaned).
	interactiveReserve := int(float64(cfg.Capacity) * cfg.InteractiveRatio)
	if interactiveReserve < 1 {
		interactiveReserve = 1
	}
	if interactiveReserve > cfg.Capacity-1 {
		// Always leave at least one unit of batch reserve so batch isn't starved
		// by rounding alone.
		interactiveReserve = cfg.Capacity - 1
	}
	batchReserve := cfg.Capacity - interactiveReserve

	batchHardCap := batchReserve
	if cfg.BatchHardCapRatio > 0 {
		if cfg.BatchHardCapRatio >= 1 {
			return nil, fmt.Errorf("reservation: batch hard-cap ratio must be < 1, got %v", cfg.BatchHardCapRatio)
		}
		batchHardCap = int(float64(cfg.Capacity) * cfg.BatchHardCapRatio)
		if batchHardCap < 1 {
			batchHardCap = 1
		}
	}

	highWaterUnits := int(float64(cfg.Capacity) * highWater)
	if highWaterUnits < 1 {
		highWaterUnits = 1
	}

	return &Reservation{
		capacity: cfg.Capacity,
		reserve: map[WorkloadClass]int{
			Interactive: interactiveReserve,
			Batch:       batchReserve,
		},
		used: map[WorkloadClass]int{
			Interactive: 0,
			Batch:       0,
		},
		highWaterUnits:  highWaterUnits,
		batchHardCapUse: batchHardCap,
	}, nil
}

// totalUsedLocked returns combined in-use units across both classes. Caller
// must hold mu.
func (r *Reservation) totalUsedLocked() int {
	return r.used[Interactive] + r.used[Batch]
}

// admitLocked is the core admission decision shared by Admit and Reserve.
// Caller must hold mu. It does NOT mutate state.
func (r *Reservation) admitLocked(class WorkloadClass, amount int) Decision {
	if !class.valid() {
		return Decision{Admitted: false, Reason: fmt.Sprintf("unknown workload class %d", int(class))}
	}
	if amount <= 0 {
		return Decision{Admitted: false, Reason: "amount must be > 0"}
	}

	prospectiveClassUse := r.used[class] + amount
	prospectiveTotal := r.totalUsedLocked() + amount

	// Never exceed physical capacity.
	if prospectiveTotal > r.capacity {
		return Decision{
			Admitted: false,
			Reason:   fmt.Sprintf("pool full: %d/%d units used, request %d", r.totalUsedLocked(), r.capacity, amount),
		}
	}

	switch class {
	case Interactive:
		// Interactive may grow up to its reserve PLUS any batch-reserved units
		// that batch is not currently using (interactive may borrow idle batch
		// capacity). It is bounded only by total pool capacity, which is already
		// checked above. So interactive is admitted here.
		return Decision{Admitted: true, Reason: "interactive admitted within pool capacity"}

	case Batch:
		// Batch may never consume interactive's reserve. Its ceiling is the batch
		// reserve, expanded only by interactive's idle slack BELOW the high-water
		// mark. We compute the batch ceiling as: batch reserve + (interactive
		// reserve currently unused), but clamped so it never eats interactive's
		// reserve that interactive could still claim... which is exactly the batch
		// reserve. To honor "batch cannot consume interactive's reserve", the hard
		// rule is: batch in-use may not exceed its reserve.
		batchCeiling := r.reserve[Batch]

		// Starvation hard-cap (Gepetto): once total load reaches the high-water
		// mark, batch is clamped to batchHardCapUse so it cannot starve
		// interactive headroom.
		loadAfter := prospectiveTotal
		if loadAfter >= r.highWaterUnits || r.totalUsedLocked() >= r.highWaterUnits {
			if r.batchHardCapUse < batchCeiling {
				batchCeiling = r.batchHardCapUse
			}
			if prospectiveClassUse > batchCeiling {
				return Decision{
					Admitted: false,
					Reason: fmt.Sprintf(
						"batch hard-capped at %d units above high-water %d/%d (would be %d)",
						batchCeiling, r.totalUsedLocked(), r.highWaterUnits, prospectiveClassUse),
				}
			}
			return Decision{Admitted: true, Reason: "batch admitted within hard cap under load"}
		}

		if prospectiveClassUse > batchCeiling {
			return Decision{
				Admitted: false,
				Reason: fmt.Sprintf(
					"batch denied: would use %d > batch reserve %d (interactive reserve protected)",
					prospectiveClassUse, batchCeiling),
			}
		}
		return Decision{Admitted: true, Reason: "batch admitted within reserve"}

	default:
		return Decision{Admitted: false, Reason: "unknown workload class"}
	}
}

// Admit reports whether a new allocation of amount units for class would be
// granted under the current split and load WITHOUT mutating state. It is the
// pure decision function; Reserve applies it and then commits.
func (r *Reservation) Admit(class WorkloadClass, amount int) Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.admitLocked(class, amount)
}

// Reserve attempts to claim amount units for class. On success it commits the
// usage and returns a nil error; on denial it returns an error describing the
// admission decision and leaves usage unchanged.
func (r *Reservation) Reserve(class WorkloadClass, amount int) (Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	d := r.admitLocked(class, amount)
	if !d.Admitted {
		return d, fmt.Errorf("reservation denied for %s: %s", class, d.Reason)
	}
	r.used[class] += amount
	return d, nil
}

// Release returns amount units previously reserved for class back to the pool.
// Releasing more than is currently in use for the class is an error and leaves
// usage unchanged, so a buggy caller cannot drive usage negative and silently
// hand the freed-but-never-held units to the other class.
func (r *Reservation) Release(class WorkloadClass, amount int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !class.valid() {
		return fmt.Errorf("reservation: unknown workload class %d", int(class))
	}
	if amount <= 0 {
		return fmt.Errorf("reservation: release amount must be > 0, got %d", amount)
	}
	if amount > r.used[class] {
		return fmt.Errorf("reservation: cannot release %d units of %s; only %d in use", amount, class, r.used[class])
	}
	r.used[class] -= amount
	return nil
}

// AvailableFor reports how many additional units class could currently reserve,
// accounting for the class split, the pool capacity, and the starvation
// hard-cap. It is consistent with Admit: AvailableFor(c) == n implies
// Admit(c, n) is admitted and Admit(c, n+1) is not (when n+1 <= capacity).
func (r *Reservation) AvailableFor(class WorkloadClass) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !class.valid() {
		return 0
	}

	// Binary-free linear probe is unnecessary; compute ceilings directly.
	poolFree := r.capacity - r.totalUsedLocked()
	if poolFree <= 0 {
		return 0
	}

	switch class {
	case Interactive:
		// Interactive is bounded only by free pool capacity.
		return poolFree

	case Batch:
		batchCeiling := r.reserve[Batch]
		// If load is already at/above high-water, the hard cap applies to the
		// CURRENT state. Otherwise, adding batch units could cross the high-water
		// mark, at which point the hard cap applies to the post-add state. We take
		// the binding constraint by probing the smallest ceiling that holds.
		// Simplest correct form: the batch ceiling under the hard cap is
		// min(batchReserve, batchHardCapUse) whenever any admitted unit would put
		// total load at/above high-water; below that, it's batchReserve.
		headroomToReserve := batchCeiling - r.used[Batch]
		if headroomToReserve <= 0 {
			return 0
		}
		// Determine whether reserving even one unit reaches the high-water mark.
		crossesHighWater := r.totalUsedLocked()+1 >= r.highWaterUnits
		if crossesHighWater {
			capped := r.batchHardCapUse - r.used[Batch]
			if capped < 0 {
				capped = 0
			}
			if capped < headroomToReserve {
				headroomToReserve = capped
			}
		}
		if headroomToReserve > poolFree {
			headroomToReserve = poolFree
		}
		return headroomToReserve

	default:
		return 0
	}
}

// Used reports the current in-use units for class.
func (r *Reservation) Used(class WorkloadClass) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.used[class]
}

// Reserved reports the configured reserved-unit ceiling for class.
func (r *Reservation) Reserved(class WorkloadClass) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reserve[class]
}

// Capacity returns the total pool capacity in units.
func (r *Reservation) Capacity() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capacity
}
