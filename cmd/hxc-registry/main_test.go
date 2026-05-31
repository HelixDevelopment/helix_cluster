package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// newEnv returns a getenv func that points HXC_DB at a fresh on-disk database in
// a per-test temp dir. The registry runs its REAL SQLite migrations against this
// file, so every assertion below exercises end-user behaviour (a real DB sink),
// not a mock (CLAUDE-1 §5).
func newEnv(t *testing.T) func(string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "reg.db")
	return func(k string) string {
		if k == "HXC_DB" {
			return dbPath
		}
		return ""
	}
}

// invoke runs the CLI seam and returns (exitCode, stdout, stderr).
func invoke(t *testing.T, env func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, env, &out, &errb)
	return code, out.String(), errb.String()
}

// TestAddThenShow proves a row written by `add` is readable back by `show` with
// the exact field values — i.e. the create actually persisted to the DB.
// Mutation that kills it: in cmdAdd, set item.Title = "" before CreateItem (or
// drop the CreateItem call) -> show no longer prints the title -> FAIL.
func TestAddThenShow(t *testing.T) {
	env := newEnv(t)

	code, out, errOut := invoke(t, env, "add", "--id", "HXC-904",
		"--title", "Build Service", "--type", "Feature", "--priority", "P0", "--phase", "4")
	if code != exitOK {
		t.Fatalf("add exit=%d stderr=%q", code, errOut)
	}
	if want := "Created HXC-904: Build Service\n"; out != want {
		t.Fatalf("add stdout = %q, want %q", out, want)
	}

	code, out, errOut = invoke(t, env, "show", "HXC-904")
	if code != exitOK {
		t.Fatalf("show exit=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{
		"HXC ID:      HXC-904",
		"Title:       Build Service",
		"Status:      Queued",
		"Type:        Feature",
		"Priority:    P0",
		"Phase:       4",
		"Location:    Issues",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show stdout missing %q; got:\n%s", want, out)
		}
	}
}

// TestAddAutoID proves NextHXCID is wired: with no --id, the first add gets
// HXC-001 and the second add increments to HXC-002.
// Mutation: in cmdAdd, replace `itemID = next` with `itemID = "HXC-001"` ->
// second add collides / prints HXC-001 again -> FAIL.
func TestAddAutoID(t *testing.T) {
	env := newEnv(t)

	if code, out, e := invoke(t, env, "add", "--title", "First"); code != exitOK {
		t.Fatalf("add1 exit=%d stderr=%q", code, e)
	} else if !strings.HasPrefix(out, "Created HXC-001:") {
		t.Fatalf("add1 stdout = %q, want HXC-001", out)
	}

	if code, out, e := invoke(t, env, "add", "--title", "Second"); code != exitOK {
		t.Fatalf("add2 exit=%d stderr=%q", code, e)
	} else if !strings.HasPrefix(out, "Created HXC-002:") {
		t.Fatalf("add2 stdout = %q, want HXC-002", out)
	}
}

// TestListFilter proves list returns all rows unfiltered and respects a status
// filter. Two items with different statuses are created; the filtered list must
// contain only the matching one.
// Mutation: in cmdList, ignore the parsed status (status = "") -> filtered list
// includes both rows -> FAIL.
func TestListFilter(t *testing.T) {
	env := newEnv(t)
	mustOK(t, env, "add", "--id", "HXC-001", "--title", "Queued one")
	mustOK(t, env, "add", "--id", "HXC-002", "--title", "Done one")
	mustOK(t, env, "update", "HXC-002", "--status", "Completed")

	// Unfiltered: both present, total 2.
	_, out, _ := invoke(t, env, "list")
	if !strings.Contains(out, "HXC-001") || !strings.Contains(out, "HXC-002") {
		t.Fatalf("list (all) missing rows:\n%s", out)
	}
	if !strings.Contains(out, "Total: 2 items") {
		t.Fatalf("list (all) total wrong:\n%s", out)
	}

	// Filtered by Completed via --status: only HXC-002.
	_, out, _ = invoke(t, env, "list", "--status", "Completed")
	if strings.Contains(out, "HXC-001") {
		t.Fatalf("Completed filter leaked HXC-001:\n%s", out)
	}
	if !strings.Contains(out, "HXC-002") || !strings.Contains(out, "Total: 1 items") {
		t.Fatalf("Completed filter wrong:\n%s", out)
	}

	// Positional status form must behave identically.
	_, out, _ = invoke(t, env, "list", "Queued")
	if !strings.Contains(out, "HXC-001") || strings.Contains(out, "HXC-002") {
		t.Fatalf("positional Queued filter wrong:\n%s", out)
	}
}

// TestUpdatePersists proves update writes through to the DB: status/commit set
// by update are visible via show.
// Mutation: in cmdUpdate, drop the `item.Status = *status` assignment -> show
// still reports Queued -> FAIL.
func TestUpdatePersists(t *testing.T) {
	env := newEnv(t)
	mustOK(t, env, "add", "--id", "HXC-010", "--title", "Thing")

	if code, out, e := invoke(t, env, "update", "HXC-010",
		"--status", "Completed", "--commit", "abc123"); code != exitOK {
		t.Fatalf("update exit=%d stderr=%q", code, e)
	} else if out != "Updated HXC-010\n" {
		t.Fatalf("update stdout = %q", out)
	}

	_, out, _ := invoke(t, env, "show", "HXC-010")
	if !strings.Contains(out, "Status:      Completed") {
		t.Fatalf("status not persisted:\n%s", out)
	}
	if !strings.Contains(out, "Commit:      abc123") {
		t.Fatalf("commit not persisted:\n%s", out)
	}
}

// TestUpdateMissingFails proves updating a non-existent id is a real error
// (exit 1), not a silent success. GetItem fails before UpdateItem.
// Mutation: in cmdUpdate, return exitOK when GetItem errors -> exit becomes 0 ->
// FAIL.
func TestUpdateMissingFails(t *testing.T) {
	env := newEnv(t)
	code, _, errOut := invoke(t, env, "update", "HXC-999", "--status", "Completed")
	if code != exitError {
		t.Fatalf("update missing exit=%d, want %d", code, exitError)
	}
	if !strings.Contains(errOut, "HXC-999 not found") {
		t.Fatalf("stderr = %q, want not-found", errOut)
	}
}

// TestNextEmptyAndAfterAdd proves `next` reads the real max id from the DB:
// HXC-001 on an empty registry, HXC-002 after one row exists.
// Mutation: in cmdNext, print a constant "HXC-001" -> after-add case FAILs.
func TestNextEmptyAndAfterAdd(t *testing.T) {
	env := newEnv(t)
	if _, out, _ := invoke(t, env, "next"); strings.TrimSpace(out) != "HXC-001" {
		t.Fatalf("next (empty) = %q, want HXC-001", out)
	}
	mustOK(t, env, "add", "--id", "HXC-001", "--title", "x")
	if _, out, _ := invoke(t, env, "next"); strings.TrimSpace(out) != "HXC-002" {
		t.Fatalf("next (after add) = %q, want HXC-002", out)
	}
}

// TestInitCreatesDB proves init opens/migrates a real DB file and that a
// subsequent add against the same path works (schema is present).
// Mutation: make cmdInit return before opening the registry / print a fake path
// -> the reported path won't match -> FAIL.
func TestInitCreatesDB(t *testing.T) {
	env := newEnv(t)
	dbPath := env("HXC_DB")
	code, out, errOut := invoke(t, env, "init")
	if code != exitOK {
		t.Fatalf("init exit=%d stderr=%q", code, errOut)
	}
	if want := "Registry initialized at " + dbPath + "\n"; out != want {
		t.Fatalf("init stdout = %q, want %q", out, want)
	}
	// The migrated schema must be usable by a follow-up command.
	mustOK(t, env, "add", "--id", "HXC-001", "--title", "post-init")
}

// TestAddRequiresTitle proves arg validation: missing --title is a usage error
// (exit 2) with a specific message, and nothing is written to stdout.
// Mutation: remove the `*title == ""` guard in cmdAdd -> CreateItem rejects the
// empty title with a different (Error:) message and exit code -> FAIL.
func TestAddRequiresTitle(t *testing.T) {
	env := newEnv(t)
	code, out, errOut := invoke(t, env, "add", "--type", "Task")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d", code, exitUsage)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty", out)
	}
	if !strings.Contains(errOut, "--title is required") {
		t.Fatalf("stderr = %q", errOut)
	}
}

// TestAddInvalidTypeRejected proves the registry's Validate runs end-to-end: a
// bogus --type is rejected with exit 1 and no row is created.
// Mutation: in item.go Validate, accept any type -> add succeeds, exit 0 -> FAIL.
func TestAddInvalidTypeRejected(t *testing.T) {
	env := newEnv(t)
	code, _, errOut := invoke(t, env, "add", "--id", "HXC-001",
		"--title", "Bad", "--type", "Nonsense")
	if code != exitError {
		t.Fatalf("exit=%d, want %d", code, exitError)
	}
	if !strings.Contains(errOut, "invalid type") {
		t.Fatalf("stderr = %q, want invalid type", errOut)
	}
	// Confirm nothing persisted.
	if _, out, _ := invoke(t, env, "list"); !strings.Contains(out, "Total: 0 items") {
		t.Fatalf("row was created despite invalid type:\n%s", out)
	}
}

// TestUnknownCommand proves dispatch rejects unknown verbs with exit 2 and an
// explanatory stderr line while still printing usage.
// Mutation: in run, route the default case to exitOK -> FAIL.
func TestUnknownCommand(t *testing.T) {
	env := newEnv(t)
	code, out, errOut := invoke(t, env, "frobnicate")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "Unknown command: frobnicate") {
		t.Fatalf("stderr = %q", errOut)
	}
	if !strings.Contains(out, "HXC Registry CLI") {
		t.Fatalf("usage not printed:\n%s", out)
	}
}

// TestNoArgsIsUsage proves the empty-args path returns usage exit code 2.
// Mutation: in run, return exitOK for the len(args)<1 branch -> FAIL.
func TestNoArgsIsUsage(t *testing.T) {
	env := newEnv(t)
	if code, _, _ := invoke(t, env); code != exitUsage {
		t.Fatalf("no-args exit=%d, want %d", code, exitUsage)
	}
}

// TestHelpExitsZero proves `help` is a successful, distinct path (exit 0) that
// prints usage — separating intentional help from the error usage path.
// Mutation: route "help" to the default/unknown case -> exit becomes 2 -> FAIL.
func TestHelpExitsZero(t *testing.T) {
	env := newEnv(t)
	code, out, _ := invoke(t, env, "help")
	if code != exitOK {
		t.Fatalf("help exit=%d, want %d", code, exitOK)
	}
	if !strings.Contains(out, "Usage: hxc-registry") {
		t.Fatalf("help usage not printed:\n%s", out)
	}
}

// TestBadDBPathErrors proves a bad HXC_DB surfaces as an error exit, not a
// crash. A path whose parent dir does not exist cannot be opened by SQLite.
// Mutation: in openRegistry, return exitOK on error -> FAIL.
func TestBadDBPathErrors(t *testing.T) {
	badEnv := func(k string) string {
		if k == "HXC_DB" {
			return filepath.Join(t.TempDir(), "no_such_dir", "reg.db")
		}
		return ""
	}
	code, _, errOut := invoke(t, badEnv, "next")
	if code != exitError {
		t.Fatalf("exit=%d, want %d", code, exitError)
	}
	if !strings.Contains(errOut, "Error:") {
		t.Fatalf("stderr = %q, want Error:", errOut)
	}
}

func mustOK(t *testing.T, env func(string) string, args ...string) string {
	t.Helper()
	code, out, errOut := invoke(t, env, args...)
	if code != exitOK {
		t.Fatalf("%v exit=%d stderr=%q", args, code, errOut)
	}
	return out
}
