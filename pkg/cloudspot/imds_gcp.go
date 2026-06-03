package cloudspot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GCPPoller polls the GCE metadata server for preemptible/Spot reclamation. It
// GETs computeMetadata/v1/instance/preempted with the mandatory
// "Metadata-Flavor: Google" header; the endpoint returns the plain-text body
// "TRUE" once preemption is in progress (and "FALSE" while healthy). GCE gives
// a ~30s ACPI G2 soft-off warning, so the deadline is now + ~30s.
//
// It implements PreemptionSource and fires the injected Sink on detection.
type GCPPoller struct {
	baseURL  string
	client   *http.Client
	interval time.Duration
	lead     time.Duration
	sink     Sink
	// now is the injectable clock used for deadline arithmetic. It defaults to
	// time.Now; WithClock overrides it so the deadline is deterministic in tests.
	now func() time.Time

	ch chan PreemptionNotice

	mu        sync.Mutex
	sinkFired bool
}

const (
	gcpPreemptedPath        = "/computeMetadata/v1/instance/preempted"
	gcpMetadataFlavorHeader = "Metadata-Flavor"
	gcpMetadataFlavorValue  = "Google"
	// gcpDefaultLead is GCE's preemption warning window (~30s ACPI G2 soft-off).
	gcpDefaultLead = 30 * time.Second
)

// NewGCPPoller builds a GCE preempted poller. baseURL points at the metadata
// server (in tests, an httptest server). sink may be nil.
func NewGCPPoller(baseURL string, sink Sink, opts ...PollerOption) *GCPPoller {
	p := &GCPPoller{
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   &http.Client{Timeout: 5 * time.Second},
		interval: 5 * time.Second,
		lead:     gcpDefaultLead,
		sink:     sink,
		now:      time.Now,
		ch:       make(chan PreemptionNotice, 1),
	}
	cfg := pollerConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.applyGCP(p)
	return p
}

// Notifications implements PreemptionSource.
func (p *GCPPoller) Notifications() <-chan PreemptionNotice { return p.ch }

// Poll performs one preempted-flag GET. It returns (notice, true, nil) when the
// body is "TRUE", (zero, false, nil) when "FALSE", and fires the Sink once on
// the first detection.
func (p *GCPPoller) Poll(ctx context.Context) (PreemptionNotice, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+gcpPreemptedPath, nil)
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	req.Header.Set(gcpMetadataFlavorHeader, gcpMetadataFlavorValue)

	resp, err := p.client.Do(req)
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PreemptionNotice{}, false, fmt.Errorf("gcp preempted: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	switch strings.ToUpper(strings.TrimSpace(string(body))) {
	case "TRUE":
		notice := PreemptionNotice{Deadline: p.now().Add(p.lead), Reason: "gcp-preempted"}
		if err := p.fireOnce(ctx, notice); err != nil {
			return notice, true, err
		}
		return notice, true, nil
	case "FALSE", "":
		return PreemptionNotice{}, false, nil
	default:
		return PreemptionNotice{}, false, fmt.Errorf("gcp preempted: unexpected body %q", strings.TrimSpace(string(body)))
	}
}

func (p *GCPPoller) fireOnce(ctx context.Context, notice PreemptionNotice) error {
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

// Run polls until the preempted flag flips TRUE or ctx is cancelled.
func (p *GCPPoller) Run(ctx context.Context) (PreemptionNotice, error) {
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
