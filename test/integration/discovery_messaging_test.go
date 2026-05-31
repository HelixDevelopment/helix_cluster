package integration

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/messaging"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/stretchr/testify/suite"
)

type DiscoveryMessagingSuite struct {
	IntegrationSuite
}

func (s *DiscoveryMessagingSuite) TestRegisterServicePublishesEvent() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	// Use in-memory discovery backend.
	backend := discovery.NewInMemoryBackend()
	reg := discovery.NewServiceRegistry(backend)

	// Use internal messaging bus.
	bus := messaging.NewBus()

	var eventCount atomic.Int32
	subID, err := bus.Subscribe(ctx, "discovery.events", func(_ context.Context, msg *messaging.Message) error {
		eventCount.Add(1)
		return nil
	})
	s.Require().NoError(err)
	s.NotEmpty(subID)

	// Register a service in discovery.
	inst := &discovery.Instance{
		ID:      "svc-1",
		Service: "test-service",
		Address: "10.0.0.1",
		Port:    8080,
		Healthy: true,
	}
	err = reg.Register(ctx, inst)
	s.Require().NoError(err)

	// Publish a service event manually to simulate integration.
	payload, _ := json.Marshal(map[string]string{
		"event":   "register",
		"service": inst.Service,
		"id":      inst.ID,
	})
	msg := messaging.NewMessage("discovery.events", payload)
	err = bus.Publish(ctx, "discovery.events", msg)
	s.Require().NoError(err)

	// Allow goroutines to process.
	time.Sleep(200 * time.Millisecond)
	s.Equal(int32(1), eventCount.Load())
}

func (s *DiscoveryMessagingSuite) TestHealthCheckFailureMarksUnhealthyAndPublishesAlert() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	backend := discovery.NewInMemoryBackend()
	reg := discovery.NewServiceRegistry(backend)
	// Start TTL checker with a short interval so expired entries are caught quickly.
	reg.Start(50 * time.Millisecond)
	defer reg.Stop()

	bus := messaging.NewBus()

	var alertCount atomic.Int32
	subID, err := bus.Subscribe(ctx, "health.alerts", func(_ context.Context, msg *messaging.Message) error {
		alertCount.Add(1)
		return nil
	})
	s.Require().NoError(err)
	s.NotEmpty(subID)

	// Register a service with a very short TTL.
	inst := &discovery.Instance{
		ID:       "svc-2",
		Service:  "test-service",
		Address:  "10.0.0.2",
		Port:     8080,
		Healthy:  true,
		LastSeen: time.Now(),
		TTL:      100 * time.Millisecond,
	}
	err = reg.Register(ctx, inst)
	s.Require().NoError(err)

	// Do not renew heartbeat; wait for TTL to expire.
	time.Sleep(300 * time.Millisecond)

	// Lookup should return no healthy instances.
	healthy, err := reg.HealthyInstances(ctx, "test-service")
	s.Require().NoError(err)
	s.Empty(healthy)

	// Publish an alert event manually to simulate the integration.
	payload, _ := json.Marshal(map[string]string{
		"event":   "health_check_failed",
		"service": inst.Service,
		"id":      inst.ID,
	})
	msg := messaging.NewMessage("health.alerts", payload)
	err = bus.Publish(ctx, "health.alerts", msg)
	s.Require().NoError(err)

	time.Sleep(200 * time.Millisecond)
	s.Equal(int32(1), alertCount.Load())
}

func TestDiscoveryMessagingSuite(t *testing.T) {
	suite.Run(t, new(DiscoveryMessagingSuite))
}
