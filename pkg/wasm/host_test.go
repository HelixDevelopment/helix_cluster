package wasm

import (
	"os"
	"path/filepath"
	"testing"
)

// simpleAddWasm is a minimal WASM binary that exports "add(a, b) -> a+b".
// Built from WAT:
//
//	(module
//	  (func $add (param i64 i64) (result i64)
//	    local.get 0
//	    local.get 1
//	    i64.add)
//	  (export "add" (func $add))
//	)
var simpleAddWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
	0x01, 0x07, 0x01, 0x60, 0x02, 0x7e, 0x7e, 0x01,
	0x7e, 0x03, 0x02, 0x01, 0x00, 0x07, 0x07, 0x01,
	0x03, 0x61, 0x64, 0x64, 0x00, 0x00, 0x0a, 0x09,
	0x01, 0x07, 0x00, 0x20, 0x00, 0x20, 0x01, 0x7c,
	0x0b,
}

func writeTempWasm(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.wasm")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}
	return path
}

func TestHostLoadModule(t *testing.T) {
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()

	path := writeTempWasm(t, simpleAddWasm)
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
}

func TestHostInstantiateAndCall(t *testing.T) {
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()

	path := writeTempWasm(t, simpleAddWasm)
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	result, err := h.Call("add", 10, 32)
	if err != nil {
		t.Fatalf("call add: %v", err)
	}
	if result != 42 {
		t.Errorf("add(10,32) = %d, want 42", result)
	}
}

// TestInstantiateNonHelixModuleAfterSandboxWiring is the named backward-compat
// guarantee for Host.Instantiate: a module that does NOT import the "helix"
// namespace must still instantiate and run correctly even though Instantiate now
// unconditionally registers the gated helix host functions via
// sandbox.defineHostFuncs. simpleAddWasm imports nothing from "helix".
//
// Mutation that kills this: change defineHostFuncs to register helix functions
// as REQUIRED imports (or have Instantiate return an error when the module does
// not import them); then this currently-passing add(10,32)==42 path would fail
// at Instantiate or Call for a non-helix module.
func TestInstantiateNonHelixModuleAfterSandboxWiring(t *testing.T) {
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()

	path := writeTempWasm(t, simpleAddWasm)
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	// Grant a capability the module never imports; wiring must remain inert.
	h.SetCapabilities(NewCapabilities(CapClock, CapRandom, CapLog))
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate non-helix module after sandbox wiring: %v", err)
	}
	result, err := h.Call("add", 10, 32)
	if err != nil {
		t.Fatalf("call add on non-helix module: %v", err)
	}
	if result != 42 {
		t.Fatalf("add(10,32) = %d, want 42 (sandbox wiring altered non-helix module)", result)
	}
	// No log capability was exercised by the guest, so nothing must be captured.
	if logs := h.Logs(); len(logs) != 0 {
		t.Fatalf("non-helix module produced log records %v; sandbox leaked", logs)
	}
}

func TestHostCallMissingFunction(t *testing.T) {
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()

	path := writeTempWasm(t, simpleAddWasm)
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	_, err = h.Call("missing", 1, 2)
	if err == nil {
		t.Error("expected error for missing function")
	}
}

func TestHostCallBeforeInstantiate(t *testing.T) {
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()

	path := writeTempWasm(t, simpleAddWasm)
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}

	_, err = h.Call("add", 1, 2)
	if err == nil {
		t.Error("expected error when calling before instantiate")
	}
}

func TestHostLoadMissingFile(t *testing.T) {
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()

	if err := h.LoadModule("/nonexistent/file.wasm"); err == nil {
		t.Error("expected error for missing file")
	}
}
