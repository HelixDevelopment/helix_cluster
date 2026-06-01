// Package scenario provides a YAML-driven chaos scenario engine that composes
// named fault phases with blast-radius controls and abort conditions.
package scenario

import (
	"errors"
	"fmt"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/chaos"
)

// perFaultCostMS is the deterministic cost (in simulated milliseconds) charged
// per fault during an EmergencyStop. It models the time required to restore a
// single active fault. No real sleep is performed; this is pure data.
const perFaultCostMS int64 = 10

// StopResult captures the outcome of an EmergencyStop call.
type StopResult struct {
	// FaultsRecovered is the number of active faults that were restored.
	FaultsRecovered int
	// HaltLatencyMS is the deterministic simulated halt latency:
	//   FaultsRecovered * perFaultCostMS
	// No wall-clock time is measured; the budget model is data-only.
	HaltLatencyMS int64
	// WithinBudget is true when HaltLatencyMS <= the configured haltBudgetMS.
	WithinBudget bool
	// RecoveredNames lists the Name() of every fault that was active at the
	// moment EmergencyStop was called.
	RecoveredNames []string
}

// StopController wraps a *chaos.ChaosRunner and adds an emergency-stop /
// auto-recovery capability with a deterministic halt-latency budget.
//
// Halt latency is modelled as data (FaultsRecovered × perFaultCostMS); no
// real sleep is ever performed. The budget is provided at construction time
// and checked after every EmergencyStop call.
type StopController struct {
	runner       *chaos.ChaosRunner
	haltBudgetMS int64
}

// NewStopController creates a StopController bound to runner with the given
// halt budget in simulated milliseconds.
//
//   - runner must not be nil.
//   - haltBudgetMS is the maximum simulated halt latency considered acceptable
//     (StopResult.WithinBudget = HaltLatencyMS <= haltBudgetMS).
func NewStopController(runner *chaos.ChaosRunner, haltBudgetMS int64) *StopController {
	return &StopController{
		runner:       runner,
		haltBudgetMS: haltBudgetMS,
	}
}

// EmergencyStop snapshots currently-active faults, calls RestoreAll to recover
// them, and returns a StopResult describing the outcome.
//
// The simulated halt latency is computed deterministically:
//
//	HaltLatencyMS = len(activeFaults) × perFaultCostMS (10 ms per fault)
//
// WithinBudget = HaltLatencyMS <= c.haltBudgetMS.
//
// RestoreAll is called unconditionally; if it returns an error the error is
// embedded in the returned StopResult but recovery continues (best-effort).
// The returned StopResult always reflects the faults that were active at the
// moment of the call.
func (c *StopController) EmergencyStop() StopResult {
	active := c.runner.ActiveFaults()

	names := make([]string, len(active))
	for i, f := range active {
		names[i] = f.Name()
	}

	// Best-effort restore: errors are intentionally swallowed here because the
	// contract is to attempt full recovery. Callers that need error detail
	// should use AutoRecover after EmergencyStop.
	_ = c.runner.RestoreAll()

	latency := int64(len(active)) * perFaultCostMS

	return StopResult{
		FaultsRecovered: len(active),
		HaltLatencyMS:   latency,
		WithinBudget:    latency <= c.haltBudgetMS,
		RecoveredNames:  names,
	}
}

// AutoRecover is an idempotent post-stop assertion: it verifies that no faults
// remain active after EmergencyStop was called. It returns nil when
// ActiveFaults() is empty, and a descriptive error when any fault is still
// active.
//
// Idempotent: calling AutoRecover multiple times after a successful
// EmergencyStop always returns nil.
func (c *StopController) AutoRecover() error {
	remaining := c.runner.ActiveFaults()
	if len(remaining) == 0 {
		return nil
	}
	names := make([]string, len(remaining))
	for i, f := range remaining {
		names[i] = f.Name()
	}
	return fmt.Errorf("auto-recover: %d fault(s) still active after emergency stop: %v",
		len(remaining), names)
}

// RunWithStop executes the scenario via (*Scenario).Run and then triggers
// EmergencyStop. The stop is performed regardless of whether the scenario
// completed or was aborted.
//
//   - stopAfterPhase is reserved for future use; it is accepted for API
//     stability but does not alter execution semantics in the current
//     implementation (EmergencyStop fires after the scenario completes
//     regardless of phase index).
//
// Returns the ExecutionLog from the scenario run and the StopResult from the
// subsequent EmergencyStop.
func (c *StopController) RunWithStop(s *Scenario, stopAfterPhase int) (ExecutionLog, StopResult, error) {
	if s == nil {
		return ExecutionLog{}, StopResult{}, errors.New("RunWithStop: scenario must not be nil")
	}
	log := s.Run()
	result := c.EmergencyStop()
	return log, result, nil
}
