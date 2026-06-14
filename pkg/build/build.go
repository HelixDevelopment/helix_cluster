package build

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// State represents the lifecycle of a build job.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Job represents a single build request.
type Job struct {
	ID             string            `json:"id"`
	RepoURL        string            `json:"repo_url"`
	Ref            string            `json:"ref"`
	DockerfilePath string            `json:"dockerfile_path"`
	BuildArgs      map[string]string `json:"build_args"`
	State          State             `json:"state"`
	ImageTag       string            `json:"image_tag"`
	CreatedAt      time.Time         `json:"created_at"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	CompletedAt    *time.Time        `json:"completed_at,omitempty"`
	Logs           []string          `json:"logs"`
	mu             sync.RWMutex
	// done is closed exactly once when the job enters a terminal state.
	// Readers can select/wait on Done() without polling.
	done chan struct{}
	// doneOnce guards the single close of done.
	doneOnce sync.Once
}

// Done returns a channel that is closed when the job reaches a terminal state
// (StateSucceeded, StateFailed, or StateCancelled). It is safe to call Done
// from multiple goroutines. The channel is never nil once the job has been
// enqueued via Service.Submit.
func (j *Job) Done() <-chan struct{} {
	return j.done
}

// IsTerminal returns true if the build has reached a terminal state.
func (j *Job) IsTerminal() bool {
	switch j.State {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// AppendLog adds a log line thread-safely.
func (j *Job) AppendLog(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Logs = append(j.Logs, line)
}

// GetLogs returns a copy of log lines thread-safely.
func (j *Job) GetLogs() []string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]string, len(j.Logs))
	copy(out, j.Logs)
	return out
}

// SetImageTag sets the resulting image tag thread-safely. Builders MUST use
// this (rather than assigning Job.ImageTag directly) so concurrent readers via
// Service.Get/clone observe the field under the same lock.
func (j *Job) SetImageTag(tag string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ImageTag = tag
}

// TransitionTo updates the job state with validation.
func (j *Job) TransitionTo(newState State) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.IsTerminal() {
		return fmt.Errorf("cannot transition from terminal state %s", j.State)
	}

	valid := false
	switch j.State {
	case StateQueued:
		valid = newState == StateRunning || newState == StateCancelled
	case StateRunning:
		valid = newState == StateSucceeded || newState == StateFailed || newState == StateCancelled
	}

	if !valid {
		return fmt.Errorf("invalid transition: %s -> %s", j.State, newState)
	}

	j.State = newState
	now := time.Now().UTC()
	switch newState {
	case StateRunning:
		j.StartedAt = &now
	case StateSucceeded, StateFailed, StateCancelled:
		j.CompletedAt = &now
		// Signal Done() waiters. Must be called while still under j.mu so that
		// readers who observe the closed channel also see the terminal State.
		if j.done != nil {
			j.doneOnce.Do(func() { close(j.done) })
		}
	}
	return nil
}

// Builder executes the actual build for a job and is the seam through which a
// REAL image builder (e.g. docker buildx / buildkit) is injected.
//
// Contract: Build MUST drive the job to a terminal state via Job.TransitionTo
// (StateSucceeded / StateFailed / StateCancelled) and, on success of a REAL
// build, set Job.ImageTag to a tag that refers to an image that actually
// exists and is pullable. The default implementation (simulatedBuilder) does
// NOT satisfy that last guarantee — see its documentation.
type Builder interface {
	// Build runs the build for j. It must respect ctx cancellation and must
	// move j into a terminal state before returning.
	Build(ctx context.Context, j *Job)
}

// simulatedBuilder is a NON-PRODUCTION, SIMULATED build implementation.
//
// WARNING (PCS-6 / CLAUDE-1): This builder does NOT build anything real. It
// does not clone a repository, read a Dockerfile, invoke any container builder,
// produce any layer, compute any digest, or push to any registry. It sleeps
// briefly, branches on the sentinel RepoURL "fail" to simulate a failure, and
// fabricates an ImageTag string that does NOT correspond to any real image.
//
// It exists only so the orchestration plumbing (queue, worker pool, state
// machine, cancellation) can be exercised in unit tests. A green test using
// this builder proves ONLY that the SIMULATION ran — it is NOT evidence that
// any image was built. Inject a real Builder via NewServiceWithBuilder for
// production use.
type simulatedBuilder struct{}

// Simulated is an exported, unmistakable marker that this Builder produces no
// real artifact. Callers and tests can assert b.Simulated() to confirm they are
// running against the non-production simulation rather than a real builder.
func (simulatedBuilder) Simulated() bool { return true }

// Build implements Builder with a simulated (fake) build. See type docs.
func (simulatedBuilder) Build(ctx context.Context, j *Job) {
	j.AppendLog(fmt.Sprintf("[%s] SIMULATED build started on worker (NON-PRODUCTION; no real image is produced)", time.Now().UTC().Format(time.RFC3339)))

	select {
	case <-ctx.Done():
		j.TransitionTo(StateCancelled)
		j.AppendLog("Build cancelled by context")
		return
	case <-time.After(100 * time.Millisecond):
	}

	if j.RepoURL == "fail" {
		j.TransitionTo(StateFailed)
		j.AppendLog("Build failed: simulated failure")
		return
	}

	// NOTE: This tag is fabricated. No image with this tag exists anywhere.
	j.SetImageTag(fmt.Sprintf("helix/%s:%s", j.ID, j.Ref))
	j.TransitionTo(StateSucceeded)
	j.AppendLog(fmt.Sprintf("SIMULATED build succeeded: fabricated image tag %s (no real artifact)", j.ImageTag))
}

// Service orchestrates build jobs.
type Service struct {
	mu      sync.RWMutex
	jobs    map[string]*Job
	workers int
	queue   chan *Job
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	builder Builder
}

// NewService creates a build service with the given worker pool size.
//
// WARNING: The returned Service uses simulatedBuilder, a NON-PRODUCTION
// simulation that produces NO real image. Use NewServiceWithBuilder with a real
// Builder for production. See simulatedBuilder docs.
func NewService(workers int) *Service {
	return NewServiceWithBuilder(workers, simulatedBuilder{})
}

// NewServiceWithBuilder creates a build service backed by the given Builder.
// This is the injection seam for a real image builder.
func NewServiceWithBuilder(workers int, builder Builder) *Service {
	if workers < 1 {
		workers = 1
	}
	if builder == nil {
		builder = simulatedBuilder{}
	}
	return &Service{
		jobs:    make(map[string]*Job),
		workers: workers,
		queue:   make(chan *Job, 100),
		builder: builder,
	}
}

// Start begins the worker pool.
func (s *Service) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx, i)
	}
}

// Stop gracefully shuts down the service.
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// Submit enqueues a new build job.
func (s *Service) Submit(j *Job) error {
	if j.ID == "" {
		return fmt.Errorf("job ID is required")
	}
	if j.RepoURL == "" {
		return fmt.Errorf("repo URL is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[j.ID]; exists {
		return fmt.Errorf("job %s already exists", j.ID)
	}

	j.State = StateQueued
	j.CreatedAt = time.Now().UTC()
	j.Logs = make([]string, 0)
	j.done = make(chan struct{})
	s.jobs[j.ID] = j

	select {
	case s.queue <- j:
	default:
		return fmt.Errorf("build queue is full")
	}
	return nil
}

// Get retrieves a deep copy of a job by ID.
func (s *Service) Get(id string) (*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, fmt.Errorf("job %s not found", id)
	}
	return j.clone(), nil
}

// cloneStringMap returns an independent copy of m (nil-safe), so a snapshot's
// map cannot alias the canonical job's map.
func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// clone returns a deep copy of the job (logs copied, mutex zeroed).
func (j *Job) clone() *Job {
	j.mu.RLock()
	defer j.mu.RUnlock()
	c := &Job{
		ID:             j.ID,
		RepoURL:        j.RepoURL,
		Ref:            j.Ref,
		DockerfilePath: j.DockerfilePath,
		BuildArgs:      cloneStringMap(j.BuildArgs),
		State:          j.State,
		ImageTag:       j.ImageTag,
		CreatedAt:      j.CreatedAt,
		StartedAt:      j.StartedAt,
		CompletedAt:    j.CompletedAt,
		Logs:           make([]string, len(j.Logs)),
	}
	copy(c.Logs, j.Logs)
	if j.StartedAt != nil {
		t := *j.StartedAt
		c.StartedAt = &t
	}
	if j.CompletedAt != nil {
		t := *j.CompletedAt
		c.CompletedAt = &t
	}
	return c
}

// WaitDone blocks until the job identified by id reaches a terminal state or
// the context is cancelled, then returns a snapshot via Get. It is the
// preferred alternative to polling in tests and production callers alike.
func (s *Service) WaitDone(ctx context.Context, id string) (*Job, error) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("job %s not found", id)
	}
	select {
	case <-j.Done():
		return j.clone(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// List returns all jobs.
func (s *Service) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

// Cancel attempts to cancel a queued or running job.
func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	return j.TransitionTo(StateCancelled)
}

// worker processes jobs from the queue.
func (s *Service) worker(ctx context.Context, id int) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.queue:
			if err := j.TransitionTo(StateRunning); err != nil {
				continue
			}
			s.builder.Build(ctx, j)
		}
	}
}
