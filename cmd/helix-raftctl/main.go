// Command helix-raftctl is a real CLI client for the helix-raftd daemon's admin
// HTTP API (see cmd/helix-raftd/admin.go). It talks to one or more nodes over
// real HTTP using only the standard library and exposes the daemon's control
// surface as subcommands:
//
//	status  --admin <addr>[,<addr>...]   GET /status, pretty-print leadership + FSM
//	get     --admin <addr> --key K       GET /get, print the value or "not found"
//	put     --admin <addr>[,<addr>...] --key K --value V
//	                                     PUT /put; if the targeted node is a
//	                                     FOLLOWER (HTTP 421) the write is
//	                                     AUTOMATICALLY redirected to the leader, so
//	                                     an operator can target ANY node and the
//	                                     write lands on the leader.
//	leader  --admin <addr>[,<addr>...]   resolve + print the current leader
//	metrics --admin <addr>               GET /metrics, print raw exposition text
//
// The monolithic main() is split into a testable seam, mirroring cmd/hxc-registry:
//   - run(args, stdout, stderr) int does ALL the work — parses the subcommand and
//     flags, performs the HTTP calls, writes results to the supplied writers, and
//     returns a process exit code instead of calling os.Exit. Tests drive run()
//     against a LIVE helix-raftd cluster and assert exact stdout/stderr/exit-code.
//
// main() stays thin: it forwards os.Args/os.Stdout/os.Stderr to run and exits with
// the returned code.
//
// The CLI imports NOTHING from cmd/helix-raftd: it is a separate process that
// speaks only the HTTP admin API, using stdlib net/http + encoding/json.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// exit codes returned by run; kept small and stable for scripts and tests.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. args is the argument list WITHOUT the program
// name. It never calls os.Exit; it returns the intended process exit code. All
// human-readable output goes to stdout, all diagnostics to stderr.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stdout)
		return exitUsage
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "status":
		return cmdStatus(rest, stdout, stderr)
	case "get":
		return cmdGet(rest, stdout, stderr)
	case "put":
		return cmdPut(rest, stdout, stderr)
	case "leader":
		return cmdLeader(rest, stdout, stderr)
	case "metrics":
		return cmdMetrics(rest, stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "Unknown command: %s\n", cmd)
		usage(stdout)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `helix-raftctl — CLI client for the helix-raftd admin HTTP API

Usage: helix-raftctl <command> [options]

Commands:
  status  --admin <addr>[,<addr>...]              Show leadership + FSM state of node(s)
  get     --admin <addr> --key K                  Read a key from a node's FSM
  put     --admin <addr>[,<addr>...] --key K --value V
                                                  Write a key; auto-redirects a
                                                  follower (HTTP 421) to the leader
  leader  --admin <addr>[,<addr>...]              Resolve and print the current leader
  metrics --admin <addr>                          Fetch raw Prometheus /metrics text

Options:
  --admin   one admin HTTP address (host:port) or a comma-separated list
  --key     key for get/put
  --value   value for put

Examples:
  helix-raftctl status  --admin 127.0.0.1:8001,127.0.0.1:8002,127.0.0.1:8003
  helix-raftctl get     --admin 127.0.0.1:8002 --key foo
  helix-raftctl put     --admin 127.0.0.1:8002 --key foo --value bar
  helix-raftctl leader  --admin 127.0.0.1:8001,127.0.0.1:8002,127.0.0.1:8003`)
}

// parseFlags parses fs against args with a continue-on-error policy so a bad flag
// yields exitUsage instead of killing the process; FlagSet diagnostics go to
// stderr.
func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(stderr)
	return fs.Parse(args)
}

// splitAdmins splits a comma list of admin addresses into a trimmed, non-empty
// slice (order preserved).
func splitAdmins(spec string) []string {
	var out []string
	for _, a := range strings.Split(spec, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

// queryEscape percent-encodes a key/value for safe inclusion in the admin URL
// query string.
func queryEscape(s string) string { return url.QueryEscape(s) }

// cmdStatus implements `status --admin <addr>[,...]`. For each address it prints a
// compact block: id, state, is-leader, the known leader id/addr, last index and
// FSM key count. If any node fails to answer, it prints the error to stderr and
// returns exitError, but still reports the nodes that did answer (status-of-all).
func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin HTTP address(es), comma-separated")
	if err := parseFlags(fs, args, stderr); err != nil {
		return exitUsage
	}
	admins := splitAdmins(*admin)
	if len(admins) == 0 {
		fmt.Fprintln(stderr, "Error: --admin is required")
		return exitUsage
	}

	c := newClient()
	rc := exitOK
	for _, addr := range admins {
		s, err := c.status(addr)
		if err != nil {
			fmt.Fprintf(stderr, "Error: status %s: %v\n", addr, err)
			rc = exitError
			continue
		}
		printStatus(stdout, addr, s)
	}
	return rc
}

// printStatus renders one node's status block.
func printStatus(w io.Writer, addr string, s statusResponse) {
	leaderLabel := s.Leader
	if leaderLabel == "" {
		leaderLabel = "(unknown)"
	}
	leaderAddr := s.LeaderAddr
	if leaderAddr == "" {
		leaderAddr = "(unknown)"
	}
	fmt.Fprintf(w, "node %s\n", addr)
	fmt.Fprintf(w, "  id:         %s\n", s.ID)
	fmt.Fprintf(w, "  state:      %s\n", s.State)
	fmt.Fprintf(w, "  is-leader:  %t\n", s.IsLeader)
	fmt.Fprintf(w, "  leader:     %s (addr %s)\n", leaderLabel, leaderAddr)
	fmt.Fprintf(w, "  last-index: %d\n", s.LastIndex)
	fmt.Fprintf(w, "  fsm-keys:   %d\n", s.NumKeys)
	fmt.Fprintf(w, "  applied:    %d\n", s.Applied)
}

// cmdGet implements `get --admin <addr> --key K`. It prints the value when found,
// or a clear "not found" message to stderr and returns exitError when the key is
// absent — so scripts can branch on the exit code.
func cmdGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin HTTP address")
	key := fs.String("key", "", "key to read")
	if err := parseFlags(fs, args, stderr); err != nil {
		return exitUsage
	}
	admins := splitAdmins(*admin)
	if len(admins) == 0 {
		fmt.Fprintln(stderr, "Error: --admin is required")
		return exitUsage
	}
	if *key == "" {
		fmt.Fprintln(stderr, "Error: --key is required")
		return exitUsage
	}

	c := newClient()
	g, err := c.get(admins[0], *key)
	if err != nil {
		fmt.Fprintf(stderr, "Error: get %s: %v\n", admins[0], err)
		return exitError
	}
	if !g.Found {
		fmt.Fprintf(stderr, "not found: %s\n", *key)
		return exitError
	}
	fmt.Fprintln(stdout, g.Value)
	return exitOK
}

// cmdPut implements `put --admin <addr>[,...] --key K --value V`. It PUTs to the
// FIRST admin address. If that node is a FOLLOWER (HTTP 421) it AUTOMATICALLY
// resolves the leader's admin endpoint among the supplied --admin addresses (the
// one whose /status reports IsLeader) and retries the write there. It prints which
// node ultimately accepted the write. This is the operator UX win: target any
// node, the write lands on the leader.
func cmdPut(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin HTTP address(es), comma-separated")
	key := fs.String("key", "", "key to write")
	value := fs.String("value", "", "value to write")
	if err := parseFlags(fs, args, stderr); err != nil {
		return exitUsage
	}
	admins := splitAdmins(*admin)
	if len(admins) == 0 {
		fmt.Fprintln(stderr, "Error: --admin is required")
		return exitUsage
	}
	if *key == "" {
		fmt.Fprintln(stderr, "Error: --key is required")
		return exitUsage
	}
	// An empty value is permitted by the daemon (it falls back to the body), but
	// for a CLI write we require an explicit --value to avoid a silent empty set.
	if *value == "" {
		fmt.Fprintln(stderr, "Error: --value is required")
		return exitUsage
	}

	c := newClient()
	target := admins[0]

	// Resolve the leader's admin endpoint up front from the supplied addresses so
	// a 421 from the target can be redirected to a concrete admin URL. The daemon's
	// 421 carries the leader's raft TCP addr, NOT its admin addr, so we map it by
	// asking each supplied node who the leader is.
	leaderAdmin := resolveLeaderAdmin(c, admins)

	res, err := c.put(target, *key, *value, leaderAdmin)
	if err != nil {
		fmt.Fprintf(stderr, "Error: put: %v\n", err)
		return exitError
	}
	if res.Redirected {
		fmt.Fprintf(stdout, "OK: %s=%s written via leader %s (target %s was a follower, auto-redirected)\n",
			*key, *value, res.AcceptedBy, normalizeAddr(target))
	} else {
		fmt.Fprintf(stdout, "OK: %s=%s written (accepted by %s)\n", *key, *value, res.AcceptedBy)
	}
	return exitOK
}

// resolveLeaderAdmin returns the admin address (from admins) of the node that
// reports IsLeader, or "" if none can be identified (all unreachable, or no leader
// yet). Errors from individual nodes are ignored here — the goal is best-effort
// leader discovery for the redirect; the actual write path surfaces failures.
func resolveLeaderAdmin(c *client, admins []string) string {
	for _, a := range admins {
		s, err := c.status(a)
		if err != nil {
			continue
		}
		if s.IsLeader {
			return a
		}
	}
	return ""
}

// cmdLeader implements `leader --admin <addr>[,...]`. It resolves the current
// leader: it queries each supplied node and reports the first that IS the leader,
// or, failing that, the leader id/addr a follower names. It returns exitError if
// no leader can be determined (e.g. an in-progress election or all nodes down).
func cmdLeader(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("leader", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin HTTP address(es), comma-separated")
	if err := parseFlags(fs, args, stderr); err != nil {
		return exitUsage
	}
	admins := splitAdmins(*admin)
	if len(admins) == 0 {
		fmt.Fprintln(stderr, "Error: --admin is required")
		return exitUsage
	}

	c := newClient()
	var namedLeader string
	var namedLeaderAddr string
	var anyAnswered bool
	for _, a := range admins {
		s, err := c.status(a)
		if err != nil {
			continue
		}
		anyAnswered = true
		if s.IsLeader {
			fmt.Fprintf(stdout, "leader: %s (admin %s, raft addr %s)\n", s.ID, normalizeAddr(a), s.LeaderAddr)
			return exitOK
		}
		if s.Leader != "" && namedLeader == "" {
			namedLeader = s.Leader
			namedLeaderAddr = s.LeaderAddr
		}
	}
	if !anyAnswered {
		fmt.Fprintln(stderr, "Error: no node answered /status")
		return exitError
	}
	if namedLeader != "" {
		// A follower named the leader, but the leader's admin endpoint was not among
		// the supplied addresses (or it didn't answer). Report the raft-level identity.
		fmt.Fprintf(stdout, "leader: %s (raft addr %s) — not directly reachable via supplied --admin addrs\n",
			namedLeader, namedLeaderAddr)
		return exitOK
	}
	fmt.Fprintln(stderr, "Error: no leader currently known (election in progress?)")
	return exitError
}

// cmdMetrics implements `metrics --admin <addr>`. It prints the raw Prometheus
// exposition text from the node's /metrics endpoint.
func cmdMetrics(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin HTTP address")
	if err := parseFlags(fs, args, stderr); err != nil {
		return exitUsage
	}
	admins := splitAdmins(*admin)
	if len(admins) == 0 {
		fmt.Fprintln(stderr, "Error: --admin is required")
		return exitUsage
	}

	c := newClient()
	text, err := c.metrics(admins[0])
	if err != nil {
		fmt.Fprintf(stderr, "Error: metrics %s: %v\n", admins[0], err)
		return exitError
	}
	fmt.Fprint(stdout, text)
	return exitOK
}
