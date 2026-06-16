// Package llm provides LLM model management for Helix Cluster OS.
package llm

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Strategy names the routing policy used to select a model from the registered
// set. Each strategy optimises a different axis of the per-model Metrics.
type Strategy string

const (
	// StrategyLatency selects the model with the lowest per-token latency.
	StrategyLatency Strategy = "latency"
	// StrategyThroughput selects the model with the highest tokens/second.
	StrategyThroughput Strategy = "throughput"
	// StrategyQuality selects the model with the highest quality score.
	StrategyQuality Strategy = "quality"
	// StrategyCost selects the cheapest model (lowest cost per 1K tokens).
	StrategyCost Strategy = "cost"
)

// Metrics holds the routing-relevant characteristics of a model. These are the
// real inputs the router compares; a model with zero/default Metrics is still
// eligible but will rank last on every axis that prefers a higher value.
type Metrics struct {
	// LatencyMS is the typical per-request latency in milliseconds (lower is
	// better for StrategyLatency).
	LatencyMS float64
	// ThroughputTPS is the sustained generation rate in tokens/second (higher
	// is better for StrategyThroughput).
	ThroughputTPS float64
	// Quality is an opaque accuracy/quality score in [0,1] (higher is better
	// for StrategyQuality).
	Quality float64
	// CostPer1K is the price per 1000 tokens (lower is better for
	// StrategyCost).
	CostPer1K float64
}

// Backend is the injectable inference provider seam. A real backend actually
// produces tokens for the selected model; tests inject an in-process backend
// that records which model it was asked to run, so selection can be asserted
// sink-side. This replaces the former hardcoded synthetic stub string: the
// Manager never fabricates a response itself.
type Backend interface {
	// Generate runs inference for the named (already selected) model. It must
	// return a real, model-derived result or a non-nil error; it must never be
	// asked to run a model the Manager did not select.
	Generate(model, prompt string) (string, error)
}

// ErrNoEligibleModel is returned by Route/RouteInference when no registered
// model satisfies the eligibility predicate (registered, loaded, healthy) for
// the requested strategy. It is a typed error so callers can distinguish "no
// model available" from a backend or input failure rather than receiving a
// fabricated response.
type ErrNoEligibleModel struct {
	Strategy Strategy
}

func (e *ErrNoEligibleModel) Error() string {
	return fmt.Sprintf("llm: no eligible healthy model for strategy %q", e.Strategy)
}

// ErrUnknownStrategy is returned when an unrecognised Strategy value is passed
// to the router. A silent fallback to an arbitrary strategy would be a
// usability lie (the caller's optimisation axis would be ignored).
type ErrUnknownStrategy struct {
	Strategy Strategy
}

func (e *ErrUnknownStrategy) Error() string {
	return fmt.Sprintf("llm: unknown routing strategy %q", e.Strategy)
}

// IsNoEligibleModel reports whether err is (or wraps) ErrNoEligibleModel.
func IsNoEligibleModel(err error) bool {
	var t *ErrNoEligibleModel
	return errors.As(err, &t)
}

// IsUnknownStrategy reports whether err is (or wraps) ErrUnknownStrategy.
func IsUnknownStrategy(err error) bool {
	var t *ErrUnknownStrategy
	return errors.As(err, &t)
}

// Model represents a registered LLM model.
type Model struct {
	Name      string
	Path      string
	Format    string
	LoadedAt  *time.Time
	LoadCount int
	// Metrics drive routing decisions (see Strategy). Set via SetMetrics.
	Metrics Metrics
	// Healthy reports whether the model is currently serviceable. An unhealthy
	// model is excluded from routing even if loaded. Defaults to true on
	// registration so a freshly registered+loaded model is routable.
	Healthy bool
}

// Manager manages LLM models and routes inference to a backend.
type Manager struct {
	mu     sync.RWMutex
	models map[string]*Model
	// backend is the injectable inference provider. When nil, Inference and
	// RouteInference fail with a typed error instead of fabricating output.
	backend Backend
}

// NewManager creates a new LLM manager with no backend wired. Callers MUST
// install a real Backend via WithBackend/SetBackend before requesting
// inference; otherwise inference fails honestly rather than returning a stub.
func NewManager() *Manager {
	return &Manager{models: make(map[string]*Model)}
}

// WithBackend returns a Manager whose inference is served by b. It is a
// convenience constructor for the common "register models, then route" flow.
func WithBackend(b Backend) *Manager {
	return &Manager{models: make(map[string]*Model), backend: b}
}

// SetBackend installs (or replaces) the inference backend.
func (m *Manager) SetBackend(b Backend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backend = b
}

// RegisterModel registers a model. Re-registering an existing name updates its
// path and format but preserves runtime load state (LoadedAt/LoadCount): a model
// that is currently loaded must not be silently torn down by a metadata update.
func (m *Manager) RegisterModel(name, path, format string) error {
	if name == "" {
		return fmt.Errorf("model name is required")
	}
	if path == "" {
		return fmt.Errorf("model path is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.models[name]; ok {
		existing.Path = path
		existing.Format = format
		return nil
	}
	// New models are healthy by default so a freshly registered + loaded model
	// is immediately routable; an operator flips Healthy=false via SetHealth.
	m.models[name] = &Model{Name: name, Path: path, Format: format, Healthy: true}
	return nil
}

// SetMetrics attaches routing metrics to a registered model. It reports an
// error for an unknown model so callers cannot configure routing for a model
// that does not exist.
func (m *Manager) SetMetrics(name string, metrics Metrics) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, ok := m.models[name]
	if !ok {
		return fmt.Errorf("model not found: %s", name)
	}
	model.Metrics = metrics
	return nil
}

// SetHealth marks a registered model healthy or unhealthy for routing. An
// unhealthy model is excluded from Route/RouteInference even while loaded.
func (m *Manager) SetHealth(name string, healthy bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, ok := m.models[name]
	if !ok {
		return fmt.Errorf("model not found: %s", name)
	}
	model.Healthy = healthy
	return nil
}

// UnregisterModel removes a model from the registry. It reports an error if the
// model is unknown so callers can distinguish a no-op from a real removal.
func (m *Manager) UnregisterModel(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[name]; !ok {
		return fmt.Errorf("model not found: %s", name)
	}
	delete(m.models, name)
	return nil
}

// LoadModel marks a model as loaded.
func (m *Manager) LoadModel(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, ok := m.models[name]
	if !ok {
		return fmt.Errorf("model not found: %s", name)
	}
	now := time.Now()
	model.LoadedAt = &now
	model.LoadCount++
	return nil
}

// UnloadModel marks a model as unloaded.
func (m *Manager) UnloadModel(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, ok := m.models[name]
	if !ok {
		return fmt.Errorf("model not found: %s", name)
	}
	model.LoadedAt = nil
	return nil
}

// ListModels returns a snapshot of all registered models. Each returned *Model
// is a copy: callers cannot mutate the manager's internal state through the
// returned pointers (e.g. forging a LoadedAt timestamp).
func (m *Manager) ListModels() []*Model {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Model, 0, len(m.models))
	for _, model := range m.models {
		list = append(list, model.clone())
	}
	return list
}

// GetModel returns a snapshot copy of a single registered model. The bool is
// false when the model is unknown.
func (m *Manager) GetModel(name string) (*Model, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	model, ok := m.models[name]
	if !ok {
		return nil, false
	}
	return model.clone(), true
}

// IsLoaded reports whether the named model is currently loaded.
func (m *Manager) IsLoaded(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	model, ok := m.models[name]
	return ok && model.LoadedAt != nil
}

// clone returns a deep copy of the model so internal pointers (LoadedAt) are
// never shared with callers.
func (model *Model) clone() *Model {
	cp := *model
	if model.LoadedAt != nil {
		t := *model.LoadedAt
		cp.LoadedAt = &t
	}
	return &cp
}

// Inference runs inference on an explicitly named model via the installed
// Backend.
//
// The former implementation returned a synthetic "[stub inference ...]" string;
// it no longer does. Inference now produces the REAL result of the injected
// Backend for the named model. The contract guards remain and are what an end
// user depends on: inference is rejected unless the model is registered AND
// currently loaded, an empty prompt is rejected, and a missing backend is a
// typed failure (never a fabricated response).
func (m *Manager) Inference(name, prompt string) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	m.mu.RLock()
	model, ok := m.models[name]
	loaded := ok && model.LoadedAt != nil
	backend := m.backend
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("model not found: %s", name)
	}
	if !loaded {
		return "", fmt.Errorf("model not loaded: %s", name)
	}
	if backend == nil {
		return "", fmt.Errorf("llm: no inference backend installed for model %s", name)
	}
	return backend.Generate(name, prompt)
}

// Route selects the best eligible model for the given strategy WITHOUT running
// inference. A model is eligible iff it is registered, currently loaded, and
// healthy. Among eligible models the strategy chooses:
//
//   - StrategyLatency:    lowest Metrics.LatencyMS
//   - StrategyThroughput: highest Metrics.ThroughputTPS
//   - StrategyQuality:    highest Metrics.Quality
//   - StrategyCost:       lowest Metrics.CostPer1K
//
// Ties are broken deterministically by model name (ascending) so the choice is
// reproducible. It returns ErrUnknownStrategy for an unrecognised strategy and
// ErrNoEligibleModel when nothing is eligible — never a fabricated pick.
func (m *Manager) Route(strategy Strategy) (string, error) {
	if !validStrategy(strategy) {
		return "", &ErrUnknownStrategy{Strategy: strategy}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	best := ""
	var bestScore float64
	have := false
	for _, model := range m.models {
		if model.LoadedAt == nil || !model.Healthy {
			continue
		}
		score := strategyScore(strategy, model.Metrics)
		// A NaN metric (NaN/Inf raw value fed through strategyScore) is not a
		// valid measurement: every ordering comparison against NaN is false, so a
		// naive `score > bestScore` lets a NaN-scored model that happens to be
		// visited first stick as "best" forever — no finite candidate can ever
		// displace it (finite > NaN is false; finite == NaN is false). That
		// mis-routes every request to a model with a corrupt/missing metric. Treat
		// a NaN score as strictly worse than any comparable score: it may seed the
		// pick only when nothing comparable exists, and is always displaced by the
		// first finite candidate. (+Inf for a maximise axis is a legitimately
		// "best" finite-ordering value and is left to compare normally; -Inf, which
		// arises from a +Inf latency/cost on a minimise axis, correctly ranks last.)
		isNaN := score != score // NaN is the only value not equal to itself.
		switch {
		case !have:
			best, bestScore, have = model.Name, score, true
		case isNaN:
			// A NaN-scored candidate never displaces an existing pick.
			continue
		case bestScore != bestScore:
			// The incumbent best is NaN but this candidate is comparable: any
			// comparable score beats a NaN incumbent.
			best, bestScore = model.Name, score
		case score > bestScore:
			best, bestScore = model.Name, score
		case score == bestScore && model.Name < best:
			// Deterministic tie-break by name.
			best = model.Name
		}
	}
	if !have {
		return "", &ErrNoEligibleModel{Strategy: strategy}
	}
	return best, nil
}

// RouteInference selects a model per the strategy and then runs inference on it
// through the installed Backend. The returned model id is the SELECTED model so
// callers can assert sink-side that the strategy drove the choice. It returns a
// typed error (ErrUnknownStrategy / ErrNoEligibleModel / missing-backend /
// empty-prompt) instead of a synthetic response on any failure.
func (m *Manager) RouteInference(strategy Strategy, prompt string) (selected, response string, err error) {
	if prompt == "" {
		return "", "", fmt.Errorf("prompt is required")
	}
	selected, err = m.Route(strategy)
	if err != nil {
		return "", "", err
	}
	m.mu.RLock()
	backend := m.backend
	m.mu.RUnlock()
	if backend == nil {
		return "", "", fmt.Errorf("llm: no inference backend installed")
	}
	response, err = backend.Generate(selected, prompt)
	if err != nil {
		return selected, "", err
	}
	return selected, response, nil
}

// validStrategy reports whether s is one of the four supported strategies.
func validStrategy(s Strategy) bool {
	switch s {
	case StrategyLatency, StrategyThroughput, StrategyQuality, StrategyCost:
		return true
	default:
		return false
	}
}

// strategyScore maps a model's metrics onto a single "higher is better" score
// for the given strategy, so Route can compare with a uniform '>' rule. For
// minimise-axes (latency, cost) the score is negated so a smaller raw value
// yields a larger score.
func strategyScore(strategy Strategy, mt Metrics) float64 {
	switch strategy {
	case StrategyLatency:
		return -mt.LatencyMS
	case StrategyThroughput:
		return mt.ThroughputTPS
	case StrategyQuality:
		return mt.Quality
	case StrategyCost:
		return -mt.CostPer1K
	default:
		return 0
	}
}
