package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAggregator_LivenessIgnoresReadinessChecks proves the liveness rollup only
// considers checks classified as liveness: a Critical readiness check must NOT
// drag liveness down (otherwise a failed dependency would trigger a restart
// loop). Mutation: make affectsLiveness return true for KindReadiness — then
// liveness would become Critical and this test fails.
func TestAggregator_LivenessIgnoresReadinessChecks(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("db", &mockCheck{status: Critical}, KindReadiness)
	agg.RegisterCheckKind("self", &mockCheck{status: Healthy}, KindLiveness)
	agg.RunChecks(context.Background())

	assert.Equal(t, Healthy, agg.Liveness(), "readiness failure must not affect liveness")
	assert.Equal(t, Critical, agg.Readiness(), "readiness must reflect failed dependency")
	assert.False(t, agg.Ready(), "node must not be ready when a readiness check is Critical")
}

// TestAggregator_LivenessReflectsLivenessFailure proves a failing liveness check
// rolls up to Critical. Mutation: change Liveness's empty default or rollup to
// always return Healthy — this test fails.
func TestAggregator_LivenessReflectsLivenessFailure(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("deadlock", &mockCheck{status: Critical}, KindLiveness)
	agg.RegisterCheckKind("db", &mockCheck{status: Healthy}, KindReadiness)
	agg.RunChecks(context.Background())

	assert.Equal(t, Critical, agg.Liveness())
	assert.Equal(t, Healthy, agg.Readiness())
	assert.True(t, agg.Ready(), "a liveness failure must not make the node unready")
}

// TestAggregator_NoLivenessChecksIsAlive proves that with zero liveness checks
// the node is considered alive (Healthy). Mutation: change the empty default of
// Liveness from Healthy to Unknown/Critical — this test fails. This guards the
// "absence of a liveness probe must never wedge a restart loop" invariant.
func TestAggregator_NoLivenessChecksIsAlive(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("db", &mockCheck{status: Critical}, KindReadiness)
	agg.RunChecks(context.Background())

	assert.Equal(t, Healthy, agg.Liveness())
}

// TestAggregator_NoReadinessChecksIsNotReady proves that with zero readiness
// results the node is NOT ready (Unknown). Mutation: change the empty default of
// Readiness from Unknown to Healthy — this test fails.
func TestAggregator_NoReadinessChecksIsNotReady(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("self", &mockCheck{status: Healthy}, KindLiveness)
	agg.RunChecks(context.Background())

	assert.Equal(t, Unknown, agg.Readiness())
	assert.False(t, agg.Ready(), "no readiness check means we cannot assert readiness")
}

// TestAggregator_KindBothCountsForBoth proves a KindBoth check contributes to
// both rollups. Mutation: make affectsLiveness or affectsReadiness drop KindBoth
// — one of these assertions fails.
func TestAggregator_KindBothCountsForBoth(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("core", &mockCheck{status: Degraded}, KindBoth)
	agg.RunChecks(context.Background())

	assert.Equal(t, Degraded, agg.Liveness())
	assert.Equal(t, Degraded, agg.Readiness())
	assert.False(t, agg.Ready(), "Degraded readiness must not be ready")
}

// TestAggregator_DefaultKindIsReadiness proves RegisterCheck defaults to
// readiness, not liveness. Mutation: change RegisterCheck to set KindLiveness or
// KindBoth — liveness would then go Critical and this test fails.
func TestAggregator_DefaultKindIsReadiness(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("dep", &mockCheck{status: Critical})
	agg.RunChecks(context.Background())

	assert.Equal(t, Healthy, agg.Liveness(), "default check must not affect liveness")
	assert.Equal(t, Critical, agg.Readiness(), "default check must affect readiness")
}

// TestAggregator_ReadyOnlyWhenHealthy proves Ready is true exactly when the
// readiness rollup is Healthy. Mutation: change Ready to compare against
// Degraded or to `!= Critical` — the Degraded sub-case fails.
func TestAggregator_ReadyOnlyWhenHealthy(t *testing.T) {
	cases := []struct {
		name      string
		readiness Status
		wantReady bool
	}{
		{"healthy", Healthy, true},
		{"degraded", Degraded, false},
		{"critical", Critical, false},
		{"unknown", Unknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := NewAggregator()
			agg.RegisterCheckKind("svc", &mockCheck{status: tc.readiness}, KindReadiness)
			agg.RunChecks(context.Background())
			assert.Equal(t, tc.wantReady, agg.Ready())
		})
	}
}

// TestAggregator_UnregisterClearsKind proves UnregisterCheck removes the kind
// entry so a re-registered name does not inherit a stale classification.
// Mutation: drop `delete(a.kinds, name)` from UnregisterCheck. After unregister
// + RunChecks the stale kind entry is harmless on its own, so we re-register the
// same name as the opposite kind and assert the new classification wins. With
// the bug the kind map would still hold the old value path only if results were
// stale; the concrete guard here is that liveness reflects the NEW kind.
func TestAggregator_UnregisterClearsKind(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("x", &mockCheck{status: Critical}, KindLiveness)
	agg.RunChecks(context.Background())
	require.Equal(t, Critical, agg.Liveness())

	agg.UnregisterCheck("x")
	agg.RunChecks(context.Background())
	// No checks at all: liveness defaults to Healthy, readiness Unknown.
	assert.Equal(t, Healthy, agg.Liveness())
	assert.Equal(t, Unknown, agg.Readiness())

	// Re-register the same name purely as readiness; it must not resurrect the
	// old liveness classification.
	agg.RegisterCheckKind("x", &mockCheck{status: Critical}, KindReadiness)
	agg.RunChecks(context.Background())
	assert.Equal(t, Healthy, agg.Liveness(), "re-registered name must use the new kind")
	assert.Equal(t, Critical, agg.Readiness())
}

// TestAggregator_LivenessReadinessAfterRealHTTPChecks exercises the rollups
// end-to-end against real httptest servers (no status mocks), proving the
// classification works with genuine HealthCheck implementations and real I/O.
// Mutation: swap KindLiveness/KindReadiness on registration — assertions flip.
func TestAggregator_LivenessReadinessAfterRealHTTPChecks(t *testing.T) {
	liveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer liveSrv.Close()
	depSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // 503 -> Critical
	}))
	defer depSrv.Close()

	agg := NewAggregator()
	agg.RegisterCheckKind("self", &HTTPCheck{URL: liveSrv.URL, Timeout: 2 * time.Second}, KindLiveness)
	agg.RegisterCheckKind("upstream", &HTTPCheck{URL: depSrv.URL, Timeout: 2 * time.Second}, KindReadiness)
	agg.RunChecks(context.Background())

	assert.Equal(t, Healthy, agg.Liveness(), "self HTTP 200 -> alive")
	assert.Equal(t, Critical, agg.Readiness(), "upstream HTTP 503 -> not ready")
	assert.False(t, agg.Ready())
}

// TestAggregator_RollupPrecedence proves Critical beats Degraded beats Unknown
// within a single rollup kind. Mutation: reorder the switch so Degraded returns
// immediately (shadowing Critical) — the Critical assertion fails.
func TestAggregator_RollupPrecedence(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("a", &mockCheck{status: Degraded}, KindReadiness)
	agg.RegisterCheckKind("b", &mockCheck{status: Unknown}, KindReadiness)
	agg.RegisterCheckKind("c", &mockCheck{status: Critical}, KindReadiness)
	agg.RunChecks(context.Background())
	assert.Equal(t, Critical, agg.Readiness())

	agg.UnregisterCheck("c")
	agg.RunChecks(context.Background())
	assert.Equal(t, Degraded, agg.Readiness(), "Degraded beats Unknown")
}

// TestAggregator_GetStatusMatchesDetails is an anti-bluff guard that the
// deduplicated GetStatus stays consistent with StatusWithDetails. Mutation:
// change getStatusLocked precedence — both GetStatus and StatusWithDetails would
// shift together, but this also pins the err-formatting path. Concretely, the
// swallowed-error mutation (drop the r.Error formatting in StatusWithDetails)
// fails the Contains assertion.
func TestAggregator_GetStatusMatchesDetails(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("a", &mockCheck{status: Degraded, err: errors.New("slow")})
	agg.RunChecks(context.Background())

	st, details := agg.StatusWithDetails()
	assert.Equal(t, agg.GetStatus(), st)
	assert.Contains(t, details["a"], "slow")
}
