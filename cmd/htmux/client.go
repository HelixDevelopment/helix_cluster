// Package main provides the htmux CLI.
package main

import (
	"context"
	"fmt"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultSessionAddr = "localhost:50053"

// Client wraps the gRPC SessionServiceClient with connection management.
type Client struct {
	conn   *grpc.ClientConn
	client helixv1.SessionServiceClient
}

// NewClient creates a new htmux client connected to the session service.
func NewClient(addr string) (*Client, error) {
	if addr == "" {
		addr = defaultSessionAddr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to session service at %s: %w", addr, err)
	}

	return &Client{
		conn:   conn,
		client: helixv1.NewSessionServiceClient(conn),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// CreateSession creates a new session on the cluster.
func (c *Client) CreateSession(ctx context.Context, name, owner, backend, nodeID string) (*helixv1.Session, error) {
	req := &helixv1.CreateSessionRequest{
		Name:    name,
		Owner:   owner,
		Backend: backend,
	}
	if nodeID != "" {
		// Node affinity can be expressed via labels or future proto fields.
		// For now we pass mode as a hint if the server supports it.
		req.Mode = nodeID
	}

	sess, err := c.client.CreateSession(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// GetSession retrieves a session by ID.
func (c *Client) GetSession(ctx context.Context, sessionID string) (*helixv1.Session, error) {
	req := &helixv1.GetSessionRequest{SessionId: sessionID}
	sess, err := c.client.GetSession(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", sessionID, err)
	}
	return sess, nil
}

// ListSessions lists sessions, optionally filtered by owner and status.
func (c *Client) ListSessions(ctx context.Context, owner, status string) ([]*helixv1.Session, error) {
	req := &helixv1.ListSessionsRequest{
		Owner:  owner,
		Status: status,
	}
	resp, err := c.client.ListSessions(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return resp.Sessions, nil
}

// KillSession terminates a session by ID.
func (c *Client) KillSession(ctx context.Context, sessionID string) error {
	req := &helixv1.DeleteSessionRequest{SessionId: sessionID}
	_, err := c.client.DeleteSession(ctx, req)
	if err != nil {
		return fmt.Errorf("kill session %s: %w", sessionID, err)
	}
	return nil
}

// RenameSession updates the name of an existing session.
func (c *Client) RenameSession(ctx context.Context, sessionID, newName string) (*helixv1.Session, error) {
	// The API does not have a dedicated rename RPC; we use UpdateSession.
	// The server currently only supports status and resources updates,
	// so we return an error indicating the limitation.
	_ = sessionID
	_ = newName
	return nil, fmt.Errorf("rename session: not supported by session service API")
}
