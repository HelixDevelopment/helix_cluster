package gpu

// mockInventory returns a deterministic four-GPU inventory (two 40960MB and two
// 81920MB devices) used by unit tests that need a controlled multi-GPU pool.
// This is TEST-ONLY: production detection (DetectGPUs -> detectGPUsPlatform)
// reads real hardware and never fabricates devices.
func mockInventory() []*GPU {
	return []*GPU{
		{
			ID:       "gpu-0",
			UUID:     "GPU-MOCK-0000-0000-0000-000000000001",
			Model:    "NVIDIA A100-SXM4-40GB",
			MemoryMB: 40960,
			Status:   Available,
		},
		{
			ID:       "gpu-1",
			UUID:     "GPU-MOCK-0000-0000-0000-000000000002",
			Model:    "NVIDIA A100-SXM4-40GB",
			MemoryMB: 40960,
			Status:   Available,
		},
		{
			ID:       "gpu-2",
			UUID:     "GPU-MOCK-0000-0000-0000-000000000003",
			Model:    "NVIDIA H100-SXM5-80GB",
			MemoryMB: 81920,
			Status:   Available,
		},
		{
			ID:       "gpu-3",
			UUID:     "GPU-MOCK-0000-0000-0000-000000000004",
			Model:    "NVIDIA H100-SXM5-80GB",
			MemoryMB: 81920,
			Status:   Available,
		},
	}
}

// newManagerWithMockInventory builds a Manager pre-seeded with mockInventory via
// the public InjectGPUsForTest seam, so allocation/stats/offline tests run
// deterministically on any host without touching real hardware.
func newManagerWithMockInventory() *Manager {
	m := NewManager()
	m.InjectGPUsForTest(mockInventory())
	return m
}
