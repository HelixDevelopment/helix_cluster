// Package helixtask provides a task SDK that wraps a function with lifecycle
// hooks and options, then submits the decorated unit of work to an injected
// pool (Submitter).
//
// The actual TEE / remote execution backend is OUT OF SCOPE: the pool is
// modeled purely as an injected Submitter callback. This package only owns the
// deterministic decoration core — hook ordering, option propagation, and result
// surfacing. It is pure Go, deterministic, and performs no I/O.
package helixtask

import "errors"

// Hook is a lifecycle callback. It receives the Spec describing the submission
// so observers can record the propagated options.
type Hook func(spec Spec)

// ErrorHook is the failure-path callback. It receives the Spec and the error
// that the underlying work produced.
type ErrorHook func(spec Spec, err error)

// Options configure a decorated task. TEE selects trusted-execution placement;
// Retries records the desired retry budget. The hooks fire in lifecycle order.
type Options struct {
	// TEE requests trusted-execution placement for the submission. It is
	// propagated verbatim onto the Spec handed to the Submitter.
	TEE bool
	// Retries records the desired retry budget; propagated onto the Spec.
	Retries int

	// Lifecycle hooks. Any may be nil.
	OnSubmit   Hook      // fired first, before the Submitter is invoked
	OnStart    Hook      // fired after submission, before the result is read
	OnComplete Hook      // fired on success only
	OnError    ErrorHook // fired on failure only
}

// Spec is the immutable description of one submission handed to the Submitter
// and echoed to every lifecycle hook. The options recorded here are the values
// propagated from Options — clause 3 asserts this is an exact copy.
type Spec struct {
	// TEE is the propagated trusted-execution flag.
	TEE bool
	// Retries is the propagated retry budget.
	Retries int
	// Fn is the user function the pool is expected to run.
	Fn func() (any, error)
}

// Submitter is the injected pool. Submit receives the fully-built Spec and is
// expected to run the work (out of scope here) and return its result/error.
type Submitter interface {
	Submit(spec Spec) (any, error)
}

// SubmitterFunc adapts a plain function to the Submitter interface.
type SubmitterFunc func(spec Spec) (any, error)

// Submit implements Submitter.
func (f SubmitterFunc) Submit(spec Spec) (any, error) { return f(spec) }

// ErrNilSubmitter is returned when a task is invoked with no pool injected.
var ErrNilSubmitter = errors.New("helixtask: nil submitter")

// Task wraps fn with the given options and returns a callable. Invoking the
// callable submits the work to pool and drives the lifecycle:
//
//	success: OnSubmit -> OnStart -> OnComplete
//	failure: OnSubmit -> OnStart -> OnError
//
// The Spec passed to pool.Submit (and echoed to every hook) carries the
// propagated options exactly. The callable returns whatever the pool returns.
func Task(fn func() (any, error), opts Options, pool Submitter) func() (any, error) {
	return func() (any, error) {
		if pool == nil {
			return nil, ErrNilSubmitter
		}

		spec := Spec{
			TEE:     opts.TEE,
			Retries: opts.Retries,
			Fn:      fn,
		}

		// Lifecycle: submit hook fires before the pool is touched.
		if opts.OnSubmit != nil {
			opts.OnSubmit(spec)
		}

		// Start hook fires once the work is handed off / begins.
		if opts.OnStart != nil {
			opts.OnStart(spec)
		}

		result, err := pool.Submit(spec)

		if err != nil {
			// Failure path: OnError ONLY. OnComplete must NOT fire.
			if opts.OnError != nil {
				opts.OnError(spec, err)
			}
			return result, err
		}

		// Success path: OnComplete ONLY.
		if opts.OnComplete != nil {
			opts.OnComplete(spec)
		}
		return result, nil
	}
}
