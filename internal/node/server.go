// Package node provides the gRPC NodeService implementation.
package node

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/api/v1"
)

// nowFunc is the clock used for heartbeat/last-seen tracking. It is a package
// variable so tests can install a deterministic clock.
var nowFunc = time.Now

// Server implements helixv1.NodeServiceServer.
type Server struct {
	helixv1.UnimplementedNodeServiceServer
	nodes    map[string]*helixv1.Node
	health   map[string]*helixv1.HealthScore
	lastSeen map[string]time.Time
	mu       sync.RWMutex
}

// NewServer creates a new NodeService server.
func NewServer() *Server {
	return &Server{
		nodes:    make(map[string]*helixv1.Node),
		health:   make(map[string]*helixv1.HealthScore),
		lastSeen: make(map[string]time.Time),
	}
}

// RegisterNode registers a new node in the cluster.
func (s *Server) RegisterNode(ctx context.Context, req *helixv1.RegisterNodeRequest) (*helixv1.RegisterNodeResponse, error) {
	if req.Node == nil || req.Node.Id == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	req.Node.Status = "active"
	s.nodes[req.Node.Id] = req.Node
	s.lastSeen[req.Node.Id] = nowFunc()

	return &helixv1.RegisterNodeResponse{
		NodeId:   req.Node.Id,
		Accepted: true,
	}, nil
}

// GetNode retrieves a node by ID.
func (s *Server) GetNode(ctx context.Context, req *helixv1.GetNodeRequest) (*helixv1.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[req.NodeId]
	if !ok {
		return nil, fmt.Errorf("node %s not found", req.NodeId)
	}
	return node, nil
}

// ListNodes returns all nodes matching the provided filters.
func (s *Server) ListNodes(ctx context.Context, req *helixv1.ListNodesRequest) (*helixv1.ListNodesResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*helixv1.Node
	for _, node := range s.nodes {
		if req.Region != "" && node.Region != req.Region {
			continue
		}
		if !matchLabels(node.Labels, req.Filters) {
			continue
		}
		out = append(out, node)
	}
	return &helixv1.ListNodesResponse{Nodes: out}, nil
}

// UpdateNodeStatus updates a node's status and health score. It also doubles as
// the node heartbeat: each call refreshes the node's last-seen timestamp.
func (s *Server) UpdateNodeStatus(ctx context.Context, req *helixv1.UpdateNodeStatusRequest) (*helixv1.Node, error) {
	if req.NodeId == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[req.NodeId]
	if !ok {
		return nil, fmt.Errorf("node %s not found", req.NodeId)
	}

	if req.Status != "" {
		node.Status = req.Status
	}
	if req.Health != nil {
		s.health[req.NodeId] = req.Health
	}
	s.lastSeen[req.NodeId] = nowFunc()
	return node, nil
}

// DeregisterNode removes a node from the registry.
func (s *Server) DeregisterNode(ctx context.Context, req *helixv1.DeregisterNodeRequest) (*helixv1.DeregisterNodeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.nodes[req.NodeId]; !ok {
		return &helixv1.DeregisterNodeResponse{Success: false}, nil
	}
	delete(s.nodes, req.NodeId)
	delete(s.health, req.NodeId)
	delete(s.lastSeen, req.NodeId)
	return &helixv1.DeregisterNodeResponse{Success: true}, nil
}

// HealthScore returns the most recently reported health score for a node, along
// with whether one has ever been recorded.
func (s *Server) HealthScore(nodeID string) (*helixv1.HealthScore, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.health[nodeID]
	return h, ok
}

// LastSeen returns the timestamp of the node's most recent registration or
// status update, and whether the node is currently tracked.
func (s *Server) LastSeen(nodeID string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.lastSeen[nodeID]
	return t, ok
}

// StaleNodes returns the IDs of tracked nodes whose last heartbeat is older than
// the supplied threshold relative to now. The result is unordered.
func (s *Server) StaleNodes(threshold time.Duration) []string {
	now := nowFunc()
	s.mu.RLock()
	defer s.mu.RUnlock()

	var stale []string
	for id, seen := range s.lastSeen {
		if now.Sub(seen) > threshold {
			stale = append(stale, id)
		}
	}
	return stale
}

// matchLabels returns true if nodeLabels contains all required labels.
func matchLabels(nodeLabels map[string]string, required map[string]string) bool {
	for k, v := range required {
		if nodeLabels[k] != v {
			return false
		}
	}
	return true
}
