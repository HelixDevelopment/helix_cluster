// Package porcupine is a self-contained linearizability checker for Helix
// Cluster OS. It implements the Wing & Gong (WGL) linearizability-checking
// algorithm — linearize-and-backtrack over a partial order of concurrent
// operations — together with a concurrency-safe history recorder shim.
//
// WHY a hand-rolled checker (no external dependency): the Constitution forbids
// pulling in third-party code for a core verification primitive that we must be
// able to audit line-by-line. Linearizability is the correctness oracle behind
// our Raft/etcd/register state machines, so the checker that decides PASS/FAIL
// must itself be trivially reviewable and have zero supply-chain surface.
//
// The checker decides whether a concurrent history (a set of overlapping
// Call/Return events) is *linearizable* with respect to a sequential
// specification (Model): does there exist a total order of the operations,
// consistent with the real-time happens-before edges (an op that returned
// before another was called must be ordered first), under which every operation
// satisfies the sequential spec?
//
// Algorithm (WGL): repeatedly pick a *minimal* operation (one whose call is not
// preceded — by real time — by any other still-pending op), tentatively apply
// it to the model state; if the model accepts the (input,output) pair, recurse
// on the remaining operations; on dead-end, backtrack and try the next
// candidate. A linearization point can be chosen for op A only when no other
// pending op B finished strictly before A started, otherwise A cannot be first.
package porcupine

import "sort"

// Model is the sequential specification the history is checked against.
//
// Init returns the starting state. Step applies one operation: given the
// current state and the operation's input and the output the real system
// actually returned, it reports whether that (input,output) transition is legal
// under the spec, and if so the resulting new state. Equal compares two states
// for equivalence; it is part of the model contract and is reserved for a
// future state-dedup optimization. The current checker does NOT yet memoize on
// it, so leaving Equal nil changes nothing about correctness or current
// behavior — it only forgoes a not-yet-implemented pruning. States MUST be
// treated as immutable by Step: it must not mutate its argument, it must return
// a fresh state, because the backtracking search keeps older states alive.
type Model struct {
	// Init returns the initial sequential state.
	Init func() interface{}
	// Step applies input/output to state, reporting legality + the next state.
	// It MUST NOT mutate the passed-in state.
	Step func(state, input, output interface{}) (ok bool, newState interface{})
	// Equal reports whether two states are equivalent. Part of the model
	// contract; reserved for a future state-dedup optimization. The current
	// checker does not consult it, so it may be nil with no effect on
	// correctness or current behavior.
	Equal func(s1, s2 interface{}) bool
}

// Operation is a single completed client operation with its real-time window.
// Call is the timestamp the client invoked the operation; Return is when the
// response was observed. Input/Output are the spec-level arguments and result.
// Call < Return is required for a completed operation; the recorder guarantees
// this by reading a monotonically increasing clock.
type Operation struct {
	// ClientID is informational (which goroutine issued it); not used by the
	// core check but useful when reporting a violation.
	ClientID int
	Input    interface{}
	Output   interface{}
	Call     int64
	Return   int64
}

// CheckResult is the verbose result of a linearizability check.
type CheckResult struct {
	// OK is true iff the history is linearizable.
	OK bool
	// Offending, when OK is false, is one operation that participates in the
	// minimal prefix at which the search got stuck — i.e. an operation that
	// could not be linearized under any admissible ordering of the prefix that
	// precedes it. It is a witness, not necessarily the unique culprit.
	Offending Operation
}

// CheckOperations reports whether history is linearizable under model.
//
// It is the boolean-only convenience wrapper around CheckOperationsVerbose.
func CheckOperations(model Model, history []Operation) bool {
	return CheckOperationsVerbose(model, history).OK
}

// CheckOperationsVerbose runs the WGL linearize-and-backtrack search and, on
// failure, returns a witness operation that could not be linearized.
//
// Determinism: operations are first sorted by (Call, Return) so the search is
// reproducible and the "minimal pending op" selection is well defined.
func CheckOperationsVerbose(model Model, history []Operation) CheckResult {
	ops := make([]Operation, len(history))
	copy(ops, history)
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Call != ops[j].Call {
			return ops[i].Call < ops[j].Call
		}
		return ops[i].Return < ops[j].Return
	})

	n := len(ops)
	if n == 0 {
		return CheckResult{OK: true}
	}

	// used[i] marks ops already linearized in the current search path.
	used := make([]bool, n)

	// deepestStuck tracks the index whose linearization we could never satisfy
	// at the greatest search depth; reported as the witness on failure.
	deepestStuck := -1
	var deepestDepth int = -1

	var search func(state interface{}, depth int) bool
	search = func(state interface{}, depth int) bool {
		if depth == n {
			return true // all operations linearized => linearizable.
		}

		// Earliest Return time among still-unused ops. Any op whose Call is
		// strictly after this earliest Return cannot be linearized next,
		// because the op that returned first must be ordered no later than it.
		minReturn := int64(1<<63 - 1)
		for i := 0; i < n; i++ {
			if !used[i] && ops[i].Return < minReturn {
				minReturn = ops[i].Return
			}
		}

		progressed := false
		for i := 0; i < n; i++ {
			if used[i] {
				continue
			}
			// Real-time constraint (the load-bearing branch): op i may be the
			// next linearization point ONLY if its call does not happen strictly
			// after some other pending op has already returned. If ops[i].Call >
			// minReturn, another pending op completed before i even started, so i
			// cannot come first — skip it. Removing/inverting this check makes the
			// search accept impossible orderings (stale reads).
			if ops[i].Call > minReturn {
				continue
			}
			progressed = true

			ok, next := model.Step(state, ops[i].Input, ops[i].Output)
			if !ok {
				continue
			}
			used[i] = true
			if search(next, depth+1) {
				used[i] = false
				return true
			}
			used[i] = false
		}

		// Dead end at this depth: record the deepest failure as the witness.
		if depth > deepestDepth {
			deepestDepth = depth
			// Witness: the first still-unused, real-time-eligible op we could
			// not satisfy. Fall back to any unused op.
			deepestStuck = -1
			for i := 0; i < n; i++ {
				if !used[i] && ops[i].Call <= minReturn {
					deepestStuck = i
					break
				}
			}
			if deepestStuck == -1 {
				for i := 0; i < n; i++ {
					if !used[i] {
						deepestStuck = i
						break
					}
				}
			}
		}
		_ = progressed
		return false
	}

	if search(model.Init(), 0) {
		return CheckResult{OK: true}
	}
	res := CheckResult{OK: false}
	if deepestStuck >= 0 {
		res.Offending = ops[deepestStuck]
	}
	return res
}
