// Package node provides the gRPC NodeService implementation.
package node

import (
	"context"
	"fmt"
	"sync"

	"github.com/HelixDevelopment/helix_cluster/api/v1"
)

// Server implements helixv1.NodeServiceServer.
type Server struct {
	helixv1.UnimplementedNodeServiceServer
	nodes map[string]*helixv1.Node
	mu    sync.RWMutex
}

// NewServer creates a new NodeService server.
func NewServer() *Server {
	return &Server{
		nodes: make(map[string]*helixv1.Node),
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

// UpdateNodeStatus updates a node's status and health score.
func (s *Server) UpdateNodeStatus(ctx context.Context, req *helixv1.UpdateNodeStatusRequest) (*helixv1.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[req.NodeId]
	if !ok {
		return nil, fmt.Errorf("node %s not found", req.NodeId)
	}

	node.Status = req.Status
	if req.Health != nil && node.Resources == nil {
		node.Resources = &helixv1.NodeResources{}
	}
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
	return &helixv1.DeregisterNodeResponse{Success: true}, nil
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
