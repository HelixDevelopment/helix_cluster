package gitops

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// threeCells is the closure-criterion fixture: 3 registered cells across the
// three rollout tiers (one canary, plus enough tier-2/tier-1 cells to make the
// 50%/25% chunking observable). The first three-cell subset models the literal
// "3 registered cells" criterion; the larger fixtures exercise the chunking math.
func threeCells() []Cell {
	return []Cell{
		{Name: "cell-canary", Server: "https://canary.k8s", Tier: TierCanary},
		{Name: "cell-t2-a", Server: "https://t2a.k8s", Tier: TierTwo},
		{Name: "cell-t1-a", Server: "https://t1a.k8s", Tier: TierOne},
	}
}

func baseTemplate() ApplicationSetTemplate {
	return ApplicationSetTemplate{
		Project:        "helix-fed",
		RepoURL:        "https://git.example/helix/apps.git",
		TargetRevision: "main",
		BasePath:       "apps/threetier",
		OverlayDir:     "overlays",
		DestNamespace:  "helix-system",
		SyncPolicy:     AutomatedPruneSelfHeal(),
	}
}

// ---------------------------------------------------------------------------
// (a) ApplicationSet expands to the correct N per-cell Applications with correct
//     overlays.
// ---------------------------------------------------------------------------

func TestGenerate_PerCellApplications(t *testing.T) {
	const runUUID = "1367a000-0000-4000-8000-0000000000a1"
	tmpl := baseTemplate()
	cells := threeCells()

	apps, err := tmpl.Generate(cells)
	if err != nil {
		t.Fatalf("[%s] Generate error: %v", runUUID, err)
	}
	if len(apps) != len(cells) {
		t.Fatalf("[%s] generated %d apps want %d (one per cell)", runUUID, len(apps), len(cells))
	}

	// Index generated apps by cell name and assert the per-cell overlay path,
	// destination server, project, revision and syncPolicy are all correct.
	byCell := map[string]Application{}
	for _, a := range apps {
		byCell[a.Metadata.Labels[LabelCell]] = a
	}
	for _, c := range cells {
		app, ok := byCell[c.Name]
		if !ok {
			t.Fatalf("[%s] no Application generated for cell %q", runUUID, c.Name)
		}
		wantPath := "apps/threetier/overlays/" + c.Name
		if app.Spec.Source.Path != wantPath {
			t.Fatalf("[%s] cell %q overlay path=%q want %q", runUUID, c.Name, app.Spec.Source.Path, wantPath)
		}
		if app.Spec.Source.RepoURL != tmpl.RepoURL {
			t.Fatalf("[%s] cell %q repoURL=%q want %q", runUUID, c.Name, app.Spec.Source.RepoURL, tmpl.RepoURL)
		}
		if app.Spec.Source.TargetRevision != "main" {
			t.Fatalf("[%s] cell %q revision=%q want main", runUUID, c.Name, app.Spec.Source.TargetRevision)
		}
		if app.Spec.Destination.Server != c.Server {
			t.Fatalf("[%s] cell %q dest server=%q want %q", runUUID, c.Name, app.Spec.Destination.Server, c.Server)
		}
		if app.Spec.Destination.Namespace != "helix-system" {
			t.Fatalf("[%s] cell %q namespace=%q want helix-system", runUUID, c.Name, app.Spec.Destination.Namespace)
		}
		if app.Spec.Project != "helix-fed" {
			t.Fatalf("[%s] cell %q project=%q want helix-fed", runUUID, c.Name, app.Spec.Project)
		}
		if app.Metadata.Labels[LabelTier] != string(c.Tier) {
			t.Fatalf("[%s] cell %q tier label=%q want %q", runUUID, c.Name, app.Metadata.Labels[LabelTier], c.Tier)
		}
		// syncPolicy automated prune + selfHeal both on.
		if app.Spec.SyncPolicy.Automated == nil {
			t.Fatalf("[%s] cell %q syncPolicy.automated missing", runUUID, c.Name)
		}
		if !app.Spec.SyncPolicy.Automated.Prune || !app.Spec.SyncPolicy.Automated.SelfHeal {
			t.Fatalf("[%s] cell %q automated=%+v want prune+selfHeal", runUUID, c.Name, *app.Spec.SyncPolicy.Automated)
		}
	}

	// CROSS-CELL DISTINCTNESS: the overlay path MUST differ per cell (this is the
	// mutation guard — if Generate bound a constant path, all paths would match
	// and this fails).
	if byCell["cell-canary"].Spec.Source.Path == byCell["cell-t2-a"].Spec.Source.Path {
		t.Fatalf("[%s] per-cell overlays not distinct: both %q", runUUID, byCell["cell-canary"].Spec.Source.Path)
	}
	t.Logf("[%s] before: 1 ApplicationSet template + %d cells -> after: %d distinct per-cell Applications with overlays",
		runUUID, len(cells), len(apps))
}

func TestGenerate_EmptyCellsYieldsEmpty(t *testing.T) {
	const runUUID = "1367a000-0000-4000-8000-0000000000a2"
	apps, err := baseTemplate().Generate(nil)
	if err != nil {
		t.Fatalf("[%s] Generate(nil) error: %v", runUUID, err)
	}
	if apps == nil || len(apps) != 0 {
		t.Fatalf("[%s] Generate(nil)=%v want empty non-nil slice", runUUID, apps)
	}
}

func TestGenerate_DuplicateCellRejected(t *testing.T) {
	const runUUID = "1367a000-0000-4000-8000-0000000000a3"
	cells := []Cell{
		{Name: "dup", Server: "https://a", Tier: TierOne},
		{Name: "dup", Server: "https://b", Tier: TierOne},
	}
	if _, err := baseTemplate().Generate(cells); err == nil {
		t.Fatalf("[%s] duplicate cell name accepted; want error", runUUID)
	}
}

func TestGenerate_MissingRepoURLRejected(t *testing.T) {
	const runUUID = "1367a000-0000-4000-8000-0000000000a4"
	tmpl := baseTemplate()
	tmpl.RepoURL = ""
	if _, err := tmpl.Generate(threeCells()); err == nil {
		t.Fatalf("[%s] missing RepoURL accepted; want error", runUUID)
	}
}

// TestGenerate_MarshalsToArgoYAMLAndJSON proves the generated Application
// marshals to the ArgoCD custom-resource shape in BOTH JSON and YAML (the two
// vendored encoders) with the load-bearing fields present.
func TestGenerate_MarshalsToArgoYAMLAndJSON(t *testing.T) {
	const runUUID = "1367a000-0000-4000-8000-0000000000a5"
	apps, err := baseTemplate().Generate(threeCells()[:1])
	if err != nil {
		t.Fatalf("[%s] Generate error: %v", runUUID, err)
	}
	app := apps[0]

	jb, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("[%s] json marshal: %v", runUUID, err)
	}
	js := string(jb)
	for _, want := range []string{`"apiVersion":"argoproj.io/v1alpha1"`, `"kind":"Application"`, `"selfHeal":true`, `"prune":true`} {
		if !strings.Contains(js, want) {
			t.Fatalf("[%s] JSON missing %q; got %s", runUUID, want, js)
		}
	}

	yb, err := yaml.Marshal(app)
	if err != nil {
		t.Fatalf("[%s] yaml marshal: %v", runUUID, err)
	}
	ys := string(yb)
	for _, want := range []string{"kind: Application", "selfHeal: true", "prune: true"} {
		if !strings.Contains(ys, want) {
			t.Fatalf("[%s] YAML missing %q; got\n%s", runUUID, want, ys)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) RollingSync produces the correct canary->tier2->tier1 ordering.
// ---------------------------------------------------------------------------

// TestPlanRollingSync_TierOrder proves the rollout always sequences
// canary -> tier-2 -> tier-1 REGARDLESS of input order. This is the load-bearing
// guard: shuffling the cells must not let tier-1 (production) go before canary.
func TestPlanRollingSync_TierOrder(t *testing.T) {
	const runUUID = "1367b000-0000-4000-8000-0000000000b1"
	// Deliberately list tier-1 first to prove the planner re-orders.
	cells := []Cell{
		{Name: "p1", Server: "s", Tier: TierOne},
		{Name: "p2", Server: "s", Tier: TierOne},
		{Name: "p3", Server: "s", Tier: TierOne},
		{Name: "p4", Server: "s", Tier: TierOne},
		{Name: "canary1", Server: "s", Tier: TierCanary},
		{Name: "t2a", Server: "s", Tier: TierTwo},
		{Name: "t2b", Server: "s", Tier: TierTwo},
	}
	apps, err := baseTemplate().Generate(cells)
	if err != nil {
		t.Fatalf("[%s] Generate: %v", runUUID, err)
	}
	plan := PlanRollingSync(apps)
	order := plan.TierOrder()
	if len(order) == 0 {
		t.Fatalf("[%s] empty plan", runUUID)
	}
	// First wave MUST be canary.
	if order[0] != TierCanary {
		t.Fatalf("[%s] first wave tier=%q want canary (production must not go first)", runUUID, order[0])
	}
	// Tiers must be non-decreasing in the fixed order canary<tier2<tier1.
	rank := map[Tier]int{TierCanary: 0, TierTwo: 1, TierOne: 2}
	last := -1
	for i, tr := range order {
		r := rank[tr]
		if r < last {
			t.Fatalf("[%s] wave %d tier %q (rank %d) precedes a later-ranked tier (last=%d); ordering violated: %v",
				runUUID, i, tr, r, last, order)
		}
		last = r
	}
	t.Logf("[%s] before: cells listed tier1-first -> after: wave tier order %v (canary first)", runUUID, order)
}

// TestPlanRollingSync_Chunking proves tier-2 advances in 50% chunks and tier-1
// in 25% chunks (ceil, min 1), and that every app appears exactly once.
func TestPlanRollingSync_Chunking(t *testing.T) {
	const runUUID = "1367b000-0000-4000-8000-0000000000b2"
	cells := []Cell{
		{Name: "canary1", Server: "s", Tier: TierCanary},
	}
	// 4 tier-2 cells -> 50% -> chunks of 2 -> 2 waves.
	for _, n := range []string{"t2-1", "t2-2", "t2-3", "t2-4"} {
		cells = append(cells, Cell{Name: n, Server: "s", Tier: TierTwo})
	}
	// 4 tier-1 cells -> 25% -> chunks of 1 -> 4 waves.
	for _, n := range []string{"t1-1", "t1-2", "t1-3", "t1-4"} {
		cells = append(cells, Cell{Name: n, Server: "s", Tier: TierOne})
	}
	apps, err := baseTemplate().Generate(cells)
	if err != nil {
		t.Fatalf("[%s] Generate: %v", runUUID, err)
	}
	plan := PlanRollingSync(apps)

	var canaryWaves, t2Waves, t1Waves int
	seen := map[string]int{}
	for _, w := range plan.Waves {
		switch w.Tier {
		case TierCanary:
			canaryWaves++
			if len(w.Apps) != 1 {
				t.Fatalf("[%s] canary wave size=%d want 1", runUUID, len(w.Apps))
			}
		case TierTwo:
			t2Waves++
			if len(w.Apps) != 2 {
				t.Fatalf("[%s] tier-2 wave size=%d want 2 (50%% of 4)", runUUID, len(w.Apps))
			}
		case TierOne:
			t1Waves++
			if len(w.Apps) != 1 {
				t.Fatalf("[%s] tier-1 wave size=%d want 1 (25%% of 4)", runUUID, len(w.Apps))
			}
		}
		for _, a := range w.Apps {
			seen[a]++
		}
	}
	if canaryWaves != 1 || t2Waves != 2 || t1Waves != 4 {
		t.Fatalf("[%s] waves canary=%d tier2=%d tier1=%d want 1/2/4", runUUID, canaryWaves, t2Waves, t1Waves)
	}
	if len(seen) != len(apps) {
		t.Fatalf("[%s] %d distinct apps in waves want %d", runUUID, len(seen), len(apps))
	}
	for name, n := range seen {
		if n != 1 {
			t.Fatalf("[%s] app %q appears %d times in waves want exactly 1", runUUID, name, n)
		}
	}
	t.Logf("[%s] before: 1 canary + 4 tier2 + 4 tier1 -> after: waves 1/2/4 (50%%/25%% chunking)", runUUID)
}

// ---------------------------------------------------------------------------
// (c) Drift detection flags a removed resource for prune.
// ---------------------------------------------------------------------------

// TestDetectDrift_PrunesRemovedResource is the load-bearing drift/self-heal
// guard: a resource present LIVE but removed from the DESIRED (Git) set must be
// reported in ToPrune. If DetectDrift's "live not in desired" branch is removed,
// ToPrune is empty and this fails.
func TestDetectDrift_PrunesRemovedResource(t *testing.T) {
	const runUUID = "1367c000-0000-4000-8000-0000000000c1"
	desired := []Resource{
		{Group: "apps", Kind: "Deployment", Namespace: "helix-system", Name: "api"},
		{Group: "", Kind: "Service", Namespace: "helix-system", Name: "api-svc"},
	}
	// Live still has an old ConfigMap that was removed from Git.
	live := []Resource{
		{Group: "apps", Kind: "Deployment", Namespace: "helix-system", Name: "api"},
		{Group: "", Kind: "Service", Namespace: "helix-system", Name: "api-svc"},
		{Group: "", Kind: "ConfigMap", Namespace: "helix-system", Name: "legacy-config"},
	}
	rep := DetectDrift(desired, live)
	if rep.InSync() {
		t.Fatalf("[%s] report claims in-sync but a Git-removed resource is still live", runUUID)
	}
	if len(rep.ToPrune) != 1 {
		t.Fatalf("[%s] ToPrune=%v want exactly the legacy ConfigMap", runUUID, rep.ToPrune)
	}
	pr := rep.ToPrune[0]
	if pr.Kind != "ConfigMap" || pr.Name != "legacy-config" {
		t.Fatalf("[%s] pruned resource=%+v want legacy ConfigMap", runUUID, pr)
	}
	if len(rep.Missing) != 0 {
		t.Fatalf("[%s] Missing=%v want none", runUUID, rep.Missing)
	}
	t.Logf("[%s] before: desired=%d live=%d (legacy ConfigMap removed from Git) -> after: ToPrune=%+v",
		runUUID, len(desired), len(live), rep.ToPrune)
}

func TestDetectDrift_MissingAndInSync(t *testing.T) {
	const runUUID = "1367c000-0000-4000-8000-0000000000c2"
	dep := Resource{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "x"}
	svc := Resource{Group: "", Kind: "Service", Namespace: "ns", Name: "y"}

	// Desired has svc not yet live -> Missing.
	rep := DetectDrift([]Resource{dep, svc}, []Resource{dep})
	if len(rep.Missing) != 1 || rep.Missing[0] != svc {
		t.Fatalf("[%s] Missing=%v want [svc]", runUUID, rep.Missing)
	}
	if len(rep.ToPrune) != 0 {
		t.Fatalf("[%s] ToPrune=%v want none", runUUID, rep.ToPrune)
	}

	// Exact match -> in sync.
	inSync := DetectDrift([]Resource{dep, svc}, []Resource{svc, dep})
	if !inSync.InSync() {
		t.Fatalf("[%s] identical sets reported drift: %+v", runUUID, inSync)
	}
}

// ---------------------------------------------------------------------------
// (d) The client POSTs correctly against an httptest fake ArgoCD API and emits a
//     per-run UUID.
// ---------------------------------------------------------------------------

// fakeArgoCD is a LOCAL httptest emulator of the ArgoCD application REST API. It
// is not a mock-only stub: it RECORDS every upserted Application body and the
// per-run UUID header sink-side, so tests prove what was actually sent over the
// wire and in what order.
type fakeArgoCD struct {
	mu          sync.Mutex
	upserts     []Application // in wire order
	runIDHeader []string      // X-Helix-Run-Id seen per request
	managed     map[string][]Resource
}

func (f *fakeArgoCD) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/applications", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("upsert") != "true" {
			http.Error(w, "missing upsert=true", http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var app Application
		if err := json.Unmarshal(raw, &app); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.upserts = append(f.upserts, app)
		f.runIDHeader = append(f.runIDHeader, r.Header.Get("X-Helix-Run-Id"))
		f.mu.Unlock()
		// Echo the stored application back (ArgoCD returns the persisted object).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(app)
	})
	mux.HandleFunc("/applications/", func(w http.ResponseWriter, r *http.Request) {
		// .../{name}/managed-resources
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/applications/"), "/managed-resources")
		f.mu.Lock()
		items := f.managed[name]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Items []Resource `json:"items"`
		}{Items: items})
	})
	return mux
}

func TestClient_ApplyApplicationSet_PostsAndEmitsUUID(t *testing.T) {
	const runUUID = "1367d000-0000-4000-8000-0000000000d1"
	fake := &fakeArgoCD{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	cli, err := NewClient(Config{BaseURL: srv.URL, Token: "tok", RunID: runUUID})
	if err != nil {
		t.Fatalf("[%s] NewClient: %v", runUUID, err)
	}
	if cli.RunID() != runUUID {
		t.Fatalf("[%s] RunID()=%q want fixed %q", runUUID, cli.RunID(), runUUID)
	}

	cells := threeCells()
	apps, _ := baseTemplate().Generate(cells)
	plan := PlanRollingSync(apps)

	res, err := cli.ApplyApplicationSet(context.Background(), baseTemplate(), cells, &plan)
	if err != nil {
		t.Fatalf("[%s] ApplyApplicationSet: %v", runUUID, err)
	}

	// Sink-side: server received exactly one upsert per cell.
	fake.mu.Lock()
	gotUpserts := append([]Application(nil), fake.upserts...)
	gotHeaders := append([]string(nil), fake.runIDHeader...)
	fake.mu.Unlock()

	if len(gotUpserts) != len(cells) {
		t.Fatalf("[%s] server got %d upserts want %d", runUUID, len(gotUpserts), len(cells))
	}
	if len(res.Applied) != len(cells) {
		t.Fatalf("[%s] Applied=%v want %d entries", runUUID, res.Applied, len(cells))
	}
	if res.RunID != runUUID {
		t.Fatalf("[%s] result RunID=%q want %q", runUUID, res.RunID, runUUID)
	}

	// Sink-side: the per-run UUID was stamped on EVERY request header.
	for i, h := range gotHeaders {
		if h != runUUID {
			t.Fatalf("[%s] request %d X-Helix-Run-Id=%q want %q", runUUID, i, h, runUUID)
		}
	}

	// Sink-side: the WIRE ORDER matches the rolling-sync plan (canary first).
	wantOrder := make([]string, 0, len(apps))
	for _, wv := range plan.Waves {
		wantOrder = append(wantOrder, wv.Apps...)
	}
	gotOrder := make([]string, len(gotUpserts))
	for i, a := range gotUpserts {
		gotOrder[i] = a.Metadata.Name
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("[%s] wire upsert order=%v want plan order=%v", runUUID, gotOrder, wantOrder)
	}
	// First applied app must be the canary cell.
	if gotOrder[0] != "cell-canary" {
		t.Fatalf("[%s] first upsert=%q want cell-canary (canary-first)", runUUID, gotOrder[0])
	}

	// Sink-side: the upserted body carried the correct per-cell overlay + syncPolicy.
	for _, a := range gotUpserts {
		if a.Spec.SyncPolicy.Automated == nil || !a.Spec.SyncPolicy.Automated.Prune || !a.Spec.SyncPolicy.Automated.SelfHeal {
			t.Fatalf("[%s] upserted app %q missing prune+selfHeal", runUUID, a.Metadata.Name)
		}
		if !strings.HasSuffix(a.Spec.Source.Path, "/"+a.Metadata.Name) {
			t.Fatalf("[%s] upserted app %q overlay path=%q not per-cell", runUUID, a.Metadata.Name, a.Spec.Source.Path)
		}
	}
	t.Logf("[%s] before: ApplyApplicationSet(3 cells, plan) -> after: %d wire upserts in order %v, UUID stamped on all",
		runUUID, len(gotUpserts), gotOrder)
}

// TestClient_LiveResources_DriftPrune drives the END-TO-END self-heal detection
// against the fake API: the server reports a live ConfigMap that was removed from
// Git, the client fetches live resources over HTTP, and DetectDrift flags it for
// prune.
func TestClient_LiveResources_DriftPrune(t *testing.T) {
	const runUUID = "1367d000-0000-4000-8000-0000000000d2"
	dep := Resource{Group: "apps", Kind: "Deployment", Namespace: "helix-system", Name: "api"}
	stale := Resource{Group: "", Kind: "ConfigMap", Namespace: "helix-system", Name: "legacy"}
	fake := &fakeArgoCD{managed: map[string][]Resource{
		"cell-canary": {dep, stale}, // live still has the stale CM
	}}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	cli, err := NewClient(Config{BaseURL: srv.URL, RunID: runUUID})
	if err != nil {
		t.Fatalf("[%s] NewClient: %v", runUUID, err)
	}
	live, err := cli.LiveResources(context.Background(), "cell-canary")
	if err != nil {
		t.Fatalf("[%s] LiveResources: %v", runUUID, err)
	}
	desired := []Resource{dep} // Git no longer declares the ConfigMap
	rep := DetectDrift(desired, live)
	if len(rep.ToPrune) != 1 || rep.ToPrune[0] != stale {
		t.Fatalf("[%s] ToPrune=%v want [legacy ConfigMap]", runUUID, rep.ToPrune)
	}
	t.Logf("[%s] before: GET managed-resources (live has legacy CM) -> after: drift ToPrune=%+v", runUUID, rep.ToPrune)
}

func TestClient_ApplyFailureSurfaced(t *testing.T) {
	const runUUID = "1367d000-0000-4000-8000-0000000000d3"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli, err := NewClient(Config{BaseURL: srv.URL, RunID: runUUID})
	if err != nil {
		t.Fatalf("[%s] NewClient: %v", runUUID, err)
	}
	apps, _ := baseTemplate().Generate(threeCells()[:1])
	if _, err := cli.ApplyApplication(context.Background(), apps[0]); err == nil {
		t.Fatalf("[%s] 500 from server accepted as success; want ErrApplyFailed", runUUID)
	}
}

func TestNewClient_EmptyBaseURL(t *testing.T) {
	const runUUID = "1367d000-0000-4000-8000-0000000000d4"
	if _, err := NewClient(Config{}); err != ErrNoEndpoint {
		t.Fatalf("[%s] NewClient(empty)=%v want ErrNoEndpoint", runUUID, err)
	}
}

// TestNewRunID_DistinctV4 proves NewRunID mints distinct, well-formed v4 UUIDs.
func TestNewRunID_DistinctV4(t *testing.T) {
	const runUUID = "1367d000-0000-4000-8000-0000000000d5"
	seen := map[string]struct{}{}
	for i := 0; i < 1000; i++ {
		id := NewRunID()
		if len(id) != 36 || id[14] != '4' {
			t.Fatalf("[%s] malformed v4 UUID %q", runUUID, id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("[%s] duplicate UUID %q in 1000 mints", runUUID, id)
		}
		seen[id] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// Strictly-live capture (HONEST SKIP per CLAUDE-1 / §11.4.3).
// ---------------------------------------------------------------------------

// TestLiveArgoCD_ThreeCellRolloutCapture is the strictly-live portion of the
// closure criterion: a real ArgoCD with 3 registered cells, a real Git commit of
// a 3-tier app, and captured UI sync-status screenshots/logs. That requires a
// running ArgoCD, three Kubernetes clusters, and a Git server — none available in
// this hermetic unit environment. The full federation LOGIC (generation, rolling
// order, drift/prune, real HTTP apply) is proven by the deterministic tests above
// against an httptest ArgoCD; only the live UI evidence capture is skipped, and
// it is skipped HONESTLY rather than faked as a PASS.
func TestLiveArgoCD_ThreeCellRolloutCapture(t *testing.T) {
	t.Skip("requires live ArgoCD + 3 registered Kubernetes cells + Git server and UI screenshot capture: out-of-host; logic proven against httptest fake in the tests above")
}
