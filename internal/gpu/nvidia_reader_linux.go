//go:build linux

package gpu

import (
	"fmt"
	"os"
	"path/filepath"
)

// readNvidiaProcTree walks basePath/*/information, reads each file into a
// map[path]content and returns it.  Entries that cannot be read are silently
// skipped (the GPU may simply not expose that file on older drivers); an error
// is returned only when basePath itself is unreadable.
func readNvidiaProcTree(basePath string) (map[string]string, error) {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return nil, fmt.Errorf("readNvidiaProcTree: ReadDir %q: %w", basePath, err)
	}

	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		infoPath := filepath.Join(basePath, entry.Name(), "information")
		data, err := os.ReadFile(infoPath)
		if err != nil {
			// Skip unreadable entries (permission, absent file, etc.).
			continue
		}
		result[infoPath] = string(data)
	}
	return result, nil
}

// DetectNvidiaViaProc detects NVIDIA GPUs by parsing the kernel-exposed
// /proc/driver/nvidia/gpus tree.  It combines readNvidiaProcTree (real I/O)
// with ParseNvidiaProcTree (pure parsing).
//
// Available only on Linux (build-tagged); callers on other platforms must use
// the platform-specific detectGPUsPlatform path.
func DetectNvidiaViaProc() ([]NvidiaGPUInfo, error) {
	files, err := readNvidiaProcTree(nvidiaProcPath)
	if err != nil {
		return nil, fmt.Errorf("DetectNvidiaViaProc: %w", err)
	}
	return ParseNvidiaProcTree(files)
}
