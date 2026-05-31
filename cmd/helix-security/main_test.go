package main

import (
	"context"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthenticateSuccess(t *testing.T) {
	s := &server{}
	resp, err := s.Authenticate(context.Background(), &helixv1.AuthenticateRequest{
		Identity: "user1",
		Proof:    []byte("secret"),
		Method:   "password",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Error("expected success")
	}
	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
	if resp.SpiffeId == "" {
		t.Error("expected non-empty spiffe_id")
	}
}

func TestAuthenticateInvalidProof(t *testing.T) {
	s := &server{}
	resp, err := s.Authenticate(context.Background(), &helixv1.AuthenticateRequest{
		Identity: "user1",
		Proof:    []byte("invalid"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Error("expected failure for invalid proof")
	}
}

func TestAuthenticateMissingIdentity(t *testing.T) {
	s := &server{}
	_, err := s.Authenticate(context.Background(), &helixv1.AuthenticateRequest{})
	if err == nil {
		t.Fatal("expected error for missing identity")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestAuthorizeAllowed(t *testing.T) {
	s := &server{}
	resp, err := s.Authorize(context.Background(), &helixv1.AuthorizeRequest{
		Token:    "stub-token-abc",
		Resource: "cluster",
		Action:   "read",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Allowed {
		t.Error("expected allowed")
	}
}

func TestAuthorizeDenied(t *testing.T) {
	s := &server{}
	resp, err := s.Authorize(context.Background(), &helixv1.AuthorizeRequest{
		Token:    "bad-token",
		Resource: "cluster",
		Action:   "read",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Error("expected denied")
	}
	if resp.Reason == "" {
		t.Error("expected reason for denial")
	}
}

func TestAuthorizeMissingToken(t *testing.T) {
	s := &server{}
	_, err := s.Authorize(context.Background(), &helixv1.AuthorizeRequest{})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestIssueToken(t *testing.T) {
	s := &server{}
	resp, err := s.IssueToken(context.Background(), &helixv1.IssueTokenRequest{
		Identity:   "user1",
		Scopes:     []string{"read"},
		TtlSeconds: 60,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
	if resp.ExpiresAt == 0 {
		t.Error("expected non-zero expires_at")
	}
}

func TestIssueTokenMissingIdentity(t *testing.T) {
	s := &server{}
	_, err := s.IssueToken(context.Background(), &helixv1.IssueTokenRequest{})
	if err == nil {
		t.Fatal("expected error for missing identity")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestIssueTokenDefaultTTL(t *testing.T) {
	s := &server{}
	resp, err := s.IssueToken(context.Background(), &helixv1.IssueTokenRequest{
		Identity: "user1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Default TTL is 3600, so expires_at should be roughly now + 3600
	if resp.ExpiresAt <= 0 {
		t.Error("expected positive expires_at")
	}
}

func TestValidateTokenValid(t *testing.T) {
	s := &server{}
	resp, err := s.ValidateToken(context.Background(), &helixv1.ValidateTokenRequest{
		Token: "stub-token-xyz",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Valid {
		t.Error("expected valid")
	}
	if len(resp.Scopes) == 0 {
		t.Error("expected non-empty scopes")
	}
}

func TestValidateTokenInvalid(t *testing.T) {
	s := &server{}
	resp, err := s.ValidateToken(context.Background(), &helixv1.ValidateTokenRequest{
		Token: "bad-token",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Valid {
		t.Error("expected invalid")
	}
}

func TestValidateTokenMissingToken(t *testing.T) {
	s := &server{}
	_, err := s.ValidateToken(context.Background(), &helixv1.ValidateTokenRequest{})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestServerEmbedsUnimplemented(t *testing.T) {
	s := &server{}
	// Ensure the struct embeds UnimplementedSecurityServiceServer
	var _ helixv1.SecurityServiceServer = s
}
