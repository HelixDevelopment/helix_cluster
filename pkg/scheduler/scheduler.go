package scheduler

import (
	"fmt"
	"sort"
	"sync"
)

// Scheduler implements the Omega-model two-level scheduler with
// optimistic concurrency control.
type Scheduler struct {
	mu      sync.RWMutex
	queue   *Queue
	plugins []Plugin
	nodes   map[string]*Node
	// version increments on every successful state mutation.
	// Used for optimistic concurrency checks.
	version uint64
	// placements tracks every currently RUNNING placement (jobID -> placement).
	// It records which node a job consumed resources on, enabling priority
	// preemption to identify and evict victims and restore their resources.
	placements map[string]*Placement
}

// Placement is an immutable-ish record of a running job's binding to a node.
// It captures the exact resources consumed so they can be restored on eviction,
// and retains the *Job pointer so its Status can be flipped on preemption.
type Placement struct {
	JobID     string
	NodeID    string
	Priority  int
	Resources Resources
	job       *Job
}

// Queue returns the scheduler's internal job queue.
func (s *Scheduler) Queue() *Queue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queue
}

// NewScheduler creates a new in-memory scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{
		queue:      NewQueue(),
		plugins:    make([]Plugin, 0),
		nodes:      make(map[string]*Node),
		version:    1,
		placements: make(map[string]*Placement),
	}
}

// AddPlugin registers a scheduling plugin.
func (s *Scheduler) AddPlugin(p Plugin) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plugins = append(s.plugins, p)
}

// RegisterNode adds a node to the scheduler state.
func (s *Scheduler) RegisterNode(n *Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[n.ID] = n
}

// UnregisterNode removes a node from the scheduler state.
func (s *Scheduler) UnregisterNode(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodes, id)
}

// GetNode returns a copy of the node with the given ID.
func (s *Scheduler) GetNode(id string) (*Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	if !ok {
		return nil, false
	}
	return copyNode(n), true
}

// NodeCount returns the number of registered nodes.
func (s *Scheduler) NodeCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.nodes)
}

// Version returns the current state version.
func (s *Scheduler) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// Schedule evaluates all nodes through the plugin pipeline and
// binds the job to the best-scoring node.
func (s *Scheduler) Schedule(job *Job) (*ScheduleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidates := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		candidates = append(candidates, n)
	}
	// Deterministic iteration for stable placement (matches bestNodeAmong /
	// scheduleGangLocked): ranging s.nodes directly is map-iteration order, so
	// two nodes that tie on score get a RANDOMIZED placement per request. Sort by
	// node ID and select with strict `>` (first-max-wins) for a stable tiebreak.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	var bestNode *Node
	bestScore := -1

	for _, node := range candidates {
		if !s.runFilters(job, node) {
			continue
		}
		score := s.runScorers(job, node)
		if score > bestScore {
			bestScore = score
			bestNode = node
		}
	}

	if bestNode == nil {
		return nil, ErrNoNodeAvailable
	}

	if !s.runBind(job, bestNode) {
		return nil, fmt.Errorf("binding failed on node %s", bestNode.ID)
	}

	s.version++
	job.Status = JobStatusScheduled
	s.recordPlacementLocked(job, bestNode)

	return &ScheduleResult{
		AssignedNode: bestNode.ID,
		Status:       JobStatusScheduled,
	}, nil
}

// recordPlacementLocked stores a running placement for the job on the node.
// Caller must hold s.mu.
func (s *Scheduler) recordPlacementLocked(job *Job, node *Node) {
	s.placements[job.ID] = &Placement{
		JobID:     job.ID,
		NodeID:    node.ID,
		Priority:  job.Priority,
		Resources: job.Resources,
		job:       job,
	}
}

// ScheduleOptimistic attempts to schedule with a version check.
// If expectedVersion does not match the current version, it returns
// a conflict error so the caller can retry.
func (s *Scheduler) ScheduleOptimistic(job *Job, expectedVersion uint64) (*ScheduleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.version != expectedVersion {
		return nil, fmt.Errorf("optimistic concurrency conflict: expected version %d, got %d", expectedVersion, s.version)
	}

	// Reuse core logic inline to keep version locked.
	// Deterministic candidate order (see Schedule): ranging s.nodes directly
	// randomizes the placement of equal-score nodes per request.
	candidates := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		candidates = append(candidates, n)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	var bestNode *Node
	bestScore := -1

	for _, node := range candidates {
		if !s.runFiltersLocked(job, node) {
			continue
		}
		score := s.runScorersLocked(job, node)
		if score > bestScore {
			bestScore = score
			bestNode = node
		}
	}

	if bestNode == nil {
		return nil, ErrNoNodeAvailable
	}

	if !s.runBindLocked(job, bestNode) {
		return nil, fmt.Errorf("binding failed on node %s", bestNode.ID)
	}

	s.version++
	job.Status = JobStatusScheduled
	s.recordPlacementLocked(job, bestNode)

	return &ScheduleResult{
		AssignedNode: bestNode.ID,
		Status:       JobStatusScheduled,
	}, nil
}

func (s *Scheduler) runFilters(job *Job, node *Node) bool {
	for _, p := range s.plugins {
		if !p.Filter(job, node) {
			return false
		}
	}
	return true
}

func (s *Scheduler) runFiltersLocked(job *Job, node *Node) bool {
	for _, p := range s.plugins {
		if !p.Filter(job, node) {
			return false
		}
	}
	return true
}

func (s *Scheduler) runScorers(job *Job, node *Node) int {
	score := 0
	for _, p := range s.plugins {
		score += p.Score(job, node)
	}
	return score
}

func (s *Scheduler) runScorersLocked(job *Job, node *Node) int {
	score := 0
	for _, p := range s.plugins {
		score += p.Score(job, node)
	}
	return score
}

func (s *Scheduler) runBind(job *Job, node *Node) bool {
	return s.runBindLocked(job, node)
}

// runBindLocked commits the job onto node by running every plugin's Bind in
// sequence. Bind is a read-modify-write on node.AvailableResources (e.g.
// NodeResourcesFit debits capacity), so a plugin that succeeds AFTER an earlier
// one already debited, but BEFORE a later one fails, would leave the node
// partially debited for a placement that never happens — a permanent capacity
// LEAK (the version is not bumped and no Placement is recorded, so the lost
// resources can never be reclaimed). To keep Bind all-or-nothing, snapshot the
// node's resources up front and restore them on ANY plugin failure so a failed
// commit leaves node state byte-identical to before the attempt.
func (s *Scheduler) runBindLocked(job *Job, node *Node) bool {
	snapshot := node.AvailableResources
	for _, p := range s.plugins {
		if !p.Bind(job, node) {
			node.AvailableResources = snapshot // roll back partial debit
			return false
		}
	}
	return true
}

func copyNode(n *Node) *Node {
	labels := make(map[string]string, len(n.Labels))
	for k, v := range n.Labels {
		labels[k] = v
	}
	return &Node{
		ID: n.ID,
		AvailableResources: Resources{
			CPU:    n.AvailableResources.CPU,
			Memory: n.AvailableResources.Memory,
			GPU:    n.AvailableResources.GPU,
		},
		Labels: labels,
	}
}
