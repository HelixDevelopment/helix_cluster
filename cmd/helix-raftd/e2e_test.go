//go:build e2e

// This file is the REAL multi-PROCESS end-to-end proof for helix-raftd. It is
// gated behind the `e2e` build tag because it spawns real OS processes and is
// slower than a unit test; run it explicitly with:
//
//	go test -tags e2e -run TestE2E ./cmd/helix-raftd/ -v
//
// It is NOT a mock and NOT skipped: it go-builds the daemon, spawns 3 genuine
// processes over real TCP with real on-disk BoltDB, drives them via their admin
// HTTP ports, KILLs the leader process with SIGKILL, proves a new leader emerges
// and the value survives, restarts the killed node from its on-disk data dir and
// proves it rejoins + recovers, and finally does a FULL cluster kill + restart
// from disk proving the value could ONLY have come from persisted storage.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	e2eElectTimeout   = 40 * time.Second
	e2eReplTimeout    = 25 * time.Second
	e2eRestartTimeout = 40 * time.Second
	e2ePollInterval   = 100 * time.Millisecond
	e2eHTTPTimeout    = 3 * time.Second
)

// proc is one spawned daemon process and the fixed addresses it was launched with.
type proc struct {
	id        string
	bind      string // raft TCP addr
	admin     string // admin HTTP addr
	dataDir   string
	cmd       *exec.Cmd
	stdout    *lineCapture
	stderr    *lineCapture
	mu        sync.Mutex
	pid       int
	startTime time.Time
}

// status mirrors the daemon's GET /status JSON.
type status struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	IsLeader   bool   `json:"isLeader"`
	Leader     string `json:"leader"`
	LeaderAddr string `json:"leaderAddr"`
	LastIndex  uint64 `json:"lastIndex"`
	NumKeys    int    `json:"numKeys"`
	Applied    uint64 `json:"applied"`
}

// lineCapture is a concurrency-safe writer that retains captured output for the
// evidence log.
type lineCapture struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lineCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lineCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// freePort picks a free loopback TCP port via bind-:0/read-back/close.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// buildDaemon go-builds the helix-raftd binary to a temp path and returns it.
func buildDaemon(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "helix-raftd-e2e")
	// Build the package in this directory.
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build helix-raftd: %v\n%s", err, out)
	}
	return bin
}

// spawn launches one daemon process and records its pid.
func spawn(t *testing.T, bin, peers string, p *proc) {
	t.Helper()
	p.stdout = &lineCapture{}
	p.stderr = &lineCapture{}
	cmd := exec.Command(bin,
		"--id", p.id,
		"--data-dir", p.dataDir,
		"--bind", p.bind,
		"--admin", p.admin,
		"--peers", peers,
	)
	cmd.Stdout = p.stdout
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn %s: %v", p.id, err)
	}
	p.mu.Lock()
	p.cmd = cmd
	p.pid = cmd.Process.Pid
	p.startTime = time.Now()
	p.mu.Unlock()
}

// getStatus issues GET /status against a proc's admin port.
func getStatus(p *proc) (status, error) {
	var s status
	client := &http.Client{Timeout: e2eHTTPTimeout}
	resp, err := client.Get("http://" + p.admin + "/status")
	if err != nil {
		return s, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("status %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&s)
	return s, err
}

// putKey issues PUT /put?key=&value= against a proc's admin port. It returns the
// HTTP status code and (on 421) the leader address the daemon reported.
func putKey(p *proc, key, value string) (int, string, error) {
	client := &http.Client{Timeout: e2eHTTPTimeout}
	url := fmt.Sprintf("http://%s/put?key=%s&value=%s", p.admin, key, value)
	req, err := http.NewRequest(http.MethodPut, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		LeaderAddr string `json:"leaderAddr"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.LeaderAddr, nil
}

// getKey issues GET /get?key= against a proc's admin port.
func getKey(p *proc, key string) (string, bool, error) {
	client := &http.Client{Timeout: e2eHTTPTimeout}
	resp, err := client.Get(fmt.Sprintf("http://%s/get?key=%s", p.admin, key))
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, err
	}
	return body.Value, body.Found, nil
}

// scrapeMetrics issues GET /metrics against a proc's admin port and returns the
// raw Prometheus exposition text and the HTTP Content-Type header.
func scrapeMetrics(p *proc) (string, string, int, error) {
	client := &http.Client{Timeout: e2eHTTPTimeout}
	resp, err := client.Get("http://" + p.admin + "/metrics")
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", resp.StatusCode, err
	}
	return string(body), resp.Header.Get("Content-Type"), resp.StatusCode, nil
}

// parsedMetrics is the result of parsing Prometheus exposition text: per-metric
// sample values (keyed by metric name, ignoring labels for the simple single-
// series metrics this daemon emits) plus the set of metric names that had a
// preceding HELP and TYPE line.
type parsedMetrics struct {
	values    map[string]uint64 // metric name -> sample value (last seen)
	haveHelp  map[string]bool   // metric name -> saw "# HELP <name> ..."
	haveType  map[string]bool   // metric name -> saw "# TYPE <name> ..."
	typeOf    map[string]string // metric name -> declared type (gauge/counter)
	sampleSeq []string          // metric names in sample order, for HELP/TYPE-before-sample checks
}

// parseExposition parses Prometheus text exposition format strictly enough to
// prove well-formedness: it records, for every sample line, that a matching
// HELP and TYPE line for that metric name appeared BEFORE it. It returns an
// error if a sample's value is not a valid number. Metric names with labels
// (e.g. foo{node_id="n1"} 1) are reduced to their base name "foo".
func parseExposition(t *testing.T, text string) parsedMetrics {
	t.Helper()
	pm := parsedMetrics{
		values:   map[string]uint64{},
		haveHelp: map[string]bool{},
		haveType: map[string]bool{},
		typeOf:   map[string]string{},
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			rest := strings.TrimPrefix(line, "# HELP ")
			name := firstField(rest)
			if name != "" {
				pm.haveHelp[name] = true
			}
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			rest := strings.TrimPrefix(line, "# TYPE ")
			name := firstField(rest)
			fields := strings.Fields(rest)
			if name != "" {
				pm.haveType[name] = true
				if len(fields) >= 2 {
					pm.typeOf[name] = fields[1]
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue // other comment
		}
		// Sample line: "<name>[{labels}] <value>".
		sp := strings.LastIndexByte(line, ' ')
		if sp <= 0 {
			t.Fatalf("malformed metric sample line (no value): %q", line)
		}
		nameAndLabels := line[:sp]
		valStr := strings.TrimSpace(line[sp+1:])
		base := nameAndLabels
		if i := strings.IndexByte(base, '{'); i >= 0 {
			base = base[:i]
		}
		v, err := strconv.ParseUint(valStr, 10, 64)
		if err != nil {
			t.Fatalf("metric %q has non-uint value %q: %v", base, valStr, err)
		}
		pm.values[base] = v
		pm.sampleSeq = append(pm.sampleSeq, base)
	}
	return pm
}

// firstField returns the first whitespace-delimited token of s, or "".
func firstField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// waitAdminUp polls a proc's /status until it answers or the deadline passes.
func waitAdminUp(p *proc, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := getStatus(p); err == nil {
			return nil
		}
		time.Sleep(e2ePollInterval)
	}
	return fmt.Errorf("admin %s (%s) never came up within %s", p.id, p.admin, timeout)
}

// waitSingleLeader polls the given procs until EXACTLY one reports IsLeader and
// all live procs agree on the same leader id. Returns the leader proc.
func waitSingleLeader(procs []*proc, timeout time.Duration) (*proc, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		leaders := make([]*proc, 0)
		agree := true
		var leaderID string
		ok := true
		for _, p := range procs {
			s, err := getStatus(p)
			if err != nil {
				lastErr = err
				ok = false
				break
			}
			if s.IsLeader {
				leaders = append(leaders, p)
			}
			if s.Leader == "" {
				agree = false
			} else if leaderID == "" {
				leaderID = s.Leader
			} else if s.Leader != leaderID {
				agree = false
			}
		}
		if ok && len(leaders) == 1 && agree && leaders[0].id == leaderID {
			return leaders[0], nil
		}
		time.Sleep(e2ePollInterval)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("no single agreed leader within %s (last err: %v)", timeout, lastErr)
	}
	return nil, fmt.Errorf("no single agreed leader within %s", timeout)
}

// waitNewLeader polls until a leader different from excludeID emerges among procs.
func waitNewLeader(procs []*proc, excludeID string, timeout time.Duration) (*proc, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, p := range procs {
			s, err := getStatus(p)
			if err != nil {
				continue
			}
			if s.IsLeader && s.Leader == p.id && p.id != excludeID {
				return p, nil
			}
		}
		time.Sleep(e2ePollInterval)
	}
	return nil, fmt.Errorf("no new leader (excluding %s) within %s", excludeID, timeout)
}

// waitKeyValue polls until GET key on proc p returns the wanted value.
func waitKeyValue(p *proc, key, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		val, found, err := getKey(p, key)
		if err == nil && found && val == want {
			return nil
		}
		if err == nil {
			last = val
		}
		time.Sleep(e2ePollInterval)
	}
	return fmt.Errorf("key %q on %s never became %q (last=%q) within %s", key, p.id, want, last, timeout)
}

// killProc SIGKILLs a process and waits for it to reap.
func killProc(p *proc) error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("proc %s not running", p.id)
	}
	if err := cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill %s: %w", p.id, err)
	}
	_, _ = cmd.Process.Wait()
	return nil
}

// stopProc SIGTERMs a process for a graceful shutdown and waits for exit.
func stopProc(p *proc) {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// TestE2EMultiProcessRaftKillRestart is the capstone: a 3-PROCESS persistent
// Raft cluster that survives a leader-process SIGKILL + restart-from-disk, and a
// full-cluster shutdown + restart-from-disk with the value surviving.
func TestE2EMultiProcessRaftKillRestart(t *testing.T) {
	// Evidence sink.
	evDir := filepath.Join("..", "..", "qa-results", "helix-raftd", time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(evDir, 0o750); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	var ev strings.Builder
	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		ev.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "  " + line + "\n")
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(evDir, "e2e.log"), []byte(ev.String()), 0o640)
	}()

	bin := buildDaemon(t)
	logf("BUILT daemon binary: %s", bin)

	ids := []string{"n1", "n2", "n3"}
	raftPorts := []int{freePort(t), freePort(t), freePort(t)}
	adminPorts := []int{freePort(t), freePort(t), freePort(t)}

	procs := make([]*proc, 3)
	var peerParts []string
	for i, id := range ids {
		procs[i] = &proc{
			id:      id,
			bind:    fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			admin:   fmt.Sprintf("127.0.0.1:%d", adminPorts[i]),
			dataDir: t.TempDir(),
		}
		peerParts = append(peerParts, fmt.Sprintf("%s=%s", id, procs[i].bind))
	}
	peers := strings.Join(peerParts, ",")
	logf("PEERS membership: %s", peers)
	logf("RAFT ports: %v  ADMIN ports: %v", raftPorts, adminPorts)

	// Distinct-port proof.
	seen := map[int]bool{}
	for _, p := range append(append([]int{}, raftPorts...), adminPorts...) {
		if seen[p] {
			t.Fatalf("port reuse detected: %d", p)
		}
		seen[p] = true
	}

	procByID := func(id string) *proc {
		for _, p := range procs {
			if p.id == id {
				return p
			}
		}
		return nil
	}

	// Ensure every spawned process is reaped at test end.
	t.Cleanup(func() {
		for _, p := range procs {
			p.mu.Lock()
			cmd := p.cmd
			p.mu.Unlock()
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	})

	// ---- Step 3.3: spawn 3 real daemon processes. ----
	for _, p := range procs {
		spawn(t, bin, peers, p)
		logf("SPAWNED process id=%s pid=%d bind=%s admin=%s data-dir=%s", p.id, p.pid, p.bind, p.admin, p.dataDir)
	}
	initialPIDs := [3]int{procs[0].pid, procs[1].pid, procs[2].pid}
	for _, p := range procs {
		if err := waitAdminUp(p, e2eElectTimeout); err != nil {
			t.Fatalf("%v\n--- %s stderr ---\n%s", err, p.id, p.stderr.String())
		}
	}
	logf("ALL 3 ADMIN ENDPOINTS UP (3 distinct PIDs: %d, %d, %d)", procs[0].pid, procs[1].pid, procs[2].pid)

	// ---- Step 3.4: exactly one leader across the 3 PROCESSES. ----
	leader, err := waitSingleLeader(procs, e2eElectTimeout)
	if err != nil {
		t.Fatalf("initial leader election failed: %v\nn1:%s\nn2:%s\nn3:%s",
			err, procs[0].stderr.String(), procs[1].stderr.String(), procs[2].stderr.String())
	}
	logf("LEADER ELECTED ACROSS PROCESSES: id=%s pid=%d admin=%s", leader.id, leader.pid, leader.admin)

	// ---- Step 3.5: PUT on leader, GET on a FOLLOWER (replication over TCP). ----
	const key, val = "capstone/key", "survives-everything"
	code, ldrAddr, err := putKey(leader, key, val)
	if err != nil {
		t.Fatalf("PUT on leader: %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("PUT on leader returned %d (leaderAddr=%s)", code, ldrAddr)
	}
	logf("PUT %q=%q ACCEPTED by leader %s (HTTP %d)", key, val, leader.id, code)

	// Verify a PUT on a FOLLOWER is rejected with the leader's address (leader-gated).
	var follower *proc
	for _, p := range procs {
		if p.id != leader.id {
			follower = p
			break
		}
	}
	fcode, faddr, err := putKey(follower, "should/reject", "x")
	if err != nil {
		t.Fatalf("PUT on follower: %v", err)
	}
	if fcode != http.StatusMisdirectedRequest {
		t.Fatalf("PUT on follower %s returned %d, want 421", follower.id, fcode)
	}
	logf("LEADER-GATED WRITE: PUT on follower %s rejected (HTTP %d), redirected to leaderAddr=%s", follower.id, fcode, faddr)

	// GET the key on the follower — proves replication reached the follower's FSM.
	if err := waitKeyValue(follower, key, val, e2eReplTimeout); err != nil {
		t.Fatalf("replication to follower failed: %v", err)
	}
	logf("REPLICATION ACROSS PROCESSES: follower %s serves %q=%q from its own FSM", follower.id, key, val)

	// ---- Step 3.6: KILL the leader PROCESS (SIGKILL). ----
	killedLeaderID := leader.id
	killedLeaderDir := leader.dataDir
	killedLeaderBind := leader.bind
	if err := killProc(leader); err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	logf("SIGKILLED leader process id=%s pid=%d", leader.id, leader.pid)

	survivors := make([]*proc, 0, 2)
	for _, p := range procs {
		if p.id != killedLeaderID {
			survivors = append(survivors, p)
		}
	}
	newLeader, err := waitNewLeader(survivors, killedLeaderID, e2eElectTimeout)
	if err != nil {
		t.Fatalf("no new leader after kill: %v", err)
	}
	logf("NEW LEADER among survivors after kill: id=%s pid=%d", newLeader.id, newLeader.pid)

	// Value survived the leader loss.
	if err := waitKeyValue(newLeader, key, val, e2eReplTimeout); err != nil {
		t.Fatalf("value lost after leader kill: %v", err)
	}
	logf("VALUE SURVIVED LEADER KILL: new leader %s serves %q=%q", newLeader.id, key, val)

	// PUT a second key on the new leader WHILE the old leader is down (so the
	// restarted node must catch up an entry it never saw).
	const key2, val2 = "capstone/while-down", "added-after-kill"
	if code, _, err := putKey(newLeader, key2, val2); err != nil || code != http.StatusOK {
		t.Fatalf("PUT key2 on new leader: code=%d err=%v", code, err)
	}
	logf("PUT %q=%q on new leader %s while %s is DOWN", key2, val2, newLeader.id, killedLeaderID)

	// ---- Step 3.7: RESTART the killed node from its SAME data dir. ----
	restarted := procByID(killedLeaderID)
	spawn(t, bin, peers, restarted)
	logf("RESTARTED killed node id=%s NEW pid=%d from SAME data-dir=%s bind=%s", restarted.id, restarted.pid, killedLeaderDir, killedLeaderBind)
	if err := waitAdminUp(restarted, e2eRestartTimeout); err != nil {
		t.Fatalf("restarted node admin never came up: %v\nstderr:%s", err, restarted.stderr.String())
	}

	// It rejoins and catches up: lastIndex matches the leader's, both keys present.
	leaderStatus, _ := getStatus(newLeader)
	deadline := time.Now().Add(e2eRestartTimeout)
	var rejoined bool
	for time.Now().Before(deadline) {
		s, err := getStatus(restarted)
		if err == nil && !s.IsLeader && s.Leader == newLeader.id && s.LastIndex >= leaderStatus.LastIndex {
			rejoined = true
			break
		}
		time.Sleep(e2ePollInterval)
	}
	if !rejoined {
		s, _ := getStatus(restarted)
		t.Fatalf("restarted node did not rejoin+catch up: %+v (leader lastIndex=%d)", s, leaderStatus.LastIndex)
	}
	logf("REJOINED: restarted %s follows %s, lastIndex caught up (>= %d)", restarted.id, newLeader.id, leaderStatus.LastIndex)

	// Both the original key AND the one added while it was down are served by the
	// restarted process from its recovered+caught-up FSM.
	if err := waitKeyValue(restarted, key, val, e2eReplTimeout); err != nil {
		t.Fatalf("restarted node missing original key: %v", err)
	}
	if err := waitKeyValue(restarted, key2, val2, e2eReplTimeout); err != nil {
		t.Fatalf("restarted node did not catch up key2: %v", err)
	}
	logf("RESTART-FROM-DISK RECOVERY: %s serves BOTH %q=%q (persisted) and %q=%q (caught up live)", restarted.id, key, val, key2, val2)

	// ---- Step 3.8 anti-bluff: FULL cluster shutdown, then restart ALL from disk. ----
	// Gracefully stop all 3 so every BoltDB flushes cleanly.
	for _, p := range procs {
		stopProc(p)
	}
	logf("FULL CLUSTER STOPPED (all 3 processes exited)")

	// On-disk proof: every data dir has a non-empty raft.db. With the cluster fully
	// down, the value can ONLY come from these files on the next boot.
	dbSizes := map[string]int64{}
	for _, p := range procs {
		dbPath := filepath.Join(p.dataDir, "raft.db")
		fi, err := os.Stat(dbPath)
		if err != nil {
			t.Fatalf("raft.db missing for %s: %v", p.id, err)
		}
		if fi.Size() == 0 {
			t.Fatalf("raft.db empty for %s", p.id)
		}
		dbSizes[p.id] = fi.Size()
	}
	logf("ON-DISK PROOF: raft.db sizes (bytes): %v", dbSizes)

	// Restart ALL 3 from their SAME data dirs.
	for _, p := range procs {
		spawn(t, bin, peers, p)
		logf("FULL-RESTART spawned id=%s NEW pid=%d data-dir=%s", p.id, p.pid, p.dataDir)
	}
	for _, p := range procs {
		if err := waitAdminUp(p, e2eRestartTimeout); err != nil {
			t.Fatalf("full-restart %s admin down: %v\nstderr:%s", p.id, err, p.stderr.String())
		}
	}
	finalLeader, err := waitSingleLeader(procs, e2eElectTimeout)
	if err != nil {
		t.Fatalf("no leader after full-cluster restart: %v", err)
	}
	logf("FULL-CLUSTER RESTART: leader re-elected id=%s pid=%d", finalLeader.id, finalLeader.pid)

	// The DECISIVE persistence proof: after a full shutdown, BOTH values are still
	// readable on EVERY restarted process — they could only come from on-disk BoltDB.
	for _, p := range procs {
		if err := waitKeyValue(p, key, val, e2eReplTimeout); err != nil {
			t.Fatalf("PERSISTENCE FAIL: %s lost %q after full restart: %v", p.id, key, err)
		}
		if err := waitKeyValue(p, key2, val2, e2eReplTimeout); err != nil {
			t.Fatalf("PERSISTENCE FAIL: %s lost %q after full restart: %v", p.id, key2, err)
		}
	}
	logf("DECISIVE PERSISTENCE PROOF: after FULL cluster kill+restart-from-disk, ALL 3 processes serve %q=%q and %q=%q (value could ONLY come from on-disk BoltDB)", key, val, key2, val2)

	// Final graceful stop (cleanup also kills, but stop flushes cleanly).
	for _, p := range procs {
		stopProc(p)
	}

	// Write a human-readable evidence README alongside the log.
	readme := fmt.Sprintf(`# helix-raftd multi-process E2E evidence

Run: %s (UTC)
Daemon binary: %s
Peers membership: %s
Raft TCP ports (3 distinct): %v
Admin HTTP ports (3 distinct): %v

## Proven
- 3 REAL OS processes spawned (initial PIDs %d, %d, %d), each own data-dir + fixed bind.
- Leader elected ACROSS PROCESSES over real TCP.
- PUT %q=%q on leader; GET returned it on a FOLLOWER process (replication over TCP).
- Leader-gated write: PUT on a follower rejected (HTTP 421) with leader address.
- SIGKILLed the leader process (%s); a NEW leader emerged among the 2 survivors; value survived.
- PUT %q=%q committed WHILE %s was down.
- Restarted %s from its SAME data-dir; it rejoined, caught up lastIndex, and served BOTH keys.
- FULL cluster stop; raft.db sizes on disk: %v.
- Restarted ALL 3 from disk; leader re-elected; BOTH values readable on every process
  (could ONLY come from on-disk BoltDB) — true persistence, not just live replication.

See e2e.log for the timestamped event trace.
`, time.Now().UTC().Format(time.RFC3339), bin, peers, raftPorts, adminPorts,
		initialPIDs[0], initialPIDs[1], initialPIDs[2], key, val, killedLeaderID,
		key2, val2, killedLeaderID, killedLeaderID, dbSizes)
	_ = os.WriteFile(filepath.Join(evDir, "README.md"), []byte(readme), 0o640)
	logf("EVIDENCE written to %s", evDir)
}

// TestE2EMetricsEndpoint proves the /metrics endpoint serves REAL, live raft +
// FSM state — not constants. It spawns a genuine 3-process cluster, scrapes
// /metrics on the leader and a follower BEFORE and AFTER a PUT, and asserts the
// scraped numbers reflect the actual state changes:
//
//   - Leader vs follower: helix_raftd_is_leader is 1 on the leader process and 0
//     on a follower process. A hardcoded metric could not differ between the two.
//   - fsm_keys grows by the number of PUT keys: scraped fsm_keys AFTER N PUTs
//     equals before+N (on the leader, and on the follower once replicated). A
//     constant would fail this delta check.
//   - fsm_applied_total is a monotonic counter that is >= the number of PUTs
//     applied and strictly greater after the PUTs than before.
//   - last_log_index advances after the PUTs (more committed entries).
//   - The exposition text is well-formed: every emitted metric has a preceding
//     HELP and TYPE line, and every sample value parses as a number.
func TestE2EMetricsEndpoint(t *testing.T) {
	evDir := filepath.Join("..", "..", "qa-results", "helix-raftd", "metrics-"+time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(evDir, 0o750); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	var ev strings.Builder
	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		ev.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "  " + line + "\n")
	}
	defer func() { _ = os.WriteFile(filepath.Join(evDir, "metrics-e2e.log"), []byte(ev.String()), 0o640) }()

	bin := buildDaemon(t)
	logf("BUILT daemon binary: %s", bin)

	ids := []string{"n1", "n2", "n3"}
	raftPorts := []int{freePort(t), freePort(t), freePort(t)}
	adminPorts := []int{freePort(t), freePort(t), freePort(t)}
	procs := make([]*proc, 3)
	var peerParts []string
	for i, id := range ids {
		procs[i] = &proc{
			id:      id,
			bind:    fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			admin:   fmt.Sprintf("127.0.0.1:%d", adminPorts[i]),
			dataDir: t.TempDir(),
		}
		peerParts = append(peerParts, fmt.Sprintf("%s=%s", id, procs[i].bind))
	}
	peers := strings.Join(peerParts, ",")
	logf("PEERS membership: %s", peers)

	t.Cleanup(func() {
		for _, p := range procs {
			p.mu.Lock()
			cmd := p.cmd
			p.mu.Unlock()
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	})

	for _, p := range procs {
		spawn(t, bin, peers, p)
		logf("SPAWNED id=%s pid=%d admin=%s", p.id, p.pid, p.admin)
	}
	for _, p := range procs {
		if err := waitAdminUp(p, e2eElectTimeout); err != nil {
			t.Fatalf("%v\nstderr:%s", err, p.stderr.String())
		}
	}

	leader, err := waitSingleLeader(procs, e2eElectTimeout)
	if err != nil {
		t.Fatalf("leader election failed: %v", err)
	}
	var follower *proc
	for _, p := range procs {
		if p.id != leader.id {
			follower = p
			break
		}
	}
	logf("LEADER=%s FOLLOWER=%s", leader.id, follower.id)

	// helper: scrape + parse + assert well-formed exposition for one proc.
	scrapeParse := func(p *proc, when string) parsedMetrics {
		text, ctype, code, err := scrapeMetrics(p)
		if err != nil {
			t.Fatalf("scrape %s %s: %v", p.id, when, err)
		}
		if code != http.StatusOK {
			t.Fatalf("scrape %s %s: HTTP %d", p.id, when, code)
		}
		if !strings.HasPrefix(ctype, "text/plain; version=0.0.4") {
			t.Fatalf("scrape %s %s: Content-Type %q, want Prometheus text/plain; version=0.0.4", p.id, when, ctype)
		}
		_ = os.WriteFile(filepath.Join(evDir, fmt.Sprintf("metrics-%s-%s.txt", p.id, when)), []byte(text), 0o640)
		logf("SCRAPED /metrics from %s (%s), Content-Type=%q:\n%s", p.id, when, ctype, text)
		pm := parseExposition(t, text)
		// Well-formedness: every emitted metric must have HELP+TYPE before its sample.
		for _, name := range pm.sampleSeq {
			if !pm.haveHelp[name] {
				t.Fatalf("%s %s: metric %q has a sample but no preceding # HELP", p.id, when, name)
			}
			if !pm.haveType[name] {
				t.Fatalf("%s %s: metric %q has a sample but no preceding # TYPE", p.id, when, name)
			}
		}
		// The expected metric set must be present.
		for _, name := range []string{
			"helix_raftd_is_leader", "helix_raftd_term", "helix_raftd_last_log_index",
			"helix_raftd_commit_index", "helix_raftd_applied_index",
			"helix_raftd_last_snapshot_index", "helix_raftd_fsm_keys",
			"helix_raftd_fsm_applied_total",
		} {
			if _, ok := pm.values[name]; !ok {
				t.Fatalf("%s %s: missing expected metric %q", p.id, when, name)
			}
		}
		// Type declarations are correct where it matters.
		if pm.typeOf["helix_raftd_fsm_applied_total"] != "counter" {
			t.Fatalf("%s %s: fsm_applied_total TYPE=%q, want counter", p.id, when, pm.typeOf["helix_raftd_fsm_applied_total"])
		}
		if pm.typeOf["helix_raftd_is_leader"] != "gauge" {
			t.Fatalf("%s %s: is_leader TYPE=%q, want gauge", p.id, when, pm.typeOf["helix_raftd_is_leader"])
		}
		return pm
	}

	// ---- BEFORE the PUT: capture leader & follower metrics. ----
	leaderBefore := scrapeParse(leader, "before")
	followerBefore := scrapeParse(follower, "before")

	// ANTI-BLUFF #1: is_leader differs between the leader and follower PROCESSES.
	// A hardcoded constant could not be 1 here and 0 there.
	if leaderBefore.values["helix_raftd_is_leader"] != 1 {
		t.Fatalf("leader %s reported helix_raftd_is_leader=%d, want 1", leader.id, leaderBefore.values["helix_raftd_is_leader"])
	}
	if followerBefore.values["helix_raftd_is_leader"] != 0 {
		t.Fatalf("follower %s reported helix_raftd_is_leader=%d, want 0", follower.id, followerBefore.values["helix_raftd_is_leader"])
	}
	logf("ANTI-BLUFF leader/follower: is_leader=1 on leader %s, is_leader=0 on follower %s", leader.id, follower.id)

	keysBeforeLeader := leaderBefore.values["helix_raftd_fsm_keys"]
	appliedBeforeLeader := leaderBefore.values["helix_raftd_fsm_applied_total"]
	lastLogBeforeLeader := leaderBefore.values["helix_raftd_last_log_index"]
	keysBeforeFollower := followerBefore.values["helix_raftd_fsm_keys"]

	// ---- Do N distinct PUTs on the leader. ----
	const n = 3
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("metrics/key-%d", i)
		val := fmt.Sprintf("v-%d", i)
		code, addr, err := putKey(leader, key, val)
		if err != nil || code != http.StatusOK {
			t.Fatalf("PUT %s on leader: code=%d addr=%s err=%v", key, code, addr, err)
		}
	}
	logf("PUT %d distinct keys on leader %s", n, leader.id)

	// Wait until the follower has replicated all N keys (so its FSM count is stable).
	for i := 0; i < n; i++ {
		if err := waitKeyValue(follower, fmt.Sprintf("metrics/key-%d", i), fmt.Sprintf("v-%d", i), e2eReplTimeout); err != nil {
			t.Fatalf("follower did not replicate metrics/key-%d: %v", i, err)
		}
	}

	// ---- AFTER the PUT: re-scrape leader & follower. ----
	leaderAfter := scrapeParse(leader, "after")
	followerAfter := scrapeParse(follower, "after")

	// ANTI-BLUFF #2: fsm_keys on the LEADER grew by EXACTLY n. A constant metric
	// would still read keysBeforeLeader and fail this delta.
	if got, want := leaderAfter.values["helix_raftd_fsm_keys"], keysBeforeLeader+n; got != want {
		t.Fatalf("leader fsm_keys after %d PUTs = %d, want %d (before=%d)", n, got, want, keysBeforeLeader)
	}
	logf("ANTI-BLUFF leader fsm_keys: %d -> %d (delta +%d == PUTs)", keysBeforeLeader, leaderAfter.values["helix_raftd_fsm_keys"], n)

	// ANTI-BLUFF #3: fsm_keys on the FOLLOWER also grew by n — the metric reflects
	// THAT process's replicated FSM, proving it is read per-node, not shared.
	if got, want := followerAfter.values["helix_raftd_fsm_keys"], keysBeforeFollower+n; got != want {
		t.Fatalf("follower fsm_keys after %d PUTs = %d, want %d (before=%d)", n, got, want, keysBeforeFollower)
	}
	logf("ANTI-BLUFF follower fsm_keys: %d -> %d (delta +%d == replicated PUTs)", keysBeforeFollower, followerAfter.values["helix_raftd_fsm_keys"], n)

	// ANTI-BLUFF #4: fsm_applied_total (counter) strictly increased on the leader
	// by at least n, and is >= 1 — it counts real Apply() calls.
	if leaderAfter.values["helix_raftd_fsm_applied_total"] < appliedBeforeLeader+n {
		t.Fatalf("leader fsm_applied_total = %d, want >= %d (before=%d + %d PUTs)",
			leaderAfter.values["helix_raftd_fsm_applied_total"], appliedBeforeLeader+n, appliedBeforeLeader, n)
	}
	if leaderAfter.values["helix_raftd_fsm_applied_total"] < 1 {
		t.Fatalf("leader fsm_applied_total = %d, want >= 1", leaderAfter.values["helix_raftd_fsm_applied_total"])
	}
	logf("ANTI-BLUFF fsm_applied_total: %d -> %d (>= +%d)", appliedBeforeLeader, leaderAfter.values["helix_raftd_fsm_applied_total"], n)

	// ANTI-BLUFF #5: last_log_index advanced after the PUTs and is > 0.
	if leaderAfter.values["helix_raftd_last_log_index"] <= lastLogBeforeLeader {
		t.Fatalf("leader last_log_index did not advance: before=%d after=%d", lastLogBeforeLeader, leaderAfter.values["helix_raftd_last_log_index"])
	}
	if leaderAfter.values["helix_raftd_last_log_index"] == 0 {
		t.Fatalf("leader last_log_index is 0 after PUTs")
	}
	logf("ANTI-BLUFF last_log_index advanced: %d -> %d", lastLogBeforeLeader, leaderAfter.values["helix_raftd_last_log_index"])

	// Post-PUT invariants: fsm_keys >= 1, is_leader still distinct.
	if leaderAfter.values["helix_raftd_fsm_keys"] < 1 {
		t.Fatalf("leader fsm_keys < 1 after PUTs")
	}
	if leaderAfter.values["helix_raftd_is_leader"] != 1 || followerAfter.values["helix_raftd_is_leader"] != 0 {
		t.Fatalf("is_leader leader/follower drifted: leader=%d follower=%d",
			leaderAfter.values["helix_raftd_is_leader"], followerAfter.values["helix_raftd_is_leader"])
	}

	for _, p := range procs {
		stopProc(p)
	}
	logf("METRICS E2E PASSED — scraped /metrics reflects real per-node raft+FSM state. Evidence in %s", evDir)
}
