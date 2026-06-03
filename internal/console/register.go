package console

import (
	"context"
	"fmt"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
)

// ReadyGate gates node registration on local boot readiness. WaitReady MUST
// block until the node is cluster-ready and return nil, or return a non-nil
// error (e.g. ctx.Err()) if readiness is not reached. *BootCoordinator
// implements it.
type ReadyGate interface {
	WaitReady(ctx context.Context) error
}

// NodeRecord represents a console node registered with the cluster.
type NodeRecord struct {
	ID          string
	Type        ConsoleType
	Trust       TrustLevel
	Region      string
	Labels      map[string]string
	Capacity    *resources.NodeResources
	RegisteredAt time.Time
}

// Registrar handles console node registration with the cluster.
type Registrar struct {
	registry *discovery.ServiceRegistry
	gate     ReadyGate
}

// NewRegistrar creates a new console node registrar.
func NewRegistrar(registry *discovery.ServiceRegistry) *Registrar {
	return &Registrar{registry: registry}
}

// WithReadyGate attaches a boot-readiness gate. When set, RegisterCtx blocks on
// gate.WaitReady before publishing the node to discovery, so a node that has not
// reached PhaseClusterReady cannot join the cluster. Returns the receiver for
// chaining. Pass a *BootCoordinator (it implements ReadyGate).
func (r *Registrar) WithReadyGate(gate ReadyGate) *Registrar {
	r.gate = gate
	return r
}

// Register creates a NodeRecord for a console node and registers it with the
// cluster discovery backend. Trust defaults to SEMI. It is a convenience
// wrapper over RegisterCtx with a background context; prefer RegisterCtx when a
// ready gate is attached so registration can be cancelled while it waits.
func (r *Registrar) Register(id string, ct ConsoleType, region string, labels map[string]string, capacity *resources.NodeResources) (*NodeRecord, error) {
	return r.RegisterCtx(context.Background(), id, ct, region, labels, capacity)
}

// RegisterCtx creates a NodeRecord for a console node and registers it with the
// cluster discovery backend. If a ReadyGate is attached (WithReadyGate), it
// first blocks on gate.WaitReady(ctx) and refuses to publish the node to
// discovery unless the gate reports readiness — so a not-yet-ClusterReady node
// cannot join. Trust defaults to SEMI.
func (r *Registrar) RegisterCtx(ctx context.Context, id string, ct ConsoleType, region string, labels map[string]string, capacity *resources.NodeResources) (*NodeRecord, error) {
	if id == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	if r.gate != nil {
		if err := r.gate.WaitReady(ctx); err != nil {
			return nil, fmt.Errorf("boot readiness gate: %w", err)
		}
	}

	record := &NodeRecord{
		ID:           id,
		Type:         ct,
		Trust:        TrustSemi,
		Region:       region,
		Labels:       labels,
		Capacity:     capacity,
		RegisteredAt: time.Now().UTC(),
	}

	if r.registry != nil {
		inst := &discovery.Instance{
			ID:       id,
			Service:  "helix-console-node",
			Address:  "", // populated by caller if known
			Metadata: mergeLabels(labels, ct, record.Trust),
		}
		if err := r.registry.Register(nil, inst); err != nil {
			return nil, fmt.Errorf("discovery register: %w", err)
		}
	}

	return record, nil
}

// SetTrust updates the trust level of a node record after attestation.
func (r *Registrar) SetTrust(record *NodeRecord, tl TrustLevel) error {
	if record == nil {
		return fmt.Errorf("record is nil")
	}
	if !tl.IsValid() {
		return fmt.Errorf("invalid trust level: %s", tl)
	}
	record.Trust = tl
	return nil
}

func mergeLabels(base map[string]string, ct ConsoleType, tl TrustLevel) map[string]string {
	out := make(map[string]string, len(base)+2)
	for k, v := range base {
		out[k] = v
	}
	out["console.type"] = string(ct)
	out["console.trust"] = string(tl)
	return out
}
