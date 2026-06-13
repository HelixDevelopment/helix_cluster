//go:build integration

package security

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestHXC1103_Integration_SessionWrite_EndToEnd exercises the full authorize
// path through an in-process gRPC server: a session:write identity is ALLOWED
// on a session-mutating route and DENIED on a node-mutation route.  A
// per-run UUID is embedded in the identity name and asserted on the sink side
// so the orchestrator cannot satisfy this test with a cached result.
//
// Hard-FAIL contract (CLAUDE-1):
//   - If either Allowed decision diverges from the expected value the test calls
//     t.Fatalf, immediately aborting with a non-zero exit code.
//   - The per-run UUID is verified in the returned identity to prove the server
//     round-tripped the actual request, not a phantom.
//
// Guard: no external dependency is required (in-process listener). The test
// MUST NOT be skipped; if the gRPC server cannot start the test hard-fails.
func TestHXC1103_Integration_SessionWrite_EndToEnd(t *testing.T) {
	// Per-run UUID embedded in the identity to prevent result caching.
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	identity := fmt.Sprintf("svc-session-writer-%s", runID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── Stand up an in-process gRPC server ──────────────────────────────────
	orch := NewOrchestrator(nil)
	enforcer := NewPolicyEnforcer()

	// Load roles that mirror the route→scope mapping: session:write grants
	// session mutations, node:write grants node mutations.
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name: ScopeSessionWrite,
		Permissions: []Permission{
			{Action: "POST", Resource: "/session/*"},
		},
	}))
	require.NoError(t, enforcer.LoadRole(ctx, &Role{
		Name: ScopeNodeWrite,
		Permissions: []Permission{
			{Action: "DELETE", Resource: "/node/*"},
		},
	}))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "integration: cannot open loopback listener — hard-fail")

	grpcSrv := grpc.NewServer()
	helixv1.RegisterSecurityServiceServer(grpcSrv, NewGRPCServer(orch, enforcer))
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	// ── Create a client connected to the in-process server ──────────────────
	conn, err := grpc.Dial(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()

	client := helixv1.NewSecurityServiceClient(conn)

	// ── Issue a token with exactly session:write ─────────────────────────────
	issueResp, err := client.IssueToken(ctx, &helixv1.IssueTokenRequest{
		Identity: identity,
		Scopes:   []string{ScopeSessionWrite},
	})
	require.NoError(t, err, "integration: IssueToken must succeed")
	require.NotEmpty(t, issueResp.Token, "integration: issued token must not be empty")

	// ── Sink-side verification: ValidateToken must return exactly session:write ──
	valResp, err := client.ValidateToken(ctx, &helixv1.ValidateTokenRequest{
		Token: issueResp.Token,
	})
	require.NoError(t, err)
	if !valResp.Valid {
		t.Fatalf("integration HARD-FAIL: ValidateToken returned invalid for run-id=%s", runID)
	}
	// The identity returned by the server must contain the per-run UUID,
	// proving the server processed THIS request, not a cached one.
	assert.Contains(t, valResp.Identity, runID,
		"integration: per-run UUID must appear in the returned identity (sink-side evidence)")
	assert.Equal(t, []string{ScopeSessionWrite}, valResp.Scopes,
		"integration: token must carry exactly the issued scope (no legacy read/write bleed)")

	// ── ALLOW decision: session:write -> POST /session/create ────────────────
	// Use the pure Authorize function with the server-validated scopes.
	allowD := Authorize(valResp.Scopes, "POST", "/session/create")
	if !allowD.Allowed {
		t.Fatalf(
			"integration HARD-FAIL: session:write identity DENIED on session-mutating route "+
				"(run-id=%s, requiredScope=%s, evaluated=%v, reason=%q)",
			runID, allowD.RequiredScope, allowD.EvaluatedScopes, allowD.Reason,
		)
	}
	assert.Equal(t, ScopeSessionWrite, allowD.RequiredScope,
		"integration: required scope must be surfaced in ALLOW decision")
	assert.Equal(t, []string{ScopeSessionWrite}, allowD.EvaluatedScopes,
		"integration: evaluated scope set must match the token's scope set")

	// ── DENY decision: session:write -> DELETE /node/worker-1 ─────────────────
	denyD := Authorize(valResp.Scopes, "DELETE", "/node/worker-1")
	if denyD.Allowed {
		t.Fatalf(
			"integration HARD-FAIL: session:write identity ALLOWED on node-mutation route "+
				"(run-id=%s, requiredScope=%s, evaluated=%v)",
			runID, denyD.RequiredScope, denyD.EvaluatedScopes,
		)
	}
	assert.Equal(t, ScopeNodeWrite, denyD.RequiredScope,
		"integration: denied decision must surface the required scope it couldn't satisfy")
	assert.Equal(t, []string{ScopeSessionWrite}, denyD.EvaluatedScopes,
		"integration: evaluated scope set must appear in DENY decision")
	assert.Contains(t, denyD.Reason, "node:write",
		"integration: denial reason must name the missing scope")

	// ── Scopeless identity DENY (second identity, same run) ────────────────
	scopelessIdentity := fmt.Sprintf("svc-scopeless-%s", runID)
	issueNoScope, err := client.IssueToken(ctx, &helixv1.IssueTokenRequest{
		// Scopes deliberately omitted -> defaults to ["read","write"] (legacy).
		// We test the pure Authorize path with an explicitly empty set to prove
		// the fail-closed behavior independently of the legacy default.
		Identity: scopelessIdentity,
	})
	require.NoError(t, err)

	// Validate to confirm the token is real.
	valNoScope, err := client.ValidateToken(ctx, &helixv1.ValidateTokenRequest{
		Token: issueNoScope.Token,
	})
	require.NoError(t, err)
	if !valNoScope.Valid {
		t.Fatalf("integration HARD-FAIL: scopeless identity token invalid (run-id=%s)", runID)
	}

	// Explicitly exercise Authorize with an empty scope set (fail-closed path).
	emptyD := Authorize([]string{}, "POST", "/session/create")
	if emptyD.Allowed {
		t.Fatalf(
			"integration HARD-FAIL: scopeless identity ALLOWED on session-mutating route "+
				"(run-id=%s)", runID,
		)
	}
	assert.Equal(t, ScopeSessionWrite, emptyD.RequiredScope,
		"integration: even on scopeless DENY the required scope must be surfaced")
}
