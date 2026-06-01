// Package covgate provides tools for parsing Go coverage profiles and auditing
// mutation-pairing coverage (Constitution §1.1 / HXC-1138).
package covgate

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// CoverageReport is the result of parsing a Go coverprofile.
type CoverageReport struct {
	Mode              string
	Packages          map[string]float64
	Aggregate         float64
	TotalStatements   int
	CoveredStatements int
}

// ParseCoverProfile parses the text output of `go test -coverprofile`.
//
// Format:
//
//	mode: set|count|atomic
//	path/to/file.go:startLine.startCol,endLine.endCol numStmts count
//
// The Aggregate percentage is 100 * coveredStmts / totalStmts, where a block
// is "covered" if its count > 0 and it is weighted by numStmts.
// An empty profile (mode line only) returns Aggregate 100.0 and 0 statements.
func ParseCoverProfile(r io.Reader) (*CoverageReport, error) {
	scanner := bufio.NewScanner(r)

	// First line must be "mode: <mode>"
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("covgate: reading mode line: %w", err)
		}
		return nil, fmt.Errorf("covgate: empty input, missing mode line")
	}
	modeLine := scanner.Text()
	if !strings.HasPrefix(modeLine, "mode: ") {
		return nil, fmt.Errorf("covgate: invalid mode line: %q", modeLine)
	}
	mode := strings.TrimPrefix(modeLine, "mode: ")
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return nil, fmt.Errorf("covgate: empty mode in mode line")
	}

	// pkgTotal[pkg] and pkgCovered[pkg] accumulate statement counts per package.
	pkgTotal := make(map[string]int)
	pkgCovered := make(map[string]int)

	totalStmts := 0
	coveredStmts := 0

	lineNum := 1
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Format: "path/to/file.go:sl.sc,el.ec numStmts count"
		// Split on space: at most 3 fields.
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("covgate: line %d: expected 3 fields, got %d: %q", lineNum, len(fields), line)
		}

		blockSpec := fields[0] // e.g. "github.com/foo/bar/pkg/x/file.go:1.10,5.3"
		numStmtsStr := fields[1]
		countStr := fields[2]

		// Parse numStmts
		numStmts, err := strconv.Atoi(numStmtsStr)
		if err != nil {
			return nil, fmt.Errorf("covgate: line %d: invalid numStmts %q: %w", lineNum, numStmtsStr, err)
		}
		if numStmts < 0 {
			return nil, fmt.Errorf("covgate: line %d: negative numStmts %d", lineNum, numStmts)
		}

		// Parse count
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return nil, fmt.Errorf("covgate: line %d: invalid count %q: %w", lineNum, countStr, err)
		}

		// Extract file path from blockSpec (everything before the last ':')
		colonIdx := strings.LastIndex(blockSpec, ":")
		if colonIdx < 0 {
			return nil, fmt.Errorf("covgate: line %d: missing ':' in block spec %q", lineNum, blockSpec)
		}
		filePath := blockSpec[:colonIdx]

		// Derive package path = directory of file path
		pkg := path.Dir(filePath)

		pkgTotal[pkg] += numStmts
		totalStmts += numStmts
		if count > 0 {
			pkgCovered[pkg] += numStmts
			coveredStmts += numStmts
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("covgate: scanning: %w", err)
	}

	// Compute per-package coverage percentages.
	packages := make(map[string]float64, len(pkgTotal))
	for pkg, total := range pkgTotal {
		if total == 0 {
			packages[pkg] = 100.0
		} else {
			packages[pkg] = 100.0 * float64(pkgCovered[pkg]) / float64(total)
		}
	}

	// Compute aggregate.
	var aggregate float64
	if totalStmts == 0 {
		aggregate = 100.0
	} else {
		aggregate = 100.0 * float64(coveredStmts) / float64(totalStmts)
	}

	return &CoverageReport{
		Mode:              mode,
		Packages:          packages,
		Aggregate:         aggregate,
		TotalStatements:   totalStmts,
		CoveredStatements: coveredStmts,
	}, nil
}

// MeetsThreshold returns true if the aggregate coverage is >= min.
func (r *CoverageReport) MeetsThreshold(min float64) bool {
	return r.Aggregate >= min
}

// PackageCoverage returns the coverage percentage for the given package import
// path. Returns 0.0 if the package is not present in the report.
func (r *CoverageReport) PackageCoverage(pkg string) float64 {
	return r.Packages[pkg]
}

// Shortfalls returns a sorted list of package import paths whose prefix
// matches the given prefix and whose coverage is below min.
func (r *CoverageReport) Shortfalls(prefix string, min float64) []string {
	var result []string
	for pkg, pct := range r.Packages {
		if strings.HasPrefix(pkg, prefix) && pct < min {
			result = append(result, pkg)
		}
	}
	// Sort for determinism.
	sortStrings(result)
	return result
}

// sortStrings sorts a string slice in ascending order (stdlib only — no sort import needed,
// we inline a simple insertion sort to avoid import ambiguity; the slices are always small).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}
