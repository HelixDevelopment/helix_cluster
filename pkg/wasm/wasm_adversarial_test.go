package wasm

// wasm_adversarial_test.go — adversarial, mutation-verified tests for the WASM
// host sandbox/isolation gate (HostLoadModule / Instantiate / Call).
//
// This file pins the REJECTION CONTRACT of the sandbox. The dominant bug class
// for a sandbox gate is FAIL-OPEN / sandbox bypass: a module (or call, or
// binary) that MUST be rejected being ADMITTED or EXECUTED instead, or a
// hostile binary crashing the host (DoS via panic). Each test below targets the
// negative space and is paired with a canonical mutation (documented inline)
// that flips it red, so an "always-admit" / "skip-the-check" production change
// is caught.
//
// Sandbox model (see host.go + sandbox.go):
//   - LoadModule compiles the binary; a malformed binary MUST error, never panic.
//   - Instantiate links ONLY WASI + the three capability-gated "helix" host
//     functions (clock_now / random_fill / log_write). The wasmtime Linker is
//     fail-closed: a module importing ANY other function (an undefined helix.*
//     name, or an unknown namespace) is REJECTED at Instantiate with
//     "unknown import ... has not been defined". This is the isolation gate.
//   - The three helix functions ARE defined but deny-by-default: present so a
//     module that imports them instantiates, but each TRAPS at call time unless
//     its capability is granted (deny-by-default — proven in sandbox_test.go).
//   - Call before Instantiate => ErrNotInstantiated; missing/non-exported
//     function => ErrFunctionNotFound. Neither executes anything.

import (
	"errors"
	"strings"
	"testing"

	"github.com/bytecodealliance/wasmtime-go/v29"
)

// ----------------------------------------------------------------------------
// Adversarial fixtures.
// ----------------------------------------------------------------------------

// forbiddenHelixImportWat imports a "helix" function the host does NOT define
// (helix.exec_shell). The host only defines clock_now/random_fill/log_write, so
// this import is undefined and the module must be REJECTED at Instantiate. A
// module reaching for a host function it shouldn't have is a sandbox-escape
// attempt; admitting it is fail-open.
const forbiddenHelixImportWat = `
(module
  (import "helix" "exec_shell" (func $sh (param i32) (result i32)))
  (memory (export "memory") 1)
  (func (export "execute") (param $len i64) (result i64)
    (drop (call $sh (i32.const 0)))
    (i64.const 0)))
`

// unknownNamespaceImportWat imports an entirely foreign namespace (evil.syscall).
// Nothing in the host defines "evil", so this module must be REJECTED at
// Instantiate. This is the "non-Helix / untrusted import" rejection centerpiece.
const unknownNamespaceImportWat = `
(module
  (import "evil" "syscall" (func $sc (param i32) (result i32)))
  (memory (export "memory") 1)
  (func (export "execute") (param $len i64) (result i64)
    (drop (call $sc (i32.const 0)))
    (i64.const 0)))
`

// internalHelperWat exports ONLY "execute" but also defines a NON-exported
// internal function $secret. A host Call("secret") must NOT reach it
// (ErrFunctionNotFound): only exports are callable across the sandbox boundary.
const internalHelperWat = `
(module
  (memory (export "memory") 1)
  (func $secret (result i64) (i64.const 999))
  (func (export "execute") (param $len i64) (result i64)
    (call $secret)))
`

// ----------------------------------------------------------------------------
// CENTERPIECE A — non-Helix / forbidden-import rejection (fail-closed gate).
// ----------------------------------------------------------------------------

// TestForbiddenHelixImportRejected: a module importing an undefined helix.*
// function (helix.exec_shell) MUST be rejected at Instantiate and MUST NOT
// instantiate or run. This is the core sandbox-escape rejection.
//
// Mutation that kills this: call linker.DefineUnknownImportsAsTraps(module)
// before Instantiate (or otherwise admit undefined imports). Then Instantiate
// would SUCCEED for a module reaching for a host function it was never granted —
// a fail-open sandbox bypass — and this test's "expected rejection" fails.
func TestForbiddenHelixImportRejected(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, forbiddenHelixImportWat))
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()
	// Grant EVERY real capability: proves rejection is structural (the function
	// is simply not defined), not merely an ungranted-capability denial.
	h.SetCapabilities(NewCapabilities(CapClock, CapRandom, CapLog, CapFS, CapNetwork))
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}

	err = h.Instantiate()
	if err == nil {
		t.Fatal("SANDBOX BYPASS: module importing undefined helix.exec_shell was admitted at Instantiate")
	}
	if !strings.Contains(err.Error(), "exec_shell") {
		t.Fatalf("rejection error should name the offending import; got: %v", err)
	}
	// And nothing must be callable: the instance was never created.
	if _, callErr := h.Call("execute"); !errors.Is(callErr, ErrNotInstantiated) {
		t.Fatalf("after rejected Instantiate, Call must report ErrNotInstantiated; got: %v", callErr)
	}
}

// TestUnknownNamespaceImportRejected: a module importing a foreign namespace
// (evil.syscall) MUST be rejected at Instantiate — only WASI + helix are linked.
//
// Mutation that kills this: same DefineUnknownImportsAsTraps fail-open as above.
func TestUnknownNamespaceImportRejected(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, unknownNamespaceImportWat))
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}

	err = h.Instantiate()
	if err == nil {
		t.Fatal("SANDBOX BYPASS: module importing foreign 'evil' namespace was admitted at Instantiate")
	}
	if !strings.Contains(err.Error(), "syscall") {
		t.Fatalf("rejection error should name the offending import; got: %v", err)
	}
}

// ----------------------------------------------------------------------------
// CENTERPIECE B — malformed / hostile binary => clean error, never a panic (DoS).
// ----------------------------------------------------------------------------

// TestMalformedBinaryNoPanic feeds a battery of truncated/garbage/hostile byte
// sequences to LoadModule. Every one MUST return an error (or, for a genuinely
// well-formed-but-empty module, succeed) WITHOUT panicking. A panic here is a
// denial-of-service: a hostile plugin author crashes the host process.
//
// Mutation that kills this: replace the `if err != nil { return ... }` guard in
// Host.LoadModule with a panic / ignored error, or have NewModuleFromFile's
// error be unhandled — then a malformed input crashes instead of erroring.
func TestMalformedBinaryNoPanic(t *testing.T) {
	cases := []struct {
		name      string
		data      []byte
		wantError bool // false only for the well-formed empty module
	}{
		{"empty", []byte{}, true},
		{"single-zero-byte", []byte{0x00}, true},
		{"partial-magic", []byte{0x00, 0x61, 0x73}, true},
		{"magic-only-no-version", []byte{0x00, 0x61, 0x73, 0x6d}, true},
		// Valid magic + version with NO sections is a well-formed empty module.
		{"empty-module-valid", []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, false},
		// Valid header, then a garbage/invalid section id.
		{"garbage-section", []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff}, true},
		{"non-wasm-garbage", []byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe}, true},
		{"truncated-real-module", simpleAddWasm[:len(simpleAddWasm)/2], true},
		// A long run of 0xFF after a valid header (stresses the section parser).
		{"long-ff-run", append([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, makeFF(4096)...), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Recover converts any panic into a loud test failure (DoS evidence).
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DoS: LoadModule PANICKED on malformed input %q: %v", tc.name, r)
				}
			}()
			path := writeTempWasm(t, tc.data)
			h, err := NewHost()
			if err != nil {
				t.Fatalf("new host: %v", err)
			}
			defer h.Close()

			loadErr := h.LoadModule(path)
			if tc.wantError && loadErr == nil {
				t.Fatalf("expected LoadModule to reject malformed input %q, got nil error", tc.name)
			}
			if !tc.wantError && loadErr != nil {
				t.Fatalf("well-formed empty module %q should load, got: %v", tc.name, loadErr)
			}
			// A rejected module must remain non-instantiable/non-callable.
			if tc.wantError {
				if instErr := h.Instantiate(); instErr == nil {
					t.Fatalf("malformed input %q produced an instantiable module", tc.name)
				}
			}
		})
	}
}

func makeFF(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xff
	}
	return b
}

// TestMalformedBinaryViaWat2WasmRejected confirms the WAT compiler itself also
// rejects structurally-invalid text — defense in depth for fixtures.
func TestMalformedBinaryViaWat2WasmRejected(t *testing.T) {
	if _, err := wasmtime.Wat2Wasm("(module (this is not wat"); err == nil {
		t.Fatal("expected Wat2Wasm to reject malformed WAT")
	}
}

// ----------------------------------------------------------------------------
// Call-time contract — before-instantiate / missing / non-exported.
// ----------------------------------------------------------------------------

// TestCallBeforeInstantiateNoExecute: Call before Instantiate returns
// ErrNotInstantiated and executes nothing.
//
// Mutation that kills this: drop the `if h.inst == nil` guard in Host.Call —
// then a pre-instantiate call would nil-panic (or silently no-op) instead of
// returning the typed sentinel, failing the errors.Is assertion.
func TestCallBeforeInstantiateNoExecute(t *testing.T) {
	path := writeTempWasm(t, simpleAddWasm)
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	// Deliberately do NOT Instantiate.
	_, callErr := h.Call("add", 10, 32)
	if !errors.Is(callErr, ErrNotInstantiated) {
		t.Fatalf("Call before Instantiate must wrap ErrNotInstantiated; got: %v", callErr)
	}
}

// TestCallMissingFunctionNoExecute: calling a name with no matching export
// returns ErrFunctionNotFound and runs nothing.
//
// Mutation that kills this: drop the `if fn == nil` guard in Host.Call — then a
// missing name nil-panics instead of returning ErrFunctionNotFound.
func TestCallMissingFunctionNoExecute(t *testing.T) {
	path := writeTempWasm(t, simpleAddWasm)
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	_, callErr := h.Call("definitely_not_exported", 1, 2)
	if !errors.Is(callErr, ErrFunctionNotFound) {
		t.Fatalf("Call to missing function must wrap ErrFunctionNotFound; got: %v", callErr)
	}
}

// TestCallNonExportedInternalFunctionRejected: an internal (non-exported)
// function MUST NOT be reachable across the sandbox boundary. Only exports are
// callable; calling the internal $secret returns ErrFunctionNotFound.
//
// Mutation that kills this: if Host.Call resolved non-exported functions (e.g.
// by index rather than GetExport), $secret would run and return 999 — a sandbox
// boundary leak. The errors.Is(ErrFunctionNotFound) assertion catches it.
func TestCallNonExportedInternalFunctionRejected(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, internalHelperWat))
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if _, callErr := h.Call("secret"); !errors.Is(callErr, ErrFunctionNotFound) {
		t.Fatalf("non-exported internal function must be unreachable (ErrFunctionNotFound); got: %v", callErr)
	}
	// The legitimate export still works (and internally drives $secret), proving
	// the module is otherwise healthy — so the rejection above is about the
	// EXPORT boundary, not a broken module.
	got, callErr := h.Call("execute", 0)
	if callErr != nil {
		t.Fatalf("exported execute should succeed: %v", callErr)
	}
	if got != 999 {
		t.Fatalf("execute() = %d, want 999 (internal $secret drives it)", got)
	}
}

// ----------------------------------------------------------------------------
// POSITIVE BASELINE — a genuine module loads, instantiates, calls, AND a
// granted helix import produces its real sink-side effect. Guards against an
// always-reject mutation that would make the rejection tests pass vacuously.
// ----------------------------------------------------------------------------

// TestPositiveBaselineLoadInstantiateCall: the happy path end to end. If a
// mutation made Instantiate always fail (or Call always error), the rejection
// tests above would still pass — this baseline forces admit-when-valid.
func TestPositiveBaselineLoadInstantiateCall(t *testing.T) {
	path := writeTempWasm(t, simpleAddWasm)
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	got, err := h.Call("add", 19, 23)
	if err != nil {
		t.Fatalf("call add: %v", err)
	}
	if got != 42 {
		t.Fatalf("add(19,23) = %d, want 42", got)
	}
}

// TestPositiveBaselineGrantedHelixImportRuns: a genuine "helix" module that
// imports a gated host function (log_write) instantiates AND, with the
// capability granted, produces its real sink-side record. This proves the gate
// ADMITS legitimate Helix modules (the import IS defined) while the centerpiece
// rejects ones reaching for undefined host functions — both sides of the gate.
//
// Mutation that kills this: if defineHostFuncs stopped registering log_write,
// this legitimate module would be rejected at Instantiate (unknown import) and
// no record would be captured — i.e. an over-broad reject. Asserting the record
// is captured forces the host functions to actually be wired.
func TestPositiveBaselineGrantedHelixImportRuns(t *testing.T) {
	h := newCapHost(t, NewCapabilities(CapLog), 0, 1)
	defer h.Close()
	mem := h.Memory()
	if mem == nil {
		t.Fatal("instance exports no memory")
	}
	mem[0] = 3 // opcode: log
	copy(mem[128:133], []byte("HELLO"))

	n, err := h.Call("execute", 0)
	if err != nil {
		t.Fatalf("granted log_write call failed: %v", err)
	}
	if n != 5 {
		t.Fatalf("execute returned %d, want 5", n)
	}
	logs := h.Logs()
	if len(logs) != 1 || logs[0] != "HELLO" {
		t.Fatalf("expected captured log record [HELLO], got %v", logs)
	}
}
