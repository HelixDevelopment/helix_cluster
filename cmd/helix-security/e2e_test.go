//go:build e2e

// Package main's e2e suite drives the REAL helix-security binary end-to-end as an
// end user (a workload obtaining and validating a SPIFFE token over gRPC) would:
// it `go build`s the command, runs that artifact on an env-driven ephemeral
// (":0") port, dials it with a genuine gRPC client, and asserts the OBSERVABLE
// token round-trip contract:
//   - IssueToken(identity=X) returns a non-empty token.
//   - ValidateToken(token) returns valid=true with identity=X.
//
// This is a genuine end-to-end exercise (CLAUDE-1): it proves the shipped binary
// runs the real internal/security token issuance + validation logic over the
// wire — not an in-process call to run()/newSecurityServer, and not the removed
// "stub-token-..." bluff. The port is never hard-coded: it is passed as "0" via
// HELIX_SECURITY_PORT and the actually-bound address is read from the binary's
// "helix-security listening on <addr>" startup log.
//
// Run with: go test -tags e2e ./cmd/helix-security/... -run TestE2E -v
package main

import (
	"context"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestE2ESecurityBinaryIssuesAndValidatesToken builds and runs the real
// helix-security binary, then performs a full IssueToken -> ValidateToken
// round-trip over a real gRPC connection.
func TestE2ESecurityBinaryIssuesAndValidatesToken(t *testing.T) {
	bin := e2eBuild(t, "./cmd/helix-security")

	// Env-driven port discovered dynamically (no hard-coded port): reserve a free
	// ephemeral port and hand it to the binary. The binary's config validation
	// rejects a literal "0", so we pass the concrete kernel-assigned number.
	port := e2eFreePort(t)
	proc := e2eStart(t, bin, []string{portEnv("HELIX_SECURITY_PORT", port)})
	defer proc.stop(t)

	addr := proc.waitForAddr(t, "helix-security listening on ")

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial helix-security at %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()

	client := helixv1.NewSecurityServiceClient(conn)

	const identity = "spiffe://helix.cluster/workload/e2e-tester"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// ---- IssueToken(identity=X) -> non-empty token ----
	issued, err := client.IssueToken(ctx, &helixv1.IssueTokenRequest{
		Identity:   identity,
		Scopes:     []string{"scheduler.read"},
		TtlSeconds: 300,
	})
	if err != nil {
		t.Fatalf("IssueToken RPC against real binary failed: %v", err)
	}
	if issued.GetToken() == "" {
		t.Fatalf("IssueToken returned an empty token for identity %q", identity)
	}
	t.Logf("e2e IssueToken -> token issued (len=%d, expires_at=%d)", len(issued.GetToken()), issued.GetExpiresAt())

	// ---- ValidateToken(token) -> valid=true, identity=X ----
	validated, err := client.ValidateToken(ctx, &helixv1.ValidateTokenRequest{
		Token: issued.GetToken(),
	})
	if err != nil {
		t.Fatalf("ValidateToken RPC against real binary failed: %v", err)
	}
	if !validated.GetValid() {
		t.Fatalf("ValidateToken reported the freshly-issued token as invalid; resp=%+v", validated)
	}
	if got := validated.GetIdentity(); got != identity {
		t.Fatalf("ValidateToken identity mismatch: got %q, want %q", got, identity)
	}
	t.Logf("e2e ValidateToken -> valid=true identity=%q scopes=%v", validated.GetIdentity(), validated.GetScopes())
}
