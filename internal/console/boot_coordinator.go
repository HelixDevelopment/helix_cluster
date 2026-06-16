package console

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// BootPhase is the readiness phase of a node as inferred from kernel/firmware
// boot markers (roadmap §4.3, HXC-1147). It is distinct from BootState (the
// control-plane node-lifecycle FSM in linux_boot.go): BootPhase describes what
// the *local* boot has reached, read from on-disk markers, and gates node
// registration via WaitReady.
//
// The phases form a strict readiness ladder:
//
//	Unknown -> Booting -> UserspaceReady -> ClusterReady
type BootPhase string

const (
	// PhaseUnknown means no usable boot markers were observed yet (e.g. the
	// kernel version marker is absent). The node has not demonstrably booted.
	PhaseUnknown BootPhase = "Unknown"

	// PhaseBooting means the kernel is up (a kernel version marker exists) but
	// userspace has not signalled readiness.
	PhaseBooting BootPhase = "Booting"

	// PhaseUserspaceReady means userspace has signalled readiness via the boot
	// markers, but the node has not yet joined the cluster.
	PhaseUserspaceReady BootPhase = "UserspaceReady"

	// PhaseClusterReady means the node has signalled cluster readiness and may
	// proceed to register with the control plane.
	PhaseClusterReady BootPhase = "ClusterReady"
)

// BootMarkers holds the injectable paths the BootCoordinator reads. In
// production these point at Linux kernel/firmware markers
// (/proc/version, /sys/firmware/devicetree/base, /proc/cmdline); in tests they
// point at fixtures. Empty paths are treated as "marker absent".
//
// The markers are read with plain os file operations, so the coordinator builds
// and runs on every OS (CLAUDE-2): the /proc and /sys paths are Linux-specific
// *inputs*, not Linux-specific *code*. On a non-Linux host those default paths
// simply do not exist and the coordinator reports PhaseUnknown — which is the
// honest answer, not a faked success.
type BootMarkers struct {
	// VersionPath is the kernel version marker (default /proc/version). Its
	// presence advances the node out of PhaseUnknown into at least PhaseBooting.
	VersionPath string

	// DeviceTreePath is the firmware device-tree marker
	// (default /sys/firmware/devicetree/base/model or similar). Its value is
	// recorded to the boot log for diagnostics, and it is load-bearing for
	// cluster readiness: if the marker is present it MUST corroborate cluster
	// membership (not contain the negation token). A present-but-contradicting
	// device-tree marker (e.g. "helix,no-cluster") holds the node at
	// PhaseUserspaceReady even when the cmdline asserts cluster readiness, so a
	// mis-provisioned board cannot register. An absent marker is permitted (it
	// is simply unavailable on that platform) and does not block readiness.
	DeviceTreePath string

	// CmdlinePath is the kernel command line (default /proc/cmdline). Helix
	// readiness tokens (helix.userspace=ready, helix.cluster=ready) on this line
	// drive the UserspaceReady / ClusterReady phases.
	CmdlinePath string

	// BootLogPath is where the per-run UUID + probed phase are appended, proving
	// a live invocation. Required for sink-side evidence.
	BootLogPath string
}

// DefaultBootMarkers returns the production Linux marker paths.
func DefaultBootMarkers() BootMarkers {
	return BootMarkers{
		VersionPath:    "/proc/version",
		DeviceTreePath: "/sys/firmware/devicetree/base/model",
		CmdlinePath:    "/proc/cmdline",
		BootLogPath:    "/var/log/helix/boot.log",
	}
}

// Readiness tokens expected on the kernel command line.
const (
	userspaceReadyToken = "helix.userspace=ready"
	clusterReadyToken   = "helix.cluster=ready" //gosec:disable G101 -- kernel cmdline readiness sentinel string, not a credential
)

// deviceTreeNoClusterToken, when present in the device-tree marker, explicitly
// negates cluster membership: a board that reports it is held below
// ClusterReady regardless of the cmdline tokens.
const deviceTreeNoClusterToken = "no-cluster"

// BootCoordinator reads kernel/firmware boot markers via injectable paths,
// classifies the node's BootPhase, and gates node registration via WaitReady
// until the node reaches PhaseClusterReady. Each coordinator mints a per-run
// UUID and records every probe to the boot log, proving a live invocation.
//
// A BootCoordinator is safe for concurrent use.
type BootCoordinator struct {
	// PollInterval is how often WaitReady re-probes the markers. Defaults to
	// 250ms if left zero.
	PollInterval time.Duration

	markers BootMarkers
	runUUID string

	mu      sync.Mutex
	phase   BootPhase
	now     func() time.Time
	readAll func(path string) ([]byte, error) // injectable for tests; defaults to os.ReadFile
	appendf func(path, line string) error     // injectable for tests; defaults to file append
}

// NewBootCoordinator creates a coordinator over the given marker paths and mints
// a fresh per-run UUID.
func NewBootCoordinator(markers BootMarkers) *BootCoordinator {
	return &BootCoordinator{
		PollInterval: 250 * time.Millisecond,
		markers:      markers,
		runUUID:      newUUIDv4(),
		phase:        PhaseUnknown,
		now:          func() time.Time { return time.Now().UTC() },
		readAll:      os.ReadFile,
		appendf:      appendLine,
	}
}

// RunUUID returns this coordinator run's unique identifier.
func (b *BootCoordinator) RunUUID() string { return b.runUUID }

// CurrentPhase returns the phase observed by the most recent Probe (PhaseUnknown
// if none yet). Safe for concurrent use.
func (b *BootCoordinator) CurrentPhase() BootPhase {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.phase
}

// Probe reads the marker set once, classifies the BootPhase, records the phase,
// appends a UUID-stamped line to the boot log (live-invocation evidence), and
// returns the phase. Safe for concurrent use.
func (b *BootCoordinator) Probe() (BootPhase, error) {
	phase := b.classify()

	b.mu.Lock()
	b.phase = phase
	ts := b.now()
	b.mu.Unlock()

	if b.markers.BootLogPath != "" {
		dt := sanitizeLogValue(b.readMarker(b.markers.DeviceTreePath))
		line := fmt.Sprintf("%s run=%s phase=%s devicetree=%q\n", ts.Format(time.RFC3339Nano), b.runUUID, phase, dt)
		if err := b.appendf(b.markers.BootLogPath, line); err != nil {
			return phase, fmt.Errorf("write boot log: %w", err)
		}
	}
	return phase, nil
}

// sanitizeLogValue trims a marker value and collapses any embedded newlines so a
// single boot-log line stays a single line. An absent marker logs as empty.
func sanitizeLogValue(v string) string {
	v = strings.TrimSpace(v)
	v = strings.ReplaceAll(v, "\n", " ")
	v = strings.ReplaceAll(v, "\x00", "")
	return v
}

// classify reads the markers and derives the readiness phase. Missing markers
// are treated as "not present" (no error), because a not-yet-booted node simply
// lacks them.
func (b *BootCoordinator) classify() BootPhase {
	version := b.readMarker(b.markers.VersionPath)
	if strings.TrimSpace(version) == "" {
		// No kernel version marker => kernel not demonstrably up.
		return PhaseUnknown
	}

	cmdline := strings.ToLower(b.readMarker(b.markers.CmdlinePath))
	switch {
	case cmdlineHasToken(cmdline, clusterReadyToken):
		// The cmdline asserts cluster readiness. If a device-tree marker is
		// present it must corroborate membership: a board that explicitly
		// negates it (e.g. "helix,no-cluster") is held at UserspaceReady so a
		// mis-provisioned node cannot register. An absent marker does not block.
		if b.deviceTreeNegatesCluster() {
			return PhaseUserspaceReady
		}
		return PhaseClusterReady
	case cmdlineHasToken(cmdline, userspaceReadyToken):
		return PhaseUserspaceReady
	default:
		return PhaseBooting
	}
}

// cmdlineHasToken reports whether the (already lower-cased) kernel command line
// contains token as a DISCRETE, whitespace-delimited parameter — not merely as
// a byte substring of a larger token. The Linux kernel command line is a list
// of space-separated parameters, so a readiness sentinel like
// "helix.cluster=ready" only counts when it is a whole field. Matching it as a
// free substring is a fail-open readiness gate: a mis-provisioned or hostile
// cmdline value such as "helix.cluster=readyx" or "init=/x/helix.cluster=ready7"
// would otherwise wave a node that never asserted the discrete flag through the
// cluster-join gate.
func cmdlineHasToken(cmdline, token string) bool {
	for _, field := range strings.Fields(cmdline) {
		if field == token {
			return true
		}
	}
	return false
}

// deviceTreeNegatesCluster reports whether a present device-tree marker
// explicitly contradicts cluster membership. An absent/empty marker returns
// false (it cannot contradict what it does not state).
func (b *BootCoordinator) deviceTreeNegatesCluster() bool {
	dt := strings.ToLower(strings.TrimSpace(b.readMarker(b.markers.DeviceTreePath)))
	if dt == "" {
		return false
	}
	return strings.Contains(dt, deviceTreeNoClusterToken)
}

// readMarker returns the marker file contents, or "" if the path is empty or the
// file is absent/unreadable. Absence is a valid, expected observation.
func (b *BootCoordinator) readMarker(path string) string {
	if path == "" {
		return ""
	}
	data, err := b.readAll(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// WaitReady blocks until the node reaches PhaseClusterReady or ctx is done. It
// re-probes every PollInterval. It returns nil once ClusterReady, or ctx.Err()
// if the context is cancelled/expires first.
//
// *BootCoordinator satisfies the ReadyGate interface consulted by
// Registrar.RegisterCtx, which calls WaitReady before joining the cluster so a
// not-yet-ClusterReady node cannot register.
func (b *BootCoordinator) WaitReady(ctx context.Context) error {
	interval := b.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}

	// Fast path: already ready.
	if phase, err := b.Probe(); err != nil {
		return err
	} else if phase == PhaseClusterReady {
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			phase, err := b.Probe()
			if err != nil {
				return err
			}
			if phase == PhaseClusterReady {
				return nil
			}
		}
	}
}

// appendLine appends a single line to the file at path, creating it if needed.
func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	return nil
}

// newUUIDv4 returns a canonical RFC-4122 version-4 UUID string. It uses
// crypto/rand; on the (astronomically unlikely) event rand fails, it falls back
// to a time-seeded value so a coordinator always has a non-empty run id.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Degenerate fallback: still unique-enough per run via the clock.
		n := uint64(time.Now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte((n >> (8 * i)) & 0xff)
			b[i+8] = byte((n>>(8*i))&0xff) ^ 0x5a
		}
	}
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
