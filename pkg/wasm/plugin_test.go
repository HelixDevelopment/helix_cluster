package wasm

import (
	"errors"
	"testing"

	"github.com/bytecodealliance/wasmtime-go/v29"
)

// compileWat compiles WAT source to a WASM binary at test time. Using WAT keeps
// the test modules readable and lets us assert against REAL guest-computed
// output rather than hand-encoded byte blobs.
func compileWat(t *testing.T, wat string) []byte {
	t.Helper()
	b, err := wasmtime.Wat2Wasm(wat)
	if err != nil {
		t.Fatalf("wat2wasm: %v", err)
	}
	return b
}

// transformWat is a real plugin module. It exports linear memory plus the
// init/execute/shutdown lifecycle functions.
//
//   - execute(len) reads `len` bytes at offset 0, writes each byte+1 (a visible
//     transform) back at offset 0, and returns `len` as the output length.
//   - init writes sentinel byte 0xAA at offset 1024 (memory side effect proving
//     init actually ran).
//   - shutdown writes sentinel byte 0xBB at offset 1025.
const transformWat = `
(module
  (memory (export "memory") 1)
  (func (export "init")
    (i32.store8 (i32.const 1024) (i32.const 0xAA)))
  (func (export "execute") (param $len i64) (result i64)
    (local $i i32)
    (local $n i32)
    (local.set $i (i32.const 0))
    (local.set $n (i32.wrap_i64 (local.get $len)))
    (block $done
      (loop $loop
        (br_if $done (i32.ge_s (local.get $i) (local.get $n)))
        (i32.store8
          (local.get $i)
          (i32.add (i32.load8_u (local.get $i)) (i32.const 1)))
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $loop)))
    (local.get $len))
  (func (export "shutdown")
    (i32.store8 (i32.const 1025) (i32.const 0xBB))))
`

// noExecuteWat exports a memory and init/shutdown but deliberately OMITS the
// "execute" export, so Execute must surface ErrFunctionNotFound.
const noExecuteWat = `
(module
  (memory (export "memory") 1)
  (func (export "init"))
  (func (export "shutdown")))
`

func TestWasmPluginExecuteTransformsInput(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, transformWat))
	p := NewWasmPlugin(path)
	if err := p.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	defer p.Shutdown()

	in := []byte("hello")
	out, err := p.Execute(in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Each byte is incremented by the guest: "hello" -> "ifmmp".
	// This assertion FAILS if Execute ignores the WASM call and echoes input.
	want := "ifmmp"
	if string(out) != want {
		t.Errorf("execute returned %q, want %q (transformed by WASM)", string(out), want)
	}
	// Sanity: the output must NOT equal the input — an echo bug must fail here.
	if string(out) == string(in) {
		t.Errorf("execute echoed input %q; WASM transform was not applied", string(in))
	}
}

func TestWasmPluginInitSideEffect(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, transformWat))
	p := NewWasmPlugin(path)
	if err := p.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	defer p.Shutdown()

	mem := p.host.Memory()
	if mem == nil {
		t.Fatal("expected exported memory after init")
	}
	// init writes 0xAA at offset 1024. If the h.Call(initFn) line were removed,
	// this sentinel would remain 0x00 and the test would fail (mutation pairing).
	if mem[1024] != 0xAA {
		t.Errorf("init sentinel = 0x%02X at offset 1024, want 0xAA (init export did not run)", mem[1024])
	}
}

func TestWasmPluginShutdownSideEffect(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, transformWat))
	p := NewWasmPlugin(path)
	if err := p.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Capture the live memory alias BEFORE shutdown closes the store, then run
	// shutdown and observe the sentinel the shutdown export wrote.
	mem := p.host.Memory()
	if mem == nil {
		t.Fatal("expected exported memory after init")
	}
	if mem[1025] != 0x00 {
		t.Fatalf("shutdown sentinel pre-set to 0x%02X, expected clean 0x00", mem[1025])
	}

	// Call the shutdown export directly so we can inspect the side effect on the
	// still-open store; Plugin.Shutdown would then Close() the host.
	if _, err := p.host.Call("shutdown"); err != nil {
		t.Fatalf("shutdown export: %v", err)
	}
	if mem[1025] != 0xBB {
		t.Errorf("shutdown sentinel = 0x%02X at offset 1025, want 0xBB (shutdown export did not run)", mem[1025])
	}
	if err := p.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestWasmPluginExecuteMissingExport(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, noExecuteWat))
	p := NewWasmPlugin(path)
	if err := p.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	defer p.Shutdown()

	_, err := p.Execute([]byte("x"))
	if err == nil {
		t.Fatal("expected error when execute export is missing")
	}
	if !errors.Is(err, ErrFunctionNotFound) {
		t.Errorf("expected ErrFunctionNotFound, got %v", err)
	}
}

func TestWasmPluginInitCorruptModule(t *testing.T) {
	// Structurally invalid WASM: valid magic + version, then garbage sections.
	corrupt := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	}
	path := writeTempWasm(t, corrupt)
	p := NewWasmPlugin(path)
	err := p.Init()
	if err == nil {
		t.Fatal("expected Init to fail on corrupt WASM module")
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
	path := writeTempWasm(t, compileWat(t, transformWat))
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
