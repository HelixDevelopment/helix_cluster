package covgate

import (
	"strings"
	"testing"
)

// smallCatalog mimics a run_mutations.sh CASES array snippet:
// pipe-separated: id | file | find | replace | pkg | test-regex | race
// It includes one comment, one blank line, and two real entries.
const smallCatalog = `# Helix mutation catalog fixture
# id | file | find | replace | pkg | test-regex | race

metrics-atomic|pkg/metrics/package.go|atomic.AddInt64(&c.val, 1)|c.val += 1|./pkg/metrics|TestCounter_ConcurrentInc_Mutation|1
errors-rlock|pkg/errors/package.go|e.mu.RLock()|// e.mu.RLock()|./pkg/errors|TestConcurrentFieldAccess|1
`

func TestParseMutationCatalog_Valid(t *testing.T) {
	cases, err := ParseMutationCatalog(strings.NewReader(smallCatalog))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("expected 2 cases, got %d: %v", len(cases), cases)
	}

	// First case
	c0 := cases[0]
	if c0.ID != "metrics-atomic" {
		t.Errorf("cases[0].ID = %q, want %q", c0.ID, "metrics-atomic")
	}
	if c0.File != "pkg/metrics/package.go" {
		t.Errorf("cases[0].File = %q, want %q", c0.File, "pkg/metrics/package.go")
	}
	if c0.Pkg != "./pkg/metrics" {
		t.Errorf("cases[0].Pkg = %q, want %q", c0.Pkg, "./pkg/metrics")
	}
	if c0.TestRegex != "TestCounter_ConcurrentInc_Mutation" {
		t.Errorf("cases[0].TestRegex = %q, want %q", c0.TestRegex, "TestCounter_ConcurrentInc_Mutation")
	}

	// Second case
	c1 := cases[1]
	if c1.ID != "errors-rlock" {
		t.Errorf("cases[1].ID = %q, want %q", c1.ID, "errors-rlock")
	}
	if c1.Pkg != "./pkg/errors" {
		t.Errorf("cases[1].Pkg = %q, want %q", c1.Pkg, "./pkg/errors")
	}
	if c1.TestRegex != "TestConcurrentFieldAccess" {
		t.Errorf("cases[1].TestRegex = %q, want %q", c1.TestRegex, "TestConcurrentFieldAccess")
	}
}

func TestParseMutationCatalog_EmptyInput(t *testing.T) {
	cases, err := ParseMutationCatalog(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if len(cases) != 0 {
		t.Errorf("expected 0 cases for empty input, got %d", len(cases))
	}
}

func TestParseMutationCatalog_CommentsOnly(t *testing.T) {
	input := "# just a comment\n# another comment\n\n"
	cases, err := ParseMutationCatalog(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cases) != 0 {
		t.Errorf("expected 0 cases for comments-only input, got %d", len(cases))
	}
}

func TestParseMutationCatalog_MalformedLine_Error(t *testing.T) {
	// A non-comment line with only 3 fields (not 7) must return an error.
	input := "id|file|pkg\n"
	_, err := ParseMutationCatalog(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for malformed catalog line, got nil")
	}
}

// TestMissingMutationPairs is the main audit test.
//
// testedPkgs = {a, b, c}; catalog covers {a, c} => missing = [b]
//
// This test would pass if MissingMutationPairs returned an empty slice — that
// would be a PASS-bluff caught by the non-empty assertion below.
func TestMissingMutationPairs_ReturnsMissing(t *testing.T) {
	catalog := []MutationCase{
		{ID: "a-case", File: "pkg/a/a.go", Pkg: "pkg/a", TestRegex: "TestA"},
		{ID: "c-case", File: "pkg/c/c.go", Pkg: "pkg/c", TestRegex: "TestC"},
	}
	testedPkgs := []string{"pkg/a", "pkg/b", "pkg/c"}

	missing := MissingMutationPairs(testedPkgs, catalog)

	if len(missing) != 1 || missing[0] != "pkg/b" {
		t.Errorf("MissingMutationPairs = %v, want [pkg/b]", missing)
	}
}

// TestMissingMutationPairs_NoneWhenAllCovered verifies the happy path.
func TestMissingMutationPairs_NoneWhenAllCovered(t *testing.T) {
	catalog := []MutationCase{
		{ID: "a-case", Pkg: "pkg/a"},
		{ID: "b-case", Pkg: "pkg/b"},
	}
	testedPkgs := []string{"pkg/a", "pkg/b"}

	missing := MissingMutationPairs(testedPkgs, catalog)
	if len(missing) != 0 {
		t.Errorf("MissingMutationPairs = %v, want empty", missing)
	}
}

// TestMissingMutationPairs_DotSlashNormalisation verifies that "./pkg/foo" and
// "pkg/foo" compare equal (catalog uses ./-prefixed paths like run_mutations.sh).
func TestMissingMutationPairs_DotSlashNormalisation(t *testing.T) {
	catalog := []MutationCase{
		{ID: "metrics", Pkg: "./pkg/metrics"},
	}
	// testedPkgs uses the form without "./".
	testedPkgs := []string{"pkg/metrics"}

	missing := MissingMutationPairs(testedPkgs, catalog)
	if len(missing) != 0 {
		t.Errorf("normalization failure: MissingMutationPairs = %v, want empty (./pkg/metrics == pkg/metrics)", missing)
	}
}

// TestMissingMutationPairs_EmptyTestedPkgs verifies that no packages => no missing.
func TestMissingMutationPairs_EmptyTestedPkgs(t *testing.T) {
	catalog := []MutationCase{{ID: "x", Pkg: "pkg/x"}}
	missing := MissingMutationPairs(nil, catalog)
	if len(missing) != 0 {
		t.Errorf("empty testedPkgs: MissingMutationPairs = %v, want empty", missing)
	}
}

// TestMissingMutationPairs_EmptyCatalog verifies that all tested pkgs are
// reported as missing when the catalog is empty.
func TestMissingMutationPairs_EmptyCatalog(t *testing.T) {
	testedPkgs := []string{"pkg/a", "pkg/b"}
	missing := MissingMutationPairs(testedPkgs, nil)
	if len(missing) != 2 {
		t.Errorf("empty catalog: MissingMutationPairs = %v, want [pkg/a pkg/b]", missing)
	}
}

// TestMissingMutationPairs_Sorted verifies the returned slice is sorted.
func TestMissingMutationPairs_Sorted(t *testing.T) {
	// Only pkg/a is covered; pkg/b and pkg/c are missing.
	catalog := []MutationCase{{ID: "a", Pkg: "pkg/a"}}
	// Feed in reverse order to prove sorting is applied.
	testedPkgs := []string{"pkg/c", "pkg/b", "pkg/a"}

	missing := MissingMutationPairs(testedPkgs, catalog)
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing, got %d: %v", len(missing), missing)
	}
	if missing[0] != "pkg/b" || missing[1] != "pkg/c" {
		t.Errorf("sorted order wrong: got %v, want [pkg/b pkg/c]", missing)
	}
}
