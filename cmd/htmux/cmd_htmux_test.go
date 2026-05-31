package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockSessionServer is a minimal real implementation of SessionServiceServer.
// Tests dial it over a real loopback gRPC connection (no mocked transport).
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
	t.Helper()
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

// testEnv builds an env whose dial connects to a real in-process gRPC server.
func testEnv(addr string) (*env, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	e := &env{
		stdout:      &out,
		stderr:      &errBuf,
		dial:        dialGRPC,
		lookupOwner: func() (string, error) { return "default-user", nil },
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return e, &out, &errBuf
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
	assert.Equal(t, "node-1", sess.NodeID)
	assert.NotEmpty(t, sess.ID)

	got, err := client.GetSession(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, sess.ID, got.ID)
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
	err = client.KillSession(ctx, all[0].ID)
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

func TestRenameSessionUnsupported(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	client, err := NewClient(addr)
	require.NoError(t, err)
	defer client.Close()

	_, err = client.RenameSession(context.Background(), "sess-1", "newname")
	require.Error(t, err)
	// Contract: rename must surface the API limitation, not silently succeed.
	// Mutation: returning (nil, nil) from RenameSession fails this test.
	assert.Contains(t, err.Error(), "not supported")
}

// --- run()-level end-to-end tests against a real in-process gRPC server ---

func TestRunCreateExactOutput(t *testing.T) {
	addr, srv, mock := startMockServer(t)
	defer srv.Stop()

	e, out, _ := testEnv(addr)
	code := run(context.Background(), []string{"create", "--session", "demo", "--node", "node-7", "--addr", addr}, e)

	require.Equal(t, 0, code)
	// Mock assigns sess-1 to the first session; node echoes back via Mode.
	// Mutation: changing the "Created session %s (%s) on node %s" format,
	// or dropping the node arg, breaks this exact-string assertion.
	assert.Equal(t, "Created session demo (sess-1) on node node-7\n", out.String())

	// Sink-side proof: the server actually holds the created session.
	require.Len(t, mock.sessions, 1)
	assert.Equal(t, "demo", mock.sessions["sess-1"].Name)
	assert.Equal(t, "default-user", mock.sessions["sess-1"].Owner)
}

func TestRunCreateMissingSessionIsUsageError(t *testing.T) {
	addr, srv, mock := startMockServer(t)
	defer srv.Stop()

	e, out, errBuf := testEnv(addr)
	code := run(context.Background(), []string{"create", "--addr", addr}, e)

	// Mutation: removing the "create requires --session" guard would dial the
	// server and return 0 here; this asserts exit code 1 and no session made.
	require.Equal(t, 1, code)
	assert.Empty(t, out.String())
	assert.Contains(t, errBuf.String(), "create requires --session")
	assert.Len(t, mock.sessions, 0)
}

func TestRunListExactTableAndFilter(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	// Seed two sessions via the create path.
	e1, _, _ := testEnv(addr)
	require.Equal(t, 0, run(context.Background(), []string{"create", "-s", "alpha", "--owner", "alice", "--addr", addr}, e1))
	e2, _, _ := testEnv(addr)
	require.Equal(t, 0, run(context.Background(), []string{"create", "-s", "beta", "--owner", "bob", "--addr", addr}, e2))

	// List filtered to owner=alice: exactly one data row.
	e, out, _ := testEnv(addr)
	code := run(context.Background(), []string{"list", "--owner", "alice", "--addr", addr}, e)
	require.Equal(t, 0, code)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	// Mutation: dropping the owner filter (passing "" instead of flags["owner"])
	// would yield 2 data rows + header = 3 lines, failing this assertion.
	require.Len(t, lines, 2, "header + exactly one alice row, got:\n%s", out.String())
	assert.True(t, strings.HasPrefix(lines[0], "ID"), "first line is the header")
	assert.Contains(t, lines[1], "alpha")
	assert.Contains(t, lines[1], "alice")
	assert.NotContains(t, out.String(), "beta")
}

func TestRunListEmpty(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	e, out, _ := testEnv(addr)
	code := run(context.Background(), []string{"list", "--addr", addr}, e)
	require.Equal(t, 0, code)
	// Mutation: removing the len==0 branch prints a header instead of this line.
	assert.Equal(t, "No sessions found.\n", out.String())
}

func TestRunKillThenGone(t *testing.T) {
	addr, srv, mock := startMockServer(t)
	defer srv.Stop()

	ec, _, _ := testEnv(addr)
	require.Equal(t, 0, run(context.Background(), []string{"create", "-s", "doomed", "--addr", addr}, ec))
	require.Len(t, mock.sessions, 1)

	e, out, _ := testEnv(addr)
	code := run(context.Background(), []string{"kill", "-s", "sess-1", "--addr", addr}, e)
	require.Equal(t, 0, code)
	assert.Equal(t, "Killed session sess-1\n", out.String())
	// Sink-side proof: server no longer holds the session.
	// Mutation: making KillSession a no-op leaves the map non-empty.
	assert.Len(t, mock.sessions, 0)
}

func TestRunKillNotFoundExitCode(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	e, out, errBuf := testEnv(addr)
	code := run(context.Background(), []string{"kill", "-s", "ghost", "--addr", addr}, e)

	// Mutation: swallowing the gRPC error (return nil) would make this exit 0.
	require.Equal(t, 1, code)
	assert.Empty(t, out.String())
	assert.Contains(t, errBuf.String(), "ghost")
}

func TestRunAttachReportsSession(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	ec, _, _ := testEnv(addr)
	require.Equal(t, 0, run(context.Background(), []string{"create", "-s", "live", "--node", "n1", "--addr", addr}, ec))

	e, out, _ := testEnv(addr)
	code := run(context.Background(), []string{"attach", "-s", "sess-1", "--addr", addr}, e)
	require.Equal(t, 0, code)
	// Mutation: changing the attach banner or fetching the wrong session id
	// breaks this exact prefix on a real GetSession round-trip.
	assert.Equal(t, "Attaching to session live (sess-1) on node n1...\n(attach simulation: no streaming RPC available in current API)\n", out.String())
}

func TestRunRenameSurfacesUnsupported(t *testing.T) {
	addr, srv, _ := startMockServer(t)
	defer srv.Stop()

	e, out, errBuf := testEnv(addr)
	code := run(context.Background(), []string{"rename", "-s", "sess-1", "-c", "newname", "--addr", addr}, e)
	// Mutation: if RenameSession returned nil error, this would be exit 0.
	require.Equal(t, 1, code)
	assert.Empty(t, out.String())
	assert.Contains(t, errBuf.String(), "not supported")
}

func TestRunNoArgsUsage(t *testing.T) {
	e, out, errBuf := testEnv("")
	code := run(context.Background(), nil, e)
	// Mutation: returning 0/1 instead of the dedicated usage code 2 fails here.
	require.Equal(t, 2, code)
	assert.Empty(t, out.String())
	assert.Contains(t, errBuf.String(), "usage: htmux")
}

func TestRunUnknownCommand(t *testing.T) {
	e, out, errBuf := testEnv("")
	code := run(context.Background(), []string{"frobnicate"}, e)
	require.Equal(t, 2, code)
	assert.Empty(t, out.String())
	// Mutation: routing unknown commands to a handler instead of erroring
	// would not produce this message / exit code 2.
	assert.Contains(t, errBuf.String(), "unknown command: frobnicate")
}

func TestRunDialFailureExitCode(t *testing.T) {
	e, out, errBuf := testEnv("")
	// Inject a dial that always fails: proves errors propagate as exit 1,
	// not a buried log.Fatal. Mutation: panicking or os.Exit in handler dial
	// path instead of returning the error would not yield a clean code 1.
	e.dial = func(addr string) (sessionClient, error) {
		return nil, fmt.Errorf("connect to session service at %s: refused", addr)
	}
	code := run(context.Background(), []string{"list", "--addr", "127.0.0.1:1"}, e)
	require.Equal(t, 1, code)
	assert.Empty(t, out.String())
	assert.Contains(t, errBuf.String(), "refused")
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
			name: "flag without value before another flag",
			args: []string{"--verbose", "--node", "bar"},
			expected: map[string]string{
				"verbose": "",
				"node":    "bar",
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

func TestSessionFlagPrefersLong(t *testing.T) {
	// Mutation: if sessionFlag returned flags["s"] first, this would be "short".
	assert.Equal(t, "long", sessionFlag(map[string]string{"session": "long", "s": "short"}))
	assert.Equal(t, "short", sessionFlag(map[string]string{"s": "short"}))
	assert.Equal(t, "", sessionFlag(map[string]string{}))
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

	// Set a known size on the PTY so GetSize returns the configured values.
	require.NoError(t, pty.Setsize(ptyFile, &pty.Winsize{Cols: 100, Rows: 40}))

	term := NewTerminal(tty, tty)
	cols, rows, err := term.Size()
	require.NoError(t, err)
	// Mutation: hardcoding Size to return 80,24 for terminals fails this,
	// since the PTY was explicitly sized to 100x40.
	assert.Equal(t, 100, cols)
	assert.Equal(t, 40, rows)
}
