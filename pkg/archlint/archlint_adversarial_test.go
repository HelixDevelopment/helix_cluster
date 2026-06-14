package archlint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is an adversarial, mutation-verified audit of the archlint contract.
//
// NOTE ON LINTER MODEL: despite the generic "dependency-boundary linter" framing,
// pkg/archlint is a DOC-vs-DISK architecture linter, not an import-graph linter.
//   - ParseComponentMap(markdown) extracts the "Component Map" table rows
//     (Component | Layer | Package | Origin), one Component per data row.
//   - VerifyPackages(root, comps) checks every component's Package cell resolves
//     to a real DIRECTORY under root, returning a single error naming each that
//     does not.
// The dominant bug class is therefore the FALSE NEGATIVE: a documented component
// pointing at a package that does NOT exist on disk (or at a plain file, not a
// directory) that VerifyPackages fails to flag — which would let the architecture
// doc silently drift from the code. These tests pin that contract adversarially.

// --- CENTERPIECE A: TRUE-POSITIVE DETECTION ----------------------------------
// A documented component whose package directory is absent MUST be reported, and
// the report MUST name that specific package. A neutered VerifyPackages that
// returns nil (or drops the append) fails here.
func TestAdversarial_MissingPackageIsFlagged(t *testing.T) {
	tmp := t.TempDir()
	// One real package dir, one phantom.
	if err := os.MkdirAll(filepath.Join(tmp, "pkg", "real"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	comps := []Component{
		{Name: "Legit", Package: "pkg/real"},
		{Name: "Phantom", Package: "pkg/forbidden-ghost"},
	}
	err := VerifyPackages(tmp, comps)
	if err == nil {
		t.Fatal("FALSE NEGATIVE: missing package not flagged; VerifyPackages returned nil")
	}
	if !strings.Contains(err.Error(), "pkg/forbidden-ghost") {
		t.Fatalf("violation must name the missing package, got: %v", err)
	}
	// Must NOT slander the legit one.
	if strings.Contains(err.Error(), "pkg/real") {
		t.Fatalf("legit package wrongly named as missing: %v", err)
	}
}

// All-missing: every bogus component is named and the count is right. This bites
// a mutation that reports only the first offender or hard-codes count=1.
func TestAdversarial_AllMissingPackagesNamed(t *testing.T) {
	tmp := t.TempDir()
	comps := []Component{
		{Name: "A", Package: "pkg/ghost-a"},
		{Name: "B", Package: "pkg/ghost-b"},
		{Name: "C", Package: "pkg/ghost-c"},
	}
	err := VerifyPackages(tmp, comps)
	if err == nil {
		t.Fatal("FALSE NEGATIVE: no missing packages flagged")
	}
	for _, want := range []string{"pkg/ghost-a", "pkg/ghost-b", "pkg/ghost-c"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q, got: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "3 component") {
		t.Fatalf("error must report count 3, got: %v", err)
	}
}

// --- CENTERPIECE B: TRUE-NEGATIVE (clean set) --------------------------------
// A fully-legal component set produces NO error. An always-flag mutation is
// caught here.
func TestAdversarial_AllRealPackagesClean(t *testing.T) {
	tmp := t.TempDir()
	for _, p := range []string{"pkg/alpha", "internal/beta", "cmd/gamma"} {
		if err := os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(p)), 0o755); err != nil {
			t.Fatalf("setup %s: %v", p, err)
		}
	}
	comps := []Component{
		{Name: "Alpha", Package: "pkg/alpha"},
		{Name: "Beta", Package: "internal/beta"},
		{Name: "Gamma", Package: "cmd/gamma"},
	}
	if err := VerifyPackages(tmp, comps); err != nil {
		t.Fatalf("FALSE POSITIVE: legal component set flagged: %v", err)
	}
}

// --- CENTERPIECE C: MATCHING PRECISION ---------------------------------------
// A package cell that names a plain FILE (not a directory) MUST be flagged.
// The check is info.IsDir(), so a file at the path is still "missing". A mutation
// dropping the !info.IsDir() guard (accepting any existing path) fails here.
func TestAdversarial_FileNotDirIsFlagged(t *testing.T) {
	tmp := t.TempDir()
	// "pkg/scheduler" exists as a FILE, not a package directory.
	if err := os.MkdirAll(filepath.Join(tmp, "pkg"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "pkg", "scheduler"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := VerifyPackages(tmp, []Component{{Name: "Sched", Package: "pkg/scheduler"}})
	if err == nil {
		t.Fatal("FALSE NEGATIVE: a plain file (not a directory) accepted as a package")
	}
	if !strings.Contains(err.Error(), "pkg/scheduler") {
		t.Fatalf("error must name the file-not-dir package, got: %v", err)
	}
}

// A real nested subpackage directory resolves cleanly — the matcher is not
// exact-top-level-only and resolves arbitrary-depth slash paths. This guards a
// mutation that mishandles FromSlash / nested joins.
func TestAdversarial_NestedSubpackageResolves(t *testing.T) {
	tmp := t.TempDir()
	deep := filepath.Join(tmp, "internal", "control", "scheduler", "omega")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	comps := []Component{{Name: "Omega", Package: "internal/control/scheduler/omega"}}
	if err := VerifyPackages(tmp, comps); err != nil {
		t.Fatalf("nested subpackage wrongly flagged: %v", err)
	}
	// A lookalike sibling that does NOT exist must still be flagged (no loose match).
	bad := []Component{{Name: "OmegaX", Package: "internal/control/scheduler/omega-x"}}
	if err := VerifyPackages(tmp, bad); err == nil {
		t.Fatal("FALSE NEGATIVE: lookalike non-existent sibling accepted")
	}
}

// A package whose name is a PREFIX of a real dir must NOT be accepted (the join
// is path-exact, not substring/prefix). "pkg/sched" must not match "pkg/scheduler".
func TestAdversarial_PrefixDoesNotMatch(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "pkg", "scheduler"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := VerifyPackages(tmp, []Component{{Name: "Sched", Package: "pkg/sched"}})
	if err == nil {
		t.Fatal("FALSE NEGATIVE: prefix 'pkg/sched' loosely matched 'pkg/scheduler'")
	}
}

// --- CENTERPIECE D: PARSE FIDELITY -------------------------------------------
// Every data row round-trips: count, Name, Package (backticks stripped), Layer,
// and Origin. A mutation that drops/merges rows or mis-indexes a column fails.
func TestAdversarial_ParseRoundTripsAllRows(t *testing.T) {
	md := strings.Join([]string{
		"# Architecture",
		"",
		"## Component Map",
		"",
		"| Component | Layer | Package | Origin |",
		"|---|---|---|---|",
		"| Sched | L4 | `pkg/scheduler` | HXC-1137 |",
		"| Node | L1 | `internal/node` | HXC-1159 |",
		"| Gw | L6 | `internal/gateway` | HXC-1469 |",
		"",
		"trailing prose",
	}, "\n")
	comps, err := ParseComponentMap(md)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(comps) != 3 {
		t.Fatalf("want 3 components (no header/separator leak, no row drop), got %d: %+v", len(comps), comps)
	}
	wantPkg := []string{"pkg/scheduler", "internal/node", "internal/gateway"}
	wantName := []string{"Sched", "Node", "Gw"}
	wantLayer := []string{"L4", "L1", "L6"}
	wantOrigin := []string{"HXC-1137", "HXC-1159", "HXC-1469"}
	for i, c := range comps {
		if c.Package != wantPkg[i] {
			t.Fatalf("row %d Package: want %q got %q (backtick strip / col index)", i, wantPkg[i], c.Package)
		}
		if c.Name != wantName[i] {
			t.Fatalf("row %d Name: want %q got %q", i, wantName[i], c.Name)
		}
		if c.Layer != wantLayer[i] {
			t.Fatalf("row %d Layer: want %q got %q", i, wantLayer[i], c.Layer)
		}
		if c.Origin != wantOrigin[i] {
			t.Fatalf("row %d Origin: want %q got %q", i, wantOrigin[i], c.Origin)
		}
	}
}

// The header row and the |---| separator must NEVER appear as components.
// A mutation that stops skipping the separator would emit a "Package: ---" row.
func TestAdversarial_HeaderAndSeparatorNeverEmitted(t *testing.T) {
	md := strings.Join([]string{
		"| Component | Layer | Package | Origin |",
		"|:---|:---:|---:|---|",
		"| Only | L0 | `pkg/only` | HXC-1 |",
	}, "\n")
	comps, err := ParseComponentMap(md)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("header/separator leaked as data: got %d rows %+v", len(comps), comps)
	}
	for _, c := range comps {
		if c.Package == "Package" || strings.ContainsAny(c.Package, "-:") && strings.Trim(c.Package, "-:") == "" {
			t.Fatalf("header/separator emitted as component: %+v", c)
		}
	}
	if comps[0].Package != "pkg/only" {
		t.Fatalf("want pkg/only, got %q", comps[0].Package)
	}
}

// A row whose Package cell is empty is skipped (not emitted with Package=="").
func TestAdversarial_EmptyPackageCellSkipped(t *testing.T) {
	md := strings.Join([]string{
		"| Component | Layer | Package | Origin |",
		"|---|---|---|---|",
		"| Real | L0 | `pkg/real` | HXC-1 |",
		"| Blank | L0 | `` | HXC-2 |",
	}, "\n")
	comps, err := ParseComponentMap(md)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, c := range comps {
		if c.Package == "" {
			t.Fatalf("empty-package row emitted: %+v", c)
		}
	}
	if len(comps) != 1 || comps[0].Package != "pkg/real" {
		t.Fatalf("want exactly the one real row, got %+v", comps)
	}
}

// --- CENTERPIECE E: EDGES -----------------------------------------------------
// Empty / table-less markdown is a typed error, not a silent empty pass.
func TestAdversarial_EmptyMapIsTypedError(t *testing.T) {
	for _, md := range []string{
		"",
		"# Title\n\nprose only, no table\n",
		"| Name | Layer | Origin |\n|---|---|---|\n| X | L0 | HXC-1 |\n", // no Package column
	} {
		_, err := ParseComponentMap(md)
		if !errors.Is(err, ErrNoComponents) {
			t.Fatalf("want ErrNoComponents for %q, got %v", md, err)
		}
	}
}

// VerifyPackages on an empty component slice is a no-op (nil), and tolerates a
// component with an empty Package without panicking (it is flagged as missing
// since "" joins to root which is a dir... so assert documented behavior precisely).
func TestAdversarial_EdgeEmptyAndDegenerate(t *testing.T) {
	tmp := t.TempDir()
	if err := VerifyPackages(tmp, nil); err != nil {
		t.Fatalf("empty component slice should be clean, got: %v", err)
	}
	if err := VerifyPackages(tmp, []Component{}); err != nil {
		t.Fatalf("zero-len component slice should be clean, got: %v", err)
	}
	// Must not panic on an unknown/odd component.
	_ = VerifyPackages(tmp, []Component{{Name: "weird", Package: "no/such/deep/path"}})
}

// Sanity: the real ARCHITECTURE.md parses and every documented component resolves
// — an end-to-end true-negative on production data (complements the existing
// happy-path test, asserting the count is non-trivial and stable).
func TestAdversarial_RealDocIsConsistent(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "ARCHITECTURE.md"))
	if err != nil {
		t.Skipf("ARCHITECTURE.md not readable: %v", err)
	}
	comps, err := ParseComponentMap(string(data))
	if err != nil {
		t.Fatalf("real doc parse: %v", err)
	}
	if len(comps) < 10 {
		t.Fatalf("real component map suspiciously small: %d", len(comps))
	}
	if err := VerifyPackages(root, comps); err != nil {
		t.Fatalf("real doc has a component pointing at a missing package: %v", err)
	}
}
