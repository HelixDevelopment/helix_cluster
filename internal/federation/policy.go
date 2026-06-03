package federation

import "sigs.k8s.io/yaml"

// Karmada API group/version and kinds used by the builders.
const (
	policyAPIVersion         = "policy.karmada.io/v1alpha1"
	kindPropagationPolicy    = "PropagationPolicy"
	kindOverridePolicy       = "OverridePolicy"
	defaultPolicyNamespace   = "default"
)

// PropagationPolicyOptions configures NewPropagationPolicy.
type PropagationPolicyOptions struct {
	// Name is the metadata.name of the policy (required).
	Name string
	// Namespace is the metadata.namespace; defaults to "default" when empty.
	Namespace string
	// Labels/Annotations are copied onto metadata.
	Labels      map[string]string
	Annotations map[string]string
	// ResourceSelectors selects the workloads to propagate (required, >=1).
	ResourceSelectors []ResourceSelector
	// ClusterNames, when non-empty, pins propagation to these cells via
	// placement.clusterAffinity.clusterNames. This is how the engine encodes
	// a global cell-selection decision into a concrete policy.
	ClusterNames []string
	// ClusterLabelSelector, when set, selects cells by label instead of /in
	// addition to ClusterNames.
	ClusterLabelSelector map[string]string
	// SpreadConstraints models how replicas spread across the topology.
	SpreadConstraints []SpreadConstraint
	// ReplicaScheduling, when set, controls replica division.
	ReplicaScheduling *ReplicaSchedulingStrategy
}

// NewPropagationPolicy builds a PropagationPolicy struct from opts. It fills in
// the apiVersion/kind header and a default namespace, and assembles the
// placement block from the supplied affinity/spread inputs. It returns an
// error when the required fields are missing so a malformed policy is never
// emitted.
func NewPropagationPolicy(opts PropagationPolicyOptions) (*PropagationPolicy, error) {
	if opts.Name == "" {
		return nil, &ValidationError{Field: "Name", Msg: "propagation policy name is required"}
	}
	if len(opts.ResourceSelectors) == 0 {
		return nil, &ValidationError{Field: "ResourceSelectors", Msg: "at least one resource selector is required"}
	}
	for i, rs := range opts.ResourceSelectors {
		if rs.APIVersion == "" || rs.Kind == "" {
			return nil, &ValidationError{
				Field: "ResourceSelectors",
				Msg:   "resource selector " + itoa(i) + " requires both apiVersion and kind",
			}
		}
	}

	ns := opts.Namespace
	if ns == "" {
		ns = defaultPolicyNamespace
	}

	pp := &PropagationPolicy{
		TypeMeta: TypeMeta{APIVersion: policyAPIVersion, Kind: kindPropagationPolicy},
		Metadata: ObjectMeta{
			Name:        opts.Name,
			Namespace:   ns,
			Labels:      opts.Labels,
			Annotations: opts.Annotations,
		},
		Spec: PropagationSpec{ResourceSelectors: opts.ResourceSelectors},
	}

	placement := buildPlacement(opts)
	if placement != nil {
		pp.Spec.Placement = placement
	}
	return pp, nil
}

func buildPlacement(opts PropagationPolicyOptions) *Placement {
	var affinity *ClusterAffinity
	if len(opts.ClusterNames) > 0 || len(opts.ClusterLabelSelector) > 0 {
		affinity = &ClusterAffinity{}
		if len(opts.ClusterNames) > 0 {
			affinity.ClusterNames = append([]string(nil), opts.ClusterNames...)
		}
		if len(opts.ClusterLabelSelector) > 0 {
			affinity.LabelSelector = &LabelSelector{MatchLabels: opts.ClusterLabelSelector}
		}
	}

	if affinity == nil && len(opts.SpreadConstraints) == 0 && opts.ReplicaScheduling == nil {
		return nil
	}
	return &Placement{
		ClusterAffinity:   affinity,
		SpreadConstraints: opts.SpreadConstraints,
		ReplicaScheduling: opts.ReplicaScheduling,
	}
}

// OverridePolicyOptions configures NewOverridePolicy.
type OverridePolicyOptions struct {
	Name              string
	Namespace         string
	Labels            map[string]string
	Annotations       map[string]string
	ResourceSelectors []ResourceSelector
	// Rules are the per-cluster override rules (required, >=1).
	Rules []RuleWithCluster
}

// NewOverridePolicy builds an OverridePolicy struct from opts, validating that
// at least one override rule is present and each rule targets at least one
// cluster.
func NewOverridePolicy(opts OverridePolicyOptions) (*OverridePolicy, error) {
	if opts.Name == "" {
		return nil, &ValidationError{Field: "Name", Msg: "override policy name is required"}
	}
	if len(opts.Rules) == 0 {
		return nil, &ValidationError{Field: "Rules", Msg: "at least one override rule is required"}
	}
	for i, r := range opts.Rules {
		if len(r.TargetCluster.ClusterNames) == 0 && r.TargetCluster.LabelSelector == nil {
			return nil, &ValidationError{
				Field: "Rules",
				Msg:   "override rule " + itoa(i) + " must target at least one cluster (clusterNames or labelSelector)",
			}
		}
		if len(r.Overriders.Plaintext) == 0 {
			return nil, &ValidationError{
				Field: "Rules",
				Msg:   "override rule " + itoa(i) + " must declare at least one plaintext overrider",
			}
		}
	}

	ns := opts.Namespace
	if ns == "" {
		ns = defaultPolicyNamespace
	}

	return &OverridePolicy{
		TypeMeta: TypeMeta{APIVersion: policyAPIVersion, Kind: kindOverridePolicy},
		Metadata: ObjectMeta{
			Name:        opts.Name,
			Namespace:   ns,
			Labels:      opts.Labels,
			Annotations: opts.Annotations,
		},
		Spec: OverrideSpec{
			ResourceSelectors: opts.ResourceSelectors,
			OverrideRules:     opts.Rules,
		},
	}, nil
}

// MarshalYAML renders the PropagationPolicy as Karmada-compatible YAML using
// the json struct tags (camelCase), matching what `kubectl apply`/the Karmada
// API server expects.
func (pp *PropagationPolicy) MarshalYAML() ([]byte, error) { return yaml.Marshal(pp) }

// MarshalYAML renders the OverridePolicy as Karmada-compatible YAML.
func (op *OverridePolicy) MarshalYAML() ([]byte, error) { return yaml.Marshal(op) }

// ParsePropagationPolicy parses YAML produced by MarshalYAML back into a
// struct, used to prove the CR round-trips losslessly.
func ParsePropagationPolicy(data []byte) (*PropagationPolicy, error) {
	var pp PropagationPolicy
	if err := yaml.Unmarshal(data, &pp); err != nil {
		return nil, err
	}
	return &pp, nil
}

// ParseOverridePolicy parses YAML produced by MarshalYAML back into a struct.
func ParseOverridePolicy(data []byte) (*OverridePolicy, error) {
	var op OverridePolicy
	if err := yaml.Unmarshal(data, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// ValidationError is returned when builder inputs are invalid.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

// itoa is a tiny dependency-free int->string for small indices in messages.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
