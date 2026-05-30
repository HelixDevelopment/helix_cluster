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
	StateQueued     State = "queued"
	StateRunning    State = "running"
	StateSucceeded  State = "succeeded"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
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
	}
	return nil
}

// Service orchestrates build jobs.
type Service struct {
	mu      sync.RWMutex
	jobs    map[string]*Job
	workers int
	queue   chan *Job
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewService creates a build service with the given worker pool size.
func NewService(workers int) *Service {
	if workers < 1 {
		workers = 1
	}
	return &Service{
		jobs:    make(map[string]*Job),
		workers: workers,
		queue:   make(chan *Job, 100),
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

// clone returns a deep copy of the job (logs copied, mutex zeroed).
func (j *Job) clone() *Job {
	j.mu.RLock()
	defer j.mu.RUnlock()
	c := &Job{
		ID:             j.ID,
		RepoURL:        j.RepoURL,
		Ref:            j.Ref,
		DockerfilePath: j.DockerfilePath,
		BuildArgs:      j.BuildArgs,
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
			s.runBuild(ctx, j)
		}
	}
}

// runBuild executes the build logic. Override in production.
func (s *Service) runBuild(ctx context.Context, j *Job) {
	j.AppendLog(fmt.Sprintf("[%s] Build started on worker", time.Now().UTC().Format(time.RFC3339)))

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

	j.ImageTag = fmt.Sprintf("helix/%s:%s", j.ID, j.Ref)
	j.TransitionTo(StateSucceeded)
	j.AppendLog(fmt.Sprintf("Build succeeded: image %s", j.ImageTag))
}
