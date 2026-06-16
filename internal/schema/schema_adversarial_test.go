package schema

// Adversarial, sink-side probes for internal/schema.
//
// This file is package-internal (package schema, not schema_test) so it can
// reach unexported helpers (ChainUpFiles ordering internals are exported, but
// keeping it internal lets us exercise the exact verdict surface without an
// import cycle). It probes four classes:
//
//   (a) FAIL-OPEN VALIDATION  — does a malformed / empty / wrong-shape SQL
//       document pass VerifyStructure when it must fail?
//   (b) MIGRATION / TOTAL-ORDER — is ChainUpFiles a deterministic, numerically
//       correct total order, or does lexical sort mis-order a realistic chain?
//   (c) UNGUARDED SHARED STATE — concurrent VerifyStructure + ResolvePSQLConfig
//       under -race (capped goroutines) — any data race on package globals?
//   (d) BOUNDARY — substring-based CHECK fragment matching: does an unrelated
//       textual coincidence make a CHECK fact PASS (false-accept), and does the
//       table-count boundary (==15) hold at the edges?
//
// SINK asserted everywhere = the actual PASS/FAIL verdict of
// VerifyStructure/AllFactsPassed, or the actual ordered slice from
// ChainUpFiles — never "did not panic".

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// liveSQL loads the real consolidated schema or fatals.
func liveSQL(t *testing.T) string {
	t.Helper()
	sql, err := ReadSQL()
	if err != nil {
		t.Fatalf("ReadSQL: %v", err)
	}
	return sql
}

// factByContains returns the first fact whose Name contains sub, or a zero fact.
func factByContains(facts []StructuralFact, sub string) (StructuralFact, bool) {
	for _, f := range facts {
		if strings.Contains(f.Name, sub) {
			return f, true
		}
	}
	return StructuralFact{}, false
}

// ─────────────────────────────────────────────────────────────────────────────
// (a) FAIL-OPEN VALIDATION
// ─────────────────────────────────────────────────────────────────────────────

// The contract (schema.go:196-197): "All facts must have Passed=true for a valid
// schema." The promise of VerifyStructure is that it distinguishes a valid schema
// from an invalid one. Probe: an EMPTY document must NOT pass.
func TestAdversarial_FailOpen_EmptyDocumentRejected(t *testing.T) {
	facts := VerifyStructure("")
	passed, failures := AllFactsPassed(facts)
	if passed {
		t.Fatalf("FAIL-OPEN: empty SQL document passed VerifyStructure (must FAIL)")
	}
	if len(failures) == 0 {
		t.Fatalf("FAIL-OPEN: empty document produced zero failures")
	}
	t.Logf("empty doc correctly rejected with %d failures", len(failures))
}

// Probe: a document that contains all the required NAME tokens but as a single
// blob of garbage (no real DDL) — i.e. a malformed schema that merely name-drops
// every required identifier. VerifyStructure is a TEXTUAL verifier, so by
// contract this is a KNOWN, DISCLAIMED limitation (it checks for fragments, not
// parsed DDL). We pin the actual behavior so a future change is caught.
//
// The construction below name-drops every required table via the exact needle
// "CREATE TABLE IF NOT EXISTS <t> \n" and every other required fragment. If this
// PASSES, it confirms VerifyStructure is a substring verifier (documented
// contract, not a bug). If it ever FAILS, the verifier got stricter and someone
// must re-pin.
func TestAdversarial_FailOpen_SubstringVerifierContract(t *testing.T) {
	var b strings.Builder
	for _, tbl := range RequiredTables {
		// Matches needle2: "CREATE TABLE IF NOT EXISTS <T>\n" — note: NO column
		// body. A real DDL parser would reject a table with no columns.
		fmt.Fprintf(&b, "CREATE TABLE IF NOT EXISTS %s\n", tbl)
	}
	for _, fn := range RequiredFunctions {
		fmt.Fprintf(&b, "%s\n", fn)
	}
	for _, trig := range RequiredTriggers {
		fmt.Fprintf(&b, "%s\n", trig)
	}
	for _, idx := range RequiredIndexPrefixes {
		fmt.Fprintf(&b, "%s\n", idx)
	}
	for _, chk := range RequiredCheckConstraints {
		fmt.Fprintf(&b, "%s\n", chk)
	}
	// Facts 6/7/8 fragments.
	b.WriteString("PARTITION BY RANGE\n")
	b.WriteString("PARTITION OF AUDIT_LOG\n")
	b.WriteString("HELIX_NODES_AUDIT\n")

	facts := VerifyStructure(b.String())
	passed, failures := AllFactsPassed(facts)

	// SINK: this name-drop blob — columnless tables, no real DDL — PASSES.
	// That is the documented textual-verifier contract (not a code bug); the
	// integration suite is what proves real DDL applies. We assert the contract
	// explicitly so it cannot silently change.
	if !passed {
		t.Fatalf("substring-verifier contract changed: name-drop blob now FAILS (%d failures): %v\n"+
			"If VerifyStructure was intentionally hardened into a real parser, update this pin.",
			len(failures), failures)
	}
	t.Log("CONFIRMED (documented contract): VerifyStructure is a substring verifier — " +
		"a columnless name-drop blob passes. Real-DDL validity is proven only by the " +
		"integration suite (CLAUDE-1: tests necessary but not sufficient).")
}

// Probe the MUTATION direction of fail-open: drop ONE required fragment from the
// REAL schema and prove the verdict flips to FAIL. This is the bite that proves
// VerifyStructure is not a constant-true PASS-bluff.
func TestAdversarial_FailOpen_MissingRequiredFieldRejected(t *testing.T) {
	sql := liveSQL(t)

	// Baseline: real schema passes.
	if passed, fails := AllFactsPassed(VerifyStructure(sql)); !passed {
		t.Fatalf("precondition: real schema must pass, but failed: %v", fails)
	}

	// Remove the audit-log partitioning — a structural requirement (Fact 6).
	// Replace the only occurrence of "PARTITION BY RANGE" so the fact must flip.
	mutated := strings.Replace(sql, "PARTITION BY RANGE", "PARTITION BY HASH_BROKEN", 1)
	if mutated == sql {
		t.Fatal("precondition: live schema unexpectedly lacks 'PARTITION BY RANGE'")
	}
	facts := VerifyStructure(mutated)
	passed, _ := AllFactsPassed(facts)
	if passed {
		t.Fatal("FAIL-OPEN: removing 'PARTITION BY RANGE' did NOT flip verdict to FAIL")
	}
	f, ok := factByContains(facts, "PARTITION BY RANGE")
	if !ok || f.Passed {
		t.Fatalf("FAIL-OPEN: the audit_log partition fact did not register the removal: %+v", f)
	}
	t.Log("missing-required-fragment correctly rejected (verdict flipped to FAIL)")
}

// ─────────────────────────────────────────────────────────────────────────────
// (b) MIGRATION / TOTAL-ORDER
// ─────────────────────────────────────────────────────────────────────────────

// orderKeyBasenames reduces ChainUpFiles output to basenames for assertion.
func basenames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// Pin the LIVE chain ordering: it must be strictly ascending by numeric prefix.
// The contract (schema.go:138-141): "ordered list ... sorted by version." With
// the current fixed-width 3-digit prefixes (001..015), lexical sort == numeric
// sort. We assert numeric monotonicity at the SINK (the returned slice).
func TestAdversarial_Migration_LiveChainNumericallyOrdered(t *testing.T) {
	files, err := ChainUpFiles()
	if err != nil {
		t.Fatalf("ChainUpFiles: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no chain *.up.sql files present — nothing to order")
	}
	names := basenames(files)
	t.Logf("live chain order: %v", names)

	prev := -1
	for _, n := range names {
		// numeric prefix up to first '_'
		us := strings.IndexByte(n, '_')
		if us <= 0 {
			t.Fatalf("chain file %q has no numeric prefix", n)
		}
		num := 0
		for _, c := range n[:us] {
			if c < '0' || c > '9' {
				t.Fatalf("chain file %q prefix not numeric", n)
			}
			num = num*10 + int(c-'0')
		}
		if num <= prev {
			t.Fatalf("MIGRATION ORDER: chain not strictly ascending: %d after %d (%q)", num, prev, n)
		}
		prev = num
	}
}

// DETERMINISM: ChainUpFiles must return the same order on repeated calls (it
// reads a dir + sort.Strings). os.ReadDir already sorts, and sort.Strings is
// deterministic, so this must hold. Assert it at the SINK.
func TestAdversarial_Migration_Deterministic(t *testing.T) {
	a, err := ChainUpFiles()
	if err != nil {
		t.Fatalf("ChainUpFiles #1: %v", err)
	}
	for i := 0; i < 5; i++ {
		b, err := ChainUpFiles()
		if err != nil {
			t.Fatalf("ChainUpFiles #%d: %v", i+2, err)
		}
		if strings.Join(a, "\x00") != strings.Join(b, "\x00") {
			t.Fatalf("MIGRATION NONDETERMINISM: call %d differs:\n%v\nvs\n%v", i+2, a, b)
		}
	}
}

// LATENT-RISK probe (pure, no FS): the production sort key is lexical
// (sort.Strings over full paths == lexical over basenames). With fixed-width
// 3-digit prefixes this equals numeric order. This test demonstrates the latent
// hazard if a future migration is added WITHOUT zero-padding (e.g. "9_*" then
// "10_*"): lexical sort would place "10" before "9". We assert the documented
// invariant (all prefixes same width) so a mixed-width addition is caught here
// rather than silently mis-applying migrations in production.
func TestAdversarial_Migration_FixedWidthPrefixInvariant(t *testing.T) {
	files, err := ChainUpFiles()
	if err != nil {
		t.Fatalf("ChainUpFiles: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no chain files")
	}
	widthSet := map[int]bool{}
	for _, p := range files {
		n := filepath.Base(p)
		us := strings.IndexByte(n, '_')
		if us <= 0 {
			t.Fatalf("file %q has no numeric prefix", n)
		}
		widthSet[us] = true
	}
	if len(widthSet) != 1 {
		t.Fatalf("LATENT MIGRATION HAZARD: chain prefixes have MIXED widths %v — "+
			"lexical sort.Strings (schema.go:164) would mis-order them numerically. "+
			"Zero-pad every prefix to a uniform width.", widthSet)
	}
	// Demonstrate the hazard concretely on a synthetic mixed-width set so the
	// reader sees WHY uniform width matters (pure string logic; no FS writes).
	mixed := []string{"9_x.up.sql", "10_x.up.sql", "2_x.up.sql"}
	sorted := append([]string(nil), mixed...)
	sort.Strings(sorted)
	if sorted[0] != "10_x.up.sql" {
		t.Fatalf("demonstration sanity broke: %v", sorted)
	}
	t.Logf("DEMONSTRATED hazard: lexical sort of mixed-width %v = %v (10 sorts before 2/9). "+
		"Live chain is uniform-width (%d-digit), so it is currently SAFE.", mixed, sorted, func() int {
		for w := range widthSet {
			return w
		}
		return 0
	}())
}

// ─────────────────────────────────────────────────────────────────────────────
// (c) UNGUARDED SHARED STATE (run with -race)
// ─────────────────────────────────────────────────────────────────────────────

// VerifyStructure reads package-global slices (RequiredTables, …) and builds a
// fresh facts slice; ResolvePSQLConfig reads env. Concurrent readers must be
// race-free. Probe with capped goroutines under -race; assert the SINK verdict
// is the SAME on every goroutine (no torn reads of the globals).
func TestAdversarial_Concurrent_VerifyStructureRaceFree(t *testing.T) {
	sql := liveSQL(t)
	wantPassed, _ := AllFactsPassed(VerifyStructure(sql))

	const workers = 8 // capped per ANTI-HANG
	const iters = 50
	var wg sync.WaitGroup
	errCh := make(chan string, workers*iters)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				facts := VerifyStructure(sql)
				gotPassed, _ := AllFactsPassed(facts)
				if gotPassed != wantPassed {
					errCh <- fmt.Sprintf("torn verdict: got %v want %v", gotPassed, wantPassed)
					return
				}
				// also exercise the env reader concurrently
				_, _ = ResolvePSQLConfig()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	if msg, ok := <-errCh; ok {
		t.Fatalf("CONCURRENCY: %s", msg)
	}
	t.Logf("8x50 concurrent VerifyStructure calls all returned verdict=%v (race-free)", wantPassed)
}

// ─────────────────────────────────────────────────────────────────────────────
// (d) BOUNDARY
// ─────────────────────────────────────────────────────────────────────────────

// The CHECK facts (schema.go:254-262) test bare substrings like "BETWEEN 0 AND
// 100". A document where that fragment appears in an UNRELATED context (a
// comment, a different column) would false-ACCEPT. Probe the false-accept
// boundary: a doc with ONLY the fragments in comments still passes the CHECK
// facts. This is the documented substring-verifier limitation; pin it.
func TestAdversarial_Boundary_CheckFragmentFalseAcceptIsTextual(t *testing.T) {
	// A minimal doc that PASSES only the CHECK facts via comment text.
	doc := "-- BETWEEN 0 AND 100\n-- CHECK (cpu_cores > 0)\n-- CHECK (memory_bytes > 0)\n" +
		"-- CHECK (status IN ...)\n-- CHECK (role IN ...)\n" +
		"-- CHECK (confidence >= 0.0\n-- CHECK (priority >= 0\n"
	facts := VerifyStructure(doc)
	for _, chk := range RequiredCheckConstraints {
		f, ok := factByContains(facts, "CHECK: "+chk)
		if !ok {
			t.Fatalf("no fact for CHECK %q", chk)
		}
		if !f.Passed {
			t.Fatalf("substring-verifier contract changed: CHECK %q no longer matches in-comment text", chk)
		}
	}
	t.Log("CONFIRMED (documented contract): CHECK facts are substring matches — " +
		"fragments in comments false-ACCEPT. Real enforcement proven by integration CHECK-violation tests.")
}

// The table-count boundary is `tableCount == 15` (schema.go:218-222). Because the
// loop iterates exactly len(RequiredTables) entries and each can match at most
// once, the count can never EXCEED len(RequiredTables). Pin this invariant: with
// the live schema the count is exactly 15 and the fact passes; with one table
// needle removed it must drop below 15 and FAIL (no off-by-one that lets 14 pass
// as "15").
func TestAdversarial_Boundary_TableCountExactEquality(t *testing.T) {
	sql := liveSQL(t)
	facts := VerifyStructure(sql)
	f, ok := factByContains(facts, "15 required tables")
	if !ok {
		t.Fatal("no '15 required tables' fact")
	}
	if !f.Passed {
		t.Fatalf("live schema should report 15/15 tables, detail=%q", f.Detail)
	}

	// Remove exactly one table's CREATE statement → count must become 14 → FAIL.
	// 'cluster_config' is last in RequiredTables; its needle is distinctive.
	mutated := strings.Replace(sql,
		"CREATE TABLE IF NOT EXISTS cluster_config",
		"CREATE TABLE IF NOT EXISTS cluster_config_RENAMED", 1)
	if mutated == sql {
		t.Fatal("precondition: could not find cluster_config CREATE TABLE in live schema")
	}
	f2, _ := factByContains(VerifyStructure(mutated), "15 required tables")
	if f2.Passed {
		t.Fatalf("BOUNDARY: 14/15 tables incorrectly passed the ==15 count gate; detail=%q", f2.Detail)
	}
	if !strings.Contains(f2.Detail, "14/15") {
		t.Fatalf("BOUNDARY: expected detail to report 14/15, got %q", f2.Detail)
	}
	t.Log("table-count gate is exact equality: 14/15 correctly FAILS (no off-by-one).")
}
