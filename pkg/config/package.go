// Package config provides configuration loading and validation for Helix Cluster OS.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds application configuration.
type Config struct {
	AppName string
	Version string
	Debug   bool
}

// envVarName returns the environment variable name for a given key.
func envVarName(key string) string {
	return "HELIX_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		AppName: getEnv(envVarName("app-name"), "helix-cluster"),
		Version: getEnv(envVarName("version"), "0.1.0"),
		Debug:   getEnvBool(envVarName("debug"), false),
	}
	return cfg, nil
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.AppName) == "" {
		return fmt.Errorf("app name is required")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return defaultValue
		}
		return b
	}
	return defaultValue
}
