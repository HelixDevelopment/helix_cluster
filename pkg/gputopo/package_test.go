package gputopo

import (
	"errors"
	"testing"
)

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// mustTopology builds a Topology or fails the test immediately.
func mustTopology(t *testing.T, gpus []string, links []Link) Topology {
	t.Helper()
	topo, err := NewTopology(gpus, links)
	if err != nil {
		t.Fatalf("NewTopology: %v", err)
	}
	return topo
}

// ────────────────────────────────────────────────────────────────────────────
// TestPrefersNVLinkPair
//
// Guard: the `if len(nvlinkCandidates) > 0` branch in SelectPair.
// Inverting it to prefer PCIe candidates causes this test to fail because the
// returned Kind would be PCIe instead of NVLink.
// ────────────────────────────────────────────────────────────────────────────

func TestPrefersNVLinkPair(t *testing.T) {
	flushRecords()

	// Four GPUs: gpu0–gpu3.
	// gpu0<->gpu1: NVLink  600 GB/s
	// gpu2<->gpu3: PCIe     32 GB/s
	// All four GPUs are free — the NVLink pair MUST be selected.
	topo := mustTopology(t,
		[]string{"gpu0", "gpu1", "gpu2", "gpu3"},
		[]Link{
			{A: "gpu0", B: "gpu1", Kind: NVLink, BandwidthGBps: 600},
			{A: "gpu2", B: "gpu3", Kind: PCIe, BandwidthGBps: 32},
		},
	)

	runID := "run-nvlink-prefer-001"
	rec, err := SelectPair(runID, topo, []string{"gpu0", "gpu1", "gpu2", "gpu3"})
	if err != nil {
		t.Fatalf("SelectPair returned unexpected error: %v", err)
	}

	// Assert RunID propagated into record (CLAUDE-1: sink-side).
	if rec.RunID != runID {
		t.Errorf("RunID: got %q, want %q", rec.RunID, runID)
	}

	// Assert the NVLink pair was chosen (mutation-killable guard).
	if rec.Kind != NVLink {
		t.Errorf("Kind: got %v, want NVLink — NVLink pair must be preferred over PCIe pair", rec.Kind)
	}

	// Assert the bandwidth is the NVLink bandwidth, not the PCIe bandwidth.
	const wantBW = 600.0
	if rec.BandwidthGBps != wantBW {
		t.Errorf("BandwidthGBps: got %v, want %v — should be NVLink bandwidth", rec.BandwidthGBps, wantBW)
	}

	// Assert the selected GPUs are the NVLink pair.
	if (rec.GPUA != "gpu0" || rec.GPUB != "gpu1") && (rec.GPUA != "gpu1" || rec.GPUB != "gpu0") {
		t.Errorf("GPU pair: got (%s, %s), want (gpu0, gpu1)", rec.GPUA, rec.GPUB)
	}

	// Assert the record is retrievable from the store by RunID (CLAUDE-1: audit sink).
	stored, ok := RecordFor(runID)
	if !ok {
		t.Fatalf("RecordFor(%q): record not found in store", runID)
	}
	if stored.Kind != NVLink {
		t.Errorf("stored Kind: got %v, want NVLink", stored.Kind)
	}
	if stored.BandwidthGBps != wantBW {
		t.Errorf("stored BandwidthGBps: got %v, want %v", stored.BandwidthGBps, wantBW)
	}
	if stored.RunID != runID {
		t.Errorf("stored RunID: got %q, want %q", stored.RunID, runID)
	}
}

// TestPrefersHigherBandwidthNVLink verifies that when multiple NVLink pairs
// are available, the one with the highest bandwidth is selected.
// This pins the `c.link.BandwidthGBps > best.link.BandwidthGBps` guard.
func TestPrefersHigherBandwidthNVLink(t *testing.T) {
	flushRecords()

	// Three NVLink pairs with different bandwidths; the 900 GB/s pair must win.
	topo := mustTopology(t,
		[]string{"g0", "g1", "g2", "g3", "g4", "g5"},
		[]Link{
			{A: "g0", B: "g1", Kind: NVLink, BandwidthGBps: 400},
			{A: "g2", B: "g3", Kind: NVLink, BandwidthGBps: 900},
			{A: "g4", B: "g5", Kind: NVLink, BandwidthGBps: 600},
		},
	)

	runID := "run-bw-pick-002"
	rec, err := SelectPair(runID, topo, []string{"g0", "g1", "g2", "g3", "g4", "g5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.BandwidthGBps != 900 {
		t.Errorf("BandwidthGBps: got %v, want 900 — highest-bandwidth NVLink must be chosen", rec.BandwidthGBps)
	}
	if rec.RunID != runID {
		t.Errorf("RunID: got %q, want %q", rec.RunID, runID)
	}
	if rec.GPUA != "g2" || rec.GPUB != "g3" {
		t.Errorf("GPU pair: got (%s,%s), want (g2,g3)", rec.GPUA, rec.GPUB)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestFallsBackToPCIe
// ────────────────────────────────────────────────────────────────────────────

// TestFallsBackToPCIe verifies that when no NVLink-connected pair exists among
// the free GPUs, a PCIe pair is chosen and the RunID is recorded.
func TestFallsBackToPCIe(t *testing.T) {
	flushRecords()

	// gpu0<->gpu1: NVLink (but gpu0 is NOT free — simulating it being busy).
	// gpu2<->gpu3: PCIe 32 GB/s — only PCIe pair among the free GPUs.
	// gpu4<->gpu5: PCIe 16 GB/s — lower bandwidth PCIe pair.
	topo := mustTopology(t,
		[]string{"gpu0", "gpu1", "gpu2", "gpu3", "gpu4", "gpu5"},
		[]Link{
			{A: "gpu0", B: "gpu1", Kind: NVLink, BandwidthGBps: 600},
			{A: "gpu2", B: "gpu3", Kind: PCIe, BandwidthGBps: 32},
			{A: "gpu4", B: "gpu5", Kind: PCIe, BandwidthGBps: 16},
		},
	)

	// Only PCIe-connected GPUs are free (NVLink GPUs are busy).
	runID := "run-pcie-fallback-003"
	rec, err := SelectPair(runID, topo, []string{"gpu2", "gpu3", "gpu4", "gpu5"})
	if err != nil {
		t.Fatalf("SelectPair returned unexpected error: %v", err)
	}

	// Assert RunID present (CLAUDE-1: sink-side evidence).
	if rec.RunID != runID {
		t.Errorf("RunID: got %q, want %q", rec.RunID, runID)
	}

	// Assert PCIe was chosen (fallback confirmed).
	if rec.Kind != PCIe {
		t.Errorf("Kind: got %v, want PCIe — must fall back to PCIe when no NVLink pair is free", rec.Kind)
	}

	// Among the two PCIe pairs, the higher-bandwidth one (32 GB/s) should win.
	const wantBW = 32.0
	if rec.BandwidthGBps != wantBW {
		t.Errorf("BandwidthGBps: got %v, want %v — highest-bandwidth PCIe pair must be chosen", rec.BandwidthGBps, wantBW)
	}

	// Assert the record is stored under runID.
	stored, ok := RecordFor(runID)
	if !ok {
		t.Fatalf("RecordFor(%q): record not found", runID)
	}
	if stored.Kind != PCIe {
		t.Errorf("stored Kind: got %v, want PCIe", stored.Kind)
	}
	if stored.RunID != runID {
		t.Errorf("stored RunID: got %q, want %q", stored.RunID, runID)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestTooFewGPUs
// ────────────────────────────────────────────────────────────────────────────

func TestTooFewGPUs(t *testing.T) {
	flushRecords()

	topo := mustTopology(t, []string{"g0", "g1"}, []Link{
		{A: "g0", B: "g1", Kind: NVLink, BandwidthGBps: 600},
	})

	for _, freeGPUs := range [][]string{
		nil,
		{},
		{"g0"},
	} {
		_, err := SelectPair("run-few", topo, freeGPUs)
		if !errors.Is(err, ErrTooFewGPUs) {
			t.Errorf("freeGPUs=%v: got err %v, want ErrTooFewGPUs", freeGPUs, err)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestNoPairAvailable
// ────────────────────────────────────────────────────────────────────────────

func TestNoPairAvailable(t *testing.T) {
	flushRecords()

	// gpu0<->gpu1 NVLink exists, but only gpu0 and gpu2 are free and they have
	// no direct link — so no eligible pair.
	topo := mustTopology(t,
		[]string{"gpu0", "gpu1", "gpu2"},
		[]Link{
			{A: "gpu0", B: "gpu1", Kind: NVLink, BandwidthGBps: 600},
		},
	)

	_, err := SelectPair("run-nopair", topo, []string{"gpu0", "gpu2"})
	if !errors.Is(err, ErrNoPairAvailable) {
		t.Errorf("got err %v, want ErrNoPairAvailable", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestOnlyOneNVLinkGPUFree
// ────────────────────────────────────────────────────────────────────────────

// TestOnlyOneNVLinkGPUFree ensures that when only one end of an NVLink pair is
// free (the other is busy), and a PCIe pair is available among the free GPUs,
// the PCIe pair is correctly chosen (no NVLink pair is eligible).
func TestOnlyOneNVLinkGPUFree(t *testing.T) {
	flushRecords()

	topo := mustTopology(t,
		[]string{"g0", "g1", "g2", "g3"},
		[]Link{
			{A: "g0", B: "g1", Kind: NVLink, BandwidthGBps: 600},
			{A: "g2", B: "g3", Kind: PCIe, BandwidthGBps: 32},
		},
	)

	// g0 is free but g1 (its NVLink partner) is busy; only g0, g2, g3 are free.
	runID := "run-half-nvlink-004"
	rec, err := SelectPair(runID, topo, []string{"g0", "g2", "g3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Kind != PCIe {
		t.Errorf("Kind: got %v, want PCIe — only one NVLink endpoint free, must fall back to PCIe", rec.Kind)
	}
	if rec.RunID != runID {
		t.Errorf("RunID: got %q, want %q", rec.RunID, runID)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestNewTopologyValidation
// ────────────────────────────────────────────────────────────────────────────

func TestNewTopologyValidation(t *testing.T) {
	cases := []struct {
		name    string
		gpus    []string
		links   []Link
		wantErr bool
	}{
		{
			name:    "unknown GPU in link",
			gpus:    []string{"g0"},
			links:   []Link{{A: "g0", B: "g99", Kind: NVLink, BandwidthGBps: 100}},
			wantErr: true,
		},
		{
			name:    "non-positive bandwidth",
			gpus:    []string{"g0", "g1"},
			links:   []Link{{A: "g0", B: "g1", Kind: PCIe, BandwidthGBps: 0}},
			wantErr: true,
		},
		{
			name:  "duplicate link rejected",
			gpus:  []string{"g0", "g1"},
			links: []Link{
				{A: "g0", B: "g1", Kind: NVLink, BandwidthGBps: 100},
				{A: "g1", B: "g0", Kind: NVLink, BandwidthGBps: 100}, // same pair, reversed
			},
			wantErr: true,
		},
		{
			name:    "self-loop rejected",
			gpus:    []string{"g0", "g1"},
			links:   []Link{{A: "g0", B: "g0", Kind: NVLink, BandwidthGBps: 100}},
			wantErr: true,
		},
		{
			name:    "unknown Kind rejected",
			gpus:    []string{"g0", "g1"},
			links:   []Link{{A: "g0", B: "g1", Kind: Kind(0), BandwidthGBps: 100}},
			wantErr: true,
		},
		{
			name:    "valid topology",
			gpus:    []string{"g0", "g1"},
			links:   []Link{{A: "g0", B: "g1", Kind: NVLink, BandwidthGBps: 600}},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewTopology(tc.gpus, tc.links)
			if (err != nil) != tc.wantErr {
				t.Errorf("NewTopology error=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestKindString
// ────────────────────────────────────────────────────────────────────────────

func TestKindString(t *testing.T) {
	if s := NVLink.String(); s != "NVLink" {
		t.Errorf("NVLink.String() = %q, want %q", s, "NVLink")
	}
	if s := PCIe.String(); s != "PCIe" {
		t.Errorf("PCIe.String() = %q, want %q", s, "PCIe")
	}
	unknown := Kind(99)
	if s := unknown.String(); s == "" {
		t.Error("Kind(99).String() returned empty string")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestTopologyGPUsAndLink
// ────────────────────────────────────────────────────────────────────────────

func TestTopologyGPUsAndLink(t *testing.T) {
	topo := mustTopology(t,
		[]string{"z", "a", "m"},
		[]Link{
			{A: "a", B: "m", Kind: PCIe, BandwidthGBps: 16},
			{A: "a", B: "z", Kind: NVLink, BandwidthGBps: 600},
		},
	)

	gpus := topo.GPUs()
	if len(gpus) != 3 {
		t.Fatalf("GPUs() len = %d, want 3", len(gpus))
	}
	// GPUs() must be sorted.
	if gpus[0] != "a" || gpus[1] != "m" || gpus[2] != "z" {
		t.Errorf("GPUs() = %v, want [a m z]", gpus)
	}

	// Link lookup in either direction.
	l, ok := topo.Link("z", "a")
	if !ok {
		t.Fatal("Link(z, a): expected link to exist")
	}
	if l.Kind != NVLink || l.BandwidthGBps != 600 {
		t.Errorf("Link(z,a): got Kind=%v BW=%v, want NVLink 600", l.Kind, l.BandwidthGBps)
	}

	_, ok = topo.Link("z", "m")
	if ok {
		t.Error("Link(z, m): expected no link to exist")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestRecordStoreIsolation
// ────────────────────────────────────────────────────────────────────────────

// TestRecordStoreIsolation confirms that each RunID maps to exactly the record
// produced by its own SelectPair call and that a different RunID has no record.
func TestRecordStoreIsolation(t *testing.T) {
	flushRecords()

	topo := mustTopology(t,
		[]string{"g0", "g1"},
		[]Link{{A: "g0", B: "g1", Kind: PCIe, BandwidthGBps: 32}},
	)

	runID := "run-isolation-005"
	_, err := SelectPair(runID, topo, []string{"g0", "g1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The run's record must exist.
	rec, ok := RecordFor(runID)
	if !ok {
		t.Fatalf("RecordFor(%q): not found", runID)
	}
	if rec.RunID != runID {
		t.Errorf("RunID mismatch: %q != %q", rec.RunID, runID)
	}

	// A different RunID must not exist.
	_, ok = RecordFor("run-other-999")
	if ok {
		t.Error("RecordFor(run-other-999): expected no record, but found one")
	}
}
