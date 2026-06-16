package dstworkload

import "time"

// This file exposes unexported workload internals to the external
// (dstworkload_test) adversarial suite. It is a _test.go file and is therefore
// NOT part of the production build — production code remains byte-identical.

// AssignmentsSnapshot returns a copy of the task->node assignment map produced
// during Execution.
func (w *FoundationDBWorkload) AssignmentsSnapshot() map[string]string {
	out := make(map[string]string, len(w.assignments))
	for k, v := range w.assignments {
		out[k] = v
	}
	return out
}

// LatenciesSnapshot returns a copy of the virtual-clock latency slice recorded
// during Execution, in recording order.
func (w *FoundationDBWorkload) LatenciesSnapshot() []time.Duration {
	out := make([]time.Duration, len(w.latencies))
	copy(out, w.latencies)
	return out
}
