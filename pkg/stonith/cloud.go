package stonith

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// --- AWS EC2 ---------------------------------------------------------------

// EC2Client is the minimal slice of an EC2 power API needed to fence an
// instance. The real implementation would wrap the AWS SDK; keeping it behind
// this interface means the agent is testable without AWS credentials.
type EC2Client interface {
	// StopInstance forces the instance into the stopped state.
	StopInstance(ctx context.Context, instanceID string) error
	// InstanceState returns a lowercase EC2 lifecycle state, e.g. "running",
	// "stopping", "stopped".
	InstanceState(ctx context.Context, instanceID string) (string, error)
}

// EC2Agent fences a node by stopping its backing EC2 instance. The target
// passed to Fence is the EC2 instance ID.
type EC2Agent struct {
	name   string
	client EC2Client
}

// NewEC2Agent builds an EC2Agent over the given (injectable) client.
func NewEC2Agent(name string, client EC2Client) *EC2Agent {
	return &EC2Agent{name: name, client: client}
}

// Name returns the agent's name.
func (a *EC2Agent) Name() string { return a.name }

// Fence stops the instance and confirms it reached the "stopped" state.
func (a *EC2Agent) Fence(ctx context.Context, target string) (*Confirmation, error) {
	if target == "" {
		return nil, ErrEmptyTarget
	}
	if err := a.client.StopInstance(ctx, target); err != nil {
		return nil, fmt.Errorf("ec2 stop %q: %w", target, err)
	}
	fenced, err := a.IsFenced(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("ec2 confirm %q: %w", target, err)
	}
	if !fenced {
		return nil, fmt.Errorf("ec2 %q not stopped: %w", target, ErrNotConfirmed)
	}
	return &Confirmation{
		Target: target,
		Agent:  a.name,
		Method: "ec2 stop",
		At:     time.Now(),
	}, nil
}

// IsFenced reports true when the instance state is "stopped".
func (a *EC2Agent) IsFenced(ctx context.Context, target string) (bool, error) {
	st, err := a.client.InstanceState(ctx, target)
	if err != nil {
		return false, fmt.Errorf("ec2 state %q: %w", target, err)
	}
	return strings.EqualFold(st, "stopped"), nil
}

// --- Azure ARM -------------------------------------------------------------

// AzureClient is the minimal slice of the Azure Resource Manager VM API needed
// to fence a VM. The real implementation would wrap the Azure SDK; the
// interface keeps the agent testable without ARM credentials.
type AzureClient interface {
	// PowerOffVM forces the VM into the stopped/deallocated state.
	PowerOffVM(ctx context.Context, resourceGroup, vmName string) error
	// VMPowerState returns a lowercase power state, e.g. "running",
	// "stopped", "deallocated".
	VMPowerState(ctx context.Context, resourceGroup, vmName string) (string, error)
}

// AzureAgent fences a node by powering off its backing Azure VM. The target
// passed to Fence is the VM name within the agent's configured resource group.
type AzureAgent struct {
	name          string
	resourceGroup string
	client        AzureClient
}

// NewAzureAgent builds an AzureAgent for VMs in resourceGroup over the given
// (injectable) client.
func NewAzureAgent(name, resourceGroup string, client AzureClient) *AzureAgent {
	return &AzureAgent{name: name, resourceGroup: resourceGroup, client: client}
}

// Name returns the agent's name.
func (a *AzureAgent) Name() string { return a.name }

// Fence powers off the VM and confirms it reached a stopped/deallocated state.
func (a *AzureAgent) Fence(ctx context.Context, target string) (*Confirmation, error) {
	if target == "" {
		return nil, ErrEmptyTarget
	}
	if err := a.client.PowerOffVM(ctx, a.resourceGroup, target); err != nil {
		return nil, fmt.Errorf("azure power off %q: %w", target, err)
	}
	fenced, err := a.IsFenced(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("azure confirm %q: %w", target, err)
	}
	if !fenced {
		return nil, fmt.Errorf("azure %q not stopped: %w", target, ErrNotConfirmed)
	}
	return &Confirmation{
		Target: target,
		Agent:  a.name,
		Method: "azure power off",
		At:     time.Now(),
	}, nil
}

// IsFenced reports true when the VM power state is "stopped" or "deallocated".
func (a *AzureAgent) IsFenced(ctx context.Context, target string) (bool, error) {
	st, err := a.client.VMPowerState(ctx, a.resourceGroup, target)
	if err != nil {
		return false, fmt.Errorf("azure state %q: %w", target, err)
	}
	low := strings.ToLower(st)
	return low == "stopped" || low == "deallocated", nil
}
