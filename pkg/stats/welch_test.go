// Package stats provides statistical utilities for HelixCluster OS.
package stats_test

import (
	"math"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/stats"
)

// oracleT is the independently computed t-statistic for the large-separation pair.
// baseline={100,101,99,100,102} vs candidate={130,131,129,132,130}
// Computed by scipy.stats.ttest_ind(..., equal_var=False): t=-41.6025, df=8.0, p=1.2276e-10.
const (
	oracleT  = -41.60251471689218
	oracleDF = 8.0
	oracleP  = 1.2275835846451172e-10
)

// TestWelchTTest_LargeSeparation asserts sink-side facts: t-stat, df, and p-value are within
// tolerance of independently computed oracle values, and Significant=true.
func TestWelchTTest_LargeSeparation(t *testing.T) {
	baseline := []float64{100, 101, 99, 100, 102}
	candidate := []float64{130, 131, 129, 132, 130}

	result, err := stats.WelchTTest(baseline, candidate)
	if err != nil {
		t.Fatalf("WelchTTest returned unexpected error: %v", err)
	}

	// TStat tolerance: within 0.001 of oracle (absolute)
	if math.Abs(result.TStat-oracleT) > 0.001 {
		t.Errorf("TStat: got %v, want ~%v (tol 0.001)", result.TStat, oracleT)
	}

	// DF tolerance: within 0.01
	if math.Abs(result.DF-oracleDF) > 0.01 {
		t.Errorf("DF: got %v, want ~%v (tol 0.01)", result.DF, oracleDF)
	}

	// PValue tolerance: within 1e-12 (oracle ~1.228e-10)
	if math.Abs(result.PValue-oracleP) > 1e-12 {
		t.Errorf("PValue: got %v, want ~%v (tol 1e-12)", result.PValue, oracleP)
	}

	// Significant must be true at alpha=0.05
	if !result.Significant {
		t.Errorf("expected Significant=true for large-separation pair, got false (PValue=%v)", result.PValue)
	}
}

// TestWelchTTest_NearIdentical asserts that a near-identical pair is NOT significant.
func TestWelchTTest_NearIdentical(t *testing.T) {
	// baseline and candidate virtually identical; p >> 0.05
	baseline := []float64{10.0, 11.0, 9.0, 10.0, 10.5}
	candidate := []float64{10.2, 10.8, 9.5, 10.3, 10.1}

	result, err := stats.WelchTTest(baseline, candidate)
	if err != nil {
		t.Fatalf("WelchTTest returned unexpected error: %v", err)
	}

	if result.Significant {
		t.Errorf("expected Significant=false for near-identical pair, got true (PValue=%v)", result.PValue)
	}

	// p-value should be well above 0.05
	if result.PValue < 0.05 {
		t.Errorf("expected PValue >= 0.05 for near-identical pair, got %v", result.PValue)
	}
}

// TestDetectRegression_LargeSeparation verifies DetectRegression flags the large-separation pair.
func TestDetectRegression_LargeSeparation(t *testing.T) {
	baseline := []float64{100, 101, 99, 100, 102}
	candidate := []float64{130, 131, 129, 132, 130}

	result, err := stats.DetectRegression(baseline, candidate, 0.05)
	if err != nil {
		t.Fatalf("DetectRegression returned unexpected error: %v", err)
	}
	if !result.Significant {
		t.Errorf("expected Significant=true, got false (PValue=%v)", result.PValue)
	}
}

// TestDetectRegression_NearIdentical verifies DetectRegression does NOT flag a near-identical pair.
func TestDetectRegression_NearIdentical(t *testing.T) {
	baseline := []float64{10.0, 11.0, 9.0, 10.0, 10.5}
	candidate := []float64{10.2, 10.8, 9.5, 10.3, 10.1}

	result, err := stats.DetectRegression(baseline, candidate, 0.05)
	if err != nil {
		t.Fatalf("DetectRegression returned unexpected error: %v", err)
	}
	if result.Significant {
		t.Errorf("expected Significant=false for near-identical pair, got true (PValue=%v)", result.PValue)
	}
}

// TestWelchTTest_EdgeCases verifies graceful handling of degenerate inputs.
func TestWelchTTest_EdgeCases(t *testing.T) {
	t.Run("n<2 baseline", func(t *testing.T) {
		_, err := stats.WelchTTest([]float64{1.0}, []float64{1.0, 2.0, 3.0})
		if err == nil {
			t.Error("expected error for baseline n<2, got nil")
		}
	})

	t.Run("n<2 candidate", func(t *testing.T) {
		_, err := stats.WelchTTest([]float64{1.0, 2.0, 3.0}, []float64{2.0})
		if err == nil {
			t.Error("expected error for candidate n<2, got nil")
		}
	})

	t.Run("empty baseline", func(t *testing.T) {
		_, err := stats.WelchTTest(nil, []float64{1.0, 2.0, 3.0})
		if err == nil {
			t.Error("expected error for empty baseline, got nil")
		}
	})

	t.Run("zero variance — no panic, Significant=false", func(t *testing.T) {
		// Both slices are identical constants: variance=0
		baseline := []float64{5.0, 5.0, 5.0, 5.0, 5.0}
		candidate := []float64{5.0, 5.0, 5.0, 5.0, 5.0}

		result, err := stats.WelchTTest(baseline, candidate)
		// Must not panic; either an error OR Significant=false is acceptable.
		if err == nil {
			if result.Significant {
				t.Errorf("expected Significant=false for identical zero-variance inputs, got true")
			}
			if math.IsNaN(result.TStat) || math.IsInf(result.TStat, 0) {
				t.Errorf("TStat must not be NaN/Inf for zero-variance input, got %v", result.TStat)
			}
		}
		// err != nil is also acceptable (explicit zero-variance error)
	})

	t.Run("zero variance baseline only — no panic", func(t *testing.T) {
		baseline := []float64{5.0, 5.0, 5.0, 5.0, 5.0}
		candidate := []float64{10.0, 11.0, 9.0, 10.5, 10.2}

		result, err := stats.WelchTTest(baseline, candidate)
		if err == nil {
			// result must not contain NaN/Inf
			if math.IsNaN(result.TStat) || math.IsInf(result.TStat, 0) {
				t.Errorf("TStat must not be NaN/Inf, got %v", result.TStat)
			}
			if math.IsNaN(result.PValue) || math.IsInf(result.PValue, 0) {
				t.Errorf("PValue must not be NaN/Inf, got %v", result.PValue)
			}
		}
	})
}
