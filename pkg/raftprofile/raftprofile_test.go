package raftprofile_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/raftprofile"
)

// ---------------------------------------------------------------------------
// Helper: build a valid lan-baseline profile, then apply mutations.
// ---------------------------------------------------------------------------

func lanBase() raftprofile.Profile {
	return raftprofile.Profile{
		Name:              "lan",
		HeartbeatInterval: 100 * time.Millisecond,
		ElectionTimeout:   1_000 * time.Millisecond,
		SnapshotCount:     10_000,
		MaxInflightMsgs:   256,
		QuotaBackendBytes: 8 * 1024 * 1024 * 1024,
	}
}

// ---------------------------------------------------------------------------
// TestDefaultProfiles_AllValid — every entry in DefaultProfiles() passes
// Validate() and the three required names are present.
//
// MUTATION PROOF: remove the Validate() call inside DefaultProfiles (i.e. have
//   DefaultProfiles return a profile with HeartbeatInterval=0) → Validate()
//   returns a non-nil error → test fails.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestDefaultProfiles_AllValid(t *testing.T) {
	profiles := raftprofile.DefaultProfiles()

	requiredNames := map[string]bool{
		"lan":          false,
		"wan-regional": false,
		"wan-global":   false,
	}

	for _, p := range profiles {
		if err := p.Validate(); err != nil {
			t.Errorf("DefaultProfiles: profile %q fails Validate: %v", p.Name, err)
		}
		if _, ok := requiredNames[p.Name]; ok {
			requiredNames[p.Name] = true
		}
	}

	for name, seen := range requiredNames {
		if !seen {
			t.Errorf("DefaultProfiles: required profile %q not found in returned slice", name)
		}
	}

	// Assert concrete field values for the lan profile to catch silent changes.
	// MUTATION PROOF: change lan HeartbeatInterval to 50ms → this assertion fails.
	var lan raftprofile.Profile
	for _, p := range profiles {
		if p.Name == "lan" {
			lan = p
		}
	}
	if lan.HeartbeatInterval != 100*time.Millisecond {
		t.Errorf("lan: HeartbeatInterval = %v, want 100ms", lan.HeartbeatInterval)
	}
	if lan.ElectionTimeout != 1_000*time.Millisecond {
		t.Errorf("lan: ElectionTimeout = %v, want 1000ms", lan.ElectionTimeout)
	}
	if lan.SnapshotCount != 10_000 {
		t.Errorf("lan: SnapshotCount = %d, want 10000", lan.SnapshotCount)
	}
	if lan.MaxInflightMsgs != 256 {
		t.Errorf("lan: MaxInflightMsgs = %d, want 256", lan.MaxInflightMsgs)
	}
}

// ---------------------------------------------------------------------------
// TestValidate_RatioConstraint — a profile whose ElectionTimeout < 5×
// HeartbeatInterval is rejected with a clear error message.
//
// MUTATION PROOF: comment out the ratio check in Validate() →
//   the ratio-violating profile erroneously passes → t.Errorf fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestValidate_RatioConstraint(t *testing.T) {
	// 4× — below the 5× minimum.
	bad := lanBase()
	bad.ElectionTimeout = 4 * bad.HeartbeatInterval // 400ms, well below 500ms floor

	err := bad.Validate()
	if err == nil {
		t.Fatal("expected Validate to fail for ElectionTimeout < 5×HeartbeatInterval, got nil")
	}
	// Error must mention the ratio requirement so operators can understand it.
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("error message %q does not mention the ratio bound '5'", err.Error())
	}

	// Exactly 5× must pass.
	ok := lanBase()
	ok.ElectionTimeout = 5 * ok.HeartbeatInterval // 500ms
	if err := ok.Validate(); err != nil {
		t.Errorf("ElectionTimeout == 5×HeartbeatInterval should pass Validate, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestValidate_ZeroHeartbeat — zero HeartbeatInterval is rejected.
//
// MUTATION PROOF: remove the HeartbeatInterval <= 0 guard from Validate entirely →
//   zero heartbeat passes without error → t.Fatal fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestValidate_ZeroHeartbeat(t *testing.T) {
	bad := lanBase()
	bad.HeartbeatInterval = 0

	err := bad.Validate()
	if err == nil {
		t.Fatal("expected Validate to fail for HeartbeatInterval=0, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "heartbeatinterval") {
		t.Errorf("error %q should mention HeartbeatInterval", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestValidate_ZeroElection — zero ElectionTimeout is rejected.
//
// MUTATION PROOF: remove the ElectionTimeout > 0 guard in Validate →
//   zero election passes → t.Fatal fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestValidate_ZeroElection(t *testing.T) {
	bad := lanBase()
	bad.ElectionTimeout = 0

	err := bad.Validate()
	if err == nil {
		t.Fatal("expected Validate to fail for ElectionTimeout=0, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "electiontimeout") {
		t.Errorf("error %q should mention ElectionTimeout", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestValidate_ZeroSnapshotCount — SnapshotCount=0 is rejected.
//
// MUTATION PROOF: remove the SnapshotCount > 0 guard in Validate →
//   zero snapshot count passes → t.Fatal fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestValidate_ZeroSnapshotCount(t *testing.T) {
	bad := lanBase()
	bad.SnapshotCount = 0

	err := bad.Validate()
	if err == nil {
		t.Fatal("expected Validate to fail for SnapshotCount=0, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "snapshotcount") {
		t.Errorf("error %q should mention SnapshotCount", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestValidate_ElectionExceedsHardCap — ElectionTimeout > 50 s is rejected.
//
// MUTATION PROOF: change the > maxElectionTimeout guard to > 2*maxElectionTimeout
//   in Validate → a 51 s election passes → t.Fatal fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestValidate_ElectionExceedsHardCap(t *testing.T) {
	bad := lanBase()
	// Use a heartbeat that still satisfies the 5× ratio but pushes election > 50s.
	// heartbeat = 6s, election = 51s  → ratio = 8.5× (valid) but > 50s hard cap.
	bad.HeartbeatInterval = 6 * time.Second
	bad.ElectionTimeout = 51 * time.Second

	err := bad.Validate()
	if err == nil {
		t.Fatal("expected Validate to fail for ElectionTimeout > 50s, got nil")
	}
	if !strings.Contains(err.Error(), "50") {
		t.Errorf("error %q should mention the 50s cap", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestValidate_ZeroMaxInflightMsgs — MaxInflightMsgs=0 is rejected.
//
// MUTATION PROOF: remove the MaxInflightMsgs > 0 guard in Validate →
//   zero inflight passes → t.Fatal fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestValidate_ZeroMaxInflightMsgs(t *testing.T) {
	bad := lanBase()
	bad.MaxInflightMsgs = 0

	err := bad.Validate()
	if err == nil {
		t.Fatal("expected Validate to fail for MaxInflightMsgs=0, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "maxinflightmsgs") {
		t.Errorf("error %q should mention MaxInflightMsgs", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestProfileFor_Selection — verifies the exact Profile.Name returned for a
// set of representative RTT values against the table below:
//
//	rtt=0   → "lan"          (0ms < 100ms heartbeat)
//	rtt=50  → "lan"          (50ms < 100ms heartbeat → lan covers it)
//	rtt=100 → "lan"          (100ms == 100ms heartbeat → exactly covered)
//	rtt=101 → "wan-regional" (101ms > 100ms → need 250ms heartbeat)
//	rtt=250 → "wan-regional" (250ms == 250ms heartbeat)
//	rtt=251 → "wan-global"   (251ms > 250ms → need 500ms heartbeat)
//	rtt=300 → "wan-global"   (300ms < 500ms heartbeat)
//	rtt=600 → "wan-global"   (600ms > all profiles → largest returned)
//
// MUTATION PROOF: reverse the comparison in ProfileFor's sort.Slice from "<"
//   to ">" (descending) → the first matching profile is the wrong (largest)
//   one → all but the rtt=600 case get the wrong Name → t.Errorf fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestProfileFor_Selection(t *testing.T) {
	cases := []struct {
		rttMillis   int
		wantProfile string
	}{
		{rttMillis: 0, wantProfile: "lan"},
		{rttMillis: 50, wantProfile: "lan"},
		{rttMillis: 100, wantProfile: "lan"},
		{rttMillis: 101, wantProfile: "wan-regional"},
		{rttMillis: 250, wantProfile: "wan-regional"},
		{rttMillis: 251, wantProfile: "wan-global"},
		{rttMillis: 300, wantProfile: "wan-global"},
		{rttMillis: 600, wantProfile: "wan-global"},
	}

	for _, tc := range cases {
		got := raftprofile.ProfileFor(tc.rttMillis)
		if got.Name != tc.wantProfile {
			t.Errorf("ProfileFor(%d): got profile %q, want %q",
				tc.rttMillis, got.Name, tc.wantProfile)
		}
		// Also assert that the selected profile itself is valid.
		if err := got.Validate(); err != nil {
			t.Errorf("ProfileFor(%d) returned invalid profile %q: %v",
				tc.rttMillis, got.Name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// TestRenderFlags_ParseFlags_RoundTrip — RenderFlags followed by ParseFlags
// must reproduce all Profile fields exactly.
//
// The test exercises all three default profiles plus a synthetic one to ensure
// the round-trip is driven by the actual field values, not by constants.
//
// MUTATION PROOF: hardcode "--heartbeat-interval=100" in RenderFlags
//   (ignoring p.HeartbeatInterval) → ParseFlags recovers 100 ms for all
//   profiles → the wan-regional and wan-global assertions fail.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestRenderFlags_ParseFlags_RoundTrip(t *testing.T) {
	profiles := raftprofile.DefaultProfiles()

	// Append a synthetic profile with unusual values to rule out lucky
	// coincidences between default constants and a broken RenderFlags.
	profiles = append(profiles, raftprofile.Profile{
		Name:              "synthetic-test",
		HeartbeatInterval: 333 * time.Millisecond,
		ElectionTimeout:   3_330 * time.Millisecond, // 10× exactly
		SnapshotCount:     5_555,
		MaxInflightMsgs:   128,
		QuotaBackendBytes: 4 * 1024 * 1024 * 1024,
	})

	for _, orig := range profiles {
		flags := orig.RenderFlags()
		got, err := raftprofile.ParseFlags(flags)
		if err != nil {
			t.Errorf("profile %q: ParseFlags returned error: %v", orig.Name, err)
			continue
		}

		if got.Name != orig.Name {
			t.Errorf("profile %q: Name round-trip: got %q, want %q",
				orig.Name, got.Name, orig.Name)
		}
		if got.HeartbeatInterval != orig.HeartbeatInterval {
			t.Errorf("profile %q: HeartbeatInterval round-trip: got %v, want %v",
				orig.Name, got.HeartbeatInterval, orig.HeartbeatInterval)
		}
		if got.ElectionTimeout != orig.ElectionTimeout {
			t.Errorf("profile %q: ElectionTimeout round-trip: got %v, want %v",
				orig.Name, got.ElectionTimeout, orig.ElectionTimeout)
		}
		if got.SnapshotCount != orig.SnapshotCount {
			t.Errorf("profile %q: SnapshotCount round-trip: got %d, want %d",
				orig.Name, got.SnapshotCount, orig.SnapshotCount)
		}
		if got.MaxInflightMsgs != orig.MaxInflightMsgs {
			t.Errorf("profile %q: MaxInflightMsgs round-trip: got %d, want %d",
				orig.Name, got.MaxInflightMsgs, orig.MaxInflightMsgs)
		}
		if got.QuotaBackendBytes != orig.QuotaBackendBytes {
			t.Errorf("profile %q: QuotaBackendBytes round-trip: got %d, want %d",
				orig.Name, got.QuotaBackendBytes, orig.QuotaBackendBytes)
		}
	}
}

// ---------------------------------------------------------------------------
// TestParseFlags_MissingRequired — ParseFlags returns ErrMissingFlag when a
// required flag is absent.
//
// MUTATION PROOF: remove the "required" check in parseInt64 inside ParseFlags →
//   missing flags silently default to 0 → ErrMissingFlag is never returned →
//   errors.Is(err, ErrMissingFlag) returns false → t.Errorf fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestParseFlags_MissingRequired(t *testing.T) {
	required := []string{
		"heartbeat-interval",
		"election-timeout",
		"snapshot-count",
		"max-inflight-msgs",
		"quota-backend-bytes",
	}

	// Build a full flag set and drop one flag at a time.
	full := lanBase().RenderFlags()

	for _, drop := range required {
		var partial []string
		for _, f := range full {
			if !strings.Contains(f, "--"+drop+"=") {
				partial = append(partial, f)
			}
		}
		_, err := raftprofile.ParseFlags(partial)
		if err == nil {
			t.Errorf("ParseFlags without --%s: expected error, got nil", drop)
			continue
		}
		// Must wrap ErrMissingFlag — not a generic error or ErrInvalidFlag.
		if !errors.Is(err, raftprofile.ErrMissingFlag) {
			t.Errorf("ParseFlags without --%s: got %v, want errors.Is(err, ErrMissingFlag)==true", drop, err)
		}
		// The flag name must appear in the error so operators know which flag is absent.
		if !strings.Contains(err.Error(), drop) {
			t.Errorf("ParseFlags without --%s: error %q does not mention the flag name", drop, err.Error())
		}
	}
}

// ---------------------------------------------------------------------------
// TestRenderFlags_FlagCount — RenderFlags must produce exactly 6 flags.
//
// MUTATION PROOF: remove the --name= line from RenderFlags →
//   len(flags) == 5 → t.Errorf fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestRenderFlags_FlagCount(t *testing.T) {
	p := lanBase()
	flags := p.RenderFlags()
	const wantCount = 6
	if len(flags) != wantCount {
		t.Errorf("RenderFlags returned %d flags, want %d: %v", len(flags), wantCount, flags)
	}
	// Every flag must start with "--".
	for _, f := range flags {
		if !strings.HasPrefix(f, "--") {
			t.Errorf("flag %q does not start with '--'", f)
		}
	}
}

// ---------------------------------------------------------------------------
// TestValidate_ValidProfile — a correctly constructed profile passes Validate
// without error, serving as a baseline control.
//
// MUTATION PROOF: add an unconditional return fmt.Errorf("always fail") at the
//   top of Validate → this test fails.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestValidate_ValidProfile(t *testing.T) {
	p := lanBase()
	if err := p.Validate(); err != nil {
		t.Fatalf("lanBase() should be valid, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestDefaultProfiles_ElectionRatio — all default profiles use the recommended
// 10× ratio between ElectionTimeout and HeartbeatInterval.
//
// MUTATION PROOF: change wan-global ElectionTimeout to 4000ms (8×) →
//   ratio assertion for wan-global fails.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestDefaultProfiles_ElectionRatio(t *testing.T) {
	for _, p := range raftprofile.DefaultProfiles() {
		ratio := p.ElectionTimeout / p.HeartbeatInterval
		if ratio != 10 {
			t.Errorf("profile %q: ElectionTimeout/HeartbeatInterval ratio = %d, want 10",
				p.Name, ratio)
		}
	}
}

// ---------------------------------------------------------------------------
// TestValidate_ElectionRatioUpperBound — a profile whose ElectionTimeout
// exceeds 10× HeartbeatInterval is rejected by Validate, proving maxElectionRatio
// is actively enforced and not dead code.
//
// MUTATION PROOF: remove the maxElection upper-bound check from Validate →
//   the 11× profile erroneously passes → t.Errorf fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestValidate_ElectionRatioUpperBound(t *testing.T) {
	// 11× — above the 10× maximum.
	bad := lanBase()
	bad.ElectionTimeout = 11 * bad.HeartbeatInterval // 1100ms, above 1000ms ceiling

	err := bad.Validate()
	if err == nil {
		t.Fatal("expected Validate to fail for ElectionTimeout > 10×HeartbeatInterval, got nil")
	}
	// Error must mention the ratio bound so operators understand the constraint.
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("error message %q does not mention the upper ratio bound '10'", err.Error())
	}

	// Exactly 10× must pass (the boundary is inclusive).
	ok := lanBase()
	ok.ElectionTimeout = 10 * ok.HeartbeatInterval // 1000ms == 10× (boundary)
	if err := ok.Validate(); err != nil {
		t.Errorf("ElectionTimeout == 10×HeartbeatInterval should pass Validate, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestParseFlags_InvalidValue — ParseFlags returns ErrInvalidFlag when a
// numeric flag is supplied a non-numeric value.
//
// MUTATION PROOF: remove the strconv.ParseInt error path in parseInt64 (return
//   n=0, err=nil on parse failure) → ErrInvalidFlag is never returned →
//   errors.Is(err, ErrInvalidFlag) returns false → t.Errorf fires.
// The mutation still compiles.
// ---------------------------------------------------------------------------

func TestParseFlags_InvalidValue(t *testing.T) {
	cases := []struct {
		flag string
		val  string
	}{
		{"heartbeat-interval", "notanumber"},
		{"election-timeout", "1s"}, // duration string, not a bare integer
		{"snapshot-count", ""},
		{"max-inflight-msgs", "256.5"},
		{"quota-backend-bytes", "8GiB"},
	}

	for _, tc := range cases {
		flags := lanBase().RenderFlags()
		// Replace the target flag with an invalid value.
		replaced := make([]string, 0, len(flags))
		for _, f := range flags {
			if strings.HasPrefix(f, "--"+tc.flag+"=") {
				replaced = append(replaced, "--"+tc.flag+"="+tc.val)
			} else {
				replaced = append(replaced, f)
			}
		}
		_, err := raftprofile.ParseFlags(replaced)
		if err == nil {
			t.Errorf("ParseFlags with --%s=%q: expected error, got nil", tc.flag, tc.val)
			continue
		}
		// Must wrap ErrInvalidFlag, not ErrMissingFlag or a raw error.
		if !errors.Is(err, raftprofile.ErrInvalidFlag) {
			t.Errorf("ParseFlags with --%s=%q: got %v, want errors.Is(err, ErrInvalidFlag)==true",
				tc.flag, tc.val, err)
		}
	}
}
