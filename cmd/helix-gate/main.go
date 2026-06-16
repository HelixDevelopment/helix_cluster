// Command helix-gate runs the Helix Cluster OS orphan-prevention gate suite and
// makes the five gate packages (covgate, archlint, etcdlint, qualitygate,
// phasegate) actually enforce against the real repository tree (HXC-940).
//
// Each sub-command does REAL work — it parses/measures the repo and returns a
// real pass/fail with a non-zero exit code on failure. There is no always-pass
// stub: a gate that cannot fail would be a CLAUDE-1 PASS-bluff.
//
// Usage:
//
//	helix-gate <gate> [flags]
//	helix-gate all                 # run every gate; non-zero if any fails
//
// Gates:
//
//	archlint    verify docs/ARCHITECTURE.md component map -> real packages
//	etcdlint    forbid etcd imports outside cell-local packages
//	covgate     enforce coverage threshold + mutation-pairing audit
//	qualitygate evaluate a KPI metric snapshot against the baseline table
//	phasegate   validate the phase-6 soak-gate DAG (acyclic, structural)
//	all         run all of the above
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HelixDevelopment/helix_cluster/pkg/archlint"
	"github.com/HelixDevelopment/helix_cluster/pkg/covgate"
	"github.com/HelixDevelopment/helix_cluster/pkg/etcdlint"
	"github.com/HelixDevelopment/helix_cluster/pkg/phasegate"
	"github.com/HelixDevelopment/helix_cluster/pkg/qualitygate"
)

// repoRoot resolves the repository root. It honours HELIX_REPO_ROOT when set
// (so the gate can be invoked from anywhere), otherwise it walks up from the
// current working directory until it finds go.mod.
func repoRoot() (string, error) {
	if r := os.Getenv("HELIX_REPO_ROOT"); r != "" {
		return r, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("helix-gate: could not locate go.mod above %q", dir)
		}
		dir = parent
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: helix-gate <archlint|etcdlint|covgate|qualitygate|phasegate|all> [args]")
		os.Exit(2)
	}

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "helix-gate: %v\n", err)
		os.Exit(2)
	}

	gate := os.Args[1]
	args := os.Args[2:]

	var failed bool
	switch gate {
	case "archlint":
		failed = !runArchlint(root)
	case "etcdlint":
		failed = !runEtcdlint(root)
	case "covgate":
		failed = !runCovgate(root, args)
	case "qualitygate":
		failed = !runQualitygate(root, args)
	case "phasegate":
		failed = !runPhasegate()
	case "all":
		// Run every gate; report each; fail if ANY fails.
		results := []struct {
			name string
			ok   bool
		}{
			{"archlint", runArchlint(root)},
			{"etcdlint", runEtcdlint(root)},
			{"covgate", runCovgate(root, args)},
			{"qualitygate", runQualitygate(root, args)},
			{"phasegate", runPhasegate()},
		}
		fmt.Println("=== helix-gate suite summary ===")
		for _, r := range results {
			status := "PASS"
			if !r.ok {
				status = "FAIL"
				failed = true
			}
			fmt.Printf("  %-12s %s\n", r.name, status)
		}
	default:
		fmt.Fprintf(os.Stderr, "helix-gate: unknown gate %q\n", gate)
		os.Exit(2)
	}

	if failed {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// archlint gate
// ---------------------------------------------------------------------------

func runArchlint(root string) bool {
	archDoc := filepath.Join(root, "docs", "ARCHITECTURE.md")
	data, err := os.ReadFile(archDoc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[archlint] FAIL: cannot read %s: %v\n", archDoc, err)
		return false
	}
	comps, err := archlint.ParseComponentMap(string(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[archlint] FAIL: parse component map: %v\n", err)
		return false
	}
	if err := archlint.VerifyPackages(root, comps); err != nil {
		fmt.Fprintf(os.Stderr, "[archlint] FAIL: %v\n", err)
		return false
	}
	fmt.Printf("[archlint] PASS: %d documented components all map to real packages\n", len(comps))
	return true
}

// ---------------------------------------------------------------------------
// etcdlint gate
// ---------------------------------------------------------------------------

// etcdAllowlist is the set of cell-local package clause names permitted to
// import etcd. It is derived from the packages that legitimately back the
// cell-local etcd integration (discovery/leader/etcd/node/backup). Any OTHER
// package that imports go.etcd.io/etcd is a cross-cell layering violation.
var etcdAllowlist = []string{"discovery", "leader", "etcd", "node", "backup"}

func runEtcdlint(root string) bool {
	var allViolations []etcdlint.Violation
	scanRoots := []string{"pkg", "internal", "cmd"}
	for _, sr := range scanRoots {
		dir := filepath.Join(root, sr)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		vs, err := etcdlint.CheckDir(dir, etcdAllowlist)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[etcdlint] FAIL: scanning %s: %v\n", sr, err)
			return false
		}
		allViolations = append(allViolations, vs...)
	}
	if len(allViolations) > 0 {
		fmt.Fprintf(os.Stderr, "[etcdlint] FAIL: %d etcd-import violation(s) outside cell-local allowlist %v:\n",
			len(allViolations), etcdAllowlist)
		for _, v := range allViolations {
			fmt.Fprintf(os.Stderr, "  %s (package %q) imports %s\n", v.File, v.Package, v.Import)
		}
		return false
	}
	fmt.Printf("[etcdlint] PASS: no etcd imports outside cell-local packages %v\n", etcdAllowlist)
	return true
}

// ---------------------------------------------------------------------------
// covgate gate
// ---------------------------------------------------------------------------

// runCovgate enforces two things:
//  1. mutation-pairing audit: every package named in the mutation catalog
//     (test/mutation/run_mutations.sh) is a real, present input — and the
//     reverse (the catalog must cover the packages it claims) is checked by
//     MissingMutationPairs over the catalog's own package set.
//  2. if a coverage profile is present (coverage.out, or the path given as the
//     first arg / COVERAGE_OUT env), the aggregate coverage must meet the
//     threshold (COVERAGE_THRESHOLD, default 80).
func runCovgate(root string, args []string) bool {
	ok := true

	// --- mutation-pairing audit ---
	catalogPath := filepath.Join(root, "test", "mutation", "run_mutations.sh")
	cases, err := loadMutationCatalog(catalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[covgate] FAIL: mutation catalog: %v\n", err)
		return false
	}
	if len(cases) == 0 {
		fmt.Fprintf(os.Stderr, "[covgate] FAIL: mutation catalog %s has no cases\n", catalogPath)
		return false
	}
	// Verify every catalog package resolves to a real directory on disk and has
	// a non-empty paired test regex — a catalog entry pointing at a missing
	// package or with no paired test is a PASS-bluff.
	var badCases []string
	pkgs := make([]string, 0, len(cases))
	for _, c := range cases {
		pkgs = append(pkgs, c.Pkg)
		// An empty (or "./"-only) package path must NOT be accepted: it would
		// resolve via filepath.Join(root, "") to the repo root itself, which is
		// a directory, and silently pass the audit as a "real package". That is
		// a PASS-bluff (CLAUDE-1) — a catalog entry that pins NO package.
		rel := strings.TrimPrefix(c.Pkg, "./")
		if strings.TrimSpace(rel) == "" {
			badCases = append(badCases, fmt.Sprintf("%s -> empty package path", c.ID))
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(rel))
		info, statErr := os.Stat(dir)
		if statErr != nil || !info.IsDir() {
			badCases = append(badCases, fmt.Sprintf("%s -> missing package %s", c.ID, c.Pkg))
			continue
		}
		if strings.TrimSpace(c.TestRegex) == "" {
			badCases = append(badCases, fmt.Sprintf("%s -> empty paired test regex", c.ID))
		}
	}
	// MissingMutationPairs over the catalog's own package set must be empty
	// (sanity: each tested package has a catalog entry).
	missing := covgate.MissingMutationPairs(pkgs, cases)
	if len(missing) > 0 {
		badCases = append(badCases, fmt.Sprintf("packages without catalog pair: %v", missing))
	}
	if len(badCases) > 0 {
		fmt.Fprintf(os.Stderr, "[covgate] FAIL: mutation-pairing audit found %d problem(s):\n", len(badCases))
		for _, b := range badCases {
			fmt.Fprintf(os.Stderr, "  %s\n", b)
		}
		ok = false
	} else {
		fmt.Printf("[covgate] PASS: %d mutation cases all map to real packages with paired tests\n", len(cases))
	}

	// --- coverage threshold (only when a profile is available) ---
	profilePath := ""
	if len(args) > 0 && args[0] != "" {
		profilePath = args[0]
	} else if env := os.Getenv("COVERAGE_OUT"); env != "" {
		profilePath = env
	} else {
		def := filepath.Join(root, "coverage.out")
		if _, statErr := os.Stat(def); statErr == nil {
			profilePath = def
		}
	}
	if profilePath == "" {
		fmt.Println("[covgate] SKIP coverage-threshold: no coverage profile present " +
			"(set COVERAGE_OUT or pass a path / run `go test -coverprofile=coverage.out ./...`)")
		return ok
	}
	f, err := os.Open(filepath.Clean(profilePath)) //gosec:disable G703 -- profilePath comes from an operator CLI arg / COVERAGE_OUT env / committed default; trusted operator input in a local gate tool
	if err != nil {
		fmt.Fprintf(os.Stderr, "[covgate] FAIL: open coverage profile %s: %v\n", profilePath, err)
		return false
	}
	defer f.Close()
	report, err := covgate.ParseCoverProfile(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[covgate] FAIL: parse coverage profile: %v\n", err)
		return false
	}
	threshold := envFloat("COVERAGE_THRESHOLD", 80.0)
	if !report.MeetsThreshold(threshold) {
		fmt.Fprintf(os.Stderr, "[covgate] FAIL: aggregate coverage %.2f%% < threshold %.2f%%\n",
			report.Aggregate, threshold)
		return false
	}
	fmt.Printf("[covgate] PASS: aggregate coverage %.2f%% >= threshold %.2f%%\n",
		report.Aggregate, threshold)
	return ok
}

// loadMutationCatalog reads run_mutations.sh, extracts the quoted pipe-separated
// CASES array entries, strips the surrounding quotes, and parses them with
// covgate.ParseMutationCatalog.
func loadMutationCatalog(path string) ([]covgate.MutationCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		// Catalog entries are the quoted lines that contain pipe separators.
		if !strings.HasPrefix(t, `"`) || !strings.Contains(t, "|") {
			continue
		}
		t = strings.Trim(t, `"`)
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return covgate.ParseMutationCatalog(strings.NewReader(b.String()))
}

// ---------------------------------------------------------------------------
// qualitygate gate
// ---------------------------------------------------------------------------

// runQualitygate evaluates a KPI metric snapshot against the baseline rule
// table. The snapshot is read from a JSON file: the path given as the first
// arg, the QUALITYGATE_METRICS env var, or the committed baseline at
// test/qualitygate/baseline_metrics.json. The gate FAILS on any CRITICAL
// breach.
func runQualitygate(root string, args []string) bool {
	metricsPath := ""
	if len(args) > 0 && args[0] != "" {
		metricsPath = args[0]
	} else if env := os.Getenv("QUALITYGATE_METRICS"); env != "" {
		metricsPath = env
	} else {
		metricsPath = filepath.Join(root, "test", "qualitygate", "baseline_metrics.json")
	}

	data, err := os.ReadFile(filepath.Clean(metricsPath)) //gosec:disable G703 -- metricsPath comes from an operator CLI arg / QUALITYGATE_METRICS env / committed default; trusted operator input in a local gate tool
	if err != nil {
		fmt.Fprintf(os.Stderr, "[qualitygate] FAIL: read metrics snapshot %s: %v\n", metricsPath, err)
		return false
	}
	var metrics map[string]float64
	if err := json.Unmarshal(data, &metrics); err != nil {
		fmt.Fprintf(os.Stderr, "[qualitygate] FAIL: parse metrics JSON %s: %v\n", metricsPath, err)
		return false
	}

	report := qualitygate.Validate(metrics)
	if !report.Pass {
		fmt.Fprintf(os.Stderr, "[qualitygate] FAIL (run %s): %d violation(s):\n", report.RunUUID, len(report.Violations))
		for _, v := range report.Violations {
			fmt.Fprintf(os.Stderr, "  [%s] %s %s %.3f (actual %.3f)\n",
				v.Severity, v.Metric, v.Comparator, v.Threshold, v.Actual)
		}
		return false
	}
	// Report any non-blocking WARNING violations for visibility.
	for _, v := range report.Violations {
		fmt.Printf("[qualitygate] WARNING: %s %s %.3f (actual %.3f)\n",
			v.Metric, v.Comparator, v.Threshold, v.Actual)
	}
	fmt.Printf("[qualitygate] PASS (run %s): %d KPIs evaluated, no CRITICAL breach\n",
		report.RunUUID, len(report.EvaluatedKPIs))
	return true
}

// ---------------------------------------------------------------------------
// phasegate gate
// ---------------------------------------------------------------------------

// runPhasegate validates the canonical phase-6 soak-gate DAG: it must be
// structurally valid (each gate + condition well-formed), acyclic, and produce
// a deterministic topological order covering every gate. A regression that
// introduced a cycle, dangling dependency, or malformed condition would fail
// here.
func runPhasegate() bool {
	gates := phasegate.DefaultPhase6Gates()
	graph, err := phasegate.NewGateGraph(gates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[phasegate] FAIL: gate graph invalid: %v\n", err)
		return false
	}
	order, err := graph.TopoOrder()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[phasegate] FAIL: topological order: %v\n", err)
		return false
	}
	if len(order) != len(gates) {
		fmt.Fprintf(os.Stderr, "[phasegate] FAIL: topo order covers %d of %d gates\n", len(order), len(gates))
		return false
	}
	ids := make([]string, len(gates))
	for i, g := range gates {
		ids[i] = g.ID
	}
	sort.Strings(ids)
	fmt.Printf("[phasegate] PASS: %d-gate DAG is acyclic; topo order %v (gates %v)\n",
		len(gates), order, ids)
	return true
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var f float64
	if _, err := fmt.Sscanf(v, "%g", &f); err != nil {
		return def
	}
	return f
}
