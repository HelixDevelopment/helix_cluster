package ionet_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/pool"
	"github.com/HelixDevelopment/helix_cluster/pkg/provider/ionet"
)

// compile-time proof IONetProvider satisfies the pkg/pool.GPUProvider seam.
var _ pool.GPUProvider = (*ionet.IONetProvider)(nil)

// fakeIONet is a REAL-state in-process io.net REST backend. It is NOT a no-op
// mock: it mints a real run-UUID per deploy, tracks the live clusters and their
// health, and serves the exact deploy/status/terminate wire protocol the
// IONetProvider speaks. It stands in for the out-of-host io.net control plane so
// the adapter's real parse-and-return logic is exercised end to end.
type fakeIONet struct {
	mu        sync.Mutex
	seq       int
	clusters  map[string]ionet.Cluster
	health    map[string]string // cluster_id -> status
	gotAuth   string            // last Authorization header seen on deploy
	gotDeploy map[string]any    // last decoded deploy body
}

func newFakeIONet() *fakeIONet {
	return &fakeIONet{
		clusters: make(map[string]ionet.Cluster),
		health:   make(map[string]string),
	}
}

// server wires the fake onto an httptest.Server speaking the io.net REST routes:
//
//	POST   /clusters             -> deploy, returns cluster_id
//	GET    /clusters/{id}/status -> health
//	DELETE /clusters/{id}        -> terminate
func (f *fakeIONet) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/clusters", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.gotAuth = r.Header.Get("Authorization")
		f.gotDeploy = body
		f.seq++
		id := fmt.Sprintf("run-%08d-uuid", f.seq)
		gpuType, _ := body["gpu_type"].(string)
		gpuCount := 0
		if gc, ok := body["gpu_count"].(float64); ok {
			gpuCount = int(gc)
		}
		c := ionet.Cluster{ID: id, GPUType: gpuType, GPUCount: gpuCount, Status: "provisioning"}
		f.clusters[id] = c
		f.health[id] = "healthy" // cluster comes up healthy in the fake
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(c)
	})
	mux.HandleFunc("/clusters/", func(w http.ResponseWriter, r *http.Request) {
		// /clusters/{id}/status  or  /clusters/{id}
		rest := strings.TrimPrefix(r.URL.Path, "/clusters/")
		if strings.HasSuffix(rest, "/status") {
			id := strings.TrimSuffix(rest, "/status")
			f.mu.Lock()
			status, ok := f.health[id]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			ready := 0
			if status == "healthy" {
				ready = 1
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ionet.Health{
				ClusterID:     id,
				Status:        status,
				ReadyReplicas: ready,
				TotalReplicas: 1,
			})
			return
		}
		// terminate
		if r.Method == http.MethodDelete {
			id := rest
			f.mu.Lock()
			_, ok := f.clusters[id]
			delete(f.clusters, id)
			delete(f.health, id)
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeIONet) liveCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.clusters) }

// TestDeployAndHealthCheckClosure is the load-bearing closure test. It deploys a
// cluster against the httptest server, asserts a non-empty run-UUID cluster_id
// is RETURNED from DeployCluster, then health-checks that exact UUID and asserts
// a HEALTHY status — recorded with the UUID. This is the verbatim closure:
// "log shows cluster_id returned from DeployCluster and a healthy status from
// the cluster check, recorded with the UUID."
//
// MUTATION GUARD: if DeployCluster fabricated an id (instead of parsing
// cluster_id off the response) the id would not match the fake's run-UUID and
// the subsequent HealthCheck on that id would 404 -> ErrClusterNotFound, failing
// the test. If HealthCheck ignored the parsed Status, Healthy() would be wrong.
func TestDeployAndHealthCheckClosure(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)

	p, err := ionet.New(ionet.Config{
		BaseURL:  srv.URL,
		Token:    "io-test-token",
		Capacity: 4,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	spec := ionet.ClusterSpec{
		GPUType:  "H100",
		GPUCount: 8,
		Region:   "us-east",
		Image:    "ghcr.io/helix/trainer:latest",
		Command:  []string{"/bin/run", "--epochs", "3"},
		Env:      map[string]string{"WANDB_MODE": "offline"},
	}

	cluster, err := p.DeployCluster(context.Background(), spec)
	if err != nil {
		t.Fatalf("DeployCluster: %v", err)
	}
	// CLOSURE proof #1: a cluster_id (run-UUID) is returned, parsed off the wire.
	if cluster.ID == "" {
		t.Fatal("DeployCluster returned empty cluster_id")
	}
	if !strings.HasPrefix(cluster.ID, "run-") || !strings.HasSuffix(cluster.ID, "-uuid") {
		t.Fatalf("cluster_id %q is not the run-UUID minted by the backend (was it fabricated locally?)", cluster.ID)
	}
	if cluster.GPUType != "H100" || cluster.GPUCount != 8 {
		t.Fatalf("cluster echo = %q/%d, want H100/8", cluster.GPUType, cluster.GPUCount)
	}
	t.Logf("DeployCluster returned cluster_id=%q status=%q", cluster.ID, cluster.Status)

	// The deploy spec actually traveled the wire (real request, not a no-op).
	fake.mu.Lock()
	if !strings.Contains(fake.gotAuth, "io-test-token") {
		fake.mu.Unlock()
		t.Fatalf("deploy did not carry bearer token, got %q", fake.gotAuth)
	}
	if gt, _ := fake.gotDeploy["gpu_type"].(string); gt != "H100" {
		fake.mu.Unlock()
		t.Fatalf("deploy body gpu_type = %q, want H100", gt)
	}
	fake.mu.Unlock()

	// CLOSURE proof #2: health-check that exact UUID -> healthy, recorded w/ UUID.
	health, err := p.HealthCheck(context.Background(), cluster.ID)
	if err != nil {
		t.Fatalf("HealthCheck(%q): %v", cluster.ID, err)
	}
	if health.ClusterID != cluster.ID {
		t.Fatalf("health recorded for %q, want the deployed UUID %q", health.ClusterID, cluster.ID)
	}
	if !health.Healthy() {
		t.Fatalf("HealthCheck status = %q, want healthy", health.Status)
	}
	if health.ReadyReplicas != 1 || health.TotalReplicas != 1 {
		t.Fatalf("health replicas = %d/%d, want 1/1", health.ReadyReplicas, health.TotalReplicas)
	}
	t.Logf("HealthCheck(%q) -> status=%q healthy=%t ready=%d/%d",
		health.ClusterID, health.Status, health.Healthy(), health.ReadyReplicas, health.TotalReplicas)
}

// TestDeployEmptyGPUTypeRefused proves a meaningless deploy is refused before
// any wire call (no billable deploy on an empty spec).
func TestDeployEmptyGPUTypeRefused(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.DeployCluster(context.Background(), ionet.ClusterSpec{GPUCount: 1}); !errors.Is(err, ionet.ErrEmptyGPUType) {
		t.Fatalf("DeployCluster empty gpu type err = %v, want ErrEmptyGPUType", err)
	}
	if n := fake.liveCount(); n != 0 {
		t.Fatalf("a deploy reached the backend on an empty spec: live=%d", n)
	}
}

// TestDeployNon2xxIsErrDeployFailed proves a refused deploy surfaces
// ErrDeployFailed and never a fabricated cluster.
func TestDeployNon2xxIsErrDeployFailed(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c, err := p.DeployCluster(context.Background(), ionet.ClusterSpec{GPUType: "A100", GPUCount: 1})
	if !errors.Is(err, ionet.ErrDeployFailed) {
		t.Fatalf("err = %v, want ErrDeployFailed", err)
	}
	if c.ID != "" {
		t.Fatalf("got fabricated cluster id %q on failed deploy", c.ID)
	}
}

// TestDeployEmptyClusterIDIsErrDeployFailed proves a 2xx body with no cluster_id
// is rejected (no usable cluster, never a fabricated id).
func TestDeployEmptyClusterIDIsErrDeployFailed(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"provisioning"}`))
	}))
	t.Cleanup(srv.Close)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.DeployCluster(context.Background(), ionet.ClusterSpec{GPUType: "A100", GPUCount: 1}); !errors.Is(err, ionet.ErrDeployFailed) {
		t.Fatalf("err = %v, want ErrDeployFailed", err)
	}
}

// TestHealthCheckNotFound proves a 404 status maps to ErrClusterNotFound.
func TestHealthCheckNotFound(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.HealthCheck(context.Background(), "run-does-not-exist-uuid")
	if !errors.Is(err, ionet.ErrClusterNotFound) {
		t.Fatalf("err = %v, want ErrClusterNotFound", err)
	}
}

// TestHealthCheckEmptyID proves an empty id is refused before any call.
func TestHealthCheckEmptyID(t *testing.T) {
	t.Parallel()
	p, err := ionet.New(ionet.Config{BaseURL: "https://example.invalid", Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.HealthCheck(context.Background(), "   "); !errors.Is(err, ionet.ErrEmptyClusterID) {
		t.Fatalf("err = %v, want ErrEmptyClusterID", err)
	}
}

// TestProviderLifecycle exercises the pool.GPUProvider seam end to end against
// the httptest backend: Provision deploys and tracks, List reflects it, Release
// terminates over the wire and stops tracking, and the backend agrees.
func TestProviderLifecycle(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Token: "tok", Capacity: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != ionet.ProviderName {
		t.Fatalf("Name = %q, want %q", p.Name(), ionet.ProviderName)
	}
	if p.Capacity() != 2 {
		t.Fatalf("Capacity = %d, want 2", p.Capacity())
	}

	inst, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100", HourlyUSD: 1.5})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if inst.ID == "" || inst.GPUType != "A100" || inst.HourlyUSD != 1.5 {
		t.Fatalf("Provision returned %+v, want a real id with A100/1.5", inst)
	}
	if fake.liveCount() != 1 {
		t.Fatalf("backend live = %d, want 1 after Provision", fake.liveCount())
	}

	list, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != inst.ID {
		t.Fatalf("List = %+v, want exactly the provisioned instance", list)
	}

	// The provisioned cluster is healthy in the backend.
	h, err := p.HealthCheck(context.Background(), inst.ID)
	if err != nil || !h.Healthy() {
		t.Fatalf("HealthCheck(%q) = %+v, err=%v; want healthy", inst.ID, h, err)
	}

	if err := p.Release(context.Background(), inst); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if fake.liveCount() != 0 {
		t.Fatalf("backend live = %d, want 0 after Release", fake.liveCount())
	}
	list, _ = p.List(context.Background())
	if len(list) != 0 {
		t.Fatalf("List after Release = %+v, want empty", list)
	}
}

// TestReleaseUnknownInstance proves releasing a cluster we never deployed is
// ErrUnknownInstance (no silent no-op terminate).
func TestReleaseUnknownInstance(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Release(context.Background(), pool.Instance{ID: "run-nope-uuid"}); !errors.Is(err, ionet.ErrUnknownInstance) {
		t.Fatalf("err = %v, want ErrUnknownInstance", err)
	}
}

// TestProvisionAtCapacity proves the Capacity quota is enforced before a
// billable deploy.
func TestProvisionAtCapacity(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"}); err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	_, err = p.Provision(context.Background(), pool.Spec{GPUType: "A100"})
	if !errors.Is(err, ionet.ErrAtCapacity) {
		t.Fatalf("second Provision err = %v, want ErrAtCapacity", err)
	}
	if fake.liveCount() != 1 {
		t.Fatalf("backend live = %d, want 1 (no over-capacity deploy)", fake.liveCount())
	}
}

// TestConcurrentProvisionRespectsCapacity proves the reserve-under-lock gate
// keeps concurrent Provisions from collectively overrunning Capacity.
func TestConcurrentProvisionRespectsCapacity(t *testing.T) {
	t.Parallel()
	fake := newFakeIONet()
	srv := fake.server(t)
	const cap = 3
	p, err := ionet.New(ionet.Config{BaseURL: srv.URL, Capacity: cap})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var wg sync.WaitGroup
	var okCount int
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Provision(context.Background(), pool.Spec{GPUType: "A100"}); err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != cap {
		t.Fatalf("successful provisions = %d, want exactly Capacity %d", okCount, cap)
	}
	if fake.liveCount() != cap {
		t.Fatalf("backend live = %d, want %d", fake.liveCount(), cap)
	}
}

// TestNewBlankBaseURLRejected proves an explicitly whitespace BaseURL is
// rejected so a misconfigured adapter never silently no-ops.
func TestNewBlankBaseURLRejected(t *testing.T) {
	t.Parallel()
	if _, err := ionet.New(ionet.Config{BaseURL: "   "}); !errors.Is(err, ionet.ErrNoEndpoint) {
		t.Fatalf("err = %v, want ErrNoEndpoint", err)
	}
}

// TestNewDefaultsBaseURL proves an empty BaseURL resolves to the real default
// (no error) without contacting it.
func TestNewDefaultsBaseURL(t *testing.T) {
	t.Parallel()
	p, err := ionet.New(ionet.Config{Capacity: 1})
	if err != nil {
		t.Fatalf("New default: %v", err)
	}
	if p == nil {
		t.Fatal("New returned nil provider")
	}
}
