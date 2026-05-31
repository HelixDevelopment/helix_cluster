// Package wasm provides a WebAssembly plugin runtime using Wasmtime for Helix Cluster OS.
package wasm

import (
	"fmt"
)

// Plugin is the high-level interface for Helix Cluster OS plugins.
type Plugin interface {
	Init() error
	Execute(input []byte) ([]byte, error)
	Shutdown() error
}

// WasmPlugin wraps a Host to satisfy the Plugin interface.
type WasmPlugin struct {
	host     *Host
	path     string
	initFn   string
	execFn   string
	shutdown string
	caps     *Capabilities
}

// NewWasmPlugin creates a plugin backed by a WASM module at path. By default
// the plugin grants NO host capabilities (deny-by-default), which preserves the
// behavior of modules that only use WASI and their own memory. Grant access
// with WithCapabilities before calling Init.
func NewWasmPlugin(path string) *WasmPlugin {
	return &WasmPlugin{
		path:     path,
		initFn:   "init",
		execFn:   "execute",
		shutdown: "shutdown",
		caps:     NewCapabilities(),
	}
}

// WithCapabilities sets the capability allow-list applied at Init time and
// returns the receiver for chaining. A module attempting a capability not in
// this set is denied at Execute time with ErrCapabilityDenied.
func (p *WasmPlugin) WithCapabilities(caps *Capabilities) *WasmPlugin {
	p.caps = caps
	return p
}

// Host returns the underlying host after Init, or nil before. It exposes
// sink-side observations such as Logs() captured through granted host funcs.
func (p *WasmPlugin) Host() *Host { return p.host }

// Init loads and instantiates the WASM module, then calls its init export.
func (p *WasmPlugin) Init() error {
	h, err := NewHost()
	if err != nil {
		return fmt.Errorf("create host: %w", err)
	}
	p.host = h
	h.SetCapabilities(p.caps)
	if err := h.LoadModule(p.path); err != nil {
		return err
	}
	if err := h.Instantiate(); err != nil {
		return err
	}
	// init is optional — ignore errors if absent.
	_, _ = h.Call(p.initFn)
	return nil
}

// Execute runs the plugin's exported "execute" function against input and
// returns the bytes the WASM module actually produced.
//
// ABI: the host copies input into the module's exported linear memory at
// offset 0, then calls execute(inputLen i64) -> (outputLen i64). The guest reads
// inputLen bytes at offset 0, writes its result starting at offset 0, and
// returns the number of output bytes. The host then reads exactly that many
// bytes from memory offset 0 and returns them. The output is therefore the real
// product of the WASM module, not an echo of the input.
func (p *WasmPlugin) Execute(input []byte) ([]byte, error) {
	if p.host == nil {
		return nil, fmt.Errorf("plugin not initialized")
	}

	mem := p.host.Memory()
	if mem == nil {
		return nil, fmt.Errorf("execute: module exports no linear memory")
	}
	if len(input) > len(mem) {
		return nil, fmt.Errorf("execute: input of %d bytes exceeds WASM memory of %d bytes", len(input), len(mem))
	}
	copy(mem, input)

	outLen, err := p.host.Call(p.execFn, int64(len(input)))
	if err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	if outLen < 0 {
		return nil, fmt.Errorf("execute: module returned negative output length %d", outLen)
	}

	// Re-fetch memory: the guest may have grown it during execution, which
	// would invalidate the earlier slice.
	mem = p.host.Memory()
	if int64(len(mem)) < outLen {
		return nil, fmt.Errorf("execute: module reported %d output bytes but memory is %d bytes", outLen, len(mem))
	}
	out := make([]byte, outLen)
	copy(out, mem[:outLen])
	return out, nil
}

// Shutdown calls the optional shutdown export and releases the host.
func (p *WasmPlugin) Shutdown() error {
	if p.host == nil {
		return nil
	}
	_, _ = p.host.Call(p.shutdown)
	p.host.Close()
	p.host = nil
	return nil
}
