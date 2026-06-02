package archlint

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the repository root, computed from this test file's location
// (pkg/archlint/archlint_test.go -> up two directories).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// TestArchitectureMap_AllComponentsMapToRealPackages is the HXC-1424 sink-side
// gate: the real docs/ARCHITECTURE.md must parse into components and every one
// must resolve to a real package directory.
func TestArchitectureMap_AllComponentsMapToRealPackages(t *testing.T) {
	const runUUID = "archlint-1424-9f1c2a"
	root := repoRoot(t)
	docPath := filepath.Join(root, "docs", "ARCHITECTURE.md")

	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("[%s] read %s: %v", runUUID, docPath, err)
	}
	comps, err := ParseComponentMap(string(data))
	if err != nil {
		t.Fatalf("[%s] parse component map: %v", runUUID, err)
	}
	// before: map unverified -> after: every component bound to a real package.
	t.Logf("[%s] before: %d documented components unverified", runUUID, len(comps))
	if len(comps) < 10 {
		t.Fatalf("[%s] expected a substantial component map, got %d rows", runUUID, len(comps))
	}
	if err := VerifyPackages(root, comps); err != nil {
		t.Fatalf("[%s] %v", runUUID, err)
	}
	t.Logf("[%s] after: all %d components resolve to real packages", runUUID, len(comps))
}

// TestArchLint_DetectsMissingPackage is the load-bearing guard: VerifyPackages
// MUST reject a component pointing at a non-existent package. If the check were
// neutered to always return nil, this test fails.
func TestArchLint_DetectsMissingPackage(t *testing.T) {
	const runUUID = "archlint-1424-miss"
	root := repoRoot(t)
	bogus := []Component{
		{Name: "Real one", Layer: "L0", Package: "pkg/archlint"},
		{Name: "Phantom", Layer: "L9", Package: "pkg/does-not-exist-xyz"},
	}
	err := VerifyPackages(root, bogus)
	if err == nil {
		t.Fatalf("[%s] expected error for missing package, got nil", runUUID)
	}
	if !strings.Contains(err.Error(), "pkg/does-not-exist-xyz") {
		t.Fatalf("[%s] error must name the missing package, got: %v", runUUID, err)
	}
	t.Logf("[%s] missing package correctly rejected: %v", runUUID, err)
}

// TestParseComponentMap_RejectsEmpty ensures a map with no rows is an error
// rather than a silent pass.
func TestParseComponentMap_RejectsEmpty(t *testing.T) {
	const runUUID = "archlint-1424-empty"
	md := "# Title\n\nSome prose with no table.\n"
	if _, err := ParseComponentMap(md); !errors.Is(err, ErrNoComponents) {
		t.Fatalf("[%s] expected ErrNoComponents, got %v", runUUID, err)
	}
	t.Logf("[%s] empty map correctly rejected", runUUID)
}

// TestParseComponentMap_SkipsHeaderAndSeparator verifies the parser does not
// emit the header or the |---| separator as components, and strips backticks.
func TestParseComponentMap_SkipsHeaderAndSeparator(t *testing.T) {
	const runUUID = "archlint-1424-parse"
	md := strings.Join([]string{
		"## Component Map",
		"",
		"| Component | Layer | Package | Origin |",
		"|---|---|---|---|",
		"| Sched | L4 | `pkg/scheduler` | HXC-1137 |",
		"| Node | L1 | `internal/node` | HXC-1159 |",
		"",
	}, "\n")
	comps, err := ParseComponentMap(md)
	if err != nil {
		t.Fatalf("[%s] parse: %v", runUUID, err)
	}
	if len(comps) != 2 {
		t.Fatalf("[%s] want 2 components, got %d: %+v", runUUID, len(comps), comps)
	}
	if comps[0].Package != "pkg/scheduler" || comps[1].Package != "internal/node" {
		t.Fatalf("[%s] backtick strip / column extraction wrong: %+v", runUUID, comps)
	}
	if comps[0].Name != "Sched" || comps[0].Origin != "HXC-1137" {
		t.Fatalf("[%s] name/origin extraction wrong: %+v", runUUID, comps[0])
	}
}
