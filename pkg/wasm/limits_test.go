package wasm

import (
	"testing"
	"time"
)

// spinWat is a guest whose exported `spin` never returns (infinite loop). With a
// fuel budget it is guaranteed to trap when fuel is exhausted, so feeding it to
// a fuel-limited host can never hang the test.
const spinWat = `
(module
  (func (export "spin") (result i64)
    (loop $l (br $l))
    (unreachable)))`

// growWat grows linear memory by the requested number of 64 KiB pages and
// returns the result of memory.grow (-1 if the growth was refused, e.g. by the
// store memory limiter).
const growWat = `
(module
  (memory (export "mem") 1)
  (func (export "growto") (param $p i64) (result i64)
    (i64.extend_i32_s (memory.grow (i32.wrap_i64 (local.get $p))))))`

// TestFuelLimitInterruptsInfiniteLoop proves the CPU bound: an infinite-loop
// plugin is interrupted (traps) by fuel exhaustion instead of wedging the host.
// The call runs on a goroutine guarded by a deadline; because fuel is enabled it
// is guaranteed to return, so no spinning goroutine is leaked.
func TestFuelLimitInterruptsInfiniteLoop(t *testing.T) {
	h, err := NewHost() // DefaultLimits: fuel enabled
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.LoadModule(writeTempWasm(t, compileWat(t, spinWat))); err != nil {
		t.Fatal(err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatal(err)
	}

	type res struct{ err error }
	done := make(chan res, 1)
	go func() {
		_, callErr := h.Call("spin")
		done <- res{callErr}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("infinite-loop plugin returned nil error; fuel limit did not interrupt it")
		}
		t.Logf("infinite loop interrupted by fuel limit: %v", r.err)
	case <-time.After(10 * time.Second):
		t.Fatal("infinite-loop plugin was NOT interrupted within 10s; fuel CPU bound not enforced")
	}
}

// TestFuelIsActuallyConsumed proves fuel accounting is live (not a no-op): a
// real call leaves strictly less fuel than the budget it started with.
func TestFuelIsActuallyConsumed(t *testing.T) {
	limits := DefaultLimits()
	h, err := NewHostWithLimits(limits)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	// A trivial add module from the existing test corpus pattern.
	if err := h.LoadModule(writeTempWasm(t, compileWat(t, `
(module (func (export "add") (param i64 i64) (result i64)
  (i64.add (local.get 0) (local.get 1))))`))); err != nil {
		t.Fatal(err)
	}
	if err := h.Instantiate(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Call("add", 2, 3); err != nil {
		t.Fatalf("add call: %v", err)
	}
	remaining, err := h.store.GetFuel()
	if err != nil {
		t.Fatalf("get fuel: %v", err)
	}
	if remaining >= limits.Fuel {
		t.Fatalf("fuel not consumed: remaining %d >= budget %d (fuel accounting is a no-op)", remaining, limits.Fuel)
	}
	t.Logf("fuel consumed by add(2,3): %d of %d", limits.Fuel-remaining, limits.Fuel)
}

// TestMemoryLimitCapsGrowth proves the memory bound: under a tight cap a large
// memory.grow is refused (-1), while under a generous cap the same growth
// succeeds — so the limit is genuinely load-bearing (the anti-bluff pair).
func TestMemoryLimitCapsGrowth(t *testing.T) {
	wasm := compileWat(t, growWat)

	// Tight cap: 2 MiB. Growing by 1000 pages (~64 MiB) must be refused.
	tight, err := NewHostWithLimits(Limits{Fuel: DefaultLimits().Fuel, MemoryBytes: 2 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer tight.Close()
	if err := tight.LoadModule(writeTempWasm(t, wasm)); err != nil {
		t.Fatal(err)
	}
	if err := tight.Instantiate(); err != nil {
		t.Fatal(err)
	}
	got, err := tight.Call("growto", 1000)
	if err != nil {
		t.Fatalf("growto under tight cap: %v", err)
	}
	if got != -1 {
		t.Fatalf("growto(1000 pages) under 2MiB cap returned %d, want -1 (growth must be refused)", got)
	}

	// Generous cap: 256 MiB. The same growth succeeds (returns the prior page count >= 0).
	loose, err := NewHostWithLimits(Limits{Fuel: DefaultLimits().Fuel, MemoryBytes: 256 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer loose.Close()
	if err := loose.LoadModule(writeTempWasm(t, wasm)); err != nil {
		t.Fatal(err)
	}
	if err := loose.Instantiate(); err != nil {
		t.Fatal(err)
	}
	got, err = loose.Call("growto", 1000)
	if err != nil {
		t.Fatalf("growto under generous cap: %v", err)
	}
	if got < 0 {
		t.Fatalf("growto(1000 pages) under 256MiB cap returned %d, want >= 0 (growth should succeed); the cap is not actually the discriminator", got)
	}
	t.Logf("memory cap is load-bearing: refused under 2MiB (-1), succeeded under 256MiB (%d)", got)
}
