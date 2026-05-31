package console

import (
	"fmt"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/HelixDevelopment/helix_cluster/pkg/resources"
)

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
}

// NewRegistrar creates a new console node registrar.
func NewRegistrar(registry *discovery.ServiceRegistry) *Registrar {
	return &Registrar{registry: registry}
}

// Register creates a NodeRecord for a console node and registers it
// with the cluster discovery backend. Trust defaults to SEMI.
func (r *Registrar) Register(id string, ct ConsoleType, region string, labels map[string]string, capacity *resources.NodeResources) (*NodeRecord, error) {
	if id == "" {
		return nil, fmt.Errorf("node ID is required")
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
