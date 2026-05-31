package session

import (
	"errors"
	"fmt"
)

// lifecycleAction enumerates the lifecycle operations whose transitions are
// guarded by the session finite state machine (FSM).
type lifecycleAction string

const (
	actionAttach    lifecycleAction = "attach"
	actionDetach    lifecycleAction = "detach"
	actionMigrate   lifecycleAction = "migrate"
	actionTerminate lifecycleAction = "terminate"
)

// InvalidTransitionError is returned when a lifecycle action is attempted from a
// state the FSM does not permit. It carries the offending from-state and action
// so callers (and tests) can assert precisely which guard fired, rather than
// pattern-matching error strings.
type InvalidTransitionError struct {
	From   SessionStatus
	Action lifecycleAction
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("invalid session transition: cannot %s while %s", e.Action, e.From)
}

// asInvalidTransition is a thin wrapper over errors.As used by tests.
func asInvalidTransition(err error, target **InvalidTransitionError) bool {
	return errors.As(err, target)
}

// allowedTransitions is the FSM definition: for each action it lists the source
// states from which the action is permitted. Any (state, action) pair not
// present here is an illegal transition and MUST be rejected.
//
// Rationale for the chosen edges:
//   - attach:    only a Running session can be attached to.
//   - detach:    a Running or Paused session can be detached (idempotent-ish;
//                a no-op detach from Paused is harmless and user-expected).
//   - migrate:   only a Running session may begin migration. A session already
//                Migrating, Terminated, Creating, Paused or Failed may not.
//   - terminate: nearly any non-terminal state may be terminated (Running,
//                Migrating, Paused, Creating, Failed) — but a Terminated
//                session may NOT be terminated again (double-terminate guard).
var allowedTransitions = map[lifecycleAction]map[SessionStatus]bool{
	actionAttach: {
		StatusRunning: true,
	},
	actionDetach: {
		StatusRunning: true,
		StatusPaused:  true,
	},
	actionMigrate: {
		StatusRunning: true,
	},
	actionTerminate: {
		StatusRunning:   true,
		StatusMigrating: true,
		StatusPaused:    true,
		StatusCreating:  true,
		StatusFailed:    true,
	},
}

// validateTransition returns nil if `action` is permitted from state `from`,
// otherwise an *InvalidTransitionError. This is the single source of truth for
// the lifecycle FSM and is invoked by every state-changing Manager operation.
func validateTransition(from SessionStatus, action lifecycleAction) error {
	if sources, ok := allowedTransitions[action]; ok && sources[from] {
		return nil
	}
	return &InvalidTransitionError{From: from, Action: action}
}
