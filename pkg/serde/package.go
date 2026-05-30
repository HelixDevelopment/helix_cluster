// Package serde provides serialization/deserialization utilities for Helix Cluster OS.
package serde

import (
	"encoding/json"
	"fmt"
)

// Marshal marshals v to JSON bytes.
func Marshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal unmarshals JSON bytes into v.
func Unmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// MustMarshal panics if marshaling fails.
func MustMarshal(v interface{}) []byte {
	b, err := Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal failed: %v", err))
	}
	return b
}
