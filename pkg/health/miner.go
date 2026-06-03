package health

// Miner / GraVal health checks.
//
// This file adds two Chutes-specific dependency checks on top of the existing
// DependencyRollup machinery (see rollup.go):
//
//   - RegisterMinerAPI: an HTTP liveness/readiness probe against the Chutes
//     miner-api Deployment endpoint. When the miner-api is up it returns 2xx and
//     the dependency is Healthy; when the miner-api is killed (connection
//     refused, 5xx, timeout) the dependency goes Unhealthy and — because it is a
//     named Dependency in the rollup — the SPECIFIC failing check is named in the
//     Rollup.Checks output.
//
//   - RegisterGraValAttestation: a GraVal bootstrap DaemonSet
//     attestation-readiness probe. It drives the REAL chutes.Attestor
//     challenge/respond/verify path: a node that produces a valid proof is
//     attestation-ready (Healthy); a node whose proof fails verification (or whose
//     probe errors) is Unhealthy with the verification failure named.
//
// Both wire into the same DependencyRollup so overall cluster health reflects
// Chutes miner liveness + GraVal attestation readiness, and NewRunID stamps a
// per-run UUID onto a captured rollup for forensic correlation.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/chutes"
)

const (
	// defaultMinerAPIName is used when MinerAPIConfig.Name is empty.
	defaultMinerAPIName = "chutes-miner-api"
	// defaultGraValName is used when GraValConfig.Name is empty.
	defaultGraValName = "graval-attestation"
)

// NewRunID returns a fresh, random per-run identifier in canonical RFC-4122
// version-4 UUID string form (8-4-4-4-12 lowercase hex). It is generated from
// crypto/rand using only the standard library, so it is portable across every
// OS (CLAUDE-2: no platform-specific facility). Callers stamp it onto a captured
// health rollup so a "healthy -> unhealthy" transition can be correlated to the
// exact observation run.
func NewRunID() string {
	var b [16]byte
	// rand.Read never returns a short read; treat any error as fatal-to-the-id by
	// falling back to a time-seeded value so we never emit an empty id.
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		// Extremely unlikely; derive a non-empty unique-ish id from the clock.
		now := uint64(time.Now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte(now >> (8 * i))
		}
	}
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}

// MinerAPIConfig configures an HTTP health probe against the Chutes miner-api
// Deployment.
type MinerAPIConfig struct {
	// Name uniquely identifies the dependency in the rollup. Defaults to
	// defaultMinerAPIName when empty. This is the name reported as the failing
	// check when the miner-api is down.
	Name string
	// Endpoint is the miner-api liveness/readiness URL to GET (e.g.
	// "http://chutes-miner-api:8000/livez"). Required.
	Endpoint string
	// Kind selects liveness vs. readiness. Defaults to Readiness when empty.
	Kind CheckKind
	// Critical marks the miner-api as critical: its failure makes the overall
	// rollup Unhealthy rather than merely Degraded.
	Critical bool
	// Timeout bounds a single probe. Defaults to defaultCheckTimeout when zero.
	Timeout time.Duration
	// Client is an optional injectable HTTP client (for tests / custom
	// transports). Defaults to a client whose per-request timeout matches Timeout.
	Client *http.Client
}

// MinerAPICheck returns the DependencyCheck closure for a miner-api probe. It is
// exported so callers can compose it into their own Dependency if they do not
// want the convenience registration. A non-2xx status, a transport error
// (connection refused, DNS failure), or a context timeout all return a non-nil
// error so the rollup reports the check Unhealthy with that error named.
func MinerAPICheck(cfg MinerAPIConfig) DependencyCheck {
	name := cfg.Name
	if name == "" {
		name = defaultMinerAPIName
	}
	client := cfg.Client
	return func(ctx context.Context) error {
		if cfg.Endpoint == "" {
			return fmt.Errorf("%s: no endpoint configured", name)
		}
		c := client
		if c == nil {
			c = &http.Client{}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Endpoint, nil)
		if err != nil {
			return fmt.Errorf("%s: build request: %w", name, err)
		}
		resp, err := c.Do(req)
		if err != nil {
			return fmt.Errorf("%s: miner-api unreachable: %w", name, err)
		}
		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("%s: miner-api returned HTTP %d", name, resp.StatusCode)
		}
		return nil
	}
}

// RegisterMinerAPI registers the Chutes miner-api Deployment health check into
// the rollup so overall cluster health reflects miner-api liveness/readiness.
// The dependency is named (cfg.Name or defaultMinerAPIName) so when the
// miner-api is killed the rollup reports Unhealthy with that SPECIFIC check named
// and an error describing why.
func RegisterMinerAPI(r *DependencyRollup, cfg MinerAPIConfig) {
	name := cfg.Name
	if name == "" {
		name = defaultMinerAPIName
	}
	r.Register(Dependency{
		Name:     name,
		Kind:     cfg.Kind,
		Critical: cfg.Critical,
		Timeout:  cfg.Timeout,
		Check:    MinerAPICheck(cfg),
	})
}

// AttestationProbe collects one fresh GraVal attestation
// (challenge/response) from a node so it can be verified. It MUST honor ctx.
// Returning an error means the node could not be probed and is treated as an
// attestation failure (not-ready).
type AttestationProbe func(ctx context.Context) (chutes.AttestationResult, error)

// GraValConfig configures the GraVal bootstrap DaemonSet attestation-readiness
// check.
type GraValConfig struct {
	// Name uniquely identifies the dependency in the rollup. Defaults to
	// defaultGraValName when empty.
	Name string
	// Attestor verifies a node's attestation response. Required. Any
	// implementation of chutes.Attestor (HMAC reference or a real
	// proof-of-GPU/TEE attestor) plugs in unchanged.
	Attestor chutes.Attestor
	// Probe collects a fresh attestation (challenge + node response) to verify.
	// Required.
	Probe AttestationProbe
	// Kind selects liveness vs. readiness. Defaults to Readiness when empty —
	// attestation is naturally a readiness gate (a node not yet attested should
	// not receive traffic) but liveness is supported for bootstrap DaemonSets.
	Kind CheckKind
	// Critical marks attestation as critical: failure makes the overall rollup
	// Unhealthy.
	Critical bool
	// Timeout bounds a single probe+verify. Defaults to defaultCheckTimeout.
	Timeout time.Duration
}

// errNoAttestor / errNoProbe surface misconfiguration as a named, non-nil check
// error rather than a panic.
var (
	errNoAttestor = errors.New("graval: no attestor configured")
	errNoProbe    = errors.New("graval: no attestation probe configured")
)

// GraValAttestationCheck returns the DependencyCheck closure for a GraVal
// attestation-readiness probe. It drives the real chutes.Attestor verify path:
// it collects a fresh challenge/response via the probe and verifies it. A failed
// verification, a probe error, or misconfiguration returns a non-nil error so the
// rollup reports the check Unhealthy with the verification failure named.
func GraValAttestationCheck(cfg GraValConfig) DependencyCheck {
	name := cfg.Name
	if name == "" {
		name = defaultGraValName
	}
	return func(ctx context.Context) error {
		if cfg.Attestor == nil {
			return fmt.Errorf("%s: %w", name, errNoAttestor)
		}
		if cfg.Probe == nil {
			return fmt.Errorf("%s: %w", name, errNoProbe)
		}
		// Respect cancellation before doing work.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		ar, err := cfg.Probe(ctx)
		if err != nil {
			return fmt.Errorf("%s: attestation probe failed: %w", name, err)
		}
		if err := cfg.Attestor.Verify(ar.Challenge, ar.Response); err != nil {
			return fmt.Errorf("%s: attestation not ready: %w", name, err)
		}
		return nil
	}
}

// RegisterGraValAttestation registers the GraVal bootstrap DaemonSet
// attestation-readiness check into the rollup so overall cluster health reflects
// GraVal attestation readiness. The dependency is named so an un-attested or
// failing node names the SPECIFIC failing check in the rollup output.
func RegisterGraValAttestation(r *DependencyRollup, cfg GraValConfig) {
	name := cfg.Name
	if name == "" {
		name = defaultGraValName
	}
	r.Register(Dependency{
		Name:     name,
		Kind:     cfg.Kind,
		Critical: cfg.Critical,
		Timeout:  cfg.Timeout,
		Check:    GraValAttestationCheck(cfg),
	})
}

// CapturedRollup pairs a Rollup with the per-run UUID under which it was
// observed, for forensic capture of a healthy -> unhealthy transition.
type CapturedRollup struct {
	RunID  string `json:"run_id"`
	Rollup Rollup `json:"rollup"`
}

// CaptureLiveness evaluates liveness dependencies and stamps the result with a
// fresh per-run UUID. Use it to capture before/after deltas (e.g. miner up ->
// miner-api killed) with a stable correlation id.
func CaptureLiveness(ctx context.Context, r *DependencyRollup) CapturedRollup {
	return CapturedRollup{RunID: NewRunID(), Rollup: r.CheckLiveness(ctx)}
}

// CaptureReadiness evaluates readiness dependencies and stamps the result with a
// fresh per-run UUID.
func CaptureReadiness(ctx context.Context, r *DependencyRollup) CapturedRollup {
	return CapturedRollup{RunID: NewRunID(), Rollup: r.CheckReadiness(ctx)}
}

// FailingChecks returns the names of all checks in the rollup that are not
// Healthy (Unhealthy, Degraded, or Starting), so a caller can log the SPECIFIC
// failing checks by name.
func FailingChecks(rollup Rollup) []string {
	var names []string
	for _, c := range rollup.Checks {
		if c.Status != Healthy {
			names = append(names, c.Name)
		}
	}
	return names
}
