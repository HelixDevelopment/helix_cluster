package helixv1

import (
	"testing"

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
