// Package session provides tests for the Session gRPC server.
package session

import (
	"context"
	"net"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func startTestServer(t *testing.T) (helixv1.SessionServiceClient, func()) {
	t.Helper()

	srv := NewServer()
	gs := grpc.NewServer()
	helixv1.RegisterSessionServiceServer(gs, srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() {
		if err := gs.Serve(lis); err != nil {
			t.Logf("serve: %v", err)
		}
	}()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	client := helixv1.NewSessionServiceClient(conn)
	return client, func() {
		conn.Close()
		gs.GracefulStop()
	}
}

func TestServerCreateSession(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "test", Owner: "alice"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.Name != "test" {
		t.Errorf("name = %s, want test", resp.Name)
	}
	if resp.Owner != "alice" {
		t.Errorf("owner = %s, want alice", resp.Owner)
	}
}

func TestServerGetSession(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s1", Owner: "bob"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := client.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Id != created.Id {
		t.Errorf("id = %s, want %s", got.Id, created.Id)
	}
}

func TestServerListSessions(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s2", Owner: "carol"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	list, err := client.ListSessions(ctx, &helixv1.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list.Sessions) == 0 {
		t.Error("expected at least one session")
	}
}

func TestServerUpdateSession(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s3", Owner: "dave"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	updated, err := client.UpdateSession(ctx, &helixv1.UpdateSessionRequest{SessionId: created.Id, Status: "paused"})
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if updated.Status != "paused" {
		t.Errorf("status = %s, want paused", updated.Status)
	}
}

func TestServerDeleteSession(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s4", Owner: "eve"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	resp, err := client.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !resp.Success {
		t.Error("expected success=true")
	}
}
