// Package federation implements a Karmada PropagationPolicy engine for
// two-level scheduling in Helix Cluster OS.
//
// The model is "global picks cell, local Omega picks node": the global
// federation layer selects which member cluster (cell) a workload is
// propagated to by scoring/filtering candidate cells against constraints
// (data locality, latency, cost, compliance); the local Omega scheduler then
// places the workload on a node inside the chosen cell.
//
// All Karmada custom resources (PropagationPolicy, OverridePolicy and the
// objects they reference) are modeled as plain Go structs and marshaled with
// sigs.k8s.io/yaml so the emitted YAML matches the field names the Karmada API
// server expects (camelCase, via the structs' json tags). This package does
// NOT depend on k8s.io/client-go or apimachinery; the structs are
// self-contained.
package federation

// TypeMeta mirrors the apiVersion/kind header that every Kubernetes-style
// object carries. It is embedded into each CR struct.
type TypeMeta struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

// ObjectMeta is the trimmed metadata block carried by every CR. Only the
// fields the engine actually populates are modeled; this keeps the emitted
// YAML free of empty Kubernetes bookkeeping fields.
type ObjectMeta struct {
	Name        string            `json:"name,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// LabelSelector is the standard Kubernetes label selector (matchLabels only;
// matchExpressions is intentionally omitted as the engine does not emit it).
type LabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// ResourceSelector identifies the workload(s) a PropagationPolicy applies to.
type ResourceSelector struct {
	APIVersion    string         `json:"apiVersion"`
	Kind          string         `json:"kind"`
	Name          string         `json:"name,omitempty"`
	Namespace     string         `json:"namespace,omitempty"`
	LabelSelector *LabelSelector `json:"labelSelector,omitempty"`
}

// ClusterAffinity restricts propagation to an explicit set of clusters and/or
// clusters whose labels match a selector. It maps to
// Placement.ClusterAffinity in Karmada.
type ClusterAffinity struct {
	ClusterNames  []string       `json:"clusterNames,omitempty"`
	ExcludeClusters []string     `json:"exclude,omitempty"`
	LabelSelector *LabelSelector `json:"labelSelector,omitempty"`
}

// SpreadConstraint controls how replicas are distributed across a topology
// dimension (e.g. region/zone/cluster). MaxGroups/MinGroups bound the number
// of distinct groups; SpreadByField names the topology key.
type SpreadConstraint struct {
	SpreadByField string `json:"spreadByField,omitempty"`
	SpreadByLabel string `json:"spreadByLabel,omitempty"`
	MaxGroups     int    `json:"maxGroups,omitempty"`
	MinGroups     int    `json:"minGroups,omitempty"`
}

// ReplicaSchedulingType enumerates how Karmada divides replicas.
type ReplicaSchedulingType string

const (
	// ReplicaSchedulingDuplicated copies the full replica count to every
	// selected cluster.
	ReplicaSchedulingDuplicated ReplicaSchedulingType = "Duplicated"
	// ReplicaSchedulingDivided splits the replica count across selected
	// clusters according to ReplicaDivisionPreference.
	ReplicaSchedulingDivided ReplicaSchedulingType = "Divided"
)

// ReplicaDivisionPreference enumerates how divided replicas are weighted.
type ReplicaDivisionPreference string

const (
	// ReplicaDivisionWeighted divides replicas by static or dynamic weight.
	ReplicaDivisionWeighted ReplicaDivisionPreference = "Weighted"
	// ReplicaDivisionAggregated packs replicas into as few clusters as
	// possible.
	ReplicaDivisionAggregated ReplicaDivisionPreference = "Aggregated"
)

// ReplicaSchedulingStrategy controls replica division across clusters.
type ReplicaSchedulingStrategy struct {
	ReplicaSchedulingType     ReplicaSchedulingType     `json:"replicaSchedulingType,omitempty"`
	ReplicaDivisionPreference ReplicaDivisionPreference `json:"replicaDivisionPreference,omitempty"`
}

// Placement is the scheduling directive of a PropagationPolicy.
type Placement struct {
	ClusterAffinity   *ClusterAffinity           `json:"clusterAffinity,omitempty"`
	SpreadConstraints []SpreadConstraint         `json:"spreadConstraints,omitempty"`
	ReplicaScheduling *ReplicaSchedulingStrategy `json:"replicaScheduling,omitempty"`
}

// PropagationSpec is the spec block of a PropagationPolicy.
type PropagationSpec struct {
	ResourceSelectors []ResourceSelector `json:"resourceSelectors"`
	Placement         *Placement         `json:"placement,omitempty"`
}

// PropagationPolicy is the top-level Karmada CR that tells the control plane
// which workloads to propagate to which clusters and how.
type PropagationPolicy struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta      `json:"metadata"`
	Spec     PropagationSpec `json:"spec"`
}

// PlaintextOverrider is one of the override operators Karmada supports. Only
// the plaintext form (a JSON-patch-like op/path/value triple) is modeled, as
// it is sufficient for per-cluster image/env/config overrides.
type PlaintextOverrider struct {
	Path     string      `json:"path"`
	Operator string      `json:"operator"`
	Value    interface{} `json:"value,omitempty"`
}

// Overriders groups the override operators applied to a target.
type Overriders struct {
	Plaintext []PlaintextOverrider `json:"plaintext,omitempty"`
}

// RuleWithCluster ties a set of overriders to the clusters they apply to.
type RuleWithCluster struct {
	TargetCluster ClusterAffinity `json:"targetCluster"`
	Overriders    Overriders      `json:"overriders"`
}

// OverrideSpec is the spec block of an OverridePolicy.
type OverrideSpec struct {
	ResourceSelectors []ResourceSelector `json:"resourceSelectors,omitempty"`
	OverrideRules     []RuleWithCluster  `json:"overrideRules"`
}

// OverridePolicy is the Karmada CR that applies per-cluster differences (image
// registries, env vars, replica counts) to a propagated workload.
type OverridePolicy struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta   `json:"metadata"`
	Spec     OverrideSpec `json:"spec"`
}
