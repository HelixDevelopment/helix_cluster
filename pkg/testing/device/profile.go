// Package device provides device simulation profiles for virtual cluster testing.
package device

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Profile defines the hardware specification of a simulated device.
type Profile struct {
	ID      string  `yaml:"id"`
	Tier    string  `yaml:"tier"`
	CPU     CPU     `yaml:"cpu"`
	RAM     RAM     `yaml:"ram"`
	GPU     GPU     `yaml:"gpu"`
	Network Network `yaml:"network"`
}

// CPU holds CPU specifications.
type CPU struct {
	Cores        int     `yaml:"cores"`
	FrequencyGHz float64 `yaml:"frequency_ghz"`
	Architecture string  `yaml:"architecture"`
}

// RAM holds memory specifications.
type RAM struct {
	TotalGB int    `yaml:"total_gb"`
	Type    string `yaml:"type"`
}

// GPU holds GPU specifications.
type GPU struct {
	Count    int    `yaml:"count"`
	Model    string `yaml:"model"`
	MemoryGB int    `yaml:"memory_gb"`
	Vulkan   bool   `yaml:"vulkan"`
}

// Network holds network specifications.
type Network struct {
	BandwidthMbps int `yaml:"bandwidth_mbps"`
	LatencyMs     int `yaml:"latency_ms"`
	JitterMs      int `yaml:"jitter_ms"`
}

// Registry holds a collection of device profiles keyed by tier.
type Registry struct {
	profiles map[string]*Profile
}

// NewRegistry creates a registry pre-populated with the default T1-T8 tiers.
func NewRegistry() *Registry {
	r := &Registry{profiles: make(map[string]*Profile)}
	r.seedDefaults()
	return r
}

// Register adds or overwrites a profile.
func (reg *Registry) Register(p *Profile) {
	reg.profiles[p.Tier] = p
}

// Get returns a profile by tier, or nil if not found.
func (reg *Registry) Get(tier string) *Profile {
	return reg.profiles[tier]
}

// List returns all registered profiles sorted by tier.
func (reg *Registry) List() []*Profile {
	keys := make([]string, 0, len(reg.profiles))
	for k := range reg.profiles {
		keys = append(keys, k)
	}
	// Natural sort for T1..T8.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	out := make([]*Profile, len(keys))
	for i, k := range keys {
		out[i] = reg.profiles[k]
	}
	return out
}

// SaveYAML writes all profiles to a YAML file.
func (reg *Registry) SaveYAML(path string) error {
	data, err := yaml.Marshal(reg.profiles)
	if err != nil {
		return fmt.Errorf("marshal profiles: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// LoadYAML reads profiles from a YAML file and merges them into the registry.
func (reg *Registry) LoadYAML(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	var loaded map[string]*Profile
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("unmarshal profiles: %w", err)
	}
	for _, p := range loaded {
		reg.Register(p)
	}
	return nil
}

func (reg *Registry) seedDefaults() {
	defaults := []*Profile{
		{
			ID: "t1-micro", Tier: "T1",
			CPU:     CPU{Cores: 1, FrequencyGHz: 1.0, Architecture: "arm64"},
			RAM:     RAM{TotalGB: 1, Type: "LPDDR4"},
			GPU:     GPU{Count: 0, Model: "none", MemoryGB: 0, Vulkan: false},
			Network: Network{BandwidthMbps: 100, LatencyMs: 20, JitterMs: 5},
		},
		{
			ID: "t2-small", Tier: "T2",
			CPU:     CPU{Cores: 2, FrequencyGHz: 1.5, Architecture: "arm64"},
			RAM:     RAM{TotalGB: 2, Type: "LPDDR4"},
			GPU:     GPU{Count: 0, Model: "none", MemoryGB: 0, Vulkan: false},
			Network: Network{BandwidthMbps: 250, LatencyMs: 15, JitterMs: 3},
		},
		{
			ID: "t3-medium", Tier: "T3",
			CPU:     CPU{Cores: 4, FrequencyGHz: 2.0, Architecture: "x86_64"},
			RAM:     RAM{TotalGB: 4, Type: "DDR4"},
			GPU:     GPU{Count: 0, Model: "none", MemoryGB: 0, Vulkan: false},
			Network: Network{BandwidthMbps: 500, LatencyMs: 10, JitterMs: 2},
		},
		{
			ID: "t4-large", Tier: "T4",
			CPU:     CPU{Cores: 8, FrequencyGHz: 2.5, Architecture: "x86_64"},
			RAM:     RAM{TotalGB: 16, Type: "DDR4"},
			GPU:     GPU{Count: 1, Model: "integrated", MemoryGB: 2, Vulkan: true},
			Network: Network{BandwidthMbps: 1000, LatencyMs: 5, JitterMs: 1},
		},
		{
			ID: "t5-xlarge", Tier: "T5",
			CPU:     CPU{Cores: 16, FrequencyGHz: 3.0, Architecture: "x86_64"},
			RAM:     RAM{TotalGB: 32, Type: "DDR5"},
			GPU:     GPU{Count: 1, Model: "dedicated", MemoryGB: 8, Vulkan: true},
			Network: Network{BandwidthMbps: 2500, LatencyMs: 3, JitterMs: 1},
		},
		{
			ID: "t6-2xlarge", Tier: "T6",
			CPU:     CPU{Cores: 32, FrequencyGHz: 3.5, Architecture: "x86_64"},
			RAM:     RAM{TotalGB: 64, Type: "DDR5"},
			GPU:     GPU{Count: 2, Model: "dedicated", MemoryGB: 16, Vulkan: true},
			Network: Network{BandwidthMbps: 5000, LatencyMs: 2, JitterMs: 0},
		},
		{
			ID: "t7-4xlarge", Tier: "T7",
			CPU:     CPU{Cores: 64, FrequencyGHz: 3.5, Architecture: "x86_64"},
			RAM:     RAM{TotalGB: 128, Type: "DDR5"},
			GPU:     GPU{Count: 4, Model: "dedicated", MemoryGB: 32, Vulkan: true},
			Network: Network{BandwidthMbps: 10000, LatencyMs: 1, JitterMs: 0},
		},
		{
			ID: "t8-8xlarge", Tier: "T8",
			CPU:     CPU{Cores: 128, FrequencyGHz: 4.0, Architecture: "x86_64"},
			RAM:     RAM{TotalGB: 512, Type: "DDR5"},
			GPU:     GPU{Count: 8, Model: "dedicated", MemoryGB: 80, Vulkan: true},
			Network: Network{BandwidthMbps: 25000, LatencyMs: 1, JitterMs: 0},
		},
	}
	for _, p := range defaults {
		reg.Register(p)
	}
}
