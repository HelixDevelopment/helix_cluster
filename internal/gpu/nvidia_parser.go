// Package gpu — pure NVIDIA /proc information parser.
// No build tag: runs on all platforms (M3, Linux, etc.).
// Does no I/O; all I/O is in nvidia_reader_linux.go (linux-only).
package gpu

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// NvidiaGPUInfo holds the fields extracted from a single
// /proc/driver/nvidia/gpus/<pci>/information file.
type NvidiaGPUInfo struct {
	Index    int
	PCIID    string
	Model    string
	UUID     string
	MemoryMB int
}

// ParseNvidiaInformation parses a single /proc/driver/nvidia/gpus/<pci>/information
// file body (supplied as a string). It recognises:
//
//	"Model:"         — GPU model name (required; missing → error)
//	"GPU UUID:"      — preferred UUID key
//	"UUID:"          — fallback UUID key
//	"Video Memory:"  — value in "MiB" or "MB" (malformed → MemoryMB=0, not error)
//
// Returns an error when the content contains no "Model:" line (empty/garbage input).
// The returned NvidiaGPUInfo has Index==0; the caller assigns the real index.
func ParseNvidiaInformation(content string) (NvidiaGPUInfo, error) {
	var info NvidiaGPUInfo

	lines := strings.Split(content, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)

		switch {
		case strings.HasPrefix(line, "Model:"):
			info.Model = strings.TrimSpace(strings.TrimPrefix(line, "Model:"))

		case strings.HasPrefix(line, "GPU UUID:"):
			info.UUID = strings.TrimSpace(strings.TrimPrefix(line, "GPU UUID:"))

		case strings.HasPrefix(line, "UUID:") && info.UUID == "":
			// Only use bare "UUID:" when "GPU UUID:" has not been seen yet.
			info.UUID = strings.TrimSpace(strings.TrimPrefix(line, "UUID:"))

		case strings.HasPrefix(line, "Video Memory:"):
			memStr := strings.TrimSpace(strings.TrimPrefix(line, "Video Memory:"))
			// Strip unit suffixes; tolerate "40960 MiB", "40960MiB", "4096 MB", "4096MB".
			memStr = strings.TrimSuffix(memStr, "MiB")
			memStr = strings.TrimSuffix(memStr, "MB")
			memStr = strings.TrimSpace(memStr)
			if v, err := strconv.Atoi(memStr); err == nil {
				info.MemoryMB = v
			}
			// Malformed value → leave MemoryMB==0 and continue (not an error).
		}
	}

	if info.Model == "" {
		return NvidiaGPUInfo{}, fmt.Errorf("nvidia_parser: no Model line found in information content")
	}
	return info, nil
}

// ParseNvidiaProcTree parses a set of /proc/driver/nvidia/gpus "information"
// file contents.  The map key is the full path (e.g.
// "/proc/driver/nvidia/gpus/0000:01:00.0/information"); the value is the file
// body.
//
// Each entry whose content contains no "Model:" line is silently skipped (not
// an error).  Surviving entries are sorted by map key and assigned contiguous
// Index values starting at 0.  The PCIID is derived from the last path
// component of the directory (the PCI address portion, one directory up from
// the "information" leaf).
//
// An empty map returns ([]NvidiaGPUInfo{}, nil).
func ParseNvidiaProcTree(files map[string]string) ([]NvidiaGPUInfo, error) {
	if len(files) == 0 {
		return []NvidiaGPUInfo{}, nil
	}

	// Sort keys for deterministic Index assignment.
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]NvidiaGPUInfo, 0, len(keys))
	for _, path := range keys {
		content := files[path]
		info, err := ParseNvidiaInformation(content)
		if err != nil {
			// No Model line → skip this entry (not a fatal error for the tree).
			continue
		}

		// Derive PCI ID from the path: the "information" file lives at
		//   <basePath>/<pciaddr>/information
		// so the PCI address is one directory up from the leaf.
		info.PCIID = pciIDFromPath(path)
		info.Index = len(result)
		result = append(result, info)
	}

	return result, nil
}

// pciIDFromPath extracts the PCI address directory from a path of the form
// ".../0000:01:00.0/information". It strips the trailing "/information" leaf
// (or any leaf) and returns the last remaining path segment.
func pciIDFromPath(path string) string {
	// Normalise separators and strip trailing slash.
	p := strings.TrimRight(path, "/")
	// Drop the leaf filename ("information").
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		p = p[:idx]
	}
	// Return the last segment (the PCI address directory).
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}
