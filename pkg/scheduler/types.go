package scheduler

// JobStatus represents the lifecycle state of a job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusScheduled JobStatus = "scheduled"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
)

// Resources describes compute requirements.
type Resources struct {
	CPU    float64 // CPU cores
	Memory uint64  // Memory in MB
	GPU    int     // Number of GPUs
}

// Job is a unit of work to be scheduled.
type Job struct {
	ID       string
	Command  string
	Resources Resources
	Priority int
	Status   JobStatus
	Labels   map[string]string // requirements / constraints
}

// Node is a cluster node that can run jobs.
type Node struct {
	ID                 string
	AvailableResources Resources
	Labels             map[string]string
}

// ScheduleResult is the outcome of a scheduling attempt.
type ScheduleResult struct {
	AssignedNode string
	Status       JobStatus
}

// Plugin is the interface for scheduling plugins.
type Plugin interface {
	Name() string
	// Filter returns true if the node is acceptable for the job.
	Filter(job *Job, node *Node) bool
	// Score returns a score for the node (higher is better).
	Score(job *Job, node *Node) int
	// Bind is called after a node is chosen; returns true on success.
	Bind(job *Job, node *Node) bool
}
