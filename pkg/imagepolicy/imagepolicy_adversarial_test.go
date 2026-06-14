package imagepolicy

import (
	"testing"
)

// adversarialPolicy is the strict gate the adversarial cases probe against: a
// single canonical allowed registry, signature required, privileged forbidden.
// Kept independent of the happy-path test helpers so this file is self-contained.
func adversarialPolicy() Policy {
	return Policy{
		AllowedRegistries: NewRegistrySet("registry.helix.internal"),
		RequireSignature:  true,
		AllowPrivileged:   false,
	}
}

// genuineCompliant is a fully-compliant signed image from the allowed registry.
// It exists so an "always-deny" mutation of Admit is also caught: this case
// MUST be admitted.
func genuineCompliant() Workload {
	return Workload{
		Name: "payments-api",
		Image: Image{
			Registry:       "registry.helix.internal",
			Digest:         "sha256:deadbeef",
			Signed:         true,
			SignatureValid: true,
		},
		Privileged: false,
	}
}

// TestAdmit_GenuineAllowBaseline pins the allow path so a blanket always-deny
// mutation (e.g. `return false, ReasonRegistryNotAllowed`) is caught here, not
// only by the deny tests. Without a green allow case, an always-deny gate would
// "pass" every deny assertion vacuously.
func TestAdmit_GenuineAllowBaseline(t *testing.T) {
	policy := adversarialPolicy()
	w := genuineCompliant()

	admitted, reason := Admit(policy, w)
	t.Logf("baseline registry=%q signed=%v sigValid=%v privileged=%v -> admitted=%v reason=%q",
		w.Image.Registry, w.Image.Signed, w.Image.SignatureValid, w.Privileged, admitted, reason)

	if !admitted || reason != ReasonAdmitted {
		t.Fatalf("genuine compliant workload was DENIED: admitted=%v reason=%q (want admitted=true reason=%q)",
			admitted, reason, ReasonAdmitted)
	}
}

// TestAdmit_RegistryAllowlistBypass is the core anti-bypass table. The gate uses
// an EXACT map-key lookup; every lookalike/superstring/substring/case/whitespace
// variant of the allowed registry MUST be denied with ReasonRegistryNotAllowed.
//
// Mutation that this BITES: changing the exact map lookup to a substring or
// prefix/suffix match (e.g. strings.Contains(reg, allowed) or
// strings.HasPrefix(reg, allowed)) would admit one or more of these lookalikes.
func TestAdmit_RegistryAllowlistBypass(t *testing.T) {
	policy := adversarialPolicy()

	denyCases := []struct {
		name     string
		registry string
	}{
		{"superstring-suffix-attacker-domain", "registry.helix.internal.evil.com"},
		{"substring-attacker-prefix", "evil.com/registry.helix.internal"},
		{"trailing-slash", "registry.helix.internal/"},
		{"trailing-path", "registry.helix.internal/library"},
		{"leading-dot", ".registry.helix.internal"},
		{"embedded", "xregistry.helix.internalx"},
		{"uppercase-all", "REGISTRY.HELIX.INTERNAL"},
		{"mixed-case", "Registry.Helix.Internal"},
		{"trailing-space", "registry.helix.internal "},
		{"leading-space", " registry.helix.internal"},
		{"trailing-tab", "registry.helix.internal\t"},
		{"trailing-newline", "registry.helix.internal\n"},
		{"empty-string", ""},
		{"unrelated-public-registry", "docker.io"},
		{"lookalike-port", "registry.helix.internal:5000"},
		{"homoglyph-style-suffix", "registry.helix.internal.attacker"},
	}

	for _, tc := range denyCases {
		t.Run(tc.name, func(t *testing.T) {
			w := genuineCompliant()
			w.Image.Registry = tc.registry

			admitted, reason := Admit(policy, w)
			t.Logf("registry=%q -> admitted=%v reason=%q", tc.registry, admitted, reason)

			if admitted {
				t.Fatalf("BYPASS: registry %q was ADMITTED but is not the exact allowed registry", tc.registry)
			}
			if reason != ReasonRegistryNotAllowed {
				t.Fatalf("registry %q denied with reason %q, want %q", tc.registry, reason, ReasonRegistryNotAllowed)
			}
		})
	}
}

// TestAdmit_EmptyAndNilAllowlistFailClosed pins the documented contract
// "A nil or empty set permits no registry." A nil map and an empty map MUST
// deny every image, including an empty registry string, and MUST NOT panic.
//
// Mutation that this BITES: treating a nil/empty allowlist as "allow all"
// (e.g. `if len(policy.AllowedRegistries) == 0 { /* skip check */ }`).
func TestAdmit_EmptyAndNilAllowlistFailClosed(t *testing.T) {
	policies := []struct {
		name   string
		policy Policy
	}{
		{
			name: "nil-allowlist",
			policy: Policy{
				AllowedRegistries: nil,
				RequireSignature:  true,
			},
		},
		{
			name: "empty-allowlist",
			policy: Policy{
				AllowedRegistries: map[string]struct{}{},
				RequireSignature:  true,
			},
		},
	}

	registries := []string{"registry.helix.internal", "", "docker.io"}

	for _, pc := range policies {
		for _, reg := range registries {
			t.Run(pc.name+"/registry="+reg, func(t *testing.T) {
				w := genuineCompliant()
				w.Image.Registry = reg

				admitted, reason := Admit(pc.policy, w)
				t.Logf("policy=%s registry=%q -> admitted=%v reason=%q", pc.name, reg, admitted, reason)

				if admitted {
					t.Fatalf("FAIL-OPEN: %s admitted registry %q; nil/empty allowlist must permit no registry", pc.name, reg)
				}
				if reason != ReasonRegistryNotAllowed {
					t.Fatalf("%s registry %q reason=%q, want %q", pc.name, reg, reason, ReasonRegistryNotAllowed)
				}
			})
		}
	}
}

// TestAdmit_ZeroValueWorkloadFailsClosed feeds a fully zero-valued Workload to
// both a strict policy and a zero-valued (nil-allowlist) policy. The gate MUST
// NOT panic and MUST deny (empty registry is not in any non-empty allowlist,
// and a nil allowlist permits nothing).
func TestAdmit_ZeroValueWorkloadFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		policy Policy
	}{
		{"strict-policy", adversarialPolicy()},
		{"zero-policy", Policy{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w Workload // zero value: empty registry, unsigned, not privileged

			admitted, reason := Admit(tc.policy, w)
			t.Logf("policy=%s zero-workload -> admitted=%v reason=%q", tc.name, admitted, reason)

			if admitted {
				t.Fatalf("FAIL-OPEN: zero-value workload was ADMITTED under %s", tc.name)
			}
			if reason != ReasonRegistryNotAllowed {
				t.Fatalf("%s zero-workload reason=%q, want %q (registry checked first)", tc.name, reason, ReasonRegistryNotAllowed)
			}
		})
	}
}

// TestAdmit_SignatureMissingTreatedAsValid pins BOTH halves of the signature
// guard `!Signed || !SignatureValid`. In particular the (Signed=false,
// SignatureValid=true) row models a forged/erroneous "signature verified" flag
// on an image that is not actually signed: it MUST be denied.
//
// Mutations that this BITES:
//   - dropping the `!Signed` half (checking only SignatureValid) admits the
//     signed=false,sigValid=true row.
//   - dropping the `!SignatureValid` half (checking only Signed) admits the
//     signed=true,sigValid=false row.
func TestAdmit_SignatureMissingTreatedAsValid(t *testing.T) {
	policy := adversarialPolicy() // RequireSignature = true

	denyCases := []struct {
		name   string
		signed bool
		sigVal bool
	}{
		{"unsigned-and-invalid", false, false},
		{"unsigned-but-claims-valid", false, true},
		{"signed-but-invalid", true, false},
	}

	for _, tc := range denyCases {
		t.Run(tc.name, func(t *testing.T) {
			w := genuineCompliant()
			w.Image.Signed = tc.signed
			w.Image.SignatureValid = tc.sigVal

			admitted, reason := Admit(policy, w)
			t.Logf("signed=%v sigValid=%v -> admitted=%v reason=%q", tc.signed, tc.sigVal, admitted, reason)

			if admitted {
				t.Fatalf("FAIL-OPEN: signed=%v sigValid=%v was ADMITTED under RequireSignature", tc.signed, tc.sigVal)
			}
			if reason != ReasonUnsigned {
				t.Fatalf("signed=%v sigValid=%v reason=%q, want %q", tc.signed, tc.sigVal, reason, ReasonUnsigned)
			}
		})
	}

	// Genuine valid signature MUST still be admitted (catches always-deny in the
	// signature guard).
	t.Run("genuinely-signed-and-valid-admitted", func(t *testing.T) {
		w := genuineCompliant()
		admitted, reason := Admit(policy, w)
		t.Logf("signed=true sigValid=true -> admitted=%v reason=%q", admitted, reason)
		if !admitted || reason != ReasonAdmitted {
			t.Fatalf("genuinely signed image DENIED: admitted=%v reason=%q", admitted, reason)
		}
	})
}

// TestAdmit_PrivilegedFailsClosed pins the privileged guard against a fail-open
// mutation. A privileged workload MUST be denied when the policy forbids
// privilege, even if registry+signature are fully compliant.
//
// Mutation that this BITES: changing `if workload.Privileged && !policy.AllowPrivileged`
// to default-allow (e.g. removing the guard, or `!workload.Privileged`).
func TestAdmit_PrivilegedFailsClosed(t *testing.T) {
	policy := adversarialPolicy() // AllowPrivileged = false

	w := genuineCompliant()
	w.Privileged = true

	admitted, reason := Admit(policy, w)
	t.Logf("privileged=true allowPrivileged=false -> admitted=%v reason=%q", admitted, reason)

	if admitted {
		t.Fatalf("FAIL-OPEN: privileged workload ADMITTED while policy forbids privilege")
	}
	if reason != ReasonPrivilegedDenied {
		t.Fatalf("reason=%q, want %q", reason, ReasonPrivilegedDenied)
	}
}

// TestAdmit_EvaluationOrderFirstViolationWins pins the documented fixed rule
// order: registry, then signature, then privilege. A workload violating ALL
// THREE must report ReasonRegistryNotAllowed (the first rule), and a workload
// passing registry but violating signature+privilege must report ReasonUnsigned.
// This guarantees the gate never short-circuits to ADMIT and that the reason is
// the first-failing rule.
func TestAdmit_EvaluationOrderFirstViolationWins(t *testing.T) {
	policy := adversarialPolicy()

	t.Run("all-three-violations-report-registry", func(t *testing.T) {
		w := genuineCompliant()
		w.Image.Registry = "evil.com"
		w.Image.Signed = false
		w.Image.SignatureValid = false
		w.Privileged = true

		admitted, reason := Admit(policy, w)
		t.Logf("all-violations -> admitted=%v reason=%q", admitted, reason)
		if admitted {
			t.Fatalf("FAIL-OPEN: triple-violating workload was ADMITTED")
		}
		if reason != ReasonRegistryNotAllowed {
			t.Fatalf("reason=%q, want %q (registry evaluated first)", reason, ReasonRegistryNotAllowed)
		}
	})

	t.Run("signature-and-privilege-violations-report-signature", func(t *testing.T) {
		w := genuineCompliant() // registry OK
		w.Image.Signed = false
		w.Image.SignatureValid = false
		w.Privileged = true

		admitted, reason := Admit(policy, w)
		t.Logf("sig+priv-violations -> admitted=%v reason=%q", admitted, reason)
		if admitted {
			t.Fatalf("FAIL-OPEN: signature+privilege-violating workload was ADMITTED")
		}
		if reason != ReasonUnsigned {
			t.Fatalf("reason=%q, want %q (signature evaluated before privilege)", reason, ReasonUnsigned)
		}
	})
}
