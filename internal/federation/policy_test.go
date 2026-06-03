package federation

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestNewPropagationPolicy_BuildsValidCR(t *testing.T) {
	pp, err := NewPropagationPolicy(PropagationPolicyOptions{
		Name:      "web-prop",
		Namespace: "apps",
		Labels:    map[string]string{"app": "web"},
		ResourceSelectors: []ResourceSelector{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"},
		},
		ClusterNames: []string{"cell-eu-west", "cell-eu-central", "cell-us-east"},
		SpreadConstraints: []SpreadConstraint{
			{SpreadByField: "cluster", MaxGroups: 2, MinGroups: 1},
		},
		ReplicaScheduling: &ReplicaSchedulingStrategy{
			ReplicaSchedulingType:     ReplicaSchedulingDivided,
			ReplicaDivisionPreference: ReplicaDivisionWeighted,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pp.APIVersion != "policy.karmada.io/v1alpha1" || pp.Kind != "PropagationPolicy" {
		t.Fatalf("wrong type meta: %+v", pp.TypeMeta)
	}
	if pp.Metadata.Namespace != "apps" {
		t.Fatalf("namespace = %q want apps", pp.Metadata.Namespace)
	}
	if pp.Spec.Placement == nil || pp.Spec.Placement.ClusterAffinity == nil {
		t.Fatalf("placement/clusterAffinity not built: %+v", pp.Spec.Placement)
	}
	if got := pp.Spec.Placement.ClusterAffinity.ClusterNames; len(got) != 3 {
		t.Fatalf("clusterNames = %v want 3", got)
	}
	if len(pp.Spec.Placement.SpreadConstraints) != 1 {
		t.Fatalf("spread constraints not carried: %+v", pp.Spec.Placement.SpreadConstraints)
	}
}

func TestNewPropagationPolicy_DefaultsNamespace(t *testing.T) {
	pp, err := NewPropagationPolicy(PropagationPolicyOptions{
		Name:              "n",
		ResourceSelectors: []ResourceSelector{{APIVersion: "apps/v1", Kind: "Deployment", Name: "n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pp.Metadata.Namespace != "default" {
		t.Fatalf("namespace = %q want default", pp.Metadata.Namespace)
	}
	if pp.Spec.Placement != nil {
		t.Fatalf("placement should be nil when no affinity/spread given: %+v", pp.Spec.Placement)
	}
}

func TestNewPropagationPolicy_Validation(t *testing.T) {
	if _, err := NewPropagationPolicy(PropagationPolicyOptions{}); err == nil {
		t.Fatal("expected error for missing name")
	}
	if _, err := NewPropagationPolicy(PropagationPolicyOptions{Name: "x"}); err == nil {
		t.Fatal("expected error for missing resource selectors")
	}
	if _, err := NewPropagationPolicy(PropagationPolicyOptions{
		Name:              "x",
		ResourceSelectors: []ResourceSelector{{Kind: "Deployment"}}, // missing apiVersion
	}); err == nil {
		t.Fatal("expected error for selector missing apiVersion")
	}
}

func TestPropagationPolicy_YAMLIsKarmadaShapedAndRoundTrips(t *testing.T) {
	pp, err := NewPropagationPolicy(PropagationPolicyOptions{
		Name:              "web-prop",
		Namespace:         "apps",
		ResourceSelectors: []ResourceSelector{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}},
		ClusterNames:      []string{"cell-a", "cell-b"},
		SpreadConstraints: []SpreadConstraint{{SpreadByField: "cluster", MaxGroups: 2, MinGroups: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := pp.MarshalYAML()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)

	// Assert the emitted YAML uses the camelCase keys the Karmada API expects.
	for _, want := range []string{
		"apiVersion: policy.karmada.io/v1alpha1",
		"kind: PropagationPolicy",
		"resourceSelectors:",
		"clusterAffinity:",
		"clusterNames:",
		"spreadConstraints:",
		"spreadByField: cluster",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("emitted YAML missing %q:\n%s", want, got)
		}
	}
	// Must NOT leak Go-style field names.
	for _, bad := range []string{"ClusterNames", "ResourceSelectors", "SpreadByField"} {
		if strings.Contains(got, bad) {
			t.Fatalf("emitted YAML leaked Go field name %q:\n%s", bad, got)
		}
	}

	// Round-trip: parse back and confirm equality of the meaningful fields.
	back, err := ParsePropagationPolicy(data)
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if back.Metadata.Name != pp.Metadata.Name ||
		back.Spec.Placement.ClusterAffinity.ClusterNames[1] != "cell-b" ||
		back.Spec.ResourceSelectors[0].Kind != "Deployment" {
		t.Fatalf("round-trip mismatch:\norig=%+v\nback=%+v", pp, back)
	}
}

func TestNewOverridePolicy_BuildsAndRoundTrips(t *testing.T) {
	op, err := NewOverridePolicy(OverridePolicyOptions{
		Name:              "web-override",
		Namespace:         "apps",
		ResourceSelectors: []ResourceSelector{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}},
		Rules: []RuleWithCluster{
			{
				TargetCluster: ClusterAffinity{ClusterNames: []string{"cell-us-east"}},
				Overriders: Overriders{Plaintext: []PlaintextOverrider{
					{Path: "/spec/template/spec/containers/0/image", Operator: "replace", Value: "us-registry/web:1.2.3"},
				}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := op.MarshalYAML()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"kind: OverridePolicy",
		"overrideRules:",
		"targetCluster:",
		"plaintext:",
		"operator: replace",
		"us-registry/web:1.2.3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("override YAML missing %q:\n%s", want, got)
		}
	}
	back, err := ParseOverridePolicy(data)
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if back.Spec.OverrideRules[0].Overriders.Plaintext[0].Value != "us-registry/web:1.2.3" {
		t.Fatalf("override value lost in round-trip: %+v", back.Spec.OverrideRules[0])
	}
}

func TestNewOverridePolicy_Validation(t *testing.T) {
	if _, err := NewOverridePolicy(OverridePolicyOptions{Name: ""}); err == nil {
		t.Fatal("expected error for missing name")
	}
	if _, err := NewOverridePolicy(OverridePolicyOptions{Name: "x"}); err == nil {
		t.Fatal("expected error for no rules")
	}
	// Rule with no target cluster.
	if _, err := NewOverridePolicy(OverridePolicyOptions{
		Name:  "x",
		Rules: []RuleWithCluster{{Overriders: Overriders{Plaintext: []PlaintextOverrider{{Path: "/p", Operator: "replace"}}}}},
	}); err == nil {
		t.Fatal("expected error for rule without target cluster")
	}
	// Rule with no overriders.
	if _, err := NewOverridePolicy(OverridePolicyOptions{
		Name:  "x",
		Rules: []RuleWithCluster{{TargetCluster: ClusterAffinity{ClusterNames: []string{"c"}}}},
	}); err == nil {
		t.Fatal("expected error for rule without overriders")
	}
}

// TestYAMLValidUnstructured proves the emitted document is well-formed YAML
// that an unstructured client (kubectl/Karmada) can decode without our structs.
func TestYAMLValidUnstructured(t *testing.T) {
	pp, _ := NewPropagationPolicy(PropagationPolicyOptions{
		Name:              "n",
		ResourceSelectors: []ResourceSelector{{APIVersion: "apps/v1", Kind: "Deployment", Name: "n"}},
		ClusterNames:      []string{"c1"},
	})
	data, _ := pp.MarshalYAML()
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("emitted YAML is not valid: %v", err)
	}
	if m["kind"] != "PropagationPolicy" {
		t.Fatalf("unstructured kind = %v", m["kind"])
	}
}
