package schema_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/schema"
)

// ─── HXC-1639 drift guard ─────────────────────────────────────────────────────
//
// The golang-migrate chain (migrations/postgresql/001_*.up.sql … 015_*.up.sql) is
// the SINGLE canonical Postgres schema source. The consolidated
// 0001_primary_schema.sql — which internal/schema.ApplyPrimarySchema applies — is a
// DERIVED artifact: the in-order concatenation of the chain bodies, produced by
// migrations/postgresql/.gen_schema.py.
//
// These tests make divergence impossible to introduce silently: they parse and
// normalize BOTH SQL sources (no live Postgres required) and fail the build if the
// two ever disagree on content or on the set of objects (tables, indexes, CHECK
// constraints, triggers, functions) they create. If someone hand-edits one source
// without regenerating, this test goes red.

// normalizeSQL strips SQL line comments and collapses all runs of whitespace to a
// single space, lower-casing the result, so two semantically-identical SQL texts
// that differ only in comments/indentation compare equal.
func normalizeSQL(sql string) string {
	var sb strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		// Drop full-line "--" comments and trailing inline comments.
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		sb.WriteString(line)
		sb.WriteByte(' ')
	}
	collapsed := regexp.MustCompile(`\s+`).ReplaceAllString(sb.String(), " ")
	return strings.ToLower(strings.TrimSpace(collapsed))
}

// extractMatches returns the unique, sorted set of the first capture group of re
// found across the normalized SQL.
func extractMatches(t *testing.T, sql, pattern string) []string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(sql, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func loadBothSources(t *testing.T) (consolidated, chain string) {
	t.Helper()
	c, err := schema.ReadSQL()
	if err != nil {
		t.Fatalf("ReadSQL (0001_primary_schema.sql): %v", err)
	}
	ch, err := schema.ReadChainSQL()
	if err != nil {
		t.Fatalf("ReadChainSQL (001..015): %v", err)
	}
	if len(c) == 0 || len(ch) == 0 {
		t.Fatal("one of the schema sources is empty")
	}
	return c, ch
}

// TestDriftGuard_ConsolidatedEqualsChain is the core no-divergence assertion:
// after normalization, the consolidated 0001 file must be byte-for-byte equal to
// the in-order concatenation of the chain up-migrations.
func TestDriftGuard_ConsolidatedEqualsChain(t *testing.T) {
	consolidated, chain := loadBothSources(t)

	normC := normalizeSQL(consolidated)
	normChain := normalizeSQL(chain)

	if normC == normChain {
		return
	}

	// Produce a focused diff hint so a future failure is actionable.
	cTokens := strings.Split(normC, " ")
	chTokens := strings.Split(normChain, " ")
	min := len(cTokens)
	if len(chTokens) < min {
		min = len(chTokens)
	}
	firstDiff := -1
	for i := 0; i < min; i++ {
		if cTokens[i] != chTokens[i] {
			firstDiff = i
			break
		}
	}
	window := func(toks []string, i int) string {
		lo := i - 6
		if lo < 0 {
			lo = 0
		}
		hi := i + 6
		if hi > len(toks) {
			hi = len(toks)
		}
		return strings.Join(toks[lo:hi], " ")
	}
	hint := "(no token-level diff found; likely a trailing length difference)"
	if firstDiff >= 0 {
		hint = "first divergence near token " + itoa(firstDiff) + ":\n" +
			"  0001 : ..." + window(cTokens, firstDiff) + "...\n" +
			"  chain: ..." + window(chTokens, firstDiff) + "..."
	}
	t.Fatalf("HXC-1639 DRIFT: 0001_primary_schema.sql is NOT the concatenation of the "+
		"001..015 chain.\nRegenerate with: python3 migrations/postgresql/.gen_schema.py\n"+
		"0001 tokens=%d, chain tokens=%d\n%s",
		len(cTokens), len(chTokens), hint)
}

// itoa avoids pulling strconv just for the diagnostic above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestDriftGuard_SameTableSet asserts both sources create exactly the same set of
// tables (this is the assertion the task calls out specifically: 0001 must never be
// missing a table the chain creates, and vice versa).
func TestDriftGuard_SameTableSet(t *testing.T) {
	consolidated, chain := loadBothSources(t)
	// Match real tables and partitions: "create table [if not exists] <name>".
	pat := `create table (?:if not exists )?([a-z0-9_]+)`
	cTables := extractMatches(t, normalizeSQL(consolidated), pat)
	chTables := extractMatches(t, normalizeSQL(chain), pat)

	if strings.Join(cTables, ",") != strings.Join(chTables, ",") {
		t.Errorf("table set differs between 0001 and chain.\n0001 : %v\nchain: %v", cTables, chTables)
	}

	// Every RequiredTable must be present in BOTH sources.
	for _, want := range schema.RequiredTables {
		if !contains(cTables, want) {
			t.Errorf("RequiredTable %q missing from 0001_primary_schema.sql", want)
		}
		if !contains(chTables, want) {
			t.Errorf("RequiredTable %q missing from migrate chain", want)
		}
	}
}

// TestDriftGuard_SameIndexSet asserts identical index coverage across both sources.
func TestDriftGuard_SameIndexSet(t *testing.T) {
	consolidated, chain := loadBothSources(t)
	pat := `create index (?:if not exists )?([a-z0-9_]+)`
	cIdx := extractMatches(t, normalizeSQL(consolidated), pat)
	chIdx := extractMatches(t, normalizeSQL(chain), pat)
	if strings.Join(cIdx, ",") != strings.Join(chIdx, ",") {
		t.Errorf("index set differs.\n0001 : %v\nchain: %v", cIdx, chIdx)
	}
}

// TestDriftGuard_SameTriggerSet asserts identical trigger names across both sources.
func TestDriftGuard_SameTriggerSet(t *testing.T) {
	consolidated, chain := loadBothSources(t)
	pat := `create trigger ([a-z0-9_]+)`
	cTrig := extractMatches(t, normalizeSQL(consolidated), pat)
	chTrig := extractMatches(t, normalizeSQL(chain), pat)
	if strings.Join(cTrig, ",") != strings.Join(chTrig, ",") {
		t.Errorf("trigger set differs.\n0001 : %v\nchain: %v", cTrig, chTrig)
	}
}

// TestDriftGuard_SameFunctionSet asserts identical trigger-function names.
func TestDriftGuard_SameFunctionSet(t *testing.T) {
	consolidated, chain := loadBothSources(t)
	pat := `create or replace function ([a-z0-9_]+)\(`
	cFn := extractMatches(t, normalizeSQL(consolidated), pat)
	chFn := extractMatches(t, normalizeSQL(chain), pat)
	if strings.Join(cFn, ",") != strings.Join(chFn, ",") {
		t.Errorf("function set differs.\n0001 : %v\nchain: %v", cFn, chFn)
	}
}

// TestDriftGuard_SameCheckConstraintSet asserts identical CHECK constraint names.
func TestDriftGuard_SameCheckConstraintSet(t *testing.T) {
	consolidated, chain := loadBothSources(t)
	pat := `constraint ([a-z0-9_]+)\s+check`
	cChk := extractMatches(t, normalizeSQL(consolidated), pat)
	chChk := extractMatches(t, normalizeSQL(chain), pat)
	if strings.Join(cChk, ",") != strings.Join(chChk, ",") {
		t.Errorf("CHECK constraint set differs.\n0001 : %v\nchain: %v", cChk, chChk)
	}
	if len(cChk) == 0 {
		t.Error("no CHECK constraints found — schema appears to have lost its constraints")
	}
}

// TestDriftGuard_MutationDetectsDivergence is a §1.1 mutation/bite proof: if the
// chain gains a table that 0001 lacks, the equality + table-set guards MUST fail.
// We simulate by appending a CREATE TABLE to a copy of the chain text and checking
// the comparison logic catches it (operating on strings only — no file writes).
func TestDriftGuard_MutationDetectsDivergence(t *testing.T) {
	consolidated, chain := loadBothSources(t)

	mutatedChain := chain + "\nCREATE TABLE drift_canary (id UUID PRIMARY KEY);\n"

	if normalizeSQL(consolidated) == normalizeSQL(mutatedChain) {
		t.Error("mutation: a divergent extra table in the chain was NOT detected by the equality guard — bluff")
	}

	pat := `create table (?:if not exists )?([a-z0-9_]+)`
	cTables := extractMatches(t, normalizeSQL(consolidated), pat)
	chTables := extractMatches(t, normalizeSQL(mutatedChain), pat)
	if strings.Join(cTables, ",") == strings.Join(chTables, ",") {
		t.Error("mutation: divergent table set was NOT detected by the table-set guard — bluff")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
