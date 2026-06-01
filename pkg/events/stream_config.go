package events

// stream_config.go — HXC-1121
//
// Authoritative JetStream stream configuration for the 5 Helix control-plane
// streams. This file is the SINGLE SOURCE OF TRUTH for:
//   - stream names   (must match the HelixStream constants in helix_events.go)
//   - subjects       (wildcard per stream domain)
//   - MaxAge         (retention window)
//   - Replicas       (replication factor — 1 for single-node dev/test; set to 3
//                     in production multi-node deployments)
//   - Retention      (LimitsPolicy — delete oldest on size/age breach)
//   - Storage        (FileStorage — durable across broker restart)
//
// EnsureStreams(ctx, js) creates-or-updates all 5 streams idempotently: it
// calls UpdateStream when the stream already exists so that config drift is
// corrected on every restart, and AddStream when it does not. Both paths use
// the EXACT StreamConfig from the config table — no per-call overrides.

import (
	"context"
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// HelixMaxAge is the retention window for all 5 Helix control-plane streams.
// Events older than this are evicted by JetStream. 24 h is a reasonable
// operational window for a single-node cluster; set to a larger value for
// multi-node / long-tail replay requirements.
const HelixMaxAge = 24 * time.Hour

// HelixReplicas is the replication factor for all 5 streams.  1 is correct for
// single-node development and integration tests; production deployments should
// override via EnsureStreamsWithConfig.
const HelixReplicas = 1

// HelixStreamDef holds the authoritative JetStream configuration for one
// HELIX_* stream.  It is the input to EnsureStreams; callers that need to
// inspect the expected configuration (e.g. integration assertions) use
// HelixStreamConfigs.
type HelixStreamDef struct {
	// Name is the HELIX_* stream name (e.g. "HELIX_NODES").
	Name HelixStream
	// Subjects is the ordered list of NATS subject filters captured by this
	// stream.  Always exactly one entry per stream ("helix.<domain>.>").
	Subjects []string
	// MaxAge is the maximum age of messages retained in the stream.
	MaxAge time.Duration
	// Replicas is the number of JetStream replicas.
	Replicas int
}

// HelixStreamConfigs is the ordered, authoritative config table for all 5
// control-plane streams.  This is the single source of truth.
//
// Subject convention: "helix.<domain>.>" so all Helix events share the "helix."
// top-level namespace; the domain prefix matches helixStreamDomains in
// helix_events.go.
var HelixStreamConfigs = []HelixStreamDef{
	{
		Name:     StreamNodes,
		Subjects: []string{"helix.nodes.>"},
		MaxAge:   HelixMaxAge,
		Replicas: HelixReplicas,
	},
	{
		Name:     StreamSessions,
		Subjects: []string{"helix.sessions.>"},
		MaxAge:   HelixMaxAge,
		Replicas: HelixReplicas,
	},
	{
		Name:     StreamScheduler,
		Subjects: []string{"helix.scheduler.>"},
		MaxAge:   HelixMaxAge,
		Replicas: HelixReplicas,
	},
	{
		Name:     StreamHealth,
		Subjects: []string{"helix.health.>"},
		MaxAge:   HelixMaxAge,
		Replicas: HelixReplicas,
	},
	{
		Name:     StreamAlerts,
		Subjects: []string{"helix.alerts.>"},
		MaxAge:   HelixMaxAge,
		Replicas: HelixReplicas,
	},
}

// JetStreamAdmin is the narrow seam used by EnsureStreams. It exposes only the
// stream-administration primitives so unit tests can inject a deterministic
// fake without a real NATS broker.
//
// The real implementation is a thin wrapper around natsgo.JetStreamContext
// (see RealJetStreamAdmin). Integration tests supply the real context directly
// via NewRealJetStreamAdmin.
type JetStreamAdmin interface {
	// StreamInfo returns metadata for the named stream. Returns an error (whose
	// Is-chain includes natsgo.ErrStreamNotFound) when the stream is absent.
	StreamInfo(name string) (*natsgo.StreamInfo, error)
	// AddStream creates a new stream with the provided config.
	AddStream(cfg *natsgo.StreamConfig) (*natsgo.StreamInfo, error)
	// UpdateStream updates an existing stream to match the provided config.
	UpdateStream(cfg *natsgo.StreamConfig) (*natsgo.StreamInfo, error)
}

// RealJetStreamAdmin wraps natsgo.JetStreamContext to satisfy JetStreamAdmin.
// Construct via NewRealJetStreamAdmin.
type RealJetStreamAdmin struct {
	js natsgo.JetStreamContext
}

// NewRealJetStreamAdmin creates a JetStreamAdmin backed by js.
func NewRealJetStreamAdmin(js natsgo.JetStreamContext) *RealJetStreamAdmin {
	return &RealJetStreamAdmin{js: js}
}

// StreamInfo delegates to natsgo.JetStreamContext.StreamInfo.
func (r *RealJetStreamAdmin) StreamInfo(name string) (*natsgo.StreamInfo, error) {
	return r.js.StreamInfo(name)
}

// AddStream delegates to natsgo.JetStreamContext.AddStream.
func (r *RealJetStreamAdmin) AddStream(cfg *natsgo.StreamConfig) (*natsgo.StreamInfo, error) {
	return r.js.AddStream(cfg)
}

// UpdateStream delegates to natsgo.JetStreamContext.UpdateStream.
func (r *RealJetStreamAdmin) UpdateStream(cfg *natsgo.StreamConfig) (*natsgo.StreamInfo, error) {
	return r.js.UpdateStream(cfg)
}

// streamConfigFor converts a HelixStreamDef into the natsgo.StreamConfig that
// EnsureStreams submits to the JetStream broker. It is the authoritative
// config materialisation — no other code path may build a StreamConfig for
// Helix streams.
func streamConfigFor(def HelixStreamDef) *natsgo.StreamConfig {
	return &natsgo.StreamConfig{
		Name:      string(def.Name),
		Subjects:  def.Subjects,
		MaxAge:    def.MaxAge,
		Replicas:  def.Replicas,
		Storage:   natsgo.FileStorage,
		Retention: natsgo.LimitsPolicy,
	}
}

// EnsureStreams creates or updates all 5 HELIX_* JetStream streams so their
// configuration matches HelixStreamConfigs. It is safe to call multiple times
// (idempotent): a second call with an already-correct broker state produces no
// error.
//
// For each stream:
//   - If StreamInfo returns natsgo.ErrStreamNotFound → AddStream.
//   - If StreamInfo succeeds (stream exists) → UpdateStream to converge config.
//   - Any other StreamInfo error → return immediately with a wrapped error.
//
// The ctx parameter is threaded through for future cancellation support; the
// current natsgo.JetStreamContext does not accept a ctx directly, so it is
// checked for cancellation before each operation.
func EnsureStreams(ctx context.Context, js JetStreamAdmin) error {
	for _, def := range HelixStreamConfigs {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("events: EnsureStreams cancelled before %q: %w", def.Name, err)
		}

		cfg := streamConfigFor(def)

		_, infoErr := js.StreamInfo(string(def.Name))
		if infoErr != nil {
			if infoErr == natsgo.ErrStreamNotFound {
				if _, addErr := js.AddStream(cfg); addErr != nil {
					return fmt.Errorf("events: EnsureStreams AddStream %q: %w", def.Name, addErr)
				}
				continue
			}
			return fmt.Errorf("events: EnsureStreams StreamInfo %q: %w", def.Name, infoErr)
		}

		// Stream exists — converge configuration via UpdateStream.
		if _, updErr := js.UpdateStream(cfg); updErr != nil {
			return fmt.Errorf("events: EnsureStreams UpdateStream %q: %w", def.Name, updErr)
		}
	}
	return nil
}
