// Package health provides the health check aggregation service for Helix Cluster OS.
package health

import (
	"context"
	"sync"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the helix.v1.HealthService gRPC interface.
type Server struct {
	helixv1.UnimplementedHealthServiceServer

	aggregator *Aggregator
	mu         sync.RWMutex
	watchers   map[chan *helixv1.HealthEvent]struct{}
}

// NewServer creates a new gRPC health server backed by an Aggregator.
func NewServer(aggregator *Aggregator) *Server {
	return &Server{
		aggregator: aggregator,
		watchers:   make(map[chan *helixv1.HealthEvent]struct{}),
	}
}

// Check returns the health status for a specific service or the overall cluster.
func (s *Server) Check(ctx context.Context, req *helixv1.CheckRequest) (*helixv1.CheckResponse, error) {
	st, details := s.aggregator.StatusWithDetails()

	if req.Service != "" {
		svc, ok := s.aggregator.GetServiceStatus(req.Service)
		if !ok {
			return nil, status.Errorf(codes.NotFound, "service %q not found", req.Service)
		}
		return &helixv1.CheckResponse{
			Status: svc.Status.String(),
			Details: map[string]string{
				req.Service: details[req.Service],
			},
		}, nil
	}

	return &helixv1.CheckResponse{
		Status:  st.String(),
		Details: details,
	}, nil
}

// Watch streams health updates to the client until the context is done.
func (s *Server) Watch(req *helixv1.WatchRequest, stream grpc.ServerStreamingServer[helixv1.HealthEvent]) error {
	ch := make(chan *helixv1.HealthEvent, 8)
	s.mu.Lock()
	s.watchers[ch] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.watchers, ch)
		s.mu.Unlock()
		close(ch)
	}()

	// Send initial snapshot.
	if err := s.sendCurrentStatus(req.NodeId, stream); err != nil {
		return err
	}

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		}
	}
}

// ReportHealth accepts a health report from a node.
func (s *Server) ReportHealth(ctx context.Context, req *helixv1.ReportHealthRequest) (*helixv1.ReportHealthResponse, error) {
	// For now, accept the report and broadcast it to watchers.
	s.broadcast(&helixv1.HealthEvent{
		NodeId:    req.NodeId,
		Status:    s.aggregator.GetStatus().String(),
		Score:     req.Score,
		Timestamp: time.Now().Unix(),
	})
	return &helixv1.ReportHealthResponse{Accepted: true}, nil
}

// broadcast sends an event to all active watchers.
func (s *Server) broadcast(evt *helixv1.HealthEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ch := range s.watchers {
		select {
		case ch <- evt:
		default:
		}
	}
}

// sendCurrentStatus sends the current aggregated status as a HealthEvent.
func (s *Server) sendCurrentStatus(nodeID string, stream grpc.ServerStreamingServer[helixv1.HealthEvent]) error {
	st, _ := s.aggregator.StatusWithDetails()
	return stream.Send(&helixv1.HealthEvent{
		NodeId:    nodeID,
		Status:    st.String(),
		Timestamp: time.Now().Unix(),
	})
}
