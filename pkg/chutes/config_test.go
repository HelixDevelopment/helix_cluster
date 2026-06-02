package chutes

import (
	"errors"
	"testing"
)

// validMinerConfig returns a fully valid ChutesMinerConfig that Validate()
// must accept. Tests that need an invalid config mutate a copy of this value.
func validMinerConfig() ChutesMinerConfig {
	return ChutesMinerConfig{
		NodeID:           "miner-001",
		ValidatorHotkey:  "5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty",
		HourlyCostUSD:    0.75,
		GPUShortRef:      "h100",
		GPUCount:         8,
		BittensorColdkey: "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY",
		BittensorHotkey:  "5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty",
		CacheMaxSizeGB:   80,
		NodeSelector:     map[string]string{"region": "us-east"},
		TEEEnabled:       false,
	}
}

// validValidatorConfig returns a fully valid ValidatorConfig that Validate()
// must accept.
func validValidatorConfig() ValidatorConfig {
	return ValidatorConfig{
		Hotkey:   "5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty",
		Endpoint: "https://validator.chutes.example:8080",
	}
}

// TestChutesMinerConfigValidate_AcceptsValid proves a fully-valid config
// returns nil (CLAUDE-1: must exercise the happy path).
func TestChutesMinerConfigValidate_AcceptsValid(t *testing.T) {
	if err := validMinerConfig().Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestChutesMinerConfigValidate_AcceptsZeroCost proves that HourlyCostUSD == 0
// is accepted (the boundary case: "free" nodes are valid).
func TestChutesMinerConfigValidate_AcceptsZeroCost(t *testing.T) {
	cfg := validMinerConfig()
	cfg.HourlyCostUSD = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("zero cost must be accepted, got %v", err)
	}
}

// TestChutesMinerConfigValidate_AcceptsNilNodeSelector proves that a nil
// NodeSelector does not cause a validation error (it is optional).
func TestChutesMinerConfigValidate_AcceptsNilNodeSelector(t *testing.T) {
	cfg := validMinerConfig()
	cfg.NodeSelector = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("nil NodeSelector must be accepted, got %v", err)
	}
}

// TestChutesMinerConfigValidate_AcceptsEmptyNodeSelector proves that an empty
// (but non-nil) NodeSelector does not cause a validation error.
func TestChutesMinerConfigValidate_AcceptsEmptyNodeSelector(t *testing.T) {
	cfg := validMinerConfig()
	cfg.NodeSelector = map[string]string{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty NodeSelector must be accepted, got %v", err)
	}
}

// TestChutesMinerConfigValidate_AcceptsTEEEnabled proves that TEEEnabled=true
// alone does not cause a validation error.
func TestChutesMinerConfigValidate_AcceptsTEEEnabled(t *testing.T) {
	cfg := validMinerConfig()
	cfg.TEEEnabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("TEEEnabled=true on an otherwise valid config must be accepted, got %v", err)
	}
}

// TestChutesMinerConfigValidate_InvalidFields is a table-driven test that
// proves every individually-invalid field produces a non-nil error that wraps
// (errors.Is) the appropriate sentinel. Removing any single branch in
// ChutesMinerConfig.Validate() will make the corresponding sub-test fail —
// that is the load-bearing mutation guard.
func TestChutesMinerConfigValidate_InvalidFields(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*ChutesMinerConfig)
		wantErr   error
	}{
		{
			name:    "empty NodeID",
			mutate:  func(c *ChutesMinerConfig) { c.NodeID = "" },
			wantErr: ErrConfigNodeID,
		},
		{
			name:    "empty ValidatorHotkey",
			mutate:  func(c *ChutesMinerConfig) { c.ValidatorHotkey = "" },
			wantErr: ErrConfigValidatorHotkey,
		},
		{
			name:    "negative HourlyCostUSD",
			mutate:  func(c *ChutesMinerConfig) { c.HourlyCostUSD = -0.01 },
			wantErr: ErrConfigHourlyCost,
		},
		{
			name:    "empty GPUShortRef",
			mutate:  func(c *ChutesMinerConfig) { c.GPUShortRef = "" },
			wantErr: ErrConfigGPUShortRef,
		},
		{
			name:    "GPUCount zero",
			mutate:  func(c *ChutesMinerConfig) { c.GPUCount = 0 },
			wantErr: ErrConfigGPUCount,
		},
		{
			name:    "GPUCount negative",
			mutate:  func(c *ChutesMinerConfig) { c.GPUCount = -1 },
			wantErr: ErrConfigGPUCount,
		},
		{
			name:    "empty BittensorColdkey",
			mutate:  func(c *ChutesMinerConfig) { c.BittensorColdkey = "" },
			wantErr: ErrConfigBittensorColdkey,
		},
		{
			name:    "empty BittensorHotkey",
			mutate:  func(c *ChutesMinerConfig) { c.BittensorHotkey = "" },
			wantErr: ErrConfigBittensorHotkey,
		},
		{
			name:    "CacheMaxSizeGB zero",
			mutate:  func(c *ChutesMinerConfig) { c.CacheMaxSizeGB = 0 },
			wantErr: ErrConfigCacheMaxSizeGB,
		},
		{
			name:    "CacheMaxSizeGB negative",
			mutate:  func(c *ChutesMinerConfig) { c.CacheMaxSizeGB = -1 },
			wantErr: ErrConfigCacheMaxSizeGB,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validMinerConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected non-nil error for %q, got nil", tc.name)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected errors.Is(err, %v) == true, got err = %v", tc.wantErr, err)
			}
		})
	}
}

// TestValidatorConfigValidate_AcceptsValid proves a fully-valid ValidatorConfig
// returns nil (CLAUDE-1: must exercise the happy path).
func TestValidatorConfigValidate_AcceptsValid(t *testing.T) {
	if err := validValidatorConfig().Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestValidatorConfigValidate_InvalidFields is a table-driven test that proves
// every individually-invalid field produces a non-nil error wrapping the
// appropriate sentinel. Removing any single branch in ValidatorConfig.Validate()
// makes the corresponding sub-test fail — that is the load-bearing mutation guard.
func TestValidatorConfigValidate_InvalidFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ValidatorConfig)
		wantErr error
	}{
		{
			name:    "empty Hotkey",
			mutate:  func(v *ValidatorConfig) { v.Hotkey = "" },
			wantErr: ErrConfigValidatorHotkeyField,
		},
		{
			name:    "empty Endpoint",
			mutate:  func(v *ValidatorConfig) { v.Endpoint = "" },
			wantErr: ErrConfigValidatorEndpoint,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validValidatorConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected non-nil error for %q, got nil", tc.name)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected errors.Is(err, %v) == true, got err = %v", tc.wantErr, err)
			}
		})
	}
}

// TestChutesMinerConfigValidate_SentinelOrdering proves that Validate() checks
// NodeID before other fields (order matters for caller UX — the first error
// names the first offending field, so NodeID is checked first).
//
// Mutation guard: inverting the NodeID check before the GPUCount check will
// make this test observe ErrConfigGPUCount instead of ErrConfigNodeID and fail.
func TestChutesMinerConfigValidate_SentinelOrdering(t *testing.T) {
	cfg := validMinerConfig()
	cfg.NodeID = ""     // violates NodeID rule
	cfg.GPUCount = -99  // would also violate GPUCount rule
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	// NodeID check must fire before GPUCount check.
	if !errors.Is(err, ErrConfigNodeID) {
		t.Fatalf("expected ErrConfigNodeID first, got %v", err)
	}
}
