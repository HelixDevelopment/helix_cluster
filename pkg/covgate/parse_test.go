package covgate

import (
	"strings"
	"testing"
)

// multiPkgProfile is a realistic coverprofile with two packages and mixed
// covered/uncovered blocks.  Statement counts are chosen so that the expected
// percentages are exact integers, making the assertions unambiguous.
//
// Package breakdown (pkg = path.Dir of the file path):
//
//	github.com/example/proj/pkg/alpha  — file alpha.go
//	  block1: 4 stmts, count=1  (covered)
//	  block2: 1 stmt,  count=0  (not covered)
//	  => 4/5 = 80.0%
//
//	github.com/example/proj/pkg/beta   — file beta.go
//	  block1: 3 stmts, count=2  (covered)
//	  block2: 3 stmts, count=0  (not covered)
//	  => 3/6 = 50.0%
//
//	Aggregate: (4+3)/(5+6) = 7/11 ≈ 63.636…%
const multiPkgProfile = `mode: count
github.com/example/proj/pkg/alpha/alpha.go:10.1,14.2 4 1
github.com/example/proj/pkg/alpha/alpha.go:16.1,16.2 1 0
github.com/example/proj/pkg/beta/beta.go:5.1,7.2 3 2
github.com/example/proj/pkg/beta/beta.go:9.1,11.2 3 0
`

func TestParseCoverProfile_MultiPkg(t *testing.T) {
	report, err := ParseCoverProfile(strings.NewReader(multiPkgProfile))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mode
	if report.Mode != "count" {
		t.Errorf("Mode = %q, want %q", report.Mode, "count")
	}

	// Statement counts — computed from parsing, not hardcoded totals.
	wantTotal := 11 // 4+1+3+3
	wantCovered := 7 // 4+3
	if report.TotalStatements != wantTotal {
		t.Errorf("TotalStatements = %d, want %d", report.TotalStatements, wantTotal)
	}
	if report.CoveredStatements != wantCovered {
		t.Errorf("CoveredStatements = %d, want %d", report.CoveredStatements, wantCovered)
	}

	// Aggregate must be derived from the parsed counts, not hardcoded.
	wantAggregate := 100.0 * float64(wantCovered) / float64(wantTotal)
	if report.Aggregate != wantAggregate {
		t.Errorf("Aggregate = %.4f, want %.4f", report.Aggregate, wantAggregate)
	}

	// Per-package coverage.
	alphaPkg := "github.com/example/proj/pkg/alpha"
	betaPkg := "github.com/example/proj/pkg/beta"

	wantAlpha := 100.0 * 4.0 / 5.0 // 80.0
	wantBeta := 100.0 * 3.0 / 6.0  // 50.0

	if got := report.PackageCoverage(alphaPkg); got != wantAlpha {
		t.Errorf("PackageCoverage(%q) = %.4f, want %.4f", alphaPkg, got, wantAlpha)
	}
	if got := report.PackageCoverage(betaPkg); got != wantBeta {
		t.Errorf("PackageCoverage(%q) = %.4f, want %.4f", betaPkg, got, wantBeta)
	}

	// Unknown package returns 0.
	if got := report.PackageCoverage("github.com/example/proj/pkg/gamma"); got != 0.0 {
		t.Errorf("PackageCoverage(unknown) = %.4f, want 0.0", got)
	}
}

func TestParseCoverProfile_EmptyModeOnly(t *testing.T) {
	report, err := ParseCoverProfile(strings.NewReader("mode: set\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Mode != "set" {
		t.Errorf("Mode = %q, want %q", report.Mode, "set")
	}
	if report.TotalStatements != 0 {
		t.Errorf("TotalStatements = %d, want 0", report.TotalStatements)
	}
	if report.CoveredStatements != 0 {
		t.Errorf("CoveredStatements = %d, want 0", report.CoveredStatements)
	}
	// Empty profile => 100% by convention.
	if report.Aggregate != 100.0 {
		t.Errorf("Aggregate = %.4f, want 100.0", report.Aggregate)
	}
}

func TestParseCoverProfile_MissingModeLine_Error(t *testing.T) {
	// Input has no mode line at all — must error.
	_, err := ParseCoverProfile(strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

func TestParseCoverProfile_InvalidModeLine_Error(t *testing.T) {
	// First line is not "mode: ..." — must error.
	_, err := ParseCoverProfile(strings.NewReader("github.com/x/y/z.go:1.1,2.2 1 0\n"))
	if err == nil {
		t.Fatal("expected error for missing mode: prefix, got nil")
	}
}

func TestParseCoverProfile_MalformedDataLine_Error(t *testing.T) {
	input := "mode: set\ngithub.com/x/y/z.go 1 0\n" // missing colon in block spec field
	_, err := ParseCoverProfile(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for malformed data line, got nil")
	}
}

// TestMeetsThreshold_Pass verifies that a report at ~63.6% passes a 60.0% threshold.
func TestMeetsThreshold_Pass(t *testing.T) {
	report, err := ParseCoverProfile(strings.NewReader(multiPkgProfile))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Aggregate is 7/11 ≈ 63.636%, which is >= 60.0.
	if !report.MeetsThreshold(60.0) {
		t.Errorf("MeetsThreshold(60.0) = false, want true (Aggregate=%.4f)", report.Aggregate)
	}
}

// TestMeetsThreshold_Fail is the FAIL case: same report does NOT pass 70.0%.
// This test would pass if MeetsThreshold always returned true — that would be
// a PASS-bluff, caught because this assertion expects false.
func TestMeetsThreshold_Fail(t *testing.T) {
	report, err := ParseCoverProfile(strings.NewReader(multiPkgProfile))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Aggregate ≈ 63.636%, which is < 70.0.
	if report.MeetsThreshold(70.0) {
		t.Errorf("MeetsThreshold(70.0) = true, want false (Aggregate=%.4f)", report.Aggregate)
	}
}

// lowCoverageProfile: one package with 45% coverage.
//
//	pkg/low/low.go: 9 stmts covered out of 20 = 45%
const lowCoverageProfile = `mode: set
github.com/example/proj/pkg/low/low.go:1.1,5.2 9 1
github.com/example/proj/pkg/low/low.go:6.1,10.2 11 0
`

func TestMeetsThreshold_45Pct_Fails60(t *testing.T) {
	report, err := ParseCoverProfile(strings.NewReader(lowCoverageProfile))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 9/20 = 45%, must fail the 60% threshold.
	if report.MeetsThreshold(60.0) {
		t.Errorf("MeetsThreshold(60.0) = true for 45%% report, want false (Aggregate=%.4f)", report.Aggregate)
	}
}

// TestShortfalls verifies that packages below the threshold under a prefix are returned.
func TestShortfalls(t *testing.T) {
	report, err := ParseCoverProfile(strings.NewReader(multiPkgProfile))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// alpha=80%, beta=50%. Threshold 60% => beta is a shortfall.
	prefix := "github.com/example/proj/pkg"
	shortfalls := report.Shortfalls(prefix, 60.0)

	betaPkg := "github.com/example/proj/pkg/beta"
	if len(shortfalls) != 1 || shortfalls[0] != betaPkg {
		t.Errorf("Shortfalls(%q, 60.0) = %v, want [%q]", prefix, shortfalls, betaPkg)
	}
}

// TestShortfalls_NoneBelow verifies empty result when all packages are above threshold.
func TestShortfalls_NoneBelow(t *testing.T) {
	report, err := ParseCoverProfile(strings.NewReader(multiPkgProfile))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	shortfalls := report.Shortfalls("github.com/example/proj/pkg", 40.0)
	if len(shortfalls) != 0 {
		t.Errorf("Shortfalls with threshold 40%% = %v, want empty", shortfalls)
	}
}

// TestShortfalls_PrefixFiltering verifies that packages not matching the prefix
// are excluded even if they are below the threshold.
func TestShortfalls_PrefixFiltering(t *testing.T) {
	report, err := ParseCoverProfile(strings.NewReader(multiPkgProfile))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Use a prefix that matches neither package.
	shortfalls := report.Shortfalls("github.com/other", 10.0)
	if len(shortfalls) != 0 {
		t.Errorf("Shortfalls with non-matching prefix = %v, want empty", shortfalls)
	}
}
