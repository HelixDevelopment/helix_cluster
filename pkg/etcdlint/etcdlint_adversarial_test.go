// Package etcdlint_test — adversarial companion suite.
//
// This file complements etcdlint_test.go by attacking the parts of the linter
// contract most likely to hide a FALSE NEGATIVE (a banned etcd import slipping
// through unflagged) or a FALSE POSITIVE (a legal occurrence wrongly flagged).
// Every fixture is a real .go source written into t.TempDir(); no hand-built
// ASTs, no mocks. Each test names the exact production mutation that flips it.
//
// Contract under test (etcdlint.go):
//   - banned predicate: strings.HasPrefix(path, "go.etcd.io/etcd")  [PREFIX rule]
//   - a violation == file whose `package <name>` is NOT in allow AND that has a
//     real import (an entry in *ast.File.Imports) matching the predicate.
//   - comment / string-literal occurrences of the path are NOT imports → never
//     a violation.
package etcdlint_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/etcdlint"
)

// adv writes dir/name with content and returns the full path.
func adv(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("write %q: %v", p, err)
	}
	return p
}

// ===========================================================================
// CENTERPIECE A — BANNED IMPORT DETECTION across every real path form.
// A false negative here is the dominant, gate-defeating bug class.
// Anti-bluff mutation: make IsEtcdImport always return false (or flip HasPrefix
// to a never-true comparison) → every sub-case below reports 0 violations and
// the per-case len==1 assertions fail.
// ===========================================================================

func TestAdv_BannedImport_AllRealForms_Detected(t *testing.T) {
	cases := []struct {
		name    string // fixture file name
		src     string // file body
		pkg     string // expected Violation.Package
		wantImp string // expected Violation.Import
	}{
		{
			name: "plain_single.go",
			src: `package federation

import "go.etcd.io/etcd/client/v3"

var _ = clientv3.Config{}
`,
			pkg:     "federation",
			wantImp: "go.etcd.io/etcd/client/v3",
		},
		{
			name: "aliased.go",
			src: `package federation

import etcd "go.etcd.io/etcd/client/v3"

var _ = etcd.New
`,
			pkg:     "federation",
			wantImp: "go.etcd.io/etcd/client/v3",
		},
		{
			name: "blank_import.go",
			src: `package federation

import _ "go.etcd.io/etcd/client/v3"
`,
			pkg:     "federation",
			wantImp: "go.etcd.io/etcd/client/v3",
		},
		{
			name: "dot_import.go",
			src: `package federation

import . "go.etcd.io/etcd/api/v3/etcdserverpb"
`,
			pkg:     "federation",
			wantImp: "go.etcd.io/etcd/api/v3/etcdserverpb",
		},
		{
			name: "bare_module_root.go", // exactly the prefix, no sub-path
			src: `package federation

import etcd "go.etcd.io/etcd"

var _ = etcd.X
`,
			pkg:     "federation",
			wantImp: "go.etcd.io/etcd",
		},
		{
			name: "grouped_block.go", // banned import buried in a group
			src: `package federation

import (
	"context"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
)

var _ = context.Background
var _ = fmt.Println
var _ = clientv3.New
`,
			pkg:     "federation",
			wantImp: "go.etcd.io/etcd/client/v3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := adv(t, dir, tc.name, tc.src)

			vs, err := etcdlint.CheckFiles([]string{path}, []string{"etcd"})
			if err != nil {
				t.Fatalf("CheckFiles error: %v", err)
			}
			if len(vs) != 1 {
				t.Fatalf("FALSE NEGATIVE: expected exactly 1 violation for %s, got %d: %v",
					tc.name, len(vs), vs)
			}
			if vs[0].Import != tc.wantImp {
				t.Errorf("Violation.Import = %q, want %q", vs[0].Import, tc.wantImp)
			}
			if vs[0].Package != tc.pkg {
				t.Errorf("Violation.Package = %q, want %q", vs[0].Package, tc.pkg)
			}
			if vs[0].File != path {
				t.Errorf("Violation.File = %q, want %q", vs[0].File, path)
			}
		})
	}
}

// ===========================================================================
// CENTERPIECE B — PRECISION: comment and string-literal occurrences of the
// banned path are NOT imports and MUST NOT be flagged. Only entries in
// *ast.File.Imports count.
// Anti-bluff mutation: replace the AST import-walk with a textual scan
// (e.g. strings.Contains(fileBytes, etcdImportPrefix)) → the comment + string
// occurrences match and 1+ spurious violations appear, breaking len==0.
// ===========================================================================

func TestAdv_CommentAndStringOccurrences_NotFlagged(t *testing.T) {
	dir := t.TempDir()
	// The path "go.etcd.io/etcd/client/v3" appears in a line comment, a block
	// comment, a const string, and a struct-tag-like string — but is NEVER
	// actually imported. The only real import is the stdlib "context".
	path := adv(t, dir, "doc.go", `package federation

// Federation must NOT import go.etcd.io/etcd/client/v3 — see the architecture
// rule. This comment mentions the path on purpose.

/*
Block comment also naming go.etcd.io/etcd/api/v3/mvccpb to try to fool a
naive textual scanner.
*/

import "context"

// bannedPath documents the forbidden prefix as data, not as an import.
const bannedPath = "go.etcd.io/etcd/client/v3"

var note = "do not import go.etcd.io/etcd here"

var _ = context.Background
var _ = bannedPath
var _ = note
`)

	vs, err := etcdlint.CheckFiles([]string{path}, []string{"federation"})
	if err != nil {
		t.Fatalf("CheckFiles error: %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("FALSE POSITIVE: comment/string occurrence wrongly flagged; got %d: %v", len(vs), vs)
	}

	// Same source, this time with the package NOT on the allowlist, to prove the
	// zero result comes from "not an import" and not from the allowlist exemption.
	vs2, err := etcdlint.CheckFiles([]string{path}, []string{})
	if err != nil {
		t.Fatalf("CheckFiles (empty allow) error: %v", err)
	}
	if len(vs2) != 0 {
		t.Fatalf("FALSE POSITIVE: comment/string flagged even off-allowlist; got %d: %v", len(vs2), vs2)
	}
}

// ===========================================================================
// CENTERPIECE C — ALLOWED PASS: a non-allowlisted package importing only legal
// packages produces no violation (kills an "always flag" mutation), and the
// designated cell-local "etcd" wrapper package IS exempt even while importing
// etcd directly.
// Anti-bluff mutation: drop the `allowed` map lookup → the etcd-wrapper file is
// flagged and len(wrapperViolations)==0 fails.
// ===========================================================================

func TestAdv_AllowedPass_LegalImportsAndWrapperExempt(t *testing.T) {
	dir := t.TempDir()

	clean := adv(t, dir, "clean.go", `package federation

import (
	"context"
	"go.etcd.io/bbolt"
)

var _ = context.Background
var _ = bbolt.Open
`)
	wrapper := adv(t, dir, "wrapper.go", `package etcd

import clientv3 "go.etcd.io/etcd/client/v3"

var _ = clientv3.New
`)

	vs, err := etcdlint.CheckFiles([]string{clean, wrapper}, []string{"etcd"})
	if err != nil {
		t.Fatalf("CheckFiles error: %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("expected 0 violations (legal imports + exempt wrapper), got %d: %v", len(vs), vs)
	}
}

// ===========================================================================
// PRECISION D — go.etcd.io/bbolt (a sibling module that is NOT etcd) is never a
// violation, pinning that the predicate does not over-match the go.etcd.io org.
// Anti-bluff mutation: shorten etcdImportPrefix to "go.etcd.io" → bbolt matches
// and a spurious violation appears.
// ===========================================================================

func TestAdv_SiblingModule_Bbolt_NotFlagged(t *testing.T) {
	dir := t.TempDir()
	path := adv(t, dir, "store.go", `package federation

import "go.etcd.io/bbolt"

var _ = bbolt.Open
`)

	vs, err := etcdlint.CheckFiles([]string{path}, []string{})
	if err != nil {
		t.Fatalf("CheckFiles error: %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("FALSE POSITIVE: go.etcd.io/bbolt wrongly flagged; got %d: %v", len(vs), vs)
	}
}

// ===========================================================================
// PRECISION E — CONTRACT PIN for the PREFIX rule's boundary.
// The documented rule is HasPrefix(path, "go.etcd.io/etcd"), so a path that
// continues the prefix WITHOUT a "/" separator (e.g. "go.etcd.io/etcdfoo")
// DOES match by contract. This test pins that documented behavior so that any
// future tightening to a path-segment rule is a deliberate, visible change.
//
// NOTE: this is a documented-contract pin, not a bug claim — no such module
// "go.etcd.io/etcdfoo" exists in practice; every REAL etcd path is followed by
// "/". See the audit report for the precision discussion.
// Anti-bluff mutation: tightening the rule to require a trailing "/" or exact
// segment match flips this to 0 and the test fails — forcing an intentional
// contract decision.
// ===========================================================================

func TestAdv_PrefixRule_BoundaryIsDocumentedPrefix(t *testing.T) {
	dir := t.TempDir()
	path := adv(t, dir, "lookalike.go", `package federation

import etcdfoo "go.etcd.io/etcdfoo"

var _ = etcdfoo.X
`)

	vs, err := etcdlint.CheckFiles([]string{path}, []string{})
	if err != nil {
		t.Fatalf("CheckFiles error: %v", err)
	}
	// Per the documented PREFIX contract this matches. Pin it explicitly.
	if len(vs) != 1 {
		t.Fatalf("documented prefix rule: expected 1 violation for go.etcd.io/etcdfoo, got %d: %v", len(vs), vs)
	}
	if vs[0].Import != "go.etcd.io/etcdfoo" {
		t.Errorf("Violation.Import = %q, want %q", vs[0].Import, "go.etcd.io/etcdfoo")
	}
	// Direct predicate cross-check.
	if !etcdlint.IsEtcdImport("go.etcd.io/etcdfoo") {
		t.Errorf("IsEtcdImport(go.etcd.io/etcdfoo) = false, want true under prefix contract")
	}
}

// ===========================================================================
// EDGE F — CheckDir on a nonexistent path returns a non-nil error and does not
// panic.
// Anti-bluff mutation: swallow the WalkDir error (return nil) → err==nil and
// this test's t.Fatal fires.
// ===========================================================================

func TestAdv_CheckDir_NonexistentPath_ErrorsNoPanic(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	vs, err := etcdlint.CheckDir(missing, []string{"etcd"})
	if err == nil {
		t.Fatalf("expected error for nonexistent dir, got nil (violations=%v)", vs)
	}
}

// ===========================================================================
// EDGE G — CheckDir ignores non-.go files even when their content contains the
// banned path, and reports the one real .go violator.
// Anti-bluff mutation: remove the `!strings.HasSuffix(name, ".go")` skip → the
// .txt fixture is parsed, fails to parse as Go, and CheckDir returns ErrParse
// instead of the single expected violation (len/err assertions both break).
// ===========================================================================

func TestAdv_CheckDir_IgnoresNonGoFiles(t *testing.T) {
	dir := t.TempDir()

	// A non-Go file that textually contains a banned import line.
	adv(t, dir, "notes.txt", `import "go.etcd.io/etcd/client/v3" // not Go, must be ignored`)
	// A README-like markdown also mentioning it.
	adv(t, dir, "README.md", "Do not import go.etcd.io/etcd/client/v3.\n")
	// The single real Go violator.
	bad := adv(t, dir, "router.go", `package router

import clientv3 "go.etcd.io/etcd/client/v3"

var _ = clientv3.New
`)

	vs, err := etcdlint.CheckDir(dir, []string{})
	if err != nil {
		t.Fatalf("CheckDir error: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("expected exactly 1 violation (only the .go file), got %d: %v", len(vs), vs)
	}
	if vs[0].File != bad {
		t.Errorf("Violation.File = %q, want %q", vs[0].File, bad)
	}
}

// ===========================================================================
// EDGE H — a .go file with no imports at all is clean (no violation, no error).
// Anti-bluff mutation: any change that flags files lacking a banned import
// (e.g. always-append) breaks len==0.
// ===========================================================================

func TestAdv_NoImports_Clean(t *testing.T) {
	dir := t.TempDir()
	path := adv(t, dir, "empty_imports.go", `package federation

var X = 1
`)

	vs, err := etcdlint.CheckFiles([]string{path}, []string{})
	if err != nil {
		t.Fatalf("CheckFiles error: %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("expected 0 violations for import-less file, got %d: %v", len(vs), vs)
	}
}

// ===========================================================================
// EDGE I — an empty (zero-byte) .go file has no package clause → parse error
// surfaced as ErrParse, never swallowed.
// Anti-bluff mutation: swallow the parser error (return nil,nil) → err==nil and
// errors.Is(err, ErrParse) is false; the t.Fatal below fires.
// ===========================================================================

func TestAdv_EmptyFile_ReturnsErrParse(t *testing.T) {
	dir := t.TempDir()
	path := adv(t, dir, "blank.go", ``)

	vs, err := etcdlint.CheckFiles([]string{path}, []string{"etcd"})
	if err == nil {
		t.Fatalf("expected ErrParse for empty file, got nil (violations=%v)", vs)
	}
	if !errors.Is(err, etcdlint.ErrParse) {
		t.Errorf("error does not wrap ErrParse: got %v", err)
	}
}
