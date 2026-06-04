package scheduler

import (
	"fmt"
	"strings"
)

// NodeResourcesFit checks that a node has sufficient resources.
type NodeResourcesFit struct{}

func (n *NodeResourcesFit) Name() string { return "NodeResourcesFit" }

func (n *NodeResourcesFit) Filter(job *Job, node *Node) bool {
	if node == nil {
		return false
	}
	return node.AvailableResources.CPU >= job.Resources.CPU &&
		node.AvailableResources.Memory >= job.Resources.Memory &&
		node.AvailableResources.GPU >= job.Resources.GPU
}

func (n *NodeResourcesFit) Score(job *Job, node *Node) int {
	if !n.Filter(job, node) {
		return 0
	}
	// Prefer nodes with more remaining resources after placement.
	score := 0
	if job.Resources.CPU > 0 {
		score += int((node.AvailableResources.CPU - job.Resources.CPU) * 100)
	}
	if job.Resources.Memory > 0 && node.AvailableResources.Memory >= job.Resources.Memory {
		// Memory is in MB; the difference cannot approach int64 max, so the
		// uint64->int score conversion is value-preserving. Guarding the
		// subtraction also prevents a uint64 underflow when job > available.
		score += int((node.AvailableResources.Memory - job.Resources.Memory) / 1024) //gosec:disable G115 -- bounded MB-scale resource value, well within int range
	}
	if job.Resources.GPU > 0 {
		score += (node.AvailableResources.GPU - job.Resources.GPU) * 1000
	}
	return score
}

func (n *NodeResourcesFit) Bind(job *Job, node *Node) bool {
	if !n.Filter(job, node) {
		return false
	}
	node.AvailableResources.CPU -= job.Resources.CPU
	node.AvailableResources.Memory -= job.Resources.Memory
	node.AvailableResources.GPU -= job.Resources.GPU
	return true
}

// PrioritySort is a queue-level plugin; it does not filter or score nodes.
type PrioritySort struct{}

func (p *PrioritySort) Name() string { return "PrioritySort" }

func (p *PrioritySort) Filter(job *Job, node *Node) bool { return true }
func (p *PrioritySort) Score(job *Job, node *Node) int   { return 0 }
func (p *PrioritySort) Bind(job *Job, node *Node) bool   { return true }

// CapabilityMatch ensures node labels satisfy job label requirements.
type CapabilityMatch struct{}

func (c *CapabilityMatch) Name() string { return "CapabilityMatch" }

func (c *CapabilityMatch) Filter(job *Job, node *Node) bool {
	if node == nil {
		return false
	}
	for k, v := range job.Labels {
		if strings.HasPrefix(k, "require_") {
			reqKey := strings.TrimPrefix(k, "require_")
			nodeVal, ok := node.Labels[reqKey]
			if !ok || !strings.EqualFold(nodeVal, v) {
				return false
			}
		}
	}
	return true
}

func (c *CapabilityMatch) Score(job *Job, node *Node) int {
	if !c.Filter(job, node) {
		return 0
	}
	matches := 0
	for k, v := range job.Labels {
		if strings.HasPrefix(k, "prefer_") {
			prefKey := strings.TrimPrefix(k, "prefer_")
			if nodeVal, ok := node.Labels[prefKey]; ok && strings.EqualFold(nodeVal, v) {
				matches++
			}
		}
	}
	return matches * 10
}

func (c *CapabilityMatch) Bind(job *Job, node *Node) bool {
	return true
}

// LoadAware prefers underutilized nodes.
type LoadAware struct{}

func (l *LoadAware) Name() string { return "LoadAware" }

func (l *LoadAware) Filter(job *Job, node *Node) bool { return true }

func (l *LoadAware) Score(job *Job, node *Node) int {
	if node == nil {
		return 0
	}
	// Higher score for more free resources.
	score := int(node.AvailableResources.CPU*10) +
		int(node.AvailableResources.Memory/1024) + //gosec:disable G115 -- bounded MB-scale resource value, well within int range
		node.AvailableResources.GPU*100
	return score
}

func (l *LoadAware) Bind(job *Job, node *Node) bool { return true }

// ErrNoNodeAvailable is returned when no node passes all filter plugins.
var ErrNoNodeAvailable = fmt.Errorf("no node available for job")
