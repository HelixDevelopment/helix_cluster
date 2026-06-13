package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpTimeout bounds a single admin HTTP call. The CLI is interactive/operator-
// facing, so a short timeout turns an unreachable or wedged node into a prompt
// error rather than an indefinite hang.
const httpTimeout = 5 * time.Second

// statusResponse mirrors the daemon's GET /status JSON (see
// cmd/helix-raftd/admin.go). Only the fields the CLI consumes are decoded; extra
// fields the daemon may add are ignored by encoding/json.
type statusResponse struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	IsLeader   bool   `json:"isLeader"`
	Leader     string `json:"leader"`
	LeaderAddr string `json:"leaderAddr"`
	LastIndex  uint64 `json:"lastIndex"`
	NumKeys    int    `json:"numKeys"`
	Applied    uint64 `json:"applied"`
}

// getResponse mirrors the daemon's GET /get JSON.
type getResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Found bool   `json:"found"`
}

// notLeaderResponse mirrors the daemon's 421 body: the leader id+addr a follower
// returns so a client can redirect the write.
type notLeaderResponse struct {
	Error      string `json:"error"`
	Leader     string `json:"leader"`
	LeaderAddr string `json:"leaderAddr"`
}

// client is a thin admin-API HTTP client for one or more helix-raftd nodes. It
// holds a single *http.Client (connection pooling, fixed timeout) reused across
// calls and across nodes.
type client struct {
	hc *http.Client
}

// newClient builds a client with the standard per-call timeout.
func newClient() *client {
	return &client{hc: &http.Client{Timeout: httpTimeout}}
}

// normalizeAddr accepts a bare host:port admin address (the daemon's --admin
// form) or a full http URL and returns a base http URL with no trailing slash.
// The daemon reports leaderAddr as the raft TCP host:port; for redirects the CLI
// is given an admin host:port by the operator, so this only needs to tolerate the
// two operator-supplied spellings.
func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimRight(addr, "/")
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

// getJSON issues a GET to base+path and decodes a 2xx JSON body into out. A
// connection error, a non-2xx status, or a malformed body all become a clear
// error (never a panic). The decoded status code is returned for callers that
// branch on it (e.g. the 421 redirect path).
func (c *client) getJSON(base, path string, out any) (int, error) {
	url := base + path
	resp, err := c.hc.Get(url)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("GET %s: read body: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("GET %s: malformed JSON: %w", url, err)
	}
	return resp.StatusCode, nil
}

// status fetches GET /status from the node at admin addr.
func (c *client) status(addr string) (statusResponse, error) {
	var s statusResponse
	_, err := c.getJSON(normalizeAddr(addr), "/status", &s)
	return s, err
}

// get fetches GET /get?key=K from the node at admin addr.
func (c *client) get(addr, key string) (getResponse, error) {
	var g getResponse
	_, err := c.getJSON(normalizeAddr(addr), "/get?key="+queryEscape(key), &g)
	return g, err
}

// putResult reports the outcome of a put: the node (admin addr) that ultimately
// accepted the write and whether a 421 redirect to the leader was followed.
type putResult struct {
	AcceptedBy string // admin addr that returned 200
	Redirected bool   // a follower 421'd and we retried the leader
	LeaderAddr string // leaderAddr reported by the follower's 421 (raft TCP addr)
}

// put issues PUT /put?key=&value= to the node at admin addr. If that node is a
// FOLLOWER it answers 421 with the leader's address; put then AUTOMATICALLY
// retries the write against leaderAdmin (the operator-supplied leader admin addr
// to redirect to). When leaderAdmin is empty and a 421 is hit, put returns the
// 421 leaderAddr in the error so the caller can resolve+retry. A non-leader retry
// that itself 421s (e.g. a leadership change mid-flight) is surfaced as an error
// rather than looping forever.
//
// redirectAddr is how the CLI maps a 421 to a concrete admin endpoint: the
// operator passes the cluster's admin addresses, and put walks to the one whose
// node reports IsLeader. That resolution is done by the caller (run); put itself
// performs at most ONE redirect to an already-resolved leader admin addr.
func (c *client) put(addr, key, value, leaderAdmin string) (putResult, error) {
	code, nl, err := c.putOnce(addr, key, value)
	if err != nil {
		return putResult{}, err
	}
	if code == http.StatusOK {
		return putResult{AcceptedBy: normalizeAddr(addr)}, nil
	}
	if code == http.StatusMisdirectedRequest {
		if leaderAdmin == "" {
			return putResult{LeaderAddr: nl.LeaderAddr}, fmt.Errorf(
				"node %s is a follower (leader raft addr=%s); no leader admin endpoint to redirect to",
				normalizeAddr(addr), nl.LeaderAddr)
		}
		// One redirect to the resolved leader admin endpoint.
		code2, nl2, err := c.putOnce(leaderAdmin, key, value)
		if err != nil {
			return putResult{}, fmt.Errorf("redirect to leader %s: %w", normalizeAddr(leaderAdmin), err)
		}
		if code2 == http.StatusOK {
			return putResult{AcceptedBy: normalizeAddr(leaderAdmin), Redirected: true, LeaderAddr: nl.LeaderAddr}, nil
		}
		if code2 == http.StatusMisdirectedRequest {
			return putResult{}, fmt.Errorf(
				"redirect target %s is also not leader (leader raft addr=%s) — leadership may have changed",
				normalizeAddr(leaderAdmin), nl2.LeaderAddr)
		}
		return putResult{}, fmt.Errorf("redirect to leader %s: unexpected HTTP %d", normalizeAddr(leaderAdmin), code2)
	}
	return putResult{}, fmt.Errorf("PUT to %s: unexpected HTTP %d", normalizeAddr(addr), code)
}

// putOnce performs a single PUT and returns the HTTP status code and, on 421, the
// decoded not-leader body. It does not redirect.
func (c *client) putOnce(addr, key, value string) (int, notLeaderResponse, error) {
	base := normalizeAddr(addr)
	url := base + "/put?key=" + queryEscape(key) + "&value=" + queryEscape(value)
	req, err := http.NewRequest(http.MethodPut, url, nil)
	if err != nil {
		return 0, notLeaderResponse{}, fmt.Errorf("PUT %s: %w", url, err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, notLeaderResponse{}, fmt.Errorf("PUT %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, notLeaderResponse{}, fmt.Errorf("PUT %s: read body: %w", url, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.StatusCode, notLeaderResponse{}, nil
	case http.StatusMisdirectedRequest:
		var nl notLeaderResponse
		// The daemon returns JSON for 421; tolerate a non-JSON body without panicking.
		_ = json.Unmarshal(body, &nl)
		return resp.StatusCode, nl, nil
	default:
		return resp.StatusCode, notLeaderResponse{}, fmt.Errorf(
			"PUT %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// metrics fetches the raw GET /metrics exposition text from the node at addr.
func (c *client) metrics(addr string) (string, error) {
	base := normalizeAddr(addr)
	url := base + "/metrics"
	resp, err := c.hc.Get(url)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("GET %s: read body: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}
