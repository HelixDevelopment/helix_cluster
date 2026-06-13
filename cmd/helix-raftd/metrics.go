package main

import (
	"net/http"
	"strconv"
	"strings"
)

// metricsContentType is the Prometheus text exposition format (v0.0.4) media type.
// Scrapers (Prometheus, victoria-metrics, etc.) negotiate on this exact value.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// handleMetrics emits this node's LIVE raft + FSM state in Prometheus text
// exposition format. Every sample is read at scrape time from the real raft node
// (node.Raft().Stats(), node.IsLeader(), node.LastIndex()) and its FSM
// (node.FSM().Len()/AppliedCount()) — nothing is cached or hardcoded, so the
// scraped numbers change as the cluster's state changes (a follower reports
// is_leader 0, the leader reports 1; fsm_keys rises after a PUT; etc.).
//
// Format: for each metric we print a `# HELP` line, a `# TYPE` line, then the
// sample line(s), matching the layout a Prometheus scraper validates.
func (a *adminServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}

	// Snapshot live state ONCE per scrape so the emitted sample set is internally
	// consistent for this request.
	nodeID := a.node.ID()
	isLeader := a.node.IsLeader()
	stats := a.node.Raft().Stats() // real map[string]string from hashicorp/raft
	fsmKeys := a.node.FSM().Len()
	fsmApplied := a.node.FSM().AppliedCount()

	// lastLogIndex prefers the parsed Stats() value, falling back to the node's
	// own LastIndex() accessor if the key is somehow absent/unparseable.
	lastLogIndex := statUint(stats, "last_log_index")
	if _, ok := stats["last_log_index"]; !ok {
		lastLogIndex = a.node.LastIndex()
	}

	// label is the common {node_id="..."} label, value safely escaped.
	label := `{node_id="` + escapeLabelValue(nodeID) + `"}`

	var b strings.Builder
	b.Grow(1024)

	gauge := func(name, help string, labeled bool, value uint64) {
		b.WriteString("# HELP " + name + " " + help + "\n")
		b.WriteString("# TYPE " + name + " gauge\n")
		b.WriteString(name)
		if labeled {
			b.WriteString(label)
		}
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(value, 10))
		b.WriteByte('\n')
	}
	counter := func(name, help string, labeled bool, value uint64) {
		b.WriteString("# HELP " + name + " " + help + "\n")
		b.WriteString("# TYPE " + name + " counter\n")
		b.WriteString(name)
		if labeled {
			b.WriteString(label)
		}
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(value, 10))
		b.WriteByte('\n')
	}

	boolVal := uint64(0)
	if isLeader {
		boolVal = 1
	}

	gauge("helix_raftd_is_leader",
		"Whether this node is the current raft leader (1) or not (0).",
		true, boolVal)
	gauge("helix_raftd_term",
		"Current raft term as seen by this node.",
		true, statUint(stats, "term"))
	gauge("helix_raftd_last_log_index",
		"Index of this node's last raft log entry.",
		true, lastLogIndex)
	gauge("helix_raftd_commit_index",
		"Highest raft log index known to be committed on this node.",
		true, statUint(stats, "commit_index"))
	gauge("helix_raftd_applied_index",
		"Highest raft log index applied to this node's FSM.",
		true, statUint(stats, "applied_index"))
	gauge("helix_raftd_last_snapshot_index",
		"Index of this node's most recent raft snapshot (0 if none).",
		true, statUint(stats, "last_snapshot_index"))
	gauge("helix_raftd_fsm_keys",
		"Number of keys currently stored in this node's replicated key/value FSM.",
		true, uint64(fsmKeys))
	counter("helix_raftd_fsm_applied_total",
		"Total number of commands this node's FSM has applied since process start.",
		true, fsmApplied)

	w.Header().Set("Content-Type", metricsContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// statUint parses stats[key] (a decimal string from raft.Stats()) as a uint64,
// returning 0 for a missing or unparseable value — never panicking. This is the
// "emit 0, not a panic" contract for any absent/garbage key.
func statUint(stats map[string]string, key string) uint64 {
	s, ok := stats[key]
	if !ok {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// escapeLabelValue escapes a Prometheus label value per the text exposition
// format: backslash, double-quote, and newline. Node ids are simple (n1/n2/n3)
// but a malformed id must not be able to produce invalid exposition text.
func escapeLabelValue(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}
