// Package modelrouter resolves the sentinel model name "default" to a concrete
// model id based on a routing strategy, and produces outbound JSON request
// bodies that carry sink-side evidence (the resolved model id and the runID).
package modelrouter

import (
	"encoding/json"
	"errors"
	"fmt"
)

// SentinelDefault is the literal string callers pass when they want the router
// to substitute the strategy-specific concrete model id.
const SentinelDefault = "default"

// Strategy names the four supported routing policies.
type Strategy string

const (
	StrategyLatency    Strategy = "latency"
	StrategyThroughput Strategy = "throughput"
	StrategyQuality    Strategy = "quality"
	StrategyCost       Strategy = "cost"
)

// ErrUnknownStrategy is returned by Resolve and BuildRequestBody when the
// caller supplies a Strategy value that is not in the router's table.
type ErrUnknownStrategy struct {
	Given Strategy
}

func (e *ErrUnknownStrategy) Error() string {
	return fmt.Sprintf("modelrouter: unknown strategy %q", e.Given)
}

// defaultModels maps each strategy to its concrete model id.
//
// The four ids MUST remain distinct.  A swap between any two entries makes the
// per-strategy assertion in TestBuildRequestBody_AllStrategies fail immediately,
// because each decoded "model" field will no longer equal the expected constant.
//
// MUTATION GUARD — TestBuildRequestBody_AllStrategies pins this table:
// swapping any two values here causes that test to fail.
var defaultModels = map[Strategy]string{
	StrategyLatency:    "helix-fast-v2",    // latency: optimised for low p50
	StrategyThroughput: "helix-bulk-v3",    // throughput: high tokens/s
	StrategyQuality:    "helix-precise-v4", // quality: highest accuracy
	StrategyCost:       "helix-nano-v1",    // cost: cheapest per token
}

// Router resolves model names and builds outbound request bodies.
type Router struct {
	// table is a snapshot of defaultModels taken at construction time so the
	// zero-value Router (no table) returns ErrUnknownStrategy rather than
	// silently producing empty strings.
	table map[Strategy]string
}

// New returns a Router initialised from the package-level defaultModels table.
func New() *Router {
	t := make(map[Strategy]string, len(defaultModels))
	for k, v := range defaultModels {
		t[k] = v
	}
	return &Router{table: t}
}

// Resolve maps (strategy, requestedModel) to a concrete model id.
//
//   - If requestedModel == SentinelDefault the strategy's concrete id is
//     returned.
//   - Otherwise requestedModel is returned unchanged (pass-through).
//   - If strategy is not in the router's table, ErrUnknownStrategy is returned.
func (r *Router) Resolve(strategy Strategy, requestedModel string) (string, error) {
	concrete, ok := r.table[strategy]
	if !ok {
		return "", &ErrUnknownStrategy{Given: strategy}
	}
	if requestedModel == SentinelDefault {
		return concrete, nil
	}
	return requestedModel, nil
}

// RequestBody is the outbound JSON payload.  The model field carries the
// RESOLVED id (sink-side evidence); runID allows end-to-end correlation.
type RequestBody struct {
	Model string `json:"model"`
	RunID string `json:"run_id"`
}

// BuildRequestBody resolves the model name and serialises the outbound request.
// It returns a typed error for an unknown strategy; all other errors come from
// the JSON encoder (practically impossible for this simple struct).
func (r *Router) BuildRequestBody(strategy Strategy, requestedModel, runID string) ([]byte, error) {
	resolved, err := r.Resolve(strategy, requestedModel)
	if err != nil {
		return nil, err
	}
	body := RequestBody{
		Model: resolved,
		RunID: runID,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("modelrouter: marshal: %w", err)
	}
	return data, nil
}

// StrategyDefault returns the concrete model id registered for strategy, or
// ErrUnknownStrategy if strategy is unknown.  This is a convenience accessor
// for callers that need the bare default without building a full request body.
func (r *Router) StrategyDefault(strategy Strategy) (string, error) {
	concrete, ok := r.table[strategy]
	if !ok {
		return "", &ErrUnknownStrategy{Given: strategy}
	}
	return concrete, nil
}

// IsUnknownStrategy reports whether err is (or wraps) ErrUnknownStrategy.
func IsUnknownStrategy(err error) bool {
	var target *ErrUnknownStrategy
	return errors.As(err, &target)
}
