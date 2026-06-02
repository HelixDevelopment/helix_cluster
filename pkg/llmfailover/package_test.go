package llmfailover

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fixedNow returns a deterministic clock seam for the tests.
func fixedNow() func() time.Time {
	t := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// newClassifier builds a Classifier wired with the standard sentinel-error
// predicates that match the package-level sentinel errors.
func newClassifier() *Classifier {
	return &Classifier{
		Predicates: map[ErrorClass]Predicate{
			RateLimited:     func(e error) bool { return errors.Is(e, ErrRateLimited) },
			ServerError:     func(e error) bool { return errors.Is(e, ErrServerError) },
			Timeout:         func(e error) bool { return errors.Is(e, ErrTimeout) },
			AuthError:       func(e error) bool { return errors.Is(e, ErrAuthError) },
			ContentFiltered: func(e error) bool { return errors.Is(e, ErrContentFiltered) },
		},
		Now: fixedNow(),
	}
}

// ---------------------------------------------------------------------------
// TestFallbackPathPerClass
//
// MUTATION GUARD: the fallbackPaths map in package.go maps each ErrorClass to
// a distinct ordered action slice.  This test asserts the EXACT first action of
// each primary class so that swapping any two entries (e.g. RateLimited and
// ServerError) makes the first-action assertion for at least one class fail.
//
// Concretely:
//   - RateLimited[0] == ActionRetryAfterDelay, RateLimited[1] == ActionQueueForLater
//   - ServerError[0] == ActionRetryAfterDelay, ServerError[1] == ActionSwitchEndpoint
//     → swapping them: ServerError would get ActionQueueForLater at [1], failing
//     the "want ActionSwitchEndpoint" check.
//   - Timeout[0]  == ActionReduceTokenCount (unique — kills any swap with the above two)
//   - AuthError   == [ActionAbort] (terminal, length==1)
//   - ContentFiltered == [ActionAbort] (terminal, length==1)
// ---------------------------------------------------------------------------
func TestFallbackPathPerClass(t *testing.T) {
	const runID = "run-fallback-per-class"

	cases := []struct {
		class        ErrorClass
		wantActions  []FallbackAction
		wantTerminal bool // terminal classes have no retry path (len==1, only Abort)
	}{
		// RateLimited: back off → queue → fallback model.
		// Guard: first action MUST be ActionRetryAfterDelay; second MUST be
		// ActionQueueForLater (not ActionSwitchEndpoint).  Swapping RateLimited
		// and ServerError produces ActionSwitchEndpoint at [1] and fails here.
		{
			class:       RateLimited,
			wantActions: []FallbackAction{ActionRetryAfterDelay, ActionQueueForLater, ActionFallbackModel},
		},
		// ServerError: retry → switch endpoint → escalate.
		// Guard: second action MUST be ActionSwitchEndpoint (not ActionQueueForLater).
		{
			class:       ServerError,
			wantActions: []FallbackAction{ActionRetryAfterDelay, ActionSwitchEndpoint, ActionEscalateAlert},
		},
		// Timeout: reduce tokens first (unique first action — kills swap with the two above).
		{
			class:       Timeout,
			wantActions: []FallbackAction{ActionReduceTokenCount, ActionRetryAfterDelay, ActionSwitchEndpoint},
		},
		// AuthError: terminal.
		{
			class:        AuthError,
			wantActions:  []FallbackAction{ActionAbort},
			wantTerminal: true,
		},
		// ContentFiltered: terminal.
		{
			class:        ContentFiltered,
			wantActions:  []FallbackAction{ActionAbort},
			wantTerminal: true,
		},
		// Unknown: conservative fallback.
		{
			class:       Unknown,
			wantActions: []FallbackAction{ActionQueueForLater, ActionEscalateAlert},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.class.String(), func(t *testing.T) {
			result := FallbackPath(runID, tc.class)

			// Assert the RunID is threaded through.
			if result.RunID != runID {
				t.Errorf("RunID: got %q, want %q", result.RunID, runID)
			}
			if result.Class != tc.class {
				t.Errorf("Class: got %v, want %v", result.Class, tc.class)
			}

			// Assert exact length.
			if len(result.Actions) != len(tc.wantActions) {
				t.Fatalf("Actions length: got %d, want %d (actions=%v)",
					len(result.Actions), len(tc.wantActions), result.Actions)
			}

			// Assert exact ordered sequence.
			for i, want := range tc.wantActions {
				if result.Actions[i] != want {
					t.Errorf("Actions[%d]: got %q, want %q (full path=%v)",
						i, result.Actions[i], want, result.Actions)
				}
			}

			// Terminal classes must have exactly one action and it must be Abort.
			if tc.wantTerminal {
				if len(result.Actions) != 1 || result.Actions[0] != ActionAbort {
					t.Errorf("terminal class %v: want [%s], got %v",
						tc.class, ActionAbort, result.Actions)
				}
			}

			// Non-terminal primary classes must NOT contain ActionAbort.
			if !tc.wantTerminal {
				for _, act := range result.Actions {
					if act == ActionAbort {
						t.Errorf("non-terminal class %v must not contain ActionAbort, got %v",
							tc.class, result.Actions)
					}
				}
			}

			// Distinct-path invariant: each primary class must produce a path that
			// differs from every other primary class path in at least the first or
			// second action position.
		})
	}

	// Sanity-check: all four primary non-terminal classes return distinct paths.
	primaryClasses := []ErrorClass{RateLimited, ServerError, Timeout}
	paths := make([][]FallbackAction, len(primaryClasses))
	for i, c := range primaryClasses {
		paths[i] = FallbackPath(runID, c).Actions
	}
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			if actionsEqual(paths[i], paths[j]) {
				t.Errorf("classes %v and %v have identical paths %v — swap is undetectable",
					primaryClasses[i], primaryClasses[j], paths[i])
			}
		}
	}
}

func actionsEqual(a, b []FallbackAction) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// TestSchedulerHonorsHints
//
// Verifies that Place passes the job hints through to the Placement and that
// the RunID is observable in the returned value.
// ---------------------------------------------------------------------------

// directScheduler is a real Scheduler seam backed by a real in-process HTTP
// server via net/http/httptest.  The server echoes the job hints back as JSON;
// the client unmarshals them and constructs a Placement.  This proves that the
// hint contract survives a real network round-trip inside the test process.
type directScheduler struct {
	server *httptest.Server
	client *http.Client
}

func newDirectScheduler(t *testing.T) *directScheduler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/schedule", func(w http.ResponseWriter, r *http.Request) {
		// Parse hints from query parameters (no external JSON decoder needed).
		q := r.URL.Query()
		jobID := q.Get("job_id")
		runID := q.Get("run_id")
		priority := 0
		if _, err := fmt.Sscanf(q.Get("priority"), "%d", &priority); err != nil {
			http.Error(w, "bad priority", http.StatusBadRequest)
			return
		}
		preemptable := q.Get("preemptable") == "true"
		flavor := q.Get("flavor")

		// Echo the hints back as a minimal JSON object.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w,
			`{"run_id":%q,"job_id":%q,"node_id":"node-42","priority":%d,"preemptable":%v,"flavor":%q}`,
			runID, jobID, priority, preemptable, flavor,
		)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &directScheduler{server: srv, client: srv.Client()}
}

func (s *directScheduler) Schedule(runID string, job SandboxJob) (Placement, error) {
	rawURL := fmt.Sprintf(
		"%s/schedule?run_id=%s&job_id=%s&priority=%d&preemptable=%v&flavor=%s",
		s.server.URL,
		url.QueryEscape(runID),
		url.QueryEscape(job.ID),
		job.Priority,
		job.Preemptable,
		url.QueryEscape(job.Flavor),
	)
	resp, err := s.client.Get(rawURL)
	if err != nil {
		return Placement{}, fmt.Errorf("directScheduler: HTTP error: %w", err)
	}
	defer resp.Body.Close()

	// Manual JSON decode using only the standard library.
	var p Placement
	n, err := fmt.Fscanf(resp.Body,
		`{"run_id":%q,"job_id":%q,"node_id":%q,"priority":%d,"preemptable":%v,"flavor":%q}`,
		&p.RunID, &p.JobID, &p.NodeID, &p.Priority, &p.Preemptable, &p.Flavor,
	)
	if err != nil {
		return Placement{}, fmt.Errorf("directScheduler: parse error: %w", err)
	}
	if n != 6 {
		return Placement{}, fmt.Errorf("directScheduler: parse error: scanned %d/6 fields", n)
	}
	return p, nil
}

func TestSchedulerHonorsHints(t *testing.T) {
	const runID = "run-scheduler-honors-hints"

	sched := newDirectScheduler(t)

	job := SandboxJob{
		ID:          "job-alpha",
		Priority:    7,
		Preemptable: true,
		Flavor:      "gpu-large",
	}

	placement, err := Place(runID, job, sched)
	if err != nil {
		t.Fatalf("Place: unexpected error: %v", err)
	}

	// Assert RunID threads through.
	if placement.RunID != runID {
		t.Errorf("RunID: got %q, want %q", placement.RunID, runID)
	}

	// Assert job hints are reflected unchanged.
	if placement.Priority != job.Priority {
		t.Errorf("Priority: got %d, want %d", placement.Priority, job.Priority)
	}
	if placement.Preemptable != job.Preemptable {
		t.Errorf("Preemptable: got %v, want %v", placement.Preemptable, job.Preemptable)
	}
	if placement.Flavor != job.Flavor {
		t.Errorf("Flavor: got %q, want %q", placement.Flavor, job.Flavor)
	}

	// Assert the scheduler assigned a non-empty NodeID (real sink-side evidence).
	if placement.NodeID == "" {
		t.Error("NodeID: scheduler returned empty node — no placement was made")
	}
}

// ---------------------------------------------------------------------------
// TestSchedulerHonorsHintsSpecialChars verifies that job IDs and flavors
// containing URL-special characters (spaces, '+', '&', '=') survive the
// HTTP round-trip through directScheduler unchanged.
//
// MUTATION GUARD: removing url.QueryEscape from directScheduler.Schedule
// causes the server to receive a truncated/corrupted flavor (e.g. the part
// after a '+' or ' ' is treated as a separate query parameter and dropped),
// making placement.Flavor != job.Flavor and failing this test.
// ---------------------------------------------------------------------------
func TestSchedulerHonorsHintsSpecialChars(t *testing.T) {
	const runID = "run-scheduler-special+chars&id=1"

	sched := newDirectScheduler(t)

	job := SandboxJob{
		ID:          "job beta+one",
		Priority:    3,
		Preemptable: false,
		Flavor:      "gpu large+fast&turbo=yes",
	}

	placement, err := Place(runID, job, sched)
	if err != nil {
		t.Fatalf("Place: unexpected error: %v", err)
	}

	// RunID must survive round-trip (it is URL-encoded in the query string).
	if placement.RunID != runID {
		t.Errorf("RunID: got %q, want %q", placement.RunID, runID)
	}
	// JobID must survive round-trip.
	if placement.JobID != job.ID {
		t.Errorf("JobID: got %q, want %q", placement.JobID, job.ID)
	}
	// Flavor containing '+' and '&' must survive unchanged.
	if placement.Flavor != job.Flavor {
		t.Errorf("Flavor: got %q, want %q (URL-encoding not applied)", placement.Flavor, job.Flavor)
	}
	// Scheduler must assign a real node.
	if placement.NodeID == "" {
		t.Error("NodeID: scheduler returned empty node")
	}
}

// ---------------------------------------------------------------------------
// TestClassifyRunID asserts that Classify embeds the RunID in the result.
// ---------------------------------------------------------------------------
func TestClassifyRunID(t *testing.T) {
	const runID = "run-classify-id"
	clf := newClassifier()

	result := clf.Classify(runID, ErrRateLimited)
	if result.RunID != runID {
		t.Errorf("RunID: got %q, want %q", result.RunID, runID)
	}
	if result.Class != RateLimited {
		t.Errorf("Class: got %v, want RateLimited", result.Class)
	}
}

// ---------------------------------------------------------------------------
// TestClassifyPredicatePrecedence verifies that injected predicates override
// the sentinel-error fallback.
// ---------------------------------------------------------------------------
func TestClassifyPredicatePrecedence(t *testing.T) {
	const runID = "run-classify-predicate"

	// Inject a predicate that always claims ServerError.
	clf := &Classifier{
		Predicates: map[ErrorClass]Predicate{
			ServerError: func(e error) bool { return true },
		},
		Now: fixedNow(),
	}

	// Even though the error is ErrRateLimited, the injected ServerError predicate fires first.
	result := clf.Classify(runID, ErrRateLimited)
	if result.RunID != runID {
		t.Errorf("RunID: got %q, want %q", result.RunID, runID)
	}
	if result.Class != ServerError {
		t.Errorf("Class: got %v, want ServerError (predicate should override sentinel)", result.Class)
	}
}

// ---------------------------------------------------------------------------
// TestClassifyUnknown verifies that an unrecognised error maps to Unknown.
// ---------------------------------------------------------------------------
func TestClassifyUnknown(t *testing.T) {
	const runID = "run-classify-unknown"
	clf := newClassifier()

	result := clf.Classify(runID, errors.New("some completely novel error"))
	if result.RunID != runID {
		t.Errorf("RunID: got %q, want %q", result.RunID, runID)
	}
	if result.Class != Unknown {
		t.Errorf("Class: got %v, want Unknown", result.Class)
	}
}

// ---------------------------------------------------------------------------
// TestClassifyTimestamp verifies the injected clock is used, not wall time.
// ---------------------------------------------------------------------------
func TestClassifyTimestamp(t *testing.T) {
	const runID = "run-classify-ts"
	fixed := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	clf := &Classifier{
		Predicates: map[ErrorClass]Predicate{},
		Now:        func() time.Time { return fixed },
	}

	result := clf.Classify(runID, ErrTimeout)
	if result.RunID != runID {
		t.Errorf("RunID: got %q, want %q", result.RunID, runID)
	}
	if !result.Timestamp.Equal(fixed) {
		t.Errorf("Timestamp: got %v, want %v (injected clock not used)", result.Timestamp, fixed)
	}
}

// ---------------------------------------------------------------------------
// TestPlaceNilScheduler verifies that Place returns ErrNoCapability when
// no scheduler is injected.
// ---------------------------------------------------------------------------
func TestPlaceNilScheduler(t *testing.T) {
	const runID = "run-place-nil"
	job := SandboxJob{ID: "job-nil", Priority: 1}

	_, err := Place(runID, job, nil)
	if err == nil {
		t.Fatal("Place with nil scheduler: expected error, got nil")
	}
	if !errors.Is(err, ErrNoCapability) {
		t.Errorf("error: got %v, want wrapping ErrNoCapability", err)
	}
}

// ---------------------------------------------------------------------------
// TestPlaceContractViolationPriority verifies that Place rejects a Placement
// that violates the hint-passthrough contract (priority not reflected).
// ---------------------------------------------------------------------------
func TestPlaceContractViolationPriority(t *testing.T) {
	const runID = "run-contract-priority"
	job := SandboxJob{ID: "job-bad", Priority: 5, Preemptable: false, Flavor: "cpu"}

	// Bad scheduler: returns wrong priority.
	bad := &badScheduler{priority: 99, preemptable: false, flavor: "cpu"}
	_, err := Place(runID, job, bad)
	if err == nil {
		t.Fatal("expected contract-violation error, got nil")
	}
}

// TestPlaceContractViolationPreemptable verifies that Place rejects a Placement
// that violates the hint-passthrough contract (preemptable not reflected).
func TestPlaceContractViolationPreemptable(t *testing.T) {
	const runID = "run-contract-preemptable"
	job := SandboxJob{ID: "job-bad2", Priority: 3, Preemptable: true, Flavor: "mem"}

	bad := &badScheduler{priority: 3, preemptable: false, flavor: "mem"} // flipped preemptable
	_, err := Place(runID, job, bad)
	if err == nil {
		t.Fatal("expected contract-violation error, got nil")
	}
}

// TestPlaceContractViolationFlavor verifies that Place rejects a Placement
// that violates the hint-passthrough contract (flavor not reflected).
func TestPlaceContractViolationFlavor(t *testing.T) {
	const runID = "run-contract-flavor"
	job := SandboxJob{ID: "job-bad3", Priority: 2, Preemptable: false, Flavor: "gpu-small"}

	bad := &badScheduler{priority: 2, preemptable: false, flavor: "wrong-flavor"}
	_, err := Place(runID, job, bad)
	if err == nil {
		t.Fatal("expected contract-violation error, got nil")
	}
}

// badScheduler is a Scheduler that returns a Placement with caller-controlled
// (potentially wrong) hints.
type badScheduler struct {
	priority    int
	preemptable bool
	flavor      string
}

func (b *badScheduler) Schedule(runID string, job SandboxJob) (Placement, error) {
	return Placement{
		RunID:       runID,
		JobID:       job.ID,
		NodeID:      "node-x",
		Priority:    b.priority,
		Preemptable: b.preemptable,
		Flavor:      b.flavor,
	}, nil
}

// ---------------------------------------------------------------------------
// TestFallbackPathCopy verifies that FallbackPath returns a copy so mutations
// by the caller do not affect subsequent calls.
// ---------------------------------------------------------------------------
func TestFallbackPathCopy(t *testing.T) {
	const runID = "run-copy"
	r1 := FallbackPath(runID, RateLimited)
	original := make([]FallbackAction, len(r1.Actions))
	copy(original, r1.Actions)

	// Mutate the returned slice.
	r1.Actions[0] = ActionAbort

	r2 := FallbackPath(runID, RateLimited)
	if r2.Actions[0] != original[0] {
		t.Errorf("FallbackPath returned a shared slice: mutation leaked (got %q, want %q)",
			r2.Actions[0], original[0])
	}
}

// ---------------------------------------------------------------------------
// TestErrorClassString verifies String() for all classes (sink-side: the string
// representation is used in log/metrics pipelines).
// ---------------------------------------------------------------------------
func TestErrorClassString(t *testing.T) {
	cases := []struct {
		class ErrorClass
		want  string
	}{
		{Unknown, "Unknown"},
		{RateLimited, "RateLimited"},
		{ServerError, "ServerError"},
		{Timeout, "Timeout"},
		{AuthError, "AuthError"},
		{ContentFiltered, "ContentFiltered"},
	}
	for _, tc := range cases {
		if got := tc.class.String(); got != tc.want {
			t.Errorf("ErrorClass(%d).String(): got %q, want %q", int(tc.class), got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestSentinelErrors verifies the sentinel-error path in Classify without any
// injected predicates.
// ---------------------------------------------------------------------------
func TestSentinelErrors(t *testing.T) {
	const runID = "run-sentinel"
	clf := &Classifier{
		Predicates: map[ErrorClass]Predicate{},
		Now:        fixedNow(),
	}

	cases := []struct {
		err       error
		wantClass ErrorClass
	}{
		{ErrRateLimited, RateLimited},
		{ErrServerError, ServerError},
		{ErrTimeout, Timeout},
		{ErrAuthError, AuthError},
		{ErrContentFiltered, ContentFiltered},
		// Wrapping sentinels must also be recognised.
		{fmt.Errorf("wrapped: %w", ErrTimeout), Timeout},
	}
	for _, tc := range cases {
		result := clf.Classify(runID, tc.err)
		if result.RunID != runID {
			t.Errorf("RunID: got %q, want %q", result.RunID, runID)
		}
		if result.Class != tc.wantClass {
			t.Errorf("Classify(%v): got %v, want %v", tc.err, result.Class, tc.wantClass)
		}
	}
}
