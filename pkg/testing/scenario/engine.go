package scenario

import (
	"fmt"
)

// PhaseResult captures the outcome of a single executed phase.
type PhaseResult struct {
	// Name is the phase name from the scenario definition.
	Name string
	// FaultsInjected contains the fault names that were actually injected
	// (after blast-radius clamping).
	FaultsInjected []string
	// BlastRadiusExceeded is true when the phase's fault list was clamped
	// either by Phase.MaxFaults or by the global Scenario.MaxAffected budget.
	BlastRadiusExceeded bool
	// Aborted is true when this phase triggered an AbortCondition and caused
	// the scenario to halt early.
	Aborted bool
}

// ExecutionLog is the full trace produced by Scenario.Run.
type ExecutionLog struct {
	ScenarioName string
	// Phases contains only the phases that were started (not any that were
	// skipped because an abort fired before reaching them).
	Phases      []PhaseResult
	Aborted     bool
	AbortReason string
	TotalFaults int
}

// Run executes the scenario deterministically (no wall-clock or sleeps;
// DurationMS is stored as data only). It processes phases in order:
//
//  1. Clamp the phase fault list to Phase.MaxFaults and to the remaining
//     global MaxAffected budget; set BlastRadiusExceeded when clamping occurs.
//  2. Record the injected fault names in PhaseResult.FaultsInjected.
//  3. After the phase, evaluate AbortOn against TotalFaults; halt if triggered.
func (s *Scenario) Run() ExecutionLog {
	log := ExecutionLog{
		ScenarioName: s.Name,
		Phases:       make([]PhaseResult, 0, len(s.Phases)),
	}

	totalFaults := 0

	for _, phase := range s.Phases {
		pr := PhaseResult{
			Name:           phase.Name,
			FaultsInjected: []string{},
		}

		// Determine how many faults may be injected in this phase.
		// The per-phase blast radius is Phase.MaxFaults; the global
		// budget is s.MaxAffected - totalFaults.
		globalBudget := s.MaxAffected - totalFaults
		if globalBudget < 0 {
			globalBudget = 0
		}

		allowedByPhase := phase.MaxFaults
		if allowedByPhase <= 0 {
			// 0 means uncapped at the phase level; use the full list length.
			allowedByPhase = len(phase.Faults)
		}

		limit := allowedByPhase
		if globalBudget < limit {
			limit = globalBudget
		}

		// Inject up to limit faults.
		for i, name := range phase.Faults {
			if i >= limit {
				break
			}
			pr.FaultsInjected = append(pr.FaultsInjected, name)
		}

		// Blast-radius flag: set when we could not inject all listed faults.
		if len(pr.FaultsInjected) < len(phase.Faults) {
			pr.BlastRadiusExceeded = true
		}

		totalFaults += len(pr.FaultsInjected)

		// Check the abort condition now that faults have been injected.
		if s.AbortOn != nil {
			if fires, reason := s.AbortOn.evaluate(totalFaults); fires {
				pr.Aborted = true
				log.Phases = append(log.Phases, pr)
				log.Aborted = true
				log.AbortReason = reason
				log.TotalFaults = totalFaults
				return log
			}
		}

		log.Phases = append(log.Phases, pr)
	}

	log.TotalFaults = totalFaults
	return log
}

// evaluate checks whether the abort condition fires given the current value of
// the counter identified by field. Returns (true, human-readable reason) when
// the condition is satisfied.
func (a *AbortCondition) evaluate(faultsInjected int) (bool, string) {
	var current int
	switch a.Field {
	case "faults_injected":
		current = faultsInjected
	default:
		// Unknown field: never fires, avoids false positives.
		return false, ""
	}

	var fires bool
	switch a.Op {
	case "==":
		fires = current == a.Value
	case ">":
		fires = current > a.Value
	case "<":
		fires = current < a.Value
	}

	if fires {
		return true, fmt.Sprintf("abort: %s %s %d (current=%d)", a.Field, a.Op, a.Value, current)
	}
	return false, ""
}
