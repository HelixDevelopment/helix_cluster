package cloudspot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeSink is the injected, recording Sink. It captures, in call order, which
// lifecycle methods fired and the deadline each was handed. This is the
// sink-side evidence: tests assert on what the node actually DID on a notice,
// not merely that a poll returned without error.
type fakeSink struct {
	mu        sync.Mutex
	order     []string
	deadlines []time.Time
	failOn    string // method name that should return an error, or ""
}

func (s *fakeSink) record(name string, deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, name)
	s.deadlines = append(s.deadlines, deadline)
	if s.failOn == name {
		return errors.New(name + " sink failure")
	}
	return nil
}

func (s *fakeSink) Drain(_ context.Context, d time.Time) error      { return s.record("drain", d) }
func (s *fakeSink) Checkpoint(_ context.Context, d time.Time) error { return s.record("checkpoint", d) }
func (s *fakeSink) Upload(_ context.Context, d time.Time) error     { return s.record("upload", d) }

func (s *fakeSink) snapshot() ([]string, []time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := append([]string(nil), s.order...)
	d := append([]time.Time(nil), s.deadlines...)
	return o, d
}

// wantSinkSequence is the canonical drain → checkpoint → upload order.
var wantSinkSequence = []string{"drain", "checkpoint", "upload"}

// ---- AWS IMDSv2 -----------------------------------------------------------

// awsRequest records one observed request against the fake IMDS.
type awsRequest struct {
	method string
	path   string
	ttl    string // X-aws-ec2-metadata-token-ttl-seconds (PUT)
	token  string // X-aws-ec2-metadata-token (GET)
}

// newAWSIMDS builds an httptest server reproducing the REAL IMDSv2 wire bytes:
// a PUT /latest/api/token mints a token (echoing the TTL header), and a GET of
// the instance-action path requires that exact token header and returns the
// genuine {"action","time"} body once `interrupted` is set, else 404.
func newAWSIMDS(t *testing.T, instanceActionBody string, interrupted *bool, log *[]awsRequest, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	const issuedToken = "AQAEAExampleTokenBytes=="
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*log = append(*log, awsRequest{
			method: r.Method,
			path:   r.URL.Path,
			ttl:    r.Header.Get(awsTokenTTLHeader),
			token:  r.Header.Get(awsTokenHeader),
		})
		mu.Unlock()

		switch {
		case r.Method == http.MethodPut && r.URL.Path == awsTokenPath:
			if r.Header.Get(awsTokenTTLHeader) == "" {
				// Real IMDSv2 rejects a token PUT without the TTL header.
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(issuedToken))
		case r.Method == http.MethodGet && r.URL.Path == awsInstanceActionPath:
			if r.Header.Get(awsTokenHeader) != issuedToken {
				// Real IMDSv2 returns 401 when the token header is missing/wrong.
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			on := *interrupted
			mu.Unlock()
			if !on {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(instanceActionBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestAWSPoller_TokenDanceThenDetectAndFireSink is the core sink-side proof for
// AWS. It captures BEFORE (no interruption → healthy, sink untouched) and AFTER
// (interruption → token PUT precedes the metadata GET, notice parsed, and
// drain→checkpoint→upload fired with the parsed deadline).
//
// Mutation that this kills: in AWSPoller.Poll, skip the token() call / drop the
// awsTokenHeader on the GET. The fake IMDS then answers 401, no notice is
// detected, the sink never fires, and the AFTER assertions (detected==true,
// sink sequence) FAIL. Equivalently, removing fireSink's Drain call makes the
// recorded order differ from {drain,checkpoint,upload} → FAIL.
func TestAWSPoller_TokenDanceThenDetectAndFireSink(t *testing.T) {
	var (
		interrupted bool
		log         []awsRequest
		logMu       sync.Mutex
	)
	const deadlineStr = "2026-06-03T10:02:00Z"
	body := `{"action":"terminate","time":"` + deadlineStr + `"}`
	srv := newAWSIMDS(t, body, &interrupted, &log, &logMu)
	defer srv.Close()

	sink := &fakeSink{}
	p := NewAWSPoller(srv.URL, sink,
		WithHTTPClient(srv.Client()),
		WithTokenTTL(60*time.Second),
	)

	// BEFORE: healthy instance → no notice, sink untouched.
	if _, detected, err := p.Poll(context.Background()); err != nil || detected {
		t.Fatalf("healthy poll: detected=%v err=%v; want detected=false err=nil", detected, err)
	}
	if got, _ := sink.snapshot(); len(got) != 0 {
		t.Fatalf("sink fired on healthy instance: %v", got)
	}

	// AFTER: provider schedules reclamation.
	logMu.Lock()
	interrupted = true
	logMu.Unlock()

	notice, detected, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("interrupted poll err: %v", err)
	}
	if !detected {
		t.Fatal("interruption not detected after instance-action published")
	}
	wantDeadline, _ := time.Parse(time.RFC3339, deadlineStr)
	if !notice.Deadline.Equal(wantDeadline) {
		t.Fatalf("notice deadline = %v, want %v", notice.Deadline, wantDeadline)
	}
	if notice.Reason != "aws-spot-terminate" {
		t.Fatalf("notice reason = %q, want aws-spot-terminate", notice.Reason)
	}

	// SINK-SIDE: drain→checkpoint→upload fired, each with the PARSED deadline.
	gotOrder, gotDeadlines := sink.snapshot()
	if !reflect.DeepEqual(gotOrder, wantSinkSequence) {
		t.Fatalf("sink order = %v, want %v", gotOrder, wantSinkSequence)
	}
	for i, d := range gotDeadlines {
		if !d.Equal(wantDeadline) {
			t.Fatalf("sink step %d got deadline %v, want %v", i, d, wantDeadline)
		}
	}

	// WIRE-ORDER: a token PUT must precede every metadata GET (IMDSv2 dance).
	assertAWSTokenDance(t, &log, &logMu)
}

// assertAWSTokenDance verifies that the first request was the PUT token mint and
// that every instance-action GET carried a non-empty token header issued after
// a PUT — i.e. the real IMDSv2 sequence, not an unauthenticated GET.
func assertAWSTokenDance(t *testing.T, log *[]awsRequest, mu *sync.Mutex) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if len(*log) == 0 {
		t.Fatal("no requests reached IMDS")
	}
	first := (*log)[0]
	if first.method != http.MethodPut || first.path != awsTokenPath {
		t.Fatalf("first request = %s %s, want PUT %s (token dance must come first)", first.method, first.path, awsTokenPath)
	}
	if first.ttl != "60" {
		t.Fatalf("token PUT TTL header = %q, want 60", first.ttl)
	}
	sawPut := false
	for _, rq := range *log {
		if rq.method == http.MethodPut && rq.path == awsTokenPath {
			sawPut = true
			continue
		}
		if rq.method == http.MethodGet && rq.path == awsInstanceActionPath {
			if !sawPut {
				t.Fatal("instance-action GET issued before any token PUT")
			}
			if rq.token == "" {
				t.Fatal("instance-action GET carried no IMDSv2 token header")
			}
		}
	}
}

// TestAWSPoller_TokenRefreshOnExpiry proves IMDSv2 token expiry/refresh: with a
// tiny TTL and an injected advancing clock, a second poll re-runs the PUT dance
// rather than reusing the stale token.
//
// Mutation that this kills: in AWSPoller.token, drop the expiry check
// (`p.now().Before(p.tokenExp)`) so the cached token is reused forever. Then
// only ONE PUT is observed and the assertion (>=2 PUTs) FAILS.
func TestAWSPoller_TokenRefreshOnExpiry(t *testing.T) {
	var (
		interrupted bool
		log         []awsRequest
		logMu       sync.Mutex
	)
	body := `{"action":"stop","time":"2026-06-03T10:02:00Z"}`
	srv := newAWSIMDS(t, body, &interrupted, &log, &logMu)
	defer srv.Close()

	clock := time.Now()
	p := NewAWSPoller(srv.URL, &fakeSink{},
		WithHTTPClient(srv.Client()),
		WithTokenTTL(2*time.Second),
		WithClock(func() time.Time { return clock }),
	)

	if _, _, err := p.Poll(context.Background()); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	// Advance the clock past the (early-)expiry so the cached token is stale.
	clock = clock.Add(5 * time.Second)
	if _, _, err := p.Poll(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	logMu.Lock()
	defer logMu.Unlock()
	puts := 0
	for _, rq := range log {
		if rq.method == http.MethodPut && rq.path == awsTokenPath {
			puts++
		}
	}
	if puts < 2 {
		t.Fatalf("token PUTs = %d, want >= 2 (token must refresh after TTL expiry)", puts)
	}
}

// TestAWSPoller_FiresOnce proves repeated polls within the 2-minute window do
// not re-drain: the sink fires exactly once.
//
// Mutation that this kills: remove the sinkFired guard in fireOnce. Then a
// second detected poll re-fires the sink and the order length doubles → FAIL.
func TestAWSPoller_FiresOnce(t *testing.T) {
	var (
		interrupted = true
		log         []awsRequest
		logMu       sync.Mutex
	)
	body := `{"action":"terminate","time":"2026-06-03T10:02:00Z"}`
	srv := newAWSIMDS(t, body, &interrupted, &log, &logMu)
	defer srv.Close()

	sink := &fakeSink{}
	p := NewAWSPoller(srv.URL, sink, WithHTTPClient(srv.Client()))

	for i := 0; i < 3; i++ {
		if _, detected, err := p.Poll(context.Background()); err != nil || !detected {
			t.Fatalf("poll %d: detected=%v err=%v", i, detected, err)
		}
	}
	if got, _ := sink.snapshot(); !reflect.DeepEqual(got, wantSinkSequence) {
		t.Fatalf("sink fired %v across repeated polls, want exactly one %v sequence", got, wantSinkSequence)
	}
}

// ---- Azure Scheduled Events ----------------------------------------------

// newAzureIMDS reproduces the Scheduled Events wire bytes: it requires the
// "Metadata: true" header and serves the supplied document.
func newAzureIMDS(t *testing.T, doc string, sawMetadataHeader *bool, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != azureScheduledEventsPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get(azureMetadataHeader) != "true" {
			// Real Azure IMDS rejects requests lacking Metadata:true.
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		*sawMetadataHeader = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(doc))
	}))
}

// TestAzurePoller_PreemptDetectAndFireSink captures BEFORE (empty events →
// healthy) vs AFTER (a Preempt event → drain fired with the NotBefore deadline).
//
// Mutation that this kills: make isAzurePreemption return false for "Preempt"
// (or skip the Metadata header). Then the event is ignored / the GET is
// rejected, the sink never fires, and the AFTER assertions FAIL.
func TestAzurePoller_PreemptDetectAndFireSink(t *testing.T) {
	var sawHeader bool
	var mu sync.Mutex

	// BEFORE: no events.
	healthySrv := newAzureIMDS(t, `{"DocumentIncarnation":1,"Events":[]}`, &sawHeader, &mu)
	defer healthySrv.Close()
	sink0 := &fakeSink{}
	p0 := NewAzurePoller(healthySrv.URL, sink0, WithHTTPClient(healthySrv.Client()))
	if _, detected, err := p0.Poll(context.Background()); err != nil || detected {
		t.Fatalf("healthy azure poll: detected=%v err=%v", detected, err)
	}
	if got, _ := sink0.snapshot(); len(got) != 0 {
		t.Fatalf("sink fired on healthy azure: %v", got)
	}
	mu.Lock()
	if !sawHeader {
		t.Fatal("Metadata:true header never sent to Azure IMDS")
	}
	mu.Unlock()

	// AFTER: a Preempt event with a concrete NotBefore.
	notBefore := time.Now().Add(30 * time.Second).UTC().Truncate(time.Second)
	doc := `{"DocumentIncarnation":2,"Events":[{"EventId":"E1","EventType":"Preempt","ResourceType":"VirtualMachine","Resources":["vm0"],"EventStatus":"Scheduled","NotBefore":"` +
		notBefore.Format(time.RFC1123) + `"}]}`
	preemptSrv := newAzureIMDS(t, doc, &sawHeader, &mu)
	defer preemptSrv.Close()

	sink := &fakeSink{}
	p := NewAzurePoller(preemptSrv.URL, sink, WithHTTPClient(preemptSrv.Client()))
	notice, detected, err := p.Poll(context.Background())
	if err != nil || !detected {
		t.Fatalf("preempt poll: detected=%v err=%v", detected, err)
	}
	wantDeadline, _ := time.Parse(time.RFC1123, notBefore.Format(time.RFC1123))
	if !notice.Deadline.Equal(wantDeadline) {
		t.Fatalf("notice deadline = %v, want %v (NotBefore)", notice.Deadline, wantDeadline)
	}
	if notice.Reason != "azure-preempt" {
		t.Fatalf("notice reason = %q, want azure-preempt", notice.Reason)
	}
	gotOrder, gotDeadlines := sink.snapshot()
	if !reflect.DeepEqual(gotOrder, wantSinkSequence) {
		t.Fatalf("sink order = %v, want %v", gotOrder, wantSinkSequence)
	}
	for i, d := range gotDeadlines {
		if !d.Equal(wantDeadline) {
			t.Fatalf("azure sink step %d deadline %v, want %v", i, d, wantDeadline)
		}
	}
}

// ---- GCP preempted --------------------------------------------------------

// newGCPMeta reproduces the GCE metadata wire bytes: it requires the
// "Metadata-Flavor: Google" header and serves "TRUE"/"FALSE".
func newGCPMeta(t *testing.T, value string, sawFlavor *bool, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gcpPreemptedPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get(gcpMetadataFlavorHeader) != gcpMetadataFlavorValue {
			// Real GCE metadata server 403s without the Metadata-Flavor header.
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mu.Lock()
		*sawFlavor = true
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(value))
	}))
}

// TestGCPPoller_PreemptedDetectAndFireSink captures BEFORE ("FALSE" → healthy)
// vs AFTER ("TRUE" → drain fired with a ~30s deadline).
//
// Mutation that this kills: in GCPPoller.Poll, treat "TRUE" as not-detected (or
// drop the Metadata-Flavor header). Then the sink never fires and the AFTER
// assertions FAIL.
func TestGCPPoller_PreemptedDetectAndFireSink(t *testing.T) {
	var sawFlavor bool
	var mu sync.Mutex

	// BEFORE.
	healthySrv := newGCPMeta(t, "FALSE", &sawFlavor, &mu)
	defer healthySrv.Close()
	sink0 := &fakeSink{}
	p0 := NewGCPPoller(healthySrv.URL, sink0, WithHTTPClient(healthySrv.Client()))
	if _, detected, err := p0.Poll(context.Background()); err != nil || detected {
		t.Fatalf("healthy gcp poll: detected=%v err=%v", detected, err)
	}
	if got, _ := sink0.snapshot(); len(got) != 0 {
		t.Fatalf("sink fired on healthy gcp: %v", got)
	}
	mu.Lock()
	if !sawFlavor {
		t.Fatal("Metadata-Flavor:Google header never sent to GCE metadata")
	}
	mu.Unlock()

	// AFTER.
	preemptSrv := newGCPMeta(t, "TRUE", &sawFlavor, &mu)
	defer preemptSrv.Close()
	sink := &fakeSink{}
	before := time.Now()
	p := NewGCPPoller(preemptSrv.URL, sink, WithHTTPClient(preemptSrv.Client()))
	notice, detected, err := p.Poll(context.Background())
	if err != nil || !detected {
		t.Fatalf("preempted poll: detected=%v err=%v", detected, err)
	}
	if notice.Reason != "gcp-preempted" {
		t.Fatalf("notice reason = %q, want gcp-preempted", notice.Reason)
	}
	// Deadline ~ now + 30s.
	lead := notice.Deadline.Sub(before)
	if lead < 25*time.Second || lead > 35*time.Second {
		t.Fatalf("gcp deadline lead = %v, want ~30s", lead)
	}
	gotOrder, gotDeadlines := sink.snapshot()
	if !reflect.DeepEqual(gotOrder, wantSinkSequence) {
		t.Fatalf("sink order = %v, want %v", gotOrder, wantSinkSequence)
	}
	for i, d := range gotDeadlines {
		if !d.Equal(notice.Deadline) {
			t.Fatalf("gcp sink step %d deadline %v, want %v", i, d, notice.Deadline)
		}
	}
}

// ---- cross-cutting: sink error propagation & PreemptionSource wiring -------

// TestFireSink_StopsAtFirstError proves the drain→checkpoint→upload sequence
// halts at the first failing step (e.g. checkpoint), so upload is not attempted
// on a half-checkpointed node.
//
// Mutation that this kills: in fireSink, ignore the Checkpoint error (don't
// return). Then Upload would run and the recorded order would include "upload"
// → the assertion FAILS.
func TestFireSink_StopsAtFirstError(t *testing.T) {
	sink := &fakeSink{failOn: "checkpoint"}
	err := fireSink(context.Background(), sink, PreemptionNotice{Deadline: time.Now().Add(time.Minute)})
	if err == nil {
		t.Fatal("fireSink returned nil despite checkpoint failure")
	}
	got, _ := sink.snapshot()
	want := []string{"drain", "checkpoint"} // upload must NOT run
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sink order = %v, want %v (must stop before upload)", got, want)
	}
}

// TestAWSPoller_DrivesHandlerViaPreemptionSource proves the poller is a real
// PreemptionSource: emitting on its channel makes the existing Handler run the
// drain lifecycle. This is the end-to-end seam from metadata detection to the
// shared drain machinery.
//
// Mutation that this kills: make AWSPoller.fireOnce not send on p.ch (drop the
// channel send). Then the Handler never receives a notice, never drains, and
// the recorded Handler hook order is empty → FAIL.
func TestAWSPoller_DrivesHandlerViaPreemptionSource(t *testing.T) {
	interrupted := true
	var log []awsRequest
	var logMu sync.Mutex
	deadline := time.Now().Add(2 * time.Second).UTC().Truncate(time.Second)
	body := `{"action":"terminate","time":"` + deadline.Format(time.RFC3339) + `"}`
	srv := newAWSIMDS(t, body, &interrupted, &log, &logMu)
	defer srv.Close()

	p := NewAWSPoller(srv.URL, nil, WithHTTPClient(srv.Client()))

	var rec recorder
	var forced int32
	h := NewHandler(p, recordingHooks(&rec, &forced))

	done := make(chan Result, 1)
	go func() { done <- h.Run(context.Background()) }()

	// Trigger a poll so the poller emits the notice onto its PreemptionSource
	// channel, which the Handler is selecting on.
	if _, detected, err := p.Poll(context.Background()); err != nil || !detected {
		t.Fatalf("poll: detected=%v err=%v", detected, err)
	}

	select {
	case res := <-done:
		if res.Outcome != OutcomeDrained {
			t.Fatalf("handler outcome = %q, want %q", res.Outcome, OutcomeDrained)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Handler never drained from poller-emitted notice")
	}
	want := []DrainStep{StepStopAdmission, StepCheckpoint, StepDeregister}
	if got := rec.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("handler hook order = %v, want %v", got, want)
	}
}
