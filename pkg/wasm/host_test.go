package wasm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bytecodealliance/wasmtime-go/v29"
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

// f32ResultWat is a minimal module whose exported "get_f32" function returns a
// single f32 value. Wasmtime's Func.Call returns float32 (not int64) for such a
// function, so Host.Call's `results.(int64)` type assertion fails and the
// ErrUnexpectedResult branch is taken.
//
// §1.1 mutation pairing: if the `if !ok { return 0, … ErrUnexpectedResult }` guard
// is removed (or the branch omitted), Host.Call would panic on the type assertion
// or silently return 0, nil — both cause this test to fail.
const f32ResultWat = `
(module
  (func (export "get_f32") (result f32)
    f32.const 1.5)
)`

// multiValueResultWat is a module whose "get_pair" function returns TWO i64
// values. Wasmtime's Func.Call returns []Val (not int64) for multi-value
// functions, so Host.Call must also return ErrUnexpectedResult.
//
// §1.1 mutation pairing: same as above — removing the !ok branch silences the
// error, causing the assertion below to fail with "expected error, got nil".
const multiValueResultWat = `
(module
  (func (export "get_pair") (result i64 i64)
    i64.const 1
    i64.const 2)
)`

// compileWatHost is the *testing.T twin of plugin_test.go's compileWat, used
// here in host_test.go which is in the same package. It avoids a redeclaration
// because compileWat is already declared in plugin_test.go; we reuse it below.
// (No separate helper needed — we call compileWat directly.)

// TestHostCallErrUnexpectedResultF32 verifies that calling a WASM function
// whose return type is f32 (not i64) causes Host.Call to return a wrapped
// ErrUnexpectedResult. This directly exercises the `val, ok := results.(int64);
// if !ok { … ErrUnexpectedResult }` guard in host.go.
//
// Mutation that kills this test: comment out or remove the `if !ok` branch in
// Host.Call. Without it the function would panic on a bad type assertion (or
// return 0,nil if the assertion were changed to a comma-ok form without the
// error), and the errors.Is check below would fail.
func TestHostCallErrUnexpectedResultF32(t *testing.T) {
	wasm, err := wasmtime.Wat2Wasm(f32ResultWat)
	if err != nil {
		t.Fatalf("wat2wasm f32ResultWat: %v", err)
	}

	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()

	path := writeTempWasm(t, wasm)
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	_, callErr := h.Call("get_f32")
	if callErr == nil {
		t.Fatal("expected ErrUnexpectedResult for f32-returning function, got nil error")
	}
	if !errors.Is(callErr, ErrUnexpectedResult) {
		t.Fatalf("expected errors.Is(err, ErrUnexpectedResult); got: %v", callErr)
	}
}

// TestHostCallErrUnexpectedResultMultiValue verifies that calling a WASM
// function that returns two i64 values (a multi-value result, which Wasmtime
// returns as []Val rather than int64) causes Host.Call to return a wrapped
// ErrUnexpectedResult.
//
// Mutation that kills this test: same as above — removing the !ok guard means
// callErr is nil and the errors.Is check fails.
func TestHostCallErrUnexpectedResultMultiValue(t *testing.T) {
	wasm, err := wasmtime.Wat2Wasm(multiValueResultWat)
	if err != nil {
		t.Fatalf("wat2wasm multiValueResultWat: %v", err)
	}

	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()

	path := writeTempWasm(t, wasm)
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}

	_, callErr := h.Call("get_pair")
	if callErr == nil {
		t.Fatal("expected ErrUnexpectedResult for multi-value-returning function, got nil error")
	}
	if !errors.Is(callErr, ErrUnexpectedResult) {
		t.Fatalf("expected errors.Is(err, ErrUnexpectedResult); got: %v", callErr)
	}
}
