// Package build provides the build orchestration service for Helix Cluster OS.
package build

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/build"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
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

// Job represents a single build request managed by the orchestrator.
type Job struct {
	ID             string
	RepoURL        string
	Ref            string
	DockerfilePath string
	BuildArgs      map[string]string
	State          State
	ImageTag       string
	CreatedAt      time.Time
	StartedAt      *time.Time
	CompletedAt    *time.Time
	Logs           []string
	mu             sync.RWMutex
}

// IsTerminal returns true if the build has reached a terminal state.
func (j *Job) IsTerminal() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
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

	switch j.State {
	case StateSucceeded, StateFailed, StateCancelled:
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

// clone returns a deep copy of the job.
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

// BuildSpec defines the parameters for submitting a new build job.
type BuildSpec struct {
	ID             string
	RepoURL        string
	Ref            string
	DockerfilePath string
	BuildArgs      map[string]string
	Platforms      []build.Platform
}

// BuildFilters defines filtering criteria for listing builds.
type BuildFilters struct {
	State   State
	RepoURL string
}

// Orchestrator manages build jobs, their lifecycle, artifacts, and cache.
type Orchestrator struct {
	mu        sync.RWMutex
	jobs      map[string]*Job
	queue     chan *Job
	cache     cache.Cache
	artifacts map[string]string // buildID -> digest
	workers   int
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewOrchestrator creates a build orchestrator with the given worker pool size and cache.
func NewOrchestrator(workers int, c cache.Cache) *Orchestrator {
	if workers < 1 {
		workers = 1
	}
	if c == nil {
		c = cache.NewMemoryCache()
	}
	return &Orchestrator{
		jobs:      make(map[string]*Job),
		queue:     make(chan *Job, 100),
		cache:     c,
		artifacts: make(map[string]string),
		workers:   workers,
	}
}

// Start begins the internal worker pool that processes queued jobs.
func (o *Orchestrator) Start(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	o.cancel = cancel
	for i := 0; i < o.workers; i++ {
		o.wg.Add(1)
		go o.worker(ctx, i)
	}
}

// Stop gracefully shuts down the orchestrator and waits for workers to finish.
func (o *Orchestrator) Stop() {
	o.mu.Lock()
	cancel := o.cancel
	o.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	o.wg.Wait()
}

// SubmitBuild enqueues a new build job from a spec.
func (o *Orchestrator) SubmitBuild(ctx context.Context, spec BuildSpec) (*Job, error) {
	if spec.ID == "" {
		return nil, fmt.Errorf("build ID is required")
	}
	if spec.RepoURL == "" {
		return nil, fmt.Errorf("repo URL is required")
	}

	j := &Job{
		ID:             spec.ID,
		RepoURL:        spec.RepoURL,
		Ref:            spec.Ref,
		DockerfilePath: spec.DockerfilePath,
		BuildArgs:      spec.BuildArgs,
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if _, exists := o.jobs[j.ID]; exists {
		return nil, fmt.Errorf("build %s already exists", j.ID)
	}

	j.State = StateQueued
	j.CreatedAt = time.Now().UTC()
	j.Logs = make([]string, 0)
	o.jobs[j.ID] = j

	select {
	case o.queue <- j:
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, fmt.Errorf("build queue is full")
	}

	return j, nil
}

// GetBuildStatus retrieves the current status of a build by ID.
func (o *Orchestrator) GetBuildStatus(ctx context.Context, buildID string) (*Job, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	j, ok := o.jobs[buildID]
	if !ok {
		return nil, fmt.Errorf("build %s not found", buildID)
	}
	return j.clone(), nil
}

// CancelBuild attempts to cancel a queued or running build.
func (o *Orchestrator) CancelBuild(ctx context.Context, buildID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	j, ok := o.jobs[buildID]
	if !ok {
		return fmt.Errorf("build %s not found", buildID)
	}
	return j.TransitionTo(StateCancelled)
}

// ListBuilds returns all builds matching the provided filters.
func (o *Orchestrator) ListBuilds(ctx context.Context, filters BuildFilters) []*Job {
	o.mu.RLock()
	jobs := make([]*Job, 0, len(o.jobs))
	for _, j := range o.jobs {
		jobs = append(jobs, j)
	}
	o.mu.RUnlock()

	out := make([]*Job, 0, len(jobs))
	for _, j := range jobs {
		c := j.clone()
		if filters.State != "" && c.State != filters.State {
			continue
		}
		if filters.RepoURL != "" && c.RepoURL != filters.RepoURL {
			continue
		}
		out = append(out, c)
	}
	return out
}

// RegisterArtifact associates a build with a cached artifact digest.
func (o *Orchestrator) RegisterArtifact(buildID, digest string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.artifacts[buildID] = digest
}

// GetArtifactDigest returns the artifact digest for a build, if any.
func (o *Orchestrator) GetArtifactDigest(buildID string) (string, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	digest, ok := o.artifacts[buildID]
	return digest, ok
}

// Cache returns the underlying cache instance.
func (o *Orchestrator) Cache() cache.Cache {
	return o.cache
}

// worker processes jobs from the queue.
func (o *Orchestrator) worker(ctx context.Context, id int) {
	defer o.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-o.queue:
			if err := j.TransitionTo(StateRunning); err != nil {
				continue
			}
			o.runBuild(ctx, j)
		}
	}
}

// runBuild executes the build logic. Override in production.
func (o *Orchestrator) runBuild(ctx context.Context, j *Job) {
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

	j.mu.Lock()
	j.ImageTag = fmt.Sprintf("helix/%s:%s", j.ID, j.Ref)
	j.mu.Unlock()
	j.TransitionTo(StateSucceeded)
	j.AppendLog(fmt.Sprintf("Build succeeded: image %s", j.ImageTag))
}
