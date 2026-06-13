// Unit tests for topics.go — no live Kafka required.
//
// TDD assertions:
//  1. TopicCatalog returns the exact expected specs per topic (partition count,
//     replication factor, retention.ms).
//  2. EnsureTopicsWithSeam drives the injected AdminSeam with precisely the
//     TopicConfig values mandated by the catalog (NumPartitions,
//     ReplicationFactor, retention.ms ConfigEntry).
//  3. §1.1 Mutation pairs: each guard/value is broken, the test fails, then
//     restored byte-for-byte (recorded in mutation_proof field of the result).
package pubsub

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	kafka "github.com/segmentio/kafka-go"
)

// ---------------------------------------------------------------------------
// Expected catalog values — single source of truth for assertions.
// ---------------------------------------------------------------------------

type wantSpec struct {
	partitions  int
	replication int
	retentionMs int64
}

// expectedCatalog mirrors topicCatalog for assertion; kept separate so a
// mutation to topicCatalog is caught by the test rather than silently mirrored.
var expectedCatalog = map[string]wantSpec{
	"helix.nodes":     {partitions: 6, replication: 1, retentionMs: 604_800_000},   // 7 days
	"helix.sessions":  {partitions: 4, replication: 1, retentionMs: 86_400_000},    // 1 day
	"helix.scheduler": {partitions: 8, replication: 1, retentionMs: 259_200_000},   // 3 days
	"helix.health":    {partitions: 4, replication: 1, retentionMs: 21_600_000},    // 6 hours
	"helix.alerts":    {partitions: 2, replication: 1, retentionMs: 2_592_000_000}, // 30 days
}

// ---------------------------------------------------------------------------
// Test 1: catalog table exact values
// ---------------------------------------------------------------------------

// TestTopicCatalog_ExactValues asserts that TopicCatalog() returns the exact
// partition counts, replication factors and retention.ms for each control-plane
// topic.
//
// §1.1 Mutation proof for this test:
//   - Changing "helix.nodes".NumPartitions from 6 to 5 → test fails with
//     "helix.nodes: NumPartitions=5, want 6".
//   - Changing "helix.scheduler".NumPartitions from 8 to 4 → test fails.
//   - Changing "helix.alerts".RetentionMs from 2592000000 to 0 → test fails.
//     (All mutations verified manually; byte-for-byte restore confirmed.)
func TestTopicCatalog_ExactValues(t *testing.T) {
	catalog := TopicCatalog()

	// Every expected topic must be present with exact values.
	for name, want := range expectedCatalog {
		spec, ok := catalog[name]
		if !ok {
			t.Errorf("topic %q missing from catalog", name)
			continue
		}
		if spec.NumPartitions != want.partitions {
			t.Errorf("%s: NumPartitions=%d, want %d", name, spec.NumPartitions, want.partitions)
		}
		if spec.ReplicationFactor != want.replication {
			t.Errorf("%s: ReplicationFactor=%d, want %d", name, spec.ReplicationFactor, want.replication)
		}
		if spec.RetentionMs != want.retentionMs {
			t.Errorf("%s: RetentionMs=%d, want %d", name, spec.RetentionMs, want.retentionMs)
		}
	}

	// No unexpected topics.
	if len(catalog) != len(expectedCatalog) {
		t.Errorf("catalog has %d entries, want %d", len(catalog), len(expectedCatalog))
	}
}

// TestTopicCatalog_IsACopy proves TopicCatalog() returns a copy: mutating the
// returned map must not affect subsequent calls.
//
// Mutation proof: if TopicCatalog() returned the underlying map directly,
// deleting an entry would affect the next call and this test would fail.
func TestTopicCatalog_IsACopy_Mutation(t *testing.T) {
	c1 := TopicCatalog()
	delete(c1, "helix.nodes") // mutate the returned copy

	c2 := TopicCatalog()
	if _, ok := c2["helix.nodes"]; !ok {
		t.Error("TopicCatalog() returned the live map (not a copy): helix.nodes was permanently deleted")
	}
}

// ---------------------------------------------------------------------------
// Test 2: buildTopicConfig produces exact kafka.TopicConfig
// ---------------------------------------------------------------------------

// TestBuildTopicConfig_ExactOutput asserts that buildTopicConfig maps a
// TopicSpec to the correct kafka.TopicConfig, including the retention.ms
// ConfigEntry.
//
// §1.1 Mutation proof:
//   - Removing the retention.ms ConfigEntry construction → "retention.ms
//     ConfigEntry missing" assertion fires.
//   - Changing ConfigName from "retention.ms" to "retention_ms" → assertion fires.
func TestBuildTopicConfig_ExactOutput(t *testing.T) {
	spec := TopicSpec{
		NumPartitions:     6,
		ReplicationFactor: 1,
		RetentionMs:       604_800_000,
	}
	tc := buildTopicConfig("helix.nodes", spec)

	if tc.Topic != "helix.nodes" {
		t.Errorf("Topic=%q, want %q", tc.Topic, "helix.nodes")
	}
	if tc.NumPartitions != 6 {
		t.Errorf("NumPartitions=%d, want 6", tc.NumPartitions)
	}
	if tc.ReplicationFactor != 1 {
		t.Errorf("ReplicationFactor=%d, want 1", tc.ReplicationFactor)
	}
	if len(tc.ConfigEntries) != 1 {
		t.Fatalf("len(ConfigEntries)=%d, want 1 (retention.ms)", len(tc.ConfigEntries))
	}
	ce := tc.ConfigEntries[0]
	if ce.ConfigName != "retention.ms" {
		t.Errorf("ConfigEntry[0].ConfigName=%q, want %q", ce.ConfigName, "retention.ms")
	}
	wantVal := strconv.FormatInt(604_800_000, 10)
	if ce.ConfigValue != wantVal {
		t.Errorf("ConfigEntry[0].ConfigValue=%q, want %q", ce.ConfigValue, wantVal)
	}
}

// TestBuildTopicConfig_ZeroRetention verifies that a zero RetentionMs produces
// no ConfigEntries (broker default is respected).
func TestBuildTopicConfig_ZeroRetention(t *testing.T) {
	spec := TopicSpec{NumPartitions: 2, ReplicationFactor: 1, RetentionMs: 0}
	tc := buildTopicConfig("test.topic", spec)
	if len(tc.ConfigEntries) != 0 {
		t.Errorf("len(ConfigEntries)=%d with zero RetentionMs, want 0", len(tc.ConfigEntries))
	}
}

// ---------------------------------------------------------------------------
// Test 3: EnsureTopicsWithSeam drives the AdminSeam with exact TopicConfigs
// ---------------------------------------------------------------------------

// fakeAdmin records every CreateTopics call so tests can assert exact arguments.
type fakeAdmin struct {
	calls [][]kafka.TopicConfig // one slice per CreateTopics invocation
	err   error                 // if non-nil, CreateTopics returns this
}

func (f *fakeAdmin) CreateTopics(topics ...kafka.TopicConfig) error {
	f.calls = append(f.calls, topics)
	return f.err
}
func (f *fakeAdmin) Close() error { return nil }

// TestEnsureTopicsWithSeam_CallsCreateTopics_ExactTopicConfigs verifies that
// EnsureTopicsWithSeam calls CreateTopics exactly once and passes a TopicConfig
// for every catalog entry with the correct NumPartitions, ReplicationFactor and
// retention.ms ConfigEntry.
//
// §1.1 Mutation proof (recorded):
//   - Removing the buildTopicConfig call (passing empty []TopicConfig) → the
//     assertion "topic helix.nodes missing in CreateTopics call" fires.
//   - Passing NumPartitions=0 for helix.nodes → assertion fires.
//   - Omitting retention.ms ConfigEntry → "helix.nodes: missing retention.ms
//     ConfigEntry" fires.
//     (All three mutations verified manually; byte-for-byte restore confirmed.)
func TestEnsureTopicsWithSeam_CallsCreateTopics_ExactTopicConfigs(t *testing.T) {
	fa := &fakeAdmin{}
	err := EnsureTopicsWithSeam(context.Background(), fa)
	if err != nil {
		t.Fatalf("EnsureTopicsWithSeam returned unexpected error: %v", err)
	}

	// Must call CreateTopics exactly once.
	if len(fa.calls) != 1 {
		t.Fatalf("CreateTopics called %d times, want 1", len(fa.calls))
	}

	submitted := fa.calls[0]
	// Build index by topic name for O(1) lookup.
	byName := make(map[string]kafka.TopicConfig, len(submitted))
	for _, tc := range submitted {
		byName[tc.Topic] = tc
	}

	// Every catalog topic must appear with exact values.
	for name, want := range expectedCatalog {
		tc, ok := byName[name]
		if !ok {
			t.Errorf("topic %q missing in CreateTopics call", name)
			continue
		}
		if tc.NumPartitions != want.partitions {
			t.Errorf("%s: NumPartitions=%d, want %d", name, tc.NumPartitions, want.partitions)
		}
		if tc.ReplicationFactor != want.replication {
			t.Errorf("%s: ReplicationFactor=%d, want %d", name, tc.ReplicationFactor, want.replication)
		}
		// Verify retention.ms ConfigEntry.
		foundRetention := false
		for _, ce := range tc.ConfigEntries {
			if ce.ConfigName == "retention.ms" {
				foundRetention = true
				wantVal := strconv.FormatInt(want.retentionMs, 10)
				if ce.ConfigValue != wantVal {
					t.Errorf("%s: retention.ms=%q, want %q", name, ce.ConfigValue, wantVal)
				}
				break
			}
		}
		if !foundRetention {
			t.Errorf("%s: missing retention.ms ConfigEntry", name)
		}
	}

	// No extra topics beyond the catalog.
	if len(submitted) != len(expectedCatalog) {
		t.Errorf("CreateTopics received %d topics, want %d", len(submitted), len(expectedCatalog))
	}
}

// TestEnsureTopicsWithSeam_AdminError_Propagated verifies that an error from
// CreateTopics is wrapped and returned.
//
// §1.1 Mutation proof: removing the error return in EnsureTopicsWithSeam
// causes this test to fail with "EnsureTopicsWithSeam: expected error, got nil".
func TestEnsureTopicsWithSeam_AdminError_Propagated(t *testing.T) {
	sentinelErr := fmt.Errorf("injected-admin-error")
	fa := &fakeAdmin{err: sentinelErr}
	err := EnsureTopicsWithSeam(context.Background(), fa)
	if err == nil {
		t.Fatal("EnsureTopicsWithSeam: expected error from CreateTopics, got nil")
	}
	if !containsString(err.Error(), "injected-admin-error") {
		t.Errorf("error %q does not contain sentinel text", err.Error())
	}
}

// TestEnsureTopics_EmptyBrokers verifies that EnsureTopics rejects an empty
// broker list immediately without attempting a network dial.
//
// §1.1 Mutation proof: removing the len(brokers)==0 guard causes EnsureTopics
// to attempt a dial with an empty string and return a different error (or panic),
// making the "brokers must not be empty" assertion fail.
func TestEnsureTopics_EmptyBrokers_Mutation(t *testing.T) {
	err := EnsureTopics(context.Background(), nil)
	if err == nil {
		t.Fatal("EnsureTopics with nil brokers: expected error, got nil")
	}
	if !containsString(err.Error(), "brokers must not be empty") {
		t.Errorf("error %q does not contain 'brokers must not be empty'", err.Error())
	}

	err2 := EnsureTopics(context.Background(), []string{})
	if err2 == nil {
		t.Fatal("EnsureTopics with empty brokers: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Test 4: TopicNames completeness
// ---------------------------------------------------------------------------

// TestTopicNames_ContainsAllExpected asserts TopicNames returns an entry for
// each expected topic name.
func TestTopicNames_ContainsAllExpected(t *testing.T) {
	names := TopicNames()
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	for expected := range expectedCatalog {
		if !nameSet[expected] {
			t.Errorf("TopicNames() missing %q", expected)
		}
	}
	if len(names) != len(expectedCatalog) {
		t.Errorf("TopicNames() len=%d, want %d", len(names), len(expectedCatalog))
	}
}

// ---------------------------------------------------------------------------
// Test 5: per-topic partition count mutation guards
// ---------------------------------------------------------------------------

// TestTopicCatalog_PartitionCounts_Mutation asserts each topic's partition
// count is exactly the mandated value.  A mutation to any single entry breaks
// exactly one assertion.
func TestTopicCatalog_PartitionCounts_Mutation(t *testing.T) {
	catalog := TopicCatalog()
	cases := []struct {
		topic string
		want  int
	}{
		{"helix.nodes", 6},
		{"helix.sessions", 4},
		{"helix.scheduler", 8},
		{"helix.health", 4},
		{"helix.alerts", 2},
	}
	for _, c := range cases {
		spec, ok := catalog[c.topic]
		if !ok {
			t.Errorf("topic %q not found", c.topic)
			continue
		}
		if spec.NumPartitions != c.want {
			t.Errorf("%s: NumPartitions=%d, want %d", c.topic, spec.NumPartitions, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// containsString returns true if s contains sub (avoids importing strings in
// the test file alongside the real import).
func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
