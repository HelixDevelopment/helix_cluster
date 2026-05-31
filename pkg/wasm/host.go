// Package wasm provides a WebAssembly plugin runtime using Wasmtime for Helix Cluster OS.
package wasm

import (
	"fmt"

	"github.com/bytecodealliance/wasmtime-go/v29"
)

// Host wraps a Wasmtime engine and provides lifecycle helpers for loading and
// instantiating WASM modules with WASI support.
type Host struct {
	engine *wasmtime.Engine
	store  *wasmtime.Store
	module *wasmtime.Module
	inst   *wasmtime.Instance
}

// NewHost creates a new Wasmtime host with default configuration.
func NewHost() (*Host, error) {
	engine := wasmtime.NewEngine()
	store := wasmtime.NewStore(engine)
	return &Host{engine: engine, store: store}, nil
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
		return fmt.Errorf("no module loaded")
	}
	wasi := wasmtime.NewWasiConfig()
	h.store.SetWasi(wasi)

	linker := wasmtime.NewLinker(h.engine)
	if err := linker.DefineWasi(); err != nil {
		return fmt.Errorf("define wasi: %w", err)
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
		return 0, fmt.Errorf("module not instantiated")
	}
	fn := h.inst.GetExport(h.store, funcName)
	if fn == nil {
		return 0, fmt.Errorf("function %q not found", funcName)
	}
	wasmArgs := make([]interface{}, len(args))
	for i, v := range args {
		wasmArgs[i] = v
	}
	results, err := fn.Func().Call(h.store, wasmArgs...)
	if err != nil {
		return 0, fmt.Errorf("call %q: %w", funcName, err)
	}
	if results == nil {
		return 0, nil
	}
	val, ok := results.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected result type from %q", funcName)
	}
	return val, nil
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
