// Package archlint mechanically enforces the architecture component map in
// docs/ARCHITECTURE.md (HXC-1424). It parses the "Component Map" table and
// verifies that every component's documented Package column points at a real
// package directory in the repository. The build fails — via the package test —
// if a documented component has no corresponding package on disk, so the
// architecture diagram cannot drift away from the code it claims to describe.
package archlint

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoComponents is returned by ParseComponentMap when the markdown contains
// no parseable component rows (a malformed or empty map).
var ErrNoComponents = errors.New("archlint: no component rows found in map")

// Component is one row of the architecture Component Map.
type Component struct {
	Name    string
	Layer   string
	Package string // repository-relative path, e.g. "pkg/scheduler"
	Origin  string
}

// ParseComponentMap extracts the Component Map rows from ARCHITECTURE.md content.
// It reads the markdown table whose header contains the "Package" column,
// skips the header and separator rows, and returns one Component per data row.
// A data row's Package cell is expected to be wrapped in backticks (`pkg/x`).
func ParseComponentMap(markdown string) ([]Component, error) {
	var (
		comps   []Component
		inTable bool
		pkgCol  = -1
	)
	sc := bufio.NewScanner(strings.NewReader(markdown))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "|") {
			// A blank/non-table line ends any table we were inside.
			if inTable {
				inTable = false
				pkgCol = -1
			}
			continue
		}
		cells := splitRow(line)
		// Header row: locate the Package column index.
		if pkgCol == -1 {
			for i, c := range cells {
				if strings.EqualFold(c, "Package") {
					pkgCol = i
					inTable = true
					break
				}
			}
			continue
		}
		// Separator row (---|---|...): skip.
		if isSeparator(cells) {
			continue
		}
		if !inTable || pkgCol >= len(cells) {
			continue
		}
		pkg := strings.Trim(cells[pkgCol], "`")
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		comp := Component{Package: pkg}
		if len(cells) > 0 {
			comp.Name = cells[0]
		}
		if len(cells) > 1 {
			comp.Layer = cells[1]
		}
		if pkgCol+1 < len(cells) {
			comp.Origin = cells[pkgCol+1]
		}
		comps = append(comps, comp)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("archlint: scan: %w", err)
	}
	if len(comps) == 0 {
		return nil, ErrNoComponents
	}
	return comps, nil
}

// VerifyPackages checks that every component's Package resolves to a directory
// under root. It returns a single error naming every component whose package is
// missing; nil when all resolve. This is the load-bearing check: it must reject
// a component that points at a non-existent package.
func VerifyPackages(root string, comps []Component) error {
	var missing []string
	for _, c := range comps {
		full := filepath.Join(root, filepath.FromSlash(c.Package))
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			missing = append(missing, fmt.Sprintf("%q -> %s", c.Name, c.Package))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("archlint: %d component(s) map to a missing package: %s",
			len(missing), strings.Join(missing, "; "))
	}
	return nil
}

// splitRow splits a markdown table row "| a | b | c |" into trimmed cells,
// dropping the empty leading/trailing fields produced by the edge pipes.
func splitRow(line string) []string {
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	// Drop leading/trailing empties from the bounding pipes.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// isSeparator reports whether a row is a markdown header/body separator, i.e.
// every cell is made only of '-' and ':' characters.
func isSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c == "" {
			return false
		}
		for _, r := range c {
			if r != '-' && r != ':' {
				return false
			}
		}
	}
	return true
}
