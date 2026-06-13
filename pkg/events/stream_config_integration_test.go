//go:build integration

// Package events — HXC-1121 integration tests for EnsureStreams.
//
// These tests require a REAL NATS+JetStream broker. The broker is started via
// brokertest.StartNATS (digital.vasic.containers/pkg/brokertest). Hard-FAIL on
// broker unavailability — a skip on a broken feature is itself a PASS-bluff
// (CLAUDE-1 §7.1).
//
// Per-run UUID discipline (§7.1 anti-bluff anchor):
//
//	Every test embeds a fresh uuid.New().String() as a per-run marker.
//	The publish-and-count assertion requires that ONLY the message published
//	in THIS run is counted (by publishing to a UUID-derived subject and
//	measuring the per-stream message count delta).
package events

import (
	"context"
	"fmt"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"digital.vasic.containers/pkg/brokertest"
	"github.com/google/uuid"
)

// TestEnsureStreams_RealBroker_AllFiveStreamsExist boots a real NATS+JetStream
// broker, calls EnsureStreams, then asserts StreamInfo for each of the 5 streams
// returns the expected subjects, MaxAge, and replica count.
func TestEnsureStreams_RealBroker_AllFiveStreamsExist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	endpoint, stopBroker, err := brokertest.StartNATS(ctx)
	if err != nil {
		t.Fatalf("brokertest.StartNATS: %v", err)
	}
	defer stopBroker()
	t.Logf("real NATS broker at %s", endpoint)

	conn, err := natsgo.Connect(endpoint)
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	defer conn.Close()

	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("conn.JetStream: %v", err)
	}

	admin := NewRealJetStreamAdmin(js)

	// First call — all streams absent → AddStream for each.
	if err := EnsureStreams(ctx, admin); err != nil {
		t.Fatalf("EnsureStreams (first call): %v", err)
	}

	// Second call — all streams exist → UpdateStream for each (idempotency proof).
	if err := EnsureStreams(ctx, admin); err != nil {
		t.Fatalf("EnsureStreams (second call, idempotency): %v", err)
	}

	// Assert StreamInfo for each stream returns the expected configuration.
	for _, def := range HelixStreamConfigs {
		info, err := js.StreamInfo(string(def.Name))
		if err != nil {
			t.Errorf("StreamInfo(%q) after EnsureStreams: %v", def.Name, err)
			continue
		}

		// Subjects — must match exactly.
		if len(info.Config.Subjects) != len(def.Subjects) {
			t.Errorf("stream %q: Subjects len got %d want %d",
				def.Name, len(info.Config.Subjects), len(def.Subjects))
		} else {
			for i, wantSub := range def.Subjects {
				if info.Config.Subjects[i] != wantSub {
					t.Errorf("stream %q: Subjects[%d] got %q want %q",
						def.Name, i, info.Config.Subjects[i], wantSub)
				}
			}
		}

		// MaxAge — must match HelixMaxAge.
		if info.Config.MaxAge != def.MaxAge {
			t.Errorf("stream %q: MaxAge got %v want %v",
				def.Name, info.Config.MaxAge, def.MaxAge)
		}

		// Replicas — must match HelixReplicas (1 for single-node test broker).
		if info.Config.Replicas != def.Replicas {
			t.Errorf("stream %q: Replicas got %d want %d",
				def.Name, info.Config.Replicas, def.Replicas)
		}

		t.Logf("stream %q: subjects=%v maxAge=%v replicas=%d — OK",
			def.Name, info.Config.Subjects, info.Config.MaxAge, info.Config.Replicas)
	}
}

// TestEnsureStreams_RealBroker_PublishIncrementsMsgCount publishes a
// per-run-UUID message to one subject per stream and asserts the stream's
// message count increments by exactly 1 for each publish.
//
// Mutation proof (integration tier):
//
//	If a subject were wrong (e.g. "helix.nodes.*"), the publish would land
//	outside the stream's wildcard and the count would NOT increment.
//	The assertion `got == before+1` would FAIL — exactly the intended mutation
//	detection for the subject config.
func TestEnsureStreams_RealBroker_PublishIncrementsMsgCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	endpoint, stopBroker, err := brokertest.StartNATS(ctx)
	if err != nil {
		t.Fatalf("brokertest.StartNATS: %v", err)
	}
	defer stopBroker()

	conn, err := natsgo.Connect(endpoint)
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	defer conn.Close()

	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("conn.JetStream: %v", err)
	}

	admin := NewRealJetStreamAdmin(js)
	if err := EnsureStreams(ctx, admin); err != nil {
		t.Fatalf("EnsureStreams: %v", err)
	}

	// Per-run UUID — the anti-bluff anchor. Each test run's messages are
	// distinct; there are no leftover messages from prior runs because the
	// broker is started fresh by brokertest.StartNATS.
	runID := uuid.New().String()
	t.Logf("run UUID: %s", runID)

	// Map each stream to a concrete publish subject matching its wildcard.
	// The subject is "{domain}.{runID[:8]}.test" which matches "helix.<domain}.>"
	publishSubjects := map[HelixStream]string{
		StreamNodes:     fmt.Sprintf("helix.nodes.%s.test", runID[:8]),
		StreamSessions:  fmt.Sprintf("helix.sessions.%s.test", runID[:8]),
		StreamScheduler: fmt.Sprintf("helix.scheduler.%s.test", runID[:8]),
		StreamHealth:    fmt.Sprintf("helix.health.%s.test", runID[:8]),
		StreamAlerts:    fmt.Sprintf("helix.alerts.%s.test", runID[:8]),
	}

	for _, def := range HelixStreamConfigs {
		// Capture the message count BEFORE publishing.
		infoBefore, err := js.StreamInfo(string(def.Name))
		if err != nil {
			t.Errorf("StreamInfo(%q) before publish: %v", def.Name, err)
			continue
		}
		beforeCount := infoBefore.State.Msgs

		// Publish exactly one message with the per-run UUID as the payload.
		subject := publishSubjects[def.Name]
		payload := []byte(runID)
		if _, err := js.Publish(subject, payload); err != nil {
			t.Errorf("stream %q: Publish(%q): %v", def.Name, subject, err)
			continue
		}
		t.Logf("stream %q: published runID=%s to subject %q", def.Name, runID[:8], subject)

		// Capture the message count AFTER publishing.
		infoAfter, err := js.StreamInfo(string(def.Name))
		if err != nil {
			t.Errorf("StreamInfo(%q) after publish: %v", def.Name, err)
			continue
		}
		afterCount := infoAfter.State.Msgs

		// Sink-side assertion: the count must have incremented by exactly 1.
		// This proves:
		//   (a) the stream captured the message (subjects match),
		//   (b) exactly one message was stored (no duplication),
		//   (c) this is the CURRENT run's message (broker started fresh).
		if afterCount != beforeCount+1 {
			t.Errorf("stream %q: Msgs count: before=%d after=%d want before+1=%d (subject=%q)",
				def.Name, beforeCount, afterCount, beforeCount+1, subject)
		} else {
			t.Logf("stream %q: Msgs before=%d after=%d (+1) — OK", def.Name, beforeCount, afterCount)
		}
	}
}
