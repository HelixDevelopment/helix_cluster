// Adversarial tests for the helix-gate orphan-prevention gate suite (cmd/helix-gate).
//
// These tests target the WIRING layer in main.go — the parsing/config glue that
// feeds the real gate packages — and assert the SINK verdict (the boolean each
// run* function returns, which becomes the process exit code). The governing
// rule (CLAUDE-1) is that a gate MUST fail CLOSED: a no-data / malformed /
// empty / unauthorized input must produce FAIL, never a PASS-bluff. Every probe
// here is a deny/allow decision, not a "does not panic" smoke test.
//
// The load-bearing one is TestCovgate_EmptyPackagePath_FailsAudit: a mutation
// catalog entry with an empty package field used to pass the audit as a "real
// package" because filepath.Join(root, "") == root (a directory). That is a
// fail-open audit hole; the fix in runCovgate rejects an empty package path.
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a small helper that writes body to <dir>/<name>, creating dir.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// ---------------------------------------------------------------------------
// covgate mutation-pairing audit — fail-closed on a catalog that pins no real
// package (the load-bearing bug).
// ---------------------------------------------------------------------------

// TestCovgate_EmptyPackagePath_FailsAudit is the load-bearing probe. A catalog
// entry whose pkg field (field 5) is empty resolves, via
// filepath.Join(root, ""), to the repository root — a directory — and without
// the guard is silently accepted as "maps to a real package with paired
// tests". A mutation case that names NO package proves nothing; accepting it is
// a §1.1 / CLAUDE-1 PASS-bluff. The audit MUST FAIL.
func TestCovgate_EmptyPackagePath_FailsAudit(t *testing.T) {
	root := t.TempDir()
	mdir := filepath.Join(root, "test", "mutation")
	// 7 fields, field 5 (pkg) deliberately empty, non-empty test regex so the
	// ONLY thing wrong is the missing package.
	writeFile(t, mdir, "run_mutations.sh", "CASES=(\n  \"id1|file.go|a|b||TestX|0\"\n)\n")

	if runCovgate(root, nil) {
		t.Fatalf("FAIL-OPEN: covgate accepted a mutation case with an EMPTY package path " +
			"as a real package (filepath.Join(root, \"\") == root). A catalog entry that " +
			"pins no package must FAIL the audit (CLAUDE-1 PASS-bluff).")
	}
}

// TestCovgate_DotSlashOnlyPackagePath_FailsAudit pins the sibling case: a pkg
// field of literally "./" trims to empty and would likewise resolve to root.
func TestCovgate_DotSlashOnlyPackagePath_FailsAudit(t *testing.T) {
	root := t.TempDir()
	mdir := filepath.Join(root, "test", "mutation")
	writeFile(t, mdir, "run_mutations.sh", "CASES=(\n  \"id1|file.go|a|b|./|TestX|0\"\n)\n")

	if runCovgate(root, nil) {
		t.Fatalf("FAIL-OPEN: covgate accepted a mutation case with a \"./\"-only package path " +
			"(trims to empty -> repo root). Must FAIL the audit.")
	}
}

// TestCovgate_MissingPackagePath_FailsAudit confirms a non-empty but
// nonexistent package is rejected (the pre-existing happy-path of the guard).
func TestCovgate_MissingPackagePath_FailsAudit(t *testing.T) {
	root := t.TempDir()
	mdir := filepath.Join(root, "test", "mutation")
	writeFile(t, mdir, "run_mutations.sh",
		"CASES=(\n  \"id1|file.go|a|b|./pkg/does_not_exist|TestX|0\"\n)\n")

	if runCovgate(root, nil) {
		t.Fatalf("FAIL-OPEN: covgate accepted a mutation case pointing at a nonexistent package.")
	}
}

// TestCovgate_EmptyTestRegex_FailsAudit confirms a real package but EMPTY
// paired-test regex is rejected (an untested mutation case is a PASS-bluff).
func TestCovgate_EmptyTestRegex_FailsAudit(t *testing.T) {
	root := t.TempDir()
	mdir := filepath.Join(root, "test", "mutation")
	// pkg points at the (real, existing) test/mutation dir; test regex empty.
	writeFile(t, mdir, "run_mutations.sh",
		"CASES=(\n  \"id1|file.go|a|b|./test/mutation||0\"\n)\n")

	if runCovgate(root, nil) {
		t.Fatalf("FAIL-OPEN: covgate accepted a mutation case with an EMPTY paired-test regex.")
	}
}

// TestCovgate_RealPackageWithRegex_PassesAudit is the positive control: a valid
// catalog entry (real dir + non-empty regex, no coverage profile) PASSES the
// audit. This guards against the fix over-rejecting.
func TestCovgate_RealPackageWithRegex_PassesAudit(t *testing.T) {
	root := t.TempDir()
	mdir := filepath.Join(root, "test", "mutation")
	// Use ./test/mutation itself as a guaranteed-existing package directory.
	writeFile(t, mdir, "run_mutations.sh",
		"CASES=(\n  \"id1|file.go|a|b|./test/mutation|TestX|0\"\n)\n")

	if !runCovgate(root, nil) {
		t.Fatalf("REGRESSION: covgate rejected a VALID mutation case (real dir + non-empty regex). " +
			"The empty-package guard must not over-reject legitimate entries.")
	}
}

// TestCovgate_MissingCatalog_FailsClosed: no catalog file at all must FAIL,
// never silently PASS.
func TestCovgate_MissingCatalog_FailsClosed(t *testing.T) {
	root := t.TempDir() // no test/mutation/run_mutations.sh
	if runCovgate(root, nil) {
		t.Fatalf("FAIL-OPEN: covgate PASSED with NO mutation catalog present.")
	}
}

// TestCovgate_EmptyCatalog_FailsClosed: a catalog whose lines are all dropped
// (no quoted+piped entries) yields zero cases and MUST fail.
func TestCovgate_EmptyCatalog_FailsClosed(t *testing.T) {
	root := t.TempDir()
	mdir := filepath.Join(root, "test", "mutation")
	writeFile(t, mdir, "run_mutations.sh", "#!/bin/bash\n# no cases here\nCASES=(\n)\n")
	if runCovgate(root, nil) {
		t.Fatalf("FAIL-OPEN: covgate PASSED with an EMPTY mutation catalog (zero cases).")
	}
}

// ---------------------------------------------------------------------------
// qualitygate JSON wiring — fail-closed on no-data / malformed metric snapshots.
// ---------------------------------------------------------------------------

// TestQualitygate_EmptyObject_FailsClosed pins the wiring contract for an empty
// `{}` snapshot: zero KPIs means the safety baseline is unproven, so the gate
// MUST FAIL. This catches a regression either in the cmd wiring or in the
// underlying Validate fail-closed contract.
func TestQualitygate_EmptyObject_FailsClosed(t *testing.T) {
	root := t.TempDir()
	p := writeFile(t, root, "metrics.json", `{}`)
	if runQualitygate(root, []string{p}) {
		t.Fatalf("FAIL-OPEN: qualitygate PASSED on an EMPTY `{}` metric snapshot (no KPIs).")
	}
}

// TestQualitygate_JSONNull_FailsClosed is the sharpest probe: `null` unmarshals
// into a NIL map with NO error (json silently accepts it). The gate must still
// FAIL — a nil snapshot is no data, not a pass.
func TestQualitygate_JSONNull_FailsClosed(t *testing.T) {
	root := t.TempDir()
	p := writeFile(t, root, "metrics.json", `null`)
	if runQualitygate(root, []string{p}) {
		t.Fatalf("FAIL-OPEN: qualitygate PASSED on a `null` snapshot (nil map, no error).")
	}
}

// TestQualitygate_MalformedJSON_FailsClosed: a syntactically broken snapshot
// must FAIL (parse error -> deny), never default-allow.
func TestQualitygate_MalformedJSON_FailsClosed(t *testing.T) {
	root := t.TempDir()
	p := writeFile(t, root, "metrics.json", `{"x": `)
	if runQualitygate(root, []string{p}) {
		t.Fatalf("FAIL-OPEN: qualitygate PASSED on malformed JSON.")
	}
}

// TestQualitygate_WrongType_FailsClosed: a non-numeric metric value is a type
// error -> deny.
func TestQualitygate_WrongType_FailsClosed(t *testing.T) {
	root := t.TempDir()
	p := writeFile(t, root, "metrics.json", `{"schedule_latency_p99": "fast"}`)
	if runQualitygate(root, []string{p}) {
		t.Fatalf("FAIL-OPEN: qualitygate PASSED on a string-typed metric value.")
	}
}

// TestQualitygate_MissingFile_FailsClosed: an unreadable/absent snapshot path
// must FAIL, not silently pass.
func TestQualitygate_MissingFile_FailsClosed(t *testing.T) {
	root := t.TempDir()
	if runQualitygate(root, []string{filepath.Join(root, "nope.json")}) {
		t.Fatalf("FAIL-OPEN: qualitygate PASSED with a missing metrics file.")
	}
}

// ---------------------------------------------------------------------------
// archlint wiring — fail-closed when the architecture doc is absent.
// ---------------------------------------------------------------------------

// TestArchlint_MissingDoc_FailsClosed: no docs/ARCHITECTURE.md must FAIL.
func TestArchlint_MissingDoc_FailsClosed(t *testing.T) {
	root := t.TempDir()
	if runArchlint(root) {
		t.Fatalf("FAIL-OPEN: archlint PASSED with no docs/ARCHITECTURE.md present.")
	}
}

// ---------------------------------------------------------------------------
// envFloat config parsing — robust defaulting (config wiring contract).
// ---------------------------------------------------------------------------

// TestEnvFloat_Defaulting pins the COVERAGE_THRESHOLD parser. Unset / blank /
// non-numeric input MUST fall back to the supplied default (here 80), so a
// typo in the env var never silently lowers the coverage bar to 0.
func TestEnvFloat_Defaulting(t *testing.T) {
	const def = 80.0
	cases := []struct {
		val  string
		want float64
	}{
		{"", def},      // unset-equivalent (helper sets empty)
		{"   ", def},   // whitespace-only -> Sscanf fails -> default
		{"abc", def},   // non-numeric -> default
		{"90", 90.0},   // valid override
		{"  95 ", 95.0}, // surrounding whitespace tolerated
	}
	for _, c := range cases {
		t.Setenv("HELIX_GATE_TEST_THRESHOLD", c.val)
		got := envFloat("HELIX_GATE_TEST_THRESHOLD", def)
		if got != c.want {
			t.Errorf("envFloat(%q) = %v, want %v (a mis-parsed threshold could silently lower the bar)", c.val, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// repoRoot — HELIX_REPO_ROOT override and go.mod walk.
// ---------------------------------------------------------------------------

// TestRepoRoot_EnvOverride confirms the env override is honoured verbatim.
func TestRepoRoot_EnvOverride(t *testing.T) {
	t.Setenv("HELIX_REPO_ROOT", "/some/explicit/root")
	got, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot returned error with HELIX_REPO_ROOT set: %v", err)
	}
	if got != "/some/explicit/root" {
		t.Fatalf("repoRoot = %q, want the HELIX_REPO_ROOT override", got)
	}
}
