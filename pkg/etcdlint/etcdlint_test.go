// Package etcdlint_test provides deterministic unit tests for the etcdlint
// static-analysis gate. Every test writes real Go source fixture files into a
// t.TempDir() and invokes CheckDir / CheckFiles against them — no hand-built
// ASTs, no mocks. Each test includes an "Anti-bluff mutation" comment naming
// the exact one-line source change that would flip its assertion.
package etcdlint_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/etcdlint"
)

// writeFile creates a file at dir/name with the given content.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("writeFile(%q): %v", path, err)
	}
	return path
}

// ---------------------------------------------------------------------------
// 1. Violating package: non-allowlisted package imports etcd → 1 Violation.
//    Anti-bluff mutation: change `strings.HasPrefix(importPath, etcdImportPrefix)`
//    to `importPath == etcdImportPrefix` in etcdlint.go → zero violations returned
//    because the full path "go.etcd.io/etcd/client/v3" ≠ the bare prefix.
// ---------------------------------------------------------------------------

func TestCheckFiles_ViolationWhenPackageNotAllowlisted(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "federation.go", `package federation

import (
	"context"
	etcdv3 "go.etcd.io/etcd/client/v3"
)

var _ = etcdv3.New
var _ = context.Background
`)

	violations, err := etcdlint.CheckFiles([]string{path}, []string{"etcd"})
	if err != nil {
		t.Fatalf("CheckFiles returned unexpected error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("expected exactly 1 violation, got %d: %v", len(violations), violations)
	}
	v := violations[0]
	if v.File != path {
		t.Errorf("Violation.File = %q, want %q", v.File, path)
	}
	if v.Package != "federation" {
		t.Errorf("Violation.Package = %q, want %q", v.Package, "federation")
	}
	if v.Import != "go.etcd.io/etcd/client/v3" {
		t.Errorf("Violation.Import = %q, want %q", v.Import, "go.etcd.io/etcd/client/v3")
	}
}

// ---------------------------------------------------------------------------
// 2. Allowlisted package: same etcd import → 0 violations.
//    Anti-bluff mutation: remove the `allowed` map lookup in CheckFiles
//    (flag every etcd import unconditionally) → 1 violation returned for the
//    "etcd" package, breaking the len==0 assertion.
// ---------------------------------------------------------------------------

func TestCheckFiles_NoViolationWhenPackageAllowlisted(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "etcd.go", `package etcd

import (
	etcdv3 "go.etcd.io/etcd/client/v3"
)

var _ = etcdv3.New
`)

	violations, err := etcdlint.CheckFiles([]string{path}, []string{"etcd"})
	if err != nil {
		t.Fatalf("CheckFiles returned unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected 0 violations for allowlisted package, got %d: %v", len(violations), violations)
	}
}

// ---------------------------------------------------------------------------
// 3. Non-etcd import in a non-allowlisted package → 0 violations.
//    Anti-bluff mutation: change `strings.HasPrefix(importPath, etcdImportPrefix)`
//    to `importPath != ""` → "context" would be flagged, breaking len==0.
// ---------------------------------------------------------------------------

func TestCheckFiles_NoViolationForNonEtcdImport(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "fed.go", `package federation

import "context"

var _ = context.Background
`)

	violations, err := etcdlint.CheckFiles([]string{path}, []string{"etcd"})
	if err != nil {
		t.Fatalf("CheckFiles returned unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected 0 violations, got %d: %v", len(violations), violations)
	}
}

// ---------------------------------------------------------------------------
// 4. Multi-file dir: one violator among several clean files →
//    exactly 1 Violation pointing to the correct file.
//    Anti-bluff mutation: add an early `return violations, nil` after the
//    first file is processed in CheckFiles → violations for later files are
//    silently dropped; if the violator is not the first file in the sorted
//    list the test detects 0 violations and fails.
// ---------------------------------------------------------------------------

func TestCheckDir_OnlyViolatorReported_MultiFileDir(t *testing.T) {
	dir := t.TempDir()

	// "a_clean.go" comes lexically first — parser visits it before "z_bad.go".
	writeFile(t, dir, "a_clean.go", `package scheduler

import "context"

var _ = context.Background
`)
	// "b_clean.go" also clean.
	writeFile(t, dir, "b_clean.go", `package scheduler

import "fmt"

var _ = fmt.Println
`)
	// "z_bad.go" is the last file lexically — proves no early return.
	badPath := writeFile(t, dir, "z_bad.go", `package router

import (
	"context"
	etcdv3 "go.etcd.io/etcd/client/v3"
)

var _ = context.Background
var _ = etcdv3.New
`)

	violations, err := etcdlint.CheckDir(dir, []string{"etcd", "scheduler"})
	if err != nil {
		t.Fatalf("CheckDir returned unexpected error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("expected exactly 1 violation, got %d: %v", len(violations), violations)
	}
	v := violations[0]
	if v.File != badPath {
		t.Errorf("Violation.File = %q, want %q", v.File, badPath)
	}
	if v.Package != "router" {
		t.Errorf("Violation.Package = %q, want %q", v.Package, "router")
	}
	if v.Import != "go.etcd.io/etcd/client/v3" {
		t.Errorf("Violation.Import = %q, want %q", v.Import, "go.etcd.io/etcd/client/v3")
	}
}

// ---------------------------------------------------------------------------
// 5. Malformed source file → CheckFiles returns non-nil error wrapping ErrParse.
//    Anti-bluff mutation: swallow the parser error (return nil, nil instead of
//    returning the wrapped error) → errors.Is(err, ErrParse) is false and
//    the t.Fatal branch does not trigger, but the final err==nil check fires.
// ---------------------------------------------------------------------------

func TestCheckFiles_MalformedFile_ReturnsErrParse(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "broken.go", `package broken

THIS IS NOT VALID GO SYNTAX {{{
`)

	violations, err := etcdlint.CheckFiles([]string{path}, []string{"etcd"})
	if err == nil {
		t.Fatalf("expected non-nil error for malformed file, got nil (violations=%v)", violations)
	}
	if !errors.Is(err, etcdlint.ErrParse) {
		t.Errorf("error does not wrap ErrParse: got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 6. _test.go files are excluded by CheckDir.
//    Anti-bluff mutation: remove the `strings.HasSuffix(name, "_test.go")` guard
//    in CheckDir → the test file is parsed; since its package is "violation"
//    (not in allow) and it imports etcd, a spurious violation appears and
//    the len==0 assertion fails.
// ---------------------------------------------------------------------------

func TestCheckDir_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()

	// A _test.go file that would be a violation if parsed.
	writeFile(t, dir, "node_test.go", `package violation

import etcdv3 "go.etcd.io/etcd/client/v3"

var _ = etcdv3.New
`)

	violations, err := etcdlint.CheckDir(dir, []string{"etcd"})
	if err != nil {
		t.Fatalf("CheckDir returned unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected 0 violations (test file skipped), got %d: %v", len(violations), violations)
	}
}

// ---------------------------------------------------------------------------
// 7. vendor/ directory is skipped entirely by CheckDir.
//    Anti-bluff mutation: remove the `filepath.SkipDir` branch for "vendor"
//    in CheckDir → the vendored file is parsed and its etcd import produces a
//    violation, breaking the len==0 assertion.
// ---------------------------------------------------------------------------

func TestCheckDir_SkipsVendorDir(t *testing.T) {
	dir := t.TempDir()

	// Create vendor/somelib/etcd.go that would be a violation if parsed.
	vendorDir := filepath.Join(dir, "vendor", "somelib")
	if err := os.MkdirAll(vendorDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, vendorDir, "etcd.go", `package somelib

import etcdv3 "go.etcd.io/etcd/client/v3"

var _ = etcdv3.New
`)

	violations, err := etcdlint.CheckDir(dir, []string{"etcd"})
	if err != nil {
		t.Fatalf("CheckDir returned unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected 0 violations (vendor dir skipped), got %d: %v", len(violations), violations)
	}
}

// ---------------------------------------------------------------------------
// 8. Multiple violations in the same file (two etcd imports) → both reported,
//    sorted by Import path.
//    Anti-bluff mutation: break after the first matching import spec inside the
//    import-scan loop → only 1 violation returned instead of 2.
// ---------------------------------------------------------------------------

func TestCheckFiles_MultipleEtcdImports_BothReported(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "multi.go", `package gateway

import (
	etcdv3  "go.etcd.io/etcd/client/v3"
	etcdmvcc "go.etcd.io/etcd/api/v3/mvccpb"
)

var _ = etcdv3.New
var _ = etcdmvcc.Event_PUT
`)

	violations, err := etcdlint.CheckFiles([]string{path}, []string{"etcd"})
	if err != nil {
		t.Fatalf("CheckFiles returned unexpected error: %v", err)
	}
	if len(violations) != 2 {
		t.Fatalf("expected exactly 2 violations, got %d: %v", len(violations), violations)
	}

	// Sorted by Import: "go.etcd.io/etcd/api/v3/mvccpb" < "go.etcd.io/etcd/client/v3"
	if violations[0].Import != "go.etcd.io/etcd/api/v3/mvccpb" {
		t.Errorf("violations[0].Import = %q, want %q", violations[0].Import, "go.etcd.io/etcd/api/v3/mvccpb")
	}
	if violations[1].Import != "go.etcd.io/etcd/client/v3" {
		t.Errorf("violations[1].Import = %q, want %q", violations[1].Import, "go.etcd.io/etcd/client/v3")
	}
	// Both must point to the same file.
	for i, v := range violations {
		if v.File != path {
			t.Errorf("violations[%d].File = %q, want %q", i, v.File, path)
		}
		if v.Package != "gateway" {
			t.Errorf("violations[%d].Package = %q, want %q", i, v.Package, "gateway")
		}
	}
}

// ---------------------------------------------------------------------------
// 9. Empty allowlist: every etcd import is a violation.
//    Anti-bluff mutation: initialise allowed with a catch-all entry so every
//    package is allowed → 0 violations instead of 1.
// ---------------------------------------------------------------------------

func TestCheckFiles_EmptyAllowlist_AllEtcdImportsViolate(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "node.go", `package node

import etcdv3 "go.etcd.io/etcd/client/v3"

var _ = etcdv3.New
`)

	violations, err := etcdlint.CheckFiles([]string{path}, []string{})
	if err != nil {
		t.Fatalf("CheckFiles returned unexpected error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation with empty allowlist, got %d: %v", len(violations), violations)
	}
	if violations[0].Package != "node" {
		t.Errorf("Violation.Package = %q, want %q", violations[0].Package, "node")
	}
}

// ---------------------------------------------------------------------------
// 10. Nil / empty file list: returns zero violations without error.
//     Anti-bluff mutation: treat nil slice as a special error case → breaks
//     the err==nil assertion.
// ---------------------------------------------------------------------------

func TestCheckFiles_EmptyFileList_NoViolations(t *testing.T) {
	violations, err := etcdlint.CheckFiles(nil, []string{"etcd"})
	if err != nil {
		t.Fatalf("CheckFiles(nil) returned unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected 0 violations for empty file list, got %d", len(violations))
	}
}

// ---------------------------------------------------------------------------
// 11. Determinism: output of CheckFiles on multiple violating files is stable
//     regardless of input order (sorted by File, then Import).
//     Anti-bluff mutation: remove the sort.Slice call → the two calls receive
//     inputs in opposite order and return violations in that insertion order;
//     got1[0].File == pathB while got2[0].File == pathA, so the cross-call loop
//     fires and the test fails.
// ---------------------------------------------------------------------------

func TestCheckFiles_OutputIsDeterministic(t *testing.T) {
	dir := t.TempDir()

	// "aaa.go" < "bbb.go" lexically.
	pathA := writeFile(t, dir, "aaa.go", `package alpha

import etcdv3 "go.etcd.io/etcd/client/v3"

var _ = etcdv3.New
`)
	pathB := writeFile(t, dir, "bbb.go", `package beta

import etcdv3 "go.etcd.io/etcd/client/v3"

var _ = etcdv3.New
`)

	// Call 1: inputs in reverse lexical order (B then A).
	got1, err := etcdlint.CheckFiles([]string{pathB, pathA}, []string{})
	if err != nil {
		t.Fatalf("first CheckFiles call: %v", err)
	}
	// Call 2: inputs in forward lexical order (A then B) — opposite from call 1.
	got2, err := etcdlint.CheckFiles([]string{pathA, pathB}, []string{})
	if err != nil {
		t.Fatalf("second CheckFiles call: %v", err)
	}
	if len(got1) != 2 || len(got2) != 2 {
		t.Fatalf("expected 2 violations each call, got %d and %d", len(got1), len(got2))
	}
	// The cross-call loop is load-bearing: without sort.Slice the two calls
	// would return violations in their respective input orders (B,A vs A,B)
	// and got1[0].File != got2[0].File would trigger the error below.
	for i := range got1 {
		if got1[i].File != got2[i].File || got1[i].Import != got2[i].Import {
			t.Errorf("run 1 violation[%d] = %+v; run 2 violation[%d] = %+v — not deterministic", i, got1[i], i, got2[i])
		}
	}
	// Specifically: pathA (alpha) must precede pathB (beta) in both results.
	if got1[0].File != pathA {
		t.Errorf("got1[0].File = %q, want %q (sorted output)", got1[0].File, pathA)
	}
	if got1[1].File != pathB {
		t.Errorf("got1[1].File = %q, want %q (sorted output)", got1[1].File, pathB)
	}
}

// ---------------------------------------------------------------------------
// 12. IsEtcdImport: exported predicate correctly classifies import paths.
//     Anti-bluff mutation: change `strings.HasPrefix(importPath, etcdImportPrefix)`
//     to `importPath == etcdImportPrefix` in IsEtcdImport → the sub-path
//     "go.etcd.io/etcd/client/v3" no longer matches (it is not equal to the
//     bare prefix), so wantTrue[1] returns false and the test fails.
// ---------------------------------------------------------------------------

func TestIsEtcdImport(t *testing.T) {
	cases := []struct {
		path      string
		wantMatch bool
	}{
		// True: exact prefix with sub-path.
		{"go.etcd.io/etcd/client/v3", true},
		// True: another sub-package.
		{"go.etcd.io/etcd/api/v3/mvccpb", true},
		// False: different vendor — shares no prefix with "go.etcd.io/etcd".
		{"go.etcd.io/bbolt", false},
		// False: stdlib.
		{"context", false},
		// False: empty string.
		{"", false},
	}

	for _, tc := range cases {
		got := etcdlint.IsEtcdImport(tc.path)
		if got != tc.wantMatch {
			t.Errorf("IsEtcdImport(%q) = %v, want %v", tc.path, got, tc.wantMatch)
		}
	}
}
