package security

// security_adversarial_test.go — adversarial, SINK-SIDE probes against the
// internal/security orchestrator, policy enforcer, identity bindings, and the
// gRPC façade. Every assertion checks the ACTUAL allow/deny/trusted/valid
// verdict on hostile input (empty/nil/zero/malformed/unknown/forged), never
// merely "did not panic".
//
// PROVENANCE: this package is the WIRED consumer of pkg/security. The
// pkg/security SPIFFE trust-domain "/" fail-open (a malformed authority
// "example.org/evil" passing Validate) is fixed upstream in pkg/security
// (IsValidTrustDomain rejects '/'); here we additionally prove that
// internal/security's downstream exact-match trust-domain gate independently
// rejects every forged authority, so even a hypothetical upstream regression
// cannot leak a cross-domain identity past ValidateIdentity.
//
// FINDINGS (see file footer for the honest three-way discrimination):
//   - FAIL-OPEN (trust/authz): NO live bug — gates are fail-closed.
//   - PRECEDENCE: allow-only model by design (no Deny permission type) —
//     pinned as a documented contract, not a defect.
//   - NUMERIC (NaN/Inf): N/A — package contains no float scoring.
//   - LATENT RISK: GRPCServer.Authenticate string-builds the SPIFFE ID via
//     fmt.Sprintf without normalising req.Identity, so path-shaped identities
//     ("../x", "a/b") are accepted verbatim. This CANNOT hijack the trust
//     domain (proven below) but is pinned so a future change that makes the
//     synthesized path security-relevant is caught.

import (
	"context"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	pkgsec "github.com/HelixDevelopment/helix_cluster/pkg/security"
)

// ─────────────────────────────────────────────────────────────────────────────
// (a) FAIL-OPEN — ValidateIdentity must DENY forged/empty/malformed authorities.
// The trust-domain check is an exact match against o.spiffeTrustDomain
// ("helix.local"). A fail-open here would TRUST a cross-domain or malformed ID.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_ValidateIdentity_RejectsForgedAndMalformedAuthorities(t *testing.T) {
	orch := NewOrchestrator(nil)
	ctx := context.Background()

	type tc struct {
		name string
		id   string
	}
	denied := []tc{
		{"empty", ""},
		{"cross-domain evil.org", "spiffe://evil.org/wl"},
		{"trailing-dot domain", "spiffe://helix.local./wl"},
		{"uppercased domain (no case-fold)", "spiffe://HELIX.LOCAL/wl"},
		{"sibling-prefix domain", "spiffe://helix.localX/wl"},
		{"missing scheme", "helix.local/wl"},
		{"wrong scheme", "https://helix.local/wl"},
		{"query injection", "spiffe://helix.local/wl?x=1"},
		{"fragment injection", "spiffe://helix.local/wl#admin"},
		{"userinfo authority confusion", "spiffe://helix.local@evil.org/wl"},
		// '/' embedded in the trust-domain authority is the upstream fail-open
		// class. ParseSPIFFEID treats everything after the host as path, so a
		// raw "spiffe://helix.local/evil.org" parses TD=helix.local (allowed,
		// it is genuinely our domain). The forged-authority case that MUST deny
		// is a DIFFERENT host, covered by "cross-domain" above. We additionally
		// assert the pkg-level primitive rejects a literal '/'-bearing TD:
	}
	for _, c := range denied {
		c := c
		t.Run("DENY/"+c.name, func(t *testing.T) {
			if err := orch.ValidateIdentity(ctx, c.id); err == nil {
				t.Fatalf("FAIL-OPEN: ValidateIdentity(%q) returned nil (TRUSTED); want denial", c.id)
			}
		})
	}

	// Positive control: the one genuinely-valid in-domain ID must be accepted,
	// otherwise the test above could pass by always-denying (a useless gate).
	if err := orch.ValidateIdentity(ctx, "spiffe://helix.local/svc/gateway"); err != nil {
		t.Fatalf("regression: legitimate in-domain ID rejected: %v", err)
	}
}

// Pin the upstream primitive that internal/security composes: a trust domain
// literally containing '/' (the masked fail-open) must be VALID==false. If this
// regresses upstream, a forged authority like "helix.local/evil" could begin to
// round-trip; this test bites in THIS package's build.
func TestAdv_UpstreamTrustDomainSlashIsRejected(t *testing.T) {
	bad := &pkgsec.SPIFFEID{TrustDomain: "helix.local/evil", Path: "/wl"}
	if err := bad.Validate(); err == nil {
		t.Fatal("FAIL-OPEN(upstream): SPIFFEID.Validate accepted a '/'-bearing trust domain")
	}
	if pkgsec.IsValidTrustDomain("helix.local/evil") {
		t.Fatal("FAIL-OPEN(upstream): IsValidTrustDomain accepted a '/'-bearing trust domain")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (a) FAIL-OPEN — ValidateToken must DENY empty/unknown/revoked/expired tokens
// and must NEVER return valid=true with a nil error on a non-credential.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_ValidateToken_DeniesHostileTokens(t *testing.T) {
	orch := NewOrchestrator(nil)
	ctx := context.Background()

	// empty token: contract returns an error AND valid=false.
	if ok, _, _, err := orch.ValidateToken(ctx, ""); ok || err == nil {
		t.Fatalf("FAIL-OPEN: empty token -> valid=%v err=%v; want valid=false,err!=nil", ok, err)
	}
	// unknown token: valid=false, nil error (miss is not an error).
	if ok, id, sc, err := orch.ValidateToken(ctx, "cert-does-not-exist"); ok || err != nil || id != "" || sc != nil {
		t.Fatalf("FAIL-OPEN: unknown token -> valid=%v id=%q scopes=%v err=%v; want all-zero deny", ok, id, sc, err)
	}

	// Issue a real cert, then revoke it; ValidateToken must flip to deny.
	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{NodeID: "n1", CommonName: "n1"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if ok, _, _, _ := orch.ValidateToken(ctx, cert.ID); !ok {
		t.Fatal("setup: freshly-issued token should validate")
	}
	if err := orch.RevokeCertificate(ctx, cert.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if ok, id, sc, err := orch.ValidateToken(ctx, cert.ID); ok || err != nil || id != "" || sc != nil {
		t.Fatalf("FAIL-OPEN: revoked token validated -> valid=%v id=%q scopes=%v err=%v", ok, id, sc, err)
	}
}

// Expiry is a fail-open hotspot: an expired credential must not validate, and
// RotateCredentials must sweep it into the revoked set.
func TestAdv_ExpiredCredential_DeniedAndSwept(t *testing.T) {
	orch := NewOrchestrator(nil)
	ctx := context.Background()

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID:     "expire-me",
		CommonName: "expire-me",
		TTL:        1 * time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure NotAfter is in the past

	if ok, _, _, err := orch.ValidateToken(ctx, cert.ID); ok || err != nil {
		t.Fatalf("FAIL-OPEN: expired token validated -> valid=%v err=%v", ok, err)
	}
	if _, err := orch.RotateCredentials(ctx); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !orch.IsRevokedSerial(cert.Serial) {
		t.Fatal("FAIL-OPEN: rotation did not revoke the expired serial")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (a) FAIL-OPEN — Authorize (pure scope gate) must fail CLOSED on unknown route,
// empty scope set, and must NOT treat a literal "*" scope as a master key.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_Authorize_FailsClosed(t *testing.T) {
	cases := []struct {
		name        string
		scopes      []string
		action, rte string
		wantAllowed bool
	}{
		{"nil scopes, real route", nil, "GET", "/session/x", false},
		{"empty scopes, real route", []string{}, "GET", "/session/x", false},
		{"unknown route denies even with scopes", []string{ScopeSessionRead}, "GET", "/unknown", false},
		{"empty action+route denies", []string{ScopeSessionRead}, "", "", false},
		{"literal star scope is NOT a master key", []string{"*"}, "POST", "/session/x", false},
		{"wrong scope for write route", []string{ScopeSessionRead}, "POST", "/session/x", false},
		// positive control:
		{"correct scope allows", []string{ScopeSessionWrite}, "POST", "/session/x", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			d := Authorize(c.scopes, c.action, c.rte)
			if d.Allowed != c.wantAllowed {
				t.Fatalf("SINK MISMATCH: Authorize(%v,%q,%q).Allowed=%v want %v (reason=%q)",
					c.scopes, c.action, c.rte, d.Allowed, c.wantAllowed, d.Reason)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (a) FAIL-OPEN — EnforcePolicy must DENY a nil policy, an empty role set, and a
// role that was never loaded (no implicit grant). This is the gate the gRPC
// Authorize() feeds the token's scopes into as roles.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_EnforcePolicy_DeniesNilEmptyAndUnknownRole(t *testing.T) {
	enf := NewPolicyEnforcer()
	ctx := context.Background()

	if err := enf.EnforcePolicy(ctx, nil, "secrets", "read"); err == nil {
		t.Fatal("FAIL-OPEN: nil policy allowed")
	}
	if err := enf.EnforcePolicy(ctx, &Policy{Name: "p", Subject: "s", Roles: nil}, "secrets", "read"); err == nil {
		t.Fatal("FAIL-OPEN: policy with no roles allowed")
	}
	// A role name present in the policy but never loaded into the enforcer must
	// be skipped, not implicitly granted.
	if err := enf.EnforcePolicy(ctx, &Policy{Name: "p", Subject: "s", Roles: []string{"ghost"}}, "secrets", "read"); err == nil {
		t.Fatal("FAIL-OPEN: unloaded role 'ghost' produced an allow")
	}
}

// PRECEDENCE / TOTAL-ORDER (documented-contract pin): the enforcer is an
// ALLOW-ONLY model — there is no Deny permission type, so "deny overrides allow"
// is not expressible. We pin the actual semantics: if ANY role grants the
// (resource,action), the decision is ALLOW regardless of how many other roles
// would not grant it; ordering of roles/permissions does not change the verdict
// (first match short-circuits to allow). This test documents — and locks — that
// contract so a future regression that, say, made an unmatched role flip the
// result is caught, and so nobody mistakes the absence of deny-overrides for a
// bug this suite missed.
func TestAdv_EnforcePolicy_AllowOnlyModel_OrderIndependentAllow(t *testing.T) {
	enf := NewPolicyEnforcer()
	ctx := context.Background()

	// reader: cannot delete. admin: can do anything.
	mustLoad := func(r *Role) {
		if err := enf.LoadRole(ctx, r); err != nil {
			t.Fatalf("load role %s: %v", r.Name, err)
		}
	}
	mustLoad(&Role{Name: "reader", Permissions: []Permission{{Action: "read", Resource: "logs"}}})
	mustLoad(&Role{Name: "admin", Permissions: []Permission{{Action: "*", Resource: "*"}}})

	// Both orderings of the SAME two roles must yield the SAME allow verdict for
	// an action only admin grants. A nondeterministic (map-order-dependent)
	// enforcer would flake here.
	polAR := &Policy{Name: "ar", Subject: "u", Roles: []string{"admin", "reader"}}
	polRA := &Policy{Name: "ra", Subject: "u", Roles: []string{"reader", "admin"}}
	if err := enf.EnforcePolicy(ctx, polAR, "secrets", "delete"); err != nil {
		t.Fatalf("admin-first should allow delete: %v", err)
	}
	if err := enf.EnforcePolicy(ctx, polRA, "secrets", "delete"); err != nil {
		t.Fatalf("reader-first should STILL allow delete (order-independent): %v", err)
	}
	// And a reader-only policy must still be denied delete — proving the allow
	// above came from admin, not from a blanket fail-open.
	if err := enf.EnforcePolicy(ctx, &Policy{Name: "r", Subject: "u", Roles: []string{"reader"}}, "secrets", "delete"); err == nil {
		t.Fatal("FAIL-OPEN: reader-only policy allowed delete")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// End-to-end SINK via the gRPC façade: a read-only token must be DENIED a write
// route and a non-existent/empty token must be DENIED everything.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_GRPC_Authorize_ReadTokenDeniedWrite_BadTokenDeniedAll(t *testing.T) {
	orch := NewOrchestrator(nil)
	enf := NewPolicyEnforcer()
	ctx := context.Background()

	// Load roles whose NAMES equal the scope strings the token will carry, since
	// GRPCServer.Authorize threads scopes -> policy.Roles.
	if err := enf.LoadRole(ctx, &Role{Name: ScopeSessionRead, Permissions: []Permission{
		{Action: "GET", Resource: "/session/*"},
	}}); err != nil {
		t.Fatalf("load role: %v", err)
	}
	srv := NewGRPCServer(orch, enf)

	// Issue a read-only credential.
	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID: "reader", CommonName: "reader", Scopes: []string{ScopeSessionRead},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Read is allowed.
	resp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token: cert.ID, Resource: "/session/abc", Action: "GET",
	})
	if err != nil {
		t.Fatalf("authorize read: %v", err)
	}
	if !resp.GetAllowed() {
		t.Fatalf("regression: read-only token denied its own read (reason=%q)", resp.GetReason())
	}

	// Write must be DENIED (no role grants a write action to this scope).
	resp, err = srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token: cert.ID, Resource: "/session/abc", Action: "DELETE",
	})
	if err != nil {
		t.Fatalf("authorize write: %v", err)
	}
	if resp.GetAllowed() {
		t.Fatal("FAIL-OPEN: read-only token was allowed a DELETE")
	}

	// Empty token: InvalidArgument error, never an allow.
	if _, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{Token: "", Resource: "/x", Action: "GET"}); err == nil {
		t.Fatal("FAIL-OPEN: empty token did not error")
	}
	// Unknown token: structured deny, allowed=false.
	resp, err = srv.Authorize(ctx, &helixv1.AuthorizeRequest{Token: "nope", Resource: "/session/x", Action: "GET"})
	if err != nil {
		t.Fatalf("authorize unknown: %v", err)
	}
	if resp.GetAllowed() {
		t.Fatal("FAIL-OPEN: unknown token was allowed")
	}
}

// LATENT-RISK PIN: GRPCServer.Authenticate accepts a path-shaped identity and
// returns success=true, because it string-builds spiffe://helix.local/<id>
// without normalising. We assert the CURRENT behaviour AND prove it does not
// escalate the trust domain — the synthesized ID stays in helix.local. If a
// future change makes the synthesized path security-relevant (e.g. path-based
// authz), this pin flags that the raw identity is still unsanitised.
func TestAdv_LatentRisk_AuthenticatePathShapedIdentity_StaysInDomain(t *testing.T) {
	orch := NewOrchestrator(nil)
	srv := NewGRPCServer(orch, NewPolicyEnforcer())
	ctx := context.Background()

	resp, err := srv.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "../../evil.org/x"})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	// Documented current behaviour: success (no normalisation/rejection).
	if !resp.GetSuccess() {
		t.Skip("behaviour changed: Authenticate now rejects path-shaped identity — update this pin")
	}
	// The crucial security invariant: trust domain is NOT hijacked.
	parsed, perr := pkgsec.ParseSPIFFEID(resp.GetSpiffeId())
	if perr != nil {
		t.Fatalf("synthesized spiffe id did not parse: %v", perr)
	}
	if parsed.TrustDomain != "helix.local" {
		t.Fatalf("TRUST-DOMAIN HIJACK: path-shaped identity escalated TD to %q", parsed.TrustDomain)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (d) UNGUARDED SHARED STATE — hammer every shared map concurrently under -race.
// Orchestrator: certs, revokedSerials, identityScopes. PolicyEnforcer: roles,
// policies. Plus end-to-end through the gRPC façade.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_Concurrent_Orchestrator_NoDataRace(t *testing.T) {
	orch := NewOrchestrator(nil)
	ctx := context.Background()

	// Pre-issue a small pool of credentials ONCE (RSA-2048 keygen is expensive
	// and runs under the orchestrator write lock; doing it inside the hot loop
	// would serialise every worker behind keygen and starve the race detector
	// without adding map-access coverage). The concurrent storm below then
	// hammers the cheap shared-map paths (read/validate/rotate/revoke/register)
	// which are the actual unguarded-state surface.
	orch.RegisterIdentityScopes("id", []string{ScopeNodeRead})
	const pool = 8
	ids := make([]string, 0, pool)
	serials := make([]string, 0, pool)
	for i := 0; i < pool; i++ {
		cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{
			NodeID: "id", CommonName: "id", Scopes: []string{ScopeNodeRead, "admin"},
		})
		if err != nil {
			t.Fatalf("pre-issue: %v", err)
		}
		ids = append(ids, cert.ID)
		serials = append(serials, cert.Serial)
	}

	const workers = 24
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				orch.RegisterIdentityScopes("id", []string{ScopeNodeRead})
				tok := ids[i%pool]
				_, _, _, _ = orch.ValidateToken(ctx, tok)
				_, _ = orch.BoundScopes("id")
				_ = orch.IsRevokedSerial(serials[i%pool])
				_, _ = orch.RotateCredentials(ctx)
				_, _ = orch.GetCertificate(tok)
				if i%37 == 0 {
					_ = orch.RevokeCertificate(ctx, tok)
				}
			}
		}(w)
	}
	wg.Wait()

	// SINK assertion: clamping held under concurrency — "admin" must NEVER have
	// leaked into a credential bound to node:read only.
	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID: "id", CommonName: "id", Scopes: []string{ScopeNodeRead, "admin"},
	})
	if err != nil {
		t.Fatalf("final issue: %v", err)
	}
	for _, s := range cert.Scopes {
		if s == "admin" {
			t.Fatal("ESCALATION: 'admin' leaked past the node:read binding under concurrency")
		}
	}
}

func TestAdv_Concurrent_PolicyEnforcer_NoDataRace(t *testing.T) {
	enf := NewPolicyEnforcer()
	ctx := context.Background()
	_ = enf.LoadRole(ctx, &Role{Name: "reader", Permissions: []Permission{{Action: "read", Resource: "logs"}}})

	const workers = 24
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			pol := &Policy{Name: "p", Subject: "u", Roles: []string{"reader"}}
			for i := 0; i < 60; i++ {
				_ = enf.LoadRole(ctx, &Role{Name: "reader", Permissions: []Permission{{Action: "read", Resource: "logs"}}})
				_ = enf.LoadPolicy(ctx, pol)
				_ = enf.EnforcePolicy(ctx, pol, "logs", "read")
				_ = enf.EnforcePolicy(ctx, pol, "logs", "delete")
				_, _ = enf.GetRole("reader")
				_, _ = enf.GetPolicy("p")
				_ = enf.ListPolicies()
			}
		}(w)
	}
	wg.Wait()

	// SINK assertion: deny still denies after the concurrent storm.
	if err := enf.EnforcePolicy(ctx, &Policy{Name: "p", Subject: "u", Roles: []string{"reader"}}, "logs", "delete"); err == nil {
		t.Fatal("FAIL-OPEN: reader allowed delete after concurrent load")
	}
}
