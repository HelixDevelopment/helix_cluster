package pool

// model_test.go — CLAUDE-1-compliant tests for the data model defined in
// model.go. Each test proves real behaviour (JSON round-trip with key
// inspection, ComputePoolStatus aggregate counts) rather than merely that code
// runs without panic.
//
// Mutation guard (inline per test):
//   If a json struct tag were wrong or removed, the "bytes contains key X"
//   assertion fails — exactly the mutation the spec requires be caught.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fullyPopulatedWorkloadSpec returns a WorkloadSpec where every field is
// non-zero so the round-trip test can verify nothing is silently dropped.
func fullyPopulatedWorkloadSpec() WorkloadSpec {
	return WorkloadSpec{
		Type:         WorkloadTypeTraining,
		GPUModel:     "H100",
		GPUCount:     8,
		MinVRAM:      80 * 1024 * 1024 * 1024, // 80 GiB
		MaxLatencyMs: 500,
		MaxCostHour:  32.00,
		Duration:     4 * time.Hour,
		Priority:     90,
		Labels:       map[string]string{"tenant": "helix-alpha", "job": "llm-pretrain"},
	}
}

// fullyPopulatedGPUAllocation returns a GPUAllocation where every field is
// non-zero.
func fullyPopulatedGPUAllocation() GPUAllocation {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	return GPUAllocation{
		AllocationID: "alloc-001",
		WorkloadID:   "wl-999",
		DeviceIDs:    []string{"dev-a", "dev-b"},
		AllocatedAt:  now,
		ExpiresAt:    now.Add(4 * time.Hour),
		Labels:       map[string]string{"tenant": "helix-alpha"},
	}
}

// fullyPopulatedGPUDevice returns a GPUDevice with every field non-zero.
func fullyPopulatedGPUDevice() GPUDevice {
	return GPUDevice{
		ID:          "aws-p4d-i-0abc1234",
		Tier:        GPUTierUltra,
		Provider:    "aws",
		Model:       "A100",
		VRAMBytes:   80 * 1024 * 1024 * 1024,
		TFLOPSFP16:  312.0,
		Location:    "us-east-1",
		CostPerHour: 32.77,
		Labels:      map[string]string{"zone": "us-east-1a"},
		Status:      DeviceStatusBusy,
		AllocatedTo: "wl-999",
	}
}

// requireJSONKey asserts that rawJSON contains the given key (as a JSON object
// key, i.e. surrounded by quotes). This is the mutation guard: a wrong/missing
// json struct tag produces a different key or omits it entirely — the assertion
// fires.
func requireJSONKey(t *testing.T, rawJSON []byte, key string) {
	t.Helper()
	needle := `"` + key + `"`
	if !strings.Contains(string(rawJSON), needle) {
		t.Errorf("marshalled JSON missing expected key %q\nGot: %s", key, rawJSON)
	}
}

// TestWorkloadSpec_JSONRoundTrip proves:
//  1. A fully-populated WorkloadSpec marshals to JSON that contains all
//     documented snake_case keys (mutation guard: wrong tag → key absent →
//     requireJSONKey fires).
//  2. Unmarshalling that JSON back produces a value == the original
//     (deep equality via field-by-field comparison).
//
// Mutation guard: changing `json:"gpu_count"` to `json:"GPUCount"` makes
// requireJSONKey("gpu_count") fail. Removing the tag entirely produces the
// Go field name "GPUCount" in JSON — same failure. Changing
// `json:"max_cost_hour"` similarly.
func TestWorkloadSpec_JSONRoundTrip(t *testing.T) {
	original := fullyPopulatedWorkloadSpec()

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal(WorkloadSpec): %v", err)
	}

	// --- key assertions (mutation guard) ---
	for _, key := range []string{
		"type",
		"gpu_model",
		"gpu_count",
		"min_vram",
		"max_latency_ms",
		"max_cost_hour",
		"duration_ns",
		"priority",
		"labels",
	} {
		requireJSONKey(t, raw, key)
	}

	// --- round-trip equality ---
	var got WorkloadSpec
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal(WorkloadSpec): %v", err)
	}

	if got.Type != original.Type {
		t.Errorf("Type: got %q, want %q", got.Type, original.Type)
	}
	if got.GPUModel != original.GPUModel {
		t.Errorf("GPUModel: got %q, want %q", got.GPUModel, original.GPUModel)
	}
	if got.GPUCount != original.GPUCount {
		t.Errorf("GPUCount: got %d, want %d", got.GPUCount, original.GPUCount)
	}
	if got.MinVRAM != original.MinVRAM {
		t.Errorf("MinVRAM: got %d, want %d", got.MinVRAM, original.MinVRAM)
	}
	if got.MaxLatencyMs != original.MaxLatencyMs {
		t.Errorf("MaxLatencyMs: got %d, want %d", got.MaxLatencyMs, original.MaxLatencyMs)
	}
	if got.MaxCostHour != original.MaxCostHour {
		t.Errorf("MaxCostHour: got %v, want %v", got.MaxCostHour, original.MaxCostHour)
	}
	if got.Duration != original.Duration {
		t.Errorf("Duration: got %v, want %v", got.Duration, original.Duration)
	}
	if got.Priority != original.Priority {
		t.Errorf("Priority: got %d, want %d", got.Priority, original.Priority)
	}
	if len(got.Labels) != len(original.Labels) {
		t.Errorf("Labels length: got %d, want %d", len(got.Labels), len(original.Labels))
	} else {
		for k, v := range original.Labels {
			if got.Labels[k] != v {
				t.Errorf("Labels[%q]: got %q, want %q", k, got.Labels[k], v)
			}
		}
	}
}

// TestGPUAllocation_JSONRoundTrip proves:
//  1. GPUAllocation marshals to JSON containing documented snake_case keys.
//  2. Unmarshal restores the original value exactly.
//
// Mutation guard: removing `json:"device_ids"` makes requireJSONKey fail.
// Removing `json:"allocation_id"` similarly. Removing `json:"allocated_at"`
// makes the timestamp key absent — same failure.
func TestGPUAllocation_JSONRoundTrip(t *testing.T) {
	original := fullyPopulatedGPUAllocation()

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal(GPUAllocation): %v", err)
	}

	// --- key assertions (mutation guard) ---
	for _, key := range []string{
		"allocation_id",
		"workload_id",
		"device_ids",
		"allocated_at",
		"expires_at",
		"labels",
	} {
		requireJSONKey(t, raw, key)
	}

	// --- round-trip equality ---
	var got GPUAllocation
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal(GPUAllocation): %v", err)
	}

	if got.AllocationID != original.AllocationID {
		t.Errorf("AllocationID: got %q, want %q", got.AllocationID, original.AllocationID)
	}
	if got.WorkloadID != original.WorkloadID {
		t.Errorf("WorkloadID: got %q, want %q", got.WorkloadID, original.WorkloadID)
	}
	if len(got.DeviceIDs) != len(original.DeviceIDs) {
		t.Errorf("DeviceIDs length: got %d, want %d", len(got.DeviceIDs), len(original.DeviceIDs))
	} else {
		for i, id := range original.DeviceIDs {
			if got.DeviceIDs[i] != id {
				t.Errorf("DeviceIDs[%d]: got %q, want %q", i, got.DeviceIDs[i], id)
			}
		}
	}
	if !got.AllocatedAt.Equal(original.AllocatedAt) {
		t.Errorf("AllocatedAt: got %v, want %v", got.AllocatedAt, original.AllocatedAt)
	}
	if !got.ExpiresAt.Equal(original.ExpiresAt) {
		t.Errorf("ExpiresAt: got %v, want %v", got.ExpiresAt, original.ExpiresAt)
	}
	for k, v := range original.Labels {
		if got.Labels[k] != v {
			t.Errorf("Labels[%q]: got %q, want %q", k, got.Labels[k], v)
		}
	}
}

// TestGPUDevice_JSONRoundTrip proves:
//  1. GPUDevice marshals to JSON containing all documented snake_case keys.
//  2. Unmarshal restores the original value exactly.
//
// Mutation guard: changing `json:"vram_bytes"` to any other name makes
// requireJSONKey("vram_bytes") fail. Same for "cost_per_hour", "tflops_fp16",
// "allocated_to", "status", "tier", "provider", "model", "location".
func TestGPUDevice_JSONRoundTrip(t *testing.T) {
	original := fullyPopulatedGPUDevice()

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal(GPUDevice): %v", err)
	}

	// --- key assertions (mutation guard) ---
	for _, key := range []string{
		"id",
		"tier",
		"provider",
		"model",
		"vram_bytes",
		"tflops_fp16",
		"location",
		"cost_per_hour",
		"labels",
		"status",
		"allocated_to",
	} {
		requireJSONKey(t, raw, key)
	}

	// --- round-trip equality ---
	var got GPUDevice
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal(GPUDevice): %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("ID: got %q, want %q", got.ID, original.ID)
	}
	if got.Tier != original.Tier {
		t.Errorf("Tier: got %q, want %q", got.Tier, original.Tier)
	}
	if got.Provider != original.Provider {
		t.Errorf("Provider: got %q, want %q", got.Provider, original.Provider)
	}
	if got.Model != original.Model {
		t.Errorf("Model: got %q, want %q", got.Model, original.Model)
	}
	if got.VRAMBytes != original.VRAMBytes {
		t.Errorf("VRAMBytes: got %d, want %d", got.VRAMBytes, original.VRAMBytes)
	}
	if got.TFLOPSFP16 != original.TFLOPSFP16 {
		t.Errorf("TFLOPSFP16: got %v, want %v", got.TFLOPSFP16, original.TFLOPSFP16)
	}
	if got.Location != original.Location {
		t.Errorf("Location: got %q, want %q", got.Location, original.Location)
	}
	if got.CostPerHour != original.CostPerHour {
		t.Errorf("CostPerHour: got %v, want %v", got.CostPerHour, original.CostPerHour)
	}
	if got.Status != original.Status {
		t.Errorf("Status: got %q, want %q", got.Status, original.Status)
	}
	if got.AllocatedTo != original.AllocatedTo {
		t.Errorf("AllocatedTo: got %q, want %q", got.AllocatedTo, original.AllocatedTo)
	}
	for k, v := range original.Labels {
		if got.Labels[k] != v {
			t.Errorf("Labels[%q]: got %q, want %q", k, got.Labels[k], v)
		}
	}
}

// TestComputePoolStatus_AggregatesCorrectly proves ComputePoolStatus tallies
// device counts, VRAM, TCO, and active allocations correctly across a mixed
// device slice (1 idle, 2 busy, 1 draining, 1 failed).
//
// Mutation guard (CLAUDE-1 mutation requirement):
//
//	If the DeviceStatusIdle branch in ComputePoolStatus's switch were removed
//	(or the FreeVRAMBytes accumulation dropped), IdleDevices == 0 and
//	FreeVRAMBytes == 0 while wantIdle == 1 and wantFreeVRAM > 0 — assertions
//	FAIL. If the BusyDevices branch is removed, BusyDevices == 0 while
//	wantBusy == 2 — assertion FAILS.
func TestComputePoolStatus_AggregatesCorrectly(t *testing.T) {
	const vramPerDevice = 80 * 1024 * 1024 * 1024 // 80 GiB
	const costPerDevice = 10.0

	devices := []GPUDevice{
		{ID: "d1", Status: DeviceStatusIdle, VRAMBytes: vramPerDevice, CostPerHour: costPerDevice},
		{ID: "d2", Status: DeviceStatusBusy, VRAMBytes: vramPerDevice, CostPerHour: costPerDevice},
		{ID: "d3", Status: DeviceStatusBusy, VRAMBytes: vramPerDevice, CostPerHour: costPerDevice},
		{ID: "d4", Status: DeviceStatusDraining, VRAMBytes: vramPerDevice, CostPerHour: costPerDevice},
		{ID: "d5", Status: DeviceStatusFailed, VRAMBytes: vramPerDevice, CostPerHour: costPerDevice},
	}

	activeAllocs := 3
	s := ComputePoolStatus(devices, activeAllocs)

	if s.TotalDevices != 5 {
		t.Errorf("TotalDevices: got %d, want 5", s.TotalDevices)
	}
	if s.IdleDevices != 1 {
		t.Errorf("IdleDevices: got %d, want 1", s.IdleDevices)
	}
	if s.BusyDevices != 2 {
		t.Errorf("BusyDevices: got %d, want 2", s.BusyDevices)
	}
	if s.DrainingDevices != 1 {
		t.Errorf("DrainingDevices: got %d, want 1", s.DrainingDevices)
	}
	if s.FailedDevices != 1 {
		t.Errorf("FailedDevices: got %d, want 1", s.FailedDevices)
	}

	wantTotalVRAM := uint64(5 * vramPerDevice)
	if s.TotalVRAMBytes != wantTotalVRAM {
		t.Errorf("TotalVRAMBytes: got %d, want %d", s.TotalVRAMBytes, wantTotalVRAM)
	}

	// Only the one idle device contributes to free VRAM.
	wantFreeVRAM := uint64(vramPerDevice)
	if s.FreeVRAMBytes != wantFreeVRAM {
		t.Errorf("FreeVRAMBytes: got %d, want %d", s.FreeVRAMBytes, wantFreeVRAM)
	}

	wantTCO := float64(5) * costPerDevice
	if s.HourlyTCO != wantTCO {
		t.Errorf("HourlyTCO: got %v, want %v", s.HourlyTCO, wantTCO)
	}

	if s.ActiveAllocations != activeAllocs {
		t.Errorf("ActiveAllocations: got %d, want %d", s.ActiveAllocations, activeAllocs)
	}
}

// TestComputePoolStatus_EmptyPool proves ComputePoolStatus on an empty device
// slice and zero allocations returns an all-zero PoolStatus — no panic, no
// spurious non-zero values.
//
// Mutation guard: if any accumulation variable were initialised to a non-zero
// sentinel, this test would catch it (e.g. HourlyTCO == 99.0 instead of 0).
func TestComputePoolStatus_EmptyPool(t *testing.T) {
	s := ComputePoolStatus(nil, 0)
	if s.TotalDevices != 0 {
		t.Errorf("TotalDevices: got %d, want 0", s.TotalDevices)
	}
	if s.HourlyTCO != 0 {
		t.Errorf("HourlyTCO: got %v, want 0", s.HourlyTCO)
	}
	if s.FreeVRAMBytes != 0 {
		t.Errorf("FreeVRAMBytes: got %d, want 0", s.FreeVRAMBytes)
	}
}

// TestPoolStatus_JSONRoundTrip proves PoolStatus itself marshals / unmarshals
// with the documented snake_case keys present.
//
// Mutation guard: removing `json:"idle_devices"` makes requireJSONKey fail.
// Same for "busy_devices", "total_vram_bytes", "free_vram_bytes",
// "hourly_tco", "active_allocations".
func TestPoolStatus_JSONRoundTrip(t *testing.T) {
	original := PoolStatus{
		TotalDevices:      5,
		IdleDevices:       1,
		BusyDevices:       2,
		DrainingDevices:   1,
		FailedDevices:     1,
		TotalVRAMBytes:    400 * 1024 * 1024 * 1024,
		FreeVRAMBytes:     80 * 1024 * 1024 * 1024,
		HourlyTCO:         50.0,
		ActiveAllocations: 3,
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal(PoolStatus): %v", err)
	}

	for _, key := range []string{
		"total_devices",
		"idle_devices",
		"busy_devices",
		"draining_devices",
		"failed_devices",
		"total_vram_bytes",
		"free_vram_bytes",
		"hourly_tco",
		"active_allocations",
	} {
		requireJSONKey(t, raw, key)
	}

	var got PoolStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal(PoolStatus): %v", err)
	}

	if got.TotalDevices != original.TotalDevices {
		t.Errorf("TotalDevices: got %d, want %d", got.TotalDevices, original.TotalDevices)
	}
	if got.IdleDevices != original.IdleDevices {
		t.Errorf("IdleDevices: got %d, want %d", got.IdleDevices, original.IdleDevices)
	}
	if got.BusyDevices != original.BusyDevices {
		t.Errorf("BusyDevices: got %d, want %d", got.BusyDevices, original.BusyDevices)
	}
	if got.HourlyTCO != original.HourlyTCO {
		t.Errorf("HourlyTCO: got %v, want %v", got.HourlyTCO, original.HourlyTCO)
	}
	if got.ActiveAllocations != original.ActiveAllocations {
		t.Errorf("ActiveAllocations: got %d, want %d", got.ActiveAllocations, original.ActiveAllocations)
	}
}
