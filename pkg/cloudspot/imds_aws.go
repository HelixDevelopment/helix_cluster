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

// Sink is the injectable side effect a metadata poller fires the instant it
// detects a real interruption notice: drain, then checkpoint, then upload. It
// is the honest seam for "what the node does on a 2-minute warning" — tests
// inject a fake that records the calls; production wires it to the real drain
// lifecycle. The poller NEVER calls a real S3/GCS client directly; that is the
// Sink implementation's concern.
//
// Deadline is the absolute wall-clock instant by which work must be safely
// persisted (the provider reclaims the instance after it). Sink methods run in
// drain → checkpoint → upload order and each receives the parsed deadline so an
// implementation can bound its own work.
type Sink interface {
	// Drain stops admitting new work. Called first.
	Drain(ctx context.Context, deadline time.Time) error
	// Checkpoint flushes in-flight work to durable local storage. Called second.
	Checkpoint(ctx context.Context, deadline time.Time) error
	// Upload pushes the checkpoint to remote object storage. Called third.
	Upload(ctx context.Context, deadline time.Time) error
}

// fireSink runs the drain → checkpoint → upload sequence against the sink,
// stopping at the first error. It is shared by all provider pollers so the
// detection-to-action wiring is identical across AWS/Azure/GCP.
func fireSink(ctx context.Context, sink Sink, notice PreemptionNotice) error {
	if sink == nil {
		return nil
	}
	if err := sink.Drain(ctx, notice.Deadline); err != nil {
		return fmt.Errorf("drain: %w", err)
	}
	if err := sink.Checkpoint(ctx, notice.Deadline); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	if err := sink.Upload(ctx, notice.Deadline); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	return nil
}

// AWSPoller polls the EC2 Instance Metadata Service (IMDSv2) for a spot
// interruption notice. It performs the genuine IMDSv2 token dance — a PUT to
// /latest/api/token with the TTL header, then GETs carrying the returned token
// in X-aws-ec2-metadata-token — and parses the
// /latest/meta-data/spot/instance-action JSON ({"action","time"}) that is the
// 2-minute reclamation warning.
//
// It implements PreemptionSource so it plugs straight into Handler, and it also
// fires the injected Sink (drain → checkpoint → upload) on detection.
type AWSPoller struct {
	baseURL  string // e.g. http://169.254.169.254 ; injectable for tests
	client   *http.Client
	interval time.Duration
	tokenTTL time.Duration
	sink     Sink
	now      func() time.Time

	ch chan PreemptionNotice

	mu          sync.Mutex
	cachedToken string
	tokenExp    time.Time
	sinkFired   bool
}

// awsInstanceAction is the JSON body of /latest/meta-data/spot/instance-action.
// The endpoint returns 404 while the instance is healthy and this body once a
// reclamation is scheduled. Example: {"action":"terminate","time":"2026-06-03T10:00:00Z"}.
type awsInstanceAction struct {
	Action string `json:"action"`
	Time   string `json:"time"`
}

const (
	awsTokenPath          = "/latest/api/token" //gosec:disable G101 -- public AWS IMDSv2 endpoint path, not a secret
	awsInstanceActionPath = "/latest/meta-data/spot/instance-action"
	awsTokenTTLHeader     = "X-aws-ec2-metadata-token-ttl-seconds" //gosec:disable G101 -- HTTP header name, not a credential value
	awsTokenHeader        = "X-aws-ec2-metadata-token"             //gosec:disable G101 -- HTTP header name, not a credential value
)

// NewAWSPoller builds an AWS IMDSv2 poller. baseURL points at the metadata
// service (in tests, an httptest server reproducing the real wire bytes). sink
// may be nil (then only the PreemptionSource channel is driven).
func NewAWSPoller(baseURL string, sink Sink, opts ...PollerOption) *AWSPoller {
	p := &AWSPoller{
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   &http.Client{Timeout: 5 * time.Second},
		interval: 5 * time.Second,
		tokenTTL: 21600 * time.Second, // AWS default max (6h)
		sink:     sink,
		now:      time.Now,
		ch:       make(chan PreemptionNotice, 1),
	}
	cfg := pollerConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.applyAWS(p)
	return p
}

// Notifications implements PreemptionSource.
func (p *AWSPoller) Notifications() <-chan PreemptionNotice { return p.ch }

// token returns a valid IMDSv2 token, refreshing via the PUT dance if the
// cached one is missing or expired. This honours IMDSv2 token expiry/refresh.
func (p *AWSPoller) token(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.cachedToken != "" && p.now().Before(p.tokenExp) {
		tok := p.cachedToken
		p.mu.Unlock()
		return tok, nil
	}
	p.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.baseURL+awsTokenPath, nil)
	if err != nil {
		return "", err
	}
	ttlSecs := int(p.tokenTTL / time.Second)
	req.Header.Set(awsTokenTTLHeader, fmt.Sprintf("%d", ttlSecs))

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("imds token PUT: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(body))
	if tok == "" {
		return "", fmt.Errorf("imds token PUT: empty token")
	}

	p.mu.Lock()
	p.cachedToken = tok
	// Expire slightly early so a near-expiry token never gets used on the wire.
	p.tokenExp = p.now().Add(p.tokenTTL - time.Second)
	p.mu.Unlock()
	return tok, nil
}

// Poll performs one token-authenticated GET of the instance-action endpoint. It
// returns (notice, true, nil) when a reclamation is scheduled, (zero, false,
// nil) when the instance is healthy (404), and a non-nil error on transport or
// protocol faults. On the first detection it also fires the Sink exactly once.
func (p *AWSPoller) Poll(ctx context.Context) (PreemptionNotice, bool, error) {
	tok, err := p.token(ctx)
	if err != nil {
		return PreemptionNotice{}, false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+awsInstanceActionPath, nil)
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	req.Header.Set(awsTokenHeader, tok)

	resp, err := p.client.Do(req)
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		// Healthy: no interruption scheduled.
		return PreemptionNotice{}, false, nil
	case http.StatusUnauthorized:
		// Token rejected (expired/invalid): drop it so the next poll refreshes.
		p.mu.Lock()
		p.cachedToken, p.tokenExp = "", time.Time{}
		p.mu.Unlock()
		return PreemptionNotice{}, false, fmt.Errorf("imds instance-action: token unauthorized")
	case http.StatusOK:
		// fall through to parse
	default:
		return PreemptionNotice{}, false, fmt.Errorf("imds instance-action: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return PreemptionNotice{}, false, err
	}
	var ia awsInstanceAction
	if err := json.Unmarshal(body, &ia); err != nil {
		return PreemptionNotice{}, false, fmt.Errorf("imds instance-action: parse: %w", err)
	}
	if ia.Action == "" {
		return PreemptionNotice{}, false, fmt.Errorf("imds instance-action: empty action")
	}
	deadline, err := time.Parse(time.RFC3339, ia.Time)
	if err != nil {
		return PreemptionNotice{}, false, fmt.Errorf("imds instance-action: bad time %q: %w", ia.Time, err)
	}
	notice := PreemptionNotice{Deadline: deadline, Reason: "aws-spot-" + ia.Action}

	if err := p.fireOnce(ctx, notice); err != nil {
		return notice, true, err
	}
	return notice, true, nil
}

// fireOnce fires the sink and emits to the channel exactly once across the
// poller's lifetime, so repeated polls during the 2-minute window do not
// re-trigger drain.
func (p *AWSPoller) fireOnce(ctx context.Context, notice PreemptionNotice) error {
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

// Run polls until a notice is detected (then it fires the sink and returns the
// notice) or ctx is cancelled. Transport/404 results simply schedule the next
// tick; only a detected interruption ends the loop. This is the long-running
// driver an agent would launch in a goroutine.
func (p *AWSPoller) Run(ctx context.Context) (PreemptionNotice, error) {
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
