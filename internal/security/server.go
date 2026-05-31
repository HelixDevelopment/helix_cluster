package security

import (
	"context"
	"fmt"
	"strings"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCServer implements helixv1.SecurityServiceServer and delegates
// security operations to Orchestrator and PolicyEnforcer.
type GRPCServer struct {
	helixv1.UnimplementedSecurityServiceServer
	orch     *Orchestrator
	enforcer *PolicyEnforcer
}

// NewGRPCServer creates a new gRPC security server.
func NewGRPCServer(orch *Orchestrator, enforcer *PolicyEnforcer) *GRPCServer {
	return &GRPCServer{
		orch:     orch,
		enforcer: enforcer,
	}
}

// Authenticate performs identity verification and returns a token on success.
func (s *GRPCServer) Authenticate(ctx context.Context, req *helixv1.AuthenticateRequest) (*helixv1.AuthenticateResponse, error) {
	if req.Identity == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
	}

	// Validate identity via orchestrator (SPIFFE validation for SPIFFE IDs)
	spiffeID := req.Identity
	if !strings.HasPrefix(spiffeID, "spiffe://") {
		spiffeID = fmt.Sprintf("spiffe://helix.local/%s", req.Identity)
	}
	if err := s.orch.ValidateIdentity(ctx, spiffeID); err != nil {
		return &helixv1.AuthenticateResponse{Success: false}, nil
	}

	// Issue a real certificate as the token
	resp, err := s.orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID:     req.Identity,
		CommonName: req.Identity,
	})
	if err != nil {
		return nil, fmt.Errorf("issue certificate: %w", err)
	}

	return &helixv1.AuthenticateResponse{
		Success:  true,
		Token:    resp.ID,
		SpiffeId: spiffeID,
	}, nil
}

// Authorize checks whether the provided token may perform action on resource.
func (s *GRPCServer) Authorize(ctx context.Context, req *helixv1.AuthorizeRequest) (*helixv1.AuthorizeResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}

	// Validate token first, capturing the scopes/roles it actually carries.
	valid, identity, scopes, err := s.orch.ValidateToken(ctx, req.Token)
	if err != nil || !valid {
		return &helixv1.AuthorizeResponse{Allowed: false, Reason: "invalid token"}, nil
	}

	// Check policy via enforcer using the caller's REAL roles, not a hard-coded
	// "admin" grant. Hard-coding admin here was a privilege-escalation defect:
	// every authenticated identity would match an "admin" role if one were ever
	// loaded into the enforcer.
	policy := &Policy{
		Name:    identity,
		Subject: identity,
		Roles:   scopes,
	}
	err = s.enforcer.EnforcePolicy(ctx, policy, req.Resource, req.Action)
	if err != nil {
		return &helixv1.AuthorizeResponse{Allowed: false, Reason: err.Error()}, nil
	}

	return &helixv1.AuthorizeResponse{Allowed: true}, nil
}

// IssueToken generates a new token for the given identity and scopes.
func (s *GRPCServer) IssueToken(ctx context.Context, req *helixv1.IssueTokenRequest) (*helixv1.IssueTokenResponse, error) {
	if req.Identity == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
	}

	resp, err := s.orch.IssueCertificate(ctx, IssueCertificateRequest{
		NodeID:     req.Identity,
		CommonName: req.Identity,
	})
	if err != nil {
		return nil, fmt.Errorf("issue certificate: %w", err)
	}

	return &helixv1.IssueTokenResponse{
		Token:     resp.ID,
		ExpiresAt: resp.ExpiresAt.Unix(),
	}, nil
}

// ValidateToken checks token validity and returns decoded claims.
func (s *GRPCServer) ValidateToken(ctx context.Context, req *helixv1.ValidateTokenRequest) (*helixv1.ValidateTokenResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}

	valid, identity, scopes, err := s.orch.ValidateToken(ctx, req.Token)
	if err != nil || !valid {
		return &helixv1.ValidateTokenResponse{Valid: false}, nil
	}

	return &helixv1.ValidateTokenResponse{
		Valid:    true,
		Identity: identity,
		Scopes:   scopes,
	}, nil
}
