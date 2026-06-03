// Package ionet ships an IONetProvider: an adapter over the io.net (IO Cloud)
// REST API that rents decentralized GPU clusters. It deploys a GPU cluster from
// a ClusterSpec, health-checks a deployed cluster by its run-UUID, and maps the
// io.net deploy/status/terminate lifecycle onto the pkg/pool.GPUProvider
// Provision/Release/List/Capacity contract used by the sibling provider
// adapters (runpod, aws, chutes).
//
// SCOPE: this adapter rents raw GPU clusters from the io.net REST control plane.
// DeployCluster issues a deploy request and returns the io.net cluster_id
// (a run-UUID) read OFF the response; HealthCheck polls the cluster status
// endpoint and returns the parsed health. Higher layers (scheduler,
// marketplace) consume the typed cluster handle and health; this package owns
// only the deploy/health/terminate lifecycle over the wire.
//
// HONEST EXTERNAL SEAM (CLAUDE-1 / Constitution §11.4.3): the live io.net GPU
// cloud — a real io.net account, a real decentralized GPU cluster, and real
// worker health — is OUT OF SCOPE for this package and is NOT contacted. We add
// NO io.net SDK (or any) dependency: the REST control API is spoken over
// net/http against a configurable BaseURL (default cloud.io.net/api/v1) +
// injectable *http.Client + bearer Token. Tests drive every method end to end
// against a local net/http/httptest server that emulates deploy/status/
// terminate; pointing BaseURL at the real io.net endpoint with a real API key
// is the only change needed to drive production. The cluster_id and health
// status are PARSED from the response, never fabricated locally.
package ionet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/pool"
)

// DefaultBaseURL is the real io.net (IO Cloud) REST control-API root. It is only
// a default for the HTTP client; tests point BaseURL at a local httptest server,
// so this address is never contacted under test.
const DefaultBaseURL = "https://cloud.io.net/api/v1"

// ProviderName is the stable provider label used in logs and selection.
const ProviderName = "ionet"

// Sentinel errors let callers branch on the failure class with errors.Is.
var (
	// ErrNoEndpoint is returned by New when no BaseURL resolves (it cannot
	// happen with the default, but an explicitly-blanked BaseURL is rejected) so
	// a misconfigured adapter never silently no-ops.
	ErrNoEndpoint = errors.New("ionet: no endpoint configured")
	// ErrNoHTTPClient is returned by New when a nil *http.Client is injected, so
	// the adapter never panics on a nil client at request time.
	ErrNoHTTPClient = errors.New("ionet: no http client configured")
	// ErrEmptyGPUType is returned by DeployCluster/Provision when the spec has no
	// GPU type, so a meaningless (billable) deploy is refused before any call.
	ErrEmptyGPUType = errors.New("ionet: empty gpu type")
	// ErrDeployFailed wraps a non-2xx deploy response or an empty cluster_id in
	// an otherwise-2xx body; the returned cluster is the zero value, never a
	// fabricated one.
	ErrDeployFailed = errors.New("ionet: deploy failed")
	// ErrEmptyClusterID is returned by HealthCheck/Terminate when called with an
	// empty cluster id.
	ErrEmptyClusterID = errors.New("ionet: empty cluster id")
	// ErrClusterNotFound is returned when io.net reports the cluster does not
	// exist (HTTP 404). It wraps so errors.Is can detect it.
	ErrClusterNotFound = errors.New("ionet: cluster not found")
	// ErrAtCapacity is returned by Provision when the number of live clusters is
	// already at the configured Capacity quota.
	ErrAtCapacity = errors.New("ionet: at capacity")
	// ErrUnknownInstance is returned by Release for a cluster this adapter does
	// not currently hold, so double-release / wrong-provider bugs surface.
	ErrUnknownInstance = errors.New("ionet: release of unknown instance")
)

// ClusterSpec describes the GPU cluster to deploy on io.net. Every field maps
// onto an io.net deploy request field documented on DeployCluster.
type ClusterSpec struct {
	// GPUType is the requested accelerator model, e.g. "H100", "A100".
	GPUType string `json:"gpu_type"`
	// GPUCount is the number of GPUs to allocate across the cluster.
	GPUCount int `json:"gpu_count"`
	// Region is the io.net region/zone, e.g. "us-east".
	Region string `json:"region"`
	// Image is the container image the cluster runs.
	Image string `json:"image"`
	// Command is the entrypoint command for the image (optional).
	Command []string `json:"command,omitempty"`
	// Env is the environment passed to the container (optional).
	Env map[string]string `json:"env,omitempty"`
	// HourlyUSD is the advertised price per cluster-hour, carried through to the
	// pool.Instance for TCO math. It is not sent to io.net.
	HourlyUSD float64 `json:"-"`
}

// Cluster is a deployed io.net GPU cluster handle returned by DeployCluster.
// ID is the io.net cluster_id (a run-UUID); every field is parsed from the
// io.net response, never invented.
type Cluster struct {
	// ID is the io.net cluster_id / run-UUID, required for HealthCheck/Terminate.
	ID string `json:"cluster_id"`
	// GPUType echoes the deployed accelerator model.
	GPUType string `json:"gpu_type"`
	// GPUCount echoes the number of GPUs allocated.
	GPUCount int `json:"gpu_count"`
	// Status is the cluster status reported at deploy time, e.g. "provisioning".
	Status string `json:"status"`
}

// Health is the parsed result of HealthCheck for a deployed cluster.
type Health struct {
	// ClusterID echoes the run-UUID the health refers to.
	ClusterID string `json:"cluster_id"`
	// Status is the raw io.net status string, e.g. "healthy", "provisioning".
	Status string `json:"status"`
	// ReadyReplicas is how many cluster workers are reporting ready.
	ReadyReplicas int `json:"ready_replicas"`
	// TotalReplicas is the total worker count the cluster targets.
	TotalReplicas int `json:"total_replicas"`
}

// Healthy reports whether the cluster is in the io.net "healthy" state. It is a
// derived, sink-side view of the parsed Status so callers branch on one method
// rather than re-deriving the string compare.
func (h Health) Healthy() bool {
	return strings.EqualFold(strings.TrimSpace(h.Status), "healthy")
}

// Config configures an IONetProvider.
type Config struct {
	// BaseURL is the io.net REST root. Empty resolves to DefaultBaseURL; an
	// explicitly whitespace-only value is rejected with ErrNoEndpoint.
	BaseURL string
	// Token is the io.net API bearer token. Sent as Authorization: Bearer ...
	// when non-empty.
	Token string
	// HTTPClient is the injected client used for every request. When nil a
	// client with Timeout is built; Timeout defaults to 30s.
	HTTPClient *http.Client
	// Timeout is the per-request timeout applied via context when > 0.
	Timeout time.Duration
	// Capacity is the hard upper bound on simultaneously-live clusters this
	// adapter holds. A Provision beyond it fails with ErrAtCapacity. Zero or
	// negative fails closed (no deploys permitted) so an unset quota does not
	// deploy unbounded billable capacity.
	Capacity int
}

// IONetProvider is the io.net REST adapter. It implements pool.GPUProvider
// (Provision/Release/List/Capacity) and adds the io.net-specific DeployCluster
// and HealthCheck. It is safe for concurrent use; it tracks the clusters it has
// deployed so Capacity is enforced locally before a billable deploy is issued.
type IONetProvider struct {
	base    string
	token   string
	hc      *http.Client
	timeout time.Duration

	mu       sync.Mutex
	capacity int
	// deployed maps cluster_id -> the pool.Instance we returned, so Release/List
	// reconcile against what we actually deployed.
	deployed map[string]pool.Instance
	// reserved counts in-flight Provision calls that passed the Capacity gate but
	// have not yet inserted their cluster (the billable deploy runs lock-free).
	reserved int
}

// compile-time proof the adapter satisfies the pool seam AND the optional
// Name() capability some pool consumers select on.
var (
	_ pool.GPUProvider = (*IONetProvider)(nil)
	_ named            = (*IONetProvider)(nil)
)

// named is the optional Name() capability mirrored from the runpod sibling.
type named interface{ Name() string }

// New builds an IONetProvider from cfg. It resolves the base URL (default
// DefaultBaseURL), rejects an explicitly-blank BaseURL and a nil-but-required
// client config, and returns a ready provider. No network call is made here.
func New(cfg Config) (*IONetProvider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if strings.TrimSpace(base) == "" {
		return nil, ErrNoEndpoint
	}
	hc := cfg.HTTPClient
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	if hc == nil {
		return nil, ErrNoHTTPClient
	}
	capacity := cfg.Capacity
	if capacity < 0 {
		capacity = 0
	}
	return &IONetProvider{
		base:     strings.TrimRight(base, "/"),
		token:    cfg.Token,
		hc:       hc,
		timeout:  timeout,
		capacity: capacity,
		deployed: make(map[string]pool.Instance),
	}, nil
}

// Name returns the stable provider label.
func (p *IONetProvider) Name() string { return ProviderName }

// Capacity implements pool.GPUProvider: the hard upper bound on live clusters.
func (p *IONetProvider) Capacity() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.capacity
}

// deployRequest is the POST body sent to the io.net deploy endpoint.
type deployRequest struct {
	GPUType  string            `json:"gpu_type"`
	GPUCount int               `json:"gpu_count"`
	Region   string            `json:"region,omitempty"`
	Image    string            `json:"image,omitempty"`
	Command  []string          `json:"command,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

// DeployCluster deploys a GPU cluster on io.net and returns the deployed
// Cluster whose ID is the io.net cluster_id (run-UUID) read FROM the response.
//
// Wire protocol:
//
//	POST {base}/clusters  {deployRequest}  -> 200/201 {Cluster with cluster_id}
//
// A missing GPUType is refused (ErrEmptyGPUType) before any call. A non-2xx
// response or an empty cluster_id in a 2xx body is ErrDeployFailed — never a
// fabricated cluster id.
func (p *IONetProvider) DeployCluster(ctx context.Context, spec ClusterSpec) (Cluster, error) {
	if strings.TrimSpace(spec.GPUType) == "" {
		return Cluster{}, ErrEmptyGPUType
	}
	body, err := json.Marshal(deployRequest{
		GPUType:  spec.GPUType,
		GPUCount: spec.GPUCount,
		Region:   spec.Region,
		Image:    spec.Image,
		Command:  spec.Command,
		Env:      spec.Env,
	})
	if err != nil {
		return Cluster{}, fmt.Errorf("ionet: marshal deploy: %w", err)
	}
	raw, status, err := p.do(ctx, http.MethodPost, "/clusters", body)
	if err != nil {
		return Cluster{}, fmt.Errorf("ionet: deploy: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return Cluster{}, fmt.Errorf("ionet: deploy: %w: status %d", ErrDeployFailed, status)
	}
	var c Cluster
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cluster{}, fmt.Errorf("ionet: decode cluster: %w", err)
	}
	if strings.TrimSpace(c.ID) == "" {
		return Cluster{}, fmt.Errorf("ionet: deploy: %w: empty cluster_id in response", ErrDeployFailed)
	}
	// Echo the requested model/count when the response omits them, so the handle
	// is fully populated for callers even on a terse deploy ack.
	if c.GPUType == "" {
		c.GPUType = spec.GPUType
	}
	if c.GPUCount == 0 {
		c.GPUCount = spec.GPUCount
	}
	return c, nil
}

// HealthCheck polls the io.net cluster-status endpoint for clusterID and returns
// the parsed Health. The health status is read OFF the response, never assumed.
//
// Wire protocol:
//
//	GET {base}/clusters/{id}/status  -> 200 {Health}  (404 => ErrClusterNotFound)
func (p *IONetProvider) HealthCheck(ctx context.Context, clusterID string) (Health, error) {
	id := strings.TrimSpace(clusterID)
	if id == "" {
		return Health{}, ErrEmptyClusterID
	}
	raw, status, err := p.do(ctx, http.MethodGet, "/clusters/"+id+"/status", nil)
	if err != nil {
		return Health{}, fmt.Errorf("ionet: health check %q: %w", id, err)
	}
	if status == http.StatusNotFound {
		return Health{}, fmt.Errorf("ionet: health check %q: %w", id, ErrClusterNotFound)
	}
	if status != http.StatusOK {
		return Health{}, fmt.Errorf("ionet: health check %q: unexpected status %d", id, status)
	}
	var h Health
	if err := json.Unmarshal(raw, &h); err != nil {
		return Health{}, fmt.Errorf("ionet: decode health: %w", err)
	}
	if h.ClusterID == "" {
		h.ClusterID = id
	}
	return h, nil
}

// Provision implements pool.GPUProvider by deploying ONE io.net cluster matching
// spec and tracking it so Capacity is enforced. It reserves a capacity slot
// atomically with the gate check (the billable deploy runs lock-free), deploys,
// then records the cluster. The returned Instance.ID is the io.net cluster_id.
func (p *IONetProvider) Provision(ctx context.Context, spec pool.Spec) (pool.Instance, error) {
	if strings.TrimSpace(spec.GPUType) == "" {
		return pool.Instance{}, ErrEmptyGPUType
	}

	p.mu.Lock()
	if len(p.deployed)+p.reserved >= p.capacity {
		at := p.capacity
		p.mu.Unlock()
		return pool.Instance{}, fmt.Errorf("%w: %d", ErrAtCapacity, at)
	}
	p.reserved++
	p.mu.Unlock()

	c, err := p.DeployCluster(ctx, ClusterSpec{
		GPUType:   spec.GPUType,
		GPUCount:  1,
		HourlyUSD: spec.HourlyUSD,
	})
	if err != nil {
		p.mu.Lock()
		p.reserved--
		p.mu.Unlock()
		return pool.Instance{}, err
	}

	inst := pool.Instance{ID: c.ID, GPUType: spec.GPUType, HourlyUSD: spec.HourlyUSD}
	p.mu.Lock()
	p.reserved--
	p.deployed[inst.ID] = inst
	p.mu.Unlock()
	return inst, nil
}

// Release implements pool.GPUProvider by terminating the cluster over the wire
// and dropping the local record only after the backend acknowledges, so a
// failed terminate leaves the cluster tracked (and counted against Capacity)
// rather than leaking a billable cluster from the adapter's view. Releasing a
// cluster the adapter does not hold is ErrUnknownInstance.
func (p *IONetProvider) Release(ctx context.Context, inst pool.Instance) error {
	if strings.TrimSpace(inst.ID) == "" {
		return ErrEmptyClusterID
	}
	p.mu.Lock()
	_, ok := p.deployed[inst.ID]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownInstance, inst.ID)
	}
	if err := p.Terminate(ctx, inst.ID); err != nil {
		return err
	}
	p.mu.Lock()
	delete(p.deployed, inst.ID)
	p.mu.Unlock()
	return nil
}

// Terminate issues DELETE {base}/clusters/{id}. A 404 is treated as success
// (already gone); any other non-2xx is an error. It is exported so callers can
// terminate a cluster by run-UUID independent of the pool bookkeeping.
func (p *IONetProvider) Terminate(ctx context.Context, clusterID string) error {
	id := strings.TrimSpace(clusterID)
	if id == "" {
		return ErrEmptyClusterID
	}
	_, status, err := p.do(ctx, http.MethodDelete, "/clusters/"+id, nil)
	if err != nil {
		return fmt.Errorf("ionet: terminate %q: %w", id, err)
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("ionet: terminate %q: unexpected status %d", id, status)
	}
	return nil
}

// List implements pool.GPUProvider, returning the clusters this adapter has
// deployed (and not released) in ID order.
func (p *IONetProvider) List(ctx context.Context) ([]pool.Instance, error) {
	p.mu.Lock()
	out := make([]pool.Instance, 0, len(p.deployed))
	for _, inst := range p.deployed {
		out = append(out, inst)
	}
	p.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// do performs one HTTP request against the configured base URL, attaching the
// bearer token and a JSON content type for bodied requests.
func (p *IONetProvider) do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}
