package policy

import (
	"testing"
)

func TestEngineLoadAndEvaluate(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy("test", "package test\nallow { true }"); err != nil {
		t.Fatal(err)
	}

	allowed, decisions, err := e.Evaluate("test", map[string]interface{}{"allowed": true})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("expected allowed")
	}
	if decisions["policy"] != "test" {
		t.Errorf("policy = %v, want test", decisions["policy"])
	}
}

func TestEngineEvaluateDenied(t *testing.T) {
	e := NewEngine()
	_ = e.LoadPolicy("test", "package test")

	allowed, _, err := e.Evaluate("test", map[string]interface{}{"allowed": false})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("expected denied")
	}
}

func TestEngineMissingPolicy(t *testing.T) {
	e := NewEngine()
	_, _, err := e.Evaluate("missing", nil)
	if err == nil {
		t.Error("expected error for missing policy")
	}
}

func TestEngineListPolicies(t *testing.T) {
	e := NewEngine()
	_ = e.LoadPolicy("p1", "")
	_ = e.LoadPolicy("p2", "")

	policies := e.ListPolicies()
	if len(policies) != 2 {
		t.Errorf("policies = %d, want 2", len(policies))
	}
}
