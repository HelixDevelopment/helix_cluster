package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/chutes"
)

// findCheck returns the DependencyResult with the given name, or fails the test.
func findCheck(t *testing.T, rollup Rollup, name string) DependencyResult {
	t.Helper()
	for _, c := range rollup.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in rollup checks %+v", name, rollup.Checks)
	return DependencyResult{}
}

// --- run-UUID ---

func TestNewRunID_UniqueAndFormatted(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := NewRunID()
		if id == "" {
			t.Fatalf("NewRunID returned empty")
		}
		// canonical 8-4-4-4-12 hex UUID, 36 chars
		if len(id) != 36 {
			t.Fatalf("NewRunID len = %d, want 36 (%q)", len(id), id)
		}
		parts := strings.Split(id, "-")
		if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 ||
			len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
			t.Fatalf("NewRunID not canonical UUID shape: %q", id)
		}
		if seen[id] {
			t.Fatalf("NewRunID produced duplicate: %q", id)
		}
		seen[id] = true
	}
}

// --- miner-api Deployment check ---

// TestMinerAPICheck_HealthyThenUnhealthy is the core closure test. It brings a
// fake miner-api up (healthy), registers the miner-api dep into the rollup,
// asserts the rollup is Healthy, then 500s the miner-api and asserts the rollup
// is Unhealthy WITH the specific failing check named, emitting a per-run UUID.
func TestMinerAPICheck_HealthyThenUnhealthy(t *testing.T) {
	var down atomic.Bool

	// Fake miner-api: serves 200 on /livez until "killed" (down), then 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rollup := NewRollup()
	RegisterMinerAPI(rollup, MinerAPIConfig{
		Name:     "chutes-miner-api",
		Endpoint: srv.URL + "/livez",
		Kind:     Liveness,
		Critical: true,
	})

	ctx := context.Background()

	// --- BEFORE-DELTA: with miner up, rollup is healthy ---
	before := rollup.CheckLiveness(ctx)
	if before.Status != Healthy {
		t.Fatalf("with miner up: overall = %s, want healthy (%+v)", before.Status, before)
	}
	beforeCheck := findCheck(t, before, "chutes-miner-api")
	if beforeCheck.Status != Healthy {
		t.Fatalf("with miner up: chutes-miner-api = %s, want healthy", beforeCheck.Status)
	}

	runID := NewRunID()
	t.Logf("run-id=%s miner-api up: overall=%s", runID, before.Status)

	// --- kill the miner-api ---
	down.Store(true)

	// --- AFTER-DELTA: rollup is unhealthy WITH the specific failing check named ---
	after := rollup.CheckLiveness(ctx)
	if after.Status != Unhealthy {
		t.Fatalf("after killing miner-api: overall = %s, want unhealthy (%+v)", after.Status, after)
	}
	afterCheck := findCheck(t, after, "chutes-miner-api")
	if afterCheck.Status != Unhealthy {
		t.Fatalf("after killing miner-api: chutes-miner-api = %s, want unhealthy", afterCheck.Status)
	}
	if afterCheck.Error == "" {
		t.Fatalf("after killing miner-api: expected a non-empty error naming the failure")
	}
	t.Logf("run-id=%s miner-api down: overall=%s failing-check=%q error=%q",
		runID, after.Status, afterCheck.Name, afterCheck.Error)
}

func TestMinerAPICheck_ConnectionRefused(t *testing.T) {
	// Point at a server we immediately close so the dial fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL + "/livez"
	srv.Close()

	rollup := NewRollup()
	RegisterMinerAPI(rollup, MinerAPIConfig{
		Name:     "chutes-miner-api",
		Endpoint: url,
		Kind:     Liveness,
		Critical: true,
	})

	res := findCheck(t, rollup.CheckLiveness(context.Background()), "chutes-miner-api")
	if res.Status != Unhealthy {
		t.Fatalf("connection refused: status = %s, want unhealthy", res.Status)
	}
	if res.Error == "" {
		t.Fatalf("connection refused: expected non-empty error")
	}
}

func TestMinerAPICheck_DefaultName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rollup := NewRollup()
	RegisterMinerAPI(rollup, MinerAPIConfig{Endpoint: srv.URL})

	res := findCheck(t, rollup.CheckReadiness(context.Background()), defaultMinerAPIName)
	if res.Status != Healthy {
		t.Fatalf("default-name healthy miner: status = %s, want healthy", res.Status)
	}
}

// --- GraVal attestation-readiness check ---

func TestGraValCheck_AttestationReady(t *testing.T) {
	attestor, err := chutes.NewHMACAttestor([]byte("shared-secret"))
	if err != nil {
		t.Fatalf("new attestor: %v", err)
	}

	// honest node: computes the correct proof -> attestation passes.
	probe := func(ctx context.Context) (chutes.AttestationResult, error) {
		c, err := attestor.Challenge("gpu-node-1")
		if err != nil {
			return chutes.AttestationResult{}, err
		}
		r, err := attestor.Respond(c)
		if err != nil {
			return chutes.AttestationResult{}, err
		}
		return chutes.AttestationResult{Challenge: c, Response: r}, nil
	}

	rollup := NewRollup()
	RegisterGraValAttestation(rollup, GraValConfig{
		Name:     "graval-bootstrap",
		Attestor: attestor,
		Probe:    probe,
		Critical: true,
	})

	res := findCheck(t, rollup.CheckReadiness(context.Background()), "graval-bootstrap")
	if res.Status != Healthy {
		t.Fatalf("honest attestation: status = %s, want healthy (err=%q)", res.Status, res.Error)
	}
}

func TestGraValCheck_AttestationFailsNamed(t *testing.T) {
	attestor, _ := chutes.NewHMACAttestor([]byte("shared-secret"))
	imposter, _ := chutes.NewHMACAttestor([]byte("WRONG-secret"))

	// node computes proof with the WRONG secret -> verification fails.
	probe := func(ctx context.Context) (chutes.AttestationResult, error) {
		c, _ := attestor.Challenge("gpu-node-1")
		r, _ := imposter.Respond(c)
		return chutes.AttestationResult{Challenge: c, Response: r}, nil
	}

	rollup := NewRollup()
	RegisterGraValAttestation(rollup, GraValConfig{
		Name:     "graval-bootstrap",
		Attestor: attestor,
		Probe:    probe,
		Critical: true,
	})

	overall := rollup.CheckReadiness(context.Background())
	if overall.Status != Unhealthy {
		t.Fatalf("bad attestation: overall = %s, want unhealthy", overall.Status)
	}
	res := findCheck(t, overall, "graval-bootstrap")
	if res.Status != Unhealthy {
		t.Fatalf("bad attestation: status = %s, want unhealthy", res.Status)
	}
	if res.Error == "" {
		t.Fatalf("bad attestation: expected non-empty error naming the failure")
	}
}

func TestGraValCheck_ProbeError(t *testing.T) {
	attestor, _ := chutes.NewHMACAttestor([]byte("s"))
	probe := func(ctx context.Context) (chutes.AttestationResult, error) {
		return chutes.AttestationResult{}, context.Canceled
	}
	rollup := NewRollup()
	RegisterGraValAttestation(rollup, GraValConfig{Attestor: attestor, Probe: probe, Critical: false})

	res := findCheck(t, rollup.CheckReadiness(context.Background()), defaultGraValName)
	if res.Status != Unhealthy {
		t.Fatalf("probe error: status = %s, want unhealthy", res.Status)
	}
}

// --- combined cluster rollup: miner liveness + attestation readiness ---

func TestClusterHealth_MinerAndGraVal_Delta(t *testing.T) {
	var minerDown atomic.Bool
	miner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if minerDown.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer miner.Close()

	attestor, _ := chutes.NewHMACAttestor([]byte("shared-secret"))
	probe := func(ctx context.Context) (chutes.AttestationResult, error) {
		c, _ := attestor.Challenge("gpu-node-1")
		r, _ := attestor.Respond(c)
		return chutes.AttestationResult{Challenge: c, Response: r}, nil
	}

	rollup := NewRollup()
	RegisterMinerAPI(rollup, MinerAPIConfig{
		Name: "chutes-miner-api", Endpoint: miner.URL, Kind: Liveness, Critical: true,
	})
	RegisterGraValAttestation(rollup, GraValConfig{
		Name: "graval-bootstrap", Attestor: attestor, Probe: probe, Kind: Liveness, Critical: true,
	})

	ctx := context.Background()
	runID := NewRunID()

	// BEFORE: both healthy.
	before := rollup.CheckLiveness(ctx)
	if before.Status != Healthy {
		t.Fatalf("cluster before: %s, want healthy (%+v)", before.Status, before)
	}
	t.Logf("run-id=%s cluster(before)=%s checks=%d", runID, before.Status, len(before.Checks))

	// kill miner-api only.
	minerDown.Store(true)
	after := rollup.CheckLiveness(ctx)
	if after.Status != Unhealthy {
		t.Fatalf("cluster after: %s, want unhealthy", after.Status)
	}
	// GraVal still healthy, miner-api is the named failing check.
	if g := findCheck(t, after, "graval-bootstrap"); g.Status != Healthy {
		t.Fatalf("graval after miner kill: %s, want healthy", g.Status)
	}
	m := findCheck(t, after, "chutes-miner-api")
	if m.Status != Unhealthy {
		t.Fatalf("miner-api after kill: %s, want unhealthy", m.Status)
	}
	t.Logf("run-id=%s cluster(after)=%s failing-check=%q error=%q", runID, after.Status, m.Name, m.Error)
}

func TestCaptureLiveness_StampsRunIDAndNamesFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rollup := NewRollup()
	RegisterMinerAPI(rollup, MinerAPIConfig{
		Name: "chutes-miner-api", Endpoint: srv.URL, Kind: Liveness, Critical: true,
	})

	cap1 := CaptureLiveness(context.Background(), rollup)
	cap2 := CaptureLiveness(context.Background(), rollup)
	if cap1.RunID == "" || cap2.RunID == "" {
		t.Fatalf("captured run ids must be non-empty")
	}
	if cap1.RunID == cap2.RunID {
		t.Fatalf("two captures shared a run id: %q", cap1.RunID)
	}
	if cap1.Rollup.Status != Unhealthy {
		t.Fatalf("captured status = %s, want unhealthy", cap1.Rollup.Status)
	}
	failing := FailingChecks(cap1.Rollup)
	if len(failing) != 1 || failing[0] != "chutes-miner-api" {
		t.Fatalf("FailingChecks = %v, want [chutes-miner-api]", failing)
	}
}

func TestMinerAPICheck_NoEndpoint(t *testing.T) {
	rollup := NewRollup()
	RegisterMinerAPI(rollup, MinerAPIConfig{Name: "m", Kind: Readiness, Critical: true})
	res := findCheck(t, rollup.CheckReadiness(context.Background()), "m")
	if res.Status != Unhealthy || res.Error == "" {
		t.Fatalf("no-endpoint: status=%s err=%q, want unhealthy+error", res.Status, res.Error)
	}
}

func TestGraValCheck_NoAttestor(t *testing.T) {
	rollup := NewRollup()
	RegisterGraValAttestation(rollup, GraValConfig{
		Name:  "g",
		Probe: func(ctx context.Context) (chutes.AttestationResult, error) { return chutes.AttestationResult{}, nil },
	})
	res := findCheck(t, rollup.CheckReadiness(context.Background()), "g")
	if res.Status != Unhealthy {
		t.Fatalf("no-attestor: status=%s, want unhealthy", res.Status)
	}
}
