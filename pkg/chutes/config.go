package chutes

import (
	"errors"
	"fmt"
)

// Config validation sentinel errors. Callers can branch on them with errors.Is.
var (
	// ErrConfigNodeID is returned when a ChutesMinerConfig has an empty NodeID.
	ErrConfigNodeID = errors.New("chutes: config: NodeID must not be empty")
	// ErrConfigGPUCount is returned when GPUCount is <= 0.
	ErrConfigGPUCount = errors.New("chutes: config: GPUCount must be > 0")
	// ErrConfigHourlyCost is returned when HourlyCostUSD is negative.
	ErrConfigHourlyCost = errors.New("chutes: config: HourlyCostUSD must be >= 0")
	// ErrConfigCacheMaxSizeGB is returned when CacheMaxSizeGB is <= 0.
	ErrConfigCacheMaxSizeGB = errors.New("chutes: config: CacheMaxSizeGB must be > 0")
	// ErrConfigBittensorColdkey is returned when BittensorColdkey is empty.
	ErrConfigBittensorColdkey = errors.New("chutes: config: BittensorColdkey must not be empty")
	// ErrConfigBittensorHotkey is returned when BittensorHotkey is empty.
	ErrConfigBittensorHotkey = errors.New("chutes: config: BittensorHotkey must not be empty")
	// ErrConfigValidatorHotkey is returned when ValidatorHotkey is empty.
	ErrConfigValidatorHotkey = errors.New("chutes: config: ValidatorHotkey must not be empty")
	// ErrConfigGPUShortRef is returned when GPUShortRef is empty.
	ErrConfigGPUShortRef = errors.New("chutes: config: GPUShortRef must not be empty")
	// ErrConfigValidatorHotkeyField is returned when ValidatorConfig.Hotkey is empty.
	ErrConfigValidatorHotkeyField = errors.New("chutes: config: validator Hotkey must not be empty")
	// ErrConfigValidatorEndpoint is returned when ValidatorConfig.Endpoint is empty.
	ErrConfigValidatorEndpoint = errors.New("chutes: config: validator Endpoint must not be empty")
)

// ChutesMinerConfig holds the runtime configuration for a Chutes miner node.
// A miner registers GPU capacity to the Bittensor subnet and serves inference
// requests routed by a validator. TEEEnabled signals that the node runs inside
// a Trusted Execution Environment; when true, the attestation path expects a
// real TEE quote rather than a plain HMAC proof.
type ChutesMinerConfig struct {
	// NodeID is the stable cluster-internal identifier for this miner node.
	// It is embedded in challenge/response proofs so must be non-empty and
	// unique within the subnet.
	NodeID string

	// ValidatorHotkey is the SS58-encoded Bittensor hotkey of the validator
	// this miner is registered to. Used to verify signed routing decisions.
	ValidatorHotkey string

	// HourlyCostUSD is the operator's declared cost-per-hour in USD for this
	// node. It MUST be non-negative; zero means "free" (e.g. internal test
	// nodes). Negative values are nonsensical and are rejected.
	HourlyCostUSD float64

	// GPUShortRef is a short hardware identifier such as "h100", "a100", or
	// "rtx4090". It appears in routing tables so must be non-empty.
	GPUShortRef string

	// GPUCount is the number of physical GPUs available on this node. At least
	// one GPU is required for the node to participate in inference routing.
	GPUCount int

	// BittensorColdkey is the SS58-encoded Bittensor coldkey (wallet) owning
	// this miner registration. Required for on-chain accounting.
	BittensorColdkey string

	// BittensorHotkey is the SS58-encoded Bittensor hotkey signing on-chain
	// operations for this miner. Must be distinct from and linked to the coldkey.
	BittensorHotkey string

	// CacheMaxSizeGB is the maximum GPU-memory cache size in GiB. Must be > 0;
	// a zero value would prevent caching altogether and break inference.
	CacheMaxSizeGB int

	// NodeSelector is an arbitrary key/value label map applied to inference
	// scheduling decisions (e.g. {"region": "eu-west", "tier": "premium"}).
	// May be nil/empty — no routing constraints is a valid configuration.
	NodeSelector map[string]string

	// TEEEnabled signals that this node runs inside a Trusted Execution
	// Environment. When true the proof-of-GPU path expects a real hardware
	// TEE attestation quote; when false an HMAC-based proof is accepted.
	TEEEnabled bool
}

// Validate enforces all non-negotiable invariants on a ChutesMinerConfig.
// It returns the first sentinel error whose name identifies the offending field,
// or nil when every invariant passes. The caller can branch via errors.Is.
//
// Invariants enforced:
//   - NodeID non-empty
//   - ValidatorHotkey non-empty
//   - HourlyCostUSD >= 0
//   - GPUShortRef non-empty
//   - GPUCount > 0
//   - BittensorColdkey non-empty
//   - BittensorHotkey non-empty
//   - CacheMaxSizeGB > 0
func (c ChutesMinerConfig) Validate() error {
	if c.NodeID == "" {
		return ErrConfigNodeID
	}
	if c.ValidatorHotkey == "" {
		return ErrConfigValidatorHotkey
	}
	if c.HourlyCostUSD < 0 {
		return fmt.Errorf("%w: got %.6f", ErrConfigHourlyCost, c.HourlyCostUSD)
	}
	if c.GPUShortRef == "" {
		return ErrConfigGPUShortRef
	}
	if c.GPUCount <= 0 {
		return fmt.Errorf("%w: got %d", ErrConfigGPUCount, c.GPUCount)
	}
	if c.BittensorColdkey == "" {
		return ErrConfigBittensorColdkey
	}
	if c.BittensorHotkey == "" {
		return ErrConfigBittensorHotkey
	}
	if c.CacheMaxSizeGB <= 0 {
		return fmt.Errorf("%w: got %d", ErrConfigCacheMaxSizeGB, c.CacheMaxSizeGB)
	}
	return nil
}

// ValidatorConfig holds the registration descriptor for a Chutes subnet
// validator. A validator is identified by its Bittensor hotkey and exposes an
// inference-routing endpoint to miners registered against it.
type ValidatorConfig struct {
	// Hotkey is the SS58-encoded Bittensor hotkey identifying this validator.
	// It is used by miners to verify signed routing decisions and must be
	// non-empty.
	Hotkey string

	// Endpoint is the URL (or host:port) of the validator's inference-routing
	// service. Miners connect to this address to receive work assignments and
	// to submit inference results. Must be non-empty.
	Endpoint string
}

// Validate enforces invariants on a ValidatorConfig. It returns a sentinel
// error naming the first offending field, or nil when every invariant passes.
//
// Invariants enforced:
//   - Hotkey non-empty
//   - Endpoint non-empty
func (v ValidatorConfig) Validate() error {
	if v.Hotkey == "" {
		return ErrConfigValidatorHotkeyField
	}
	if v.Endpoint == "" {
		return ErrConfigValidatorEndpoint
	}
	return nil
}
