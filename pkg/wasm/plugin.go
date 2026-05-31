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
}

// NewWasmPlugin creates a plugin backed by a WASM module at path.
func NewWasmPlugin(path string) *WasmPlugin {
	return &WasmPlugin{
		path:     path,
		initFn:   "init",
		execFn:   "execute",
		shutdown: "shutdown",
	}
}

// Init loads and instantiates the WASM module, then calls its init export.
func (p *WasmPlugin) Init() error {
	h, err := NewHost()
	if err != nil {
		return fmt.Errorf("create host: %w", err)
	}
	p.host = h
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

// Execute calls the exported execute function with input length as a single i64
// argument.  The real implementation would use WASM memory; here we pass the
// length as a simple integration signal.
func (p *WasmPlugin) Execute(input []byte) ([]byte, error) {
	if p.host == nil {
		return nil, fmt.Errorf("plugin not initialized")
	}
	_, err := p.host.Call(p.execFn, int64(len(input)))
	if err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	return input, nil
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
