package wasm

import (
	"encoding/binary"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytecodealliance/wasmtime-go/v29"
)

// mustWat compiles WAT for benchmarks (testing.B has no compileWat helper).
func mustWat(b *testing.B, wat string) []byte {
	b.Helper()
	out, err := wasmtime.Wat2Wasm(wat)
	if err != nil {
		b.Fatalf("wat2wasm: %v", err)
	}
	return out
}

// writeTempWasmB is the testing.B twin of writeTempWasm.
func writeTempWasmB(b *testing.B, data []byte) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "bench.wasm")
	if err := os.WriteFile(path, data, 0644); err != nil {
		b.Fatalf("write wasm: %v", err)
	}
	return path
}

// capWat is a real guest module that IMPORTS the gated helix host functions and
// exercises one per execute() call, selected by the opcode byte the host writes
// at memory[0] before calling execute.
//
//   - opcode 1 (clock):  call helix.clock_now(ptr=64). The host writes 8 bytes
//     of wall-clock nanoseconds at memory[64]. On success execute returns 8.
//   - opcode 2 (random): call helix.random_fill(ptr=64, n=8). The host fills 8
//     bytes of randomness at memory[64]. On success execute returns 8.
//   - opcode 3 (log):    call helix.log_write(ptr=128, n=5). memory[128..133]
//     is preloaded by the test with "HELLO"; the host captures it as a record.
//     On success execute returns 5.
//
// If the capability is denied the host function traps, execute never returns,
// and NONE of the side effects (memory write / captured record) occur — which
// is exactly what the deny tests assert sink-side.
const capWat = `
(module
  (import "helix" "clock_now"  (func $clock_now  (param i32) (result i32)))
  (import "helix" "random_fill" (func $random_fill (param i32 i32) (result i32)))
  (import "helix" "log_write"  (func $log_write  (param i32 i32) (result i32)))
  (memory (export "memory") 1)
  (func (export "execute") (param $len i64) (result i64)
    (local $op i32)
    (local.set $op (i32.load8_u (i32.const 0)))
    (if (i32.eq (local.get $op) (i32.const 1))
      (then
        (drop (call $clock_now (i32.const 64)))
        (return (i64.const 8))))
    (if (i32.eq (local.get $op) (i32.const 2))
      (then
        (drop (call $random_fill (i32.const 64) (i32.const 8)))
        (return (i64.const 8))))
    (if (i32.eq (local.get $op) (i32.const 3))
      (then
        (drop (call $log_write (i32.const 128) (i32.const 5)))
        (return (i64.const 5))))
    (i64.const 0)))
`

// newCapHost builds an instantiated host running capWat with the given grants
// and deterministic clock/rand seams, returning the host and live memory.
func newCapHost(t *testing.T, caps *Capabilities, clock int64, seed int64) *Host {
	t.Helper()
	path := writeTempWasm(t, compileWat(t, capWat))
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	h.SetCapabilities(caps)
	// Inject deterministic seams so a given (clock,seed) reproduces exactly.
	r := rand.New(rand.NewSource(seed))
	h.sandbox.clock = func() int64 { return clock }
	h.sandbox.rand = func(p []byte) { _, _ = r.Read(p) }
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	return h
}

// TestCapabilityLogDeniedThenGranted is the core anti-bluff test: the SAME
// module is denied when log is ungranted (no record captured) and succeeds when
// granted (real record captured sink-side).
//
// Mutation that kills this: make sandbox.has always return true (or drop the
// `if !s.has(CapLog)` trap in defineHostFuncs.logFn). Then the denied branch
// would ALSO capture a record, failing the "no record + ErrCapabilityDenied"
// assertions below.
func TestCapabilityLogDeniedThenGranted(t *testing.T) {
	// --- DENIED ---
	h := newCapHost(t, NewCapabilities(), 0, 1)
	mem := h.Memory()
	mem[0] = 3 // opcode: log
	copy(mem[128:133], []byte("HELLO"))

	_, err := h.Call("execute", 0)
	if err == nil {
		t.Fatal("expected denial when CapLog ungranted, got nil error")
	}
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("denied error = %v, want wrapped ErrCapabilityDenied", err)
	}
	// Sink-side: NO record may have been captured.
	if logs := h.Logs(); len(logs) != 0 {
		t.Fatalf("side effect leaked: captured %v while CapLog denied", logs)
	}
	h.Close()

	// --- GRANTED ---
	h2 := newCapHost(t, NewCapabilities(CapLog), 0, 1)
	mem2 := h2.Memory()
	mem2[0] = 3
	copy(mem2[128:133], []byte("HELLO"))
	defer h2.Close()

	out, err := h2.Call("execute", 0)
	if err != nil {
		t.Fatalf("granted CapLog execute failed: %v", err)
	}
	if out != 5 {
		t.Fatalf("execute returned %d, want 5", out)
	}
	// Sink-side: exactly the record the guest wrote must be captured.
	logs := h2.Logs()
	if len(logs) != 1 || logs[0] != "HELLO" {
		t.Fatalf("captured logs = %v, want [HELLO] (granted side effect)", logs)
	}
}

// TestCapabilityClockDeniedThenGranted proves clock gating with a memory side
// effect: the host writes 8 LE bytes of the (deterministic) clock at offset 64.
//
// Mutation that kills this: removing the CapClock trap. Then the denied branch
// would write the clock bytes, failing the "memory[64..72] stays zero" check.
func TestCapabilityClockDeniedThenGranted(t *testing.T) {
	const stamp int64 = 0x0102030405060708

	// --- DENIED ---
	h := newCapHost(t, NewCapabilities(), stamp, 1)
	mem := h.Memory()
	mem[0] = 1 // opcode: clock
	_, err := h.Call("execute", 0)
	if err == nil || !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("clock denied err = %v, want ErrCapabilityDenied", err)
	}
	if got := binary.LittleEndian.Uint64(mem[64:72]); got != 0 {
		t.Fatalf("denied clock still wrote 0x%X at offset 64; side effect leaked", got)
	}
	h.Close()

	// --- GRANTED ---
	h2 := newCapHost(t, NewCapabilities(CapClock), stamp, 1)
	mem2 := h2.Memory()
	mem2[0] = 1
	defer h2.Close()
	if _, err := h2.Call("execute", 0); err != nil {
		t.Fatalf("granted clock execute failed: %v", err)
	}
	if got := binary.LittleEndian.Uint64(mem2[64:72]); got != uint64(stamp) {
		t.Fatalf("granted clock wrote 0x%X, want 0x%X", got, uint64(stamp))
	}
}

// TestCapabilityRandomDeniedThenGranted proves random gating AND determinism:
// the same seed reproduces the same guest-visible bytes.
//
// Mutation that kills this: removing the CapRandom trap leaks bytes into the
// denied path (the all-zero assertion fails). Replacing the seeded source with
// an unseeded one breaks the determinism assertion across runs.
func TestCapabilityRandomDeniedThenGranted(t *testing.T) {
	// --- DENIED ---
	h := newCapHost(t, NewCapabilities(), 0, 42)
	mem := h.Memory()
	mem[0] = 2 // opcode: random
	_, err := h.Call("execute", 0)
	if err == nil || !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("random denied err = %v, want ErrCapabilityDenied", err)
	}
	for i := 64; i < 72; i++ {
		if mem[i] != 0 {
			t.Fatalf("denied random wrote byte 0x%02X at %d; side effect leaked", mem[i], i)
		}
	}
	h.Close()

	// --- GRANTED, seed 42, captured twice ---
	want := capturedRandom(t, 42)
	got := capturedRandom(t, 42)
	if string(want) != string(got) {
		t.Fatalf("seed 42 not reproducible: %x vs %x", want, got)
	}
	// And a different seed yields different bytes (rand is actually consulted).
	other := capturedRandom(t, 7)
	if string(want) == string(other) {
		t.Fatalf("seeds 42 and 7 produced identical bytes %x; rand not consulted", want)
	}
}

func capturedRandom(t *testing.T, seed int64) []byte {
	t.Helper()
	h := newCapHost(t, NewCapabilities(CapRandom), 0, seed)
	defer h.Close()
	mem := h.Memory()
	mem[0] = 2
	if _, err := h.Call("execute", 0); err != nil {
		t.Fatalf("granted random execute failed: %v", err)
	}
	out := make([]byte, 8)
	copy(out, mem[64:72])
	// Sink-side proof the hostRand seam was actually consulted and produced real
	// output: an all-zero result would ALSO pass the deny test's "stays zero"
	// check and the reproducibility check (zeros == zeros), so without this
	// assertion a fill loop that ran zero iterations would look correct. Reject
	// it explicitly. seeds 42 and 7 both yield non-zero bytes for the first 8.
	if allZero(out) {
		t.Fatalf("granted random produced all-zero bytes for seed %d; rand seam not consulted", seed)
	}
	return out
}

// allZero reports whether every byte in b is zero.
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// trapWat is a guest that always traps via `unreachable` in execute(). Its trap
// is a stock wasmtime trap ("wasm `unreachable` instruction executed"), NOT a
// capability denial — so Call must NOT wrap it as ErrCapabilityDenied.
const trapWat = `
(module
  (memory (export "memory") 1)
  (func (export "execute") (param i64) (result i64)
    (unreachable)))
`

// TestCapabilityDeniedErrorIsAndChain proves the structured denial error
// contract: errors.Is(err, ErrCapabilityDenied) matches the sentinel, the typed
// error reports which capability was denied, AND the original Wasmtime error
// (with its wasm backtrace and the exact per-capability deny message) stays
// reachable through the Unwrap chain rather than being discarded.
//
// Mutation that kills this: have capabilityDeniedError.Unwrap return only
// ErrCapabilityDenied (dropping orig) — then the err.Error() no longer contains
// the backtrace text and the wantBacktrace assertion fails; or drop the sentinel
// from Unwrap — then errors.Is fails.
func TestCapabilityDeniedErrorIsAndChain(t *testing.T) {
	h := newCapHost(t, NewCapabilities(), 0, 1)
	defer h.Close()
	mem := h.Memory()
	mem[0] = 1 // opcode: clock — denied (no CapClock)

	_, err := h.Call("execute", 0)
	if err == nil {
		t.Fatal("expected capability-denied error, got nil")
	}
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("errors.Is(err, ErrCapabilityDenied) = false; err = %v", err)
	}
	// Typed error must report the exact denied capability.
	var cde *capabilityDeniedError
	if !errors.As(err, &cde) {
		t.Fatalf("errors.As(*capabilityDeniedError) = false; err = %v", err)
	}
	if cde.Capability() != CapClock {
		t.Fatalf("denied capability = %q, want %q", cde.Capability(), CapClock)
	}
	// The original Wasmtime error must remain reachable THROUGH THE UNWRAP CHAIN
	// (not merely re-stringified in Error()): the multi-Unwrap slice must carry
	// both the sentinel and a distinct non-sentinel error bearing the backtrace.
	parts := cde.Unwrap()
	var sawSentinel, sawOrig bool
	for _, p := range parts {
		if p == ErrCapabilityDenied { //nolint:errorlint // identity check is intentional
			sawSentinel = true
		} else if strings.Contains(p.Error(), "wasm backtrace") {
			sawOrig = true
		}
	}
	if !sawSentinel {
		t.Fatalf("Unwrap chain missing ErrCapabilityDenied sentinel: %v", parts)
	}
	if !sawOrig {
		t.Fatalf("Unwrap chain dropped original wasmtime error (no backtrace): %v", parts)
	}
	// And the exact per-capability deny message must survive in the surfaced error.
	if !strings.Contains(err.Error(), capDeniedMessage(CapClock)) {
		t.Fatalf("denial error missing exact message %q: %q", capDeniedMessage(CapClock), err.Error())
	}
}

// TestNonDenialTrapNotMisclassified proves a stock guest trap (unreachable) is
// NOT wrapped as a capability denial — guarding against the fragile-substring
// failure mode the structural Trap.Message() prefix check was added to prevent.
//
// Mutation that kills this: revert Call to sniff the full err.Error() string for
// ErrCapabilityDenied text, or drop the capDeniedPrefix guard so ANY trap is
// classified as a denial; then this errors.Is must-be-false assertion fails.
func TestNonDenialTrapNotMisclassified(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, trapWat))
	h, err := NewHost()
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	defer h.Close()
	// Grant everything: a denial is impossible, so the only trap is unreachable.
	h.SetCapabilities(NewCapabilities(CapClock, CapRandom, CapLog))
	if err := h.LoadModule(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	_, err = h.Call("execute", 0)
	if err == nil {
		t.Fatal("expected unreachable trap, got nil")
	}
	if errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("stock unreachable trap misclassified as ErrCapabilityDenied: %v", err)
	}
}

// TestDefaultGrantPreservesExecuteOutput proves an empty/default grant set
// leaves the existing Execute transform path byte-identical. This is the
// backward-compatibility guarantee.
//
// Mutation that kills this: if defineHostFuncs returned an error for the
// default sandbox, or SetCapabilities(nil) granted everything and altered the
// transform module's behavior, this exact-output check would fail.
func TestDefaultGrantPreservesExecuteOutput(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, transformWat))
	p := NewWasmPlugin(path) // default == NewCapabilities() (empty)
	if err := p.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	defer p.Shutdown()

	out, err := p.Execute([]byte("hello"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(out) != "ifmmp" {
		t.Fatalf("default-grant execute = %q, want %q (transform unchanged)", out, "ifmmp")
	}
}

// TestPluginWithCapabilitiesGrantsLog wires the capability through the public
// WasmPlugin.WithCapabilities path and asserts the sink-side log record.
//
// Mutation that kills this: WithCapabilities not threading p.caps into
// SetCapabilities at Init would leave CapLog ungranted, so Execute would error
// and Host().Logs() would be empty.
func TestPluginWithCapabilitiesGrantsLog(t *testing.T) {
	path := writeTempWasm(t, compileWat(t, capWat))
	p := NewWasmPlugin(path).WithCapabilities(NewCapabilities(CapLog))
	if err := p.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	defer p.Shutdown()

	mem := p.host.Memory()
	mem[0] = 3
	copy(mem[128:133], []byte("HELLO"))

	out, err := p.Execute(nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(out) != 5 {
		t.Fatalf("execute output len = %d, want 5", len(out))
	}
	logs := p.Host().Logs()
	if len(logs) != 1 || logs[0] != "HELLO" {
		t.Fatalf("plugin logs = %v, want [HELLO]", logs)
	}
}

// TestCapabilitiesGrantedDeterministicOrder asserts Granted() is sorted, so DST
// replays enumerate capabilities identically.
//
// Mutation that kills this: dropping the sort.Slice in Granted() makes map
// iteration order nondeterministic and this exact-slice check flaky/failing.
func TestCapabilitiesGrantedDeterministicOrder(t *testing.T) {
	c := NewCapabilities(CapNetwork, CapClock, CapLog, CapFS, CapRandom)
	got := c.Granted()
	want := []Capability{CapClock, CapFS, CapLog, CapNetwork, CapRandom}
	if len(got) != len(want) {
		t.Fatalf("granted len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("granted[%d] = %q, want %q (order not deterministic)", i, got[i], want[i])
		}
	}
}

// BenchmarkSpawnInstantiateExecute measures full spawn throughput: a fresh
// host + load + instantiate + one execute per iteration. This is the
// instantiate+execute cost a scheduler pays per plugin invocation.
func BenchmarkSpawnInstantiateExecute(b *testing.B) {
	wasm := mustWat(b, transformWat)
	path := writeTempWasmB(b, wasm)
	in := []byte("hello")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := NewWasmPlugin(path)
		if err := p.Init(); err != nil {
			b.Fatalf("init: %v", err)
		}
		out, err := p.Execute(in)
		if err != nil {
			b.Fatalf("execute: %v", err)
		}
		if len(out) != len(in) {
			b.Fatalf("execute output len = %d, want %d", len(out), len(in))
		}
		_ = p.Shutdown()
	}
}

// BenchmarkSpawnExecuteHot measures execute-only throughput on an already
// instantiated module (the steady-state hot path).
func BenchmarkSpawnExecuteHot(b *testing.B) {
	wasm := mustWat(b, transformWat)
	path := writeTempWasmB(b, wasm)
	p := NewWasmPlugin(path)
	if err := p.Init(); err != nil {
		b.Fatalf("init: %v", err)
	}
	defer p.Shutdown()
	in := []byte("hello")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Execute(in); err != nil {
			b.Fatalf("execute: %v", err)
		}
	}
}
