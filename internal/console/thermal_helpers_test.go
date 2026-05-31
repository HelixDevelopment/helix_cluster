package console

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// readZone / readZonesLinux are OS-independent; the GOOS gate lives in
// ReadZones. Exercise the milli-degree parsing and zone discovery directly.

func TestReadZone_ParsesMilliCelsius(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "type"), []byte("x86_pkg_temp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "temp"), []byte("62500\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tm := NewThermalMonitor()
	z, err := tm.readZone(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutation: change /1000.0 to /1.0 -> 62500 != 62.5 and this fails.
	if z.TempC != 62.5 {
		t.Errorf("expected 62.5C, got %f", z.TempC)
	}
	if z.Name != "x86_pkg_temp" || z.Type != "x86_pkg_temp" {
		t.Errorf("expected type x86_pkg_temp, got name=%q type=%q", z.Name, z.Type)
	}
	if z.Path != dir {
		t.Errorf("expected path %q, got %q", dir, z.Path)
	}
}

func TestReadZone_MissingTempIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "type"), []byte("cpu\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tm := NewThermalMonitor()
	// No "temp" file present.
	if _, err := tm.readZone(dir); err == nil {
		t.Fatal("expected error when temp file is missing")
	}
}

func TestReadZone_NonNumericTempIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "type"), []byte("cpu\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "temp"), []byte("not-a-number\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tm := NewThermalMonitor()
	// Mutation: ignore the ParseInt error and proceed -> this expectation fails.
	if _, err := tm.readZone(dir); err == nil {
		t.Fatal("expected error for non-numeric temp")
	}
}

func TestReadZonesLinux_DiscoversAndSkips(t *testing.T) {
	base := t.TempDir()
	// zone0: valid.
	z0 := filepath.Join(base, "thermal_zone0")
	if err := os.MkdirAll(z0, 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(z0, "type"), "soc_thermal\n")
	mustWrite(t, filepath.Join(z0, "temp"), "40000\n")
	// zone1: unreadable temp -> must be skipped, not fatal.
	z1 := filepath.Join(base, "thermal_zone1")
	if err := os.MkdirAll(z1, 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(z1, "type"), "broken\n")
	// non-zone dir: must be ignored.
	other := filepath.Join(base, "cooling_device0")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(other, "temp"), "99000\n")

	tm := NewThermalMonitor()
	tm.sysfsBase = base
	zones, err := tm.readZonesLinux()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only zone0 is fully readable; zone1 skipped; cooling_device0 ignored.
	// Mutation: drop the `HasPrefix(entry.Name(), "thermal_zone")` filter ->
	// cooling_device0 would be parsed (a second zone) and this count fails.
	if len(zones) != 1 {
		t.Fatalf("expected exactly 1 readable zone, got %d", len(zones))
	}
	if zones[0].Name != "soc_thermal" || zones[0].TempC != 40.0 {
		t.Errorf("unexpected zone: %+v", zones[0])
	}
}

func TestReadZonesLinux_MissingBaseIsEmpty(t *testing.T) {
	tm := NewThermalMonitor()
	tm.sysfsBase = filepath.Join(t.TempDir(), "absent")
	zones, err := tm.readZonesLinux()
	// Mutation: change the os.IsNotExist branch to `return nil, err` -> fails.
	if err != nil {
		t.Fatalf("expected nil error for absent base, got %v", err)
	}
	if len(zones) != 0 {
		t.Errorf("expected 0 zones, got %d", len(zones))
	}
}

// Zones() returns a defensive copy; mutating the returned slice must not
// corrupt the monitor's internal state.
func TestZones_ReturnsDefensiveCopy(t *testing.T) {
	tm := NewThermalMonitor()
	tm.mu.Lock()
	tm.zones = []ThermalZone{{Name: "a", TempC: 10}, {Name: "b", TempC: 20}}
	tm.mu.Unlock()

	got := tm.Zones()
	if len(got) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(got))
	}
	got[0].TempC = 999 // mutate the copy

	again := tm.Zones()
	// Mutation: return tm.zones directly instead of copy(out, tm.zones) ->
	// the 999 write would leak back and this fails.
	if again[0].TempC != 10 {
		t.Errorf("internal state mutated through returned slice: %f", again[0].TempC)
	}
}

// The RWMutex-protected accessors must be safe under concurrent ReadZones /
// Zones / MaxTemp / ThrottleDetected. Run under -race to catch data races.
func TestThermalMonitor_ConcurrentAccess(t *testing.T) {
	base := t.TempDir()
	z := filepath.Join(base, "thermal_zone0")
	if err := os.MkdirAll(z, 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(z, "type"), "cpu\n")
	mustWrite(t, filepath.Join(z, "temp"), "50000\n")

	tm := NewThermalMonitor()
	tm.sysfsBase = base
	// Seed internal zones so readers have data even on non-linux where
	// ReadZones is a NoOp.
	tm.mu.Lock()
	tm.zones = []ThermalZone{{Name: "cpu", TempC: 50}}
	tm.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = tm.ReadZones()
				_ = tm.Zones()
				_, _ = tm.MaxTemp()
				_ = tm.ThrottleDetected(70)
			}
		}()
	}
	wg.Wait()
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
