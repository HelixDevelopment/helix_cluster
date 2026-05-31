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
