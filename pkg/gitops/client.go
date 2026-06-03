package gitops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNoEndpoint is returned by NewClient when BaseURL is empty so a misconfigured
// client never silently no-ops against a zero address.
var ErrNoEndpoint = errors.New("gitops: empty ArgoCD base URL")

// ErrApplyFailed is returned by ApplyApplication when the ArgoCD API responds
// with a non-2xx status.
var ErrApplyFailed = errors.New("gitops: ArgoCD application apply failed")

// Config configures a Client against an ArgoCD API server.
type Config struct {
	// BaseURL is the ArgoCD API root, e.g. "https://argocd.example/api/v1".
	// Required; NewClient returns ErrNoEndpoint when empty.
	BaseURL string
	// Token, when non-empty, is sent as "Authorization: Bearer" on every request.
	Token string
	// Timeout bounds each individual HTTP request. Zero means no per-request
	// timeout beyond the caller's context.
	Timeout time.Duration
	// HTTPClient lets callers/tests inject a client; nil uses a default.
	HTTPClient *http.Client
	// RunID, when non-empty, fixes the per-run UUID (useful for tests). When
	// empty, NewClient mints a fresh RFC-4122 v4 UUID.
	RunID string
}

// Client speaks the ArgoCD REST API to create/upsert generated Applications. It
// carries a per-run UUID (RunID) stamped on every request via the
// "X-Helix-Run-Id" header and returned from ApplyApplicationSet for forensic
// correlation with captured ArgoCD sync evidence.
type Client struct {
	base    string
	token   string
	timeout time.Duration
	hc      *http.Client
	runID   string
}

// NewClient builds a Client from cfg. It mints a per-run UUID when cfg.RunID is
// empty.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, ErrNoEndpoint
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	runID := strings.TrimSpace(cfg.RunID)
	if runID == "" {
		runID = NewRunID()
	}
	return &Client{
		base:    strings.TrimRight(cfg.BaseURL, "/"),
		token:   cfg.Token,
		timeout: cfg.Timeout,
		hc:      hc,
		runID:   runID,
	}, nil
}

// RunID returns the per-run UUID stamped on this client's requests.
func (c *Client) RunID() string { return c.runID }

// ApplyResult records the outcome of applying generated Applications. RunID is
// the per-run UUID; Applied lists the Application names successfully upserted, in
// the order applied.
type ApplyResult struct {
	RunID   string
	Applied []string
}

// ApplyApplicationSet generates Applications from the template + cells and
// upserts each into ArgoCD (POST {base}/applications?upsert=true), in the order
// of the supplied RollingSyncPlan when non-nil — so the wire-level apply order
// matches the canary->tier-2->tier-1 rollout. When plan is nil, apps are applied
// in generation order. It returns an ApplyResult carrying the per-run UUID and
// the names actually applied (read back from the server response).
func (c *Client) ApplyApplicationSet(ctx context.Context, tmpl ApplicationSetTemplate, cells []Cell, plan *RollingSyncPlan) (ApplyResult, error) {
	apps, err := tmpl.Generate(cells)
	if err != nil {
		return ApplyResult{RunID: c.runID}, err
	}
	byName := make(map[string]Application, len(apps))
	for _, a := range apps {
		byName[a.Metadata.Name] = a
	}

	order := make([]string, 0, len(apps))
	if plan != nil {
		for _, w := range plan.Waves {
			order = append(order, w.Apps...)
		}
	} else {
		for _, a := range apps {
			order = append(order, a.Metadata.Name)
		}
	}

	res := ApplyResult{RunID: c.runID}
	for _, name := range order {
		app, ok := byName[name]
		if !ok {
			continue
		}
		applied, err := c.ApplyApplication(ctx, app)
		if err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, applied)
	}
	return res, nil
}

// ApplyApplication upserts a single Application into ArgoCD and returns the
// Application name read back from the server response body (proving the server
// accepted it), never the locally-supplied name. A non-2xx status surfaces
// ErrApplyFailed.
func (c *Client) ApplyApplication(ctx context.Context, app Application) (string, error) {
	body, err := json.Marshal(app)
	if err != nil {
		return "", fmt.Errorf("gitops: marshal application: %w", err)
	}
	raw, status, err := c.do(ctx, http.MethodPost, "/applications?upsert=true", body)
	if err != nil {
		return "", fmt.Errorf("gitops: apply application %q: %w", app.Metadata.Name, err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", fmt.Errorf("gitops: apply application %q: %w: status %d", app.Metadata.Name, ErrApplyFailed, status)
	}
	var echoed Application
	if err := json.Unmarshal(raw, &echoed); err != nil {
		return "", fmt.Errorf("gitops: decode application response: %w", err)
	}
	if echoed.Metadata.Name == "" {
		return "", fmt.Errorf("gitops: apply application %q: %w: empty name in response", app.Metadata.Name, ErrApplyFailed)
	}
	return echoed.Metadata.Name, nil
}

// LiveResources fetches the live managed-resource set ArgoCD reports for a named
// Application via GET {base}/applications/{name}/managed-resources. It returns
// the resources for use with DetectDrift against the Git-desired set.
func (c *Client) LiveResources(ctx context.Context, appName string) ([]Resource, error) {
	if strings.TrimSpace(appName) == "" {
		return nil, errors.New("gitops: empty application name")
	}
	raw, status, err := c.do(ctx, http.MethodGet, "/applications/"+appName+"/managed-resources", nil)
	if err != nil {
		return nil, fmt.Errorf("gitops: live resources for %q: %w", appName, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("gitops: live resources for %q: unexpected status %d", appName, status)
	}
	var env struct {
		Items []Resource `json:"items"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("gitops: decode managed resources: %w", err)
	}
	return env.Items, nil
}

// do performs one HTTP request against the configured base URL, stamping the
// per-run UUID header on every request, and returns the raw body + status code.
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// Per-run UUID stamped on every request for forensic correlation with the
	// captured ArgoCD sync evidence (closure criterion: "emit per-run UUID").
	req.Header.Set("X-Helix-Run-Id", c.runID)
	resp, err := c.hc.Do(req)
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

// NewRunID mints a fresh RFC-4122 v4 UUID using crypto/rand. WHY in-package: the
// parallel-wave rule forbids adding a uuid dependency; the v4 layout is trivial
// to produce from 16 random bytes with the version/variant nibbles set.
func NewRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is catastrophic; fall back to a time-seeded but
		// still well-formed UUID rather than panicking in a library.
		t := uint64(time.Now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte(t >> (8 * i))
			b[8+i] = byte(t >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
