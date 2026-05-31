package main

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/creack/pty"
	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockSessionServer is a minimal implementation of SessionServiceServer for testing.
type mockSessionServer struct {
	helixv1.UnimplementedSessionServiceServer
	sessions map[string]*helixv1.Session
	nextID   int
}

func newMockSessionServer() *mockSessionServer {
	return &mockSessionServer{
		sessions: make(map[string]*helixv1.Session),
	}
}

func (m *mockSessionServer) CreateSession(_ context.Context, req *helixv1.CreateSessionRequest) (*helixv1.Session, error) {
	m.nextID++
	id := fmt.Sprintf("sess-%d", m.nextID)
	sess := &helixv1.Session{
		Id:      id,
		Name:    req.Name,
		Owner:   req.Owner,
		Status:  "running",
		Backend: req.Backend,
		NodeId:  req.Mode,
	}
	m.sessions[id] = sess
	return sess, nil
}

func (m *mockSessionServer) GetSession(_ context.Context, req *helixv1.GetSessionRequest) (*helixv1.Session, error) {
	sess, ok := m.sessions[req.SessionId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}
	return sess, nil
}

func (m *mockSessionServer) ListSessions(_ context.Context, req *helixv1.ListSessionsRequest) (*helixv1.ListSessionsResponse, error) {
	var out []*helixv1.Session
	for _, s := range m.sessions {
		if req.Owner != "" && s.Owner != req.Owner {
			continue
		}
		if req.Status != "" && s.Status != req.Status {
			continue
		}
		out = append(out, s)
	}
	return &helixv1.ListSessionsResponse{Sessions: out}, nil
}

func (m *mockSessionServer) DeleteSession(_ context.Context, req *helixv1.DeleteSessionRequest) (*helixv1.DeleteSessionResponse, error) {
	if _, ok := m.sessions[req.SessionId]; !ok {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionId)
	}
	delete(m.sessions, req.SessionId)
	return &helixv1.DeleteSessionResponse{Success: true}, nil
}

func startMockServer(t *testing.T) (string, *grpc.Server, *mockSessionServer) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	mock := newMockSessionServer()
	helixv1.RegisterSessionServiceServer(grpcServer, mock)

	go func() {
		if err := grpcServer.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			t.Logf("mock server serve error: %v", err)
		}
	}()

	return lis.Addr().String(), grpcServer, mock
}

func TestClientCreateAndGet(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	client, err := NewClient(addr)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, err := client.CreateSession(ctx, "test-session", "tester", "tmux", "node-1")
	require.NoError(t, err)
	assert.Equal(t, "test-session", sess.Name)
	assert.Equal(t, "tester", sess.Owner)
	assert.Equal(t, "tmux", sess.Backend)
	assert.Equal(t, "node-1", sess.NodeId)
	assert.NotEmpty(t, sess.Id)

	got, err := client.GetSession(ctx, sess.Id)
	require.NoError(t, err)
	assert.Equal(t, sess.Id, got.Id)
	assert.Equal(t, sess.Name, got.Name)
}

func TestClientListAndKill(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	client, err := NewClient(addr)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.CreateSession(ctx, "sess-a", "alice", "tmux", "")
	require.NoError(t, err)
	_, err = client.CreateSession(ctx, "sess-b", "alice", "tmux", "")
	require.NoError(t, err)
	_, err = client.CreateSession(ctx, "sess-c", "bob", "tmux", "")
	require.NoError(t, err)

	all, err := client.ListSessions(ctx, "", "")
	require.NoError(t, err)
	assert.Len(t, all, 3)

	alice, err := client.ListSessions(ctx, "alice", "")
	require.NoError(t, err)
	assert.Len(t, alice, 2)

	// Kill one session.
	err = client.KillSession(ctx, all[0].Id)
	require.NoError(t, err)

	remaining, err := client.ListSessions(ctx, "", "")
	require.NoError(t, err)
	assert.Len(t, remaining, 2)
}

func TestClientKillNotFound(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	client, err := NewClient(addr)
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.KillSession(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

func TestParseFlags(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		expected map[string]string
	}{
		{
			name: "long flags with values",
			args: []string{"--session", "foo", "--node", "bar", "--command", "bash"},
			expected: map[string]string{
				"session": "foo",
				"node":    "bar",
				"command": "bash",
			},
		},
		{
			name: "equals syntax",
			args: []string{"--session=foo", "--node=bar"},
			expected: map[string]string{
				"session": "foo",
				"node":    "bar",
			},
		},
		{
			name: "short flags",
			args: []string{"-s", "foo", "-n", "bar"},
			expected: map[string]string{
				"s": "foo",
				"n": "bar",
			},
		},
		{
			name:     "no flags",
			args:     []string{},
			expected: map[string]string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFlags(tc.args)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestTerminalModeSwitch(t *testing.T) {
	// Use a pseudo-terminal pair to test raw mode without affecting the real terminal.
	ptyFile, tty, err := pty.Open()
	require.NoError(t, err)
	defer ptyFile.Close()
	defer tty.Close()

	term := NewTerminal(tty, tty)
	require.NotNil(t, term)

	// Should report as terminal.
	assert.True(t, term.IsTerminal())

	// Set raw mode.
	err = term.SetRawMode()
	require.NoError(t, err)

	// Restore.
	err = term.Restore()
	require.NoError(t, err)
}

func TestTerminalSize(t *testing.T) {
	ptyFile, tty, err := pty.Open()
	require.NoError(t, err)
	defer ptyFile.Close()
	defer tty.Close()

	// Set a known size on the PTY so GetSize returns non-zero values.
	require.NoError(t, pty.Setsize(ptyFile, &pty.Winsize{Cols: 100, Rows: 40}))

	term := NewTerminal(tty, tty)
	cols, rows, err := term.Size()
	require.NoError(t, err)
	assert.Greater(t, cols, 0)
	assert.Greater(t, rows, 0)
}
