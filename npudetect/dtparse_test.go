package npudetect

import (
	"os"
	"path/filepath"
	"testing"
)

// writeDTProp writes a NUL-terminated device-tree string property file.
func writeDTProp(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(value), 0x00), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildFixtureRoots constructs fixture device-tree / sys / dev trees describing
// a Rockchip RK3588 (RKNN), an NVIDIA Jetson (nvdla), and a Qualcomm (hexagon).
func buildFixtureRoots(t *testing.T) linuxRoots {
	t.Helper()
	base := t.TempDir()
	dt := filepath.Join(base, "device-tree")
	sys := filepath.Join(base, "sys")
	dev := filepath.Join(base, "dev")

	// Rockchip RKNN: device-tree node rknpu@fdab0000 + sysfs devfreq + /dev node.
	writeDTProp(t, filepath.Join(dt, "rknpu@fdab0000", "compatible"), "rockchip,rk3588-rknpu")
	if err := os.MkdirAll(filepath.Join(sys, "class", "devfreq", "fdab0000.npu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dev, "rknpu"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	// NVIDIA Jetson DLA: nvdla node.
	writeDTProp(t, filepath.Join(dt, "nvdla0@15880000", "compatible"), "nvidia,tegra194-nvdla")

	// Qualcomm Hexagon (cdsp).
	writeDTProp(t, filepath.Join(dt, "remoteproc-cdsp@auto", "compatible"), "qcom,sc8280xp-cdsp-pas")

	return linuxRoots{deviceTree: dt, sys: sys, dev: dev}
}

func TestScanLinuxRoots_AllVendors(t *testing.T) {
	roots := buildFixtureRoots(t)
	npus := scanLinuxRoots(roots)

	byVendor := map[string]NPU{}
	for _, n := range npus {
		byVendor[n.Vendor] = n
	}

	// Rockchip RKNN is REQUIRED by the task closure.
	rk, ok := byVendor["Rockchip"]
	if !ok {
		t.Fatalf("expected Rockchip RKNN NPU, got %+v", npus)
	}
	if rk.Model != "Rockchip RKNN NPU" || rk.Runtime != "RKNN" {
		t.Errorf("rockchip fields wrong: %+v", rk)
	}

	// Task requires NVIDIA DLA OR Qualcomm — assert BOTH here for coverage.
	if _, ok := byVendor["NVIDIA"]; !ok {
		t.Errorf("expected NVIDIA DLA, got %+v", npus)
	}
	if _, ok := byVendor["Qualcomm"]; !ok {
		t.Errorf("expected Qualcomm Hexagon, got %+v", npus)
	}

	if len(npus) != 3 {
		t.Errorf("expected exactly 3 NPUs, got %d: %+v", len(npus), npus)
	}
}

func TestRockchip_DevfreqOnly(t *testing.T) {
	base := t.TempDir()
	sys := filepath.Join(base, "sys")
	if err := os.MkdirAll(filepath.Join(sys, "class", "devfreq", "ff9a0000.npu"), 0o755); err != nil {
		t.Fatal(err)
	}
	n, ok := detectRockchipRKNN(linuxRoots{sys: sys})
	if !ok {
		t.Fatal("expected RKNN detection via devfreq alone")
	}
	if n.Vendor != "Rockchip" {
		t.Errorf("wrong vendor: %q", n.Vendor)
	}
}

func TestRockchip_DevNodeOnly(t *testing.T) {
	base := t.TempDir()
	dev := filepath.Join(base, "dev")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dev, "rknpu"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := detectRockchipRKNN(linuxRoots{dev: dev}); !ok {
		t.Fatal("expected RKNN detection via /dev/rknpu alone")
	}
}

func TestNoNPU_EmptyRoots(t *testing.T) {
	base := t.TempDir()
	empty := linuxRoots{
		deviceTree: filepath.Join(base, "dt"),
		sys:        filepath.Join(base, "sys"),
		dev:        filepath.Join(base, "dev"),
	}
	for _, p := range []string{empty.deviceTree, empty.sys, empty.dev} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if n := scanLinuxRoots(empty); len(n) != 0 {
		t.Errorf("expected no NPUs on empty roots, got %+v", n)
	}
}

func TestReadDTString_NulHandling(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "compatible")
	// Two NUL-separated compatibles, as real device-tree emits.
	if err := os.WriteFile(p, []byte("rockchip,rk3588-rknpu\x00rockchip,rknpu\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readDTString(p)
	if got == "" || got[0] == 0x00 {
		t.Errorf("NUL not normalised: %q", got)
	}
}
