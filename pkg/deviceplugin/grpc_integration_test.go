package deviceplugin_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	dp "github.com/HelixDevelopment/helix_cluster/pkg/deviceplugin"
)

const bufSize = 1 << 20

// startManager stands up a real grpc.Server (backed by bufconn) hosting the
// device-plugin Registrar service over a fresh Registry, and returns the
// registry plus a dialled plugin client. All traffic crosses the real gRPC
// stack — no in-memory shortcut — satisfying CLAUDE-1's no-mock-only rule.
func startManager(t *testing.T) (*dp.Registry, *dp.RegistrarClient) {
	t.Helper()

	reg := dp.NewRegistry()
	lis := bufconn.Listen(bufSize)
	gs := dp.NewServer()
	dp.RegisterRegistrarServer(gs, dp.NewManagerServer(reg))

	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.GracefulStop()
		_ = lis.Close()
	})

	dialOpts := append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, dp.DefaultDialOptions()...)

	cc, err := grpc.NewClient("passthrough:///bufnet", dialOpts...)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })

	return reg, dp.NewRegistrarClient(cc)
}

// TestRegister_FullFingerprintOverWire is closure criterion #1.
//
// A plugin client registers a device carrying a FULL fingerprint over real
// gRPC; the test then asserts the Manager's Registry discovered it with every
// field populated exactly as sent over the wire.
func TestRegister_FullFingerprintOverWire(t *testing.T) {
	reg, client := startManager(t)

	want := dp.Device{
		ID:                 "0000:65:00.0",
		Model:              "rtx3080",
		MemoryBytes:        10 << 30,
		DriverVersion:      "550.54.15",
		PCIeBandwidth:      "16GT/s",
		Capabilities:       []string{"fp16", "tensor-cores", "nvlink"},
		Health:             dp.HealthHealthy,
		UtilizationPercent: 37.5,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Register(ctx, &dp.RegisterRequest{
		Fingerprint: dp.Fingerprint{
			PluginName:   "nvidia-gpu",
			ResourceKind: "gpu",
			Devices:      []dp.Device{want},
		},
	})
	if err != nil {
		t.Fatalf("Register RPC: %v", err)
	}
	if !resp.Accepted {
		t.Fatalf("server rejected registration: %q", resp.Message)
	}
	if resp.DevicesKnown != 1 {
		t.Fatalf("server reports %d devices known, want 1", resp.DevicesKnown)
	}

	// Sink-side assertion: the Registry must hold the device exactly as sent.
	got, ok := reg.Device(want.ID)
	if !ok {
		t.Fatalf("device %q not discovered by Manager after Register", want.ID)
	}

	if got.ID != want.ID {
		t.Errorf("ID: got %q want %q", got.ID, want.ID)
	}
	if got.Model != want.Model {
		t.Errorf("Model: got %q want %q", got.Model, want.Model)
	}
	if got.MemoryBytes != want.MemoryBytes {
		t.Errorf("MemoryBytes: got %d want %d", got.MemoryBytes, want.MemoryBytes)
	}
	if got.DriverVersion != want.DriverVersion {
		t.Errorf("DriverVersion: got %q want %q", got.DriverVersion, want.DriverVersion)
	}
	if got.PCIeBandwidth != want.PCIeBandwidth {
		t.Errorf("PCIeBandwidth: got %q want %q", got.PCIeBandwidth, want.PCIeBandwidth)
	}
	if got.Health != want.Health {
		t.Errorf("Health: got %q want %q", got.Health, want.Health)
	}
	if got.UtilizationPercent != want.UtilizationPercent {
		t.Errorf("UtilizationPercent: got %v want %v", got.UtilizationPercent, want.UtilizationPercent)
	}
	if len(got.Capabilities) != len(want.Capabilities) {
		t.Fatalf("Capabilities len: got %v want %v", got.Capabilities, want.Capabilities)
	}
	for i := range want.Capabilities {
		if got.Capabilities[i] != want.Capabilities[i] {
			t.Errorf("Capabilities[%d]: got %q want %q", i, got.Capabilities[i], want.Capabilities[i])
		}
	}
}

// TestEndToEnd_RegisterThenAllocateThenOversubscribe drives the full feature as
// a scheduler would: a plugin registers 2 rtx3080 over gRPC, the scheduler
// parses "gpu:rtx3080:2", allocates them (success), then a request for 1 more is
// rejected as oversubscription. Allocatable inventory is captured before/after.
func TestEndToEnd_RegisterThenAllocateThenOversubscribe(t *testing.T) {
	reg, client := startManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mk := func(id string) dp.Device {
		return dp.Device{
			ID: id, Model: "rtx3080", MemoryBytes: 10 << 30,
			DriverVersion: "550.54.15", PCIeBandwidth: "16GT/s",
			Capabilities: []string{"fp16"}, Health: dp.HealthHealthy,
		}
	}
	if _, err := client.Register(ctx, &dp.RegisterRequest{Fingerprint: dp.Fingerprint{
		PluginName: "nvidia-gpu", ResourceKind: "gpu",
		Devices: []dp.Device{mk("gpu-0"), mk("gpu-1")},
	}}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Scheduler parses a GRES request and uses the device component.
	parsed, err := dp.ParseGRES("gpu:rtx3080:2")
	if err != nil {
		t.Fatalf("ParseGRES: %v", err)
	}
	if len(parsed) != 1 || !parsed[0].IsDeviceRequest() {
		t.Fatalf("unexpected parse result: %#v", parsed)
	}

	if before := reg.Allocatable("rtx3080"); before != 2 {
		t.Fatalf("allocatable before: want 2, got %d", before)
	}

	alloc, err := reg.Allocate(parsed[0])
	if err != nil {
		t.Fatalf("Allocate(2): %v", err)
	}
	if len(alloc.DeviceIDs) != 2 {
		t.Fatalf("want 2 devices allocated, got %v", alloc.DeviceIDs)
	}

	if after := reg.Allocatable("rtx3080"); after != 0 {
		t.Fatalf("allocatable after: want 0, got %d", after)
	}

	_, err = reg.Allocate(dp.Resource{Kind: "gpu", Model: "rtx3080", Count: 1})
	if !errors.Is(err, dp.ErrOversubscription) {
		t.Fatalf("want ErrOversubscription on 3rd device, got %v", err)
	}
}
