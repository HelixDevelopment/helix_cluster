package wasm

import (
	"testing"
)

// noopWasm is a minimal valid WASM module exporting init, execute, and shutdown.
// Type 0: () -> ()    | Type 1: (i64) -> (i64)
// Func 0: init (type 0) | Func 1: execute (type 1) | Func 2: shutdown (type 0)
var noopWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
	0x01, 0x09, 0x02, 0x60, 0x00, 0x00, 0x60, 0x01,
	0x7e, 0x01, 0x7e, 0x03, 0x04, 0x03, 0x00, 0x01,
	0x00, 0x07, 0x1d, 0x03, 0x04, 0x69, 0x6e, 0x69,
	0x74, 0x00, 0x00, 0x07, 0x65, 0x78, 0x65, 0x63,
	0x75, 0x74, 0x65, 0x00, 0x01, 0x08, 0x73, 0x68,
	0x75, 0x74, 0x64, 0x6f, 0x77, 0x6e, 0x00, 0x02,
	0x0a, 0x0c, 0x03, 0x02, 0x00, 0x0b, 0x04, 0x00,
	0x20, 0x00, 0x0b, 0x02, 0x00, 0x0b,
}

func TestWasmPluginLifecycle(t *testing.T) {
	path := writeTempWasm(t, noopWasm)
	p := NewWasmPlugin(path)
	if err := p.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := p.Execute([]byte("hello"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("execute returned %q, want %q", string(out), "hello")
	}
	if err := p.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestWasmPluginExecuteBeforeInit(t *testing.T) {
	p := NewWasmPlugin("/dev/null")
	_, err := p.Execute([]byte("x"))
	if err == nil {
		t.Error("expected error when executing before init")
	}
}

func TestWasmPluginDoubleShutdown(t *testing.T) {
	path := writeTempWasm(t, noopWasm)
	p := NewWasmPlugin(path)
	if err := p.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := p.Shutdown(); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := p.Shutdown(); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}
