package security

import (
	"context"
	"fmt"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Authorize() — pure scope-to-route mapping ─────────────────────────────

// TestAuthorize_SessionWrite_ALLOW asserts an identity holding session:write is
// ALLOWED to create a session (POST /session/create).
//
// Mutation (§1.1): change the route table entry for POST /session to require
// ScopeSessionRead instead of ScopeSessionWrite -> RequiredScope becomes
// "session:read"; the identity holds "session:write" not "session:read" =>
// Allowed flips to false -> assert.True(d.Allowed) FAILS.
func TestAuthorize_SessionWrite_ALLOW(t *testing.T) {
	scopes := []string{ScopeSessionWrite}
	d := Authorize(scopes, "POST", "/session/create")

	assert.True(t, d.Allowed, "session:write must be ALLOWED on POST /session/create")
	assert.Equal(t, ScopeSessionWrite, d.RequiredScope, "required scope must be surfaced")
	assert.Equal(t, []string{ScopeSessionWrite}, d.EvaluatedScopes, "evaluated scopes must be the exact snapshot consulted")
	assert.Empty(t, d.Reason, "no denial reason on ALLOW")
}

// TestAuthorize_SessionWrite_Missing_DENY asserts an identity WITHOUT session:write
// is DENIED on a session-mutating route, and the evaluated scope set is surfaced.
//
// Mutation (§1.1): in the Authorize implementation replace `if _, has := held[required]`
// with `if true` -> every request is ALLOWED -> assert.False(d.Allowed) FAILS.
func TestAuthorize_SessionWrite_Missing_DENY(t *testing.T) {
	// Identity holds only session:read — not the write scope.
	scopes := []string{ScopeSessionRead, ScopeNodeRead}
	d := Authorize(scopes, "POST", "/session/create")

	assert.False(t, d.Allowed, "identity lacking session:write must be DENIED on POST /session/create")
	assert.Equal(t, ScopeSessionWrite, d.RequiredScope, "required scope must be surfaced even on denial")
	assert.Equal(t, scopes, d.EvaluatedScopes, "all evaluated scopes must appear in the decision")
	assert.Contains(t, d.Reason, "session:write", "denial reason must name the missing scope")
}

// TestAuthorize_NodeRead_ALLOW proves GET /node/... requires node:read and an
// identity holding it is ALLOWED.
//
// Mutation (§1.1): change the GET /node route table entry to ScopeNodeWrite ->
// the identity's node:read no longer satisfies the requirement -> d.Allowed
// becomes false -> assert.True(d.Allowed) FAILS.
func TestAuthorize_NodeRead_ALLOW(t *testing.T) {
	scopes := []string{ScopeNodeRead}
	d := Authorize(scopes, "GET", "/node/worker-1")

	assert.True(t, d.Allowed)
	assert.Equal(t, ScopeNodeRead, d.RequiredScope)
	assert.Equal(t, []string{ScopeNodeRead}, d.EvaluatedScopes)
}

// TestAuthorize_NodeWrite_Missing_DENY proves an identity with only node:read
// is DENIED on a node mutation (DELETE /node/worker-1).
//
// Mutation (§1.1): change the DELETE /node entry to ScopeNodeRead -> the
// identity's scope satisfies the requirement -> d.Allowed becomes true ->
// assert.False FAILS.
func TestAuthorize_NodeWrite_Missing_DENY(t *testing.T) {
	scopes := []string{ScopeNodeRead}
	d := Authorize(scopes, "DELETE", "/node/worker-1")

	assert.False(t, d.Allowed)
	assert.Equal(t, ScopeNodeWrite, d.RequiredScope)
	assert.Contains(t, d.Reason, "node:write")
	// EvaluatedScopes contains the exact snapshot that was consulted.
	assert.Equal(t, []string{ScopeNodeRead}, d.EvaluatedScopes)
}

// TestAuthorize_ScheduleWrite_ALLOW proves an identity with schedule:write is
// ALLOWED on a scheduler route.
//
// Mutation (§1.1): change the /schedule/ entry to ScopeAdvisoryAdmin ->
// the identity's schedule:write no longer satisfies it -> d.Allowed becomes
// false -> assert.True FAILS.
func TestAuthorize_ScheduleWrite_ALLOW(t *testing.T) {
	scopes := []string{ScopeScheduleWrite}
	d := Authorize(scopes, "POST", "/schedule/jobs")

	assert.True(t, d.Allowed)
	assert.Equal(t, ScopeScheduleWrite, d.RequiredScope)
	assert.Equal(t, []string{ScopeScheduleWrite}, d.EvaluatedScopes)
}

// TestAuthorize_ScheduleWrite_Missing_DENY proves an identity without
// schedule:write is DENIED even if it holds other write scopes.
//
// Mutation (§1.1): remove the routeScopeEntry for /schedule/ entirely ->
// requiredScope returns ("", false) -> the decision becomes "no scope rule
// defined..." (also false); however the RequiredScope would be "" not
// ScopeScheduleWrite -> assert.Equal(ScopeScheduleWrite, d.RequiredScope) FAILS.
func TestAuthorize_ScheduleWrite_Missing_DENY(t *testing.T) {
	scopes := []string{ScopeNodeWrite, ScopeSessionWrite}
	d := Authorize(scopes, "POST", "/schedule/jobs")

	assert.False(t, d.Allowed)
	assert.Equal(t, ScopeScheduleWrite, d.RequiredScope)
	assert.Contains(t, d.Reason, "schedule:write")
}

// TestAuthorize_AdvisoryAdmin_ALLOW proves advisory:admin unlocks /advisory/.
func TestAuthorize_AdvisoryAdmin_ALLOW(t *testing.T) {
	scopes := []string{ScopeAdvisoryAdmin}
	d := Authorize(scopes, "DELETE", "/advisory/nodes")

	assert.True(t, d.Allowed)
	assert.Equal(t, ScopeAdvisoryAdmin, d.RequiredScope)
}

// TestAuthorize_AdvisoryAdmin_Missing_DENY proves a non-admin is DENIED
// on /advisory/ even with every other scope present.
func TestAuthorize_AdvisoryAdmin_Missing_DENY(t *testing.T) {
	scopes := []string{ScopeNodeRead, ScopeNodeWrite, ScopeSessionRead, ScopeSessionWrite,
		ScopePoolRead, ScopeScheduleWrite} // everything EXCEPT advisory:admin
	d := Authorize(scopes, "DELETE", "/advisory/nodes")

	assert.False(t, d.Allowed)
	assert.Equal(t, ScopeAdvisoryAdmin, d.RequiredScope)
	assert.Contains(t, d.Reason, "advisory:admin")
}

// TestAuthorize_UnknownRoute_DENY proves that routes not covered by any entry
// are denied (fail-closed), and the evaluated scope set is still surfaced.
func TestAuthorize_UnknownRoute_DENY(t *testing.T) {
	scopes := []string{ScopeAdvisoryAdmin} // broadest possible credential
	d := Authorize(scopes, "GET", "/unknown/endpoint")

	assert.False(t, d.Allowed)
	assert.Empty(t, d.RequiredScope)
	assert.Equal(t, []string{ScopeAdvisoryAdmin}, d.EvaluatedScopes)
}

// TestAuthorize_EvaluatedScopesCopied proves the EvaluatedScopes slice in the
// returned decision is a defensive copy: mutating it after the call does not
// affect the input slice (and vice versa).
//
// Mutation (§1.1): in Authorize return `EvaluatedScopes: identityScopes`
// directly (no copy) -> mutating the returned slice corrupts the caller's
// input slice -> the post-mutation assert.Equal on the input slice FAILS.
func TestAuthorize_EvaluatedScopesCopied(t *testing.T) {
	input := []string{ScopeSessionWrite}
	d := Authorize(input, "POST", "/session/x")

	// Mutate the returned snapshot.
	d.EvaluatedScopes[0] = "corrupted"

	// The original input must be unchanged.
	assert.Equal(t, []string{ScopeSessionWrite}, input, "mutating EvaluatedScopes must not corrupt input slice")
}

// TestAuthorize_InlineMethodRoute proves the caller may supply "METHOD /path"
// as the action string (action contains a space), and the route argument is
// then used verbatim.
func TestAuthorize_InlineMethodRoute(t *testing.T) {
	scopes := []string{ScopeSessionWrite}
	// Supply the method and path concatenated in the action field.
	d := Authorize(scopes, "POST /session/create", "")

	assert.True(t, d.Allowed)
	assert.Equal(t, ScopeSessionWrite, d.RequiredScope)
}

// ── End-to-end: Authorize() through gRPC server with Helix scopes ─────────

// TestHXC1103_SessionWrite_ALLOW_EndToEnd is the primary closure-criteria
// test for HXC-1103.  It issues a token with Scopes:[ScopeSessionWrite] via
// the GRPCServer.IssueToken path, then exercises the pure Authorize function
// with the token's validated scope set.  Both the ALLOW and DENY decisions are
// captured with the evaluated scope set, and the evaluated scopes are asserted
// to contain exactly the expected value.
//
// Mutation (§1.1): in scopes.go change the POST /session route entry to
// require ScopeSessionRead -> the session:write identity gets DENIED ->
// assert.True(allowD.Allowed) FAILS.
func TestHXC1103_SessionWrite_ALLOW_EndToEnd(t *testing.T) {
	ctx := context.Background()
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()
	srv := NewGRPCServer(orch, enforcer)

	// Issue a token that carries exactly session:write.
	issue, err := srv.IssueToken(ctx, &helixv1.IssueTokenRequest{
		Identity: "svc-session-writer",
		Scopes:   []string{ScopeSessionWrite},
	})
	require.NoError(t, err)
	require.NotEmpty(t, issue.Token)

	// Retrieve the validated scope set (sink-side fact: what the orchestrator
	// stored and is now returning through ValidateToken).
	valid, identity, tokenScopes, err := orch.ValidateToken(ctx, issue.Token)
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, "svc-session-writer", identity)
	require.Equal(t, []string{ScopeSessionWrite}, tokenScopes,
		"token must carry exactly the issued scope, not the legacy read/write default")

	// ALLOW: session:write -> POST /session/create.
	allowD := Authorize(tokenScopes, "POST", "/session/create")
	assert.True(t, allowD.Allowed, "session:write must be ALLOWED on session-mutating route")
	assert.Equal(t, ScopeSessionWrite, allowD.RequiredScope)
	assert.Equal(t, []string{ScopeSessionWrite}, allowD.EvaluatedScopes,
		"evaluated scope set must be surfaced in ALLOW decision")

	// DENY: same token has no session:read for a read-only route... but it also
	// has no node:write for a node mutation.  Demonstrate both directions.
	denyD := Authorize(tokenScopes, "DELETE", "/node/worker-1")
	assert.False(t, denyD.Allowed, "session:write must be DENIED on node-mutation route")
	assert.Equal(t, ScopeNodeWrite, denyD.RequiredScope)
	assert.Equal(t, []string{ScopeSessionWrite}, denyD.EvaluatedScopes,
		"evaluated scope set must be surfaced in DENY decision")
	assert.Contains(t, denyD.Reason, "node:write")
}

// TestHXC1103_ScopelessIdentity_DENY proves an identity with no scopes is
// denied on every Helix route, confirming fail-closed behavior.
//
// Mutation (§1.1): in requiredScope return ("", false) unconditionally ->
// Authorize returns Reason containing "no scope rule defined"; the
// RequiredScope is "" not ScopeSessionWrite ->
// assert.Equal(ScopeSessionWrite, d.RequiredScope) FAILS.
func TestHXC1103_ScopelessIdentity_DENY(t *testing.T) {
	var noScopes []string

	routes := []struct {
		method string
		route  string
		want   string
	}{
		{"POST", "/session/create", ScopeSessionWrite},
		{"GET", "/session/list", ScopeSessionRead},
		{"DELETE", "/node/worker-1", ScopeNodeWrite},
		{"GET", "/node/status", ScopeNodeRead},
		{"POST", "/schedule/jobs", ScopeScheduleWrite},
	}
	for _, tc := range routes {
		t.Run(fmt.Sprintf("%s %s", tc.method, tc.route), func(t *testing.T) {
			d := Authorize(noScopes, tc.method, tc.route)
			assert.False(t, d.Allowed)
			assert.Equal(t, tc.want, d.RequiredScope)
			assert.Empty(t, d.EvaluatedScopes, "empty input -> empty evaluated scopes in decision")
		})
	}
}

// TestHXC1103_AllScopeConstants_Defined proves all seven canonical Helix
// scopes are present in AllDefinedScopes and carry the expected string values.
// This guards against typos and scope-set truncation.
//
// Mutation (§1.1): remove ScopeScheduleWrite from AllDefinedScopes ->
// assert.Contains(AllDefinedScopes, ScopeScheduleWrite) FAILS.
func TestHXC1103_AllScopeConstants_Defined(t *testing.T) {
	expected := []string{
		ScopeNodeRead,      // "node:read"
		ScopeNodeWrite,     // "node:write"
		ScopeSessionRead,   // "session:read"
		ScopeSessionWrite,  // "session:write"
		ScopePoolRead,      // "pool:read"
		ScopeScheduleWrite, // "schedule:write"
		ScopeAdvisoryAdmin, // "advisory:admin"
	}
	assert.Len(t, AllDefinedScopes, len(expected), "AllDefinedScopes must list exactly 7 canonical scopes")
	for _, s := range expected {
		assert.Contains(t, AllDefinedScopes, s, "canonical scope %q must be in AllDefinedScopes", s)
	}

	// Verify the actual string values (guard against silent rename).
	assert.Equal(t, "node:read", ScopeNodeRead)
	assert.Equal(t, "node:write", ScopeNodeWrite)
	assert.Equal(t, "session:read", ScopeSessionRead)
	assert.Equal(t, "session:write", ScopeSessionWrite)
	assert.Equal(t, "pool:read", ScopePoolRead)
	assert.Equal(t, "schedule:write", ScopeScheduleWrite)
	assert.Equal(t, "advisory:admin", ScopeAdvisoryAdmin)
}

// TestHXC1103_NodeStar_Coverage proves both node:read and node:write are
// individually enforced (the "node:*" family specified in the task).
//
// Mutation (§1.1): collapse the two node entries into a single "node:read" ->
// node:write is never required -> a GET /node/x with only node:write is now
// ALLOWED (shouldn't be per clean separation) — but more critically the test
// for node:write on DELETE becomes DENIED for the wrong reason.
func TestHXC1103_NodeStar_Coverage(t *testing.T) {
	// node:read alone is sufficient for GET but NOT for DELETE.
	readOnly := []string{ScopeNodeRead}
	assert.True(t, Authorize(readOnly, "GET", "/node/worker").Allowed)
	assert.False(t, Authorize(readOnly, "DELETE", "/node/worker").Allowed)

	// node:write alone is sufficient for DELETE but NOT for GET (requires node:read).
	writeOnly := []string{ScopeNodeWrite}
	assert.True(t, Authorize(writeOnly, "DELETE", "/node/worker").Allowed)
	assert.False(t, Authorize(writeOnly, "GET", "/node/worker").Allowed)

	// Both scopes cover both operations.
	both := []string{ScopeNodeRead, ScopeNodeWrite}
	assert.True(t, Authorize(both, "GET", "/node/worker").Allowed)
	assert.True(t, Authorize(both, "DELETE", "/node/worker").Allowed)
}
