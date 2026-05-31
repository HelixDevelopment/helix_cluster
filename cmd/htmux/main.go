// Package main provides the htmux CLI — Helix Terminal Multiplexer.
package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"
)

const defaultAddr = "localhost:50053"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "htmux: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: htmux <create|attach|list|kill|rename> [options]")
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	flags := parseFlags(args)
	addr := flags["addr"]
	if addr == "" {
		addr = defaultAddr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch cmd {
	case "create":
		return handleCreate(ctx, flags, addr)
	case "attach":
		return handleAttach(ctx, flags, addr)
	case "list":
		return handleList(ctx, flags, addr)
	case "kill":
		return handleKill(ctx, flags, addr)
	case "rename":
		return handleRename(ctx, flags, addr)
	default:
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

func handleCreate(ctx context.Context, flags map[string]string, addr string) error {
	name := flags["session"]
	if name == "" {
		name = flags["s"]
	}
	if name == "" {
		return fmt.Errorf("create requires --session or -s flag")
	}

	node := flags["node"]
	backend := flags["backend"]
	if backend == "" {
		backend = "tmux"
	}

	owner := flags["owner"]
	if owner == "" {
		u, err := user.Current()
		if err == nil {
			owner = u.Username
		}
	}

	client, err := NewClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	sess, err := client.CreateSession(ctx, name, owner, backend, node)
	if err != nil {
		return err
	}

	fmt.Printf("Created session %s (%s) on node %s\n", sess.Name, sess.Id, sess.NodeId)
	return nil
}

func handleAttach(ctx context.Context, flags map[string]string, addr string) error {
	sessionID := flags["session"]
	if sessionID == "" {
		sessionID = flags["s"]
	}
	if sessionID == "" {
		return fmt.Errorf("attach requires --session or -s flag")
	}

	client, err := NewClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	sess, err := client.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}

	fmt.Printf("Attaching to session %s (%s) on node %s...\n", sess.Name, sess.Id, sess.NodeId)

	// The current SessionService API does not expose a streaming attach RPC.
	// We simulate attachment by entering raw mode and displaying a message.
	term := NewTerminal(os.Stdin, os.Stdout)
	if term.IsTerminal() {
		if err := term.SetRawMode(); err != nil {
			return fmt.Errorf("failed to set raw mode: %w", err)
		}
		defer term.Restore()
	}

	fmt.Println("(attach simulation: no streaming RPC available in current API)")
	return nil
}

func handleList(ctx context.Context, flags map[string]string, addr string) error {
	owner := flags["owner"]
	status := flags["status"]

	client, err := NewClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	sessions, err := client.ListSessions(ctx, owner, status)
	if err != nil {
		return err
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	fmt.Printf("%-24s %-16s %-12s %-10s %s\n", "ID", "NAME", "OWNER", "STATUS", "NODE")
	for _, s := range sessions {
		fmt.Printf("%-24s %-16s %-12s %-10s %s\n", s.Id, s.Name, s.Owner, s.Status, s.NodeId)
	}
	return nil
}

func handleKill(ctx context.Context, flags map[string]string, addr string) error {
	sessionID := flags["session"]
	if sessionID == "" {
		sessionID = flags["s"]
	}
	if sessionID == "" {
		return fmt.Errorf("kill requires --session or -s flag")
	}

	client, err := NewClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.KillSession(ctx, sessionID); err != nil {
		return err
	}

	fmt.Printf("Killed session %s\n", sessionID)
	return nil
}

func handleRename(ctx context.Context, flags map[string]string, addr string) error {
	sessionID := flags["session"]
	if sessionID == "" {
		sessionID = flags["s"]
	}
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

	client, err := NewClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	_, err = client.RenameSession(ctx, sessionID, newName)
	return err
}

// parseFlags parses a simple subset of flags from os.Args-style slice.
// Supports --key=value, --key value, -k=value, -k value.
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
