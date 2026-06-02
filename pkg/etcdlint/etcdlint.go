// Package etcdlint implements a static-analysis lint gate that enforces the
// Helix Cluster OS architectural rule: only cell-local packages are permitted
// to import etcd ("go.etcd.io/etcd…"). Cross-cell / federation-layer packages
// must not. A violation is any .go source file whose declared package clause
// name is NOT on the caller-supplied allowlist yet contains an import whose
// path starts with the prefix "go.etcd.io/etcd".
//
// The package uses only the Go standard library (go/parser, go/token,
// path/filepath, sort, strconv, strings, fmt, errors). It does NOT depend on
// golang.org/x/tools/go/analysis or any external module.
package etcdlint

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// etcdImportPrefix is the canonical prefix that identifies all etcd import
// paths that must be restricted to cell-local packages.
const etcdImportPrefix = "go.etcd.io/etcd"

// ErrParse is the sentinel error returned (as a wrapping cause) when a source
// file cannot be parsed. It is never silently swallowed.
var ErrParse = errors.New("etcdlint: parse error")

// Violation records a single file that imports etcd from a package that is not
// on the allowlist.
type Violation struct {
	// File is the absolute or relative path of the offending source file,
	// exactly as supplied to CheckFiles or discovered by CheckDir.
	File string
	// Package is the package clause name declared in the source file
	// (e.g. "federation"). It is the raw identifier from `package <name>`,
	// not a full import path.
	Package string
	// Import is the full offending import path
	// (e.g. "go.etcd.io/etcd/client/v3").
	Import string
}

// CheckFiles parses each of the given .go source files and returns a Violation
// for every file/import pair where:
//   - the file's package clause name is NOT in allow, AND
//   - the import path starts with "go.etcd.io/etcd".
//
// allow is compared by exact string match against the package clause name.
// All files are processed even when a violation is found (no early return on
// first hit). Output is sorted by File then Import for determinism.
//
// If any file cannot be parsed, CheckFiles stops immediately and returns a
// non-nil error wrapping ErrParse together with the offending file name.
func CheckFiles(files []string, allow []string) ([]Violation, error) {
	allowed := make(map[string]struct{}, len(allow))
	for _, a := range allow {
		allowed[a] = struct{}{}
	}

	fset := token.NewFileSet()
	var violations []Violation

	for _, path := range files {
		// Use mode=0 (full parse) rather than ImportsOnly so that genuine
		// syntax errors (missing package clause, invalid declarations) are
		// surfaced and wrapped in ErrParse. ImportsOnly silently ignores
		// declaration errors after the import section.
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrParse, path, err)
		}

		pkgName := f.Name.Name
		if _, ok := allowed[pkgName]; ok {
			// package is on the allowlist — no violation possible.
			continue
		}

		for _, imp := range f.Imports {
			// imp.Path.Value is a quoted string literal such as
			// `"go.etcd.io/etcd/client/v3"`. Use strconv.Unquote so the
			// unquoting is correct by contract (handles all Go string literal
			// forms) and surfaces malformed literals as a parse error rather
			// than silently returning a wrong path.
			importPath, unquoteErr := strconv.Unquote(imp.Path.Value)
			if unquoteErr != nil {
				return nil, fmt.Errorf("%w: %s: unquoting import path %s: %v",
					ErrParse, path, imp.Path.Value, unquoteErr)
			}
			if IsEtcdImport(importPath) {
				violations = append(violations, Violation{
					File:    path,
					Package: pkgName,
					Import:  importPath,
				})
			}
		}
	}

	// Sort for determinism: primary key = File, secondary = Import.
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Import < violations[j].Import
	})

	return violations, nil
}

// CheckDir walks root recursively for *.go files, skips files whose base name
// ends in "_test.go" and any directory named "vendor", then delegates to
// CheckFiles. Output is sorted by File then Import for determinism.
//
// If any eligible file cannot be parsed, CheckDir stops immediately and
// returns a non-nil error wrapping ErrParse.
func CheckDir(root string, allow []string) ([]Violation, error) {
	var goFiles []string

	// filepath.WalkDir visits in lexical order, which already provides a
	// deterministic file list before CheckFiles sorts violations.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip vendor directories entirely.
		if d.IsDir() && d.Name() == "vendor" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		// Accept only .go files; exclude test files.
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		goFiles = append(goFiles, path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return CheckFiles(goFiles, allow)
}

// IsEtcdImport reports whether an already-unquoted import path refers to an
// etcd package. It is the single authoritative predicate used by CheckFiles;
// tests may call it directly to verify the exact condition that a mutation of
// HasPrefix (e.g. changing it to == or EqualFold) would break.
func IsEtcdImport(importPath string) bool {
	return strings.HasPrefix(importPath, etcdImportPrefix)
}
