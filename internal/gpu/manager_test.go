package gpu

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// TestInjectGPUsForTestSeam proves the test-only injection seam installs a
// controlled inventory and marks the Manager started (so allocation works)
// without touching real hardware. This replaces the former DetectGPUs->mock
// path that production no longer has.
// Mutation: drop `m.started.Store(true)` in InjectGPUsForTest -> AllocateGPU
// below fails the "not detected" guard.
func TestInjectGPUsForTestSeam(t *testing.T) {
	mgr := newManagerWithMockInventory()

	gpus := mgr.ListGPUs()
	if len(gpus) != 4 {
		t.Fatalf("expected 4 injected GPUs, got %d", len(gpus))
	}

	for _, gpu := range gpus {
		if gpu.ID == "" {
			t.Error("GPU ID should not be empty")
		}
		if gpu.UUID == "" {
			t.Error("GPU UUID should not be empty")
		}
		if gpu.Model == "" {
			t.Error("GPU Model should not be empty")
		}
		if gpu.Status != Available {
			t.Errorf("expected status Available, got %s", gpu.Status)
		}
	}

	// Injection must also start the Manager so allocation is permitted.
	if _, err := mgr.AllocateGPU("seam-job"); err != nil {
		t.Fatalf("expected allocation after injection, got %v", err)
	}
}

func TestAllocateAndReleaseGPU(t *testing.T) {
	mgr := newManagerWithMockInventory()

	jobID := "job-123"
	gpu, err := mgr.AllocateGPU(jobID)
	if err != nil {
		t.Fatalf("AllocateGPU failed: %v", err)
	}
	if gpu.AllocatedTo != jobID {
		t.Errorf("expected AllocatedTo=%s, got %s", jobID, gpu.AllocatedTo)
	}
	if gpu.Status != Allocated {
		t.Errorf("expected status Allocated, got %s", gpu.Status)
	}

	// Verify internal state
	gpus := mgr.ListGPUs()
	var found bool
	for _, g := range gpus {
		if g.ID == gpu.ID {
			found = true
			if g.Status != Allocated {
				t.Errorf("expected status Allocated in list, got %s", g.Status)
			}
			break
		}
	}
	if !found {
		t.Fatal("allocated GPU not found in list")
	}

	mgr.ReleaseGPU(jobID)

	gpus = mgr.ListGPUs()
	for _, g := range gpus {
		if g.ID == gpu.ID {
			if g.Status != Available {
				t.Errorf("expected status Available after release, got %s", g.Status)
			}
			if g.AllocatedTo != "" {
				t.Errorf("expected AllocatedTo empty after release, got %s", g.AllocatedTo)
			}
			break
		}
	}
}

func TestAllocateGPUExhaustion(t *testing.T) {
	mgr := newManagerWithMockInventory()

	gpus := mgr.ListGPUs()
	// Allocate all GPUs
	for i := range gpus {
		_, err := mgr.AllocateGPU("job-" + string(rune('A'+i)))
		if err != nil {
			t.Fatalf("AllocateGPU failed on iteration %d: %v", i, err)
		}
	}

	// Next allocation should fail
	_, err := mgr.AllocateGPU("job-excess")
	if err == nil {
		t.Fatal("expected error when no GPUs available")
	}
}

func TestConcurrentAllocation(t *testing.T) {
	mgr := newManagerWithMockInventory()

	gpus := mgr.ListGPUs()
	numGPUs := len(gpus)

	var wg sync.WaitGroup
	numWorkers := numGPUs + 5
	results := make(chan error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			jobID := "job-concurrent-" + string(rune('0'+idx))
			_, err := mgr.AllocateGPU(jobID)
			results <- err
		}(i)
	}

	wg.Wait()
	close(results)

	successCount := 0
	failCount := 0
	for err := range results {
		if err == nil {
			successCount++
		} else {
			failCount++
		}
	}

	if successCount != numGPUs {
		t.Fatalf("expected %d successful allocations, got %d", numGPUs, successCount)
	}
	if failCount != 5 {
		t.Fatalf("expected 5 failures, got %d", failCount)
	}
}

func TestGPUJSONSerialization(t *testing.T) {
	gpu := &GPU{
		ID:                 "gpu-test",
		UUID:               "GPU-TEST-UUID",
		Model:              "Test Model",
		MemoryMB:           16384,
		UtilizationPercent: 45.5,
		TemperatureC:       65.0,
		AllocatedTo:        "job-abc",
		Status:             Allocated,
	}

	data, err := json.Marshal(gpu)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var restored GPU
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if restored.ID != gpu.ID {
		t.Errorf("ID mismatch: %s vs %s", restored.ID, gpu.ID)
	}
	if restored.Status != gpu.Status {
		t.Errorf("Status mismatch: %s vs %s", restored.Status, gpu.Status)
	}
}

func TestMonitorLifecycle(t *testing.T) {
	mgr := newManagerWithMockInventory()

	mon := newMonitor(mgr, WithInterval(100*time.Millisecond))
	if mon.IsRunning() {
		t.Fatal("monitor should not be running before Start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mon.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !mon.IsRunning() {
		t.Fatal("monitor should be running after Start")
	}

	// Wait for at least one check cycle
	time.Sleep(250 * time.Millisecond)

	mon.Stop()
	if mon.IsRunning() {
		t.Fatal("monitor should not be running after Stop")
	}
}

func TestMonitorUpdatesGPUHealth(t *testing.T) {
	mgr := newManagerWithMockInventory()

	// Use very low thresholds so mock random values will exceed them frequently
	mon := newMonitor(mgr, WithInterval(50*time.Millisecond), WithTemperatureThreshold(1.0), WithUtilizationThreshold(1.0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mon.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for several check cycles
	time.Sleep(300 * time.Millisecond)
	mon.Stop()

	gpus := mgr.ListGPUs()
	unhealthyCount := 0
	for _, gpu := range gpus {
		if gpu.Status == Unhealthy {
			unhealthyCount++
		}
		if gpu.TemperatureC == 0 {
			t.Error("expected temperature to be updated")
		}
	}

	if unhealthyCount == 0 {
		t.Log("no GPUs marked unhealthy; this is possible but unlikely with such low thresholds")
	}
}

func TestGetGPUByID(t *testing.T) {
	mgr := newManagerWithMockInventory()

	gpus := mgr.ListGPUs()
	if len(gpus) == 0 {
		t.Fatal("expected at least one GPU")
	}

	gpu, err := mgr.GetGPUByID(gpus[0].ID)
	if err != nil {
		t.Fatalf("GetGPUByID failed: %v", err)
	}
	if gpu.ID != gpus[0].ID {
		t.Errorf("ID mismatch: %s vs %s", gpu.ID, gpus[0].ID)
	}

	_, err = mgr.GetGPUByID("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent GPU")
	}
}
