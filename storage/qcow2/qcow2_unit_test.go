package qcow2

import (
	"errors"
	"testing"
)

// These unit tests exercise the pure chain-management / depth-limit logic and
// the typed-error contracts. They do NOT require qemu-img to be installed.

func TestChainTooDeepError_IsAndMessage(t *testing.T) {
	err := error(&ChainTooDeepError{AttemptedDepth: 11, MaxDepth: 10, Backing: "/tmp/o10.qcow2"})
	if !errors.Is(err, ErrChainTooDeep) {
		t.Fatalf("errors.Is(err, ErrChainTooDeep) = false; want true")
	}
	msg := err.Error()
	for _, want := range []string{"11", "10", "/tmp/o10.qcow2"} {
		if !contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

func TestToolAbsentError_Is(t *testing.T) {
	err := error(&ToolAbsentError{Tool: "qemu-img"})
	if !errors.Is(err, ErrQemuImgUnavailable) {
		t.Fatalf("errors.Is(err, ErrQemuImgUnavailable) = false; want true")
	}
	if !contains(err.Error(), "qemu-img") {
		t.Errorf("error %q does not mention qemu-img", err.Error())
	}
}

func TestNewManager_DefaultsAndOptions(t *testing.T) {
	// WithQemuImgPath bypasses PATH resolution so this runs without qemu-img.
	m, err := NewManager(WithQemuImgPath("/usr/bin/false"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m.MaxDepth() != MaxChainDepth {
		t.Errorf("default MaxDepth = %d; want %d", m.MaxDepth(), MaxChainDepth)
	}
	if m.QemuImgPath() != "/usr/bin/false" {
		t.Errorf("QemuImgPath = %q; want /usr/bin/false", m.QemuImgPath())
	}

	m2, err := NewManager(WithQemuImgPath("/usr/bin/false"), WithMaxDepth(3))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m2.MaxDepth() != 3 {
		t.Errorf("MaxDepth override = %d; want 3", m2.MaxDepth())
	}

	// WithMaxDepth(<1) is ignored.
	m3, _ := NewManager(WithQemuImgPath("/usr/bin/false"), WithMaxDepth(0))
	if m3.MaxDepth() != MaxChainDepth {
		t.Errorf("MaxDepth(0) should be ignored, got %d", m3.MaxDepth())
	}
}

func TestNewManager_ToolAbsent(t *testing.T) {
	// Force resolution against an empty PATH by pointing at a name that cannot
	// be found. We simulate absence by setting a bogus binary name via PATH is
	// awkward; instead verify the typed error path with an empty PATH lookup.
	t.Setenv("PATH", "")
	_, err := NewManager()
	if err == nil {
		t.Fatal("expected error when qemu-img absent")
	}
	if !errors.Is(err, ErrQemuImgUnavailable) {
		t.Fatalf("err = %v; want errors.Is ErrQemuImgUnavailable", err)
	}
	var tae *ToolAbsentError
	if !errors.As(err, &tae) {
		t.Fatalf("err = %v; want *ToolAbsentError", err)
	}
}

func TestEmptyPathValidation(t *testing.T) {
	m, err := NewManager(WithQemuImgPath("/usr/bin/false"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := t.Context()
	if err := m.CreateBase(ctx, "", "64M"); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("CreateBase empty path: %v; want ErrEmptyPath", err)
	}
	if err := m.CreateOverlay(ctx, "", "b.qcow2"); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("CreateOverlay empty overlay: %v; want ErrEmptyPath", err)
	}
	if err := m.CreateOverlay(ctx, "o.qcow2", ""); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("CreateOverlay empty backing: %v; want ErrEmptyPath", err)
	}
	if _, err := m.ChainInfo(ctx, ""); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("ChainInfo empty: %v; want ErrEmptyPath", err)
	}
	if err := m.Commit(ctx, ""); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("Commit empty: %v; want ErrEmptyPath", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
