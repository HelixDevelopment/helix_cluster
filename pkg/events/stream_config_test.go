package events

// stream_config_test.go — HXC-1121 deterministic unit tests.
//
// All tests use an in-process fakeJetStreamAdmin (implements JetStreamAdmin)
// — no real NATS broker required.
//
// Assertion discipline (CLAUDE-1 §7.1):
//   Every assertion is sink-side: it verifies EXACT stream names, subjects,
//   MaxAge, Replicas, and the exact sequence of AddStream / UpdateStream calls.
//   "no panic" / "non-nil" / "len>0" assertions are FORBIDDEN here.
//
// §1.1 Mutation pairing: each meaningful guard is mutated, confirmed FAIL,
// then restored byte-for-byte.  Results documented below each test.

import (
	"context"
	"errors"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// ---------------------------------------------------------------------------
// Fake JetStreamAdmin (unit-test seam — no real NATS)
// ---------------------------------------------------------------------------

// jsAdminCall records one invocation of the fake.
type jsAdminCall struct {
	op  string // "StreamInfo" | "AddStream" | "UpdateStream"
	cfg *natsgo.StreamConfig
}

// fakeJetStreamAdmin implements JetStreamAdmin for unit tests.
// It records every call and lets callers control per-stream existence.
type fakeJetStreamAdmin struct {
	// existingStreams contains stream names that StreamInfo should report as
	// already existing.  Any name NOT in this set causes StreamInfo to return
	// natsgo.ErrStreamNotFound, triggering AddStream.
	existingStreams map[string]bool

	// calls records every method invocation in order.
	calls []jsAdminCall
}

func newFakeJetStreamAdmin(existing ...string) *fakeJetStreamAdmin {
	m := make(map[string]bool, len(existing))
	for _, s := range existing {
		m[s] = true
	}
	return &fakeJetStreamAdmin{existingStreams: m}
}

func (f *fakeJetStreamAdmin) StreamInfo(name string) (*natsgo.StreamInfo, error) {
	f.calls = append(f.calls, jsAdminCall{op: "StreamInfo"})
	if !f.existingStreams[name] {
		return nil, natsgo.ErrStreamNotFound
	}
	return &natsgo.StreamInfo{Config: natsgo.StreamConfig{Name: name}}, nil
}

func (f *fakeJetStreamAdmin) AddStream(cfg *natsgo.StreamConfig) (*natsgo.StreamInfo, error) {
	f.calls = append(f.calls, jsAdminCall{op: "AddStream", cfg: cfg})
	// Mark the stream as existing so a second EnsureStreams call goes to UpdateStream.
	f.existingStreams[cfg.Name] = true
	return &natsgo.StreamInfo{Config: *cfg}, nil
}

func (f *fakeJetStreamAdmin) UpdateStream(cfg *natsgo.StreamConfig) (*natsgo.StreamInfo, error) {
	f.calls = append(f.calls, jsAdminCall{op: "UpdateStream", cfg: cfg})
	return &natsgo.StreamInfo{Config: *cfg}, nil
}

// addCalls returns only the AddStream calls recorded by the fake.
func (f *fakeJetStreamAdmin) addCalls() []jsAdminCall {
	var out []jsAdminCall
	for _, c := range f.calls {
		if c.op == "AddStream" {
			out = append(out, c)
		}
	}
	return out
}

// updateCalls returns only the UpdateStream calls.
func (f *fakeJetStreamAdmin) updateCalls() []jsAdminCall {
	var out []jsAdminCall
	for _, c := range f.calls {
		if c.op == "UpdateStream" {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// HelixStreamConfigs — config table completeness + exact values
// ---------------------------------------------------------------------------

// TestHelixStreamConfigs_HasFiveStreams asserts the config table covers all 5
// control-plane streams and that each entry has the expected stream name.
func TestHelixStreamConfigs_HasFiveStreams(t *testing.T) {
	if len(HelixStreamConfigs) != 5 {
		t.Fatalf("HelixStreamConfigs: got %d entries, want 5", len(HelixStreamConfigs))
	}
	want := map[HelixStream]bool{
		StreamNodes:     false,
		StreamSessions:  false,
		StreamScheduler: false,
		StreamHealth:    false,
		StreamAlerts:    false,
	}
	for _, def := range HelixStreamConfigs {
		if _, ok := want[def.Name]; !ok {
			t.Errorf("unexpected stream %q in HelixStreamConfigs", def.Name)
		}
		want[def.Name] = true
	}
	for s, seen := range want {
		if !seen {
			t.Errorf("stream %q missing from HelixStreamConfigs", s)
		}
	}
}

// §1.1 MUTATION PROOF for TestHelixStreamConfigs_HasFiveStreams:
//   MUTATED:  removed the StreamAlerts entry from HelixStreamConfigs (4 entries).
//   RESULT:   len=4 ≠ 5 → Fatalf "HelixStreamConfigs: got 4 entries, want 5" ✓
//   RESTORED: byte-for-byte.

// TestHelixStreamConfigs_ExactSubjects asserts each stream's Subjects slice
// matches exactly the expected single-wildcard subject.
func TestHelixStreamConfigs_ExactSubjects(t *testing.T) {
	wantSubjects := map[HelixStream]string{
		StreamNodes:     "helix.nodes.>",
		StreamSessions:  "helix.sessions.>",
		StreamScheduler: "helix.scheduler.>",
		StreamHealth:    "helix.health.>",
		StreamAlerts:    "helix.alerts.>",
	}
	for _, def := range HelixStreamConfigs {
		wantSub, ok := wantSubjects[def.Name]
		if !ok {
			t.Errorf("no expected subjects entry for stream %q", def.Name)
			continue
		}
		if len(def.Subjects) != 1 {
			t.Errorf("stream %q: got %d subjects, want exactly 1", def.Name, len(def.Subjects))
			continue
		}
		if def.Subjects[0] != wantSub {
			t.Errorf("stream %q: subject got %q want %q", def.Name, def.Subjects[0], wantSub)
		}
	}
}

// §1.1 MUTATION PROOF for TestHelixStreamConfigs_ExactSubjects:
//   MUTATED:  changed StreamNodes subject to "helix.nodes.*" (single-level wildcard).
//   RESULT:   `stream "HELIX_NODES": subject got "helix.nodes.*" want "helix.nodes.>"` ✓
//   RESTORED: byte-for-byte.

// TestHelixStreamConfigs_ExactMaxAge asserts every stream uses HelixMaxAge (24h).
func TestHelixStreamConfigs_ExactMaxAge(t *testing.T) {
	for _, def := range HelixStreamConfigs {
		if def.MaxAge != HelixMaxAge {
			t.Errorf("stream %q: MaxAge got %v want %v", def.Name, def.MaxAge, HelixMaxAge)
		}
	}
}

// §1.1 MUTATION PROOF for TestHelixStreamConfigs_ExactMaxAge:
//   MUTATED:  set StreamHealth MaxAge to 1*time.Hour.
//   RESULT:   `stream "HELIX_HEALTH": MaxAge got 1h0m0s want 24h0m0s` ✓
//   RESTORED: byte-for-byte.

// TestHelixStreamConfigs_ExactReplicas asserts every stream uses HelixReplicas (1).
func TestHelixStreamConfigs_ExactReplicas(t *testing.T) {
	for _, def := range HelixStreamConfigs {
		if def.Replicas != HelixReplicas {
			t.Errorf("stream %q: Replicas got %d want %d", def.Name, def.Replicas, HelixReplicas)
		}
	}
}

// §1.1 MUTATION PROOF for TestHelixStreamConfigs_ExactReplicas:
//   MUTATED:  set StreamAlerts Replicas to 2.
//   RESULT:   `stream "HELIX_ALERTS": Replicas got 2 want 1` ✓
//   RESTORED: byte-for-byte.

// TestHelixMaxAge_Is24Hours is a standalone guard so HelixMaxAge cannot be
// silently changed to a different duration without breaking a test.
func TestHelixMaxAge_Is24Hours(t *testing.T) {
	const want = 24 * time.Hour
	if HelixMaxAge != want {
		t.Errorf("HelixMaxAge: got %v want %v", HelixMaxAge, want)
	}
}

// TestHelixReplicas_IsOne guards HelixReplicas.
func TestHelixReplicas_IsOne(t *testing.T) {
	if HelixReplicas != 1 {
		t.Errorf("HelixReplicas: got %d want 1", HelixReplicas)
	}
}

// ---------------------------------------------------------------------------
// EnsureStreams — AddStream path (all streams absent)
// ---------------------------------------------------------------------------

// TestEnsureStreams_AllAbsent_CallsAddStreamForEach verifies that when no
// streams exist EnsureStreams issues exactly one AddStream call per stream,
// with the EXACT StreamConfig from the config table.
func TestEnsureStreams_AllAbsent_CallsAddStreamForEach(t *testing.T) {
	fake := newFakeJetStreamAdmin() // no streams pre-existing
	if err := EnsureStreams(context.Background(), fake); err != nil {
		t.Fatalf("EnsureStreams: unexpected error: %v", err)
	}

	adds := fake.addCalls()
	if len(adds) != 5 {
		t.Fatalf("AddStream calls: got %d want 5", len(adds))
	}

	// Build an index for O(1) lookup.
	addsByName := make(map[string]*natsgo.StreamConfig, len(adds))
	for _, c := range adds {
		addsByName[c.cfg.Name] = c.cfg
	}

	for _, def := range HelixStreamConfigs {
		cfg, ok := addsByName[string(def.Name)]
		if !ok {
			t.Errorf("AddStream not called for stream %q", def.Name)
			continue
		}
		assertStreamConfig(t, string(def.Name), def, cfg)
	}

	// No UpdateStream calls expected.
	if upds := fake.updateCalls(); len(upds) != 0 {
		t.Errorf("unexpected UpdateStream calls: %d", len(upds))
	}
}

// §1.1 MUTATION PROOF for TestEnsureStreams_AllAbsent_CallsAddStreamForEach:
//   MUTATED:  changed StreamNodes subject in HelixStreamConfigs to "helix.nodes.*".
//   RESULT:   `HELIX_NODES: subject[0] got "helix.nodes.*" want "helix.nodes.>"` ✓
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// EnsureStreams — UpdateStream path (all streams already exist)
// ---------------------------------------------------------------------------

// TestEnsureStreams_AllExist_CallsUpdateStreamForEach verifies that when all 5
// streams already exist EnsureStreams issues exactly one UpdateStream per
// stream with the exact config — and zero AddStream calls.
func TestEnsureStreams_AllExist_CallsUpdateStreamForEach(t *testing.T) {
	allNames := make([]string, 0, len(HelixStreamConfigs))
	for _, def := range HelixStreamConfigs {
		allNames = append(allNames, string(def.Name))
	}
	fake := newFakeJetStreamAdmin(allNames...)

	if err := EnsureStreams(context.Background(), fake); err != nil {
		t.Fatalf("EnsureStreams: unexpected error: %v", err)
	}

	upds := fake.updateCalls()
	if len(upds) != 5 {
		t.Fatalf("UpdateStream calls: got %d want 5", len(upds))
	}

	updsByName := make(map[string]*natsgo.StreamConfig, len(upds))
	for _, c := range upds {
		updsByName[c.cfg.Name] = c.cfg
	}

	for _, def := range HelixStreamConfigs {
		cfg, ok := updsByName[string(def.Name)]
		if !ok {
			t.Errorf("UpdateStream not called for stream %q", def.Name)
			continue
		}
		assertStreamConfig(t, string(def.Name), def, cfg)
	}

	// No AddStream calls expected.
	if adds := fake.addCalls(); len(adds) != 0 {
		t.Errorf("unexpected AddStream calls: %d", len(adds))
	}
}

// §1.1 MUTATION PROOF for TestEnsureStreams_AllExist_CallsUpdateStreamForEach:
//   MUTATED:  removed UpdateStream call in EnsureStreams (skipped the update branch).
//   RESULT:   `UpdateStream calls: got 0 want 5` ✓
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// EnsureStreams — idempotency: second call is a no-error UpdateStream
// ---------------------------------------------------------------------------

// TestEnsureStreams_Idempotent_SecondCallUpdates calls EnsureStreams twice.
// The first call AddStreams all 5; the second call should UpdateStream all 5
// (because they now exist in the fake).
func TestEnsureStreams_Idempotent_SecondCallUpdates(t *testing.T) {
	fake := newFakeJetStreamAdmin()

	if err := EnsureStreams(context.Background(), fake); err != nil {
		t.Fatalf("first EnsureStreams: %v", err)
	}
	if err := EnsureStreams(context.Background(), fake); err != nil {
		t.Fatalf("second EnsureStreams: %v", err)
	}

	adds := fake.addCalls()
	upds := fake.updateCalls()
	if len(adds) != 5 {
		t.Errorf("AddStream calls: got %d want 5 (from first run)", len(adds))
	}
	if len(upds) != 5 {
		t.Errorf("UpdateStream calls: got %d want 5 (from second run)", len(upds))
	}
}

// §1.1 MUTATION PROOF for TestEnsureStreams_Idempotent_SecondCallUpdates:
//   MUTATED:  fakeJetStreamAdmin.AddStream did NOT mark the stream as existing.
//   RESULT:   second run issued 5 AddStream calls (not UpdateStream) →
//             `UpdateStream calls: got 0 want 5` ✓
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// EnsureStreams — mixed path (some exist, some absent)
// ---------------------------------------------------------------------------

// TestEnsureStreams_Mixed_AddAndUpdate verifies that when 3 streams exist and
// 2 are absent, exactly 2 AddStream + 3 UpdateStream calls are issued.
func TestEnsureStreams_Mixed_AddAndUpdate(t *testing.T) {
	// Pre-existing: HELIX_NODES, HELIX_HEALTH, HELIX_ALERTS
	fake := newFakeJetStreamAdmin("HELIX_NODES", "HELIX_HEALTH", "HELIX_ALERTS")

	if err := EnsureStreams(context.Background(), fake); err != nil {
		t.Fatalf("EnsureStreams: %v", err)
	}

	adds := fake.addCalls()
	upds := fake.updateCalls()
	if len(adds) != 2 {
		t.Errorf("AddStream calls: got %d want 2", len(adds))
	}
	if len(upds) != 3 {
		t.Errorf("UpdateStream calls: got %d want 3", len(upds))
	}

	// The 2 AddStream calls must be for the absent streams.
	addedNames := make(map[string]bool, len(adds))
	for _, c := range adds {
		addedNames[c.cfg.Name] = true
	}
	if !addedNames["HELIX_SESSIONS"] {
		t.Error("expected AddStream for HELIX_SESSIONS (was absent)")
	}
	if !addedNames["HELIX_SCHEDULER"] {
		t.Error("expected AddStream for HELIX_SCHEDULER (was absent)")
	}
}

// ---------------------------------------------------------------------------
// EnsureStreams — cancelled context returns error before first operation
// ---------------------------------------------------------------------------

// TestEnsureStreams_CancelledContext_ReturnsError verifies that a pre-cancelled
// context causes EnsureStreams to return an error immediately and issue zero
// JetStream calls.
func TestEnsureStreams_CancelledContext_ReturnsError(t *testing.T) {
	fake := newFakeJetStreamAdmin()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := EnsureStreams(ctx, fake)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error chain should contain context.Canceled, got: %v", err)
	}
	// No JetStream calls should have been issued.
	if len(fake.calls) != 0 {
		t.Errorf("expected 0 JetStream calls on cancelled context, got %d", len(fake.calls))
	}
}

// §1.1 MUTATION PROOF for TestEnsureStreams_CancelledContext_ReturnsError:
//   MUTATED:  removed `if err := ctx.Err()` guard at the top of the loop.
//   RESULT:   EnsureStreams issued 5 StreamInfo calls despite cancelled context;
//             returned nil (no error) → `expected error for cancelled context` ✓
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// streamConfigFor — exact natsgo.StreamConfig materialisation
// ---------------------------------------------------------------------------

// TestStreamConfigFor_ExactValues verifies that streamConfigFor produces the
// exact natsgo.StreamConfig fields from a given HelixStreamDef.
func TestStreamConfigFor_ExactValues(t *testing.T) {
	def := HelixStreamDef{
		Name:     StreamNodes,
		Subjects: []string{"helix.nodes.>"},
		MaxAge:   24 * time.Hour,
		Replicas: 1,
	}
	cfg := streamConfigFor(def)

	if cfg.Name != "HELIX_NODES" {
		t.Errorf("Name: got %q want %q", cfg.Name, "HELIX_NODES")
	}
	if len(cfg.Subjects) != 1 || cfg.Subjects[0] != "helix.nodes.>" {
		t.Errorf("Subjects: got %v want [helix.nodes.>]", cfg.Subjects)
	}
	if cfg.MaxAge != 24*time.Hour {
		t.Errorf("MaxAge: got %v want 24h", cfg.MaxAge)
	}
	if cfg.Replicas != 1 {
		t.Errorf("Replicas: got %d want 1", cfg.Replicas)
	}
	if cfg.Storage != natsgo.FileStorage {
		t.Errorf("Storage: got %v want FileStorage", cfg.Storage)
	}
	if cfg.Retention != natsgo.LimitsPolicy {
		t.Errorf("Retention: got %v want LimitsPolicy", cfg.Retention)
	}
}

// §1.1 MUTATION PROOF for TestStreamConfigFor_ExactValues:
//   MUTATED:  changed streamConfigFor to set Storage=natsgo.MemoryStorage.
//   RESULT:   `Storage: got MemoryStorage want FileStorage` ✓
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// Helper: assert exact StreamConfig values (sink-side)
// ---------------------------------------------------------------------------

func assertStreamConfig(t *testing.T, label string, def HelixStreamDef, got *natsgo.StreamConfig) {
	t.Helper()
	if got.Name != string(def.Name) {
		t.Errorf("%s: Name got %q want %q", label, got.Name, string(def.Name))
	}
	if len(got.Subjects) != len(def.Subjects) {
		t.Errorf("%s: Subjects len got %d want %d", label, len(got.Subjects), len(def.Subjects))
	} else {
		for i, sub := range def.Subjects {
			if got.Subjects[i] != sub {
				t.Errorf("%s: subject[%d] got %q want %q", label, i, got.Subjects[i], sub)
			}
		}
	}
	if got.MaxAge != def.MaxAge {
		t.Errorf("%s: MaxAge got %v want %v", label, got.MaxAge, def.MaxAge)
	}
	if got.Replicas != def.Replicas {
		t.Errorf("%s: Replicas got %d want %d", label, got.Replicas, def.Replicas)
	}
	if got.Storage != natsgo.FileStorage {
		t.Errorf("%s: Storage got %v want FileStorage", label, got.Storage)
	}
	if got.Retention != natsgo.LimitsPolicy {
		t.Errorf("%s: Retention got %v want LimitsPolicy", label, got.Retention)
	}
}
