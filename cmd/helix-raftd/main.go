// Command helix-raftd is a single-process daemon hosting ONE persistent +
// networked Raft node (real loopback TCP transport + real on-disk BoltDB), the
// capstone composition of the pkg/raft building blocks. Run three of these as
// SEPARATE OS processes — each with its own --data-dir and --bind, all sharing
// the same --peers membership — and they elect a leader, replicate over real TCP,
// and survive a leader-process kill + restart-from-disk. There is no in-process
// shim: every node is a real OS process, every RPC is a real kernel socket, every
// committed entry is on a real bbolt file on disk.
//
// Flags:
//
//	--id        this node's raft server id (must appear in --peers)
//	--data-dir  durable directory for this node's BoltDB (raft.db) + snapshots
//	--bind      this node's raft TCP listen address (host:port), e.g. 127.0.0.1:7001
//	--peers     full initial cluster membership INCLUDING self, comma-separated
//	            id=addr pairs, e.g. n1=127.0.0.1:7001,n2=127.0.0.1:7002,n3=...
//	--admin     this node's admin HTTP listen address (host:port), separate port
//
// Admin HTTP API (see admin.go):
//
//	GET  /status         -> JSON {id, isLeader, leader, leaderAddr, lastIndex, ...}
//	GET  /get?key=K      -> JSON {key, value, found} read from THIS node's FSM
//	PUT  /put?key=K&value=V (or body val) -> Apply through raft; leader-only,
//	                        else 421 with the leader's address so a client redirects
//
// On SIGINT/SIGTERM it shuts down gracefully: stop the admin server, then
// Node.Shutdown (stop raft, close the TCP transport, close BoltDB — in that
// order) so the data dir flushes and can be reopened by a later process.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	hraft "github.com/hashicorp/raft"

	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
)

// peer is one id=addr entry from the --peers membership list.
type peer struct {
	id   string
	addr string
}

// parsePeers parses a comma-separated "id=addr,id=addr,..." membership string
// into an ordered, de-duplicated peer slice and the equivalent []hraft.Server
// bootstrap configuration. It validates that ids and addrs are non-empty and
// unique; the membership is used ONLY on a node's FIRST boot (fresh data dir) —
// on reopen the durable on-disk configuration wins.
func parsePeers(spec string) ([]peer, []hraft.Server, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil, fmt.Errorf("empty --peers")
	}
	var peers []peer
	servers := make([]hraft.Server, 0)
	seenID := make(map[string]struct{})
	seenAddr := make(map[string]struct{})
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 || eq == len(raw)-1 {
			return nil, nil, fmt.Errorf("bad peer %q: want id=addr", raw)
		}
		id := strings.TrimSpace(raw[:eq])
		addr := strings.TrimSpace(raw[eq+1:])
		if id == "" || addr == "" {
			return nil, nil, fmt.Errorf("bad peer %q: empty id or addr", raw)
		}
		if _, dup := seenID[id]; dup {
			return nil, nil, fmt.Errorf("duplicate peer id %q", id)
		}
		if _, dup := seenAddr[addr]; dup {
			return nil, nil, fmt.Errorf("duplicate peer addr %q", addr)
		}
		seenID[id] = struct{}{}
		seenAddr[addr] = struct{}{}
		peers = append(peers, peer{id: id, addr: addr})
		servers = append(servers, hraft.Server{
			Suffrage: hraft.Voter,
			ID:       hraft.ServerID(id),
			Address:  hraft.ServerAddress(addr),
		})
	}
	if len(peers) == 0 {
		return nil, nil, fmt.Errorf("no valid peers in %q", spec)
	}
	return peers, servers, nil
}

// config is the validated daemon configuration parsed from flags.
type config struct {
	id      string
	dataDir string
	bind    string
	admin   string
	peers   []peer
	servers []hraft.Server
}

// parseFlags parses and validates the daemon flags from the given argument slice
// (os.Args[1:] in main). It enforces that --bind matches the address this node's
// own --id is given in --peers, so a node's advertised raft address is exactly
// what its peers will dial.
func parseFlags(args []string) (*config, error) {
	fs := flag.NewFlagSet("helix-raftd", flag.ContinueOnError)
	var (
		id      = fs.String("id", "", "this node's raft server id (must appear in --peers)")
		dataDir = fs.String("data-dir", "", "durable directory for this node's BoltDB + snapshots")
		bind    = fs.String("bind", "", "this node's raft TCP listen address host:port")
		admin   = fs.String("admin", "", "this node's admin HTTP listen address host:port")
		peers   = fs.String("peers", "", "full initial membership incl. self: id=addr,id=addr,...")
	)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *id == "" {
		return nil, fmt.Errorf("--id is required")
	}
	if *dataDir == "" {
		return nil, fmt.Errorf("--data-dir is required")
	}
	if *bind == "" {
		return nil, fmt.Errorf("--bind is required")
	}
	if *admin == "" {
		return nil, fmt.Errorf("--admin is required")
	}
	peerList, servers, err := parsePeers(*peers)
	if err != nil {
		return nil, fmt.Errorf("--peers: %w", err)
	}

	// The node's --id must be in the membership, and its --bind must equal the
	// address recorded for that id (peers dial the membership address).
	var selfAddr string
	for _, p := range peerList {
		if p.id == *id {
			selfAddr = p.addr
			break
		}
	}
	if selfAddr == "" {
		ids := make([]string, 0, len(peerList))
		for _, p := range peerList {
			ids = append(ids, p.id)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("--id %q not found in --peers (have: %s)", *id, strings.Join(ids, ","))
	}
	if selfAddr != *bind {
		return nil, fmt.Errorf("--bind %q != address %q recorded for --id %q in --peers", *bind, selfAddr, *id)
	}

	return &config{
		id:      *id,
		dataDir: *dataDir,
		bind:    *bind,
		admin:   *admin,
		peers:   peerList,
		servers: servers,
	}, nil
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "helix-raftd: %v\n", err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "helix-raftd: %v\n", err)
		os.Exit(1)
	}
}

// run starts the persistent+networked raft node, serves the admin API, and
// blocks until SIGINT/SIGTERM, then shuts everything down gracefully. It returns
// the first error encountered during startup or shutdown.
func run(cfg *config) error {
	logger := log.New(os.Stderr, fmt.Sprintf("[helix-raftd %s] ", cfg.id), log.LstdFlags|log.Lmicroseconds)

	// Bring up the REAL persistent+networked node: TCP transport bound to the
	// fixed --bind, BoltDB at --data-dir. On a fresh data dir it bootstraps the
	// full --peers membership; on reopen (restart-from-disk) it recovers the
	// durable log/config and rejoins WITHOUT re-bootstrapping.
	pn, err := raft.NewPersistentNetworkNodeAt(cfg.id, cfg.dataDir, cfg.bind, nil, cfg.servers)
	if err != nil {
		return fmt.Errorf("start raft node: %w", err)
	}
	node := pn.Node
	logger.Printf("raft node up: bind=%s admin=%s data-dir=%s tcp=%s peers=%d",
		cfg.bind, cfg.admin, cfg.dataDir, pn.TCPAddr.String(), len(cfg.peers))

	// Admin HTTP server on its OWN port (separate from the raft TCP port).
	admin := newAdminServer(node, cfg.admin, logger)
	if err := admin.start(); err != nil {
		_ = node.Shutdown()
		return fmt.Errorf("start admin server: %w", err)
	}
	logger.Printf("admin HTTP listening on %s", admin.addr())
	// Announce readiness on stdout in a machine-parseable form so a supervising
	// process (e.g. the E2E test) can learn the bound admin address without
	// racing on the port. Printed AFTER both listeners are up.
	fmt.Printf("HELIX_RAFTD_READY id=%s bind=%s admin=%s tcp=%s\n",
		cfg.id, cfg.bind, admin.addr(), pn.TCPAddr.String())

	// Block until a termination signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Printf("received signal %s, shutting down", sig)

	// Graceful shutdown: stop admin first (no new requests), then the raft node
	// (raft -> TCP transport -> BoltDB, in that order, flushing the data dir).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var firstErr error
	if err := admin.stop(ctx); err != nil {
		logger.Printf("admin shutdown error: %v", err)
		firstErr = err
	}
	if err := node.Shutdown(); err != nil {
		logger.Printf("raft node shutdown error: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}
	logger.Printf("shutdown complete")
	return firstErr
}
