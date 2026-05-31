package device

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// TestProvision_WalksExactFSMPath asserts the device transitions through the
// exact ordered FSM path with recorded events.
//
// Mutation that kills it: in Provision, skip the StateProvisioning transition
// (go Requested->Ready directly) -> the recorded To-sequence no longer equals
// [Requested, Provisioning, Ready] and the test fails.
func TestProvision_WalksExactFSMPath(t *testing.T) {
	p := NewSoftwareProvisioner(42)
	d, err := p.Provision(context.Background(), DeviceSpec{Tier: "T3", Arch: "x86_64", MemoryGB: 4})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got := p.Status(d); got != StateReady {
		t.Fatalf("status after provision = %s, want Ready", got)
	}
	want := []State{StateRequested, StateProvisioning, StateReady}
	evs := d.Events()
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(evs), len(want), evs)
	}
	for i, w := range want {
		if evs[i].To != w {
			t.Errorf("event %d To = %s, want %s", i, evs[i].To, w)
		}
	}
	// Origin event self-loops; the rest must chain From==prev.To.
	for i := 1; i < len(evs); i++ {
		if evs[i].From != evs[i-1].To {
			t.Errorf("event %d From=%s does not chain prev To=%s", i, evs[i].From, evs[i-1].To)
		}
	}
}

// TestRelease_WalksReleasingReleased asserts Release drives Ready/InUse through
// Releasing to Released.
//
// Mutation that kills it: in Release, return nil before the StateReleased
// transition -> final state stays Releasing and the want check fails.
func TestRelease_WalksReleasingReleased(t *testing.T) {
	p := NewSoftwareProvisioner(7)
	d, err := p.Provision(context.Background(), DeviceSpec{Tier: "T1"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := p.Release(context.Background(), d); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := p.Status(d); got != StateReleased {
		t.Fatalf("status after release = %s, want Released", got)
	}
	evs := d.Events()
	last2 := evs[len(evs)-2:]
	if last2[0].To != StateReleasing || last2[1].To != StateReleased {
		t.Errorf("tail transitions = [%s,%s], want [Releasing,Released]", last2[0].To, last2[1].To)
	}
}

// TestDoubleRelease_Errors asserts releasing a terminal device is rejected.
//
// Mutation that kills it: drop the legalTransitions guard (allow any edge) in
// transition() -> the second Release succeeds and err is nil, failing the test.
func TestDoubleRelease_Errors(t *testing.T) {
	p := NewSoftwareProvisioner(1)
	d, _ := p.Provision(context.Background(), DeviceSpec{})
	if err := p.Release(context.Background(), d); err != nil {
		t.Fatalf("first release: %v", err)
	}
	err := p.Release(context.Background(), d)
	if err == nil {
		t.Fatal("second release returned nil, want error")
	}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("err = %v, want wrapped ErrIllegalTransition", err)
	}
	if got := p.Status(d); got != StateReleased {
		t.Errorf("state after double release = %s, want still Released", got)
	}
}

// TestClaim_RequiresReady asserts InUse is only reachable from Ready.
//
// Mutation that kills it: add StateReleased->StateInUse to legalTransitions ->
// claiming a released device succeeds, failing the want-error check.
func TestClaim_RequiresReady(t *testing.T) {
	p := NewSoftwareProvisioner(2)
	d, _ := p.Provision(context.Background(), DeviceSpec{})
	if err := p.Claim(d); err != nil {
		t.Fatalf("claim ready device: %v", err)
	}
	if got := p.Status(d); got != StateInUse {
		t.Fatalf("state after claim = %s, want InUse", got)
	}
	// Release the in-use device then attempt to re-claim the terminal device.
	if err := p.Release(context.Background(), d); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := p.Claim(d); err == nil {
		t.Fatal("claim of released device returned nil, want error")
	}
}

// TestProvision_Deterministic asserts same seed => identical IDs and identical
// event timestamps (DST reproducibility).
//
// Mutation that kills it: seed the rng with time.Now().UnixNano() instead of the
// passed seed -> the two runs diverge and the equality checks fail.
func TestProvision_Deterministic(t *testing.T) {
	run := func() ([]string, [][]Event) {
		p := NewSoftwareProvisioner(12345)
		var ids []string
		var evs [][]Event
		for i := 0; i < 5; i++ {
			d, err := p.Provision(context.Background(), DeviceSpec{Tier: "T2"})
			if err != nil {
				t.Fatalf("provision %d: %v", i, err)
			}
			ids = append(ids, d.ID)
			evs = append(evs, d.Events())
		}
		return ids, evs
	}
	ids1, evs1 := run()
	ids2, evs2 := run()
	for i := range ids1 {
		if ids1[i] != ids2[i] {
			t.Errorf("device %d id mismatch: %q vs %q", i, ids1[i], ids2[i])
		}
		for j := range evs1[i] {
			if !evs1[i][j].At.Equal(evs2[i][j].At) {
				t.Errorf("device %d event %d timestamp mismatch: %v vs %v",
					i, j, evs1[i][j].At, evs2[i][j].At)
			}
			if evs1[i][j].To != evs2[i][j].To {
				t.Errorf("device %d event %d state mismatch: %s vs %s",
					i, j, evs1[i][j].To, evs2[i][j].To)
			}
		}
	}
}

// TestProvision_CancelledContext asserts a cancelled context yields no device.
//
// Mutation that kills it: delete the ctx.Err() guard at the top of Provision ->
// a device is returned despite cancellation, failing the want-error check.
func TestProvision_CancelledContext(t *testing.T) {
	p := NewSoftwareProvisioner(3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, err := p.Provision(ctx, DeviceSpec{})
	if err == nil {
		t.Fatal("provision with cancelled ctx returned nil error")
	}
	if d != nil {
		t.Errorf("provision with cancelled ctx returned device %v, want nil", d)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want wrapped context.Canceled", err)
	}
}

// TestState_String asserts the FSM labels are stable (used in events/logs).
//
// Mutation that kills it: swap two case labels in State.String() -> the mapping
// check fails.
func TestState_String(t *testing.T) {
	cases := map[State]string{
		StateRequested:    "Requested",
		StateProvisioning: "Provisioning",
		StateReady:        "Ready",
		StateInUse:        "InUse",
		StateReleasing:    "Releasing",
		StateReleased:     "Released",
		StateFailed:       "Failed",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}

// TestEvents_ConcurrentReadIsSafe exercises the RWMutex on Device under -race.
//
// Mutation that kills it: drop the RLock in Device.Events()/State() -> the race
// detector flags a data race against the concurrent transition writer.
func TestEvents_ConcurrentReadIsSafe(t *testing.T) {
	p := NewSoftwareProvisioner(9)
	d, _ := p.Provision(context.Background(), DeviceSpec{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = d.State()
				_ = d.Events()
			}
		}()
	}
	// Concurrent writer through the FSM.
	_ = p.Claim(d)
	_ = p.Release(context.Background(), d)
	wg.Wait()
	if got := d.State(); got != StateReleased {
		t.Errorf("final state = %s, want Released", got)
	}
}
