// Package qcow2 implements a real copy-on-write overlay manager for qcow2
// disk images by wrapping the host's qemu-img(1) binary.
//
// A qcow2 overlay stores only the blocks that differ from its backing file,
// forming a backing chain: overlay -> ... -> base. This package can create a
// base image, stack overlays on top of it, inspect/validate the chain, commit
// a top overlay back into its backing file, and — critically — enforce a
// maximum chain depth so an unbounded stack of overlays cannot be built.
//
// All format-level work is done by real qemu-img against real qcow2 files;
// nothing about the image format is mocked. The only pure-Go logic is the
// bookkeeping that tracks depth and enforces the limit, which is unit-testable
// without qemu-img present.
package qcow2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// MaxChainDepth is the maximum number of overlays permitted on top of a base
// image. The base image itself is depth 0; the first overlay is depth 1; the
// tenth overlay is depth 10. An attempt to create an 11th overlay (depth 11)
// is rejected with ErrChainTooDeep.
const MaxChainDepth = 10

// Sentinel errors returned by the manager. Callers can match them with
// errors.Is.
var (
	// ErrChainTooDeep is returned by CreateOverlay when creating the overlay
	// would push the backing chain past MaxChainDepth.
	ErrChainTooDeep = errors.New("qcow2: backing chain would exceed maximum depth")

	// ErrQemuImgUnavailable is returned when the qemu-img binary cannot be
	// located on PATH. It allows callers (and tests) to honestly distinguish a
	// missing tool from a real failure.
	ErrQemuImgUnavailable = errors.New("qcow2: qemu-img binary not found on PATH")

	// ErrEmptyPath is returned when an image path argument is empty.
	ErrEmptyPath = errors.New("qcow2: image path must not be empty")
)

// ChainTooDeepError is the concrete, typed error wrapping ErrChainTooDeep with
// the offending depth values, so callers get actionable detail while still
// being able to match errors.Is(err, ErrChainTooDeep).
type ChainTooDeepError struct {
	// AttemptedDepth is the depth the new overlay would have had.
	AttemptedDepth int
	// MaxDepth is the configured maximum (MaxChainDepth).
	MaxDepth int
	// Backing is the backing image whose chain was already at the limit.
	Backing string
}

func (e *ChainTooDeepError) Error() string {
	return fmt.Sprintf(
		"qcow2: creating overlay on %q would make chain depth %d, exceeding maximum of %d",
		e.Backing, e.AttemptedDepth, e.MaxDepth,
	)
}

// Is allows errors.Is(err, ErrChainTooDeep) to succeed.
func (e *ChainTooDeepError) Is(target error) bool { return target == ErrChainTooDeep }

// ToolAbsentError is returned by operations that need qemu-img when the binary
// is absent. It implements Is(ErrQemuImgUnavailable) and carries the tool name
// so callers can produce an honest SKIP-with-reason rather than a fake pass.
type ToolAbsentError struct {
	Tool string
}

func (e *ToolAbsentError) Error() string {
	return fmt.Sprintf("qcow2: required tool %q not found on PATH", e.Tool)
}

func (e *ToolAbsentError) Is(target error) bool { return target == ErrQemuImgUnavailable }

// Manager creates and manages qcow2 copy-on-write overlays by shelling out to
// qemu-img. The zero value is not usable; construct one with NewManager.
type Manager struct {
	// qemuImg is the resolved path to the qemu-img binary.
	qemuImg string
	// maxDepth is the enforced maximum overlay depth (defaults to MaxChainDepth).
	maxDepth int
}

// Option configures a Manager.
type Option func(*Manager)

// WithMaxDepth overrides the default maximum chain depth. Primarily for tests;
// values < 1 are ignored.
func WithMaxDepth(d int) Option {
	return func(m *Manager) {
		if d >= 1 {
			m.maxDepth = d
		}
	}
}

// WithQemuImgPath forces a specific qemu-img binary path instead of resolving
// it from PATH. Primarily for tests.
func WithQemuImgPath(path string) Option {
	return func(m *Manager) { m.qemuImg = path }
}

// NewManager constructs a Manager, resolving qemu-img on PATH. If qemu-img is
// not installed it returns a *ToolAbsentError (matchable via
// errors.Is(err, ErrQemuImgUnavailable)) so callers can SKIP honestly. When
// WithQemuImgPath is supplied, PATH resolution is skipped.
func NewManager(opts ...Option) (*Manager, error) {
	m := &Manager{maxDepth: MaxChainDepth}
	for _, o := range opts {
		o(m)
	}
	if m.qemuImg == "" {
		p, err := exec.LookPath("qemu-img")
		if err != nil {
			return nil, &ToolAbsentError{Tool: "qemu-img"}
		}
		m.qemuImg = p
	}
	return m, nil
}

// QemuImgPath returns the resolved qemu-img binary path.
func (m *Manager) QemuImgPath() string { return m.qemuImg }

// MaxDepth returns the configured maximum overlay depth.
func (m *Manager) MaxDepth() int { return m.maxDepth }

// QemuImgAvailable reports whether qemu-img is present on PATH. It is a free
// function so tests can decide between real integration and an honest skip
// before constructing a Manager.
func QemuImgAvailable() bool {
	_, err := exec.LookPath("qemu-img")
	return err == nil
}

// CreateBase creates a new base qcow2 image of the given virtual size (e.g.
// "64M", "1G") at path. The base has no backing file and is depth 0.
func (m *Manager) CreateBase(ctx context.Context, path, size string) error {
	if path == "" {
		return ErrEmptyPath
	}
	if size == "" {
		return errors.New("qcow2: base image size must not be empty")
	}
	if err := m.run(ctx, "create", "-f", "qcow2", path, size); err != nil {
		return fmt.Errorf("qcow2: create base %q: %w", path, err)
	}
	return nil
}

// CreateOverlay creates a new qcow2 overlay at overlayPath whose backing file
// is backingPath, using:
//
//	qemu-img create -f qcow2 -b <backing> -F qcow2 <overlay>
//
// Before creating, it measures the existing depth of backingPath's chain and
// rejects the operation with a *ChainTooDeepError if the resulting overlay
// would exceed the configured maximum depth. The base is depth 0, so an
// overlay placed directly on the base is depth 1; the limit permits depths up
// to and including m.maxDepth.
func (m *Manager) CreateOverlay(ctx context.Context, overlayPath, backingPath string) error {
	if overlayPath == "" || backingPath == "" {
		return ErrEmptyPath
	}

	// Depth of the backing image itself (its own chain length above the base).
	backingDepth, err := m.ChainDepth(ctx, backingPath)
	if err != nil {
		return fmt.Errorf("qcow2: measure backing chain depth of %q: %w", backingPath, err)
	}
	newDepth := backingDepth + 1
	if newDepth > m.maxDepth {
		return &ChainTooDeepError{
			AttemptedDepth: newDepth,
			MaxDepth:       m.maxDepth,
			Backing:        backingPath,
		}
	}

	// -F declares the backing format explicitly (required by modern qemu-img
	// and avoids a format-probe security warning).
	if err := m.run(ctx, "create", "-f", "qcow2", "-b", backingPath, "-F", "qcow2", overlayPath); err != nil {
		return fmt.Errorf("qcow2: create overlay %q on %q: %w", overlayPath, backingPath, err)
	}
	return nil
}

// BackingChainEntry describes one image in a backing chain as reported by
// qemu-img info --backing-chain --output=json.
type BackingChainEntry struct {
	Filename            string `json:"filename"`
	Format              string `json:"format"`
	VirtualSize         int64  `json:"virtual-size"`
	ActualSize          int64  `json:"actual-size"`
	BackingFilename     string `json:"backing-filename"`
	FullBackingFilename string `json:"full-backing-filename"`
}

// ChainInfo returns the full backing chain for the image at path, ordered from
// the image itself (index 0) down to the base (last index). It invokes:
//
//	qemu-img info --backing-chain --output=json <path>
//
// The slice length minus one is the chain depth (see ChainDepth).
func (m *Manager) ChainInfo(ctx context.Context, path string) ([]BackingChainEntry, error) {
	if path == "" {
		return nil, ErrEmptyPath
	}
	out, err := m.output(ctx, "info", "--backing-chain", "--output=json", path)
	if err != nil {
		return nil, fmt.Errorf("qcow2: info backing-chain %q: %w", path, err)
	}

	// qemu-img emits a JSON array when --backing-chain is used. Be tolerant of
	// a single-object form for a chainless image just in case.
	trimmed := strings.TrimSpace(string(out))
	if strings.HasPrefix(trimmed, "[") {
		var entries []BackingChainEntry
		if err := json.Unmarshal(out, &entries); err != nil {
			return nil, fmt.Errorf("qcow2: parse backing-chain JSON for %q: %w", path, err)
		}
		return entries, nil
	}
	var single BackingChainEntry
	if err := json.Unmarshal(out, &single); err != nil {
		return nil, fmt.Errorf("qcow2: parse info JSON for %q: %w", path, err)
	}
	return []BackingChainEntry{single}, nil
}

// ChainDepth returns the number of backing files below the image at path —
// i.e. the overlay depth. A base image returns 0; an overlay directly on the
// base returns 1; and so on. It is derived from ChainInfo (length - 1).
func (m *Manager) ChainDepth(ctx context.Context, path string) (int, error) {
	entries, err := m.ChainInfo(ctx, path)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("qcow2: empty backing chain for %q", path)
	}
	return len(entries) - 1, nil
}

// Commit collapses the overlay at overlayPath into its immediate backing file,
// writing the overlay's changes down one level, via:
//
//	qemu-img commit <overlay>
//
// After a successful commit the overlay no longer holds the committed deltas
// (its contents now live in the backing file). Callers that want the chain to
// physically shorten should subsequently discard the now-empty overlay and use
// the backing image directly; ChainInfo on the backing image will show the
// shortened chain. This method validates that overlayPath actually has a
// backing file before attempting the commit.
func (m *Manager) Commit(ctx context.Context, overlayPath string) error {
	if overlayPath == "" {
		return ErrEmptyPath
	}
	depth, err := m.ChainDepth(ctx, overlayPath)
	if err != nil {
		return fmt.Errorf("qcow2: commit %q: %w", overlayPath, err)
	}
	if depth == 0 {
		return fmt.Errorf("qcow2: commit %q: image has no backing file to commit into", overlayPath)
	}
	if err := m.run(ctx, "commit", overlayPath); err != nil {
		return fmt.Errorf("qcow2: commit %q: %w", overlayPath, err)
	}
	return nil
}

// run executes qemu-img with the given args, discarding stdout but capturing
// stderr for error reporting.
func (m *Manager) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, m.qemuImg, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// output executes qemu-img with the given args and returns stdout.
func (m *Manager) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, m.qemuImg, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return []byte(stdout.String()), nil
}
