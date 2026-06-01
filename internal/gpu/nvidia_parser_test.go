package gpu

import (
	"strings"
	"testing"
)

// realFormatA100 is a realistic /proc/driver/nvidia/gpus/.../information body
// for an NVIDIA A100-SXM4-40GB with "MiB" unit and "GPU UUID:" key.
const realFormatA100 = `Model: NVIDIA A100-SXM4-40GB
GPU UUID: GPU-abc12345-def6-7890-abcd-ef1234567890
Video Memory: 40960 MiB
Bus Location: 0000:01:00.0
Device Minor: 0
`

// realFormatMBUnit is the same card but with "MB" suffix.
const realFormatMBUnit = `Model: NVIDIA A100-SXM4-40GB
GPU UUID: GPU-abc12345-def6-7890-abcd-ef1234567890
Video Memory: 40960 MB
`

// noModelContent has no "Model:" line at all.
const noModelContent = `GPU UUID: GPU-abc12345-def6-7890-abcd-ef1234567890
Video Memory: 40960 MiB
`

// malformedMemoryContent has a "Model:" but a non-numeric memory value.
const malformedMemoryContent = `Model: NVIDIA T4
GPU UUID: GPU-deadbeef-dead-beef-dead-beefdeadbeef
Video Memory: unknown
`

// bareUUIDContent uses the "UUID:" prefix instead of "GPU UUID:".
const bareUUIDContent = `Model: NVIDIA V100
UUID: GPU-v100uuid-0000-0000-0000-000000000000
Video Memory: 16384 MiB
`

// ---------------------------------------------------------------------------
// ParseNvidiaInformation tests
// ---------------------------------------------------------------------------

func TestParseNvidiaInformation_A100_MiB(t *testing.T) {
	info, err := ParseNvidiaInformation(realFormatA100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Model != "NVIDIA A100-SXM4-40GB" {
		t.Errorf("Model: got %q, want %q", info.Model, "NVIDIA A100-SXM4-40GB")
	}
	if info.UUID != "GPU-abc12345-def6-7890-abcd-ef1234567890" {
		t.Errorf("UUID: got %q, want %q", info.UUID, "GPU-abc12345-def6-7890-abcd-ef1234567890")
	}
	if info.MemoryMB != 40960 {
		t.Errorf("MemoryMB: got %d, want 40960", info.MemoryMB)
	}
}

func TestParseNvidiaInformation_MBUnit(t *testing.T) {
	info, err := ParseNvidiaInformation(realFormatMBUnit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The "MB" suffix must also be stripped and parsed identically.
	if info.MemoryMB != 40960 {
		t.Errorf("MemoryMB (MB unit): got %d, want 40960", info.MemoryMB)
	}
	if info.Model != "NVIDIA A100-SXM4-40GB" {
		t.Errorf("Model: got %q, want %q", info.Model, "NVIDIA A100-SXM4-40GB")
	}
}

func TestParseNvidiaInformation_BareUUIDKey(t *testing.T) {
	info, err := ParseNvidiaInformation(bareUUIDContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "GPU-v100uuid-0000-0000-0000-000000000000" {
		t.Errorf("UUID (bare key): got %q, want %q", info.UUID, "GPU-v100uuid-0000-0000-0000-000000000000")
	}
	if info.Model != "NVIDIA V100" {
		t.Errorf("Model: got %q, want %q", info.Model, "NVIDIA V100")
	}
	if info.MemoryMB != 16384 {
		t.Errorf("MemoryMB: got %d, want 16384", info.MemoryMB)
	}
}

func TestParseNvidiaInformation_NoModel_ReturnsError(t *testing.T) {
	_, err := ParseNvidiaInformation(noModelContent)
	if err == nil {
		t.Fatal("expected error for content with no Model line, got nil")
	}
	if !strings.Contains(err.Error(), "Model") {
		t.Errorf("error message should mention 'Model', got: %v", err)
	}
}

func TestParseNvidiaInformation_EmptyContent_ReturnsError(t *testing.T) {
	_, err := ParseNvidiaInformation("")
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
}

func TestParseNvidiaInformation_MalformedMemory_ZeroNotError(t *testing.T) {
	info, err := ParseNvidiaInformation(malformedMemoryContent)
	if err != nil {
		t.Fatalf("unexpected error for malformed memory: %v", err)
	}
	// MemoryMB must be 0 (not parsed), but Model and UUID must still be captured.
	if info.MemoryMB != 0 {
		t.Errorf("MemoryMB (malformed): got %d, want 0", info.MemoryMB)
	}
	if info.Model != "NVIDIA T4" {
		t.Errorf("Model: got %q, want %q", info.Model, "NVIDIA T4")
	}
}

// TestParseNvidiaInformation_WrongFieldLabel verifies that a parser which
// reads the wrong label (e.g. maps "UUID:" when looking for "Model:") will
// fail the assertion. This is the mutation-resistance test: any code change
// that accidentally swaps field labels produces a test failure.
func TestParseNvidiaInformation_FieldsAreDistinct(t *testing.T) {
	info, err := ParseNvidiaInformation(realFormatA100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Model must not contain "GPU-" (UUID prefix) — would fail if labels swapped.
	if strings.HasPrefix(info.Model, "GPU-") {
		t.Errorf("Model looks like a UUID: %q — label mapping appears wrong", info.Model)
	}
	// UUID must not be a plain model name.
	if info.UUID == info.Model {
		t.Errorf("UUID and Model are identical (%q) — parser likely swapped fields", info.UUID)
	}
	// MemoryMB must be a positive integer, not a string artefact.
	if info.MemoryMB <= 0 {
		t.Errorf("MemoryMB should be > 0, got %d", info.MemoryMB)
	}
}

// ---------------------------------------------------------------------------
// ParseNvidiaProcTree tests
// ---------------------------------------------------------------------------

// buildTreeFixtures returns a map of 3 "information" file paths → content for
// PCI addresses 0000:01:00.0, 0000:02:00.0, 0000:03:00.0.
func buildTreeFixtures() map[string]string {
	return map[string]string{
		"/proc/driver/nvidia/gpus/0000:01:00.0/information": `Model: NVIDIA A100-SXM4-40GB
GPU UUID: GPU-uuid-01-000000000000
Video Memory: 40960 MiB
`,
		"/proc/driver/nvidia/gpus/0000:02:00.0/information": `Model: NVIDIA A100-SXM4-40GB
GPU UUID: GPU-uuid-02-000000000000
Video Memory: 40960 MiB
`,
		"/proc/driver/nvidia/gpus/0000:03:00.0/information": `Model: NVIDIA A100-SXM4-40GB
GPU UUID: GPU-uuid-03-000000000000
Video Memory: 40960 MiB
`,
	}
}

func TestParsNvidiaProcTree_ThreeGPUs(t *testing.T) {
	files := buildTreeFixtures()
	infos, err := ParseNvidiaProcTree(files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("len(infos): got %d, want 3", len(infos))
	}

	// Keys are sorted lexicographically, so the expected order is 01 < 02 < 03.
	wantPCIs := []string{"0000:01:00.0", "0000:02:00.0", "0000:03:00.0"}
	wantUUIDs := []string{
		"GPU-uuid-01-000000000000",
		"GPU-uuid-02-000000000000",
		"GPU-uuid-03-000000000000",
	}

	for i, info := range infos {
		// Index must be contiguous starting at 0.
		if info.Index != i {
			t.Errorf("infos[%d].Index: got %d, want %d", i, info.Index, i)
		}
		if info.PCIID != wantPCIs[i] {
			t.Errorf("infos[%d].PCIID: got %q, want %q", i, info.PCIID, wantPCIs[i])
		}
		if info.UUID != wantUUIDs[i] {
			t.Errorf("infos[%d].UUID: got %q, want %q", i, info.UUID, wantUUIDs[i])
		}
		if info.MemoryMB != 40960 {
			t.Errorf("infos[%d].MemoryMB: got %d, want 40960", i, info.MemoryMB)
		}
	}
}

func TestParsNvidiaProcTree_EmptyMap(t *testing.T) {
	infos, err := ParseNvidiaProcTree(map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error for empty map: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(infos))
	}
}

func TestParsNvidiaProcTree_SkipsModellessEntry(t *testing.T) {
	files := map[string]string{
		"/proc/driver/nvidia/gpus/0000:01:00.0/information": `Model: NVIDIA A100-SXM4-40GB
GPU UUID: GPU-uuid-01-000000000000
Video Memory: 40960 MiB
`,
		// This entry has no "Model:" line — it must be silently skipped.
		"/proc/driver/nvidia/gpus/0000:02:00.0/information": `GPU UUID: GPU-uuid-02-000000000000
Video Memory: 40960 MiB
`,
		"/proc/driver/nvidia/gpus/0000:03:00.0/information": `Model: NVIDIA A100-SXM4-40GB
GPU UUID: GPU-uuid-03-000000000000
Video Memory: 40960 MiB
`,
	}

	infos, err := ParseNvidiaProcTree(files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos): got %d, want 2 (model-less entry must be skipped)", len(infos))
	}

	// Remaining entries get contiguous indices 0 and 1.
	for i, info := range infos {
		if info.Index != i {
			t.Errorf("infos[%d].Index: got %d, want %d", i, info.Index, i)
		}
	}

	// The surviving entries must be the ones with real models (not the
	// model-less 0000:02 entry).
	for _, info := range infos {
		if info.PCIID == "0000:02:00.0" {
			t.Errorf("model-less entry 0000:02:00.0 should have been skipped but appeared in results")
		}
		if info.Model == "" {
			t.Errorf("a surviving entry has an empty Model: %+v", info)
		}
	}
}

func TestParsNvidiaProcTree_IndexIsContiguous(t *testing.T) {
	// Four entries where two are model-less; surviving two must get indices 0,1.
	files := map[string]string{
		"/proc/driver/nvidia/gpus/0000:01:00.0/information": `Model: NVIDIA T4
GPU UUID: GPU-t4-01
Video Memory: 16384 MiB
`,
		"/proc/driver/nvidia/gpus/0000:02:00.0/information": `GPU UUID: GPU-nomodel-02
Video Memory: 16384 MiB
`,
		"/proc/driver/nvidia/gpus/0000:03:00.0/information": `GPU UUID: GPU-nomodel-03
Video Memory: 16384 MiB
`,
		"/proc/driver/nvidia/gpus/0000:04:00.0/information": `Model: NVIDIA T4
GPU UUID: GPU-t4-04
Video Memory: 16384 MiB
`,
	}

	infos, err := ParseNvidiaProcTree(files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos): got %d, want 2", len(infos))
	}
	if infos[0].Index != 0 {
		t.Errorf("infos[0].Index: got %d, want 0", infos[0].Index)
	}
	if infos[1].Index != 1 {
		t.Errorf("infos[1].Index: got %d, want 1", infos[1].Index)
	}
}
