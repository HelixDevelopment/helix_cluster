// Package discovery provides service discovery for Helix Cluster OS.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// TrustTier is an ordered trust classification for a service instance. Higher
// numeric values denote MORE trust. The scale runs from untrusted public /
// volunteer devices at the bottom up to fully-trusted, attested datacenter
// hardware at the top, giving callers a single comparable dimension on which to
// filter discovery results and gate sensitive workloads.
//
// The ordering is the load-bearing contract: a >= comparison on TrustTier is the
// canonical "at least this trusted" test used throughout the trust-aware
// registry. Do NOT renumber existing tiers without auditing every >= comparison.
type TrustTier int

// The trust ladder. Values are explicit and monotonically increasing so that
// (a) the >= comparison is meaningful and (b) JSON/metadata round-trips are
// stable across processes. Roughly 15 tiers from the roadmap, grouped from the
// most-trusted datacenter hardware down to anonymous public devices.
const (
	// TrustUnknown is the zero value: an instance whose trust has not been
	// classified. It is the LEAST trusted tier so an unclassified instance can
	// never accidentally satisfy a minimum-trust bar.
	TrustUnknown TrustTier = iota // 0

	// --- Untrusted / external edge ---
	TrustPublicAnonymous // 1  anonymous public device, no identity
	TrustVolunteer       // 2  volunteer-donated compute (BOINC-style)
	TrustUntrustedEdge   // 3  edge device outside any trust boundary
	TrustCommunity       // 4  community-operated, weakly identified

	// --- Partner / managed external ---
	TrustPartner     // 5  contractually-bound partner operator
	TrustManagedEdge // 6  managed but off-premises edge node
	TrustTenant      // 7  tenant-owned node inside a shared cluster

	// --- On-premise / internal ---
	TrustInternal  // 8  internal, organization-owned host
	TrustOnPrem    // 9  on-premise host inside the trust boundary
	TrustZoneLocal // 10 same availability-zone datacenter host

	// --- Datacenter hardened ---
	TrustDatacenter    // 11 core datacenter host
	TrustHardened      // 12 hardened / security-baselined host
	TrustAttested      // 13 remotely-attested (TPM/measured boot) host
	TrustSecureEnclave // 14 confidential-compute / secure-enclave host

	// TrustRoot is the maximum tier: the fully-trusted control-plane root of
	// trust. Nothing ranks above it.
	TrustRoot // 15
)

// Convenience anchors used by callers and tests as readable thresholds. These
// are aliases onto the ladder above, NOT new tiers, so they compare correctly.
const (
	TrustLow    = TrustUntrustedEdge // a typical "low trust" bar
	TrustMedium = TrustInternal      // a typical "medium trust" bar
	TrustHigh   = TrustDatacenter    // a typical "high trust" bar
)

// metaTrustKey is the reserved Instance.Metadata key under which the trust tier
// is persisted. Storing trust in Metadata (rather than adding a struct field)
// keeps Instance — and its JSON wire format used by the etcd backend —
// backward-compatible: old readers simply ignore the key.
const metaTrustKey = "helix.trust.tier"

var (
	// ErrTrustPolicyDenied is returned by a policy-gated lookup when no
	// instance satisfies the required minimum trust tier.
	ErrTrustPolicyDenied = errors.New("trust policy denied: no instance meets required trust tier")
	// ErrInvalidTrustTier is returned when a trust tier outside the known
	// ladder is supplied.
	ErrInvalidTrustTier = errors.New("invalid trust tier")
)

// String renders a tier as a stable human label (for logs / diagnostics).
func (t TrustTier) String() string {
	switch t {
	case TrustUnknown:
		return "unknown"
	case TrustPublicAnonymous:
		return "public-anonymous"
	case TrustVolunteer:
		return "volunteer"
	case TrustUntrustedEdge:
		return "untrusted-edge"
	case TrustCommunity:
		return "community"
	case TrustPartner:
		return "partner"
	case TrustManagedEdge:
		return "managed-edge"
	case TrustTenant:
		return "tenant"
	case TrustInternal:
		return "internal"
	case TrustOnPrem:
		return "on-prem"
	case TrustZoneLocal:
		return "zone-local"
	case TrustDatacenter:
		return "datacenter"
	case TrustHardened:
		return "hardened"
	case TrustAttested:
		return "attested"
	case TrustSecureEnclave:
		return "secure-enclave"
	case TrustRoot:
		return "root"
	default:
		return "tier(" + strconv.Itoa(int(t)) + ")"
	}
}

// Valid reports whether t is within the known trust ladder (inclusive of
// TrustUnknown, which is a legitimate "not yet classified" state).
func (t TrustTier) Valid() bool {
	return t >= TrustUnknown && t <= TrustRoot
}

// AtLeast reports whether t is at least as trusted as min. This is the single
// canonical trust comparison used by the registry; it exists so callers never
// hand-roll the >= and accidentally invert it.
func (t TrustTier) AtLeast(min TrustTier) bool {
	return t >= min
}

// trustToMeta writes the trust tier into a (copied) metadata map so the caller's
// map is never mutated.
func trustToMeta(meta map[string]string, tier TrustTier) map[string]string {
	out := make(map[string]string, len(meta)+1)
	for k, v := range meta {
		out[k] = v
	}
	out[metaTrustKey] = strconv.Itoa(int(tier))
	return out
}

// TrustOf extracts the trust tier tagged on an instance. An instance with no
// trust tag (or an unparseable one) is reported as TrustUnknown — the least
// trusted tier — so that untagged instances can never slip past a minimum-trust
// bar above TrustUnknown.
func TrustOf(inst *Instance) TrustTier {
	if inst == nil || inst.Metadata == nil {
		return TrustUnknown
	}
	raw, ok := inst.Metadata[metaTrustKey]
	if !ok {
		return TrustUnknown
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return TrustUnknown
	}
	tier := TrustTier(n)
	if !tier.Valid() {
		return TrustUnknown
	}
	return tier
}

// TrustPolicy is the policy hook callers install to gate sensitive workloads. It
// is consulted by RequireTrust-style lookups. Returning a non-nil error rejects
// the lookup even if some instances exist; returning nil admits the (already
// trust-filtered) candidate set.
//
// The default policy (DefaultMinTrustPolicy) simply enforces a minimum tier, but
// the hook is an interface so callers can layer arbitrary rules (e.g. require an
// attested host for key-signing, or refuse volunteer nodes for PII).
type TrustPolicy interface {
	// Authorize is called with the service name, the required minimum tier, and
	// the trust-filtered candidate instances. It returns nil to admit them.
	Authorize(service string, min TrustTier, candidates []*Instance) error
}

// DefaultMinTrustPolicy admits a lookup iff at least one candidate meets the
// required minimum tier. Because candidates are already filtered to >= min
// before the policy runs, this reduces to a non-empty check, but it is expressed
// explicitly so the contract is auditable and so custom policies have a
// reference implementation.
type DefaultMinTrustPolicy struct{}

// Authorize implements TrustPolicy.
func (DefaultMinTrustPolicy) Authorize(service string, min TrustTier, candidates []*Instance) error {
	for _, c := range candidates {
		if TrustOf(c).AtLeast(min) {
			return nil
		}
	}
	return fmt.Errorf("service %q: %w (required >= %s)", service, ErrTrustPolicyDenied, min)
}

// TrustRegistry layers trust-tier tagging, trust-filtered lookup, and a policy
// hook on top of an existing *ServiceRegistry. It does not replace the registry:
// every plain Register/Lookup/Select call on the embedded registry keeps working
// unchanged, so existing callers are unaffected.
type TrustRegistry struct {
	*ServiceRegistry
	policy TrustPolicy
}

// NewTrustRegistry wraps an existing ServiceRegistry with trust awareness. If
// policy is nil, DefaultMinTrustPolicy is used.
func NewTrustRegistry(reg *ServiceRegistry, policy TrustPolicy) *TrustRegistry {
	if policy == nil {
		policy = DefaultMinTrustPolicy{}
	}
	return &TrustRegistry{ServiceRegistry: reg, policy: policy}
}

// NewStaticTrustRegistry is a convenience constructor returning a trust-aware
// registry backed by an in-memory backend with the default policy.
func NewStaticTrustRegistry(opts ...Option) *TrustRegistry {
	return NewTrustRegistry(NewStaticRegistry(opts...), nil)
}

// RegisterWithTrust registers an instance and tags it with the given trust tier.
// The tier is persisted in instance metadata so it survives the backend
// round-trip and is visible to trust-filtered lookups. The supplied instance's
// own Metadata map is not mutated.
func (tr *TrustRegistry) RegisterWithTrust(ctx context.Context, inst *Instance, tier TrustTier) error {
	if inst == nil {
		return fmt.Errorf("instance is nil: %w", ErrNilInstance)
	}
	if !tier.Valid() {
		return fmt.Errorf("%w: %d", ErrInvalidTrustTier, int(tier))
	}
	tagged := *inst
	tagged.Metadata = trustToMeta(inst.Metadata, tier)
	return tr.ServiceRegistry.Register(ctx, &tagged)
}

// SetTrust updates the trust tier of an already-registered instance, both in the
// local cache and the backend, so subsequent trust-filtered lookups observe the
// change (sink-side). It returns ErrInstanceNotFound if the instance is unknown.
func (tr *TrustRegistry) SetTrust(ctx context.Context, service, id string, tier TrustTier) error {
	if !tier.Valid() {
		return fmt.Errorf("%w: %d", ErrInvalidTrustTier, int(tier))
	}
	key := instanceKey(service, id)

	tr.mu.Lock()
	inst, ok := tr.instances[key]
	if !ok {
		tr.mu.Unlock()
		return fmt.Errorf("instance not found: %s: %w", key, ErrInstanceNotFound)
	}
	inst.Metadata = trustToMeta(inst.Metadata, tier)
	copyInst := *inst
	tr.mu.Unlock()

	return tr.backend.Put(ctx, key, &copyInst)
}

// LookupMinTrust returns the healthy instances for a service whose trust tier is
// AT LEAST min, sorted by descending trust then by ID for determinism. It is the
// trust-filtered analogue of Lookup. An empty result is not an error.
func (tr *TrustRegistry) LookupMinTrust(ctx context.Context, service string, min TrustTier) ([]*Instance, error) {
	all, err := tr.ServiceRegistry.Lookup(ctx, service)
	if err != nil {
		return nil, err
	}
	return filterByMinTrust(all, min), nil
}

// ListAllByTrust returns ALL healthy instances for a service paired with their
// trust tier, sorted by descending trust then ID. Useful for diagnostics and for
// callers that want to inspect the full trust distribution.
func (tr *TrustRegistry) ListAllByTrust(ctx context.Context, service string) ([]*Instance, error) {
	return tr.LookupMinTrust(ctx, service, TrustUnknown)
}

// RequireTrust performs a policy-gated, trust-filtered lookup intended for
// sensitive workloads. It (1) filters healthy instances to those at tier >= min,
// (2) runs the installed TrustPolicy over the filtered set, and (3) returns the
// admitted instances. If the policy rejects (e.g. nothing meets the bar) it
// returns the policy's error (wrapping ErrTrustPolicyDenied for the default
// policy) and a nil slice — callers MUST NOT fall back to lower-trust nodes.
func (tr *TrustRegistry) RequireTrust(ctx context.Context, service string, min TrustTier) ([]*Instance, error) {
	candidates, err := tr.LookupMinTrust(ctx, service, min)
	if err != nil {
		return nil, err
	}
	if err := tr.policy.Authorize(service, min, candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

// SelectTrusted picks a single healthy instance at tier >= min using the same
// deterministic weighted load-balancing as ServiceRegistry.Select, but drawing
// only from the trust-filtered candidate set. It returns ErrNoInstances if no
// instance meets the bar, so a sensitive selection can never silently fall back
// to an untrusted node.
func (tr *TrustRegistry) SelectTrusted(ctx context.Context, service string, min TrustTier) (*Instance, error) {
	candidates, err := tr.LookupMinTrust(ctx, service, min)
	if err != nil {
		return nil, err
	}
	pick := tr.selector.Pick(candidates)
	if pick == nil {
		return nil, ErrNoInstances
	}
	return pick, nil
}

// filterByMinTrust returns the subset of instances at tier >= min, sorted by
// descending trust then ascending ID. This is the one place the trust threshold
// is applied; the explicit AtLeast call is what the mutation tests target.
func filterByMinTrust(instances []*Instance, min TrustTier) []*Instance {
	var out []*Instance
	for _, inst := range instances {
		if TrustOf(inst).AtLeast(min) {
			out = append(out, inst)
		}
	}
	// Deterministic order: highest trust first, then ID.
	sortByTrustDescThenID(out)
	return out
}

// sortByTrustDescThenID gives a stable, deterministic ordering for trust-filtered
// results without pulling in extra deps beyond the stdlib already used.
func sortByTrustDescThenID(insts []*Instance) {
	// Simple insertion sort: result sets are small (per-service instance lists)
	// and this keeps the comparison logic obvious and dependency-free.
	for i := 1; i < len(insts); i++ {
		j := i
		for j > 0 && lessTrustThenID(insts[j], insts[j-1]) {
			insts[j], insts[j-1] = insts[j-1], insts[j]
			j--
		}
	}
}

// lessTrustThenID reports whether a should sort before b: higher trust first,
// ties broken by ascending ID.
func lessTrustThenID(a, b *Instance) bool {
	ta, tb := TrustOf(a), TrustOf(b)
	if ta != tb {
		return ta > tb
	}
	return a.ID < b.ID
}
