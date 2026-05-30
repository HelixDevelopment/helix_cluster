package infra

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, "helix-cluster", cfg.ProjectName)
	assert.Equal(t, 5*time.Minute, cfg.DefaultTimeout)
	assert.NotEmpty(t, cfg.Services)
}

func TestNewOrchestrator(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	require.NotNil(t, o)
	assert.NotNil(t, o.services)
	assert.NotNil(t, o.vmNodes)
}

func TestInfraOrchestrator_Boot(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	// Boot specific services
	results, err := o.Boot(ctx, []string{"postgres-primary", "redis-master-1"})
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.Equal(t, "running", results[0].Status)
	assert.True(t, results[0].Healthy)

	// Boot all (empty slice)
	results2, err := o.Boot(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, results2, len(cfg.Services))
}

func TestInfraOrchestrator_Stop(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	results, err := o.Stop(ctx, []string{"postgres-primary"}, false)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "stopped", results[0].Status)
	assert.False(t, results[0].Healthy)
}

func TestInfraOrchestrator_Stop_WithVolumes(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	results, err := o.Stop(ctx, []string{"postgres-primary"}, true)
	require.NoError(t, err)
	assert.Contains(t, results[0].Message, "volumes removed")
}

func TestInfraOrchestrator_Status(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	results, err := o.Status(ctx, []string{"postgres-primary"})
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "postgres-primary", results[0].Name)

	// Not found
	results2, err := o.Status(ctx, []string{"nonexistent"})
	require.NoError(t, err)
	assert.Equal(t, "not_found", results2[0].Status)
}

func TestInfraOrchestrator_Health(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	results, err := o.Health(ctx, []string{"postgres-primary"})
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.True(t, results[0].Healthy)
}

func TestInfraOrchestrator_Logs(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	err := o.Logs(ctx, "postgres-primary", true, 100, "", true)
	assert.NoError(t, err)

	err = o.Logs(ctx, "nonexistent", true, 100, "", true)
	assert.Error(t, err)
}

func TestInfraOrchestrator_Scale(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.Boot(ctx, []string{"postgres-primary"})
	status, err := o.Scale(ctx, "postgres-primary", 3)
	require.NoError(t, err)
	assert.Equal(t, 3, status.Replicas)
	assert.Contains(t, status.Message, "scaled to 3")
}

func TestInfraOrchestrator_VMSpawn(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, err := o.VMSpawn(ctx, 3)
	require.NoError(t, err)
	assert.Len(t, nodes, 3)
	assert.Equal(t, "running", nodes[0].Status)
	assert.Equal(t, 2, nodes[0].CPU)
	assert.Equal(t, int64(4096), nodes[0].Memory)
}

func TestInfraOrchestrator_VMDestroy(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	err := o.VMDestroy(ctx, nodes[0].ID)
	assert.NoError(t, err)

	list, _ := o.VMList(ctx)
	assert.Len(t, list, 0)

	err = o.VMDestroy(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestInfraOrchestrator_VMList(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	_, _ = o.VMSpawn(ctx, 2)
	list, err := o.VMList(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestInfraOrchestrator_VMStatus(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	status, err := o.VMStatus(ctx, nodes[0].ID)
	require.NoError(t, err)
	assert.Equal(t, nodes[0].ID, status.ID)

	_, err = o.VMStatus(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestInfraOrchestrator_VMSSH(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	cmd, err := o.VMSSH(ctx, nodes[0].ID)
	require.NoError(t, err)
	assert.Contains(t, cmd, "ssh")

	_, err = o.VMSSH(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestInfraOrchestrator_VMSimulateFailure(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	err := o.VMSimulateFailure(ctx, nodes[0].ID)
	require.NoError(t, err)

	status, _ := o.VMStatus(ctx, nodes[0].ID)
	assert.Equal(t, "failed", status.Status)

	err = o.VMSimulateFailure(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestInfraOrchestrator_VMSimulatePartition(t *testing.T) {
	cfg := DefaultConfig()
	o := NewOrchestrator(cfg)
	ctx := context.Background()

	nodes, _ := o.VMSpawn(ctx, 1)
	err := o.VMSimulatePartition(ctx, nodes[0].ID, 100*time.Millisecond)
	require.NoError(t, err)

	status, _ := o.VMStatus(ctx, nodes[0].ID)
	assert.Equal(t, "partitioned", status.Status)

	// Wait for auto-heal
	time.Sleep(200 * time.Millisecond)
	status, _ = o.VMStatus(ctx, nodes[0].ID)
	assert.Equal(t, "running", status.Status)

	err = o.VMSimulatePartition(ctx, "nonexistent", time.Second)
	assert.Error(t, err)
}
