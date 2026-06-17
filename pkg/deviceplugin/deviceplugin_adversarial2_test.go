package deviceplugin

// deviceplugin_adversarial2_test.go — second-wave adversarial probes targeting
// the ALIASING bug class that the first-wave suite (which concluded "NO BUG")
// did not cover.
//
// Contract under test:
//   - Registry "tracks which devices are healthy/allocatable" and is the
//     authoritative inventory. A Device returned by Device()/Inventory() is a
//     read-only VIEW the caller must not be able to weaponise against committed
//     state; and a Fingerprint the caller hands to ApplyFingerprint is COMMITTED
//     — the registry owns its copy, not the caller's mutable buffer.
//
// REAL BUG FOUND + FIXED (see report): Device.Capabilities is a []string. Before
// the fix, ApplyFingerprint stored the caller's slice header verbatim
// (r.devices[d.ID] = d), and Device()/Inventory() returned the stored struct by
// value — so the Capabilities backing array was SHARED with the registry. A
// caller mutating a returned device's Capabilities (or mutating the slice it had
// passed to ApplyFingerprint) silently corrupted committed inventory. The fix
// clones Capabilities at both the ingest and read boundaries (cloneDevice).
//
// Each probe below is mutation-survivable: it FAILS on the pre-fix code (shared
// backing array) and PASSES once Capabilities is cloned. None of these depend on
// -race; they assert sink-side state divergence directly.

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// withTimeout runs fn and fails the test if it does not complete within d. This
// guards the concurrent probe from ever hanging the package test binary.
func withTimeout(t *testing.T, d time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not complete within %s — possible deadlock/hang", name, d)
	}
}

// TestAdversarial2_InventoryDoesNotAliasInternalCapabilities proves a caller
// mutating the Capabilities slice of an Inventory() result cannot reach back and
// corrupt the registry's committed copy.
//
// Mutation guard: with the pre-fix `out = append(out, d)` (no clone), inv[0]'s
// Capabilities aliases the stored device, so the mutation below is visible via a
// fresh Device() read and the final assertion FAILS.
func TestAdversarial2_InventoryDoesNotAliasInternalCapabilities(t *testing.T) {
	reg := NewRegistry()
	orig := healthyGPU("alias-inv-0") // Capabilities: ["fp16","tensor-cores"]
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "alias-plugin",
		ResourceKind: "gpu",
		Devices:      []Device{orig},
	}); err != nil {
		t.Fatalf("ApplyFingerprint: %v", err)
	}

	inv := reg.Inventory()
	if len(inv) != 1 || len(inv[0].Capabilities) == 0 {
		t.Fatalf("unexpected inventory shape: %#v", inv)
	}
	// Hostile caller mutates the returned snapshot's Capabilities.
	inv[0].Capabilities[0] = "CORRUPTED"

	// The registry's committed copy must be untouched.
	got, ok := reg.Device("alias-inv-0")
	if !ok {
		t.Fatalf("device vanished")
	}
	if got.Capabilities[0] == "CORRUPTED" {
		t.Fatalf("ALIASING: mutating Inventory() result corrupted registry state (cap[0]=%q) — Inventory aliases internal Capabilities", got.Capabilities[0])
	}
	if got.Capabilities[0] != "fp16" {
		t.Fatalf("committed Capabilities[0]=%q, want %q", got.Capabilities[0], "fp16")
	}
}

// TestAdversarial2_DeviceDoesNotAliasInternalCapabilities proves the same for the
// single-device Device() accessor: two independent reads must not share a backing
// array such that mutating one corrupts the other (and the store).
//
// Mutation guard: pre-fix Device() returned the stored struct by value, sharing
// the Capabilities backing array; the cross-read corruption below is then visible
// and this test FAILS.
func TestAdversarial2_DeviceDoesNotAliasInternalCapabilities(t *testing.T) {
	reg := NewRegistry()
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "alias-plugin",
		ResourceKind: "gpu",
		Devices:      []Device{healthyGPU("alias-dev-0")},
	}); err != nil {
		t.Fatalf("ApplyFingerprint: %v", err)
	}

	a, ok := reg.Device("alias-dev-0")
	if !ok || len(a.Capabilities) == 0 {
		t.Fatalf("unexpected device: %#v ok=%v", a, ok)
	}
	a.Capabilities[0] = "CORRUPTED"

	b, _ := reg.Device("alias-dev-0")
	if b.Capabilities[0] == "CORRUPTED" {
		t.Fatalf("ALIASING: mutating one Device() result corrupted a later Device() read (cap[0]=%q) — Device aliases internal Capabilities", b.Capabilities[0])
	}
}

// TestAdversarial2_ApplyFingerprintCopiesCallerCapabilities proves the INGEST
// boundary is defended: after ApplyFingerprint returns, the plugin may reuse /
// mutate the Device.Capabilities slice it passed (a common scratch-buffer
// pattern) without that mutation leaking into committed inventory.
//
// Mutation guard: pre-fix `r.devices[d.ID] = d` stored the caller's slice header,
// so the post-Apply mutation below reaches committed state and this test FAILS.
func TestAdversarial2_ApplyFingerprintCopiesCallerCapabilities(t *testing.T) {
	reg := NewRegistry()
	caps := []string{"fp16", "tensor-cores"}
	dev := Device{
		ID:           "ingest-0",
		Model:        "rtx3080",
		Health:       HealthHealthy,
		Capabilities: caps,
	}
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "alias-plugin",
		ResourceKind: "gpu",
		Devices:      []Device{dev},
	}); err != nil {
		t.Fatalf("ApplyFingerprint: %v", err)
	}

	// Caller reuses its buffer after the fingerprint was committed.
	caps[0] = "REUSED_BUFFER"

	got, ok := reg.Device("ingest-0")
	if !ok {
		t.Fatalf("device vanished")
	}
	if got.Capabilities[0] == "REUSED_BUFFER" {
		t.Fatalf("ALIASING: mutating caller's slice after ApplyFingerprint corrupted committed inventory (cap[0]=%q) — ingest stores caller's backing array", got.Capabilities[0])
	}
	if got.Capabilities[0] != "fp16" {
		t.Fatalf("committed Capabilities[0]=%q, want %q", got.Capabilities[0], "fp16")
	}
}

// TestAdversarial2_ConcurrentReadersMutateSnapshotsNoRaceNoCorruption hammers the
// read boundary: many goroutines pull Inventory()/Device() snapshots and mutate
// their Capabilities, while one goroutine continuously re-fingerprints. With the
// clone in place each snapshot is private, so the writer's committed values stay
// canonical. The probe is timeout-guarded so it can never hang the suite.
//
// This also gives a single, bounded concurrency exercise of the aliasing fix:
// pre-fix, the reader mutations and the writer's stores share backing arrays and
// the final canonical-state assertion FAILS (and -race would also flag it).
func TestAdversarial2_ConcurrentReadersMutateSnapshotsNoRaceNoCorruption(t *testing.T) {
	const (
		k       = 8
		readers = 24
		rounds  = 200
	)
	reg := NewRegistry()
	base := make([]Device, k)
	for i := 0; i < k; i++ {
		base[i] = healthyGPU(fmt.Sprintf("conc-alias-%02d", i))
	}
	if err := reg.ApplyFingerprint(Fingerprint{
		PluginName:   "alias-plugin",
		ResourceKind: "gpu",
		Devices:      base,
	}); err != nil {
		t.Fatalf("seed ApplyFingerprint: %v", err)
	}

	withTimeout(t, 30*time.Second, "concurrent aliasing storm", func() {
		var wg sync.WaitGroup
		stop := make(chan struct{})

		// Writer: continuously re-commit the canonical full set. Each call passes a
		// FRESH copy of the devices (as a real plugin streaming would), so the only
		// way committed Capabilities could become corrupted is via a shared backing
		// array surfaced by the read path.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				fresh := make([]Device, k)
				for i := 0; i < k; i++ {
					fresh[i] = healthyGPU(fmt.Sprintf("conc-alias-%02d", i))
				}
				_ = reg.ApplyFingerprint(Fingerprint{
					PluginName:   "alias-plugin",
					ResourceKind: "gpu",
					Devices:      fresh,
				})
			}
		}()

		// Readers: snapshot and vandalise their own copies.
		for g := 0; g < readers; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for r := 0; r < rounds; r++ {
					for _, d := range reg.Inventory() {
						for i := range d.Capabilities {
							d.Capabilities[i] = "VANDAL"
						}
					}
					if d, ok := reg.Device(fmt.Sprintf("conc-alias-%02d", r%k)); ok {
						for i := range d.Capabilities {
							d.Capabilities[i] = "VANDAL"
						}
					}
				}
			}()
		}

		// Let readers run, then stop the writer and drain.
		time.Sleep(50 * time.Millisecond)
		close(stop)
		wg.Wait()
	})

	// Sink: every committed device must still carry its canonical, un-vandalised
	// capabilities. Any "VANDAL" leak proves a shared backing array.
	for _, d := range reg.Inventory() {
		want := []string{"fp16", "tensor-cores"}
		if len(d.Capabilities) != len(want) {
			t.Fatalf("device %q committed Capabilities=%v, want %v", d.ID, d.Capabilities, want)
		}
		for i := range want {
			if d.Capabilities[i] != want[i] {
				t.Fatalf("ALIASING under concurrency: device %q Capabilities[%d]=%q, want %q — reader mutation leaked into committed state", d.ID, i, d.Capabilities[i], want[i])
			}
		}
	}
}
