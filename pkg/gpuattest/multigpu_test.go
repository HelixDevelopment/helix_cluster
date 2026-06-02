package gpuattest

import (
	"errors"
	"testing"
)

const runUUIDMultiGPU = "gpuattest-HXC1573-3b1f9a44-0c27-4d18-9e6a-71f5c2d80a9e"

// newDevices is a helper that builds n distinct-UUID devices for a node. It
// fails the test (not returns an error) so callers stay terse.
func newDevices(t *testing.T, uuids ...string) []*Device {
	t.Helper()
	devs := make([]*Device, 0, len(uuids))
	for i, u := range uuids {
		d, err := NewDevice("NVIDIA", "H100", u, uint64(80<<30)+uint64(i))
		if err != nil {
			t.Fatalf("NewDevice(%q): %v", u, err)
		}
		devs = append(devs, d)
	}
	return devs
}

// TestEnumerateNode_NProofsIndependentlyVerify is the LOAD-BEARING GUARD for
// HXC-1573: per-device independence + count.
//
// It builds a 3-distinct-UUID node, asserts EnumerateNode returns EXACTLY 3
// proofs each keyed by the matching device UUID, asserts VerifyNode passes on
// the genuine set, and then asserts TWO independent tampers each make VerifyNode
// FAIL:
//  1. mutating one proof's Response signed field (Tick) -> ErrTampered.
//  2. swapping one proof's Descriptor to another device   -> ErrForgedDescriptor.
//
// Mutation-falsifiability:
//   - If EnumerateNode emits fewer/more than N proofs, or mis-keys GPUUUID, the
//     count/key assertions fail.
//   - If EnumerateNode reused ONE challenge for all devices or had a device
//     respond for another device, the per-proof binding would break and either
//     the genuine VerifyNode would fail OR a tamper would NOT be detected.
//   - If VerifyNode stopped verifying each proof independently (e.g. only
//     checked proofs[0]), the tamper-on-proof[2] cases below would no longer
//     fail -> this test fails.
func TestEnumerateNode_NProofsIndependentlyVerify(t *testing.T) {
	wantUUIDs := []string{"GPU-UUID-AAA", "GPU-UUID-BBB", "GPU-UUID-CCC"}
	devs := newDevices(t, wantUUIDs...)
	node := NodeDescriptor{NodeID: "node-helix-7", Devices: devs}

	v := NewVerifier()
	const issueTick, expiryTick, respondTick, nowTick = 100, 200, 150, 160

	proofs, err := EnumerateNode(v, node, issueTick, expiryTick, respondTick)
	if err != nil {
		t.Fatalf("[%s] EnumerateNode genuine: %v", runUUIDMultiGPU, err)
	}

	// Exactly N proofs, each keyed by the matching device UUID in order.
	if len(proofs) != len(wantUUIDs) {
		t.Fatalf("[%s] proof count: got %d want %d", runUUIDMultiGPU, len(proofs), len(wantUUIDs))
	}
	for i, want := range wantUUIDs {
		if proofs[i].GPUUUID != want {
			t.Fatalf("[%s] proof[%d].GPUUUID: got %q want %q", runUUIDMultiGPU, i, proofs[i].GPUUUID, want)
		}
		if proofs[i].Descriptor.UUID != want {
			t.Fatalf("[%s] proof[%d].Descriptor.UUID: got %q want %q", runUUIDMultiGPU, i, proofs[i].Descriptor.UUID, want)
		}
	}

	// Genuine set must verify (before -> after delta: 3 untampered proofs pass).
	if err := VerifyNode(v, proofs, nowTick); err != nil {
		t.Fatalf("[%s] VerifyNode genuine: unexpected error %v", runUUIDMultiGPU, err)
	}
	t.Logf("[%s] genuine: %d proofs each independently VERIFY (UUIDs %v)", runUUIDMultiGPU, len(proofs), wantUUIDs)

	// --- Tamper 1: mutate one proof's Response signed field (Tick). ---
	// A fresh Verifier so the genuine nonces are not already consumed; copy the
	// proofs and corrupt proof[2]'s response Tick, which is a signed field.
	v1 := NewVerifier()
	tampered := make([]DeviceProof, len(proofs))
	copy(tampered, proofs)
	tampered[2].Response.Tick = respondTick + 999 // signed-field mutation
	if err := VerifyNode(v1, tampered, nowTick); err == nil {
		t.Fatalf("[%s] VerifyNode tampered-Tick: got nil, want failure", runUUIDMultiGPU)
	} else if !errors.Is(err, ErrTampered) {
		t.Fatalf("[%s] VerifyNode tampered-Tick: got %v, want ErrTampered", runUUIDMultiGPU, err)
	}
	t.Logf("[%s] tamper#1 Response.Tick %d->%d on proof[2]: VerifyNode now FAILS (ErrTampered)", runUUIDMultiGPU, respondTick, respondTick+999)

	// --- Tamper 2: swap one proof's Descriptor to another device. ---
	v2 := NewVerifier()
	swapped := make([]DeviceProof, len(proofs))
	copy(swapped, proofs)
	// proof[1] keeps its own challenge+response (signed by device B) but now
	// presents device C's descriptor -> fingerprint mismatch.
	swapped[1].Descriptor = proofs[2].Descriptor
	if err := VerifyNode(v2, swapped, nowTick); err == nil {
		t.Fatalf("[%s] VerifyNode swapped-Descriptor: got nil, want failure", runUUIDMultiGPU)
	} else if !errors.Is(err, ErrForgedDescriptor) {
		t.Fatalf("[%s] VerifyNode swapped-Descriptor: got %v, want ErrForgedDescriptor", runUUIDMultiGPU, err)
	}
	t.Logf("[%s] tamper#2 proof[1].Descriptor <- device C: VerifyNode now FAILS (ErrForgedDescriptor)", runUUIDMultiGPU)
}

// TestEnumerateNode_RejectsDuplicateUUID proves duplicate device UUIDs are
// rejected with ErrDuplicateGPUUUID and NO proofs are produced.
//
// Mutation: if the duplicate-UUID guard is removed, EnumerateNode would return 3
// proofs with nil error -> both assertions below fail.
func TestEnumerateNode_RejectsDuplicateUUID(t *testing.T) {
	// Two devices share "GPU-DUP" (distinct key pairs, same descriptor UUID).
	devs := newDevices(t, "GPU-DUP", "GPU-OK", "GPU-DUP")
	node := NodeDescriptor{NodeID: "node-dup", Devices: devs}

	v := NewVerifier()
	proofs, err := EnumerateNode(v, node, 1, 10, 5)
	if !errors.Is(err, ErrDuplicateGPUUUID) {
		t.Fatalf("[%s] duplicate UUID: got err=%v want ErrDuplicateGPUUUID", runUUIDMultiGPU, err)
	}
	if proofs != nil {
		t.Fatalf("[%s] duplicate UUID: got %d proofs, want nil", runUUIDMultiGPU, len(proofs))
	}
	t.Logf("[%s] duplicate-UUID node (3 devices, 2 share GPU-DUP): rejected -> ErrDuplicateGPUUUID, 0 proofs", runUUIDMultiGPU)
}

// TestEnumerateNode_RejectsEmpty proves an empty device list is rejected with
// ErrNoDevices and yields no proofs.
//
// Mutation: if the empty guard is removed, EnumerateNode returns an empty,
// non-nil proof slice with nil error -> assertions below fail.
func TestEnumerateNode_RejectsEmpty(t *testing.T) {
	node := NodeDescriptor{NodeID: "node-empty", Devices: nil}

	v := NewVerifier()
	proofs, err := EnumerateNode(v, node, 1, 10, 5)
	if !errors.Is(err, ErrNoDevices) {
		t.Fatalf("[%s] empty node: got err=%v want ErrNoDevices", runUUIDMultiGPU, err)
	}
	if proofs != nil {
		t.Fatalf("[%s] empty node: got %d proofs, want nil", runUUIDMultiGPU, len(proofs))
	}
	t.Logf("[%s] empty node (0 devices): rejected -> ErrNoDevices, 0 proofs", runUUIDMultiGPU)
}
