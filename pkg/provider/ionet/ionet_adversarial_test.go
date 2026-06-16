package ionet_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/pool"
	"github.com/HelixDevelopment/helix_cluster/pkg/provider/ionet"
)

// ---------------------------------------------------------------------------
// Adversarial test suite for pkg/provider/ionet.
//
// This package has NO pricing/score comparator or sort-on-price (those live in
// pkg/pool, out of scope). The hostile surfaces that DO exist here are:
//   - HTTP body parsing on 2xx responses: garbage / wrong-shape bodies must not
//     panic (DoS) and must not fabricate a usable cluster or a fake "healthy".
//   - Capacity quota edges (zero/fail-closed) and the reserve/release accounting
//     that gates a *billable* deploy.
//   - Healthy() string derivation (trim/fold) must not let a near-miss status
//     ("healthyish", "not healthy") win the healthy slot.
//
// Each probe is classified inline: REAL BUG | DOCUMENTED CONTRACT | LATENT RISK.
// ---------------------------------------------------------------------------

// bodyServer serves a fixed status code + body on every route. Used to feed
// hostile bodies into the adapter's parse path.
func bodyServer(t *testing.T, code int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDeployGarbageBodyDoesNotPanicOrFabricate feeds a 2xx response whose body
// is NOT valid JSON. Promise (DeployCluster doc): a 2xx with an unparseable body
// is a decode error and NEVER a fabricated cluster id.
//
// CONTRACT PIN: json.Unmarshal returns an error (does not panic) on garbage, and
// DeployCluster surfaces it without an ID. Guards against a future refactor that
// swallows the decode error and returns Cluster{} as success.
func TestDeployGarbageBodyDoesNotPanicOrFabricate(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`}{not json at all`,
		`<html>503 from a proxy</html>`,
		``, // empty 2xx body
		`null`,
		`[1,2,3]`, // valid JSON, wrong shape (array, not object)
		"\x00\x01\x02garbage",
	} {
		srv := bodyServer(t, http.StatusOK, body)
		p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		c, err := p.DeployCluster(context.Background(), ionet.ClusterSpec{GPUType: "H100", GPUCount: 1})
		if err == nil {
			t.Fatalf("body %q: DeployCluster returned nil error; a non-cluster 2xx body must be an error, got cluster %+v", body, c)
		}
		if c.ID != "" {
			t.Fatalf("body %q: DeployCluster fabricated cluster id %q on a garbage body", body, c.ID)
		}
	}
}

// TestDeployJSONWrongTypeForIDIsRejected sends a 2xx body where cluster_id is the
// wrong JSON type. encoding/json will error on type mismatch into a string
// field; the adapter must surface that, not fabricate.
func TestDeployJSONWrongTypeForIDIsRejected(t *testing.T) {
	t.Parallel()
	// cluster_id as a number -> json type error into string field.
	srv := bodyServer(t, http.StatusOK, `{"cluster_id": 12345, "status":"provisioning"}`)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c, err := p.DeployCluster(context.Background(), ionet.ClusterSpec{GPUType: "H100", GPUCount: 1})
	if err == nil {
		t.Fatalf("DeployCluster accepted a numeric cluster_id, got %+v", c)
	}
	if c.ID != "" {
		t.Fatalf("fabricated id %q on wrong-type cluster_id", c.ID)
	}
}

// TestDeployWhitespaceOnlyClusterIDIsErrDeployFailed: a 2xx body whose cluster_id
// is whitespace-only must be rejected (DeployCluster trims before the empty
// check). A usable handle must carry a real id.
//
// MUTATION TARGET: the strings.TrimSpace(c.ID) == "" guard at ionet.go:276.
func TestDeployWhitespaceOnlyClusterIDIsErrDeployFailed(t *testing.T) {
	t.Parallel()
	srv := bodyServer(t, http.StatusOK, `{"cluster_id":"   ","status":"provisioning"}`)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c, err := p.DeployCluster(context.Background(), ionet.ClusterSpec{GPUType: "H100", GPUCount: 1})
	if !errors.Is(err, ionet.ErrDeployFailed) {
		t.Fatalf("err = %v, want ErrDeployFailed for whitespace cluster_id (got cluster %+v)", err, c)
	}
	if c.ID != "" {
		t.Fatalf("returned a usable-looking id %q for a whitespace cluster_id", c.ID)
	}
}

// TestHealthCheckGarbageBodyDoesNotFakeHealthy feeds a 200 status with a hostile
// body. Promise: HealthCheck PARSES status off the wire; a garbage body is a
// decode error, never a fabricated healthy result.
//
// CONTRACT PIN: a near-miss / non-JSON body must not yield Healthy()==true.
func TestHealthCheckGarbageBodyDoesNotFakeHealthy(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`not json`,
		`[1,2,3]`,
		"\x00\x01\x02",
	} {
		srv := bodyServer(t, http.StatusOK, body)
		p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		h, err := p.HealthCheck(context.Background(), "run-abc-uuid")
		if err == nil {
			t.Fatalf("body %q: HealthCheck returned nil error on a garbage body (health=%+v)", body, h)
		}
		if h.Healthy() {
			t.Fatalf("body %q: HealthCheck fabricated a healthy result", body)
		}
	}
}

// TestHealthyRejectsNearMissStatuses is the adversarial guard on Health.Healthy()
// derivation. Only an exact "healthy" (case-insensitive, surrounding whitespace
// trimmed) is healthy; near-misses must NOT seize the healthy verdict.
//
// MUTATION TARGET: strings.EqualFold(strings.TrimSpace(...), "healthy") at
// ionet.go:131. A mutation to strings.Contains would let "not healthy" pass.
func TestHealthyRejectsNearMissStatuses(t *testing.T) {
	t.Parallel()
	healthy := []string{"healthy", "HEALTHY", "Healthy", "  healthy ", "\thealthy\n"}
	notHealthy := []string{
		"healthyish", "unhealthy", "not healthy", "very healthy please",
		"provisioning", "", "  ", "degraded", "health",
	}
	for _, s := range healthy {
		if !(ionet.Health{Status: s}).Healthy() {
			t.Fatalf("status %q should be healthy", s)
		}
	}
	for _, s := range notHealthy {
		if (ionet.Health{Status: s}).Healthy() {
			t.Fatalf("status %q must NOT be reported healthy (substring/loose match bug?)", s)
		}
	}
}

// TestCapacityZeroFailsClosed proves a zero Capacity refuses every Provision
// (no unbounded billable deploy on an unset quota).
//
// MUTATION TARGET: the >= gate at ionet.go:331 combined with the cfg.Capacity
// handling. With cap 0, len(deployed)+reserved (0) >= 0 is true -> refused.
func TestCapacityZeroFailsClosed(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Provision(context.Background(), pool.Spec{GPUType: "A100"})
	if !errors.Is(err, ionet.ErrAtCapacity) {
		t.Fatalf("Provision at cap 0 err = %v, want ErrAtCapacity", err)
	}
	if n := fake.liveCount(); n != 0 {
		t.Fatalf("a deploy reached the backend at cap 0: live=%d", n)
	}
}

// TestNegativeCapacityClampedFailsClosed proves a negative Capacity is clamped to
// 0 (fail-closed), not treated as a huge/negative bound that lets deploys through.
func TestNegativeCapacityClampedFailsClosed(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: -5})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Capacity() != 0 {
		t.Fatalf("Capacity() = %d, want 0 (negative clamped)", p.Capacity())
	}
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"}); !errors.Is(err, ionet.ErrAtCapacity) {
		t.Fatalf("Provision with negative cap err = %v, want ErrAtCapacity", err)
	}
	if n := fake.liveCount(); n != 0 {
		t.Fatalf("negative cap let a deploy through: live=%d", n)
	}
}

// TestProvisionDeployFailureReleasesReservation is the load-bearing accounting
// probe. A failing deploy MUST decrement the in-flight reservation so a later
// Provision is not permanently locked out below Capacity.
//
// Setup: Capacity 1, a backend that fails the FIRST deploy (502) and succeeds
// the rest. The first Provision fails; if its reservation leaked, the second
// Provision would wrongly see 0+1 >= 1 and return ErrAtCapacity. A correct
// adapter releases the reservation and the second Provision succeeds.
//
// MUTATION TARGET: the p.reserved-- on the deploy-error path at ionet.go:346.
func TestProvisionDeployFailureReleasesReservation(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway) // first deploy fails
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"cluster_id":"run-ok-uuid","status":"provisioning"}`))
	}))
	t.Cleanup(srv.Close)

	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// First Provision fails at the backend.
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"}); err == nil {
		t.Fatal("first Provision unexpectedly succeeded")
	} else if errors.Is(err, ionet.ErrAtCapacity) {
		t.Fatalf("first Provision should fail at deploy, not capacity: %v", err)
	}
	// Second Provision must NOT be locked out by a leaked reservation.
	inst, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"})
	if err != nil {
		t.Fatalf("second Provision after a failed deploy = %v; reservation leaked under capacity?", err)
	}
	if inst.ID == "" {
		t.Fatal("second Provision returned empty id")
	}
}

// TestReleaseTerminateFailureKeepsTracking proves a failed terminate does NOT
// drop the local record — so a billable cluster is never silently forgotten by
// the adapter (DOCUMENTED CONTRACT at Release). On failure the cluster stays
// tracked and still counts against Capacity.
//
// MUTATION TARGET: the ordering at ionet.go:374-379 (terminate THEN delete). If
// delete ran before/regardless of the terminate result, the cluster would leak.
func TestReleaseTerminateFailureKeepsTracking(t *testing.T) {
	t.Parallel()
	var failTerminate atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost: // deploy
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"cluster_id":"run-keep-uuid","status":"provisioning"}`))
		case http.MethodDelete: // terminate
			if failTerminate.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	inst, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Terminate fails -> Release errors AND keeps the cluster tracked.
	failTerminate.Store(true)
	if err := p.Release(context.Background(), inst); err == nil {
		t.Fatal("Release with a failing terminate returned nil error")
	}
	list, _ := p.List(context.Background())
	if len(list) != 1 || list[0].ID != inst.ID {
		t.Fatalf("after a failed terminate, cluster must stay tracked; List=%+v", list)
	}
	// It must still count against capacity (no over-deploy).
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"}); !errors.Is(err, ionet.ErrAtCapacity) {
		t.Fatalf("a still-tracked cluster must hold its capacity slot; got %v", err)
	}

	// Now terminate succeeds -> Release frees the slot.
	failTerminate.Store(false)
	if err := p.Release(context.Background(), inst); err != nil {
		t.Fatalf("Release after terminate recovers: %v", err)
	}
	list, _ = p.List(context.Background())
	if len(list) != 0 {
		t.Fatalf("after a successful Release, List must be empty; got %+v", list)
	}
}

// TestHealthCheckNon200NonNotFoundIsError proves a 2xx-but-not-200 or any other
// unexpected status is an error (no fabricated health), and specifically that a
// 5xx is not silently treated as healthy.
func TestHealthCheckNon200NonNotFoundIsError(t *testing.T) {
	t.Parallel()
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusForbidden, http.StatusAccepted} {
		srv := bodyServer(t, code, `{"status":"healthy"}`)
		p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		h, err := p.HealthCheck(context.Background(), "run-x-uuid")
		if err == nil {
			t.Fatalf("status %d: HealthCheck returned nil error (health=%+v)", code, h)
		}
		if h.Healthy() {
			t.Fatalf("status %d: non-200 response fabricated a healthy verdict", code)
		}
	}
}

// TestConcurrentProvisionReleaseNoLeak hammers Provision/Release concurrently and
// asserts the adapter's tracked set never exceeds Capacity and ends consistent
// with the backend (no reservation/accounting corruption under contention).
func TestConcurrentProvisionReleaseNoLeak(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	const capLimit = 4
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: capLimit})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"})
			if err != nil {
				return // capacity contention, fine
			}
			_ = p.Release(context.Background(), inst)
		}()
	}
	wg.Wait()
	list, _ := p.List(context.Background())
	if len(list) > capLimit {
		t.Fatalf("tracked clusters %d exceed Capacity %d", len(list), capLimit)
	}
	if fake.liveCount() != len(list) {
		t.Fatalf("backend live=%d disagrees with tracked=%d after churn", fake.liveCount(), len(list))
	}
}

// TestTerminateNotFoundIsSuccess pins the documented contract that a 404 on
// terminate (already gone) is success, not an error.
func TestTerminateNotFoundIsSuccess(t *testing.T) {
	t.Parallel()
	srv := bodyServer(t, http.StatusNotFound, ``)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Terminate(context.Background(), "run-gone-uuid"); err != nil {
		t.Fatalf("Terminate on 404 = %v, want nil (already gone is success)", err)
	}
}

// sanity: keep the strings import used even if probes change.
var _ = strings.TrimSpace
