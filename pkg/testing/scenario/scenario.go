// Package scenario provides a YAML-driven chaos scenario engine that composes
// named fault phases with blast-radius controls and abort conditions.
package scenario

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// AbortCondition halts scenario execution when a running counter crosses a
// threshold. Field must be "faults_injected"; Op is one of "==", ">", "<";
// Value is the integer threshold.
type AbortCondition struct {
	Field string `yaml:"field"`
	Op    string `yaml:"op"`
	Value int    `yaml:"value"`
}

// Phase describes one logical stage of a scenario. Faults lists the fault
// names to inject; MaxFaults caps how many of those are actually injected
// (blast radius for this phase).
type Phase struct {
	Name       string   `yaml:"name"`
	Faults     []string `yaml:"faults"`
	DurationMS int64    `yaml:"duration_ms"`
	MaxFaults  int      `yaml:"max_faults"`
}

// Scenario is the top-level descriptor parsed from YAML.
type Scenario struct {
	Name        string          `yaml:"name"`
	MaxAffected int             `yaml:"max_affected"`
	Phases      []Phase         `yaml:"phases"`
	AbortOn     *AbortCondition `yaml:"abort_on,omitempty"`
}

// ParseScenarioYAML deserialises a YAML document into a Scenario and validates
// the required fields.
func ParseScenarioYAML(data []byte) (*Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("scenario: yaml unmarshal: %w", err)
	}
	if s.Name == "" {
		return nil, errors.New("scenario: name must not be empty")
	}
	if len(s.Phases) == 0 {
		return nil, errors.New("scenario: at least one phase is required")
	}
	if s.MaxAffected <= 0 {
		return nil, errors.New("scenario: max_affected must be > 0")
	}
	return &s, nil
}
