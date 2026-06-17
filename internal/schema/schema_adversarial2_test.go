package schema

// Adversarial PATH-TRAVERSAL probes (wave 2) for internal/schema.
//
// CONTEXT: a systematic scan flagged internal/schema as a path-traversal
// candidate because it calls filepath.Join with a root/dir-like base and a
// variable component, with no filepath.Rel / HasPrefix containment check. The
// same class was found+fixed tonight in five sibling packages (pkg/storage
// FileStore, internal/build/cache DiskCache, testing/snapshot, testing/evidence,
// internal/gateway).
//
// VERDICT for internal/schema: FALSE POSITIVE / DOCUMENTED CONTRACT.
//
// Every filepath.Join site in schema.go joins ONLY against:
//   - filepath.Dir(callerFile) from runtime.Caller(0)  -> a compile-time
//     constant (this source file's location), never caller input; and
//   - string LITERALS ("..", "migrations", "postgresql",
//     "0001_primary_schema.sql"); and
//   - dir-entry base names returned by os.ReadDir (ChainUpFiles), which are
//     filesystem-enumerated leaf names, NOT a caller/request-supplied string.
//
// There is NO exported function in this package that accepts a caller-controlled
// schema id / filename / version / key and feeds it into filepath.Join. So a
// "../../escape" value cannot be injected through the Join surface at all.
// (RunSQLFile takes a filePath, but it is handed straight to `psql -f` and is
// never composed via filepath.Join here; it is out of the Join-traversal class.)
//
// These tests PIN that safe contract sink-side so a future refactor that
// introduces a caller-controlled component is caught.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// thisFileDir returns the directory of THIS test source file, which is the same
// directory schema.go resolves via runtime.Caller(0). Used as the trusted root
// oracle for containment assertions.
func thisFileDir(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Dir(self)
}

// repoRootOracle computes the intended repo root the same way schema.go does:
// two levels up from internal/schema.
func repoRootOracle(t *testing.T) string {
	t.Helper()
	return filepath.Clean(filepath.Join(thisFileDir(t), "..", ".."))
}

// TestSchemaPath_ResolvesInsideRepo_NoTraversal pins that SchemaPath() always
// resolves under the repo root / migrations dir and never escapes it. SchemaPath
// takes NO arguments, so there is no attacker-controlled component; this is the
// load-bearing proof that the join is constant-only.
func TestSchemaPath_ResolvesInsideRepo_NoTraversal(t *testing.T) {
	p, err := SchemaPath()
	if err != nil {
		t.Fatalf("SchemaPath: %v", err)
	}
	root := repoRootOracle(t)
	wantPrefix := filepath.Join(root, "migrations", "postgresql") + string(filepath.Separator)
	if !strings.HasPrefix(p, wantPrefix) {
		t.Fatalf("SchemaPath escaped intended dir:\n  got    %q\n  prefix %q", p, wantPrefix)
	}
	if filepath.Base(p) != "0001_primary_schema.sql" {
		t.Fatalf("SchemaPath base = %q, want 0001_primary_schema.sql", filepath.Base(p))
	}
	// Canonical-path invariant: filepath.Rel from the dir must not climb out.
	rel, err := filepath.Rel(filepath.Join(root, "migrations", "postgresql"), p)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		t.Fatalf("SchemaPath is not contained: rel=%q", rel)
	}
}

// TestMigrationsDir_Contained pins MigrationsDir() (also zero-arg, constant-only
// join) to the intended migrations/postgresql directory under the repo root.
func TestMigrationsDir_Contained(t *testing.T) {
	dir, err := MigrationsDir()
	if err != nil {
		t.Fatalf("MigrationsDir: %v", err)
	}
	want := filepath.Join(repoRootOracle(t), "migrations", "postgresql")
	if dir != want {
		t.Fatalf("MigrationsDir = %q, want %q", dir, want)
	}
}

// TestChainUpFiles_AllContained pins the third Join site: filepath.Join(dir,
// name) in ChainUpFiles, where `name = e.Name()` from os.ReadDir. Every produced
// path MUST stay inside MigrationsDir — i.e. the variable component (a ReadDir
// base name) can never be "../something". This is the closest analogue to the
// flagged class and the proof that the variable is filesystem-enumerated, not
// caller-injected.
func TestChainUpFiles_AllContained(t *testing.T) {
	files, err := ChainUpFiles()
	if err != nil {
		t.Fatalf("ChainUpFiles: %v", err)
	}
	dir, err := MigrationsDir()
	if err != nil {
		t.Fatalf("MigrationsDir: %v", err)
	}
	for _, f := range files {
		// Sink-side containment: each file resolves directly under dir with a
		// single path segment and no "..".
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			t.Fatalf("Rel(%q,%q): %v", dir, f, err)
		}
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			t.Fatalf("ChainUpFiles produced an escaping path: rel=%q file=%q", rel, f)
		}
		if strings.Contains(rel, string(filepath.Separator)) {
			t.Fatalf("ChainUpFiles produced a nested/escaping path (expected leaf only): rel=%q", rel)
		}
		if filepath.Dir(f) != dir {
			t.Fatalf("ChainUpFiles file parent = %q, want %q (file=%q)", filepath.Dir(f), dir, f)
		}
	}
}

// TestChainUpFiles_ReadDirNamesAreLeafOnly is the LOAD-BEARING mutation proof for
// the false-positive classification. It demonstrates the security-relevant
// property the production code RELIES ON: os.ReadDir over the real migrations
// directory only ever yields leaf base names (no "/" and no ".." component). If
// this invariant could be violated — i.e. if a caller could inject a "../escape"
// component — ChainUpFiles' unguarded filepath.Join(dir, name) WOULD traverse.
// Because the names come exclusively from ReadDir (not from any argument), the
// invariant holds and the Join is safe.
//
// The synthetic check below proves the property is what makes the package safe:
// we assert that NO real entry name is path-bearing, and that IF one
// hypothetically were "../escape", filepath.Join would escape (documenting that
// the safety is owed entirely to the ReadDir source, not to any guard in code).
func TestChainUpFiles_ReadDirNamesAreLeafOnly(t *testing.T) {
	dir, err := MigrationsDir()
	if err != nil {
		t.Fatalf("MigrationsDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "." || name == ".." {
			t.Fatalf("ReadDir yielded a traversal entry %q (unexpected)", name)
		}
		if strings.ContainsRune(name, filepath.Separator) {
			t.Fatalf("ReadDir yielded a path-bearing entry %q (unexpected)", name)
		}
	}

	// Document the counterfactual: an attacker-controlled "../escape" name WOULD
	// escape an unguarded Join. This proves the safety is provided by the trusted
	// ReadDir source above, not by any containment check in ChainUpFiles — so if a
	// future change feeds a caller-supplied name into that Join, it becomes a real
	// traversal bug and these pins must be upgraded to a guard.
	evil := filepath.Join(dir, "..", "escape.sql")
	rel, err := filepath.Rel(dir, evil)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if !strings.HasPrefix(rel, "..") {
		t.Fatalf("counterfactual sanity failed: %q did not escape dir (rel=%q)", evil, rel)
	}
}
