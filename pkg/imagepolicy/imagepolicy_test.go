package imagepolicy

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// runUUID returns a random run identifier for log correlation.
func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// strictPolicy is the canonical policy used across cases: a single allowed
// registry, signature required, privileged forbidden.
func strictPolicy() Policy {
	return Policy{
		AllowedRegistries: NewRegistrySet("registry.helix.internal"),
		RequireSignature:  true,
		AllowPrivileged:   false,
	}
}

// compliantWorkload is a fully-compliant signed image from the allowed registry.
func compliantWorkload() Workload {
	return Workload{
		Name: "payments-api",
		Image: Image{
			Registry:       "registry.helix.internal",
			Digest:         "sha256:abc",
			Signed:         true,
			SignatureValid: true,
		},
		Privileged: false,
	}
}

// TestAdmit_DistinctReasonsPerCase covers CLOSURE clause 1: a compliant signed
// image is ADMITTED, while an unsigned image, a privileged workload, and a
// disallowed-registry image are each REJECTED with their distinct reason.
func TestAdmit_DistinctReasonsPerCase(t *testing.T) {
	uuid := runUUID(t)
	policy := strictPolicy()

	cases := []struct {
		name         string
		mutate       func(w Workload) Workload
		wantAdmitted bool
		wantReason   Reason
	}{
		{
			name:         "compliant-signed-allowed-registry",
			mutate:       func(w Workload) Workload { return w },
			wantAdmitted: true,
			wantReason:   ReasonAdmitted,
		},
		{
			name: "unsigned-image",
			mutate: func(w Workload) Workload {
				w.Image.Signed = false
				w.Image.SignatureValid = false
				return w
			},
			wantAdmitted: false,
			wantReason:   ReasonUnsigned,
		},
		{
			name: "privileged-workload",
			mutate: func(w Workload) Workload {
				w.Privileged = true
				return w
			},
			wantAdmitted: false,
			wantReason:   ReasonPrivilegedDenied,
		},
		{
			name: "disallowed-registry",
			mutate: func(w Workload) Workload {
				w.Image.Registry = "docker.io"
				return w
			},
			wantAdmitted: false,
			wantReason:   ReasonRegistryNotAllowed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.mutate(compliantWorkload())
			t.Logf("run=%s case=%s BEFORE registry=%q signed=%v sigValid=%v privileged=%v",
				uuid, tc.name, w.Image.Registry, w.Image.Signed, w.Image.SignatureValid, w.Privileged)

			admitted, reason := Admit(policy, w)

			t.Logf("run=%s case=%s AFTER admitted=%v reason=%q",
				uuid, tc.name, admitted, reason)

			// Mutation: assert exact admitted value derived from the case.
			if admitted != tc.wantAdmitted {
				t.Fatalf("admitted = %v, want %v (reason=%q)", admitted, tc.wantAdmitted, reason)
			}
			// Mutation: assert the distinct typed reason for this case.
			if reason != tc.wantReason {
				t.Fatalf("reason = %q (%d), want %q (%d)", reason, reason, tc.wantReason, tc.wantReason)
			}
		})
	}
}

// TestAdmit_EachRuleIndependentlyLoadBearing covers CLOSURE clause 2: a
// workload failing EXACTLY ONE rule is rejected for THAT rule's reason, and the
// otherwise-identical compliant workload is admitted. This proves each rule is
// independently load-bearing rather than masked by another.
func TestAdmit_EachRuleIndependentlyLoadBearing(t *testing.T) {
	uuid := runUUID(t)
	policy := strictPolicy()

	// Baseline must be admitted so any single-rule failure is attributable.
	base := compliantWorkload()
	if admitted, reason := Admit(policy, base); !admitted || reason != ReasonAdmitted {
		t.Fatalf("baseline not admitted: admitted=%v reason=%q", admitted, reason)
	}
	t.Logf("run=%s baseline admitted reason=%q", uuid, ReasonAdmitted)

	t.Run("only-registry-fails", func(t *testing.T) {
		w := compliantWorkload()
		w.Image.Registry = "ghcr.io" // sole violation
		t.Logf("run=%s BEFORE registry=%q", uuid, w.Image.Registry)
		admitted, reason := Admit(policy, w)
		t.Logf("run=%s AFTER admitted=%v reason=%q", uuid, admitted, reason)
		// Mutation: dropping the registry check would admit this -> fail here.
		if admitted {
			t.Fatalf("disallowed registry was admitted")
		}
		if reason != ReasonRegistryNotAllowed {
			t.Fatalf("reason = %q, want %q", reason, ReasonRegistryNotAllowed)
		}
	})

	t.Run("only-signature-fails", func(t *testing.T) {
		w := compliantWorkload()
		w.Image.Signed = false // sole violation; registry + privilege still compliant
		t.Logf("run=%s BEFORE signed=%v", uuid, w.Image.Signed)
		admitted, reason := Admit(policy, w)
		t.Logf("run=%s AFTER admitted=%v reason=%q", uuid, admitted, reason)
		// Mutation: dropping the signature check would admit this -> fail here.
		if admitted {
			t.Fatalf("unsigned image was admitted")
		}
		if reason != ReasonUnsigned {
			t.Fatalf("reason = %q, want %q", reason, ReasonUnsigned)
		}
	})

	t.Run("only-signature-invalid-fails", func(t *testing.T) {
		w := compliantWorkload()
		w.Image.Signed = true
		w.Image.SignatureValid = false // signed but signature did not verify
		t.Logf("run=%s BEFORE signed=%v sigValid=%v", uuid, w.Image.Signed, w.Image.SignatureValid)
		admitted, reason := Admit(policy, w)
		t.Logf("run=%s AFTER admitted=%v reason=%q", uuid, admitted, reason)
		// Mutation: the SignatureValid clause must be load-bearing.
		if admitted {
			t.Fatalf("invalid-signature image was admitted")
		}
		if reason != ReasonUnsigned {
			t.Fatalf("reason = %q, want %q", reason, ReasonUnsigned)
		}
	})

	t.Run("only-privilege-fails", func(t *testing.T) {
		w := compliantWorkload()
		w.Privileged = true // sole violation
		t.Logf("run=%s BEFORE privileged=%v", uuid, w.Privileged)
		admitted, reason := Admit(policy, w)
		t.Logf("run=%s AFTER admitted=%v reason=%q", uuid, admitted, reason)
		// Mutation: dropping the privilege check would admit this -> fail here.
		if admitted {
			t.Fatalf("privileged workload was admitted")
		}
		if reason != ReasonPrivilegedDenied {
			t.Fatalf("reason = %q, want %q", reason, ReasonPrivilegedDenied)
		}
	})
}

// TestAdmit_PrivilegedAllowedWhenPolicyPermits proves AllowPrivileged is itself
// load-bearing: the same privileged workload that was denied is admitted once
// the policy permits privilege (and all other rules hold).
func TestAdmit_PrivilegedAllowedWhenPolicyPermits(t *testing.T) {
	uuid := runUUID(t)
	policy := strictPolicy()
	policy.AllowPrivileged = true

	w := compliantWorkload()
	w.Privileged = true
	t.Logf("run=%s BEFORE allowPrivileged=%v privileged=%v", uuid, policy.AllowPrivileged, w.Privileged)
	admitted, reason := Admit(policy, w)
	t.Logf("run=%s AFTER admitted=%v reason=%q", uuid, admitted, reason)
	// Mutation: if Admit ignored AllowPrivileged, this would be denied.
	if !admitted || reason != ReasonAdmitted {
		t.Fatalf("privileged workload denied under permissive policy: admitted=%v reason=%q", admitted, reason)
	}
}

// TestAdmit_SignatureNotRequired proves RequireSignature gates the signature
// rule: an unsigned image is admitted when the policy does not require signing.
func TestAdmit_SignatureNotRequired(t *testing.T) {
	uuid := runUUID(t)
	policy := strictPolicy()
	policy.RequireSignature = false

	w := compliantWorkload()
	w.Image.Signed = false
	w.Image.SignatureValid = false
	t.Logf("run=%s BEFORE requireSig=%v signed=%v", uuid, policy.RequireSignature, w.Image.Signed)
	admitted, reason := Admit(policy, w)
	t.Logf("run=%s AFTER admitted=%v reason=%q", uuid, admitted, reason)
	// Mutation: if RequireSignature were ignored (always-on), this would fail.
	if !admitted || reason != ReasonAdmitted {
		t.Fatalf("unsigned image denied under non-requiring policy: admitted=%v reason=%q", admitted, reason)
	}
}
