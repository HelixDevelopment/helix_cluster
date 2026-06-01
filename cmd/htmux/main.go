// Package main provides the htmux CLI — Helix Terminal Multiplexer.
//
// htmux is a thin gRPC client for the cluster SessionService. It is NOT a
// long-lived network server: each invocation dials the session service,
// performs one operation, prints a concrete result to stdout, and exits.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"strings"
	"time"
)

const defaultAddr = "localhost:50053"

// usage is the top-level usage string, emitted on argument errors.
const usage = "usage: htmux <create|attach|list|kill|rename> [options]"

// commandTimeout bounds every remote operation a single invocation performs.
const commandTimeout = 30 * time.Second

// dialFunc constructs a session client for the given address. It is a seam so
// tests can inject an in-process client without real network egress.
type dialFunc func(addr string) (sessionClient, error)

// sessionClient is the subset of the gRPC client the handlers depend on.
// Defining it here lets tests substitute a fake or an in-process real client.
type sessionClient interface {
	CreateSession(ctx context.Context, name, owner, backend, nodeID string) (*sessionView, error)
	GetSession(ctx context.Context, sessionID string) (*sessionView, error)
	ListSessions(ctx context.Context, owner, status string) ([]*sessionView, error)
	KillSession(ctx context.Context, sessionID string) error
	RenameSession(ctx context.Context, sessionID, newName string) (*sessionView, error)
	Close() error
}

// sessionView is a transport-agnostic projection of a session, so handler
// output formatting is decoupled from the generated proto type.
type sessionView struct {
	ID      string
	Name    string
	Owner   string
	Status  string
	Backend string
	NodeID  string
}

// env carries the injectable dependencies for a single CLI invocation.
type env struct {
	stdout io.Writer
	stderr io.Writer
	dial   dialFunc
	// lookupOwner resolves the default session owner when none is supplied.
	lookupOwner func() (string, error)
	log         *slog.Logger
	// stream holds the injectable WebSocket streaming dependencies used by
	// handleAttach. If nil, defaultStreamEnv() is used at call time.
	stream *streamEnv
}

func main() {
	se := defaultStreamEnv()
	e := &env{
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		dial:        dialGRPC,
		lookupOwner: currentUsername,
		log:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		stream:      &se,
	}
	os.Exit(run(context.Background(), os.Args[1:], e))
}

// run is the testable entry point. It parses args, dispatches to a handler,
// and returns a process exit code (0 success, 1 failure, 2 usage error).
func run(ctx context.Context, args []string, e *env) int {
	if e.log == nil {
		e.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if len(args) < 1 {
		fmt.Fprintln(e.stderr, usage)
		return 2
	}

	cmd := args[0]
	flags := parseFlags(args[1:])
	addr := flags["addr"]
	if addr == "" {
		addr = defaultAddr
	}

	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	var handler func(context.Context, *env, map[string]string, string) error
	switch cmd {
	case "create":
		handler = handleCreate
	case "attach":
		handler = handleAttach
	case "list":
		handler = handleList
	case "kill":
		handler = handleKill
	case "rename":
		handler = handleRename
	default:
		fmt.Fprintf(e.stderr, "htmux: unknown command: %s\n%s\n", cmd, usage)
		return 2
	}

	if err := handler(ctx, e, flags, addr); err != nil {
		e.log.Error("command failed", "command", cmd, "error", err)
		fmt.Fprintf(e.stderr, "htmux: %v\n", err)
		return 1
	}
	return 0
}

// sessionFlag returns the session identifier from either --session or -s.
func sessionFlag(flags map[string]string) string {
	if v := flags["session"]; v != "" {
		return v
	}
	return flags["s"]
}

func handleCreate(ctx context.Context, e *env, flags map[string]string, addr string) error {
	name := sessionFlag(flags)
	if name == "" {
		return fmt.Errorf("create requires --session or -s flag")
	}

	node := flags["node"]
	backend := flags["backend"]
	if backend == "" {
		backend = "tmux"
	}

	owner := flags["owner"]
	if owner == "" && e.lookupOwner != nil {
		if u, err := e.lookupOwner(); err == nil {
			owner = u
		}
	}

	client, err := e.dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	sess, err := client.CreateSession(ctx, name, owner, backend, node)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.stdout, "Created session %s (%s) on node %s\n", sess.Name, sess.ID, sess.NodeID)
	return nil
}

func handleAttach(ctx context.Context, e *env, flags map[string]string, addr string) error {
	sessionID := sessionFlag(flags)
	if sessionID == "" {
		return fmt.Errorf("attach requires --session or -s flag")
	}

	client, err := e.dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	sess, err := client.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.stdout, "Attaching to session %s (%s) on node %s...\n", sess.Name, sess.ID, sess.NodeID)

	// Resolve the WebSocket address: --wsaddr flag overrides, otherwise use
	// the same host:port as the gRPC endpoint (helix-session serves WS and
	// gRPC on the same port via separate handlers when configured that way,
	// or the operator may supply a distinct --wsaddr).
	wsAddr := flags["wsaddr"]
	if wsAddr == "" {
		wsAddr = addr
	}

	// Resolve stream dependencies: use the injected seam (for tests) or the
	// real default wired in main().
	se := defaultStreamEnv()
	if e.stream != nil {
		se = *e.stream
	}

	// streamSession blocks until the WS connection closes or an error occurs.
	// ctx cancellation (e.g. commandTimeout) propagates via the WS read/write
	// deadlines inside streamSession.
	return streamSession(sess.ID, wsAddr, se)
}

func handleList(ctx context.Context, e *env, flags map[string]string, addr string) error {
	owner := flags["owner"]
	status := flags["status"]

	client, err := e.dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	sessions, err := client.ListSessions(ctx, owner, status)
	if err != nil {
		return err
	}

	if len(sessions) == 0 {
		fmt.Fprintln(e.stdout, "No sessions found.")
		return nil
	}

	fmt.Fprintf(e.stdout, "%-24s %-16s %-12s %-10s %s\n", "ID", "NAME", "OWNER", "STATUS", "NODE")
	for _, s := range sessions {
		fmt.Fprintf(e.stdout, "%-24s %-16s %-12s %-10s %s\n", s.ID, s.Name, s.Owner, s.Status, s.NodeID)
	}
	return nil
}

func handleKill(ctx context.Context, e *env, flags map[string]string, addr string) error {
	sessionID := sessionFlag(flags)
	if sessionID == "" {
		return fmt.Errorf("kill requires --session or -s flag")
	}

	client, err := e.dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.KillSession(ctx, sessionID); err != nil {
		return err
	}

	fmt.Fprintf(e.stdout, "Killed session %s\n", sessionID)
	return nil
}

func handleRename(ctx context.Context, e *env, flags map[string]string, addr string) error {
	sessionID := sessionFlag(flags)
	if sessionID == "" {
		return fmt.Errorf("rename requires --session or -s flag")
	}

	newName := flags["command"]
	if newName == "" {
		newName = flags["c"]
	}
	if newName == "" {
		return fmt.Errorf("rename requires --command or -c flag for the new name")
	}

	client, err := e.dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	_, err = client.RenameSession(ctx, sessionID, newName)
	return err
}

// currentUsername resolves the current OS user's name as the default owner.
func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// parseFlags parses a simple subset of flags from an os.Args-style slice.
// Supports --key=value, --key value, -k=value, -k value. A flag with no
// following value (or followed by another flag) maps to the empty string.
func parseFlags(args []string) map[string]string {
	flags := make(map[string]string)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		key := strings.TrimLeft(arg, "-")
		if idx := strings.Index(key, "="); idx != -1 {
			flags[key[:idx]] = key[idx+1:]
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			flags[key] = args[i+1]
			i++
		} else {
			flags[key] = ""
		}
	}
	return flags
}
