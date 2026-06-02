// Package raftprofile codifies per-cell etcd Raft WAN-safe tuning profiles for
// the Helix Cluster OS. It encodes real operational guidance from the etcd
// documentation and enforces invariants that are hard requirements at runtime:
//
//   - HeartbeatInterval must be at least the observed network RTT (documented
//     below; enforced via Validate which rejects zero intervals).
//   - ElectionTimeout must be between 5× and 10× HeartbeatInterval (etcd
//     recommends exactly 10×; it must be >5× to survive a slow heartbeat
//     without spurious re-elections).
//   - ElectionTimeout is bounded by etcd's hard cap of 50 s.
//   - SnapshotCount must be positive (controls when Raft snapshots are taken).
//   - MaxInflightMsgs must be positive (pipeline depth; raise for high-BDP WAN
//     links, lower for memory-constrained nodes).
//   - QuotaBackendBytes controls the boltdb storage limit; the zero value
//     passes validation because etcd treats 0 as "use default" (2 GiB).
//
// # Network-RTT guidance
//
// HeartbeatInterval should be at least the round-trip time of the slowest
// peer in the cluster so that a single lost heartbeat does not cause a
// leader-change. Typical values:
//
//	LAN / same-DC:    RTT  <  5 ms → heartbeat 100 ms
//	WAN regional:     RTT ~ 50-150 ms → heartbeat 250 ms
//	WAN global:       RTT ~ 150-300 ms → heartbeat 500 ms
//
// The ProfileFor helper applies this heuristic automatically.
package raftprofile

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxElectionTimeout is etcd's documented hard cap on the election timeout.
// Values above this are rejected at cluster startup.
const maxElectionTimeout = 50 * time.Second

// minRatioNumerator and minRatioDenominator encode the lower bound of the
// ElectionTimeout/HeartbeatInterval ratio (> 5×).
// We accept exactly 5× as the minimum (etcd says "must be > 5 times", but
// uses ">= 5 * heartbeat" in its own source validation, so we follow the
// more permissive interpretation that etcd actually enforces at startup).
const minElectionRatio = 5

// maxElectionRatio is the hard upper bound for ElectionTimeout as a multiple
// of HeartbeatInterval enforced by Validate(). Values above 10× are rejected
// because they cause unacceptably slow failure-detection and have no operational
// justification; etcd itself recommends exactly 10×.
const maxElectionRatio = 10

// Profile holds a complete set of etcd Raft tuning parameters for one network
// tier. All durations are in terms of Go's time.Duration so callers can
// inspect and manipulate them without unit-conversion arithmetic.
//
// The exported fields map 1-to-1 to etcd startup flags:
//
//	HeartbeatInterval  → --heartbeat-interval (milliseconds)
//	ElectionTimeout    → --election-timeout   (milliseconds)
//	SnapshotCount      → --snapshot-count
//	MaxInflightMsgs    → --max-inflight-msgs
//	QuotaBackendBytes  → --quota-backend-bytes  (0 = use etcd default of 2 GiB)
type Profile struct {
	// Name is a human-readable identifier for this profile, e.g. "lan",
	// "wan-regional", "wan-global". It is used by ProfileFor for deterministic
	// selection and is preserved through RenderFlags/ParseFlags round-trips.
	Name string

	// HeartbeatInterval is how often the leader sends heartbeats to followers.
	// Must be greater than zero. Should be ≥ the worst observed network RTT
	// across all peers in the cluster.
	HeartbeatInterval time.Duration

	// ElectionTimeout is how long a follower waits without a heartbeat before
	// starting an election. Must satisfy:
	//   5 × HeartbeatInterval ≤ ElectionTimeout ≤ 50 s
	ElectionTimeout time.Duration

	// SnapshotCount is the number of committed Raft entries that trigger a
	// snapshot. Must be greater than zero.
	SnapshotCount uint64

	// MaxInflightMsgs is the maximum number of in-flight Raft messages the
	// leader pipelines to a single follower without waiting for an ACK.
	// Must be greater than zero. Increasing this raises throughput on high-BDP
	// WAN links at the cost of more memory use.
	MaxInflightMsgs int

	// QuotaBackendBytes is the maximum size of the boltdb backend in bytes.
	// A value of 0 is accepted and means "use etcd's default (2 GiB)".
	// Operators should set this explicitly in production to avoid silent
	// storage exhaustion.
	QuotaBackendBytes int64
}

// Validate checks that p satisfies all etcd operational constraints.
// It returns a non-nil descriptive error for each violation; only the first
// detected violation is returned (fail-fast) so callers can fix issues one
// at a time.
//
// Constraints enforced (in order):
//  1. HeartbeatInterval > 0
//  2. ElectionTimeout > 0
//  3. ElectionTimeout ≥ 5 × HeartbeatInterval
//  4. ElectionTimeout ≤ 10 × HeartbeatInterval
//  5. ElectionTimeout ≤ 50 s (etcd hard cap)
//  6. SnapshotCount > 0
//  7. MaxInflightMsgs > 0
func (p Profile) Validate() error {
	if p.HeartbeatInterval <= 0 {
		return fmt.Errorf("raftprofile: HeartbeatInterval must be > 0, got %v", p.HeartbeatInterval)
	}
	if p.ElectionTimeout <= 0 {
		return fmt.Errorf("raftprofile: ElectionTimeout must be > 0, got %v", p.ElectionTimeout)
	}
	minElection := time.Duration(minElectionRatio) * p.HeartbeatInterval
	if p.ElectionTimeout < minElection {
		return fmt.Errorf(
			"raftprofile: ElectionTimeout (%v) must be >= %d × HeartbeatInterval (%v = %v)",
			p.ElectionTimeout, minElectionRatio, p.HeartbeatInterval, minElection,
		)
	}
	maxElection := time.Duration(maxElectionRatio) * p.HeartbeatInterval
	if p.ElectionTimeout > maxElection {
		return fmt.Errorf(
			"raftprofile: ElectionTimeout (%v) exceeds %d × HeartbeatInterval (%v = %v); "+
				"tune HeartbeatInterval up or reduce ElectionTimeout",
			p.ElectionTimeout, maxElectionRatio, p.HeartbeatInterval, maxElection,
		)
	}
	if p.ElectionTimeout > maxElectionTimeout {
		return fmt.Errorf(
			"raftprofile: ElectionTimeout (%v) exceeds etcd hard cap of %v",
			p.ElectionTimeout, maxElectionTimeout,
		)
	}
	if p.SnapshotCount == 0 {
		return errors.New("raftprofile: SnapshotCount must be > 0")
	}
	if p.MaxInflightMsgs <= 0 {
		return fmt.Errorf("raftprofile: MaxInflightMsgs must be > 0, got %d", p.MaxInflightMsgs)
	}
	return nil
}

// DefaultProfiles returns the three canonical Helix Cluster OS Raft tuning
// profiles. Every returned profile passes Validate().
//
// The profiles are:
//
//	"lan"          — same datacenter, sub-millisecond RTT; heartbeat 100 ms,
//	                 election 1 000 ms (10×).
//	"wan-regional" — cross-region within a continent (50–150 ms RTT);
//	                 heartbeat 250 ms, election 2 500 ms (10×).
//	"wan-global"   — intercontinental (150–300 ms RTT);
//	                 heartbeat 500 ms, election 5 000 ms (10×).
//
// SnapshotCount is 10 000 (etcd default) and MaxInflightMsgs is 256 for all
// profiles; QuotaBackendBytes is 8 GiB, appropriate for a Helix cell.
func DefaultProfiles() []Profile {
	const (
		snapshotCount     uint64 = 10_000
		maxInflightMsgs          = 256
		quotaBackendBytes int64  = 8 * 1024 * 1024 * 1024 // 8 GiB
	)
	return []Profile{
		{
			Name:              "lan",
			HeartbeatInterval: 100 * time.Millisecond,
			ElectionTimeout:   1_000 * time.Millisecond,
			SnapshotCount:     snapshotCount,
			MaxInflightMsgs:   maxInflightMsgs,
			QuotaBackendBytes: quotaBackendBytes,
		},
		{
			Name:              "wan-regional",
			HeartbeatInterval: 250 * time.Millisecond,
			ElectionTimeout:   2_500 * time.Millisecond,
			SnapshotCount:     snapshotCount,
			MaxInflightMsgs:   maxInflightMsgs,
			QuotaBackendBytes: quotaBackendBytes,
		},
		{
			Name:              "wan-global",
			HeartbeatInterval: 500 * time.Millisecond,
			ElectionTimeout:   5_000 * time.Millisecond,
			SnapshotCount:     snapshotCount,
			MaxInflightMsgs:   maxInflightMsgs,
			QuotaBackendBytes: quotaBackendBytes,
		},
	}
}

// ProfileFor selects the profile whose HeartbeatInterval is the smallest value
// that is still ≥ rttMillis milliseconds, providing a safe margin above the
// measured RTT. If no profile's heartbeat is large enough, the profile with
// the largest HeartbeatInterval is returned (defensive: never under-provision).
//
// Selection is deterministic: profiles are sorted ascending by
// HeartbeatInterval before scanning; ties are broken by Name (lexicographic).
// Callers that supply their own profile list (not DefaultProfiles) get the
// same deterministic guarantee.
func ProfileFor(rttMillis int) Profile {
	profiles := DefaultProfiles()

	// Sort ascending by HeartbeatInterval, then by Name for tie-breaking so
	// the result is independent of DefaultProfiles() ordering.
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].HeartbeatInterval != profiles[j].HeartbeatInterval {
			return profiles[i].HeartbeatInterval < profiles[j].HeartbeatInterval
		}
		return profiles[i].Name < profiles[j].Name
	})

	rtt := time.Duration(rttMillis) * time.Millisecond

	// Walk from smallest to largest heartbeat and pick the first one that
	// covers the RTT. This gives the tightest (most responsive) safe profile.
	for _, p := range profiles {
		if p.HeartbeatInterval >= rtt {
			return p
		}
	}
	// All profiles are below the RTT: return the largest (safest) one.
	return profiles[len(profiles)-1]
}

// RenderFlags converts p into the corresponding etcd startup flag strings.
// The returned slice is deterministically ordered (matches the etcd man-page
// order) and can be passed directly to an etcd process as extra arguments.
//
// Flag format:
//
//	--heartbeat-interval=<ms>
//	--election-timeout=<ms>
//	--snapshot-count=<n>
//	--max-inflight-msgs=<n>
//	--quota-backend-bytes=<n>
//	--name=<profile-name>
func (p Profile) RenderFlags() []string {
	hiMs := p.HeartbeatInterval.Milliseconds()
	etMs := p.ElectionTimeout.Milliseconds()
	return []string{
		fmt.Sprintf("--heartbeat-interval=%d", hiMs),
		fmt.Sprintf("--election-timeout=%d", etMs),
		fmt.Sprintf("--snapshot-count=%d", p.SnapshotCount),
		fmt.Sprintf("--max-inflight-msgs=%d", p.MaxInflightMsgs),
		fmt.Sprintf("--quota-backend-bytes=%d", p.QuotaBackendBytes),
		fmt.Sprintf("--name=%s", p.Name),
	}
}

// flagKey strips the "--" prefix and the "=<value>" suffix from a single
// "--key=value" flag string, returning the bare key and the raw value string.
// It returns an error for flags that do not match the expected format.
func flagKey(flag string) (key, value string, err error) {
	if !strings.HasPrefix(flag, "--") {
		return "", "", fmt.Errorf("raftprofile: flag %q does not start with '--'", flag)
	}
	rest := strings.TrimPrefix(flag, "--")
	idx := strings.IndexByte(rest, '=')
	if idx < 0 {
		return "", "", fmt.Errorf("raftprofile: flag %q has no '=' separator", flag)
	}
	return rest[:idx], rest[idx+1:], nil
}

// ErrMissingFlag is returned by ParseFlags when a required etcd flag is absent
// from the input slice.
var ErrMissingFlag = errors.New("raftprofile: missing required flag")

// ErrInvalidFlag is returned by ParseFlags when a flag value cannot be parsed
// as the expected numeric type.
var ErrInvalidFlag = errors.New("raftprofile: invalid flag value")

// ParseFlags is the inverse of RenderFlags. It reconstructs a Profile from a
// slice of "--key=value" strings as produced by RenderFlags, enabling a full
// round-trip proof that RenderFlags encodes all fields correctly.
//
// Required flags: --heartbeat-interval, --election-timeout, --snapshot-count,
// --max-inflight-msgs, --quota-backend-bytes. The --name flag is optional; if
// absent, Profile.Name is left empty.
//
// Extra (unrecognised) flags are silently ignored so that ParseFlags can be
// used against the full flag set of a real etcd binary.
//
// Returns ErrMissingFlag if a required flag is absent, or ErrInvalidFlag if a
// value cannot be parsed.
func ParseFlags(flags []string) (Profile, error) {
	kv := make(map[string]string, len(flags))
	for _, f := range flags {
		k, v, err := flagKey(f)
		if err != nil {
			return Profile{}, err
		}
		kv[k] = v
	}

	parseInt64 := func(flagName string, required bool) (int64, error) {
		v, ok := kv[flagName]
		if !ok {
			if required {
				return 0, fmt.Errorf("%w: --%s", ErrMissingFlag, flagName)
			}
			return 0, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: --%s=%q: %v", ErrInvalidFlag, flagName, v, err)
		}
		return n, nil
	}

	hiMs, err := parseInt64("heartbeat-interval", true)
	if err != nil {
		return Profile{}, err
	}
	etMs, err := parseInt64("election-timeout", true)
	if err != nil {
		return Profile{}, err
	}
	scRaw, err := parseInt64("snapshot-count", true)
	if err != nil {
		return Profile{}, err
	}
	miRaw, err := parseInt64("max-inflight-msgs", true)
	if err != nil {
		return Profile{}, err
	}
	qbbRaw, err := parseInt64("quota-backend-bytes", true)
	if err != nil {
		return Profile{}, err
	}

	p := Profile{
		HeartbeatInterval: time.Duration(hiMs) * time.Millisecond,
		ElectionTimeout:   time.Duration(etMs) * time.Millisecond,
		SnapshotCount:     uint64(scRaw),
		MaxInflightMsgs:   int(miRaw),
		QuotaBackendBytes: qbbRaw,
	}
	if name, ok := kv["name"]; ok {
		p.Name = name
	}
	return p, nil
}
