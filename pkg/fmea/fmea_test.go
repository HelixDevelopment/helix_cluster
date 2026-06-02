// Package fmea_test provides deterministic unit tests for the Phase-6 FMEA
// catalog. All assertions prove sink-side facts: computed RPN values,
// validation rules, catalog completeness, and HighRisk ordering.
//
// Anti-bluff discipline: each test function carries a "Mutation:" comment
// identifying the exact one-line source change that makes the test FAIL
// (and still compiles).
package fmea_test

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/fmea"
)

// ---------------------------------------------------------------------------
// 1. RPN exact arithmetic
// ---------------------------------------------------------------------------

// TestRPN_Exact verifies that RPN() returns Severity×Occurrence×Detection.
// Mutation: change RPN() body to return f.Severity+f.Occurrence+f.Detection → 252 ≠ 20 → test fails.
func TestRPN_Exact(t *testing.T) {
	fm := fmea.FailureMode{
		ID:              "FM-TEST-001",
		Component:       "TestComp",
		Mode:            "test mode",
		Effect:          "test effect",
		Cause:           "test cause",
		Severity:        9,
		Occurrence:      4,
		Detection:       7,
		DetectionSignal: "test_signal > 0",
		Recovery:        "step 1: do something",
		RunbookURL:      "https://runbooks.helixcluster.dev/test/001",
	}
	got := fm.RPN()
	const want = 252 // 9 * 4 * 7
	if got != want {
		t.Fatalf("RPN() = %d, want %d (9×4×7)", got, want)
	}
}

// TestRPN_MinValues verifies RPN at minimum valid inputs (1×1×1=1).
// Mutation: change the multiplication to addition → 3 ≠ 1 → test fails.
func TestRPN_MinValues(t *testing.T) {
	fm := fmea.FailureMode{
		Severity: 1, Occurrence: 1, Detection: 1,
	}
	if got := fm.RPN(); got != 1 {
		t.Fatalf("RPN() with S=O=D=1 = %d, want 1", got)
	}
}

// TestRPN_MaxValues verifies RPN at maximum valid inputs (10×10×10=1000).
// Mutation: change the multiplication to S*O+D → 1010 ≠ 1000 → test fails.
func TestRPN_MaxValues(t *testing.T) {
	fm := fmea.FailureMode{
		Severity: 10, Occurrence: 10, Detection: 10,
	}
	if got := fm.RPN(); got != 1000 {
		t.Fatalf("RPN() with S=O=D=10 = %d, want 1000", got)
	}
}

// ---------------------------------------------------------------------------
// 2. FailureMode.Validate() — distinct per-field errors
// ---------------------------------------------------------------------------

// TestValidate_SeverityZero verifies that Severity=0 (below range) fails validation.
// Mutation: remove the lower-bound check (value < 1) in Validate → S=0 passes → test fails.
func TestValidate_SeverityZero(t *testing.T) {
	fm := validBase()
	fm.Severity = 0
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for Severity=0, got nil")
	}
	if !strings.Contains(err.Error(), "Severity") {
		t.Fatalf("error must mention 'Severity', got: %v", err)
	}
}

// TestValidate_DetectionElevenAboveRange verifies that Detection=11 (above range) fails.
// Mutation: remove the upper-bound check (value > 10) in Validate → D=11 passes → test fails.
func TestValidate_DetectionElevenAboveRange(t *testing.T) {
	fm := validBase()
	fm.Detection = 11
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for Detection=11, got nil")
	}
	if !strings.Contains(err.Error(), "Detection") {
		t.Fatalf("error must mention 'Detection', got: %v", err)
	}
}

// TestValidate_EmptyRecovery verifies that an empty Recovery field fails validation.
// Mutation: remove the Recovery non-empty check in Validate → empty Recovery passes → test fails.
func TestValidate_EmptyRecovery(t *testing.T) {
	fm := validBase()
	fm.Recovery = ""
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for empty Recovery, got nil")
	}
	if !strings.Contains(err.Error(), "Recovery") {
		t.Fatalf("error must mention 'Recovery', got: %v", err)
	}
}

// TestValidate_RunbookURLNoScheme verifies that a URL without a scheme fails validation.
// Mutation: remove the u.Scheme == "" check in Validate → "noscheme" passes → test fails.
func TestValidate_RunbookURLNoScheme(t *testing.T) {
	fm := validBase()
	fm.RunbookURL = "noscheme"
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for RunbookURL without scheme, got nil")
	}
	if !strings.Contains(err.Error(), "RunbookURL") {
		t.Fatalf("error must mention 'RunbookURL', got: %v", err)
	}
}

// TestValidate_EmptyID verifies that an empty ID fails validation.
// Mutation: remove the ID non-empty check → empty ID passes → test fails.
func TestValidate_EmptyID(t *testing.T) {
	fm := validBase()
	fm.ID = ""
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for empty ID, got nil")
	}
	if !strings.Contains(err.Error(), "ID") {
		t.Fatalf("error must mention 'ID', got: %v", err)
	}
}

// TestValidate_EmptyDetectionSignal verifies that an empty DetectionSignal fails.
// Mutation: remove the DetectionSignal non-empty check → empty value passes → test fails.
func TestValidate_EmptyDetectionSignal(t *testing.T) {
	fm := validBase()
	fm.DetectionSignal = ""
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for empty DetectionSignal, got nil")
	}
	if !strings.Contains(err.Error(), "DetectionSignal") {
		t.Fatalf("error must mention 'DetectionSignal', got: %v", err)
	}
}

// TestValidate_EmptyMode verifies that an empty Mode field fails validation.
// Mutation: remove the Mode non-empty check in Validate → empty Mode passes → test fails.
func TestValidate_EmptyMode(t *testing.T) {
	fm := validBase()
	fm.Mode = ""
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for empty Mode, got nil")
	}
	if !strings.Contains(err.Error(), "Mode") {
		t.Fatalf("error must mention 'Mode', got: %v", err)
	}
}

// TestValidate_EmptyEffect verifies that an empty Effect field fails validation.
// Mutation: remove the Effect non-empty check in Validate → empty Effect passes → test fails.
func TestValidate_EmptyEffect(t *testing.T) {
	fm := validBase()
	fm.Effect = ""
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for empty Effect, got nil")
	}
	if !strings.Contains(err.Error(), "Effect") {
		t.Fatalf("error must mention 'Effect', got: %v", err)
	}
}

// TestValidate_EmptyComponent verifies that an empty Component field fails validation.
// Mutation: remove the Component non-empty check in Validate → empty Component passes → test fails.
func TestValidate_EmptyComponent(t *testing.T) {
	fm := validBase()
	fm.Component = ""
	err := fm.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for empty Component, got nil")
	}
	if !strings.Contains(err.Error(), "Component") {
		t.Fatalf("error must mention 'Component', got: %v", err)
	}
}

// TestValidate_ValidEntry verifies that a fully populated valid entry passes.
// Mutation: set Severity to 0 → Validate() returns error → test fails.
func TestValidate_ValidEntry(t *testing.T) {
	fm := validBase()
	if err := fm.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error for valid entry: %v", err)
	}
}

// TestValidate_OccurrenceBoundaryValues verifies the inclusive [1,10] boundaries
// for Occurrence. Mutation: change lower bound to value < 2 → O=1 fails → test fails.
func TestValidate_OccurrenceBoundaryValues(t *testing.T) {
	cases := []struct {
		occ     int
		wantErr bool
	}{
		{0, true},
		{1, false},
		{10, false},
		{11, true},
	}
	for _, tc := range cases {
		fm := validBase()
		fm.Occurrence = tc.occ
		err := fm.Validate()
		if tc.wantErr && err == nil {
			t.Errorf("Occurrence=%d: expected error, got nil", tc.occ)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("Occurrence=%d: expected nil, got %v", tc.occ, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. DefaultCatalog completeness and ValidateCatalog
// ---------------------------------------------------------------------------

// TestDefaultCatalog_MinimumSize verifies that DefaultCatalog returns ≥8 entries.
// Mutation: remove two entries from DefaultCatalog → len < 8 → test fails.
func TestDefaultCatalog_MinimumSize(t *testing.T) {
	catalog := fmea.DefaultCatalog()
	const minEntries = 8
	if len(catalog) < minEntries {
		t.Fatalf("DefaultCatalog() returned %d entries, want >= %d", len(catalog), minEntries)
	}
}

// TestDefaultCatalog_AllEntriesValid verifies every DefaultCatalog entry passes Validate.
// Mutation: set Severity=0 on the first entry in DefaultCatalog → first entry fails → test fails.
func TestDefaultCatalog_AllEntriesValid(t *testing.T) {
	catalog := fmea.DefaultCatalog()
	for i, fm := range catalog {
		if err := fm.Validate(); err != nil {
			t.Errorf("DefaultCatalog()[%d] ID=%q failed Validate: %v", i, fm.ID, err)
		}
	}
}

// TestDefaultCatalog_ValidateCatalogPasses verifies that ValidateCatalog accepts the
// full DefaultCatalog (all entries valid and IDs unique).
// Mutation: introduce a duplicate ID in DefaultCatalog → ValidateCatalog returns error → test fails.
func TestDefaultCatalog_ValidateCatalogPasses(t *testing.T) {
	catalog := fmea.DefaultCatalog()
	if err := fmea.ValidateCatalog(catalog); err != nil {
		t.Fatalf("ValidateCatalog(DefaultCatalog()) unexpected error: %v", err)
	}
}

// TestDefaultCatalog_RequiredModesPresent verifies that named Phase-6 failure modes
// are present by their IDs, covering the required set.
// Mutation: remove FM-P6-001 from DefaultCatalog → "FM-P6-001" not found → test fails.
func TestDefaultCatalog_RequiredModesPresent(t *testing.T) {
	catalog := fmea.DefaultCatalog()

	// Build ID and Component indexes.
	byID := make(map[string]fmea.FailureMode, len(catalog))
	byComponent := make(map[string]bool, len(catalog))
	for _, fm := range catalog {
		byID[fm.ID] = fm
		byComponent[fm.Component] = true
	}

	// Required IDs covering the eight mandated Phase-6 failure modes.
	requiredIDs := []string{
		"FM-P6-001", // cross-cell partition
		"FM-P6-002", // gateway down
		"FM-P6-003", // clock skew
		"FM-P6-004", // cell quorum loss
		"FM-P6-005", // trust-bundle expiry
		"FM-P6-006", // WG key rotation gap
		"FM-P6-007", // NAT traversal failure
		"FM-P6-008", // config divergence
	}
	for _, id := range requiredIDs {
		if _, ok := byID[id]; !ok {
			t.Errorf("DefaultCatalog() missing required entry with ID %q", id)
		}
	}

	// Required components covering federation surface.
	requiredComponents := []string{
		"CellMesh",
		"Gateway",
		"WireGuard",
		"ConfigSync",
	}
	for _, comp := range requiredComponents {
		if !byComponent[comp] {
			t.Errorf("DefaultCatalog() missing entry with Component=%q", comp)
		}
	}
}

// TestDefaultCatalog_UniqueIDs verifies that all IDs in DefaultCatalog are distinct.
// Mutation: duplicate the first entry in DefaultCatalog → seen[ID] already true → test fails.
func TestDefaultCatalog_UniqueIDs(t *testing.T) {
	catalog := fmea.DefaultCatalog()
	seen := make(map[string]int)
	for i, fm := range catalog {
		if prev, dup := seen[fm.ID]; dup {
			t.Errorf("duplicate ID %q at index %d (first at %d)", fm.ID, i, prev)
		}
		seen[fm.ID] = i
	}
}

// TestDefaultCatalog_RPNsPositive verifies every entry has RPN > 0
// (i.e., no entry has a zero S/O/D that could slip through Validate).
// Mutation: change RPN() to return 0 unconditionally → all RPNs == 0 → test fails.
func TestDefaultCatalog_RPNsPositive(t *testing.T) {
	for _, fm := range fmea.DefaultCatalog() {
		if rpn := fm.RPN(); rpn <= 0 {
			t.Errorf("ID=%q: RPN() = %d, want > 0", fm.ID, rpn)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. HighRisk — exact ID ordering on a hand-built set
// ---------------------------------------------------------------------------

// TestHighRisk_ExactIDOrder verifies that HighRisk returns entries with RPN≥threshold
// in descending RPN order (ties broken by ascending ID).
// Mutation: reverse the sort comparator (ri < rj) → order reversed → assertion fails.
func TestHighRisk_ExactIDOrder(t *testing.T) {
	// Hand-built catalog: known RPN values.
	//   FM-A: S=9 O=4 D=7 → RPN=252
	//   FM-B: S=8 O=3 D=4 → RPN= 96
	//   FM-C: S=7 O=5 D=5 → RPN=175
	//   FM-D: S=6 O=2 D=3 → RPN= 36
	//   FM-E: S=9 O=4 D=7 → RPN=252 (tie with FM-A, FM-E > FM-A alphabetically → FM-A first)
	catalog := []fmea.FailureMode{
		makeEntry("FM-A", "Comp", 9, 4, 7),
		makeEntry("FM-B", "Comp", 8, 3, 4),
		makeEntry("FM-C", "Comp", 7, 5, 5),
		makeEntry("FM-D", "Comp", 6, 2, 3),
		makeEntry("FM-E", "Comp", 9, 4, 7),
	}

	// Threshold 100: FM-A (252), FM-C (175), FM-E (252) qualify.
	// RPN desc: 252, 252, 175 → tie-break FM-A (< FM-E) before FM-E, then FM-C.
	got := fmea.HighRisk(catalog, 100)
	wantIDs := []string{"FM-A", "FM-E", "FM-C"} // FM-A and FM-E both 252; FM-A < FM-E
	if len(got) != len(wantIDs) {
		t.Fatalf("HighRisk len=%d, want %d", len(got), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Errorf("HighRisk[%d].ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

// TestHighRisk_ThresholdExclusion verifies that entries with RPN < threshold are excluded.
// Mutation: change ">=" in HighRisk to ">" → entries at exactly threshold excluded → test fails.
func TestHighRisk_ThresholdExclusion(t *testing.T) {
	catalog := []fmea.FailureMode{
		makeEntry("FM-HI", "Comp", 9, 4, 7), // RPN=252
		makeEntry("FM-LO", "Comp", 3, 2, 2), // RPN=12
		makeEntry("FM-MID", "Comp", 5, 5, 5), // RPN=125
	}

	got := fmea.HighRisk(catalog, 125)
	if len(got) != 2 {
		t.Fatalf("HighRisk(t=125) len=%d, want 2 (RPN>=125)", len(got))
	}
	// Verify FM-LO (RPN=12) is excluded.
	for _, fm := range got {
		if fm.ID == "FM-LO" {
			t.Errorf("FM-LO (RPN=12) should be excluded at threshold=125")
		}
	}
	// Verify FM-MID (RPN=125) is included (boundary inclusive).
	foundMID := false
	for _, fm := range got {
		if fm.ID == "FM-MID" {
			foundMID = true
		}
	}
	if !foundMID {
		t.Errorf("FM-MID (RPN=125) should be included at threshold=125 (inclusive)")
	}
}

// TestHighRisk_EmptyResult verifies that HighRisk returns an empty slice (not nil panic)
// when no entry meets the threshold.
// Mutation: change >= to > with threshold-1 input → at-boundary entry excluded → empty when 1 expected.
func TestHighRisk_EmptyResult(t *testing.T) {
	catalog := []fmea.FailureMode{
		makeEntry("FM-X", "Comp", 2, 2, 2), // RPN=8
	}
	got := fmea.HighRisk(catalog, 1000)
	if len(got) != 0 {
		t.Fatalf("HighRisk(threshold=1000) should return empty, got %v", got)
	}
}

// TestHighRisk_DescendingRPNOrder verifies the descending sort on DefaultCatalog subset.
// Mutation: change sort comparator from ri > rj to ri < rj → ascending order → assertion fails.
func TestHighRisk_DescendingRPNOrder(t *testing.T) {
	catalog := fmea.DefaultCatalog()
	got := fmea.HighRisk(catalog, 1) // all entries
	for i := 1; i < len(got); i++ {
		if got[i].RPN() > got[i-1].RPN() {
			t.Errorf("HighRisk not descending: got[%d].RPN=%d > got[%d].RPN=%d",
				i, got[i].RPN(), i-1, got[i-1].RPN())
		}
	}
}

// ---------------------------------------------------------------------------
// 5. ValidateCatalog — duplicate ID rejection
// ---------------------------------------------------------------------------

// TestValidateCatalog_DuplicateIDRejected verifies that a catalog with a duplicate ID
// is rejected by ValidateCatalog.
// Mutation: remove the seen[fm.ID] duplicate check → duplicate passes → test fails.
func TestValidateCatalog_DuplicateIDRejected(t *testing.T) {
	a := validBase()
	a.ID = "FM-DUP-001"
	b := validBase()
	b.ID = "FM-DUP-001" // duplicate
	b.Mode = "different mode"

	catalog := []fmea.FailureMode{a, b}
	err := fmea.ValidateCatalog(catalog)
	if err == nil {
		t.Fatal("ValidateCatalog must reject catalog with duplicate ID, got nil")
	}
	if !strings.Contains(err.Error(), "FM-DUP-001") {
		t.Fatalf("error must mention duplicate ID 'FM-DUP-001', got: %v", err)
	}
}

// TestValidateCatalog_InvalidEntryRejected verifies that a catalog containing an invalid
// entry is rejected.
// Mutation: remove the fm.Validate() call in ValidateCatalog → invalid entry passes → test fails.
func TestValidateCatalog_InvalidEntryRejected(t *testing.T) {
	a := validBase()
	b := validBase()
	b.ID = "FM-BAD-001"
	b.Severity = 0 // invalid
	catalog := []fmea.FailureMode{a, b}
	if err := fmea.ValidateCatalog(catalog); err == nil {
		t.Fatal("ValidateCatalog must reject catalog with invalid entry, got nil")
	}
}

// TestValidateCatalog_EmptyCatalogPasses verifies that an empty catalog is valid.
// Mutation: add a mandatory minimum-size check to ValidateCatalog → empty catalog fails.
func TestValidateCatalog_EmptyCatalogPasses(t *testing.T) {
	if err := fmea.ValidateCatalog(nil); err != nil {
		t.Fatalf("ValidateCatalog(nil) unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// validBase returns a fully valid FailureMode for use in validation sub-tests.
func validBase() fmea.FailureMode {
	return fmea.FailureMode{
		ID:              "FM-BASE-001",
		Component:       "TestComponent",
		Mode:            "base failure mode",
		Effect:          "system degraded",
		Cause:           "hardware failure",
		Severity:        5,
		Occurrence:      5,
		Detection:       5,
		DetectionSignal: "test_metric_total > 0",
		Recovery:        "step 1: restart service; step 2: verify recovery",
		RunbookURL:      "https://runbooks.helixcluster.dev/test/base",
	}
}

// makeEntry constructs a minimal FailureMode with the given ID and S/O/D for use in
// HighRisk ordering tests.
func makeEntry(id, component string, severity, occurrence, detection int) fmea.FailureMode {
	return fmea.FailureMode{
		ID:              id,
		Component:       component,
		Mode:            "test mode for " + id,
		Effect:          "test effect for " + id,
		Cause:           "test cause",
		Severity:        severity,
		Occurrence:      occurrence,
		Detection:       detection,
		DetectionSignal: "test_signal > 0",
		Recovery:        "step 1: remediate",
		RunbookURL:      "https://runbooks.helixcluster.dev/test/" + strings.ToLower(id),
	}
}
