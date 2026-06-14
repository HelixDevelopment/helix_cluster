package raftprofile_test

// Adversarial audit of pkg/raftprofile.
//
// Centerpieces:
//   1. ParseFlags robustness — every malformed / hostile flag input must return
//      a typed error and NEVER panic (TestParseFlags_HostileInput_NeverPanics).
//   2. Raft timing-invariant sweep — for EVERY DefaultProfile the safety
//      relationship electionTimeout > 5× heartbeat (and the documented 5×..10×
//      band) must hold, so the cluster can never be shipped a config that
//      causes perpetual elections (TestDefaultProfiles_RaftTimingInvariants).
//
// These tests are mutation-verified: each documents the canonical production
// mutation that makes it fail. See the FINDING section in the audit report.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/raftprofile"
)

// ===========================================================================
// CENTERPIECE 1 — ParseFlags robustness against hostile / malformed input.
//
// Contract (from the ParseFlags / flagKey godoc):
//   - A malformed flag (no "--", no "=") → typed error, no panic.
//   - A required flag missing            → errors.Is(err, ErrMissingFlag).
//   - A numeric flag with a non-integer / out-of-range value → errors.Is(err,
//     ErrInvalidFlag).
//   - A negative snapshot-count          → errors.Is(err, ErrInvalidFlag)
//     (because uint64 conversion would silently wrap a negative value into a
//     gigantic positive one — a real foot-gun the code guards against).
//   - In NO case may ParseFlags panic.
//
// MUTATION PROOF: in ParseFlags, change `return Profile{}, err` (the flagKey
//   error path) to `continue`, OR make parseInt64 swallow strconv errors
//   (return 0,nil) → hostile inputs stop erroring → the per-case error
//   assertions below fire. A panic in any future refactor is caught by the
//   recover() guard turning into a t.Fatalf.
// ===========================================================================

func TestParseFlags_HostileInput_NeverPanics(t *testing.T) {
	// A complete, valid baseline so we can corrupt exactly one dimension.
	valid := []string{
		"--heartbeat-interval=100",
		"--election-timeout=1000",
		"--snapshot-count=10000",
		"--max-inflight-msgs=256",
		"--quota-backend-bytes=0",
	}

	cases := []struct {
		name    string
		flags   []string
		wantErr error // nil => must succeed; sentinel => errors.Is must match
	}{
		{name: "nil slice", flags: nil, wantErr: raftprofile.ErrMissingFlag},
		{name: "empty slice", flags: []string{}, wantErr: raftprofile.ErrMissingFlag},
		{name: "bare garbage token", flags: []string{"garbage"}, wantErr: errSentinelAny},
		{name: "lone dashes", flags: []string{"--"}, wantErr: errSentinelAny},
		{name: "dashes no key", flags: []string{"--=5"}, wantErr: raftprofile.ErrMissingFlag},
		{name: "empty value", flags: []string{"--heartbeat-interval="}, wantErr: raftprofile.ErrInvalidFlag},
		{name: "non-numeric hb", flags: corrupt(valid, "heartbeat-interval", "notanumber"), wantErr: raftprofile.ErrInvalidFlag},
		{name: "float hb", flags: corrupt(valid, "heartbeat-interval", "10.5"), wantErr: raftprofile.ErrInvalidFlag},
		{name: "hex hb", flags: corrupt(valid, "heartbeat-interval", "0x10"), wantErr: raftprofile.ErrInvalidFlag},
		{name: "plus-prefixed hb", flags: corrupt(valid, "heartbeat-interval", "+100"), wantErr: nil}, // strconv accepts a leading '+'
		{name: "whitespace hb", flags: corrupt(valid, "heartbeat-interval", " 100 "), wantErr: raftprofile.ErrInvalidFlag},
		{name: "int64 overflow hb", flags: corrupt(valid, "heartbeat-interval", "99999999999999999999999999"), wantErr: raftprofile.ErrInvalidFlag},
		{name: "negative snapshot-count wraps", flags: corrupt(valid, "snapshot-count", "-1"), wantErr: raftprofile.ErrInvalidFlag},
		{name: "non-numeric election", flags: corrupt(valid, "election-timeout", "1s"), wantErr: raftprofile.ErrInvalidFlag},
		{name: "non-numeric quota", flags: corrupt(valid, "quota-backend-bytes", "8GiB"), wantErr: raftprofile.ErrInvalidFlag},
		{name: "missing election-timeout", flags: drop(valid, "election-timeout"), wantErr: raftprofile.ErrMissingFlag},
		{name: "embedded NUL in value", flags: corrupt(valid, "max-inflight-msgs", "25\x006"), wantErr: raftprofile.ErrInvalidFlag},
		{name: "all valid baseline", flags: valid, wantErr: nil},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Hard panic guard: a panic on hostile parser input is the bug
			// class this test exists to forbid.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ParseFlags PANICKED on hostile input %v: %v", tc.flags, r)
				}
			}()

			_, err := raftprofile.ParseFlags(tc.flags)

			switch {
			case tc.wantErr == nil:
				if err != nil {
					t.Fatalf("ParseFlags(%v): unexpected error: %v", tc.flags, err)
				}
			case tc.wantErr == errSentinelAny:
				if err == nil {
					t.Fatalf("ParseFlags(%v): expected some error, got nil", tc.flags)
				}
			default:
				if err == nil {
					t.Fatalf("ParseFlags(%v): expected error wrapping %v, got nil", tc.flags, tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseFlags(%v): got %v, want errors.Is(_, %v)==true", tc.flags, err, tc.wantErr)
				}
			}
		})
	}
}

// errSentinelAny marks a case where any non-nil error is acceptable (the exact
// sentinel is an implementation detail, but it must not panic and must error).
var errSentinelAny = errors.New("raftprofile_adversarial: any error accepted")

// corrupt returns a copy of flags with the value of --<key>= replaced by val.
func corrupt(flags []string, key, val string) []string {
	out := make([]string, 0, len(flags))
	prefix := "--" + key + "="
	for _, f := range flags {
		if strings.HasPrefix(f, prefix) {
			out = append(out, prefix+val)
		} else {
			out = append(out, f)
		}
	}
	return out
}

// drop returns a copy of flags with the --<key>= entry removed.
func drop(flags []string, key string) []string {
	out := make([]string, 0, len(flags))
	prefix := "--" + key + "="
	for _, f := range flags {
		if !strings.HasPrefix(f, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// ===========================================================================
// CENTERPIECE 2 — Raft timing-invariant sweep over EVERY DefaultProfile.
//
// Safety relationship being pinned (Raft / etcd operational requirement):
//
//	HeartbeatInterval > 0
//	ElectionTimeout   > HeartbeatInterval            (strictly — never <=)
//	5×HB ≤ ElectionTimeout ≤ 10×HB                   (the documented band)
//	ElectionTimeout   ≤ 50s                          (etcd hard cap)
//	SnapshotCount     > 0
//	MaxInflightMsgs   > 0
//
// A DefaultProfile with electionTimeout <= heartbeat would cause perpetual
// elections (election storm) the instant it is deployed — the single most
// dangerous misconfiguration this package can ship. This test asserts the
// relationship directly on the raw field values, independent of Validate(),
// so that a regression in BOTH the profile data AND Validate() is still caught.
//
// MUTATION PROOF: set any DefaultProfile's ElectionTimeout <= its
//   HeartbeatInterval (e.g. lan election 50ms vs heartbeat 100ms) → the
//   strict-greater and 5× assertions fire for that profile.
// ===========================================================================

func TestDefaultProfiles_RaftTimingInvariants(t *testing.T) {
	profiles := raftprofile.DefaultProfiles()
	if len(profiles) == 0 {
		t.Fatal("DefaultProfiles() returned no profiles")
	}

	const (
		minRatio = 5
		maxRatio = 10
		hardCap  = 50 * time.Second
	)

	for _, p := range profiles {
		p := p
		t.Run(p.Name, func(t *testing.T) {
			// Positivity.
			if p.HeartbeatInterval <= 0 {
				t.Fatalf("%q: HeartbeatInterval = %v, must be > 0", p.Name, p.HeartbeatInterval)
			}
			if p.ElectionTimeout <= 0 {
				t.Fatalf("%q: ElectionTimeout = %v, must be > 0", p.Name, p.ElectionTimeout)
			}
			if p.SnapshotCount == 0 {
				t.Errorf("%q: SnapshotCount = 0, must be > 0", p.Name)
			}
			if p.MaxInflightMsgs <= 0 {
				t.Errorf("%q: MaxInflightMsgs = %d, must be > 0", p.Name, p.MaxInflightMsgs)
			}

			// THE Raft safety relationship: election strictly greater than
			// heartbeat. This is the election-storm guard.
			if p.ElectionTimeout <= p.HeartbeatInterval {
				t.Fatalf("%q: ELECTION STORM RISK: ElectionTimeout (%v) <= HeartbeatInterval (%v)",
					p.Name, p.ElectionTimeout, p.HeartbeatInterval)
			}

			// Documented band: 5× ≤ ratio ≤ 10×. Assert against the actual
			// duration product (not integer division) so a near-miss like
			// 4.9× is still caught.
			minElection := minRatio * p.HeartbeatInterval
			maxElection := maxRatio * p.HeartbeatInterval
			if p.ElectionTimeout < minElection {
				t.Errorf("%q: ElectionTimeout (%v) < %d× HeartbeatInterval (%v) — too close to heartbeat",
					p.Name, p.ElectionTimeout, minRatio, minElection)
			}
			if p.ElectionTimeout > maxElection {
				t.Errorf("%q: ElectionTimeout (%v) > %d× HeartbeatInterval (%v) — failure detection too slow",
					p.Name, p.ElectionTimeout, maxRatio, maxElection)
			}

			// etcd hard cap.
			if p.ElectionTimeout > hardCap {
				t.Errorf("%q: ElectionTimeout (%v) exceeds etcd hard cap %v", p.Name, p.ElectionTimeout, hardCap)
			}

			// Cross-check: the package's own Validate() must agree with the
			// independent invariant assertions above. If Validate passes but
			// the raw relationship is violated (or vice-versa), one of them is
			// lying — surface it.
			if err := p.Validate(); err != nil {
				t.Errorf("%q: Validate() rejects a shipped DefaultProfile: %v", p.Name, err)
			}
		})
	}
}

// ===========================================================================
// ProfileFor correctness — exact selection plus the documented "never
// under-provision" default for an out-of-range RTT, and validity of every
// returned profile (a wrong / dangerous default would surface here).
//
// MUTATION PROOF: in ProfileFor, change the fall-through `return
//   profiles[len(profiles)-1]` to `return profiles[0]` → the huge-RTT case
//   returns "lan" instead of the largest-heartbeat profile → assertion fires.
//   Separately, flip the sort comparator to ">" → the small-RTT cases pick the
//   wrong profile.
// ===========================================================================

func TestProfileFor_SelectionAndSafeDefault(t *testing.T) {
	cases := []struct {
		rttMillis int
		want      string
	}{
		{rttMillis: -1_000, want: "lan"}, // negative RTT → smallest profile, never panics
		{rttMillis: 0, want: "lan"},
		{rttMillis: 100, want: "lan"},
		{rttMillis: 101, want: "wan-regional"},
		{rttMillis: 250, want: "wan-regional"},
		{rttMillis: 251, want: "wan-global"},
		{rttMillis: 500, want: "wan-global"},
		{rttMillis: 10_000, want: "wan-global"}, // beyond all profiles → largest (safe default)
	}

	for _, tc := range cases {
		got := raftprofile.ProfileFor(tc.rttMillis)
		if got.Name != tc.want {
			t.Errorf("ProfileFor(%d): got %q, want %q", tc.rttMillis, got.Name, tc.want)
		}
		// Whatever is selected MUST be a usable profile — a dangerous default
		// (e.g. an unvalidated zero profile) would be caught here.
		if err := got.Validate(); err != nil {
			t.Errorf("ProfileFor(%d) returned invalid profile %q: %v", tc.rttMillis, got.Name, err)
		}
		// And it must satisfy the election-storm guard directly.
		if got.ElectionTimeout <= got.HeartbeatInterval {
			t.Errorf("ProfileFor(%d) returned election-storm profile %q: election %v <= heartbeat %v",
				tc.rttMillis, got.Name, got.ElectionTimeout, got.HeartbeatInterval)
		}
	}
}

// ===========================================================================
// Determinism — ParseFlags and ProfileFor must be pure functions of their
// inputs. Repeated calls return byte-identical results. (DefaultProfiles is
// re-sorted inside ProfileFor; a hidden shared-slice mutation would make a
// second call diverge.)
// ===========================================================================

func TestDeterminism_ProfileFor_And_ParseFlags(t *testing.T) {
	for _, rtt := range []int{-5, 0, 100, 251, 9999} {
		first := raftprofile.ProfileFor(rtt)
		for i := 0; i < 5; i++ {
			again := raftprofile.ProfileFor(rtt)
			if again != first {
				t.Fatalf("ProfileFor(%d) non-deterministic: call0=%+v call%d=%+v", rtt, first, i+1, again)
			}
		}
	}

	flags := raftprofile.DefaultProfiles()[1].RenderFlags() // wan-regional
	first, err := raftprofile.ParseFlags(flags)
	if err != nil {
		t.Fatalf("ParseFlags baseline error: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := raftprofile.ParseFlags(flags)
		if err != nil {
			t.Fatalf("ParseFlags repeat %d error: %v", i, err)
		}
		if again != first {
			t.Fatalf("ParseFlags non-deterministic: call0=%+v call%d=%+v", first, i+1, again)
		}
	}
}

// ===========================================================================
// DefaultProfiles internal consistency — the constant fields (SnapshotCount,
// MaxInflightMsgs, QuotaBackendBytes) are identical across all profiles as the
// godoc promises, and HeartbeatInterval strictly increases lan < regional <
// global (so ProfileFor's ascending scan is well-founded).
//
// MUTATION PROOF: give wan-regional a different SnapshotCount, or set
//   wan-global HeartbeatInterval below wan-regional's → these assertions fire.
// ===========================================================================

func TestDefaultProfiles_InternalConsistency(t *testing.T) {
	byName := map[string]raftprofile.Profile{}
	for _, p := range raftprofile.DefaultProfiles() {
		if _, dup := byName[p.Name]; dup {
			t.Fatalf("duplicate profile name %q in DefaultProfiles", p.Name)
		}
		byName[p.Name] = p
	}

	lan, ok1 := byName["lan"]
	reg, ok2 := byName["wan-regional"]
	glob, ok3 := byName["wan-global"]
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("missing one of the canonical profiles: have %v", keys(byName))
	}

	// Strictly increasing heartbeat tiers.
	if !(lan.HeartbeatInterval < reg.HeartbeatInterval && reg.HeartbeatInterval < glob.HeartbeatInterval) {
		t.Errorf("HeartbeatInterval not strictly increasing: lan=%v regional=%v global=%v",
			lan.HeartbeatInterval, reg.HeartbeatInterval, glob.HeartbeatInterval)
	}

	// Shared constants identical across all profiles (godoc contract).
	for _, p := range []raftprofile.Profile{reg, glob} {
		if p.SnapshotCount != lan.SnapshotCount {
			t.Errorf("%q: SnapshotCount = %d, want shared %d", p.Name, p.SnapshotCount, lan.SnapshotCount)
		}
		if p.MaxInflightMsgs != lan.MaxInflightMsgs {
			t.Errorf("%q: MaxInflightMsgs = %d, want shared %d", p.Name, p.MaxInflightMsgs, lan.MaxInflightMsgs)
		}
		if p.QuotaBackendBytes != lan.QuotaBackendBytes {
			t.Errorf("%q: QuotaBackendBytes = %d, want shared %d", p.Name, p.QuotaBackendBytes, lan.QuotaBackendBytes)
		}
	}
}

func keys(m map[string]raftprofile.Profile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
