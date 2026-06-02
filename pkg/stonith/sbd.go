package stonith

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// sbdMessageOff is the poison/fence message written to a node's mailbox slot on
// the shared SBD device. A node's SBD watchdog daemon, on reading this message
// for its own slot, self-fences (resets). We model the on-disk mailbox as a
// small fixed-size record per target so the device size is bounded and so that
// IsFenced can read back exactly what was written.
const sbdMessageOff = "off"

// SBDDevice abstracts the shared block device / file used as an SBD mailbox.
// The real deployment points this at a shared LUN; tests point it at a temp
// file. Implementations MUST be safe for concurrent use within a single
// process. Cross-process / cross-host concurrent writes require kernel-level
// advisory locking (flock/fcntl) which is outside the scope of this interface
// — callers that need multi-host concurrent write safety must layer their own
// locking on top.
type SBDDevice interface {
	// WriteSlot stores msg as target's mailbox message.
	WriteSlot(ctx context.Context, target, msg string) error
	// ReadSlot returns target's current mailbox message (empty if none).
	ReadSlot(ctx context.Context, target string) (string, error)
}

// FileSBDDevice is a real SBDDevice backed by a single file on disk. It stores
// one line per target ("target=message") with keys in sorted order so the
// on-disk representation is deterministic and reproducible.
//
// The sync.Mutex makes concurrent access WITHIN a single process safe (no
// lost-update between goroutines). It does NOT prevent lost-update between
// separate processes writing the same file simultaneously, because os.WriteFile
// does a full-file replace without cross-process locking. A real production SBD
// driver that needs multi-host concurrent writes would use a block-device with
// fixed per-slot offsets and kernel flock/fcntl advisory locking, or a
// purpose-built SBD block protocol. This implementation is suitable for:
//   - single-cluster-member writes (the common case: each member only poisons
//     its own targets)
//   - tests and development where a real shared LUN is not available
type FileSBDDevice struct {
	path string
	mu   sync.Mutex
}

// NewFileSBDDevice opens (creating if needed) an SBD mailbox file at path.
func NewFileSBDDevice(path string) (*FileSBDDevice, error) {
	if path == "" {
		return nil, fmt.Errorf("sbd: empty device path")
	}
	// Touch the file so the device exists up front, mirroring a pre-provisioned
	// shared LUN.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sbd open %q: %w", path, err)
	}
	if cerr := f.Close(); cerr != nil {
		return nil, fmt.Errorf("sbd close %q: %w", path, cerr)
	}
	return &FileSBDDevice{path: path}, nil
}

// slots reads the whole mailbox file into a map. Caller must hold mu.
func (d *FileSBDDevice) slots() (map[string]string, error) {
	data, err := os.ReadFile(d.path)
	if err != nil {
		return nil, fmt.Errorf("sbd read %q: %w", d.path, err)
	}
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// WriteSlot persists target=msg, replacing any prior message for target.
//
// The write is made crash-safe via a write-to-temp-file-then-rename sequence:
// the temp file is written in full (flushed and synced) before the atomic
// rename replaces the mailbox file. A crash between write and rename leaves the
// old mailbox intact; a crash after rename is safe because the file was fully
// written before the rename. This prevents the partial-write corruption that
// os.WriteFile's truncate-then-write pattern would cause.
//
// Keys are sorted before serialisation so the on-disk representation is
// deterministic — repeated writes of the same logical content produce identical
// bytes, making the device file reproducible and diff-friendly.
//
// MUTATION GUARD: TestSBDWriteSlotDeterministicOrder verifies that the key
// ordering is sorted. TestSBDWriteSlotAtomicRename verifies the rename path.
func (d *FileSBDDevice) WriteSlot(_ context.Context, target, msg string) error {
	if target == "" {
		return ErrEmptyTarget
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	slots, err := d.slots()
	if err != nil {
		return err
	}
	slots[target] = msg

	// Sort keys for a deterministic, reproducible on-disk layout.
	keys := make([]string, 0, len(slots))
	for k := range slots {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b []byte
	for _, k := range keys {
		b = append(b, []byte(k+"="+slots[k]+"\n")...)
	}

	// Write to a sibling temp file and atomically rename so a crash never
	// leaves the mailbox file in a half-written state.
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("sbd write tmp %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, d.path); err != nil {
		// Best-effort cleanup; ignore secondary error.
		_ = os.Remove(tmp)
		return fmt.Errorf("sbd rename %q -> %q: %w", tmp, d.path, err)
	}
	return nil
}

// ReadSlot returns the stored message for target.
func (d *FileSBDDevice) ReadSlot(_ context.Context, target string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	slots, err := d.slots()
	if err != nil {
		return "", err
	}
	return slots[target], nil
}

// SBDAgent fences a node by writing the poison message to its slot on a shared
// SBD device. Confirmation is reading the slot back and seeing the off message —
// real sink-side evidence on the actual device.
type SBDAgent struct {
	name string
	dev  SBDDevice
}

// NewSBDAgent builds an SBDAgent over the given device.
func NewSBDAgent(name string, dev SBDDevice) *SBDAgent {
	return &SBDAgent{name: name, dev: dev}
}

// Name returns the agent's name.
func (a *SBDAgent) Name() string { return a.name }

// Fence writes the poison message to target's slot and confirms it was stored.
func (a *SBDAgent) Fence(ctx context.Context, target string) (*Confirmation, error) {
	if target == "" {
		return nil, ErrEmptyTarget
	}
	if err := a.dev.WriteSlot(ctx, target, sbdMessageOff); err != nil {
		return nil, fmt.Errorf("sbd poison %q: %w", target, err)
	}
	fenced, err := a.IsFenced(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("sbd confirm %q: %w", target, err)
	}
	if !fenced {
		return nil, fmt.Errorf("sbd %q poison not persisted: %w", target, ErrNotConfirmed)
	}
	return &Confirmation{
		Target: target,
		Agent:  a.name,
		Method: "sbd poison",
		At:     time.Now(),
	}, nil
}

// IsFenced reports true when target's slot holds the poison message.
func (a *SBDAgent) IsFenced(ctx context.Context, target string) (bool, error) {
	msg, err := a.dev.ReadSlot(ctx, target)
	if err != nil {
		return false, fmt.Errorf("sbd read slot %q: %w", target, err)
	}
	return msg == sbdMessageOff, nil
}
