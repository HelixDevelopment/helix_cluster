// Package imagepolicy implements an OPA-style workload admission policy for
// container images, performing signed-image, allowed-registry, and privilege
// checks. It is pure Go, deterministic, and self-contained.
package imagepolicy

// Image describes a container image referenced by a workload.
type Image struct {
	// Registry is the source registry (e.g. "registry.helix.internal").
	Registry string
	// Digest is the content-addressable digest of the image.
	Digest string
	// Signed reports whether the image carries a signature.
	Signed bool
	// SignatureValid reports whether the carried signature verified.
	SignatureValid bool
}

// Workload is a unit of work the scheduler wants to admit.
type Workload struct {
	// Name identifies the workload (for diagnostics only).
	Name string
	// Image is the image the workload runs.
	Image Image
	// Privileged reports whether the workload requests privileged execution.
	Privileged bool
}

// Policy is the admission policy evaluated against a Workload.
type Policy struct {
	// AllowedRegistries is the set of registries permitted to source images.
	// A nil or empty set permits no registry.
	AllowedRegistries map[string]struct{}
	// RequireSignature, when true, demands a valid signature on the image.
	RequireSignature bool
	// AllowPrivileged, when true, permits privileged workloads.
	AllowPrivileged bool
}

// Reason is a typed admission decision reason.
type Reason int

const (
	// ReasonAdmitted indicates the workload satisfied every policy rule.
	ReasonAdmitted Reason = iota
	// ReasonRegistryNotAllowed indicates the image registry is not in the allowlist.
	ReasonRegistryNotAllowed
	// ReasonUnsigned indicates a required signature was missing or invalid.
	ReasonUnsigned
	// ReasonPrivilegedDenied indicates the workload was privileged but the policy forbids it.
	ReasonPrivilegedDenied
)

// String renders a Reason as a stable, human-readable token.
func (r Reason) String() string {
	switch r {
	case ReasonAdmitted:
		return "admitted"
	case ReasonRegistryNotAllowed:
		return "registry-not-allowed"
	case ReasonUnsigned:
		return "unsigned-or-invalid-signature"
	case ReasonPrivilegedDenied:
		return "privileged-denied"
	default:
		return "unknown"
	}
}

// NewRegistrySet builds an allowed-registry set from the given registries.
func NewRegistrySet(registries ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(registries))
	for _, r := range registries {
		set[r] = struct{}{}
	}
	return set
}

// Admit evaluates policy against workload and returns whether the workload is
// admitted along with the typed reason. Rules are evaluated in a fixed order:
//
//  1. The image registry MUST be in policy.AllowedRegistries.
//  2. If policy.RequireSignature, the image MUST be Signed and SignatureValid.
//  3. If the workload is Privileged, policy.AllowPrivileged MUST be true.
//
// A workload satisfying every rule is admitted with ReasonAdmitted.
func Admit(policy Policy, workload Workload) (admitted bool, reason Reason) {
	if _, ok := policy.AllowedRegistries[workload.Image.Registry]; !ok {
		return false, ReasonRegistryNotAllowed
	}
	if policy.RequireSignature && (!workload.Image.Signed || !workload.Image.SignatureValid) {
		return false, ReasonUnsigned
	}
	if workload.Privileged && !policy.AllowPrivileged {
		return false, ReasonPrivilegedDenied
	}
	return true, ReasonAdmitted
}
