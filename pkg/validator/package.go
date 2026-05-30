// Package validator provides validation utilities for Helix Cluster OS.
package validator

import (
	"fmt"
	"regexp"
)

// Validator holds validation rules.
type Validator struct{}

// New creates a new Validator.
func New() *Validator {
	return &Validator{}
}

// IsValidID checks if id matches a simple alphanumeric pattern.
func (v *Validator) IsValidID(id string) bool {
	matched, _ := regexp.MatchString(`^[a-zA-Z0-9_-]+$`, id)
	return matched
}

// NotEmpty returns an error if s is empty.
func (v *Validator) NotEmpty(s string) error {
	if s == "" {
		return fmt.Errorf("value must not be empty")
	}
	return nil
}
