// Package config provides configuration loading and validation for Helix Cluster OS.
package config

import "fmt"

// Config holds application configuration.
type Config struct {
	AppName string
	Version string
	Debug   bool
}

// Load loads configuration from environment or defaults.
func Load() (*Config, error) {
	return &Config{
		AppName: "helix-cluster",
		Version: "0.1.0",
		Debug:   false,
	}, nil
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if c.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	return nil
}
