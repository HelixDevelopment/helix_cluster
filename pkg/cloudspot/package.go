// Package cloudspot implements a graceful-drain preemption handler for
// cloud spot / preemptible instances in Helix Cluster OS.
//
// Cloud providers warn an instance shortly before reclaiming it (e.g. AWS Spot
// "2-minute" interruption notices, GCP preemptible 30s ACPI G2 events). On such
// a notice the node must stop accepting new work, checkpoint/flush in-flight
// work, deregister from its control plane, and finish all of that before the
// provider's hard deadline — or be force-stopped at the deadline so the runtime
// is never caught mid-write when the VM disappears.
//
// HONEST EXTERNAL SEAM: the live provider signal (instance-metadata polling,
// IMDS, ACPI event, etc.) is OUT OF SCOPE for this package. It is modelled
// behind the PreemptionSource interface. A software reference implementation,
// FakeSource, is provided for tests; a real provider adapter would implement
// the same interface against the metadata endpoint. This package only consumes
// the notice and drives the deterministic drain lifecycle.
package cloudspot

import (
	"context"
	"sync"
	"time"
)

// PreemptionNotice is a provider-issued warning that the instance is about to
// be reclaimed. Deadline is the absolute wall-clock instant by which the
// instance must have finished draining; after it, the VM may vanish.
type PreemptionNotice struct {
	// Deadline is when the provider will reclaim the instance.
	Deadline time.Time
	// Reason is a free-form, provider-supplied explanation (optional).
	Reason string
}

// PreemptionSource is the seam over the (out-of-scope) live provider signal.
// Notifications returns a channel that delivers at most one notice per
// preemption event. Implementations own the channel and may close it when the
// source is exhausted.
type PreemptionSource interface {
	Notifications() <-chan PreemptionNotice
}

// DrainStep names a stage of the graceful-drain lifecycle. The handler always
// runs the steps in the order declared below.
type DrainStep string

const (
	// StepStopAdmission stops accepting new work. It runs first so nothing new
	// enters the system while the remaining steps flush what is already there.
	StepStopAdmission DrainStep = "stop_admission"
	// StepCheckpoint checkpoints/flushes in-flight work to durable storage.
	StepCheckpoint DrainStep = "checkpoint"
	// StepDeregister deregisters the node from its control plane / load balancer.
	StepDeregister DrainStep = "deregister"
)

// DrainOrder is the canonical order in which drain steps execute.
var DrainOrder = []DrainStep{StepStopAdmission, StepCheckpoint, StepDeregister}

// Hooks are the injectable side effects of a drain. Each may be nil, in which
// case that step is a no-op (but admission is still closed — see Handler).
type Hooks struct {
	// StopAdmission must make future Admit calls fail. Called first.
	StopAdmission func(context.Context) error
	// Checkpoint flushes in-flight work. Called second.
	Checkpoint func(context.Context) error
	// Deregister removes the node from the control plane. Called third.
	Deregister func(context.Context) error
	// ForceStop is invoked iff the drain does not finish before the deadline.
	// It is the last-resort "the VM is about to disappear" abort.
	ForceStop func()
}

// Outcome is the terminal result of handling one preemption notice.
type Outcome string

const (
	// OutcomeIdle means no notice was received before the source/context ended.
	OutcomeIdle Outcome = "idle"
	// OutcomeDrained means every drain step completed before the deadline.
	OutcomeDrained Outcome = "drained"
	// OutcomeForceStopped means the deadline elapsed mid-drain and ForceStop ran.
	OutcomeForceStopped Outcome = "force_stopped"
)

// Result records what the handler did, for sink-side assertions and audit.
type Result struct {
	Outcome Outcome
	// Steps lists, in execution order, the drain steps that completed.
	Steps []DrainStep
	// Err is the first non-nil error returned by a drain hook, if any.
	Err error
}

// Handler runs the drain lifecycle in response to a single preemption notice.
// It also gates new-work admission: once a notice fires, Admit returns false.
type Handler struct {
	source PreemptionSource
	hooks  Hooks
	// now allows tests to control the clock for deadline arithmetic. Defaults
	// to time.Now. The wait itself uses a real timer so deadlines are honoured
	// against wall time.
	now func() time.Time

	mu       sync.Mutex
	draining bool
}

// Option configures a Handler.
type Option func(*Handler)

// withClock overrides the clock used for deadline checks (test seam).
func withClock(now func() time.Time) Option {
	return func(h *Handler) { h.now = now }
}

// NewHandler builds a Handler over the given source and drain hooks.
func NewHandler(source PreemptionSource, hooks Hooks, opts ...Option) *Handler {
	h := &Handler{source: source, hooks: hooks, now: time.Now}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Admit reports whether new work may be accepted. It returns true until a
// preemption notice has begun draining, false afterwards. This is the
// admission gate the rest of the runtime consults before taking on new tasks.
func (h *Handler) Admit() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.draining
}

// Run blocks until either a preemption notice fires (then it drains) or ctx is
// cancelled / the source channel closes with no notice (then it returns idle).
//
// On a notice it runs the drain steps in DrainOrder. If they all finish before
// the notice Deadline, the outcome is OutcomeDrained. If the deadline elapses
// while draining, ForceStop is invoked and the outcome is OutcomeForceStopped.
func (h *Handler) Run(ctx context.Context) Result {
	ch := h.source.Notifications()
	select {
	case <-ctx.Done():
		return Result{Outcome: OutcomeIdle}
	case notice, ok := <-ch:
		if !ok {
			// Source closed without ever emitting a notice: stay idle.
			return Result{Outcome: OutcomeIdle}
		}
		return h.drain(ctx, notice)
	}
}

// drain executes the lifecycle for a received notice.
func (h *Handler) drain(ctx context.Context, notice PreemptionNotice) Result {
	// Close admission synchronously and FIRST, before any step runs, so no new
	// work can slip in while we flush. This is observable via Admit().
	h.mu.Lock()
	h.draining = true
	h.mu.Unlock()

	// Bound the whole drain by the provider deadline.
	deadlineCtx, cancel := context.WithDeadline(ctx, notice.Deadline)
	defer cancel()

	type stepDef struct {
		name DrainStep
		fn   func(context.Context) error
	}
	steps := []stepDef{
		{StepStopAdmission, h.hooks.StopAdmission},
		{StepCheckpoint, h.hooks.Checkpoint},
		{StepDeregister, h.hooks.Deregister},
	}

	res := Result{Outcome: OutcomeDrained, Steps: make([]DrainStep, 0, len(steps))}

	for _, s := range steps {
		// Deadline already blown before starting this step → force-stop.
		if !h.now().Before(notice.Deadline) {
			return h.forceStop(res)
		}

		done := make(chan error, 1)
		go func(fn func(context.Context) error) {
			if fn == nil {
				done <- nil
				return
			}
			done <- fn(deadlineCtx)
		}(s.fn)

		select {
		case err := <-done:
			if err != nil {
				res.Err = err
			}
			res.Steps = append(res.Steps, s.name)
		case <-deadlineCtx.Done():
			// Deadline elapsed mid-step: abort and force-stop. The step is NOT
			// recorded as completed.
			return h.forceStop(res)
		}
	}

	return res
}

// forceStop runs the ForceStop hook (if any) and stamps the outcome.
func (h *Handler) forceStop(res Result) Result {
	if h.hooks.ForceStop != nil {
		h.hooks.ForceStop()
	}
	res.Outcome = OutcomeForceStopped
	return res
}
