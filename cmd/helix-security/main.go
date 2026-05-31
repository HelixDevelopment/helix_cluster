// Command helix-security is the security manager daemon for Helix Cluster OS.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// server implements helixv1.SecurityService.
type server struct {
	helixv1.UnimplementedSecurityServiceServer
}

// Authenticate performs identity verification and returns a token on success.
func (s *server) Authenticate(ctx context.Context, req *helixv1.AuthenticateRequest) (*helixv1.AuthenticateResponse, error) {
	if req.Identity == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
	}
	// Stub: always succeed for known demo identity
	if string(req.Proof) == "invalid" {
		return &helixv1.AuthenticateResponse{Success: false}, nil
	}
	return &helixv1.AuthenticateResponse{
		Success:  true,
		Token:    fmt.Sprintf("stub-token-%s-%d", req.Identity, time.Now().Unix()),
		SpiffeId: fmt.Sprintf("spiffe://helix.local/%s", req.Identity),
	}, nil
}

// Authorize checks whether the provided token may perform action on resource.
func (s *server) Authorize(ctx context.Context, req *helixv1.AuthorizeRequest) (*helixv1.AuthorizeResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	// Stub: allow everything for tokens starting with "stub-token-"
	allowed := len(req.Token) > 10 && req.Token[:10] == "stub-token"
	reason := ""
	if !allowed {
		reason = "token not recognized"
	}
	return &helixv1.AuthorizeResponse{Allowed: allowed, Reason: reason}, nil
}

// IssueToken generates a new token for the given identity and scopes.
func (s *server) IssueToken(ctx context.Context, req *helixv1.IssueTokenRequest) (*helixv1.IssueTokenResponse, error) {
	if req.Identity == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
	}
	ttl := req.TtlSeconds
	if ttl == 0 {
		ttl = 3600
	}
	return &helixv1.IssueTokenResponse{
		Token:     fmt.Sprintf("stub-token-%s-%d", req.Identity, time.Now().Unix()),
		ExpiresAt: time.Now().Unix() + ttl,
	}, nil
}

// ValidateToken checks token validity and returns decoded claims.
func (s *server) ValidateToken(ctx context.Context, req *helixv1.ValidateTokenRequest) (*helixv1.ValidateTokenResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	valid := len(req.Token) > 10 && req.Token[:10] == "stub-token"
	if !valid {
		return &helixv1.ValidateTokenResponse{Valid: false}, nil
	}
	return &helixv1.ValidateTokenResponse{
		Valid:    true,
		Identity: "unknown",
		Scopes:   []string{"read", "write"},
	}, nil
}

func main() {
	port := os.Getenv("HELIX_SECURITY_PORT")
	if port == "" {
		port = "50056"
	}

	s := grpc.NewServer()
	helixv1.RegisterSecurityServiceServer(s, &server{})

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	go func() {
		log.Printf("helix-security listening on :%s", port)
		if err := s.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	s.GracefulStop()
}
