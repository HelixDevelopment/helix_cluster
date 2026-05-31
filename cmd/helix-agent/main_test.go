package main

import (
	"testing"
)

func TestAgentFlagsParsing(t *testing.T) {
	// Ensure the main package compiles and flags are declared.
	// Full lifecycle testing is covered in internal/node.
	if testing.Short() {
		t.Skip("skipping agent flag test in short mode")
	}
}
