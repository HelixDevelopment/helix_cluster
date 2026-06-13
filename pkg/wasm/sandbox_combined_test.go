package wasm

import (
	"errors"
	"testing"
	"time"
)

// hostileWat is ONE real guest module that simultaneously exercises all three
// isolation bounds enforced by a single Host configuration. A hostile plugin
// would try all three escapes; this guest exposes one export per escape so a
// single host config can be shown to deny EACH of them at once:
//
//   - spin:    an infinite loop. Escapes only if the CPU (fuel) bound is absent.
//   - growto:  grows linear memory by N 64 KiB pages, returning memory.grow's
//     result (-1 if the store memory limiter refuses). Escapes only if
//     the linear-memory cap is absent.
//   - dolog:   imports the gated helix.log_write host function and calls it on
//     the 5 bytes the test preloads at memory[128]. Escapes (captures a
//     host-side record) only if the CapLog capability is granted.
//
// Because all three exports live in ONE module instantiated against ONE Host,
// asserting that each is bounded proves the three limits are enforced together
// (simultaneity), not one-at-a-time on three separate lenient hosts.
const hostileWat = `
(module
  (import "helix" "log_write" (func $log_write (param i32 i32) (result i32)))
  (memory (export "memory") 1)
  (func (export "spin") (result i64)
    (loop $l (br $l))
    (unreachable))
  (func (export "growto") (param $p i64) (result i64)
    (i64.extend_i32_s (memory.grow (i32.wrap_i64 (local.get $p)))))
  (func (export "dolog") (result i64)
    ;; memory[128..133] is preloaded by the test with "HELLO". Returns the host
    ;; func's result: 1 on success (the call traps before returning if denied).
    (i64.extend_i32_s (call $log_write (i32.const 128) (i32.const 5)))))
`

// boundedAddWat is a finite-work guest used to prove fuel is the discriminator
// (not some other limit): it loops a fixed number of times then returns. With
// fuel DISABLED (Limits{Fuel:0}) it must complete — so the only thing that traps
// spin is fuel, not a coincidental bound.
const boundedAddWat = `
(module
  (func (export "boundedadd") (param $n i64) (result i64)
    (local $i i64)
    (local $acc i64)
    (block $done
      (loop $l
        (br_if $done (i64.ge_s (local.get $i) (local.get $n)))
        (local.set $acc (i64.add (local.get $acc) (local.get $i)))
        (local.set $i (i64.add (local.get $i) (i64.const 1)))
        (br $l)))
    (local.get $acc)))
`

// loadHostile loads + instantiates the hostile guest on h. h MUST already have
// its capabilities set (SetCapabilities is bound at instantiation time).
func loadHostile(t *testing.T, h *Host) {
	t.Helper()
	if err := h.LoadModule(writeTempWasm(t, compileWat(t, hostileWat))); err != nil {
		t.Fatalf("load hostile module: %v", err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatalf("instantiate hostile module: %v", err)
	}
}

// callSpinBounded runs the never-returning `spin` export on a deadline-guarded
// goroutine. Because the host under test enables fuel, the call is guaranteed to
// trap and return, so no goroutine is leaked. It returns the call error (which
// must be non-nil: the loop never returns normally).
func callSpinBounded(t *testing.T, h *Host) error {
	t.Helper()
	type res struct{ err error }
	done := make(chan res, 1)
	go func() {
		_, callErr := h.Call("spin")
		done <- res{callErr}
	}()
	select {
	case r := <-done:
		return r.err
	case <-time.After(10 * time.Second):
		t.Fatal("spin was NOT interrupted within 10s; fuel CPU bound not enforced on the combined host")
		return nil
	}
}

// TestSandboxThreeBoundsSimultaneous is the headline simultaneity test. It
// configures a SINGLE Host with all three bounds live at once:
//
//   - Fuel > 0           (CPU bound)
//   - MemoryBytes = 2MiB (linear-memory cap)
//   - empty capability grant (deny-everything gate)
//
// then drives the ONE hostile guest through all three escape attempts and proves
// each is independently refused. This is the core CLAUDE-1 sink-side proof: a
// single hostile plugin on one real host configuration cannot escape ANY of the
// three bounds.
//
// ANTI-BLUFF, per bound:
//   - capability: the deny here is paired with the GRANT case in
//     TestSandboxCapabilityGrantIsDiscriminator below — granting CapLog on the
//     same guest makes dolog succeed and capture a record, so the gate is proven
//     to be neither always-denying nor always-allowing.
//   - fuel: the spin-traps assertion is paired with the fuel-DISABLED completion
//     of a bounded guest in TestSandboxFuelIsDiscriminator — proving fuel (not a
//     coincidental limit) is what stops spin.
//   - memory: the refusal under the 2MiB cap here is paired with success under a
//     generous cap in TestSandboxMemoryCapIsDiscriminator — proving the cap (not
//     something else) is the discriminator.
func TestSandboxThreeBoundsSimultaneous(t *testing.T) {
	// ONE host, all three bounds live simultaneously. Empty grant = deny all caps.
	h, err := NewHostWithLimits(Limits{Fuel: 50_000_000, MemoryBytes: 2 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.SetCapabilities(NewCapabilities()) // deny everything
	loadHostile(t, h)

	// --- Bound 1: capability deny-gate (sink-side: no record captured) ---
	mem := h.Memory()
	copy(mem[128:133], []byte("HELLO"))
	_, err = h.Call("dolog")
	if err == nil {
		t.Fatal("dolog returned nil error while CapLog ungranted; capability gate NOT enforced")
	}
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("dolog error = %v, want wrapped ErrCapabilityDenied", err)
	}
	var cde interface{ Capability() Capability }
	if !errors.As(err, &cde) {
		t.Fatalf("dolog error %v does not expose Capability()", err)
	}
	if cde.Capability() != CapLog {
		t.Fatalf("denied capability = %q, want %q", cde.Capability(), CapLog)
	}
	if logs := h.Logs(); len(logs) != 0 {
		t.Fatalf("capability side effect LEAKED: captured %v while CapLog denied", logs)
	}
	t.Logf("BOUND 1 (capability): dolog denied sink-side, no record captured, Capability()=%q, err=%v", cde.Capability(), err)

	// --- Bound 2: fuel CPU bound (spin must trap, not wedge the host) ---
	spinErr := callSpinBounded(t, h)
	if spinErr == nil {
		t.Fatal("spin returned nil error on the combined host; fuel CPU bound NOT enforced")
	}
	t.Logf("BOUND 2 (fuel): infinite-loop spin trapped by fuel exhaustion: %v", spinErr)

	// --- Bound 3: linear-memory cap (large grow must be refused: -1) ---
	got, err := h.Call("growto", 1000) // ~64 MiB requested under a 2 MiB cap
	if err != nil {
		t.Fatalf("growto under combined host: %v", err)
	}
	if got != -1 {
		t.Fatalf("growto(1000 pages) under 2MiB cap returned %d, want -1; memory cap NOT enforced", got)
	}
	t.Logf("BOUND 3 (memory): growto(1000 pages) refused under 2MiB cap (returned -1)")

	t.Log("SIMULTANEITY: one hostile guest on one Host (fuel>0 + 2MiB cap + empty grant) escaped NONE of the three bounds")
}

// TestSandboxCapabilityGrantIsDiscriminator is the anti-bluff partner for the
// capability bound: the SAME hostile guest, on a host with the same fuel+memory
// limits, is run twice — once with CapLog DENIED and once GRANTED. The grant is
// the only difference, so it is the discriminator: it proves the gate is neither
// always-denying nor always-allowing.
//
// Mutation that this kills: making sandbox.has always return true (or dropping
// the `if !s.has(CapLog)` trap) — then the DENIED run would ALSO capture a
// record, failing the "no record + ErrCapabilityDenied" assertions. Making it
// always-deny would fail the GRANTED run's success + captured-record assertions.
func TestSandboxCapabilityGrantIsDiscriminator(t *testing.T) {
	const limits = "fuel=50M,mem=2MiB"
	mk := func(caps *Capabilities) *Host {
		h, err := NewHostWithLimits(Limits{Fuel: 50_000_000, MemoryBytes: 2 << 20})
		if err != nil {
			t.Fatal(err)
		}
		h.SetCapabilities(caps)
		loadHostile(t, h)
		m := h.Memory()
		copy(m[128:133], []byte("HELLO"))
		return h
	}

	// --- DENIED ---
	hDeny := mk(NewCapabilities())
	defer hDeny.Close()
	_, err := hDeny.Call("dolog")
	if err == nil || !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("denied dolog err = %v, want ErrCapabilityDenied (host %s)", err, limits)
	}
	if logs := hDeny.Logs(); len(logs) != 0 {
		t.Fatalf("denied run leaked record: %v", logs)
	}

	// --- GRANTED (the discriminator) ---
	hGrant := mk(NewCapabilities(CapLog))
	defer hGrant.Close()
	out, err := hGrant.Call("dolog")
	if err != nil {
		t.Fatalf("granted dolog failed: %v (host %s)", err, limits)
	}
	if out != 1 {
		t.Fatalf("granted dolog returned %d, want 1 (log_write success result)", out)
	}
	logs := hGrant.Logs()
	if len(logs) != 1 || logs[0] != "HELLO" {
		t.Fatalf("granted run captured %v, want [HELLO]", logs)
	}
	t.Logf("CAPABILITY discriminator: same guest+host(%s) -> DENIED (no record, ErrCapabilityDenied) vs GRANTED (returned 1, captured %v)", limits, logs)
}

// TestSandboxFuelIsDiscriminator is the anti-bluff partner for the fuel bound.
// It pairs:
//   - spin traps WITH fuel enabled (the hostile guest), and
//   - a bounded-iteration guest COMPLETES with fuel DISABLED (Limits{Fuel:0}).
//
// The disabled-vs-enabled contrast proves fuel — not some coincidental limit —
// is what stops the spin. (We never run the infinite loop with fuel disabled;
// that would hang. We use a large-but-finite loop to show fuel-off lets real
// work finish.)
//
// Mutation that this kills: if Call ignored h.limits.Fuel / never called
// SetFuel, the spin under "fuel enabled" would NOT trap and the deadline in
// callSpinBounded would fire (t.Fatal). If fuel were somehow always-on even when
// disabled, a tight budget could trap the bounded guest — but here fuel=0 and a
// finite loop completes, proving fuel-off is honored.
func TestSandboxFuelIsDiscriminator(t *testing.T) {
	// --- spin traps WITH fuel ---
	hFuel, err := NewHostWithLimits(Limits{Fuel: 50_000_000, MemoryBytes: 2 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer hFuel.Close()
	hFuel.SetCapabilities(NewCapabilities())
	loadHostile(t, hFuel)
	if spinErr := callSpinBounded(t, hFuel); spinErr == nil {
		t.Fatal("spin returned nil with fuel enabled; fuel did not trap the loop")
	} else {
		t.Logf("FUEL discriminator (enabled): spin trapped: %v", spinErr)
	}

	// --- bounded work COMPLETES with fuel DISABLED ---
	hNoFuel, err := NewHostWithLimits(Limits{Fuel: 0, MemoryBytes: 2 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer hNoFuel.Close()
	if err := hNoFuel.LoadModule(writeTempWasm(t, compileWat(t, boundedAddWat))); err != nil {
		t.Fatal(err)
	}
	if err := hNoFuel.Instantiate(); err != nil {
		t.Fatal(err)
	}
	// Sum 0..999999 = 499999500000. A finite loop of 1e6 iterations completes
	// only because fuel is disabled; with a tight fuel budget it would trap.
	const n = 1_000_000
	want := int64(n) * int64(n-1) / 2
	got, err := hNoFuel.Call("boundedadd", n)
	if err != nil {
		t.Fatalf("boundedadd with fuel DISABLED failed: %v (fuel-off not honored)", err)
	}
	if got != want {
		t.Fatalf("boundedadd(%d) = %d, want %d", n, got, want)
	}
	t.Logf("FUEL discriminator (disabled): bounded loop of %d iters completed, sum=%d", n, got)
}

// TestSandboxMemoryCapIsDiscriminator is the anti-bluff partner for the memory
// bound, run against the hostile guest's growto export. The SAME guest is grown
// under a tight cap (refused, -1) and a generous cap (succeeds, >=0). The cap is
// the only difference, so it is the discriminator.
//
// Mutation that this kills: if NewHostWithLimits stopped calling store.Limiter
// (or the cap were ignored), growto under the "tight" host would succeed,
// failing the want==-1 assertion.
func TestSandboxMemoryCapIsDiscriminator(t *testing.T) {
	mk := func(memBytes int64) *Host {
		h, err := NewHostWithLimits(Limits{Fuel: 50_000_000, MemoryBytes: memBytes})
		if err != nil {
			t.Fatal(err)
		}
		h.SetCapabilities(NewCapabilities())
		loadHostile(t, h)
		return h
	}

	// Tight: 2 MiB. Growing ~64 MiB (1000 pages) must be refused.
	hTight := mk(2 << 20)
	defer hTight.Close()
	got, err := hTight.Call("growto", 1000)
	if err != nil {
		t.Fatalf("growto under tight cap: %v", err)
	}
	if got != -1 {
		t.Fatalf("growto(1000) under 2MiB cap = %d, want -1", got)
	}

	// Generous: 256 MiB. The same growth succeeds.
	hLoose := mk(256 << 20)
	defer hLoose.Close()
	got, err = hLoose.Call("growto", 1000)
	if err != nil {
		t.Fatalf("growto under generous cap: %v", err)
	}
	if got < 0 {
		t.Fatalf("growto(1000) under 256MiB cap = %d, want >= 0; cap is not the discriminator", got)
	}
	t.Logf("MEMORY discriminator: same guest -> refused (-1) under 2MiB, succeeded (%d) under 256MiB", got)
}
