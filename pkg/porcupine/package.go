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

import (
	"sort"
	"strconv"
	"strings"
)

// Model is the sequential specification the history is checked against.
//
// Init returns the starting state. Step applies one operation: given the
// current state and the operation's input and the output the real system
// actually returned, it reports whether that (input,output) transition is legal
// under the spec, and if so the resulting new state. Equal compares two states
// for equivalence; it is part of the model contract and powers the search's
// state-dedup memoization (see CheckOperationsVerbose). When the search reaches
// a configuration whose already-linearized operation set AND model state (per
// Equal) match one previously proven to be a dead end, the subtree is pruned
// instead of re-explored. Leaving Equal nil disables ONLY this pruning; it
// never changes the PASS/FAIL verdict, only how much of the search tree is
// walked. States MUST be treated as immutable by Step: it must not mutate its
// argument, it must return a fresh state, because the backtracking search keeps
// older states alive.
type Model struct {
	// Init returns the initial sequential state.
	Init func() interface{}
	// Step applies input/output to state, reporting legality + the next state.
	// It MUST NOT mutate the passed-in state.
	Step func(state, input, output interface{}) (ok bool, newState interface{})
	// Equal reports whether two states are equivalent. Part of the model
	// contract; it powers the search's state-dedup memoization. It may be nil,
	// in which case memoization is disabled with no effect on the PASS/FAIL
	// verdict (only on search effort).
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
	// memoHits counts how many times the state-dedup memoization pruned a
	// subtree (a configuration matching a previously-proven dead end). It is
	// unexported because it is an internal search-effort diagnostic, not part
	// of the verdict; in-package tests assert it to prove memoization fires.
	memoHits int
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

	// State-dedup memoization (HXC-913). A search configuration is fully
	// characterized by (set of already-linearized ops, model state): the set of
	// remaining ops and the real-time eligibility derived from it are identical
	// for any path that arrives at the same used-set, so if such a configuration
	// was already PROVEN to be a dead end (search returned false from it), an
	// equivalent revisit cannot succeed either and is pruned.
	//
	// The key is the used-set encoded as a bitmask. Because two distinct paths
	// may reach the same used-set with NON-equivalent states (the spec is not a
	// pure function of the op-set), each bucket holds the list of dead-end
	// states seen for that op-set; a revisit prunes only when Equal matches one
	// of them. We record a dead end ONLY after its subtree fully failed, so the
	// memo can never prune a branch that could have succeeded — correctness
	// (the PASS/FAIL verdict) is preserved exactly. Disabled when Equal is nil.
	memoEnabled := model.Equal != nil
	visited := make(map[string][]interface{})
	memoHits := 0

	// usedKey encodes the current used-set as a compact, allocation-light string
	// bitmask key. For n<=64 a single uint64 in base-36 suffices; larger n falls
	// back to a multi-word encoding. The encoding is injective per op-set.
	usedKey := func() string {
		if n <= 64 {
			var mask uint64
			for i := 0; i < n; i++ {
				if used[i] {
					mask |= 1 << uint(i)
				}
			}
			return strconv.FormatUint(mask, 36)
		}
		var b strings.Builder
		var word uint64
		for i := 0; i < n; i++ {
			bit := uint(i % 64)
			if used[i] {
				word |= 1 << bit
			}
			if bit == 63 || i == n-1 {
				b.WriteString(strconv.FormatUint(word, 36))
				b.WriteByte('.')
				word = 0
			}
		}
		return b.String()
	}

	var search func(state interface{}, depth int) bool
	search = func(state interface{}, depth int) bool {
		if depth == n {
			return true // all operations linearized => linearizable.
		}

		// Memo probe: if this (used-set, state) was already proven a dead end,
		// prune. Keyed before exploring children so the whole subtree is skipped.
		var key string
		if memoEnabled {
			key = usedKey()
			for _, s := range visited[key] {
				if model.Equal(s, state) {
					memoHits++
					return false
				}
			}
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

		// Subtree from (used-set, state) exhausted without success: it is a
		// proven dead end. Record it so an equivalent revisit is pruned. The
		// used-set is unchanged here (all children restored used[i]=false), so
		// `key` still identifies this configuration.
		if memoEnabled {
			visited[key] = append(visited[key], state)
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
		return CheckResult{OK: true, memoHits: memoHits}
	}
	res := CheckResult{OK: false, memoHits: memoHits}
	if deepestStuck >= 0 {
		res.Offending = ops[deepestStuck]
	}
	return res
}
