// session_chaos.go — HXC-1109: Deterministic-sim chaos harness.
//
// This file implements the in-process cluster session simulation with node-kill
// + reschedule + membership reconvergence scenario. The entire simulation runs
// on the DST Engine (deterministic, seeded, no goroutines, no real nodes) so it
// is hermetically testable with a fixed seed.
//
// Key types:
//
//	SimCluster      — a small virtual cluster of SimNodes managed by the Engine.
//	SimSession      — a workload session assigned to a node, reschedulable.
//	RecoveryTimeline — append-only log of cluster lifecycle events with per-run UUID.
//
// The chaos scenario:
//  1. Start a SimCluster with N nodes (N >= 3).
//  2. Assign a SimSession to one of the nodes.
//  3. Kill the node (NodeCrash fault + remove from membership).
//  4. Trigger recovery: the scheduler reschedules the session onto a surviving node.
//  5. Membership reconverges to N-1.
//  6. Assert:
//     - Session.AssignedNode != killed node.
//     - Session.AssignedNode is online.
//     - Membership size == N-1.
//     - RecoveryTimeline contains "kill", "reschedule", "reconverge" events.
package dst

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// RecoveryTimeline
// ---------------------------------------------------------------------------

// TimelineEventKind describes the type of a recovery timeline event.
type TimelineEventKind string

const (
	TimelineKillNode    TimelineEventKind = "kill"
	TimelineReschedule  TimelineEventKind = "reschedule"
	TimelineReconverge  TimelineEventKind = "reconverge"
	TimelineSessionStart TimelineEventKind = "session_start"
)

// TimelineEvent is a single entry in the recovery timeline log.
type TimelineEvent struct {
	Kind      TimelineEventKind `json:"kind"`
	NodeID    string            `json:"node_id,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	VirtualTS int64             `json:"virtual_ts"`
}

// RecoveryTimeline is an append-only log of cluster lifecycle events for a
// single chaos scenario run. It carries a per-run UUID for evidence tracing.
type RecoveryTimeline struct {
	mu     sync.RWMutex
	RunID  string
	Events []TimelineEvent
}

// NewRecoveryTimeline creates a timeline with a fresh per-run UUID.
func NewRecoveryTimeline() *RecoveryTimeline {
	return &RecoveryTimeline{
		RunID:  uuid.New().String(),
		Events: make([]TimelineEvent, 0, 16),
	}
}

// Append adds a TimelineEvent to the log.
func (tl *RecoveryTimeline) Append(ev TimelineEvent) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.Events = append(tl.Events, ev)
}

// ContainsKind returns true if the timeline contains at least one event of the given kind.
func (tl *RecoveryTimeline) ContainsKind(kind TimelineEventKind) bool {
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	for _, ev := range tl.Events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

// Snapshot returns a copy of all events (safe for concurrent read after chaos).
func (tl *RecoveryTimeline) Snapshot() []TimelineEvent {
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	out := make([]TimelineEvent, len(tl.Events))
	copy(out, tl.Events)
	return out
}

// ---------------------------------------------------------------------------
// SimSession
// ---------------------------------------------------------------------------

// SimSession represents a workload session assigned to a cluster node.
type SimSession struct {
	ID           string
	AssignedNode string // current owning node ID
	Active       bool   // false if node was killed before reschedule
}

// ---------------------------------------------------------------------------
// SimCluster
// ---------------------------------------------------------------------------

// SimCluster is a virtual cluster of SimNodes managed by a DST Engine.
// It implements a minimal membership model and session scheduler: on node kill,
// the cluster reschedules any session the dead node was running.
//
// All operations are single-threaded (driven by the Engine's event loop) and
// therefore safe to call from within an Engine event handler.
type SimCluster struct {
	eng      *Engine
	timeline *RecoveryTimeline

	mu       sync.RWMutex
	members  map[string]*SimNode // nodeID -> SimNode
	sessions map[string]*SimSession // sessionID -> SimSession

	// reschedulingEnabled controls whether the scheduler tries to move sessions
	// after a node kill. Set to false to test the "disabled reschedule" mutation.
	reschedulingEnabled bool
}

// SimNode is a virtual cluster member.
type SimNode struct {
	ID      string
	Address string
	Online  bool
}

// NewSimCluster creates a SimCluster backed by eng. The cluster is initially empty.
func NewSimCluster(eng *Engine, tl *RecoveryTimeline) *SimCluster {
	return &SimCluster{
		eng:                 eng,
		timeline:            tl,
		members:             make(map[string]*SimNode),
		sessions:            make(map[string]*SimSession),
		reschedulingEnabled: true,
	}
}

// AddNode registers a new online node and mirrors it in the DST engine.
func (sc *SimCluster) AddNode(id, addr string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.members[id] = &SimNode{ID: id, Address: addr, Online: true}
	sc.eng.RegisterNode(id, addr)
}

// MemberCount returns the number of currently online members.
func (sc *SimCluster) MemberCount() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	count := 0
	for _, n := range sc.members {
		if n.Online {
			count++
		}
	}
	return count
}

// OnlineMembers returns the IDs of all online members, sorted.
func (sc *SimCluster) OnlineMembers() []string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	var ids []string
	for id, n := range sc.members {
		if n.Online {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// StartSession assigns a session to the given node. The session must not already exist.
func (sc *SimCluster) StartSession(sessionID, nodeID string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if _, exists := sc.sessions[sessionID]; exists {
		return fmt.Errorf("session %q already exists", sessionID)
	}
	n, ok := sc.members[nodeID]
	if !ok || !n.Online {
		return fmt.Errorf("node %q not online", nodeID)
	}
	sc.sessions[sessionID] = &SimSession{
		ID:           sessionID,
		AssignedNode: nodeID,
		Active:       true,
	}
	sc.timeline.Append(TimelineEvent{
		Kind:      TimelineSessionStart,
		NodeID:    nodeID,
		SessionID: sessionID,
		Detail:    "session started",
		VirtualTS: sc.eng.clock,
	})
	return nil
}

// GetSession returns the SimSession, or nil if not found.
func (sc *SimCluster) GetSession(sessionID string) *SimSession {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.sessions[sessionID]
}

// KillNode marks a node offline, appends a "kill" timeline event, and triggers
// session rescheduling for any sessions that were running on the dead node.
//
// The DST engine node is also marked offline (Online=false on the engine node).
func (sc *SimCluster) KillNode(nodeID string) error {
	sc.mu.Lock()
	n, ok := sc.members[nodeID]
	if !ok {
		sc.mu.Unlock()
		return fmt.Errorf("node %q not found", nodeID)
	}
	n.Online = false

	// Mark the engine node offline.
	sc.mu.Unlock()
	if engNode, found := sc.eng.GetNode(nodeID); found {
		engNode.Online = false
	}
	sc.mu.Lock()

	sc.timeline.Append(TimelineEvent{
		Kind:      TimelineKillNode,
		NodeID:    nodeID,
		Detail:    "node killed (NodeCrash fault applied)",
		VirtualTS: sc.eng.clock,
	})

	// Find sessions assigned to the dead node and schedule rescheduling.
	var affected []string
	for sid, sess := range sc.sessions {
		if sess.AssignedNode == nodeID && sess.Active {
			sess.Active = false // temporarily orphaned
			affected = append(affected, sid)
		}
	}
	sc.mu.Unlock()

	// Reschedule affected sessions onto a surviving node.
	for _, sid := range affected {
		if err := sc.rescheduleSession(sid, nodeID); err != nil {
			return fmt.Errorf("reschedule %q: %w", sid, err)
		}
	}
	return nil
}

// rescheduleSession picks a surviving online node and moves the session to it.
// If reschedulingEnabled is false, the session stays dead (mutation target).
func (sc *SimCluster) rescheduleSession(sessionID, killedNodeID string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sess, ok := sc.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}

	if !sc.reschedulingEnabled {
		// Reschedule disabled — session stays dead. This is the mutation target:
		// disabling this causes the "session rescheduled" assertion to fail.
		sc.timeline.Append(TimelineEvent{
			Kind:      TimelineReschedule,
			SessionID: sessionID,
			Detail:    "reschedule SKIPPED (disabled)",
			VirtualTS: sc.eng.clock,
		})
		return nil
	}

	// Pick the first online node that is NOT the dead node.
	var candidates []string
	for id, n := range sc.members {
		if n.Online && id != killedNodeID {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates) // deterministic selection
	if len(candidates) == 0 {
		return fmt.Errorf("no surviving node available for session %q", sessionID)
	}

	target := candidates[0]
	sess.AssignedNode = target
	sess.Active = true

	sc.timeline.Append(TimelineEvent{
		Kind:      TimelineReschedule,
		NodeID:    target,
		SessionID: sessionID,
		Detail:    fmt.Sprintf("session moved from %s to %s", killedNodeID, target),
		VirtualTS: sc.eng.clock,
	})
	return nil
}

// Reconverge records the membership reconvergence event (after SWIM gossip
// would have propagated the node removal). In the sim this is deterministic: we
// simply log it as an observable event on the timeline.
func (sc *SimCluster) Reconverge() {
	alive := sc.MemberCount()
	sc.timeline.Append(TimelineEvent{
		Kind:      TimelineReconverge,
		Detail:    fmt.Sprintf("membership reconverged: %d online", alive),
		VirtualTS: sc.eng.clock,
	})
}

// ---------------------------------------------------------------------------
// RunChaosScenario — the top-level entry point for HXC-1109.
// ---------------------------------------------------------------------------

// ChaosScenarioConfig parameterises the chaos scenario.
type ChaosScenarioConfig struct {
	// Seed controls the DST engine's PRNG for full reproducibility.
	Seed int64
	// NodeCount is the number of nodes in the simulated cluster (min 3).
	NodeCount int
	// KillNodeID is the node to kill mid-session. If empty, defaults to "node-1".
	KillNodeID string
	// SessionID is the session to run. If empty, defaults to "sess-A".
	SessionID string
	// DisableReschedule is the §1.1 mutation flag: set true to skip rescheduling
	// and assert the session is NOT recovered (used in mutation tests only).
	DisableReschedule bool
}

// ChaosScenarioResult is the result of RunChaosScenario.
type ChaosScenarioResult struct {
	Timeline        *RecoveryTimeline
	FinalMemberCount int
	Session         *SimSession
}

// RunChaosScenario runs the full kill-reschedule-reconverge chaos scenario on
// the DST engine and returns the observable results. It is fully deterministic
// and in-process — no real nodes, no goroutines.
//
// Scenario steps (driven by the DST event queue):
//
//  1. Build cluster with N nodes.
//  2. Start session on KillNodeID.
//  3. Schedule "kill" event at t=10.
//  4. Schedule "reconverge" event at t=20.
//  5. Run engine up to t=30 (maxEvents=100).
//  6. Return observable results.
func RunChaosScenario(cfg ChaosScenarioConfig) (*ChaosScenarioResult, error) {
	if cfg.NodeCount < 3 {
		cfg.NodeCount = 3
	}
	if cfg.KillNodeID == "" {
		cfg.KillNodeID = "node-1"
	}
	if cfg.SessionID == "" {
		cfg.SessionID = "sess-A"
	}

	eng := NewEngine(cfg.Seed)
	tl := NewRecoveryTimeline()
	cluster := NewSimCluster(eng, tl)
	cluster.reschedulingEnabled = !cfg.DisableReschedule

	// Register nodes.
	for i := 1; i <= cfg.NodeCount; i++ {
		id := fmt.Sprintf("node-%d", i)
		addr := fmt.Sprintf("10.0.0.%d", i)
		cluster.AddNode(id, addr)
	}

	// Start the session on the node that will be killed.
	if err := cluster.StartSession(cfg.SessionID, cfg.KillNodeID); err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}

	// Schedule the kill event at virtual time 10.
	eng.RegisterHandler("cluster-kill", func(e *Event) {
		if err := cluster.KillNode(cfg.KillNodeID); err != nil {
			// In the sim, errors here are fatal for the test.
			panic(fmt.Sprintf("KillNode: %v", err))
		}
	})
	eng.ScheduleEvent("cluster-kill", cfg.KillNodeID, 10)

	// Schedule the reconverge event at virtual time 20.
	eng.RegisterHandler("cluster-reconverge", func(e *Event) {
		cluster.Reconverge()
	})
	eng.ScheduleEvent("cluster-reconverge", nil, 20)

	// Run the engine.
	eng.Run(100)

	return &ChaosScenarioResult{
		Timeline:        tl,
		FinalMemberCount: cluster.MemberCount(),
		Session:         cluster.GetSession(cfg.SessionID),
	}, nil
}

// ---------------------------------------------------------------------------
// SimCluster.NowDuration — bridge to engine virtual time for logging.
// ---------------------------------------------------------------------------

// NowVirtual returns the engine's current virtual clock value as a time.Time.
func (sc *SimCluster) NowVirtual() time.Time {
	return sc.eng.Now()
}
