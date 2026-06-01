// Package policy — decision_log.go
// Provides the DecisionLogEntry type, the Engine constructor options for
// enabling an in-memory ring / external sink, and the DecisionLog accessor.
// Every Evaluate call on an Engine constructed with WithDecisionLog() or
// WithDecisionLogSink(fn) appends one DecisionLogEntry to the configured sink.
package policy

import (
	"time"
)

// DecisionLogEntry records a single OPA policy evaluation result for audit
// purposes (HXC-1220).  Each field is required:
//   - RunUUID   — extracted from input["run_uuid"] (or input["run_id"] as
//                 fallback); identifies the logical run that triggered the eval.
//   - PolicyName — the name under which the policy was loaded in the Engine.
//   - Allow     — true when OPA returned allow=true, false otherwise.
//   - Reason    — human-readable explanation forwarded from Decision.Reason.
//   - At        — wall-clock timestamp captured immediately after evaluation.
type DecisionLogEntry struct {
	RunUUID    string
	PolicyName string
	Allow      bool
	Reason     string
	At         time.Time
}

// SinkFunc is the signature for an external decision-log sink.  It is called
// synchronously after each Evaluate call on an Engine constructed with
// WithDecisionLogSink.
type SinkFunc func(DecisionLogEntry)

// engineOptions holds optional configuration for NewEngineWithOptions.
type engineOptions struct {
	// enableInMemoryLog, when true, causes Evaluate to append every
	// DecisionLogEntry to the engine's internal in-memory ring.
	enableInMemoryLog bool

	// externalSink, when non-nil, is called with each DecisionLogEntry in
	// addition to (or instead of) the in-memory ring.  If both
	// enableInMemoryLog and externalSink are set, both receive the entry.
	externalSink SinkFunc
}

// Option is a functional option for NewEngineWithOptions.
type Option func(*engineOptions)

// WithDecisionLog enables the engine's in-memory decision-log ring.
// Entries can be retrieved with Engine.DecisionLog().
func WithDecisionLog() Option {
	return func(o *engineOptions) {
		o.enableInMemoryLog = true
	}
}

// WithDecisionLogSink registers an external SinkFunc that is called after each
// Evaluate.  This is additive: if WithDecisionLog() is also supplied, the
// in-memory ring is populated as well.
func WithDecisionLogSink(fn SinkFunc) Option {
	return func(o *engineOptions) {
		o.externalSink = fn
	}
}

// NewEngineWithOptions returns a ready-to-use Engine configured by the given
// Option values.  It is equivalent to NewEngine() when no options are provided.
func NewEngineWithOptions(opts ...Option) *Engine {
	o := &engineOptions{}
	for _, fn := range opts {
		fn(o)
	}
	e := &Engine{
		policies: make(map[string]*compiledPolicy),
		logOpts:  o,
	}
	if o.enableInMemoryLog {
		e.decisionLog = make([]DecisionLogEntry, 0, 64)
	}
	return e
}

// DecisionLog returns a snapshot of all in-memory decision-log entries recorded
// since the engine was created.  It returns an empty (non-nil) slice when the
// in-memory log is disabled (i.e. the engine was not constructed with
// WithDecisionLog()).
func (e *Engine) DecisionLog() []DecisionLogEntry {
	e.logMu.RLock()
	defer e.logMu.RUnlock()
	if len(e.decisionLog) == 0 {
		return []DecisionLogEntry{}
	}
	out := make([]DecisionLogEntry, len(e.decisionLog))
	copy(out, e.decisionLog)
	return out
}

// appendDecisionLog records entry to the in-memory ring and/or calls the
// external sink.  It is called by Evaluate and is a no-op when both the
// in-memory ring and external sink are absent (logOpts is nil or zero-value).
func (e *Engine) appendDecisionLog(entry DecisionLogEntry) {
	if e.logOpts == nil {
		return
	}
	if e.logOpts.enableInMemoryLog {
		e.logMu.Lock()
		e.decisionLog = append(e.decisionLog, entry)
		e.logMu.Unlock()
	}
	if e.logOpts.externalSink != nil {
		e.logOpts.externalSink(entry)
	}
}

// extractRunUUID pulls the run identifier from an OPA input map.  It tries the
// "run_uuid" key first (canonical), then falls back to "run_id" for callers
// that use the older convention.  Returns an empty string when neither is set.
func extractRunUUID(input map[string]interface{}) string {
	if v, ok := input["run_uuid"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if v, ok := input["run_id"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}
