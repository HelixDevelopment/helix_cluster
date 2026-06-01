// Package agentprovision implements admission control and context sharing for
// interactive-agent workloads in Helix Cluster OS (HXC-1141).
package agentprovision

// WorkloadInteractiveAgent is the canonical workload-type string for
// interactive agents.  It is distinct from "batch" and "inference" and is
// intentionally defined here so callers need not touch the scheduler package.
const WorkloadInteractiveAgent = "interactive-agent"

// Agent represents an interactive agent workload submitted for admission.
type Agent struct {
	// ID is the unique identifier of this agent instance.
	ID string
	// UserID is the identifier of the user who owns this agent.
	UserID string
}
