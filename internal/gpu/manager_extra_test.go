package gpu

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAllocateGPUIdempotentPerJob proves that allocating twice for the same
// job returns the same single GPU rather than consuming a second device.
// Mutation: drop the "honor existing allocation" loop in AllocateGPU so it
// always grabs a new Available GPU -> the two IDs differ and the count of
// allocated GPUs becomes 2, failing this test.
func TestAllocateGPUIdempotentPerJob(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}

	first, err := mgr.AllocateGPU("job-x")
	if err != nil {
		t.Fatalf("first AllocateGPU failed: %v", err)
	}
	second, err := mgr.AllocateGPU("job-x")
	if err != nil {
		t.Fatalf("second AllocateGPU failed: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same GPU for same job, got %s then %s", first.ID, second.ID)
	}

	allocated := 0
	for _, g := range mgr.ListGPUs() {
		if g.Status == Allocated {
			allocated++
			if g.AllocatedTo != "job-x" {
				t.Errorf("unexpected AllocatedTo: %s", g.AllocatedTo)
			}
		}
	}
	if allocated != 1 {
		t.Fatalf("expected exactly 1 allocated GPU, got %d", allocated)
	}
}

// TestAllocateGPUEmptyJobRejected proves empty jobIDs are rejected.
// Mutation: remove the `if jobID == ""` guard in AllocateGPU -> the empty
// jobID succeeds and err is nil, failing this test.
func TestAllocateGPUEmptyJobRejected(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}
	if _, err := mgr.AllocateGPU(""); err == nil {
		t.Fatal("expected error allocating with empty jobID")
	}
	// Inventory must be untouched.
	for _, g := range mgr.ListGPUs() {
		if g.Status != Available {
			t.Fatalf("empty-job allocation must not change inventory; GPU %s is %s", g.ID, g.Status)
		}
	}
}

// TestAllocateBeforeDetectRejected proves the started lifecycle guard is real:
// allocation on a Manager that has not run DetectGPUs is refused with a clear
// error instead of silently returning "no available GPUs" (or, worse, being
// treated as a usable empty pool). This is the assertion that gives the
// `started atomic.Bool` field its meaning.
// Mutation: delete `m.started.Store(true)` in DetectGPUs (or the
// `if !m.started.Load()` guard in AllocateGPU) -> either the guard fires even
// after detection (DetectGPUs path) or allocation proceeds pre-detection
// (guard path); both break one of the two assertions below.
func TestAllocateBeforeDetectRejected(t *testing.T) {
	mgr := NewManager()

	// Before DetectGPUs: both allocation entry points must refuse.
	if _, err := mgr.AllocateGPU("early-job"); err == nil {
		t.Fatal("expected error allocating before DetectGPUs")
	}
	if _, err := mgr.AllocateGPUByMemory("early-job", 1024); err == nil {
		t.Fatal("expected error allocating-by-memory before DetectGPUs")
	}

	// After DetectGPUs: allocation must now succeed, proving the guard is gated
	// on detection and not unconditionally on.
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}
	if _, err := mgr.AllocateGPU("ready-job"); err != nil {
		t.Fatalf("expected allocation to succeed after DetectGPUs, got %v", err)
	}
}

// TestAllocateGPUByMemoryBestFit proves the smallest-fitting GPU is chosen so
// larger GPUs stay free. The mock inventory has two 40960MB and two 81920MB
// GPUs; a 50000MB request must take an 81920 GPU, but a 30000MB request must
// take a 40960 GPU (best fit), leaving an 81920 free.
// Mutation: change the best-fit comparison `gpu.MemoryMB < best.MemoryMB` to
// `>` (worst fit) -> the 30000MB request grabs an 81920 GPU and the assertion
// on returned MemoryMB fails.
func TestAllocateGPUByMemoryBestFit(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}

	g, err := mgr.AllocateGPUByMemory("small-job", 30000)
	if err != nil {
		t.Fatalf("AllocateGPUByMemory failed: %v", err)
	}
	if g.MemoryMB != 40960 {
		t.Fatalf("best-fit expected 40960MB GPU, got %dMB", g.MemoryMB)
	}
}

// TestAllocateGPUByMemoryInsufficient proves a too-large request fails when no
// GPU satisfies it.
// Mutation: change `gpu.MemoryMB < minMemoryMB` skip-condition to
// `gpu.MemoryMB < 0` -> an undersized GPU gets allocated and err is nil.
func TestAllocateGPUByMemoryInsufficient(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}
	if _, err := mgr.AllocateGPUByMemory("huge-job", 1_000_000); err == nil {
		t.Fatal("expected error: no GPU large enough")
	}
}

// TestAllocateGPUByMemoryIdempotentUndersized proves a job already holding an
// undersized GPU is reported, not silently handed a too-small device.
// Mutation: delete the `gpu.MemoryMB < minMemoryMB` undersized check in the
// idempotency branch -> it returns the small GPU with nil err, failing here.
func TestAllocateGPUByMemoryIdempotentUndersized(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}
	if _, err := mgr.AllocateGPUByMemory("job-y", 30000); err != nil {
		t.Fatalf("initial allocation failed: %v", err)
	}
	// Same job now needs more memory than its 40960MB GPU? No -- request bigger
	// than the held device to trigger the undersized path.
	if _, err := mgr.AllocateGPUByMemory("job-y", 60000); err == nil {
		t.Fatal("expected error: held GPU is undersized for new request")
	}
}

// TestStatsReflectInventory proves Stats counts statuses and memory correctly.
// Mutation: in Stats, accumulate FreeMemoryMB for Allocated instead of
// Available -> FreeMemoryMB after one allocation no longer drops, failing the
// assertion below.
func TestStatsReflectInventory(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}

	before := mgr.Stats()
	if before.Total == 0 {
		t.Fatal("expected non-zero total GPUs")
	}
	if before.Available != before.Total {
		t.Fatalf("expected all %d GPUs available, got %d", before.Total, before.Available)
	}
	if before.FreeMemoryMB != before.TotalMemoryMB {
		t.Fatalf("expected free==total memory when all available: %d vs %d", before.FreeMemoryMB, before.TotalMemoryMB)
	}

	g, err := mgr.AllocateGPU("stats-job")
	if err != nil {
		t.Fatalf("AllocateGPU failed: %v", err)
	}

	after := mgr.Stats()
	if after.Allocated != 1 {
		t.Fatalf("expected 1 allocated, got %d", after.Allocated)
	}
	if after.Available != before.Available-1 {
		t.Fatalf("expected available to drop by 1, %d -> %d", before.Available, after.Available)
	}
	if after.FreeMemoryMB != before.FreeMemoryMB-g.MemoryMB {
		t.Fatalf("expected free memory to drop by %d, %d -> %d", g.MemoryMB, before.FreeMemoryMB, after.FreeMemoryMB)
	}
	if after.TotalMemoryMB != before.TotalMemoryMB {
		t.Fatalf("total memory must not change on allocation: %d -> %d", before.TotalMemoryMB, after.TotalMemoryMB)
	}
}

// TestSetOfflineRejectsAllocated proves an allocated GPU cannot be taken
// offline (which would silently evict a running job).
// Mutation: remove the `if gpu.Status == Allocated { return err }` guard in
// SetOffline -> the allocated GPU is forced Offline and err is nil.
func TestSetOfflineRejectsAllocated(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}

	g, err := mgr.AllocateGPU("busy-job")
	if err != nil {
		t.Fatalf("AllocateGPU failed: %v", err)
	}
	if err := mgr.SetOffline(g.ID); err == nil {
		t.Fatal("expected error taking allocated GPU offline")
	}
	// GPU must remain Allocated, not Offline.
	got, err := mgr.GetGPUByID(g.ID)
	if err != nil {
		t.Fatalf("GetGPUByID failed: %v", err)
	}
	if got.Status != Allocated {
		t.Fatalf("GPU status changed to %s after rejected SetOffline", got.Status)
	}
}

// TestSetOfflineThenOnlineRoundTrip proves an available GPU can be drained and
// restored, and that an offline GPU is not allocatable.
// Mutation: make SetOffline set Status = Available instead of Offline -> the
// GPU stays allocatable and the "no allocation while one offline" check below
// fails (the drained GPU gets allocated).
func TestSetOfflineThenOnlineRoundTrip(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}

	all := mgr.ListGPUs()
	target := all[0].ID

	if err := mgr.SetOffline(target); err != nil {
		t.Fatalf("SetOffline failed: %v", err)
	}
	got, _ := mgr.GetGPUByID(target)
	if got.Status != Offline {
		t.Fatalf("expected Offline, got %s", got.Status)
	}

	// Allocate every remaining GPU; the offline one must never be handed out.
	// We must observe exactly len(all)-1 successful allocations and then
	// exhaustion via the specific "no available GPUs" error -- not an unrelated
	// early failure that would leave the offline GPU untested.
	allocated := 0
	for i := 0; i < len(all); i++ {
		g, err := mgr.AllocateGPU("drain-job-" + string(rune('a'+i)))
		if err != nil {
			if err.Error() != "no available GPUs" {
				t.Fatalf("expected exhaustion error %q, got %v", "no available GPUs", err)
			}
			break
		}
		if g.ID == target {
			t.Fatalf("offline GPU %s was allocated", target)
		}
		allocated++
	}
	if allocated != len(all)-1 {
		t.Fatalf("expected exactly %d GPUs allocated before exhaustion (offline excluded), got %d", len(all)-1, allocated)
	}

	if err := mgr.SetOnline(target); err != nil {
		t.Fatalf("SetOnline failed: %v", err)
	}
	got, _ = mgr.GetGPUByID(target)
	if got.Status != Available {
		t.Fatalf("expected Available after SetOnline, got %s", got.Status)
	}
}

// TestSetOnlineLeavesAllocatedUnchanged proves SetOnline only revives Offline
// GPUs and does not clobber an Allocated one.
// Mutation: remove the `if gpu.Status == Offline` guard in SetOnline -> the
// allocated GPU is reset to Available and its job binding is lost.
func TestSetOnlineLeavesAllocatedUnchanged(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}
	g, err := mgr.AllocateGPU("keep-job")
	if err != nil {
		t.Fatalf("AllocateGPU failed: %v", err)
	}
	if err := mgr.SetOnline(g.ID); err != nil {
		t.Fatalf("SetOnline failed: %v", err)
	}
	got, _ := mgr.GetGPUByID(g.ID)
	if got.Status != Allocated || got.AllocatedTo != "keep-job" {
		t.Fatalf("SetOnline corrupted allocated GPU: status=%s allocatedTo=%q", got.Status, got.AllocatedTo)
	}
}

// TestManagerMonitoringWiring proves the Manager-owned Monitor actually runs
// and refreshes GPU metrics (the monitor field was previously dead wiring).
// Mutation: make StartMonitoring return without calling mon.Start(ctx) -> the
// loop goroutine never runs, no metrics are refreshed, temperatures stay 0.0
// (< 30.0), and the assertion fails.
func TestManagerMonitoringWiring(t *testing.T) {
	mgr := NewManager()
	if err := mgr.DetectGPUs(); err != nil {
		t.Fatalf("DetectGPUs failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mon, err := mgr.StartMonitoring(ctx, WithInterval(20*time.Millisecond))
	if err != nil {
		t.Fatalf("StartMonitoring failed: %v", err)
	}
	if !mon.IsRunning() {
		t.Fatal("expected monitor running after StartMonitoring")
	}

	// Second call must be idempotent and return the same monitor.
	mon2, err := mgr.StartMonitoring(ctx)
	if err != nil {
		t.Fatalf("second StartMonitoring failed: %v", err)
	}
	if mon2 != mon {
		t.Fatal("expected StartMonitoring to be idempotent and return the running monitor")
	}

	// Wait for the monitor to write metrics.
	deadline := time.Now().Add(2 * time.Second)
	updated := false
	for time.Now().Before(deadline) {
		for _, g := range mgr.ListGPUs() {
			// The mock seeds temperature in [30, 100) (30.0 + rand*70.0), so a
			// genuinely-monitored GPU reads >= 30.0. Asserting >= 30.0 (not
			// merely > 0) ties this to the known mock floor and rejects a broken
			// monitor that writes an epsilon-above-zero value.
			if g.TemperatureC >= 30.0 {
				updated = true
				break
			}
		}
		if updated {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mgr.StopMonitoring()

	if !updated {
		t.Fatal("expected monitor to refresh GPU temperature metrics")
	}
	if mon.IsRunning() {
		t.Fatal("expected monitor stopped after StopMonitoring")
	}
}

// TestDetectGPUsFromParsesInformation drives the detection parser against a
// synthetic /proc-style tree (works on every OS) and proves it extracts Model
// and Video Memory, assigns contiguous IDs even when a non-GPU file precedes
// the GPU dirs, and skips a directory whose "information" file is missing.
// Mutation: revert the ID indexing from `len(gpus)` back to a loop index over
// all entries -> the leading stray file shifts the first GPU's ID to "gpu-1",
// failing the contiguous-ID assertion.
func TestDetectGPUsFromParsesInformation(t *testing.T) {
	base := t.TempDir()

	// A stray non-directory entry that sorts before the GPU dirs.
	if err := os.WriteFile(filepath.Join(base, "00-stray"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	// GPU 0: full info.
	d0 := filepath.Join(base, "0000:01:00.0")
	if err := os.MkdirAll(d0, 0o755); err != nil {
		t.Fatal(err)
	}
	info0 := "Model: \t NVIDIA A100-SXM4-40GB\nIRQ: 42\nVideo Memory: 40960 MiB\n"
	if err := os.WriteFile(filepath.Join(d0, "information"), []byte(info0), 0o644); err != nil {
		t.Fatal(err)
	}

	// GPU 1: model present, no memory line (memory should stay 0).
	d1 := filepath.Join(base, "0000:02:00.0")
	if err := os.MkdirAll(d1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d1, "information"), []byte("Model: NVIDIA H100\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A directory with NO information file -> must be skipped, not counted.
	if err := os.MkdirAll(filepath.Join(base, "0000:03:00.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	gpus, err := detectGPUsFrom(base)
	if err != nil {
		t.Fatalf("detectGPUsFrom failed: %v", err)
	}
	if len(gpus) != 2 {
		t.Fatalf("expected 2 GPUs (stray file and info-less dir skipped), got %d", len(gpus))
	}

	if gpus[0].ID != "gpu-0" {
		t.Fatalf("expected first GPU ID gpu-0, got %s", gpus[0].ID)
	}
	if gpus[1].ID != "gpu-1" {
		t.Fatalf("expected second GPU ID gpu-1, got %s", gpus[1].ID)
	}
	if gpus[0].Model != "NVIDIA A100-SXM4-40GB" {
		t.Fatalf("model parse mismatch: %q", gpus[0].Model)
	}
	if gpus[0].MemoryMB != 40960 {
		t.Fatalf("memory parse mismatch: got %d, want 40960", gpus[0].MemoryMB)
	}
	if gpus[1].MemoryMB != 0 {
		t.Fatalf("expected 0 memory when no Video Memory line, got %d", gpus[1].MemoryMB)
	}
}

// TestDetectGPUsFromMissingPath proves a non-existent base path is a hard error
// (so DetectGPUs falls back to mock) rather than returning a phantom GPU list.
// Mutation: swallow the os.ReadDir error and `return nil, nil` -> err is nil
// here, failing the assertion.
func TestDetectGPUsFromMissingPath(t *testing.T) {
	if _, err := detectGPUsFrom(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing detection path")
	}
}
