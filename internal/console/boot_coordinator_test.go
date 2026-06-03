package console

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeMarkers materialises a fixture marker-set on disk and returns the paths
// to feed into a BootCoordinator. Any field left empty is not created, so the
// coordinator observes its absence (the real-world "not yet present" case).
func writeMarkers(t *testing.T, version, deviceTree, cmdline string) (versionPath, dtPath, cmdlinePath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	versionPath = filepath.Join(dir, "version")
	dtPath = filepath.Join(dir, "devicetree")
	cmdlinePath = filepath.Join(dir, "cmdline")
	logPath = filepath.Join(dir, "boot.log")

	if version != "" {
		if err := os.WriteFile(versionPath, []byte(version), 0o644); err != nil {
			t.Fatalf("write version: %v", err)
		}
	}
	if deviceTree != "" {
		if err := os.WriteFile(dtPath, []byte(deviceTree), 0o644); err != nil {
			t.Fatalf("write devicetree: %v", err)
		}
	}
	if cmdline != "" {
		if err := os.WriteFile(cmdlinePath, []byte(cmdline), 0o644); err != nil {
			t.Fatalf("write cmdline: %v", err)
		}
	}
	return versionPath, dtPath, cmdlinePath, logPath
}

// TestBootCoordinator_PhaseTransitions feeds fixture path-sets representing each
// boot phase and asserts the resulting BootPhase. This is the core closure
// assertion (HXC-1147): each marker fixture yields exactly one asserted phase.
//
// Mutation that kills it: force Probe() to always return PhaseClusterReady ->
// the Unknown/Booting/UserspaceReady cases all fail.
func TestBootCoordinator_PhaseTransitions(t *testing.T) {
	cases := []struct {
		name       string
		version    string
		deviceTree string
		cmdline    string
		want       BootPhase
	}{
		{
			name: "no markers -> Unknown",
			want: PhaseUnknown,
		},
		{
			name:    "kernel up, no userspace/cluster -> Booting",
			version: "Linux version 6.6.0 (helix@build) #1 SMP",
			cmdline: "ro quiet",
			want:    PhaseBooting,
		},
		{
			name:    "userspace ready marker -> UserspaceReady",
			version: "Linux version 6.6.0 (helix@build) #1 SMP",
			cmdline: "ro quiet helix.userspace=ready",
			want:    PhaseUserspaceReady,
		},
		{
			name:       "cluster ready marker -> ClusterReady",
			version:    "Linux version 6.6.0 (helix@build) #1 SMP",
			deviceTree: "helix,cluster-node",
			cmdline:    "ro quiet helix.userspace=ready helix.cluster=ready",
			want:       PhaseClusterReady,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vp, dt, cl, lg := writeMarkers(t, tc.version, tc.deviceTree, tc.cmdline)
			bc := NewBootCoordinator(BootMarkers{
				VersionPath:    vp,
				DeviceTreePath: dt,
				CmdlinePath:    cl,
				BootLogPath:    lg,
			})
			got, err := bc.Probe()
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if got != tc.want {
				t.Fatalf("phase = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBootCoordinator_PathsAreInjectable proves the markers are read from the
// injected paths, NOT hardcoded /proc. We point the coordinator at a temp dir
// and flip a file's contents; the observed phase must change accordingly.
//
// Mutation that kills it: hardcode os.ReadFile("/proc/version") instead of
// reading m.VersionPath -> on the darwin/CI host /proc/version is absent, so the
// "Booting" expectation (which depends on our injected version file) fails.
func TestBootCoordinator_PathsAreInjectable(t *testing.T) {
	vp, dt, cl, lg := writeMarkers(t, "", "", "")
	bc := NewBootCoordinator(BootMarkers{
		VersionPath:    vp,
		DeviceTreePath: dt,
		CmdlinePath:    cl,
		BootLogPath:    lg,
	})

	// Initially no markers exist -> Unknown.
	if got, _ := bc.Probe(); got != PhaseUnknown {
		t.Fatalf("with no markers, phase = %q, want Unknown", got)
	}

	// Materialise the kernel version marker at the injected path.
	if err := os.WriteFile(vp, []byte("Linux version 6.6.0"), 0o644); err != nil {
		t.Fatalf("write version: %v", err)
	}
	if got, _ := bc.Probe(); got != PhaseBooting {
		t.Fatalf("after version marker appears at injected path, phase = %q, want Booting", got)
	}
}

// TestBootCoordinator_WaitReadyBlocksUntilClusterReady asserts WaitReady blocks
// while the node is not ClusterReady and returns nil only once the cluster-ready
// marker appears. We start WaitReady against a not-ready marker-set, confirm it
// is still blocked after a grace period, then flip the marker and confirm it
// unblocks.
//
// Mutation that kills it: make WaitReady return nil immediately regardless of
// phase -> the "still blocked before marker" assertion fails (it would have
// already returned).
func TestBootCoordinator_WaitReadyBlocksUntilClusterReady(t *testing.T) {
	dir := t.TempDir()
	vp := filepath.Join(dir, "version")
	cl := filepath.Join(dir, "cmdline")
	lg := filepath.Join(dir, "boot.log")

	// Kernel up but NOT cluster-ready yet.
	if err := os.WriteFile(vp, []byte("Linux version 6.6.0"), 0o644); err != nil {
		t.Fatalf("write version: %v", err)
	}
	if err := os.WriteFile(cl, []byte("ro quiet helix.userspace=ready"), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}

	bc := NewBootCoordinator(BootMarkers{
		VersionPath: vp,
		CmdlinePath: cl,
		BootLogPath: lg,
	})
	bc.PollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- bc.WaitReady(ctx) }()

	// It must NOT have returned yet — node is not ClusterReady.
	select {
	case err := <-done:
		t.Fatalf("WaitReady returned early (err=%v) before ClusterReady marker", err)
	case <-time.After(80 * time.Millisecond):
		// still blocked, as required
	}

	// Now flip the cluster-ready marker.
	if err := os.WriteFile(cl, []byte("ro quiet helix.userspace=ready helix.cluster=ready"), 0o644); err != nil {
		t.Fatalf("flip cmdline: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitReady after ClusterReady: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("WaitReady did not unblock after ClusterReady marker appeared")
	}
}

// TestBootCoordinator_WaitReadyHonorsContext asserts WaitReady returns the
// context error when cancelled before readiness (so a stuck node cannot block
// registration forever).
//
// Mutation that kills it: ignore ctx in WaitReady (loop forever) -> this test
// hangs and the -timeout trips / it never returns ctx.Err().
func TestBootCoordinator_WaitReadyHonorsContext(t *testing.T) {
	vp, dt, cl, lg := writeMarkers(t, "Linux version 6.6.0", "", "ro quiet")
	bc := NewBootCoordinator(BootMarkers{
		VersionPath:    vp,
		DeviceTreePath: dt,
		CmdlinePath:    cl,
		BootLogPath:    lg,
	})
	bc.PollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	err := bc.WaitReady(ctx)
	if err == nil {
		t.Fatalf("WaitReady returned nil, want context error (node never became ClusterReady)")
	}
	if ctx.Err() == nil {
		t.Fatalf("context was not done; WaitReady returned %v for another reason", err)
	}
}

// TestBootCoordinator_RunUUIDWrittenToBootLog proves a live invocation: each
// Probe writes a per-run UUID line to the boot log, and the UUID is unique per
// coordinator run. This is the sink-side evidence required by the closure.
//
// Mutation that kills it: stop writing to the boot log (make logBoot a no-op)
// -> the log file is empty/absent and the UUID-present assertion fails. Or hard
// code a constant UUID -> the two coordinators produce identical UUIDs and the
// uniqueness assertion fails.
func TestBootCoordinator_RunUUIDWrittenToBootLog(t *testing.T) {
	vp, dt, cl, lg := writeMarkers(t, "Linux version 6.6.0", "", "ro quiet")

	bc1 := NewBootCoordinator(BootMarkers{VersionPath: vp, DeviceTreePath: dt, CmdlinePath: cl, BootLogPath: lg})
	if _, err := bc1.Probe(); err != nil {
		t.Fatalf("probe1: %v", err)
	}
	u1 := bc1.RunUUID()
	if u1 == "" {
		t.Fatalf("RunUUID is empty")
	}

	data, err := os.ReadFile(lg)
	if err != nil {
		t.Fatalf("read boot log: %v", err)
	}
	if !strings.Contains(string(data), u1) {
		t.Fatalf("boot log does not contain run UUID %q; got:\n%s", u1, data)
	}
	if !strings.Contains(string(data), string(PhaseBooting)) {
		t.Fatalf("boot log does not record probed phase; got:\n%s", data)
	}

	// A second coordinator must mint a distinct UUID (proves it's per-run, live).
	lg2 := filepath.Join(filepath.Dir(lg), "boot2.log")
	bc2 := NewBootCoordinator(BootMarkers{VersionPath: vp, DeviceTreePath: dt, CmdlinePath: cl, BootLogPath: lg2})
	if _, err := bc2.Probe(); err != nil {
		t.Fatalf("probe2: %v", err)
	}
	if bc2.RunUUID() == u1 {
		t.Fatalf("two coordinator runs share UUID %q, want distinct per-run UUIDs", u1)
	}
}

// TestBootCoordinator_DeviceTreeGatesCluster proves DeviceTreePath is
// load-bearing, not inert config. With the same cmdline asserting cluster
// readiness, a device-tree marker that corroborates membership yields
// ClusterReady, while one that negates it (helix,no-cluster) holds the node at
// UserspaceReady — so a mis-provisioned board cannot register.
//
// Mutation that kills it: drop the deviceTreeNegatesCluster() check in classify()
// -> the negating case advances to ClusterReady and this test fails.
func TestBootCoordinator_DeviceTreeGatesCluster(t *testing.T) {
	const clusterCmdline = "ro quiet helix.userspace=ready helix.cluster=ready"

	cases := []struct {
		name       string
		deviceTree string
		want       BootPhase
	}{
		{name: "corroborating device-tree -> ClusterReady", deviceTree: "helix,cluster-node", want: PhaseClusterReady},
		{name: "absent device-tree does not block -> ClusterReady", deviceTree: "", want: PhaseClusterReady},
		{name: "negating device-tree holds at UserspaceReady", deviceTree: "helix,no-cluster", want: PhaseUserspaceReady},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vp, dt, cl, lg := writeMarkers(t, "Linux version 6.6.0", tc.deviceTree, clusterCmdline)
			bc := NewBootCoordinator(BootMarkers{VersionPath: vp, DeviceTreePath: dt, CmdlinePath: cl, BootLogPath: lg})
			got, err := bc.Probe()
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if got != tc.want {
				t.Fatalf("with device-tree %q, phase = %q, want %q", tc.deviceTree, got, tc.want)
			}
		})
	}
}

// TestBootCoordinator_DeviceTreeRecordedToBootLog proves the device-tree value
// is recorded to the boot log (sink-side evidence), not silently dropped.
//
// Mutation that kills it: stop including devicetree=%q in the boot-log line ->
// the value is absent from the log and this assertion fails.
func TestBootCoordinator_DeviceTreeRecordedToBootLog(t *testing.T) {
	const model = "helix,cluster-node-rev-b"
	vp, dt, cl, lg := writeMarkers(t, "Linux version 6.6.0", model, "ro quiet")
	bc := NewBootCoordinator(BootMarkers{VersionPath: vp, DeviceTreePath: dt, CmdlinePath: cl, BootLogPath: lg})
	if _, err := bc.Probe(); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	data, err := os.ReadFile(lg)
	if err != nil {
		t.Fatalf("read boot log: %v", err)
	}
	if !strings.Contains(string(data), model) {
		t.Fatalf("boot log does not record device-tree value %q; got:\n%s", model, data)
	}
}

// TestBootCoordinator_UUIDFormat asserts the run UUID is a canonical RFC-4122
// v4 string (8-4-4-4-12 hex), so the boot-log entry is a real identifier.
func TestBootCoordinator_UUIDFormat(t *testing.T) {
	bc := NewBootCoordinator(BootMarkers{BootLogPath: filepath.Join(t.TempDir(), "b.log")})
	u := bc.RunUUID()
	parts := strings.Split(u, "-")
	if len(parts) != 5 {
		t.Fatalf("UUID %q does not have 5 dash-separated groups", u)
	}
	lens := []int{8, 4, 4, 4, 12}
	for i, p := range parts {
		if len(p) != lens[i] {
			t.Fatalf("UUID group %d %q len=%d, want %d", i, p, len(p), lens[i])
		}
		for _, c := range p {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Fatalf("UUID %q has non-hex char %q", u, c)
			}
		}
	}
	if parts[2][0] != '4' {
		t.Fatalf("UUID %q is not version 4 (group3 starts %c)", u, parts[2][0])
	}
}

// TestBootCoordinator_ConcurrentProbe ensures Probe/RunUUID/CurrentPhase are
// safe under concurrent use. Run with -race.
func TestBootCoordinator_ConcurrentProbe(t *testing.T) {
	vp, dt, cl, lg := writeMarkers(t, "Linux version 6.6.0", "", "ro quiet helix.userspace=ready helix.cluster=ready")
	bc := NewBootCoordinator(BootMarkers{VersionPath: vp, DeviceTreePath: dt, CmdlinePath: cl, BootLogPath: lg})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if _, err := bc.Probe(); err != nil {
					t.Errorf("probe: %v", err)
					return
				}
				_ = bc.RunUUID()
				_ = bc.CurrentPhase()
			}
		}()
	}
	wg.Wait()
	if got := bc.CurrentPhase(); got != PhaseClusterReady {
		t.Fatalf("final phase = %q, want ClusterReady", got)
	}
}
