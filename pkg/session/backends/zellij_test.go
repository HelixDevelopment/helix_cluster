package backends

import (
	"errors"
	"os/exec"
	"testing"
)

// Interface compliance — compile-time assertion.
var _ Backend = (*ZellijBackend)(nil)

// TestZellijBackend_UnavailableWhenAbsent verifies that NewZellijBackend
// returns the typed ErrBackendUnavailable sentinel when the zellij binary
// is not present on the host.  On this host zellij is absent; the test
// proves that no fake/stub backend is created and the caller receives a
// meaningful typed error.
func TestZellijBackend_UnavailableWhenAbsent(t *testing.T) {
	if _, err := exec.LookPath("zellij"); err == nil {
		t.Skip("zellij is present on this host; absence test does not apply")
	}

	_, err := NewZellijBackend()
	if err == nil {
		t.Fatal("NewZellijBackend() returned nil error, expected ErrBackendUnavailable")
	}
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("expected errors.Is(err, ErrBackendUnavailable); got %v", err)
	}
}

// TestZellijBackend_MethodsReturnUnavailableWhenAbsent verifies that a
// ZellijBackend with bin="" (simulating an unavailable host) returns
// ErrBackendUnavailable from every method rather than panicking or
// silently no-oping.  This ensures no PASS-bluff where a broken method
// succeeds vacuously.
func TestZellijBackend_MethodsReturnUnavailableWhenAbsent(t *testing.T) {
	// Construct an unavailable backend directly (bin="" means absent binary).
	z := &ZellijBackend{bin: ""}

	t.Run("Create", func(t *testing.T) {
		_, err := z.Create("test", "")
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("Create: expected ErrBackendUnavailable, got %v", err)
		}
	})

	t.Run("Attach", func(t *testing.T) {
		_, err := z.Attach("test")
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("Attach: expected ErrBackendUnavailable, got %v", err)
		}
	})

	t.Run("Detach", func(t *testing.T) {
		err := z.Detach("test")
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("Detach: expected ErrBackendUnavailable, got %v", err)
		}
	})

	t.Run("List", func(t *testing.T) {
		_, err := z.List()
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("List: expected ErrBackendUnavailable, got %v", err)
		}
	})

	t.Run("Kill", func(t *testing.T) {
		err := z.Kill("test")
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("Kill: expected ErrBackendUnavailable, got %v", err)
		}
	})

	t.Run("Resize", func(t *testing.T) {
		err := z.Resize("test", 24, 80)
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("Resize: expected ErrBackendUnavailable, got %v", err)
		}
	})

	t.Run("SendInput", func(t *testing.T) {
		err := z.SendInput("test", "x")
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("SendInput: expected ErrBackendUnavailable, got %v", err)
		}
	})

	t.Run("CaptureOutput", func(t *testing.T) {
		_, err := z.CaptureOutput("test")
		if !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("CaptureOutput: expected ErrBackendUnavailable, got %v", err)
		}
	})
}

// TestParseZellijList_Unit unit-tests the zellij list parser.
func TestParseZellijList_Unit(t *testing.T) {
	input := `session-one [Created 2026-06-02T00:00:00Z]
session-two [Created 2026-06-02T01:00:00Z]
`
	sessions := parseZellijList(input)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %+v", len(sessions), sessions)
	}
	names := map[string]bool{}
	for _, s := range sessions {
		names[s.Name] = true
	}
	if !names["session-one"] {
		t.Errorf("expected 'session-one' in parsed list; got %+v", sessions)
	}
	if !names["session-two"] {
		t.Errorf("expected 'session-two' in parsed list; got %+v", sessions)
	}
}

// TestParseZellijList_Empty verifies an empty list is returned for blank output.
func TestParseZellijList_Empty(t *testing.T) {
	sessions := parseZellijList("")
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions from empty output, got %d: %+v", len(sessions), sessions)
	}
}
