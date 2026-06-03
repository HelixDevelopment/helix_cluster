package cloudspot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PollerOption configures a provider metadata poller (AWS/Azure/GCP). Options
// are shared so all three pollers accept the same knobs (HTTP client, poll
// interval, clock, and — for AWS — the IMDSv2 token TTL).
type PollerOption func(*pollerConfig)

type pollerConfig struct {
	client      *http.Client
	hasClient   bool
	interval    time.Duration
	hasInterval bool
	tokenTTL    time.Duration
	hasTokenTTL bool
	now         func() time.Time
}

// WithHTTPClient overrides the HTTP client (e.g. an httptest server's client).
func WithHTTPClient(c *http.Client) PollerOption {
	return func(p *pollerConfig) { p.client = c; p.hasClient = true }
}

// WithInterval overrides the poll interval between metadata probes.
func WithInterval(d time.Duration) PollerOption {
	return func(p *pollerConfig) { p.interval = d; p.hasInterval = true }
}

// WithTokenTTL overrides the IMDSv2 token TTL requested in the PUT (AWS only).
func WithTokenTTL(d time.Duration) PollerOption {
	return func(p *pollerConfig) { p.tokenTTL = d; p.hasTokenTTL = true }
}

// WithClock overrides the clock used for deadline/expiry arithmetic across all
// three pollers (test seam): AWS IMDSv2 token-expiry, and the GCP/Azure
// preemption-notice Deadline computation (now + lead). It defaults to time.Now.
func WithClock(now func() time.Time) PollerOption {
	return func(p *pollerConfig) { p.now = now }
}

func (c pollerConfig) applyAWS(p *AWSPoller) {
	if c.hasClient {
		p.client = c.client
	}
	if c.hasInterval {
		p.interval = c.interval
	}
	if c.hasTokenTTL {
		p.tokenTTL = c.tokenTTL
	}
	if c.now != nil {
		p.now = c.now
	}
}

func (c pollerConfig) applyAzure(p *AzurePoller) {
	if c.hasClient {
		p.client = c.client
	}
	if c.hasInterval {
		p.interval = c.interval
	}
	if c.now != nil {
		p.now = c.now
	}
}

func (c pollerConfig) applyGCP(p *GCPPoller) {
	if c.hasClient {
		p.client = c.client
	}
	if c.hasInterval {
		p.interval = c.interval
	}
	if c.now != nil {
		p.now = c.now
	}
}

// AzurePoller polls Azure Instance Metadata Service Scheduled Events. It GETs
// /metadata/scheduledevents?api-version=... with the mandatory "Metadata:true"
// header and parses the document, treating a Preempt or Terminate event as a
// reclamation warning. Azure delivers Preempt with ~30s notice for Spot VMs.
//
// It implements PreemptionSource and fires the injected Sink on detection.
type AzurePoller struct {
	baseURL    string
	apiVersion string
	client     *http.Client
	interval   time.Duration
	sink       Sink
	// now is the injectable clock used for the fallback deadline arithmetic when
	// NotBefore is absent/unparseable. It defaults to time.Now; WithClock
	// overrides it so the deadline is deterministic in tests.
	now func() time.Time

	ch chan PreemptionNotice

	mu        sync.Mutex
	sinkFired bool
}

// azureScheduledEvents mirrors the Scheduled Events document shape.
type azureScheduledEvents struct {
	DocumentIncarnation int                  `json:"DocumentIncarnation"`
	Events              []azureScheduledItem `json:"Events"`
}

type azureScheduledItem struct {
	EventID      string   `json:"EventId"`
	EventType    string   `json:"EventType"`
	ResourceType string   `json:"ResourceType"`
	Resources    []string `json:"Resources"`
	EventStatus  string   `json:"EventStatus"`
	// NotBefore is an RFC1123 timestamp: the earliest the event may start.
	NotBefore string `json:"NotBefore"`
}

const (
	azureScheduledEventsPath = "/metadata/scheduledevents"
	azureMetadataHeader      = "Metadata"
	azureDefaultAPIVersion   = "2020-07-01"
	// azureDefaultLead is the warning lead time used when NotBefore is absent
	// or unparseable; Azure Spot preemption notice is ~30s.
	azureDefaultLead = 30 * time.Second
)

// NewAzurePoller builds an Azure Scheduled Events poller. baseURL points at the
// IMDS endpoint (in tests, an httptest server). sink may be nil.
func NewAzurePoller(baseURL string, sink Sink, opts ...PollerOption) *AzurePoller {
	p := &AzurePoller{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiVersion: azureDefaultAPIVersion,
		client:     &http.Client{Timeout: 5 * time.Second},
		interval:   5 * time.Second,
		sink:       sink,
		now:        time.Now,
		ch:         make(chan PreemptionNotice, 1),
	}
	cfg := pollerConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.applyAzure(p)
	return p
}

// Notifications implements PreemptionSource.
func (p *AzurePoller) Notifications() <-chan PreemptionNotice { return p.ch }

// isPreemption reports whether an event type reclaims the VM.
func isAzurePreemption(eventType string) bool {
	switch strings.ToLower(eventType) {
	case "preempt", "terminate":
		return true
	default:
		return false
	}
}

// Poll performs one scheduled-events GET. It returns (notice, true, nil) when a
// Preempt/Terminate event is present, (zero, false, nil) when none, and fires
// the Sink once on the first detection.
func (p *AzurePoller) Poll(ctx context.Context) (PreemptionNotice, bool, error) {
	u := fmt.Sprintf("%s%s?api-version=%s", p.baseURL, azureScheduledEventsPath, p.apiVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	req.Header.Set(azureMetadataHeader, "true")

	resp, err := p.client.Do(req)
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PreemptionNotice{}, false, fmt.Errorf("azure scheduledevents: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	var doc azureScheduledEvents
	if err := json.Unmarshal(body, &doc); err != nil {
		return PreemptionNotice{}, false, fmt.Errorf("azure scheduledevents: parse: %w", err)
	}

	for _, ev := range doc.Events {
		if !isAzurePreemption(ev.EventType) {
			continue
		}
		deadline := p.deadlineFor(ev)
		notice := PreemptionNotice{Deadline: deadline, Reason: "azure-" + strings.ToLower(ev.EventType)}
		if err := p.fireOnce(ctx, notice); err != nil {
			return notice, true, err
		}
		return notice, true, nil
	}
	return PreemptionNotice{}, false, nil
}

// deadlineFor derives the reclamation deadline from an event. Azure's NotBefore
// is the earliest start; we treat it as the deadline by which the node must be
// drained. When it is absent/unparseable we fall back to now + ~30s.
func (p *AzurePoller) deadlineFor(ev azureScheduledItem) time.Time {
	if ev.NotBefore != "" {
		// Azure stamps NotBefore in RFC1123 ("Mon, 02 Jan 2006 15:04:05 GMT").
		if t, err := time.Parse(time.RFC1123, ev.NotBefore); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, ev.NotBefore); err == nil {
			return t
		}
	}
	return p.now().Add(azureDefaultLead)
}

func (p *AzurePoller) fireOnce(ctx context.Context, notice PreemptionNotice) error {
	p.mu.Lock()
	if p.sinkFired {
		p.mu.Unlock()
		return nil
	}
	p.sinkFired = true
	p.mu.Unlock()

	select {
	case p.ch <- notice:
	default:
	}
	return fireSink(ctx, p.sink, notice)
}

// Run polls until a Preempt/Terminate event is detected or ctx is cancelled.
func (p *AzurePoller) Run(ctx context.Context) (PreemptionNotice, error) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		notice, detected, err := p.Poll(ctx)
		if detected {
			return notice, err
		}
		select {
		case <-ctx.Done():
			return PreemptionNotice{}, ctx.Err()
		case <-t.C:
		}
	}
}
