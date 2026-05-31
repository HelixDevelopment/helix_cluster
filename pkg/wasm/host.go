// Package wasm provides a WebAssembly plugin runtime using Wasmtime for Helix Cluster OS.
package wasm

import (
	"errors"
	"fmt"

	"github.com/bytecodealliance/wasmtime-go/v29"
)

// capabilityDeniedError is the error surfaced from Call when a guest invoked a
// gated host function whose capability was not granted. It carries BOTH the
// ErrCapabilityDenied sentinel and the original Wasmtime error (which holds the
// wasm backtrace and the "Caused by: capability denied …" trap text). Its Unwrap
// returns a two-element slice so errors.Is matches the sentinel AND any concrete
// type in the original error chain remains reachable via errors.Is/errors.As.
//
// Note: when a host function returns a *wasmtime.Trap, wasmtime collapses it
// into a *wasmtime.Error whose message embeds the trap text under a "Caused by:"
// line — there is no recoverable *wasmtime.Trap object at the Go level, so we
// retain the original *wasmtime.Error instead.
type capabilityDeniedError struct {
	funcName string
	cap      Capability
	orig     error // the original *wasmtime.Error (backtrace + caused-by text)
}

func (e *capabilityDeniedError) Error() string {
	return fmt.Sprintf("call %q: %s: %s", e.funcName, capDeniedMessage(e.cap), e.orig.Error())
}

// Unwrap exposes both the sentinel (for errors.Is(err, ErrCapabilityDenied)) and
// the original error (so its chain stays reachable).
func (e *capabilityDeniedError) Unwrap() []error {
	return []error{ErrCapabilityDenied, e.orig}
}

// Capability reports which capability was denied.
func (e *capabilityDeniedError) Capability() Capability { return e.cap }

var (
	ErrNotLoaded        = errors.New("no module loaded")
	ErrNotInstantiated  = errors.New("module not instantiated")
	ErrFunctionNotFound = errors.New("function not found")
	ErrUnexpectedResult = errors.New("unexpected result type")
)

// Host wraps a Wasmtime engine and provides lifecycle helpers for loading and
// instantiating WASM modules with WASI support.
//
// A Host is NOT goroutine-safe: a single Host's Call/Execute (and the gated
// host functions they drive, which share a *rand.Rand and a log slice) must not
// be invoked concurrently from multiple goroutines. The default randomness
// source is internally mutex-guarded to prevent a data race on the RNG itself,
// but Wasmtime's store and the captured log records are not — use a separate
// Host per goroutine for concurrent execution.
type Host struct {
	engine  *wasmtime.Engine
	store   *wasmtime.Store
	module  *wasmtime.Module
	inst    *wasmtime.Instance
	sandbox *sandbox
}

// NewHost creates a new Wasmtime host with default configuration. The host
// starts with a deny-everything capability sandbox, preserving the historical
// behavior in which modules only touched WASI and their own linear memory. Use
// SetCapabilities to grant gated host capabilities before Instantiate.
func NewHost() (*Host, error) {
	engine := wasmtime.NewEngine()
	store := wasmtime.NewStore(engine)
	return &Host{engine: engine, store: store, sandbox: defaultSandbox()}, nil
}

// SetCapabilities replaces the host's capability grant set. It must be called
// before Instantiate to take effect (the grant set is bound into the gated
// host functions at instantiation time). A nil caps argument is treated as an
// empty (deny-everything) grant. Returns the receiver for chaining.
func (h *Host) SetCapabilities(caps *Capabilities) *Host {
	if h.sandbox == nil {
		h.sandbox = defaultSandbox()
	}
	if caps == nil {
		caps = NewCapabilities()
	}
	h.sandbox.caps = caps
	return h
}

// Capabilities returns the host's current capability grant set.
func (h *Host) Capabilities() *Capabilities {
	if h.sandbox == nil {
		return NewCapabilities()
	}
	return h.sandbox.caps
}

// LoadModule compiles a WASM module from the given file path.
func (h *Host) LoadModule(path string) error {
	mod, err := wasmtime.NewModuleFromFile(h.engine, path)
	if err != nil {
		return fmt.Errorf("load module %q: %w", path, err)
	}
	h.module = mod
	return nil
}

// Instantiate creates a WASI-linked instance of the loaded module.
func (h *Host) Instantiate() error {
	if h.module == nil {
		return fmt.Errorf("no module loaded: %w", ErrNotLoaded)
	}
	wasi := wasmtime.NewWasiConfig()
	h.store.SetWasi(wasi)

	linker := wasmtime.NewLinker(h.engine)
	if err := linker.DefineWasi(); err != nil {
		return fmt.Errorf("define wasi: %w", err)
	}

	// Register the capability-gated "helix" host functions. They are always
	// defined, but each denies (traps) unless its capability is granted in the
	// sandbox, so an empty grant set preserves prior behavior for modules that
	// never import them.
	if h.sandbox == nil {
		h.sandbox = defaultSandbox()
	}
	if err := h.sandbox.defineHostFuncs(linker); err != nil {
		return fmt.Errorf("define host funcs: %w", err)
	}

	inst, err := linker.Instantiate(h.store, h.module)
	if err != nil {
		return fmt.Errorf("instantiate: %w", err)
	}
	h.inst = inst
	return nil
}

// Call invokes an exported function by name with the provided i64 arguments.
// It returns the first result value or an error.
func (h *Host) Call(funcName string, args ...int64) (int64, error) {
	if h.inst == nil {
		return 0, fmt.Errorf("module not instantiated: %w", ErrNotInstantiated)
	}
	fn := h.inst.GetExport(h.store, funcName)
	if fn == nil {
		return 0, fmt.Errorf("function %q not found: %w", funcName, ErrFunctionNotFound)
	}
	wasmArgs := make([]interface{}, len(args))
	for i, v := range args {
		wasmArgs[i] = v
	}
	results, err := fn.Func().Call(h.store, wasmArgs...)
	if err != nil {
		// A capability-gated host function denies by returning a Wasmtime trap
		// carrying the EXACT capDeniedMessage(cap) string. Wasmtime collapses
		// that trap into an error whose text embeds it under a "Caused by:" line.
		// matchDeniedCapability looks for the precise, per-capability message
		// (e.g. `capability denied: capability "log" not granted`) under that
		// caused-by framing — NOT a bare "capability denied" substring. Only our
		// own host-function trap produces that exact framing, so a guest that
		// merely emits the words "capability denied" elsewhere cannot trigger a
		// false positive. Returning the typed error keeps the original wasmtime
		// error (backtrace included) reachable via the Unwrap chain.
		if cap, ok := matchDeniedCapability(err); ok {
			return 0, &capabilityDeniedError{funcName: funcName, cap: cap, orig: err}
		}
		return 0, fmt.Errorf("call %q: %w", funcName, err)
	}
	if results == nil {
		return 0, nil
	}
	val, ok := results.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected result type from %q: %w", funcName, ErrUnexpectedResult)
	}
	return val, nil
}

// Memory returns the instance's exported linear memory ("memory"), or nil if
// the module is not instantiated or exports no memory. The returned slice
// aliases WASM linear memory directly: writes are visible to the guest and
// guest writes are visible here. It is only valid until the next call that may
// grow memory.
func (h *Host) Memory() []byte {
	if h.inst == nil {
		return nil
	}
	ext := h.inst.GetExport(h.store, "memory")
	if ext == nil {
		return nil
	}
	mem := ext.Memory()
	if mem == nil {
		return nil
	}
	return mem.UnsafeData(h.store)
}

// Close releases engine and store resources.
func (h *Host) Close() {
	if h.store != nil {
		h.store.Close()
	}
	if h.engine != nil {
		h.engine.Close()
	}
}
