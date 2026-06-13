package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
)

// applyTimeout bounds a single leader Apply (replicate to majority + commit).
const applyTimeout = 5 * time.Second

// adminServer is the small HTTP control surface for one raft node. It exposes
// STATUS / GET / PUT over a port SEPARATE from the raft TCP port, so a client (or
// the E2E test) can drive and observe the node out-of-band from raft's own RPCs.
type adminServer struct {
	node   *raft.Node
	bind   string
	logger *log.Logger
	ln     net.Listener
	srv    *http.Server
}

// newAdminServer builds (but does not start) the admin server for node on bind.
func newAdminServer(node *raft.Node, bind string, logger *log.Logger) *adminServer {
	a := &adminServer{node: node, bind: bind, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", a.handleStatus)
	mux.HandleFunc("/get", a.handleGet)
	mux.HandleFunc("/put", a.handlePut)
	a.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return a
}

// start binds the admin listener and serves in a background goroutine. Binding is
// synchronous (so a bind failure surfaces before run() announces readiness); the
// blocking Serve runs in its own goroutine.
func (a *adminServer) start() error {
	ln, err := net.Listen("tcp", a.bind)
	if err != nil {
		return fmt.Errorf("admin listen %q: %w", a.bind, err)
	}
	a.ln = ln
	go func() {
		if err := a.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.logger.Printf("admin serve error: %v", err)
		}
	}()
	return nil
}

// addr returns the admin server's actual bound address (concrete port even if
// bind used :0).
func (a *adminServer) addr() string {
	if a.ln != nil {
		return a.ln.Addr().String()
	}
	return a.bind
}

// stop gracefully shuts the admin HTTP server down within ctx.
func (a *adminServer) stop(ctx context.Context) error {
	return a.srv.Shutdown(ctx)
}

// statusResponse is the JSON body of GET /status.
type statusResponse struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	IsLeader   bool   `json:"isLeader"`
	Leader     string `json:"leader"`     // leader server id (may be "")
	LeaderAddr string `json:"leaderAddr"` // leader raft TCP addr (may be "")
	LastIndex  uint64 `json:"lastIndex"`
	NumKeys    int    `json:"numKeys"`
	Applied    uint64 `json:"applied"`
}

// handleStatus reports this node's live raft state: leadership, the known leader
// id+addr, the persisted lastIndex (a catch-up signal after restart), and FSM
// sink-side counters. A supervising process polls this to detect "exactly one
// leader" and "follower caught up".
func (a *adminServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	resp := statusResponse{
		ID:         a.node.ID(),
		State:      a.node.State().String(),
		IsLeader:   a.node.IsLeader(),
		Leader:     a.node.Leader(),
		LeaderAddr: a.node.LeaderAddr(),
		LastIndex:  a.node.LastIndex(),
		NumKeys:    a.node.FSM().Len(),
		Applied:    a.node.FSM().AppliedCount(),
	}
	writeJSON(w, http.StatusOK, resp)
}

// getResponse is the JSON body of GET /get.
type getResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Found bool   `json:"found"`
}

// handleGet reads a key from THIS node's replicated FSM (no raft round-trip). A
// GET served by a FOLLOWER proves the value reached that follower's own state via
// replication — genuine sink-side evidence, not a leader-only read.
func (a *adminServer) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	val, found := a.node.FSM().Get(key)
	writeJSON(w, http.StatusOK, getResponse{Key: key, Value: val, Found: found})
}

// putResponse is the JSON body of a successful PUT.
type putResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	OK    bool   `json:"ok"`
}

// notLeaderResponse is the JSON body of a PUT rejected because this node is not
// the leader; it carries the leader's id+addr so the client redirects.
type notLeaderResponse struct {
	Error      string `json:"error"`
	Leader     string `json:"leader"`
	LeaderAddr string `json:"leaderAddr"`
}

// handlePut applies a key/value SET through raft. Only the leader accepts it:
// raft replicates the entry to a majority and applies it to every FSM before the
// call returns. On a follower it returns 421 Misdirected Request with the leader
// address so the caller can redirect — the leader-gated write path, surfaced over
// HTTP. The value may be passed as ?value=V or in the request body.
func (a *adminServer) handlePut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "use PUT or POST", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	value := r.URL.Query().Get("value")
	if value == "" {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		value = string(body)
	}

	if !a.node.IsLeader() {
		writeJSON(w, http.StatusMisdirectedRequest, notLeaderResponse{
			Error:      "not leader",
			Leader:     a.node.Leader(),
			LeaderAddr: a.node.LeaderAddr(),
		})
		return
	}

	if err := a.node.Apply(raft.Command{Op: raft.CommandSet, Key: key, Value: value}, applyTimeout); err != nil {
		// A leadership change can race between the IsLeader check and Apply; surface
		// the not-leader case distinctly so the client redirects rather than retrying.
		if err == raft.ErrNotLeader {
			writeJSON(w, http.StatusMisdirectedRequest, notLeaderResponse{
				Error:      "not leader",
				Leader:     a.node.Leader(),
				LeaderAddr: a.node.LeaderAddr(),
			})
			return
		}
		http.Error(w, "apply: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, putResponse{Key: key, Value: value, OK: true})
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
