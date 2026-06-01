package covgate

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// MutationCase represents a single entry in a mutation catalog file
// (the pipe-separated format used by test/mutation/run_mutations.sh).
type MutationCase struct {
	// ID is the unique identifier for the mutation case (first field).
	ID string
	// File is the source file to be mutated (second field).
	File string
	// Pkg is the Go package path under test (fifth field, e.g. "./pkg/metrics").
	Pkg string
	// TestRegex is the -run regex for the paired test (sixth field).
	TestRegex string
}

// ParseMutationCatalog parses the pipe-separated mutation case lines from r.
//
// Expected line format (7 pipe-separated fields):
//
//	id | file | literal-find | literal-replace | pkg | test-regex | race(0|1)
//
// Lines that are blank or whose first non-whitespace character is '#' are
// silently skipped. Returns an error if a non-comment, non-blank line does not
// have exactly 7 pipe-separated fields.
func ParseMutationCatalog(r io.Reader) ([]MutationCase, error) {
	scanner := bufio.NewScanner(r)
	var cases []MutationCase
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		parts := strings.Split(trimmed, "|")
		if len(parts) != 7 {
			return nil, fmt.Errorf("covgate: mutation catalog line %d: expected 7 pipe-separated fields, got %d: %q",
				lineNum, len(parts), trimmed)
		}

		cases = append(cases, MutationCase{
			ID:        strings.TrimSpace(parts[0]),
			File:      strings.TrimSpace(parts[1]),
			Pkg:       strings.TrimSpace(parts[4]),
			TestRegex: strings.TrimSpace(parts[5]),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("covgate: reading mutation catalog: %w", err)
	}
	return cases, nil
}

// MissingMutationPairs returns the packages in testedPkgs that have NO
// matching entry in catalog (where "matching" means MutationCase.Pkg equals
// the tested package path after normalising both to strip a leading "./").
//
// The returned slice is sorted in ascending order.
func MissingMutationPairs(testedPkgs []string, catalog []MutationCase) []string {
	// Build a set of packages that have at least one catalog entry.
	covered := make(map[string]struct{}, len(catalog))
	for _, mc := range catalog {
		covered[normPkg(mc.Pkg)] = struct{}{}
	}

	var missing []string
	for _, pkg := range testedPkgs {
		if _, ok := covered[normPkg(pkg)]; !ok {
			missing = append(missing, pkg)
		}
	}
	sortStrings(missing)
	return missing
}

// normPkg strips a leading "./" so that "./pkg/metrics" and "pkg/metrics"
// compare equal.
func normPkg(pkg string) string {
	return strings.TrimPrefix(pkg, "./")
}
