// Package dstworkload implements a 4-phase FoundationDB-style task-assignment
// workload for Deterministic Simulation Testing (DST).
//
// Phases:
//
//	Setup     – register 5 nodes; require quorum >= 3 online.
//	Execution – apply deterministic chaos, schedule a task storm, run the engine.
//	Check     – run invariants and return the first violation.
//	Metrics   – compute throughput and P99 latency from observed data.
package dstworkload

import (
	"fmt"
	"sort"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/chaos"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

// Task is the unit of work assigned to cluster nodes.
type Task struct {
	ID string
}

// Metrics holds performance counters computed after Execution.
type Metrics struct {
	// ThroughputPerSec is TasksAssigned / executionWall.Seconds().
	ThroughputPerSec float64
	// LatencyP99 is the 99th-percentile per-task assignment latency.
	LatencyP99 time.Duration
	// TasksAssigned is the total number of tasks that were given a node.
	TasksAssigned int
}

// taskCount is the number of tasks in the task storm.
const taskCount = 20

// FoundationDBWorkload is the top-level workload object.
type FoundationDBWorkload struct {
	Eng *dst.Engine

	// nodes holds the IDs registered in Setup, in insertion order.
	nodes []string

	// assignments maps taskID -> nodeID; populated during Execution.
	// In normal execution each key appears exactly once.
	assignments map[string]string

	// duplicates is an optional map[taskID]->count used by the test seam
	// NewWorkloadWithState to inject double-assignment evidence that
	// NoDoubleAssignment can detect.  It is nil in production workloads.
	duplicates map[string]int

	// allTasks holds every task scheduled in Execution; used by invariants.
	allTasks []Task

	// latencies records the virtual-clock latency observed for each successful
	// assignment, used to compute P99.
	latencies []time.Duration

	// executionWall is the real (wall-clock) elapsed time of the Execution call,
	// used to compute ThroughputPerSec.
	executionWall time.Duration
}

// NewWorkload creates a FoundationDBWorkload backed by the given engine.
func NewWorkload(eng *dst.Engine) *FoundationDBWorkload {
	return &FoundationDBWorkload{
		Eng:         eng,
		assignments: make(map[string]string),
	}
}

// NewWorkloadWithState is an exported test seam that lets tests inject a
// pre-built assignments map and node list without going through Setup/Execution.
// Pass a non-nil duplicates map to simulate double-assignment evidence for the
// NoDoubleAssignment invariant bite test.
// It is not part of the production API surface.
func NewWorkloadWithState(
	eng *dst.Engine,
	nodes []string,
	assignments map[string]string,
	allTasks []Task,
	duplicates map[string]int,
) *FoundationDBWorkload {
	return &FoundationDBWorkload{
		Eng:         eng,
		nodes:       nodes,
		assignments: assignments,
		allTasks:    allTasks,
		duplicates:  duplicates,
	}
}

// Setup registers 5 nodes and verifies quorum.
//
// It returns an error if fewer than 3 nodes are online after registration.
func (w *FoundationDBWorkload) Setup() error {
	const numNodes = 5
	for i := 0; i < numNodes; i++ {
		id := fmt.Sprintf("node-%d", i)
		w.Eng.RegisterNode(id, fmt.Sprintf("10.0.0.%d:7777", i))
		w.nodes = append(w.nodes, id)
	}
	return w.waitForQuorum()
}

// waitForQuorum returns an error unless at least 3 nodes are currently Online.
func (w *FoundationDBWorkload) waitForQuorum() error {
	online := 0
	for _, n := range w.Eng.ListNodes() {
		if n.Online {
			online++
		}
	}
	if online < 3 {
		return fmt.Errorf("quorum not reached: only %d of %d nodes online (need >= 3)",
			online, len(w.Eng.ListNodes()))
	}
	return nil
}

// Execution runs the task storm under deterministic chaos injection.
//
// It uses chaos.NewSchedule(eng.Seed()) to generate a reproducible fault
// sequence, reflects each node-crash fault into the simulation by setting the
// targeted node's Online flag to false, schedules taskCount "task:assign"
// events, and then drives the engine.
func (w *FoundationDBWorkload) Execution() {
	start := time.Now()

	// 1. Build deterministic chaos schedule and apply node-crash faults to sim.
	sched := chaos.NewSchedule(w.Eng.Seed())
	// Run enough steps to get variety; each step may crash a node.
	injLog := sched.Run(10)
	w.applyInjectionLog(injLog)

	// 2. Build the task list up-front so we can track all_tasks.
	tasks := make([]Task, taskCount)
	for i := 0; i < taskCount; i++ {
		tasks[i] = Task{ID: fmt.Sprintf("task-%04d", i)}
	}
	w.allTasks = tasks

	// 3. Register the assignment handler.
	w.Eng.RegisterHandler("task:assign", func(e *dst.Event) {
		t, ok := e.Payload.(Task)
		if !ok {
			return
		}
		// Find the first online node in deterministic (sorted) order.
		for _, n := range w.Eng.ListNodes() {
			if n.Online {
				// Record the assignment and latency only on the first assignment
				// for this task (guarantees map[taskID]->nodeID is 1-to-1).
				if _, already := w.assignments[t.ID]; !already {
					w.assignments[t.ID] = n.ID
					// Latency is recorded as the virtual time of this event
					// (strictly monotone, 1 µs per task slot).
					lat := time.Duration(e.Timestamp)
					w.latencies = append(w.latencies, lat)
				}
				return
			}
		}
		// All nodes offline — task is not assignable; will be caught by NoLostTasks
		// if it was expected to be assigned.
	})

	// 4. Schedule task events at staggered virtual times (1 µs apart).
	const taskDelay = int64(1_000) // 1 µs in ns
	for i, t := range tasks {
		w.Eng.ScheduleEvent("task:assign", t, int64(i+1)*taskDelay)
	}

	// 5. Drive the engine — process all scheduled events.
	w.Eng.Run(taskCount + 100)

	w.executionWall = time.Since(start)
}

// applyInjectionLog scans the chaos injection log and sets nodes offline for
// every node-crash injection it finds.  This is the mechanism by which chaos
// "actually changes sim state" rather than merely being called for show.
//
// The mapping from chaos NodeID to simulation node uses modular indexing so
// that any node ID produced by the catalog (e.g. "n1") maps onto our 5 nodes.
func (w *FoundationDBWorkload) applyInjectionLog(log chaos.InjectionLog) {
	if len(w.nodes) == 0 {
		return
	}
	for _, inj := range log {
		if inj.FaultName == "node-crash" {
			// Deterministically pick which node to crash: use the step index mod
			// the number of registered nodes so repeated steps cycle through all
			// nodes rather than crashing the same one repeatedly.
			targetIdx := inj.Step % len(w.nodes)
			nodeID := w.nodes[targetIdx]
			if n, ok := w.Eng.GetNode(nodeID); ok {
				n.Online = false
			}
		}
	}
}

// Check runs all invariants and returns the first violation encountered.
func (w *FoundationDBWorkload) Check() error {
	if err := NoLostTasks(w); err != nil {
		return err
	}
	if err := NoDoubleAssignment(w); err != nil {
		return err
	}
	if err := QuorumAtLeast3(w); err != nil {
		return err
	}
	return nil
}

// Metrics computes and returns the workload performance metrics.
//
// It is safe to call only after Execution has run.
func (w *FoundationDBWorkload) Metrics() Metrics {
	assigned := len(w.assignments)

	throughput := 0.0
	wallSec := w.executionWall.Seconds()
	if wallSec > 0 && assigned > 0 {
		throughput = float64(assigned) / wallSec
	}

	return Metrics{
		ThroughputPerSec: throughput,
		LatencyP99:       percentile99(w.latencies),
		TasksAssigned:    assigned,
	}
}

// percentile99 returns the 99th-percentile value from a slice of durations.
// Returns 0 if the slice is empty.
func percentile99(latencies []time.Duration) time.Duration {
	if len(latencies) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// Standard nearest-rank formula (1-indexed).
	idx := int(float64(len(sorted)) * 0.99)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
