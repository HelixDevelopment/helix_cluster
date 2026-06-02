package metering

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
)

// runUUID derives a deterministic run identifier from a seed string so each
// test logs a stable, traceable UUID-like token (no time.Now, no randomness).
func runUUID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(h[0:4]),
		binary.BigEndian.Uint16(h[4:6]),
		binary.BigEndian.Uint16(h[6:8]),
		binary.BigEndian.Uint16(h[8:10]),
		h[10:16])
}

// TestClosure1_RegisterClaimMeterPayout covers CLOSURE clause 1: a provider
// registers (capability proven), claims a bounty, serves N metered invocations
// totalling U units, and Payout == bountyReward + U*ratePerUnit. The expected
// value is DERIVED from the per-invocation inputs, not hardcoded.
//
// Mutation: dropping the units term (float64(totalUnits)*ratePerUnit) from
// Payout makes after==bountyReward != wantPayout, failing this test.
func TestClosure1_RegisterClaimMeterPayout(t *testing.T) {
	id := runUUID("closure1-register-claim-meter-payout")
	t.Logf("run-uuid=%s START closure=1", id)

	const (
		providerID   = "provider-alpha"
		bountyID     = "bounty-42"
		ratePerUnit  = 0.25
		bountyReward = 10.0
	)
	// N metered invocations with distinct unit counts; U is derived by summing.
	invocations := []int{3, 7, 5, 1, 4}

	m := NewMarket()

	// before: nothing accrued yet.
	before := m.Payout(providerID, ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s before-payout=%v", id, before)
	if before != 0 {
		t.Fatalf("before payout: got %v, want 0 (unregistered provider must accrue nothing)", before)
	}

	if err := m.Register(providerID, true); err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}
	if err := m.ClaimBounty(providerID, bountyID); err != nil {
		t.Fatalf("ClaimBounty: unexpected error: %v", err)
	}

	var wantUnits int64
	for i, u := range invocations {
		if err := m.Meter(providerID, u); err != nil {
			t.Fatalf("Meter[%d](%d): unexpected error: %v", i, u, err)
		}
		wantUnits += int64(u)
	}

	if gotUnits := m.TotalUnits(providerID); gotUnits != wantUnits {
		t.Fatalf("TotalUnits: got %d, want %d", gotUnits, wantUnits)
	}

	// Derived expected payout — independent recomputation from raw inputs.
	wantPayout := bountyReward + float64(wantUnits)*ratePerUnit
	after := m.Payout(providerID, ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s after-payout=%v want=%v units=%d", id, after, wantPayout, wantUnits)

	if after != wantPayout {
		t.Fatalf("Payout: got %v, want %v (bountyReward=%v + units=%d*rate=%v)",
			after, wantPayout, bountyReward, wantUnits, ratePerUnit)
	}
	// Guard the bounty term itself is present: payout must strictly exceed the
	// pure usage term, proving bountyReward is included.
	if after <= float64(wantUnits)*ratePerUnit {
		t.Fatalf("Payout %v does not include bountyReward %v", after, bountyReward)
	}
	t.Logf("run-uuid=%s END closure=1 OK", id)
}

// TestClosure2_LifecycleEnforcement covers CLOSURE clause 2: metering before
// claiming, claiming before registering, and registering with capOK=false all
// yield typed errors and accrue no payout.
//
// Mutation: allowing meter-before-claim (removing the !p.claimed guard in
// Meter) makes the meter-before-claim subtest's error nil, failing the test.
func TestClosure2_LifecycleEnforcement(t *testing.T) {
	id := runUUID("closure2-lifecycle-enforcement")
	t.Logf("run-uuid=%s START closure=2", id)

	const (
		ratePerUnit  = 1.5
		bountyReward = 4.0
	)

	t.Run("claim-before-register", func(t *testing.T) {
		m := NewMarket()
		before := m.Payout("p", ratePerUnit, bountyReward)
		err := m.ClaimBounty("p", "b1")
		after := m.Payout("p", ratePerUnit, bountyReward)
		t.Logf("run-uuid=%s claim-before-register err=%v before=%v after=%v", id, err, before, after)
		if !errors.Is(err, ErrNotRegistered) {
			t.Fatalf("ClaimBounty without Register: got %v, want ErrNotRegistered", err)
		}
		if after != 0 {
			t.Fatalf("payout after failed claim: got %v, want 0", after)
		}
	})

	t.Run("meter-before-claim", func(t *testing.T) {
		m := NewMarket()
		if err := m.Register("p", true); err != nil {
			t.Fatalf("Register: %v", err)
		}
		// Registered but no bounty claimed: metering must be rejected.
		err := m.Meter("p", 99)
		after := m.Payout("p", ratePerUnit, bountyReward)
		t.Logf("run-uuid=%s meter-before-claim err=%v after=%v units=%d", id, err, after, m.TotalUnits("p"))
		if !errors.Is(err, ErrNoBountyClaimed) {
			t.Fatalf("Meter without ClaimBounty: got %v, want ErrNoBountyClaimed", err)
		}
		if u := m.TotalUnits("p"); u != 0 {
			t.Fatalf("units accrued despite rejected Meter: got %d, want 0", u)
		}
		// No claim => payout is 0 regardless of rates.
		if after != 0 {
			t.Fatalf("payout after rejected meter (no claim): got %v, want 0", after)
		}
	})

	t.Run("register-cap-not-proven", func(t *testing.T) {
		m := NewMarket()
		err := m.Register("p", false)
		t.Logf("run-uuid=%s register-cap-false err=%v", id, err)
		if !errors.Is(err, ErrCapabilityNotProven) {
			t.Fatalf("Register(capOK=false): got %v, want ErrCapabilityNotProven", err)
		}
		// A provider rejected at registration cannot claim or meter.
		if err := m.ClaimBounty("p", "b1"); !errors.Is(err, ErrNotRegistered) {
			t.Fatalf("ClaimBounty after failed Register: got %v, want ErrNotRegistered", err)
		}
		if err := m.Meter("p", 5); !errors.Is(err, ErrNotRegistered) {
			t.Fatalf("Meter after failed Register: got %v, want ErrNotRegistered", err)
		}
		after := m.Payout("p", ratePerUnit, bountyReward)
		t.Logf("run-uuid=%s register-cap-false after=%v", id, after)
		if after != 0 {
			t.Fatalf("payout for never-registered provider: got %v, want 0", after)
		}
	})

	t.Logf("run-uuid=%s END closure=2 OK", id)
}

// TestClosure3_ExactMetering covers CLOSURE clause 3: payout reflects the
// precise summed units. Two distinct usage profiles must produce payouts that
// differ by exactly (deltaUnits * ratePerUnit), proving the units term is
// exact rather than approximate or dropped.
//
// Mutation: dropping or mis-scaling the units term makes the two payouts equal
// (or breaks the exact delta), failing this test.
func TestClosure3_ExactMetering(t *testing.T) {
	id := runUUID("closure3-exact-metering")
	t.Logf("run-uuid=%s START closure=3", id)

	const (
		ratePerUnit  = 0.5
		bountyReward = 2.0
	)

	build := func(label string, units []int) (*Market, int64) {
		m := NewMarket()
		pid := "prov-" + label
		if err := m.Register(pid, true); err != nil {
			t.Fatalf("[%s] Register: %v", label, err)
		}
		if err := m.ClaimBounty(pid, "bounty-"+label); err != nil {
			t.Fatalf("[%s] ClaimBounty: %v", label, err)
		}
		var sum int64
		for i, u := range units {
			if err := m.Meter(pid, u); err != nil {
				t.Fatalf("[%s] Meter[%d]: %v", label, i, err)
			}
			sum += int64(u)
		}
		return m, sum
	}

	lowUnits := []int{2, 2, 1}  // sum 5
	highUnits := []int{6, 4, 7} // sum 17

	mLow, sumLow := build("low", lowUnits)
	mHigh, sumHigh := build("high", highUnits)

	payLow := mLow.Payout("prov-low", ratePerUnit, bountyReward)
	payHigh := mHigh.Payout("prov-high", ratePerUnit, bountyReward)

	wantLow := bountyReward + float64(sumLow)*ratePerUnit
	wantHigh := bountyReward + float64(sumHigh)*ratePerUnit
	t.Logf("run-uuid=%s low: units=%d payout=%v want=%v", id, sumLow, payLow, wantLow)
	t.Logf("run-uuid=%s high: units=%d payout=%v want=%v", id, sumHigh, payHigh, wantHigh)

	if payLow != wantLow {
		t.Fatalf("low payout: got %v, want %v", payLow, wantLow)
	}
	if payHigh != wantHigh {
		t.Fatalf("high payout: got %v, want %v", payHigh, wantHigh)
	}

	// The payout difference must equal exactly the metered-unit difference
	// scaled by the rate — this anchors "exact metering".
	gotDelta := payHigh - payLow
	wantDelta := float64(sumHigh-sumLow) * ratePerUnit
	t.Logf("run-uuid=%s delta got=%v want=%v (deltaUnits=%d)", id, gotDelta, wantDelta, sumHigh-sumLow)
	if gotDelta != wantDelta {
		t.Fatalf("payout delta: got %v, want %v (units must be exact)", gotDelta, wantDelta)
	}
	if gotDelta == 0 {
		t.Fatalf("payout delta is 0 — units term not reflected in payout")
	}
	t.Logf("run-uuid=%s END closure=3 OK", id)
}

// TestSingleClaimEnforced verifies a provider may claim at most one bounty,
// reinforcing the single-claim model.
//
// Mutation: removing the p.claimed guard in ClaimBounty makes the second claim
// succeed, failing this test.
func TestSingleClaimEnforced(t *testing.T) {
	id := runUUID("single-claim-enforced")
	t.Logf("run-uuid=%s START single-claim", id)
	m := NewMarket()
	if err := m.Register("p", true); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.ClaimBounty("p", "b1"); err != nil {
		t.Fatalf("first ClaimBounty: %v", err)
	}
	err := m.ClaimBounty("p", "b2")
	t.Logf("run-uuid=%s second-claim err=%v", id, err)
	if !errors.Is(err, ErrBountyAlreadyClaimed) {
		t.Fatalf("second ClaimBounty: got %v, want ErrBountyAlreadyClaimed", err)
	}
	t.Logf("run-uuid=%s END single-claim OK", id)
}

// TestEmptyBountyIDRejected kills M6: ClaimBounty on a registered provider with
// an empty bountyID must return ErrEmptyBountyID and accrue no claim/payout. A
// later valid claim with a non-empty id must still succeed, proving the empty
// call did not silently mark the provider as claimed.
//
// Mutation: removing the `if bountyID == "" { return ErrEmptyBountyID }` guard
// in ClaimBounty lets the empty claim succeed (err==nil and p.claimed=true),
// which fails the errors.Is assertion and the post-state checks below.
func TestEmptyBountyIDRejected(t *testing.T) {
	id := runUUID("empty-bounty-id-rejected")
	t.Logf("run-uuid=%s START empty-bounty-id", id)

	const (
		providerID   = "provider-m6"
		ratePerUnit  = 0.5
		bountyReward = 9.0
	)
	m := NewMarket()
	if err := m.Register(providerID, true); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// before: registered, no bounty -> payout is 0 (no claim).
	before := m.Payout(providerID, ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s before-payout=%v", id, before)
	if before != 0 {
		t.Fatalf("before payout (registered, unclaimed): got %v, want 0", before)
	}

	err := m.ClaimBounty(providerID, "")
	t.Logf("run-uuid=%s empty-claim err=%v", id, err)
	if !errors.Is(err, ErrEmptyBountyID) {
		t.Fatalf("ClaimBounty(%q, \"\"): got %v, want ErrEmptyBountyID", providerID, err)
	}

	// No claim accrued: payout must still be 0 because the empty claim was rejected.
	after := m.Payout(providerID, ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s after-empty-claim payout=%v", id, after)
	if after != 0 {
		t.Fatalf("payout after rejected empty claim: got %v, want 0 (no claim must accrue)", after)
	}

	// Side-effect proof: the rejected empty claim must NOT have marked the
	// provider as claimed — a subsequent valid claim must succeed.
	if err := m.ClaimBounty(providerID, "bounty-real"); err != nil {
		t.Fatalf("valid ClaimBounty after rejected empty claim: got %v, want nil (empty claim must not mutate state)", err)
	}
	// Now claimed: bountyReward accrues with zero metered units.
	wantPayout := bountyReward // 0 units metered
	post := m.Payout(providerID, ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s post-valid-claim payout=%v want=%v", id, post, wantPayout)
	if post != wantPayout {
		t.Fatalf("payout after valid claim: got %v, want %v", post, wantPayout)
	}
	t.Logf("run-uuid=%s END empty-bounty-id OK", id)
}

// TestDuplicateRegistrationRejected kills M7: registering the same providerID
// twice must return ErrAlreadyRegistered on the second call, and the first
// registration's state must be unchanged.
//
// Mutation: removing the `if _, ok := m.providers[providerID]; ok { return
// ErrAlreadyRegistered }` guard in Register lets the second Register overwrite
// the existing provider with a fresh &provider{} — wiping the prior claim and
// metered units. The post-state checks below then fail.
func TestDuplicateRegistrationRejected(t *testing.T) {
	id := runUUID("duplicate-registration-rejected")
	t.Logf("run-uuid=%s START duplicate-registration", id)

	const (
		providerID   = "provider-m7"
		bountyID     = "bounty-m7"
		ratePerUnit  = 2.0
		bountyReward = 3.0
		units        = 8
	)
	m := NewMarket()
	if err := m.Register(providerID, true); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := m.ClaimBounty(providerID, bountyID); err != nil {
		t.Fatalf("ClaimBounty: %v", err)
	}
	if err := m.Meter(providerID, units); err != nil {
		t.Fatalf("Meter: %v", err)
	}

	// Capture first-registration state before the duplicate attempt.
	beforeUnits := m.TotalUnits(providerID)
	wantPayout := bountyReward + float64(units)*ratePerUnit
	beforePayout := m.Payout(providerID, ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s before-dup units=%d payout=%v want=%v", id, beforeUnits, beforePayout, wantPayout)
	if beforeUnits != int64(units) {
		t.Fatalf("pre-dup units: got %d, want %d", beforeUnits, units)
	}
	if beforePayout != wantPayout {
		t.Fatalf("pre-dup payout: got %v, want %v", beforePayout, wantPayout)
	}

	// Duplicate registration must be rejected.
	err := m.Register(providerID, true)
	t.Logf("run-uuid=%s dup-register err=%v", id, err)
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("second Register(%q): got %v, want ErrAlreadyRegistered", providerID, err)
	}

	// Side-effect proof: the first registration's claim + metered units survive.
	afterUnits := m.TotalUnits(providerID)
	afterPayout := m.Payout(providerID, ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s after-dup units=%d payout=%v", id, afterUnits, afterPayout)
	if afterUnits != int64(units) {
		t.Fatalf("units after rejected duplicate register: got %d, want %d (state must be unchanged)", afterUnits, units)
	}
	if afterPayout != wantPayout {
		t.Fatalf("payout after rejected duplicate register: got %v, want %v (claim must survive)", afterPayout, wantPayout)
	}
	t.Logf("run-uuid=%s END duplicate-registration OK", id)
}

// TestEmptyProviderIDRejected kills M8: Register("", true) must return
// ErrNotRegistered, and the empty id must not become registered (a later
// ClaimBounty on "" must still report ErrNotRegistered).
//
// Mutation: removing the `if providerID == "" { return ErrNotRegistered }`
// guard in Register registers the empty id (err==nil, providers[""] created),
// which fails the errors.Is assertion and the "must not be registered" check.
func TestEmptyProviderIDRejected(t *testing.T) {
	id := runUUID("empty-provider-id-rejected")
	t.Logf("run-uuid=%s START empty-provider-id", id)

	const (
		ratePerUnit  = 1.0
		bountyReward = 7.0
	)
	m := NewMarket()

	err := m.Register("", true)
	t.Logf("run-uuid=%s register-empty-id err=%v", id, err)
	if !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("Register(\"\", true): got %v, want ErrNotRegistered", err)
	}

	// Side-effect proof: the empty id must NOT have become registered. A claim
	// on it must still report ErrNotRegistered (not ErrEmptyBountyID, which
	// would mean it passed the registration check).
	claimErr := m.ClaimBounty("", "bounty-x")
	t.Logf("run-uuid=%s claim-empty-id err=%v", id, claimErr)
	if !errors.Is(claimErr, ErrNotRegistered) {
		t.Fatalf("ClaimBounty(\"\", ...) after rejected Register: got %v, want ErrNotRegistered (empty id must not be registered)", claimErr)
	}

	// And no payout can accrue to the empty id.
	after := m.Payout("", ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s empty-id payout=%v", id, after)
	if after != 0 {
		t.Fatalf("payout for empty id: got %v, want 0", after)
	}
	t.Logf("run-uuid=%s END empty-provider-id OK", id)
}

// TestNonPositiveUnitsRejected kills M9: Meter(provider, 0) and
// Meter(provider, -1) on a claimed provider must each return ErrNonPositiveUnits
// and leave TotalUnits unchanged (no units accrue from a rejected Meter).
//
// Mutation: removing the `if units <= 0 { return ErrNonPositiveUnits }` guard in
// Meter lets 0/-1 through: 0 leaves the err nil (failing errors.Is) and -1
// mutates totalUnits to a negative value (failing the unchanged-units check).
func TestNonPositiveUnitsRejected(t *testing.T) {
	id := runUUID("non-positive-units-rejected")
	t.Logf("run-uuid=%s START non-positive-units", id)

	const (
		providerID   = "provider-m9"
		bountyID     = "bounty-m9"
		ratePerUnit  = 4.0
		bountyReward = 1.0
		seedUnits    = 6
	)
	m := NewMarket()
	if err := m.Register(providerID, true); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.ClaimBounty(providerID, bountyID); err != nil {
		t.Fatalf("ClaimBounty: %v", err)
	}
	// Seed a known positive total so a negative leak would be observable.
	if err := m.Meter(providerID, seedUnits); err != nil {
		t.Fatalf("seed Meter: %v", err)
	}

	beforeUnits := m.TotalUnits(providerID)
	t.Logf("run-uuid=%s before-units=%d", id, beforeUnits)
	if beforeUnits != int64(seedUnits) {
		t.Fatalf("seed units: got %d, want %d", beforeUnits, seedUnits)
	}

	for _, bad := range []int{0, -1} {
		err := m.Meter(providerID, bad)
		got := m.TotalUnits(providerID)
		t.Logf("run-uuid=%s meter(%d) err=%v units=%d", id, bad, err, got)
		if !errors.Is(err, ErrNonPositiveUnits) {
			t.Fatalf("Meter(%q, %d): got %v, want ErrNonPositiveUnits", providerID, bad, err)
		}
		if got != int64(seedUnits) {
			t.Fatalf("TotalUnits after rejected Meter(%d): got %d, want %d (no units must accrue)", bad, got, seedUnits)
		}
	}

	// Payout must reflect ONLY the seed units, proving the rejected calls accrued nothing.
	wantPayout := bountyReward + float64(seedUnits)*ratePerUnit
	after := m.Payout(providerID, ratePerUnit, bountyReward)
	t.Logf("run-uuid=%s after-payout=%v want=%v", id, after, wantPayout)
	if after != wantPayout {
		t.Fatalf("payout after rejected non-positive meters: got %v, want %v", after, wantPayout)
	}
	t.Logf("run-uuid=%s END non-positive-units OK", id)
}
