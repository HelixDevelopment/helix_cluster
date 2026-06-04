// Package wasm provides a WebAssembly plugin runtime using Wasmtime for Helix Cluster OS.
package wasm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"

	"github.com/bytecodealliance/wasmtime-go/v29"
)

// Capability names a single host resource a WASM module may be permitted to
// touch. Capabilities are deny-by-default: a module may only use a capability
// that has been explicitly granted to it via Capabilities.
type Capability string

const (
	// CapClock allows the guest to read host wall-clock time via helix.clock_now.
	CapClock Capability = "clock"
	// CapRandom allows the guest to draw host randomness via helix.random_fill.
	CapRandom Capability = "random"
	// CapLog allows the guest to emit log records via helix.log_write.
	CapLog Capability = "log"
	// CapFS allows guest filesystem access (reserved; gated host seam below).
	CapFS Capability = "fs"
	// CapNetwork allows guest network access (reserved; gated host seam below).
	CapNetwork Capability = "network"
)

// ErrCapabilityDenied is wrapped into the error surfaced from Execute/Call when
// a guest invokes a host function whose capability was not granted. Callers can
// match it with errors.Is. The same string is embedded in the Wasmtime trap
// message so the denial is legible end to end.
var ErrCapabilityDenied = errors.New("capability denied")

// allCapabilities is the closed set of gated capabilities. matchDeniedCapability
// scans it so denial detection keys off the EXACT per-capability message rather
// than a loose substring. Keep in sync with the Cap* constants.
var allCapabilities = []Capability{CapClock, CapRandom, CapLog, CapFS, CapNetwork}

// capDeniedMessage builds the human-readable trap/deny message for a capability.
// It MUST begin with ErrCapabilityDenied.Error() so the sentinel stays legible
// end to end, and it is unique per capability so denial detection is precise.
func capDeniedMessage(c Capability) string {
	return fmt.Sprintf("%s: capability %q not granted", ErrCapabilityDenied.Error(), string(c))
}

// matchDeniedCapability reports the capability denied by a Wasmtime call error,
// if any. It matches the EXACT capDeniedMessage(cap) string for a known
// capability anywhere in err.Error() (Wasmtime embeds the host-func trap text
// under a "Caused by:" line). Because the match requires the full, structured
// per-capability message — not a bare "capability denied" substring — an
// unrelated guest trap whose text incidentally contains "capability denied"
// cannot be misclassified as a denial.
func matchDeniedCapability(err error) (Capability, bool) {
	if err == nil {
		return "", false
	}
	msg := err.Error()
	for _, c := range allCapabilities {
		if strings.Contains(msg, capDeniedMessage(c)) {
			return c, true
		}
	}
	return "", false
}

// Capabilities is an explicit allow-list of host resources a module may use.
// The zero value (and a nil *Capabilities) grants nothing, preserving the
// historical behavior where modules only ever touched WASI + their own memory.
type Capabilities struct {
	granted map[Capability]bool
}

// NewCapabilities returns an allow-list pre-populated with caps. Passing no
// arguments yields an empty (deny-everything) grant set.
func NewCapabilities(caps ...Capability) *Capabilities {
	c := &Capabilities{granted: make(map[Capability]bool, len(caps))}
	for _, cap := range caps {
		c.granted[cap] = true
	}
	return c
}

// Grant adds cap to the allow-list and returns the receiver for chaining.
func (c *Capabilities) Grant(cap Capability) *Capabilities {
	if c.granted == nil {
		c.granted = make(map[Capability]bool)
	}
	c.granted[cap] = true
	return c
}

// Has reports whether cap is granted. A nil receiver grants nothing.
func (c *Capabilities) Has(cap Capability) bool {
	if c == nil || c.granted == nil {
		return false
	}
	return c.granted[cap]
}

// Granted returns the granted capabilities in deterministic (sorted) order.
// Deterministic ordering matters for DST: a run replayed with the same grant
// set must enumerate capabilities identically.
func (c *Capabilities) Granted() []Capability {
	if c == nil {
		return nil
	}
	out := make([]Capability, 0, len(c.granted))
	for cap := range c.granted {
		out = append(out, cap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// String renders the grant set deterministically, e.g. "wasm.Capabilities{clock,log}".
func (c *Capabilities) String() string {
	g := c.Granted()
	parts := make([]string, len(g))
	for i, cap := range g {
		parts[i] = string(cap)
	}
	return "wasm.Capabilities{" + strings.Join(parts, ",") + "}"
}

// hostClock supplies wall-clock nanoseconds to the gated clock_now host func.
// It is a seam: production uses time.Now via the default; DST/tests inject a
// deterministic source so a seeded run reproduces the same guest-visible value.
type hostClock func() int64

// hostRand fills p with bytes for the gated random_fill host func. The default
// uses math/rand seeded explicitly so DST runs are reproducible.
type hostRand func(p []byte)

// sandbox bundles the capability grant set with the host-side resource seams
// the gated host functions draw from. A nil *sandbox denies everything.
type sandbox struct {
	caps  *Capabilities
	clock hostClock
	rand  hostRand
	// logs captures records written through the granted log_write host func so
	// tests (and callers) can assert the real sink-side side effect occurred.
	logs *[]string
}

// defaultSandbox returns a deny-everything sandbox with deterministic seams,
// suitable as the backward-compatible default for existing modules.
func defaultSandbox() *sandbox {
	logs := make([]string, 0)
	r := rand.New(rand.NewSource(1)) //gosec:disable G404 -- explicit seed for DST-reproducible default; deterministic by design, not security-sensitive
	// *rand.Rand is NOT goroutine-safe (documented since Go 1). Guard it with a
	// mutex so concurrent guest calls drawing host randomness do not race on the
	// shared source. The lock is uncontended on the single-goroutine fast path.
	var mu sync.Mutex
	return &sandbox{
		caps:  NewCapabilities(),
		clock: func() int64 { return 0 },
		rand: func(p []byte) {
			mu.Lock()
			defer mu.Unlock()
			_, _ = r.Read(p)
		},
		logs: &logs,
	}
}

// has reports whether cap is granted by this sandbox (nil-safe).
func (s *sandbox) has(cap Capability) bool {
	if s == nil {
		return false
	}
	return s.caps.Has(cap)
}

// callerMemory returns the calling instance's exported linear memory bytes, or
// nil if the caller exports none. Host functions use this to deliver real,
// observable side effects into guest memory.
func callerMemory(c *wasmtime.Caller) []byte {
	ext := c.GetExport("memory")
	if ext == nil {
		return nil
	}
	mem := ext.Memory()
	if mem == nil {
		return nil
	}
	return mem.UnsafeData(c)
}

// defineHostFuncs registers the gated "helix" host functions on linker. Every
// function checks its capability FIRST and returns a Wasmtime trap carrying
// capDeniedMessage when the capability is absent — so the guest call fails and
// the function's side effect never runs. When granted, each performs a real,
// guest-observable side effect (a memory write or a captured log record).
func (s *sandbox) defineHostFuncs(linker *wasmtime.Linker) error {
	// helix.clock_now(ptr i32) -> i32(ok): writes 8 little-endian bytes of host
	// nanoseconds at guest memory[ptr]. Returns 1 on success; traps if denied.
	clockFn := func(caller *wasmtime.Caller, ptr int32) (int32, *wasmtime.Trap) {
		if !s.has(CapClock) {
			return 0, wasmtime.NewTrap(capDeniedMessage(CapClock))
		}
		mem := callerMemory(caller)
		if mem == nil || int(ptr)+8 > len(mem) {
			return 0, wasmtime.NewTrap("clock_now: bad memory range")
		}
		binary.LittleEndian.PutUint64(mem[ptr:ptr+8], uint64(s.clock())) //gosec:disable G115 -- full-width int64->uint64 bit-pattern for the guest clock word; no truncation
		return 1, nil
	}
	if err := linker.FuncWrap("helix", "clock_now", clockFn); err != nil {
		return fmt.Errorf("define helix.clock_now: %w", err)
	}

	// helix.random_fill(ptr i32, n i32) -> i32(ok): fills n guest bytes at ptr
	// with host randomness. Traps if denied — guest memory is left untouched.
	randomFn := func(caller *wasmtime.Caller, ptr, n int32) (int32, *wasmtime.Trap) {
		if !s.has(CapRandom) {
			return 0, wasmtime.NewTrap(capDeniedMessage(CapRandom))
		}
		if n < 0 {
			return 0, wasmtime.NewTrap("random_fill: negative length")
		}
		mem := callerMemory(caller)
		if mem == nil || int(ptr)+int(n) > len(mem) {
			return 0, wasmtime.NewTrap("random_fill: bad memory range")
		}
		buf := make([]byte, n)
		s.rand(buf)
		copy(mem[ptr:int(ptr)+int(n)], buf)
		return 1, nil
	}
	if err := linker.FuncWrap("helix", "random_fill", randomFn); err != nil {
		return fmt.Errorf("define helix.random_fill: %w", err)
	}

	// helix.log_write(ptr i32, n i32) -> i32(ok): captures n guest bytes at ptr
	// as a log record on the host. Traps if denied — no record is captured.
	logFn := func(caller *wasmtime.Caller, ptr, n int32) (int32, *wasmtime.Trap) {
		if !s.has(CapLog) {
			return 0, wasmtime.NewTrap(capDeniedMessage(CapLog))
		}
		if n < 0 {
			return 0, wasmtime.NewTrap("log_write: negative length")
		}
		mem := callerMemory(caller)
		if mem == nil || int(ptr)+int(n) > len(mem) {
			return 0, wasmtime.NewTrap("log_write: bad memory range")
		}
		rec := string(mem[ptr : int(ptr)+int(n)])
		if s.logs != nil {
			*s.logs = append(*s.logs, rec)
		}
		return 1, nil
	}
	if err := linker.FuncWrap("helix", "log_write", logFn); err != nil {
		return fmt.Errorf("define helix.log_write: %w", err)
	}

	return nil
}

// Logs returns a copy of the records captured through the granted log_write
// host function on this host's sandbox, in write order. Returns nil if no
// sandbox is configured.
func (h *Host) Logs() []string {
	if h.sandbox == nil || h.sandbox.logs == nil {
		return nil
	}
	out := make([]string, len(*h.sandbox.logs))
	copy(out, *h.sandbox.logs)
	return out
}
