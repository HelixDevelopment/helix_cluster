package agentprovision

import (
	"fmt"
	"sync"

	"github.com/HelixDevelopment/helix_cluster/pkg/ratelimit"
)

// nodeState holds the per-node concurrency accounting.
type nodeState struct {
	live  int      // number of currently running agents on this node
	queue []string // agent IDs waiting for a slot (FIFO)
}

// AgentAdmission enforces two independent admission policies:
//
//  1. Per-user rate limiting via a PerKeyLimiter — prevents a single user from
//     flooding the cluster with admission requests.
//  2. Per-node capacity cap — limits the number of concurrently RUNNING agents
//     on a single node.  Agents that arrive when the node is at cap are queued
//     rather than rejected outright.
//
// All methods are safe for concurrent use.
type AgentAdmission struct {
	mu          sync.Mutex
	capPerNode  int
	userLimiter *ratelimit.PerKeyLimiter
	nodes       map[string]*nodeState
}

// NewAgentAdmission creates an AgentAdmission.
//
//   - capPerNode: the maximum number of concurrently live agents allowed on any
//     single node.  Must be >= 1.
//   - perUserLimiter: a *ratelimit.PerKeyLimiter that gates per-user admission
//     rate; each Admit call consumes one token from the user's bucket.
func NewAgentAdmission(capPerNode int, perUserLimiter *ratelimit.PerKeyLimiter) *AgentAdmission {
	if capPerNode < 1 {
		capPerNode = 1
	}
	return &AgentAdmission{
		capPerNode:  capPerNode,
		userLimiter: perUserLimiter,
		nodes:       make(map[string]*nodeState),
	}
}

// getOrCreateNode returns the nodeState for nodeID, creating it lazily.
// Caller must hold aa.mu.
func (aa *AgentAdmission) getOrCreateNode(nodeID string) *nodeState {
	ns, ok := aa.nodes[nodeID]
	if !ok {
		ns = &nodeState{}
		aa.nodes[nodeID] = ns
	}
	return ns
}

// Admit attempts to admit agent a onto nodeID.
//
// Decision logic (in order):
//  1. Per-user rate check via PerKeyLimiter.Allow(a.UserID).  If the user's
//     bucket is empty → (false, "rate limited: <userID>").
//  2. Per-node capacity check.  If live count < cap → admit and increment live
//     count → (true, "admitted").
//  3. Otherwise → enqueue the agent ID → (false, "queued: cap reached").
func (aa *AgentAdmission) Admit(nodeID string, a Agent) (admitted bool, reason string) {
	// Rate-limit check is stateful and must be outside the node lock to avoid
	// holding aa.mu while the limiter does its own locking.  We acquire aa.mu
	// only for the node accounting step.
	if !aa.userLimiter.Allow(a.UserID) {
		return false, fmt.Sprintf("rate limited: %s", a.UserID)
	}

	aa.mu.Lock()
	defer aa.mu.Unlock()

	ns := aa.getOrCreateNode(nodeID)
	if ns.live < aa.capPerNode {
		ns.live++
		return true, "admitted"
	}

	ns.queue = append(ns.queue, a.ID)
	return false, "queued: cap reached"
}

// Complete marks agent agentID on nodeID as finished, decrementing the live
// count.  If there are queued agents waiting for a slot the oldest queued agent
// ID is removed from the queue (the caller is responsible for re-admitting
// that agent; Complete only frees the slot, it does not call Admit).
//
// Complete is idempotent with respect to the live counter — it will not
// decrement below zero.
func (aa *AgentAdmission) Complete(nodeID, agentID string) {
	aa.mu.Lock()
	defer aa.mu.Unlock()

	ns := aa.getOrCreateNode(nodeID)
	if ns.live > 0 {
		ns.live--
	}

	// Pop the oldest queued agent (the caller re-admits it independently).
	if len(ns.queue) > 0 {
		ns.queue = ns.queue[1:]
	}
	_ = agentID // parameter kept for observability / future matching
}

// LiveCount returns the number of currently running agents on nodeID.
func (aa *AgentAdmission) LiveCount(nodeID string) int {
	aa.mu.Lock()
	defer aa.mu.Unlock()
	ns, ok := aa.nodes[nodeID]
	if !ok {
		return 0
	}
	return ns.live
}

// QueueLen returns the number of agents queued (waiting for a slot) on nodeID.
func (aa *AgentAdmission) QueueLen(nodeID string) int {
	aa.mu.Lock()
	defer aa.mu.Unlock()
	ns, ok := aa.nodes[nodeID]
	if !ok {
		return 0
	}
	return len(ns.queue)
}
