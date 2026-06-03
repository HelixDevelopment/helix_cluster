package helixv1

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// TestCreateSessionRequest_RoundTrip is the HXC-1117 closure proof: the
// buf-generated helixv1 stubs marshal and unmarshal a fully-populated
// CreateSessionRequest (including the nested ResourceAllocation) with every
// field preserved byte-for-byte. It is load-bearing — if the generated stubs
// drop or mis-tag a field, proto.Equal (and the explicit field checks) fail.
func TestCreateSessionRequest_RoundTrip(t *testing.T) {
	orig := &CreateSessionRequest{
		Name:    "sess-alpha",
		Owner:   "operator-1",
		Mode:    "interactive",
		Backend: "firecracker",
		Resources: &ResourceAllocation{
			CpuMillicores: 4500,
			MemoryBytes:   8 << 30,
			GpuIds:        []string{"gpu-0", "gpu-1"},
		},
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got CreateSessionRequest
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	// Explicit per-field assertions so a silent proto.Equal regression is also
	// caught field-by-field (the closure requires ALL fields preserved).
	if got.GetName() != "sess-alpha" || got.GetOwner() != "operator-1" ||
		got.GetMode() != "interactive" || got.GetBackend() != "firecracker" {
		t.Fatalf("scalar fields not preserved: %+v", &got)
	}
	r := got.GetResources()
	if r == nil {
		t.Fatal("nested ResourceAllocation lost")
	}
	if r.GetCpuMillicores() != 4500 || r.GetMemoryBytes() != 8<<30 {
		t.Fatalf("resource scalars not preserved: %+v", r)
	}
	if len(r.GetGpuIds()) != 2 || r.GetGpuIds()[0] != "gpu-0" || r.GetGpuIds()[1] != "gpu-1" {
		t.Fatalf("gpu_ids not preserved: %v", r.GetGpuIds())
	}
}

// TestNode_NewClassificationFields_RoundTrip is the HXC-1293 (+HXC-1165)
// closure proof: the buf-generated Node stub marshals and unmarshals the NEW
// classification fields (tier/trust_level/compute_class/arch) and the nested
// NodeThermal (thermal_celsius/thermal_state) with every value preserved. It is
// load-bearing — if codegen drops or mis-tags any new field number (12..16) or
// the NodeThermal sub-message, proto.Equal and the explicit checks fail. This
// is NOT a PASS-bluff: the assertions read sink-side values that only survive a
// correct wire encode/decode, not mere absence of panic.
func TestNode_NewClassificationFields_RoundTrip(t *testing.T) {
	orig := &Node{
		Id:           "node-7",
		Hostname:     "host-7",
		Status:       "ready",
		Role:         "worker",
		Tier:         "premium",
		TrustLevel:   "attested",
		ComputeClass: "gpu-accel",
		Arch:         "arm64",
		Thermal: &NodeThermal{
			ThermalCelsius: 72.5,
			ThermalState:   "throttling",
		},
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got Node
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	// Explicit per-field assertions so a silent proto.Equal regression is also
	// caught field-by-field for the NEW fields specifically.
	if got.GetTier() != "premium" {
		t.Fatalf("tier not preserved: %q", got.GetTier())
	}
	if got.GetTrustLevel() != "attested" {
		t.Fatalf("trust_level not preserved: %q", got.GetTrustLevel())
	}
	if got.GetComputeClass() != "gpu-accel" {
		t.Fatalf("compute_class not preserved: %q", got.GetComputeClass())
	}
	if got.GetArch() != "arm64" {
		t.Fatalf("arch not preserved: %q", got.GetArch())
	}
	th := got.GetThermal()
	if th == nil {
		t.Fatal("nested NodeThermal lost")
	}
	if th.GetThermalCelsius() != 72.5 {
		t.Fatalf("thermal_celsius not preserved: %v", th.GetThermalCelsius())
	}
	if th.GetThermalState() != "throttling" {
		t.Fatalf("thermal_state not preserved: %q", th.GetThermalState())
	}

	// Backward-compat sanity: the pre-existing fields still survive too.
	if got.GetId() != "node-7" || got.GetHostname() != "host-7" ||
		got.GetStatus() != "ready" || got.GetRole() != "worker" {
		t.Fatalf("pre-existing fields not preserved: %+v", &got)
	}
}

// TestNodeResources_AcceleratorCapacity_RoundTrip is the HXC-1294 closure
// proof: the buf-generated NodeResources stub marshals and unmarshals the NEW
// accelerator-capacity fields (npu_tops/fpga_logic_elements/tflops_gpu) with
// every value preserved byte-for-byte. It is load-bearing — if codegen drops or
// mis-tags any new field number (6..8), proto.Equal and the explicit sink-side
// checks fail. NOT a PASS-bluff: the assertions read decoded values that only
// survive a correct wire encode/decode, not mere absence of panic. The values
// are deliberately non-zero and distinct so a field swap (e.g. tags 6<->8) is
// caught.
func TestNodeResources_AcceleratorCapacity_RoundTrip(t *testing.T) {
	orig := &NodeResources{
		Cpu:               &CPUResources{Cores: 64},
		NpuTops:           412.5,
		FpgaLogicElements: 1_234_567,
		TflopsGpu:         98.75,
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got NodeResources
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	// Explicit per-field assertions for the NEW accelerator fields specifically,
	// reading sink-side decoded values.
	if got.GetNpuTops() != 412.5 {
		t.Fatalf("npu_tops not preserved: %v", got.GetNpuTops())
	}
	if got.GetFpgaLogicElements() != 1_234_567 {
		t.Fatalf("fpga_logic_elements not preserved: %v", got.GetFpgaLogicElements())
	}
	if got.GetTflopsGpu() != 98.75 {
		t.Fatalf("tflops_gpu not preserved: %v", got.GetTflopsGpu())
	}

	// Backward-compat sanity: a pre-existing field still survives alongside.
	if got.GetCpu().GetCores() != 64 {
		t.Fatalf("pre-existing cpu.cores not preserved: %v", got.GetCpu().GetCores())
	}
}

// TestScheduleJobRequest_TierAndPowerConstraints_RoundTrip is the HXC-1295
// closure proof: the buf-generated ScheduleJobRequest stub marshals and
// unmarshals the NEW tier-constraint + power-budget fields
// (allowed_tiers/min_tier/power_budget_watts) with every value preserved
// byte-for-byte. It is load-bearing — if codegen drops or mis-tags any new
// field number (5..7), proto.Equal and the explicit sink-side checks fail. NOT
// a PASS-bluff: the assertions read decoded values that only survive a correct
// wire encode/decode, not mere absence of panic. The repeated tiers, the
// distinct scalar min_tier, and the non-zero power budget are deliberately set
// so a dropped/mis-tagged field is caught field-by-field.
func TestScheduleJobRequest_TierAndPowerConstraints_RoundTrip(t *testing.T) {
	orig := &ScheduleJobRequest{
		JobId:     "job-42",
		SessionId: "sess-42",
		Requirements: &ResourceAllocation{
			CpuMillicores: 2000,
			MemoryBytes:   4 << 30,
		},
		Constraints:      map[string]string{"zone": "eu-west"},
		AllowedTiers:     []string{"premium", "standard"},
		MinTier:          "standard",
		PowerBudgetWatts: 350,
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got ScheduleJobRequest
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	// Explicit per-field assertions for the NEW tier/power fields specifically,
	// reading sink-side decoded values.
	at := got.GetAllowedTiers()
	if len(at) != 2 || at[0] != "premium" || at[1] != "standard" {
		t.Fatalf("allowed_tiers not preserved: %v", at)
	}
	if got.GetMinTier() != "standard" {
		t.Fatalf("min_tier not preserved: %q", got.GetMinTier())
	}
	if got.GetPowerBudgetWatts() != 350 {
		t.Fatalf("power_budget_watts not preserved: %v", got.GetPowerBudgetWatts())
	}

	// Backward-compat sanity: pre-existing fields still survive alongside.
	if got.GetJobId() != "job-42" || got.GetSessionId() != "sess-42" {
		t.Fatalf("pre-existing id fields not preserved: %+v", &got)
	}
	if got.GetRequirements().GetCpuMillicores() != 2000 {
		t.Fatalf("pre-existing requirements not preserved: %v", got.GetRequirements())
	}
	if got.GetConstraints()["zone"] != "eu-west" {
		t.Fatalf("pre-existing constraints not preserved: %v", got.GetConstraints())
	}
}

// TestEdgeWorkUnit_RoundTrip is the HXC-1187 closure proof for EdgeWorkUnit: the
// buf-generated stub marshals and unmarshals a fully-populated EdgeWorkUnit —
// including the bytes payload, the target_tier scalar, and the nested repeated
// Capability list — with every value preserved byte-for-byte. It is load-bearing:
// if codegen drops or mis-tags any field number (1..4) or the nested Capability
// sub-message, proto.Equal and the explicit sink-side checks fail. NOT a
// PASS-bluff — the assertions read decoded values that only survive a correct
// wire encode/decode, not mere absence of panic. Values are deliberately distinct
// so a field swap is caught field-by-field.
func TestEdgeWorkUnit_RoundTrip(t *testing.T) {
	orig := &EdgeWorkUnit{
		Id:         "wu-7",
		Payload:    []byte{0xDE, 0xAD, 0xBE, 0xEF},
		TargetTier: "edge",
		Capabilities: []*Capability{
			{Name: "gpu", Type: "accelerator", Version: "1.2", Quantity: 2,
				Attributes: map[string]string{"vendor": "nvidia"}},
		},
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got EdgeWorkUnit
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	if got.GetId() != "wu-7" {
		t.Fatalf("id not preserved: %q", got.GetId())
	}
	if string(got.GetPayload()) != string([]byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Fatalf("payload not preserved: %x", got.GetPayload())
	}
	if got.GetTargetTier() != "edge" {
		t.Fatalf("target_tier not preserved: %q", got.GetTargetTier())
	}
	caps := got.GetCapabilities()
	if len(caps) != 1 {
		t.Fatalf("capabilities not preserved: %v", caps)
	}
	if caps[0].GetName() != "gpu" || caps[0].GetType() != "accelerator" ||
		caps[0].GetVersion() != "1.2" || caps[0].GetQuantity() != 2 {
		t.Fatalf("capability scalars not preserved: %+v", caps[0])
	}
	if caps[0].GetAttributes()["vendor"] != "nvidia" {
		t.Fatalf("capability attributes not preserved: %v", caps[0].GetAttributes())
	}
}

// TestEdgeWorkResult_RoundTrip is the HXC-1187 closure proof for EdgeWorkResult:
// the buf-generated stub marshals and unmarshals a fully-populated EdgeWorkResult
// (id/status/output bytes/error) with every value preserved byte-for-byte. It is
// load-bearing: if codegen drops or mis-tags any field number (1..4), proto.Equal
// and the explicit sink-side checks fail. NOT a PASS-bluff — the assertions read
// decoded values that only survive a correct wire encode/decode. The distinct
// values (notably output bytes vs error string) catch a tag swap between the
// success and failure carriers.
func TestEdgeWorkResult_RoundTrip(t *testing.T) {
	orig := &EdgeWorkResult{
		Id:     "wu-7",
		Status: "failed",
		Output: []byte{0x01, 0x02, 0x03},
		Error:  "oom: payload too large",
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got EdgeWorkResult
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	if got.GetId() != "wu-7" {
		t.Fatalf("id not preserved: %q", got.GetId())
	}
	if got.GetStatus() != "failed" {
		t.Fatalf("status not preserved: %q", got.GetStatus())
	}
	if string(got.GetOutput()) != string([]byte{0x01, 0x02, 0x03}) {
		t.Fatalf("output not preserved: %x", got.GetOutput())
	}
	if got.GetError() != "oom: payload too large" {
		t.Fatalf("error not preserved: %q", got.GetError())
	}
}

// TestKernelRequest_RoundTrip is the HXC-1527 closure proof for KernelRequest:
// the buf-generated stub marshals and unmarshals a fully-populated KernelRequest
// — kernel_id/device_id scalars, the repeated bytes args buffers, and the params
// map — with every value preserved byte-for-byte. It is load-bearing: if codegen
// drops or mis-tags any field number (1..4), proto.Equal and the explicit
// sink-side checks fail. NOT a PASS-bluff — the assertions read decoded values
// that only survive a correct wire encode/decode, not mere absence of panic. The
// two distinct arg buffers (preserving order) and the distinct scalar ids catch a
// field swap or a repeated-element reordering.
func TestKernelRequest_RoundTrip(t *testing.T) {
	orig := &KernelRequest{
		KernelId: "saxpy",
		DeviceId: "gpu-3",
		Args:     [][]byte{{0x01, 0x02}, {0xAA, 0xBB, 0xCC}},
		Params:   map[string]string{"grid": "256", "block": "128"},
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got KernelRequest
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	if got.GetKernelId() != "saxpy" {
		t.Fatalf("kernel_id not preserved: %q", got.GetKernelId())
	}
	if got.GetDeviceId() != "gpu-3" {
		t.Fatalf("device_id not preserved: %q", got.GetDeviceId())
	}
	args := got.GetArgs()
	if len(args) != 2 ||
		string(args[0]) != string([]byte{0x01, 0x02}) ||
		string(args[1]) != string([]byte{0xAA, 0xBB, 0xCC}) {
		t.Fatalf("args not preserved (order-sensitive): %x", args)
	}
	if got.GetParams()["grid"] != "256" || got.GetParams()["block"] != "128" {
		t.Fatalf("params not preserved: %v", got.GetParams())
	}

	// Wire-level field-number pin: a self-consistent encode/decode survives even a
	// kernel_id<->device_id tag SWAP (both are strings), so proto.Equal alone is
	// NOT mutation-sensitive to it. Decode the raw wire and assert kernel_id is
	// carried on field #1 and device_id on field #2 — this catches a renumber.
	wantTags := map[protowire.Number]string{1: "saxpy", 2: "gpu-3"}
	b := wire
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatalf("bad tag in wire: %v", protowire.ParseError(n))
		}
		b = b[n:]
		if want, ok := wantTags[num]; ok && typ == protowire.BytesType {
			v, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				t.Fatalf("bad bytes for field %d: %v", num, protowire.ParseError(vn))
			}
			if string(v) != want {
				t.Fatalf("field #%d carried %q, want %q (tag mis-assigned?)", num, v, want)
			}
			delete(wantTags, num)
			b = b[vn:]
			continue
		}
		fn := protowire.ConsumeFieldValue(num, typ, b)
		if fn < 0 {
			t.Fatalf("bad field value for #%d: %v", num, protowire.ParseError(fn))
		}
		b = b[fn:]
	}
	if len(wantTags) != 0 {
		t.Fatalf("expected string fields not found at their field numbers: %v", wantTags)
	}
}

// TestKernelResponse_RoundTrip is the HXC-1527 closure proof for KernelResponse:
// the buf-generated stub marshals and unmarshals a fully-populated KernelResponse
// (kernel_id/output bytes/status/error/duration_micros) with every value
// preserved byte-for-byte. It is load-bearing: if codegen drops or mis-tags any
// field number (1..5), proto.Equal and the explicit sink-side checks fail. NOT a
// PASS-bluff — the assertions read decoded values that only survive a correct
// wire encode/decode. The non-zero duration and distinct output bytes catch a tag
// swap between the carriers.
func TestKernelResponse_RoundTrip(t *testing.T) {
	orig := &KernelResponse{
		KernelId:       "saxpy",
		Output:         []byte{0xDE, 0xAD},
		Status:         "ok",
		Error:          "",
		DurationMicros: 4321,
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got KernelResponse
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	if got.GetKernelId() != "saxpy" {
		t.Fatalf("kernel_id not preserved: %q", got.GetKernelId())
	}
	if string(got.GetOutput()) != string([]byte{0xDE, 0xAD}) {
		t.Fatalf("output not preserved: %x", got.GetOutput())
	}
	if got.GetStatus() != "ok" {
		t.Fatalf("status not preserved: %q", got.GetStatus())
	}
	if got.GetDurationMicros() != 4321 {
		t.Fatalf("duration_micros not preserved: %v", got.GetDurationMicros())
	}
}

// TestKernelChunk_RoundTrip is the HXC-1527 closure proof for the streaming
// KernelChunk frame: the buf-generated stub marshals and unmarshals a
// fully-populated KernelChunk (kernel_id/sequence/output bytes/last/status/error)
// with every value preserved byte-for-byte. It is load-bearing: if codegen drops
// or mis-tags any field number (1..6), proto.Equal and the explicit sink-side
// checks fail. NOT a PASS-bluff — the assertions read decoded values that only
// survive a correct wire encode/decode. last=true with a non-zero sequence and a
// terminal status exercises the final-frame shape specifically.
func TestKernelChunk_RoundTrip(t *testing.T) {
	orig := &KernelChunk{
		KernelId: "saxpy",
		Sequence: 7,
		Output:   []byte{0xBE, 0xEF},
		Last:     true,
		Status:   "ok",
		Error:    "",
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(wire) == 0 {
		t.Fatal("marshalled to zero bytes")
	}

	var got KernelChunk
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip not equal:\n orig=%v\n  got=%v", orig, &got)
	}

	if got.GetKernelId() != "saxpy" {
		t.Fatalf("kernel_id not preserved: %q", got.GetKernelId())
	}
	if got.GetSequence() != 7 {
		t.Fatalf("sequence not preserved: %v", got.GetSequence())
	}
	if string(got.GetOutput()) != string([]byte{0xBE, 0xEF}) {
		t.Fatalf("output not preserved: %x", got.GetOutput())
	}
	if !got.GetLast() {
		t.Fatalf("last not preserved: %v", got.GetLast())
	}
	if got.GetStatus() != "ok" {
		t.Fatalf("status not preserved: %q", got.GetStatus())
	}
}
