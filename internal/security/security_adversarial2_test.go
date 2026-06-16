package security

// security_adversarial2_test.go — DEEP second-pass adversarial probes against
// internal/security, targeting FAIL-OPEN holes the first pass
// (security_adversarial_test.go) did not cover. Every assertion checks the
// ACTUAL allow/deny verdict at the sink, on hostile/degenerate input.
//
// CENTRAL FINDING (REAL BUG, fixed + left in tree):
//   EnforcePolicy granted the EMPTY operation. A role carrying a zero-value
//   Permission{} (Action:"", Resource:"") — which LoadRole accepts, because it
//   only validates the role *Name* — matched a request with action=="" and
//   resource=="" via matchAction(""," ")/matchResource("","") (both reduce to
//   ""==""), returning ALLOW. The gRPC Authorize façade forwards req.Action /
//   req.Resource verbatim with NO emptiness check, so an
//   AuthorizeRequest{Token: <valid>, Action:"", Resource:""} reached this path
//   and was ALLOWED whenever any role bound to the token's scopes held a
//   zero-value permission. That is a deny-by-default violation: an empty action
//   /resource is never a legitimate authorizable operation.
//
//   FIX (policy_enforcer.go EnforcePolicy): reject action=="" || resource=="".
//   MUTATION PROOF: TestAdv2_EnforcePolicy_DeniesEmptyActionOrResource and
//   TestAdv2_GRPC_Authorize_DeniesEmptyActionResource_ZeroPermRole fail
//   (FAIL-OPEN allow) when the new guard is removed, pass with it present.
//
// OTHER CLASSES (no live bug — pinned fail-closed):
//   - Authorize() URL-prefix matching ("/sessionABUSE" matches the "/session"
//     prefix) never grants a HIGHER privilege: a sibling/longer path still maps
//     to the SAME domain's :read|:write requirement. Pinned so a future rule
//     that introduced a cross-domain over-grant via prefix overlap is caught.
//   - matchResource hierarchical "prefix/*" does not leak to sibling prefixes
//     or the bare prefix; resource "*" must NOT match an empty resource once the
//     empty-resource guard is in place (the empty op is denied upstream).

import (
	"context"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
)

// ─────────────────────────────────────────────────────────────────────────────
// REAL BUG (fixed): EnforcePolicy must DENY an empty action or empty resource.
// A zero-value Permission{} in a loaded role would otherwise grant the empty op.
//
// MUTATION: delete the `if action == "" || resource == ""` guard in
// EnforcePolicy → the first two sub-cases below flip to allowed=true (FAIL-OPEN)
// and this test FAILs.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv2_EnforcePolicy_DeniesEmptyActionOrResource(t *testing.T) {
	ctx := context.Background()
	enf := NewPolicyEnforcer()

	// A role that LoadRole accepts (valid Name) but whose single permission is a
	// zero-value Permission{}: Action:"" Resource:"". This is the misconfiguration
	// the empty-op fail-open weaponised.
	if err := enf.LoadRole(ctx, &Role{Name: "broken", Permissions: []Permission{{}}}); err != nil {
		t.Fatalf("load role: %v", err)
	}
	pol := &Policy{Name: "p", Subject: "s", Roles: []string{"broken"}}

	// The empty operation MUST be denied even though a zero-value permission
	// "matches" it structurally.
	if err := enf.EnforcePolicy(ctx, pol, "", ""); err == nil {
		t.Fatal("FAIL-OPEN: EnforcePolicy allowed the empty operation (action='' resource='')")
	}
	// Empty action with a non-empty resource: still denied.
	if err := enf.EnforcePolicy(ctx, pol, "secrets", ""); err == nil {
		t.Fatal("FAIL-OPEN: EnforcePolicy allowed an empty action")
	}
	// Empty resource with a non-empty action: still denied.
	if err := enf.EnforcePolicy(ctx, pol, "", "read"); err == nil {
		t.Fatal("FAIL-OPEN: EnforcePolicy allowed an empty resource")
	}

	// Positive control: a genuine, fully-specified grant still works, so the
	// guard above is not just blanket-denying everything (which would mask the
	// real behaviour).
	if err := enf.LoadRole(ctx, &Role{Name: "real", Permissions: []Permission{
		{Action: "read", Resource: "logs"},
	}}); err != nil {
		t.Fatalf("load real role: %v", err)
	}
	okPol := &Policy{Name: "ok", Subject: "s", Roles: []string{"real"}}
	if err := enf.EnforcePolicy(ctx, okPol, "logs", "read"); err != nil {
		t.Fatalf("regression: legitimate read/logs denied after empty-op guard: %v", err)
	}
}

// End-to-end SINK through the gRPC Authorize façade: an AuthorizeRequest with an
// empty Action and empty Resource must be DENIED even when the token's scope is
// bound to a role that holds a zero-value permission. This proves the fail-open
// is reachable from the request boundary and is now closed.
//
// MUTATION: delete the empty-op guard in EnforcePolicy → resp.Allowed flips to
// true here (FAIL-OPEN) and this test FAILs.
func TestAdv2_GRPC_Authorize_DeniesEmptyActionResource_ZeroPermRole(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enf := NewPolicyEnforcer()

	// Role NAME equals the scope the token will carry (GRPCServer.Authorize threads
	// scopes -> policy.Roles), with a zero-value permission.
	if err := enf.LoadRole(ctx, &Role{Name: ScopeSessionRead, Permissions: []Permission{{}}}); err != nil {
		t.Fatalf("load role: %v", err)
	}
	srv := NewGRPCServer(orch, enf)

	cert, err := orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID: "reader", CommonName: "reader", Scopes: []string{ScopeSessionRead},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	resp, err := srv.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token: cert.ID, Resource: "", Action: "",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if resp.GetAllowed() {
		t.Fatal("FAIL-OPEN: gRPC Authorize allowed empty action+resource via a zero-value permission")
	}
}

// Guard against the empty-op fix over-reaching: a wildcard role (Action:"*",
// Resource:"*") — the canonical admin grant — must still ALLOW a real,
// fully-specified operation, and must NOT be turned into a denial by the new
// empty-op guard. (The guard keys off the REQUEST's action/resource, not the
// permission pattern, so a real request is unaffected.)
func TestAdv2_EnforcePolicy_WildcardAdminStillAllowsRealOp(t *testing.T) {
	ctx := context.Background()
	enf := NewPolicyEnforcer()
	if err := enf.LoadRole(ctx, &Role{Name: "admin", Permissions: []Permission{
		{Action: "*", Resource: "*"},
	}}); err != nil {
		t.Fatalf("load admin: %v", err)
	}
	pol := &Policy{Name: "a", Subject: "s", Roles: []string{"admin"}}
	if err := enf.EnforcePolicy(ctx, pol, "secrets/db", "delete"); err != nil {
		t.Fatalf("regression: admin wildcard denied a real op after empty-op guard: %v", err)
	}
	// But the empty op is denied even for an admin wildcard — there is no real
	// operation to authorize.
	if err := enf.EnforcePolicy(ctx, pol, "", ""); err == nil {
		t.Fatal("FAIL-OPEN: admin wildcard authorized the empty operation")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PIN (no live bug): Authorize() URL-prefix matching must never grant a HIGHER
// privilege than the domain demands. A sibling/longer path that shares a rule's
// prefix ("/sessionABUSE" ⊃ "/session") still maps to that domain's read|write
// requirement — it cannot, e.g., let a session:read scope reach a node:write
// route. Locks the route table so a future entry that introduced a cross-domain
// over-grant via prefix overlap is caught.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv2_Authorize_PrefixOverlapNeverEscalatesDomain(t *testing.T) {
	cases := []struct {
		name        string
		scopes      []string
		action, rte string
		wantAllowed bool
	}{
		// Sibling path under /session prefix: read scope, GET => stays session:read.
		{"sessionABUSE GET needs session:read", []string{ScopeSessionRead}, "GET", "/sessionABUSE", true},
		// Same sibling, a write verb => session:write, read scope must be DENIED.
		{"sessionABUSE POST needs session:write", []string{ScopeSessionRead}, "POST", "/sessionABUSE", false},
		// /nodefoo GET maps to node:read; a session:read scope must NOT satisfy it.
		{"nodefoo GET not satisfied by session:read", []string{ScopeSessionRead}, "GET", "/nodefoo", false},
		{"nodefoo GET satisfied by node:read", []string{ScopeNodeRead}, "GET", "/nodefoo", true},
		// A node:read scope must never reach a schedule (any-method) route.
		{"schedule needs schedule:write not node:read", []string{ScopeNodeRead}, "GET", "/schedule/job", false},
		// An undefined mutation on /pool (only GET defined) denies by default.
		{"pool DELETE undefined denies", []string{ScopePoolRead}, "DELETE", "/pool/x", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			d := Authorize(c.scopes, c.action, c.rte)
			if d.Allowed != c.wantAllowed {
				t.Fatalf("SINK MISMATCH: Authorize(%v,%q,%q).Allowed=%v want %v (required=%q reason=%q)",
					c.scopes, c.action, c.rte, d.Allowed, c.wantAllowed, d.RequiredScope, d.Reason)
			}
		})
	}
}

// PIN (no live bug): LoadRole / LoadPolicy reject an empty Name — an unnamed
// role can never be referenced and must not be silently stored as a usable
// (potentially over-privileged) grant. This is the existing guard that makes the
// "empty-named admin role" attack a non-starter; pin it so a regression that
// dropped the Name check is caught alongside the new empty-op guard.
func TestAdv2_LoadRole_RejectsEmptyName_NoGhostAdmin(t *testing.T) {
	ctx := context.Background()
	enf := NewPolicyEnforcer()

	// Attempt to register an unnamed all-powerful role.
	if err := enf.LoadRole(ctx, &Role{Name: "", Permissions: []Permission{{Action: "*", Resource: "*"}}}); err == nil {
		t.Fatal("FAIL-OPEN: LoadRole accepted an empty-named role")
	}
	// A policy referencing the empty role name resolves to NOTHING => denied.
	pol := &Policy{Name: "p", Subject: "s", Roles: []string{""}}
	if err := enf.EnforcePolicy(ctx, pol, "secrets", "delete"); err == nil {
		t.Fatal("FAIL-OPEN: empty role name produced an allow")
	}
}
