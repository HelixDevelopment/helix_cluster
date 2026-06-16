package health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// SECOND adversarial pass for internal/health.
//
// The first pass (health_adversarial_test.go) proved the aggregate fold is
// fail-CLOSED for a malformed Status (unknown enum never folds to Healthy) and
// that worst-wins / readiness-drain hold. This pass targets the seams the
// existing tests do NOT exercise:
//   - "absence == OK" boundaries: a registered-but-unprobed check, and a check
//     that was probed once then unregistered, must not silently keep voting.
//   - The fold reads ONLY r.Status; a check that returns (Healthy, err) is taken
//     at its Status word (Status is authoritative) — pinned so a future change
//     that starts trusting err, or that downgrades on any err, is caught.
//   - The gRPC Server.Check overall path (worst-wins through the RPC, not just
//     the Aggregator) and the per-service path's status/detail wiring.
//   - StatusWithDetails returns a fresh, non-aliased details map (the
//     GetAllStatuses aliasing class, re-checked on the details sink).
//   - ReportHealth's status/score provenance (a documented "for now" stub):
//     the broadcast Status is the LOCAL aggregator overall, NOT the remote
//     node's reported health — a sink-side mismatch pinned as a latent risk.
//
// Every test here is written to PASS on the code left behind. Where a behaviour
// is a deliberate contract or a known "for now" stub rather than a bug, the test
// PINS it (so a silent regression trips) and the classification is in the
// comment. All time-bounded waits are guarded so nothing can hang.
// ============================================================================

// ----------------------------------------------------------------------------
// (A) ABSENCE == OK boundaries — DOCUMENTED CONTRACT pins.
//
// The fold iterates a.results, not a.checks. A check that has no result is
// invisible. This is correct for the data the aggregator HAS (it never observed
// that check), but it has a sharp edge: registering a Critical dependency and
// then querying WITHOUT RunChecks leaves the aggregate Healthy. Pin it so the
// contract ("verdict reflects the last RunChecks snapshot, registration alone
// does not vote") is explicit and a change is noticed.
// ----------------------------------------------------------------------------

// TestAdversarial2_UnprobedCheckDoesNotVote pins that a check registered AFTER
// the last RunChecks does not contribute until it is actually probed. The
// readiness sink is the dangerous one: it must NOT report a brand-new, never-run
// Critical dependency as draining the node OR as healthy on stale data — it
// reports on the snapshot it has. We assert the precise, current semantics so a
// regression in either direction (suddenly counting unprobed checks, or dropping
// probed ones) is caught.
func TestAdversarial2_UnprobedCheckDoesNotVote(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("ok", &mockCheck{status: Healthy})
	agg.RunChecks(context.Background())
	require.Equal(t, Healthy, agg.GetStatus())

	// Register a Critical check but do NOT re-run. It has no result yet.
	agg.RegisterCheck("crit", &mockCheck{status: Critical})

	// CONTRACT: the verdict still reflects the last snapshot (only "ok").
	assert.Equal(t, Healthy, agg.GetStatus(),
		"verdict must reflect the last RunChecks snapshot; an unprobed registration does not vote")
	assert.Len(t, agg.GetAllStatuses(), 1,
		"results hold only probed checks, not bare registrations")

	// After RunChecks the new Critical is probed and the fold turns Critical.
	agg.RunChecks(context.Background())
	assert.Equal(t, Critical, agg.GetStatus(),
		"once probed, the Critical dependency must drive the aggregate Critical (worst-wins)")
}

// TestAdversarial2_UnregisterStopsVoting pins that once a check is unregistered
// its prior result is dropped on the next RunChecks and stops contributing — a
// stale Critical must not haunt the aggregate, and a stale Healthy must not mask
// a real failure. UnregisterCheck deletes results immediately; the next run
// rebuilds from currently-registered checks only.
func TestAdversarial2_UnregisterStopsVoting(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("keep", &mockCheck{status: Healthy})
	agg.RegisterCheck("gone", &mockCheck{status: Critical})
	agg.RunChecks(context.Background())
	require.Equal(t, Critical, agg.GetStatus())

	agg.UnregisterCheck("gone")
	// UnregisterCheck drops the result eagerly, before any re-run.
	if _, ok := agg.GetServiceStatus("gone"); ok {
		t.Fatal("unregister must drop the cached result immediately")
	}
	agg.RunChecks(context.Background())
	assert.Equal(t, Healthy, agg.GetStatus(),
		"an unregistered Critical must stop voting; its stale result must not persist")
}

// ----------------------------------------------------------------------------
// (B) STATUS IS AUTHORITATIVE — DOCUMENTED CONTRACT pin.
//
// The fold reads only r.Status. A well-behaved check encodes failure in Status
// (HTTPCheck returns Critical+err). A check that returns (Healthy, err) is a
// contradiction; the aggregator trusts Status. Pin BOTH directions:
//   - (Healthy, err)  -> aggregate Healthy (err is informational, not a downgrade)
//   - (Critical, nil) -> aggregate Critical (no err is NOT required to fail)
// This guards against a future "downgrade on any non-nil err" change that would
// make every check carrying a soft warning flip the node unhealthy, AND against
// the opposite "only fail when err != nil" bluff.
// ----------------------------------------------------------------------------

func TestAdversarial2_FoldUsesStatusNotError(t *testing.T) {
	// Healthy status with a non-nil error stays Healthy (Status authoritative).
	agg := NewAggregator()
	agg.RegisterCheck("warn", &mockCheck{status: Healthy, err: errors.New("soft warning")})
	agg.RunChecks(context.Background())
	assert.Equal(t, Healthy, agg.GetStatus(),
		"Status is authoritative: a Healthy status with an informational error stays Healthy")

	// Critical status with NO error still fails the aggregate.
	agg2 := NewAggregator()
	agg2.RegisterCheck("down", &mockCheck{status: Critical, err: nil})
	agg2.RunChecks(context.Background())
	assert.Equal(t, Critical, agg2.GetStatus(),
		"a Critical status must fail the aggregate even with a nil error (no err is not a pass)")
}

// ----------------------------------------------------------------------------
// (C) gRPC Server.Check — worst-wins and per-service wiring through the RPC.
// Existing tests cover a single-Healthy overall and a single-service lookup;
// they do NOT prove worst-wins survives the RPC boundary, nor that the
// per-service response reports THAT service's status (not the overall), nor that
// the detail string carries the failing check's error.
// ----------------------------------------------------------------------------

// TestAdversarial2_ServerCheck_OverallWorstWins proves the overall gRPC Check
// response reflects the worst component, not a fold-open Healthy. Mutation that
// makes getStatusLocked fail open would flip "critical" to "healthy" here.
func TestAdversarial2_ServerCheck_OverallWorstWins(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("a", &mockCheck{status: Healthy})
	agg.RegisterCheck("b", &mockCheck{status: Healthy})
	agg.RegisterCheck("c", &mockCheck{status: Critical})
	agg.RunChecks(context.Background())

	srv := NewServer(agg)
	resp, err := srv.Check(context.Background(), &helixv1.CheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, "critical", resp.Status,
		"overall gRPC Check must report the worst component, never fold to healthy")
	// All three components appear in details.
	assert.Len(t, resp.Details, 3)
}

// TestAdversarial2_ServerCheck_PerServiceReportsOwnStatus proves a per-service
// query returns THAT service's status (Healthy) even when the overall cluster is
// Critical — the response must not leak the overall verdict into a single
// service, and the inverse: a query for the Critical service reports critical
// with its error in the detail. Mutation: return `st` (overall) instead of
// `svc.Status` in the per-service branch — the Healthy assertion flips.
func TestAdversarial2_ServerCheck_PerServiceReportsOwnStatus(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("healthy-svc", &mockCheck{status: Healthy})
	agg.RegisterCheck("broken-svc", &mockCheck{status: Critical, err: errors.New("connection refused")})
	agg.RunChecks(context.Background())
	srv := NewServer(agg)

	hresp, err := srv.Check(context.Background(), &helixv1.CheckRequest{Service: "healthy-svc"})
	require.NoError(t, err)
	assert.Equal(t, "healthy", hresp.Status,
		"per-service query must report that service's own status, not the Critical overall")
	require.Contains(t, hresp.Details, "healthy-svc")
	assert.Equal(t, "healthy", hresp.Details["healthy-svc"])

	bresp, err := srv.Check(context.Background(), &helixv1.CheckRequest{Service: "broken-svc"})
	require.NoError(t, err)
	assert.Equal(t, "critical", bresp.Status)
	require.Contains(t, bresp.Details, "broken-svc")
	assert.Contains(t, bresp.Details["broken-svc"], "connection refused",
		"the failing check's error must surface in the per-service detail")
}

// ----------------------------------------------------------------------------
// (D) StatusWithDetails returns a fresh, non-aliased map.
// GetAllStatuses was previously proven to copy; the details sink is a separate
// allocation and must be equally safe. A caller mutating the returned details
// must not corrupt subsequent reads. Mutation: return a map aliased to internal
// state (there is none today, but a refactor could cache it) — this trips.
// ----------------------------------------------------------------------------

func TestAdversarial2_StatusWithDetails_NotAliased(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc", &mockCheck{status: Degraded, err: errors.New("slow")})
	agg.RunChecks(context.Background())

	_, d1 := agg.StatusWithDetails()
	require.Contains(t, d1["svc"], "slow")
	d1["svc"] = "TAMPERED"
	d1["injected"] = "x"

	_, d2 := agg.StatusWithDetails()
	assert.Contains(t, d2["svc"], "slow",
		"a caller mutating the returned details map must not corrupt later reads")
	assert.NotContains(t, d2, "injected",
		"the details map must be freshly built each call, not aliased")
}

// ----------------------------------------------------------------------------
// (E) ReportHealth status/score provenance — LATENT RISK pin.
//
// ReportHealth is a documented "for now" stub: it accepts a remote node's report
// and broadcasts a HealthEvent whose Status is the LOCAL aggregator's overall
// status, while attaching the REMOTE node's score verbatim. So a sick remote
// node (low reported Overall) is broadcast labelled with the local node's
// (possibly Healthy) status — Status and Score can disagree. That is a sink-side
// mismatch a watcher could misread. It is NOT today an aggregation-fold bug, so
// we PIN the current behaviour: a future change that wires the remote report
// into the broadcast Status (the correct fix) will trip this and prompt updating
// the pin. Until then this documents the gap.
// ----------------------------------------------------------------------------

func TestAdversarial2_ReportHealth_BroadcastsLocalStatusNotRemote(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc", &mockCheck{status: Healthy})
	agg.RunChecks(context.Background())
	require.Equal(t, Healthy, agg.GetStatus())
	srv := NewServer(agg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeWatchStream(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Watch(&helixv1.WatchRequest{NodeId: "watcher"}, stream) }()

	// Drain the initial snapshot (guarded).
	select {
	case <-stream.onSend:
	case <-time.After(2 * time.Second):
		t.Fatal("initial snapshot not sent")
	}

	_, err := srv.ReportHealth(context.Background(), &helixv1.ReportHealthRequest{
		NodeId: "sick-node",
		Score:  &helixv1.HealthScore{NodeId: "sick-node", Overall: 1},
	})
	require.NoError(t, err)

	select {
	case <-stream.onSend:
	case <-time.After(2 * time.Second):
		t.Fatal("report not broadcast to watcher")
	}
	evs := stream.events()
	last := evs[len(evs)-1]

	// PIN: the broadcast carries the remote node id and remote score, but the
	// Status is the LOCAL aggregator overall ("healthy"), NOT derived from the
	// remote node's reported Overall=1. This is the known mismatch.
	assert.Equal(t, "sick-node", last.NodeId)
	require.NotNil(t, last.Score)
	assert.EqualValues(t, 1, last.Score.GetOverall(),
		"the remote-reported score is forwarded verbatim")
	assert.Equal(t, "healthy", last.Status,
		"LATENT RISK pin: broadcast Status is the local aggregator overall, not the remote report; "+
			"Status and Score can disagree for a remote node")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not unwind after cancel")
	}
}

// ----------------------------------------------------------------------------
// (F) Concurrent probe of the readiness/liveness SINK under -race.
// The existing race test hammers Register/Run/GetStatus. This one specifically
// races RunChecks against Ready()/Liveness()/Readiness() reads while flipping a
// check's status, asserting only that no read ever observes a torn/garbage
// verdict (every observed value is a legal Status / bool) and the final settled
// state is self-consistent. Goroutines are capped; iterations bounded.
// ----------------------------------------------------------------------------

func TestAdversarial2_ConcurrentRollupReadsRace(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("self", &mockCheck{status: Healthy}, KindLiveness)
	agg.RegisterCheckKind("dep", &mockCheck{status: Healthy}, KindReadiness)

	const workers = 6 // ANTI-HANG cap.
	const iters = 80
	var wg sync.WaitGroup

	// Writer: repeatedly flips the dep between Healthy and Critical and re-runs.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			st := Healthy
			if i%2 == 0 {
				st = Critical
			}
			agg.RegisterCheckKind("dep", &mockCheck{status: st}, KindReadiness)
			agg.RunChecks(context.Background())
		}
	}()

	// Readers: assert every observed verdict is a legal value (no torn reads).
	legal := func(s Status) bool { return s == Unknown || s == Healthy || s == Degraded || s == Critical }
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				assert.True(t, legal(agg.Readiness()), "readiness rollup must never tear")
				assert.True(t, legal(agg.Liveness()), "liveness rollup must never tear")
				_ = agg.Ready() // bool, can't tear; exercises the lock path.
			}
		}()
	}
	wg.Wait()

	// Settle deterministically and assert self-consistency.
	agg.RegisterCheckKind("dep", &mockCheck{status: Healthy}, KindReadiness)
	agg.RunChecks(context.Background())
	assert.Equal(t, Healthy, agg.Readiness())
	assert.Equal(t, Healthy, agg.Liveness())
	assert.True(t, agg.Ready(), "settled all-healthy state must be ready")
}
