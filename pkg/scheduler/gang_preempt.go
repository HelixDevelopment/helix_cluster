package scheduler

import (
	"fmt"
	"sort"
)

// GangResult is the outcome of an all-or-nothing gang scheduling attempt.
type GangResult struct {
	// Placements lists the node chosen for each gang task, in input order.
	Placements []GangPlacement
}

// GangPlacement records one task's chosen node within a gang.
type GangPlacement struct {
	JobID  string
	NodeID string
}

// PreemptResult is the outcome of a preemptive scheduling attempt.
type PreemptResult struct {
	// AssignedNode is the node the incoming job was placed on.
	AssignedNode string
	Status       JobStatus
	// Preempted lists the IDs of jobs evicted to make room (may be empty if
	// the job fit without eviction).
	Preempted []string
}

// ErrGangEmpty is returned when ScheduleGang is called with no tasks.
var ErrGangEmpty = fmt.Errorf("gang scheduling requires at least one task")

// ErrGangNotAllPlaced is returned when not every gang task can be placed
// atomically; no task is placed in this case (all-or-nothing).
var ErrGangNotAllPlaced = fmt.Errorf("gang scheduling failed: not all tasks could be placed atomically")

// ErrNoPreemptionCandidates is returned when preemption cannot free enough
// room because no running placement has strictly lower priority.
var ErrNoPreemptionCandidates = fmt.Errorf("preemption failed: no lower-priority victims provide enough room")

// RunningPlacements returns a snapshot copy of all currently running placements.
func (s *Scheduler) RunningPlacements() []*Placement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Placement, 0, len(s.placements))
	for _, p := range s.placements {
		cp := *p
		out = append(out, &cp)
	}
	return out
}

// IsRunning reports whether a job currently holds a running placement.
func (s *Scheduler) IsRunning(jobID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.placements[jobID]
	return ok
}

// ScheduleGang places a group of tasks all-or-nothing: either every task in
// jobs is placed atomically, or none are and node state is left unchanged.
//
// It reuses the existing plugin filter/score pipeline per task and respects
// the optimistic-concurrency version (bumped exactly once on success).
func (s *Scheduler) ScheduleGang(jobs []*Job) (*GangResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scheduleGangLocked(jobs)
}

// ScheduleGangOptimistic is ScheduleGang guarded by a version check.
func (s *Scheduler) ScheduleGangOptimistic(jobs []*Job, expectedVersion uint64) (*GangResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version != expectedVersion {
		return nil, fmt.Errorf("optimistic concurrency conflict: expected version %d, got %d", expectedVersion, s.version)
	}
	return s.scheduleGangLocked(jobs)
}

func (s *Scheduler) scheduleGangLocked(jobs []*Job) (*GangResult, error) {
	if len(jobs) == 0 {
		return nil, ErrGangEmpty
	}

	// Work on shadow copies of the nodes so that a failure leaves the real
	// node state completely untouched (no partial placement).
	shadow := make(map[string]*Node, len(s.nodes))
	for id, n := range s.nodes {
		shadow[id] = copyNode(n)
	}

	planned := make([]GangPlacement, 0, len(jobs))
	for _, job := range jobs {
		bestNode := s.bestNodeAmong(job, shadow)
		if bestNode == nil {
			// Atomic failure: discard the whole plan, real state untouched.
			return nil, ErrGangNotAllPlaced
		}
		// Bind against the shadow node so subsequent tasks see consumption.
		if !s.runBindLocked(job, bestNode) {
			return nil, ErrGangNotAllPlaced
		}
		planned = append(planned, GangPlacement{JobID: job.ID, NodeID: bestNode.ID})
	}

	// All tasks planned successfully: commit shadow node state and record
	// placements. This is the single atomic mutation point.
	for id, n := range shadow {
		s.nodes[id] = n
	}
	for i, job := range jobs {
		job.Status = JobStatusScheduled
		s.placements[job.ID] = &Placement{
			JobID:     job.ID,
			NodeID:    planned[i].NodeID,
			Priority:  job.Priority,
			Resources: job.Resources,
			job:       job,
		}
	}
	s.version++

	return &GangResult{Placements: planned}, nil
}

// bestNodeAmong runs the filter/score pipeline over the given node set and
// returns the highest-scoring node, or nil if none pass. Caller holds s.mu.
func (s *Scheduler) bestNodeAmong(job *Job, nodes map[string]*Node) *Node {
	var bestNode *Node
	bestScore := -1
	// Deterministic iteration for stable placement decisions.
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		node := nodes[id]
		if !s.runFiltersLocked(job, node) {
			continue
		}
		score := s.runScorersLocked(job, node)
		if score > bestScore {
			bestScore = score
			bestNode = node
		}
	}
	return bestNode
}

// SchedulePreempt schedules a job, evicting lower-priority running placements
// when necessary to make room. It first attempts a normal placement; if that
// fails, it evicts the minimum set of strictly-lower-priority victims (lowest
// priority first) until the job fits, then places it.
//
// Eviction never touches a job of equal or higher priority than the incoming
// job. If no combination of lower-priority victims frees enough room, the call
// fails and NO eviction is committed.
func (s *Scheduler) SchedulePreempt(job *Job) (*PreemptResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedulePreemptLocked(job)
}

// SchedulePreemptOptimistic is SchedulePreempt guarded by a version check.
func (s *Scheduler) SchedulePreemptOptimistic(job *Job, expectedVersion uint64) (*PreemptResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version != expectedVersion {
		return nil, fmt.Errorf("optimistic concurrency conflict: expected version %d, got %d", expectedVersion, s.version)
	}
	return s.schedulePreemptLocked(job)
}

func (s *Scheduler) schedulePreemptLocked(job *Job) (*PreemptResult, error) {
	// Fast path: try a normal placement with no eviction.
	if bestNode := s.bestNodeAmong(job, s.nodes); bestNode != nil {
		if s.runBindLocked(job, bestNode) {
			s.version++
			job.Status = JobStatusScheduled
			s.recordPlacementLocked(job, bestNode)
			return &PreemptResult{
				AssignedNode: bestNode.ID,
				Status:       JobStatusScheduled,
				Preempted:    nil,
			}, nil
		}
	}

	// Slow path: attempt eviction on a per-node basis. Choose the node where
	// the cheapest (fewest/lowest-priority) set of victims makes the job fit.
	type plan struct {
		nodeID  string
		victims []*Placement
	}
	var best *plan

	// Candidate victims grouped by node, only those strictly lower priority.
	victimsByNode := map[string][]*Placement{}
	for _, p := range s.placements {
		if p.Priority < job.Priority {
			victimsByNode[p.NodeID] = append(victimsByNode[p.NodeID], p)
		}
	}

	ids := make([]string, 0, len(s.nodes))
	for id := range s.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, nodeID := range ids {
		node := s.nodes[nodeID]
		cands := victimsByNode[nodeID]
		// Evict lowest priority first; FIFO/stable for equal priority.
		sort.SliceStable(cands, func(i, j int) bool {
			return cands[i].Priority < cands[j].Priority
		})

		// Simulate cumulative reclaim until the job fits (or victims exhausted).
		sim := copyNode(node)
		chosen := make([]*Placement, 0, len(cands))
		for _, v := range cands {
			if s.fitsAndLabelsOK(job, sim) {
				break
			}
			sim.AvailableResources.CPU += v.Resources.CPU
			sim.AvailableResources.Memory += v.Resources.Memory
			sim.AvailableResources.GPU += v.Resources.GPU
			chosen = append(chosen, v)
		}
		if !s.fitsAndLabelsOK(job, sim) {
			continue // even evicting all lower-priority victims is not enough
		}
		// Prefer the plan that evicts the fewest victims.
		if best == nil || len(chosen) < len(best.victims) {
			best = &plan{nodeID: nodeID, victims: chosen}
		}
	}

	if best == nil {
		return nil, ErrNoPreemptionCandidates
	}

	// Commit: evict chosen victims (restore their resources), then bind job.
	node := s.nodes[best.nodeID]
	preempted := make([]string, 0, len(best.victims))
	for _, v := range best.victims {
		node.AvailableResources.CPU += v.Resources.CPU
		node.AvailableResources.Memory += v.Resources.Memory
		node.AvailableResources.GPU += v.Resources.GPU
		delete(s.placements, v.JobID)
		preempted = append(preempted, v.JobID)
		if v.job != nil {
			v.job.Status = JobStatusPending
		}
	}

	if !s.runBindLocked(job, node) {
		// Should not happen because we simulated fit; treat as failure but
		// note victims were already restored (resources are consistent).
		return nil, fmt.Errorf("preemption binding failed on node %s", node.ID)
	}
	s.version++
	job.Status = JobStatusScheduled
	s.recordPlacementLocked(job, node)

	return &PreemptResult{
		AssignedNode: node.ID,
		Status:       JobStatusScheduled,
		Preempted:    preempted,
	}, nil
}

// fitsAndLabelsOK reports whether the job passes every filter plugin on node.
// This honors the existing filter pipeline (resources + capability labels).
func (s *Scheduler) fitsAndLabelsOK(job *Job, node *Node) bool {
	return s.runFiltersLocked(job, node)
}
