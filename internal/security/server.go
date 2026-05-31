package security

import (
	"context"
	"fmt"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCServer implements helixv1.SecurityServiceServer and delegates
// security operations to Orchestrator and PolicyEnforcer.
type GRPCServer struct {
	helixv1.UnimplementedSecurityServiceServer
	orch   *Orchestrator
	enforcer *PolicyEnforcer
}

// NewGRPCServer creates a new gRPC security server.
func NewGRPCServer(orch *Orchestrator, enforcer *PolicyEnforcer) *GRPCServer {
	return &GRPCServer{
		orch:     orch,
		enforcer: enforcer,
	}
}

// IssueCert issues a TLS certificate for a node.
func (s *GRPCServer) IssueCert(ctx context.Context, req *helixv1.IssueTokenRequest) (*helixv1.IssueTokenResponse, error) {
	if req.Identity == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
	}

	issueReq := IssueCertificateRequest{
		NodeID:     req.Identity,
		CommonName: req.Identity,
	}
	if req.TtlSeconds > 0 {
		// TTL is handled inside IssueCertificate; we keep the interface simple.
	}

	cert, err := s.orch.IssueCertificate(ctx, issueReq)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issue certificate: %v", err)
	}

	return &helixv1.IssueTokenResponse{
		Token:     cert.ID,
		ExpiresAt: cert.ExpiresAt.Unix(),
	}, nil
}

// RevokeCert revokes a certificate by its ID.
func (s *GRPCServer) RevokeCert(ctx context.Context, req *helixv1.ValidateTokenRequest) (*helixv1.AuthorizeResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token (cert ID) is required")
	}

	if err := s.orch.RevokeCertificate(ctx, req.Token); err != nil {
		return nil, status.Errorf(codes.NotFound, "revoke certificate: %v", err)
	}

	return &helixv1.AuthorizeResponse{Allowed: true}, nil
}

// ValidateIdentity validates a SPIFFE identity string.
func (s *GRPCServer) ValidateIdentity(ctx context.Context, req *helixv1.AuthenticateRequest) (*helixv1.AuthenticateResponse, error) {
	if req.Identity == "" {
		return nil, status.Error(codes.InvalidArgument, "identity (SPIFFE ID) is required")
	}

	if err := s.orch.ValidateIdentity(ctx, req.Identity); err != nil {
		return &helixv1.AuthenticateResponse{
			Success: false,
			SpiffeId: req.Identity,
		}, nil
	}

	return &helixv1.AuthenticateResponse{
		Success:  true,
		SpiffeId: req.Identity,
	}, nil
}

// RotateCreds triggers credential rotation.
func (s *GRPCServer) RotateCreds(ctx context.Context, _ *helixv1.ValidateTokenRequest) (*helixv1.IssueTokenResponse, error) {
	count, err := s.orch.RotateCredentials(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rotate credentials: %v", err)
	}

	return &helixv1.IssueTokenResponse{
		Token: fmt.Sprintf("rotated-%d", count),
	}, nil
}

// CheckPolicy checks whether an action on a resource is allowed by a policy.
func (s *GRPCServer) CheckPolicy(ctx context.Context, req *helixv1.AuthorizeRequest) (*helixv1.AuthorizeResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "policy name is required")
	}
	if req.Resource == "" {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	if req.Action == "" {
		return nil, status.Error(codes.InvalidArgument, "action is required")
	}

	policy, ok := s.enforcer.GetPolicy(req.Token)
	if !ok {
		return &helixv1.AuthorizeResponse{Allowed: false, Reason: "policy not found"}, nil
	}

	if err := s.enforcer.EnforcePolicy(ctx, policy, req.Resource, req.Action); err != nil {
		return &helixv1.AuthorizeResponse{Allowed: false, Reason: err.Error()}, nil
	}

	return &helixv1.AuthorizeResponse{Allowed: true}, nil
}
