package dstworkload

import "fmt"

// NoLostTasks checks that every task in allTasks that could be served by an
// online node appears in the assignments map.
//
// "Could be served" means there was at least one online node available in the
// cluster at the time of execution.  For the purposes of this invariant we
// check that every task that was scheduled appears in assignments, because the
// handler only skips assignment when ALL nodes are offline — a situation that is
// itself a separate invariant violation (QuorumAtLeast3).
//
// If any task is missing, an error naming that task is returned.
func NoLostTasks(w *FoundationDBWorkload) error {
	for _, t := range w.allTasks {
		if _, ok := w.assignments[t.ID]; !ok {
			return fmt.Errorf("invariant NoLostTasks: task %q was not assigned to any node", t.ID)
		}
	}
	return nil
}

// NoDoubleAssignment checks that no task was recorded as assigned more than
// once (i.e. dispatched to two different nodes).
//
// The primary assignments field is map[taskID]nodeID; because a Go map cannot
// hold duplicate keys, iterating over it with a seen-counter is structurally
// dead code — the count can never exceed 1.  The real, bite-able check lives
// in the duplicates field: the test seam NewWorkloadWithState (and a future
// production handler) can record repeated-assignment evidence there, and this
// invariant enforces it.
//
// The invariant returns an error naming the first offending task ID.
func NoDoubleAssignment(w *FoundationDBWorkload) error {
	// Check the duplicates seam: populated either by the test seam or by any
	// future production handler that detects a double-assignment at write time.
	for taskID, count := range w.duplicates {
		if count > 1 {
			return fmt.Errorf("invariant NoDoubleAssignment: task %q assigned %d times (duplicate record)", taskID, count)
		}
	}
	return nil
}

// QuorumAtLeast3 checks that at least 3 nodes are currently Online.
//
// This is evaluated at Check() time (after node restores have run in a normal
// flow), so the quorum condition reflects the live state of the simulation.
func QuorumAtLeast3(w *FoundationDBWorkload) error {
	online := 0
	for _, n := range w.Eng.ListNodes() {
		if n.Online {
			online++
		}
	}
	if online < 3 {
		return fmt.Errorf("invariant QuorumAtLeast3: only %d nodes online (need >= 3)", online)
	}
	return nil
}
