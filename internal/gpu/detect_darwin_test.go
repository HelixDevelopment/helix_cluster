//go:build darwin

package gpu

import "testing"

// sampleSystemProfilerJSON is a real-shaped `system_profiler -json
// SPDisplaysDataType` payload captured from an Apple M3 Pro host (display
// children trimmed). The parser must extract the GPU-level "_name".
const sampleSystemProfilerJSON = `{
  "SPDisplaysDataType" : [
    {
      "_name" : "Apple M3 Pro",
      "spdisplays_mtlgpufamilysupport" : "spdisplays_metal3",
      "spdisplays_vendor" : "sppci_vendor_Apple",
      "sppci_bus" : "spdisplays_builtin",
      "sppci_cores" : "14",
      "sppci_device_type" : "spdisplays_gpu",
      "sppci_model" : "Apple M3 Pro"
    }
  ]
}`

// TestParseSystemProfilerDisplays proves the darwin detector parses real
// system_profiler JSON into a GPU whose Model is the actual "_name" value, and
// that an Apple Silicon GPU is credited the injected unified-memory pool.
// Sink-side: we assert the parsed Model/Memory, not merely a non-nil result.
//
// §1.1 mutation: change the struct tag on spDisplay.Name from `json:"_name"`
// to a genuinely different key, e.g. `json:"_xname"` (note: encoding/json key
// matching is case-INSENSITIVE, so "_NAME" would still match -- the key must
// differ in more than case) -> Name parses as "", the entry is skipped as an
// unnamed display, len(gpus) becomes 0, and the assertions below fail. Restore
// the tag to pass again.
func TestParseSystemProfilerDisplays(t *testing.T) {
	const fakeUnifiedMB = 18432
	memFn := func() (int, error) { return fakeUnifiedMB, nil }

	gpus, err := parseSystemProfilerDisplays([]byte(sampleSystemProfilerJSON), memFn)
	if err != nil {
		t.Fatalf("parseSystemProfilerDisplays failed: %v", err)
	}
	if len(gpus) != 1 {
		t.Fatalf("expected exactly 1 GPU, got %d", len(gpus))
	}

	g := gpus[0]
	if g.Model != "Apple M3 Pro" {
		t.Fatalf("Model parse mismatch: got %q, want %q", g.Model, "Apple M3 Pro")
	}
	if g.ID != "gpu-0" {
		t.Fatalf("expected ID gpu-0, got %s", g.ID)
	}
	if g.UUID == "" {
		t.Fatal("expected non-empty UUID derived from name+index")
	}
	if g.Status != Available {
		t.Fatalf("expected Available, got %s", g.Status)
	}
	// Apple Silicon GPU shares unified memory: it must reflect the injected pool,
	// not a fabricated constant and not 0.
	if g.MemoryMB != fakeUnifiedMB {
		t.Fatalf("expected unified MemoryMB %d, got %d", fakeUnifiedMB, g.MemoryMB)
	}
}

// TestParseSystemProfilerDisplaysNoFabricatedMemory proves that when the
// unified-memory source is unavailable the parser reports 0 rather than
// inventing a number.
// Mutation: make parseSystemProfilerDisplays default unifiedMB to a constant on
// memErr -> MemoryMB is non-zero here, failing the assertion.
func TestParseSystemProfilerDisplaysNoFabricatedMemory(t *testing.T) {
	memFn := func() (int, error) { return 0, errMemUnavailable }
	gpus, err := parseSystemProfilerDisplays([]byte(sampleSystemProfilerJSON), memFn)
	if err != nil {
		t.Fatalf("parseSystemProfilerDisplays failed: %v", err)
	}
	if len(gpus) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(gpus))
	}
	if gpus[0].MemoryMB != 0 {
		t.Fatalf("expected 0 MemoryMB when pool size unknown, got %d", gpus[0].MemoryMB)
	}
	// The model must still parse even without memory.
	if gpus[0].Model != "Apple M3 Pro" {
		t.Fatalf("Model parse mismatch: got %q", gpus[0].Model)
	}
}

// TestParseSystemProfilerInvalidJSON proves malformed input is a hard error,
// not a silent empty inventory.
func TestParseSystemProfilerInvalidJSON(t *testing.T) {
	if _, err := parseSystemProfilerDisplays([]byte("{not json"), nil); err == nil {
		t.Fatal("expected error parsing invalid JSON")
	}
}

// errMemUnavailable is a sentinel for the memFn-failure test path.
var errMemUnavailable = errString("hw.memsize unavailable")

type errString string

func (e errString) Error() string { return string(e) }
