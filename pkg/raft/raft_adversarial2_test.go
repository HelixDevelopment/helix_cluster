package raft

// Adversarial SECOND-PASS probes for pkg/raft (deeper than raft_adversarial_test.go).
//
// Scope discipline (same three-way model as the first pass): the consensus core
// — election/vote/term/commit safety — is DELEGATED to github.com/hashicorp/raft
// and is pinned (not re-derived) elsewhere. THIS file targets the surface pkg/raft
// actually OWNS and that the first pass under-covered:
//
//   - Command.Encode / FSM decode round-trip FIDELITY for the value domain the
//     type promises (a general string KV store: "locks, config, leases…"). The
//     known latent: encoding/json SILENTLY rewrites invalid-UTF-8 string bytes to
//     U+FFFD, so a value written through Encode→log→Apply comes back CORRUPTED
//     while every layer reports success. That is a real silent-data-loss bug for
//     the byte domain a Go string can carry; the fix makes Encode reject it.
//   - FSM apply DETERMINISM: two INDEPENDENT FSMs fed the IDENTICAL ordered log
//     must reach BYTE-IDENTICAL state (the property that makes log-matching
//     meaningful and prevents replica divergence / split-brain).
//   - FSM snapshot/restore round-trip EXACTNESS: state after Restore(Snapshot)
//     must equal state before, for the full value domain (no lossy compaction).
//   - Apply atomicity on a decode failure: a corrupt log entry must not advance
//     applied or mutate the store.
//
// Every probe asserts the SINK (stored key/value bytes), is deterministic, and is
// non-blocking (no real cluster / no timing assumptions in this file — these are
// pure-FSM probes, which is exactly where the owned logic lives).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"unicode/utf8"

	hraft "github.com/hashicorp/raft"
)

// applyRaw feeds raw bytes straight into the FSM as a committed log entry and
// returns the FSM response (nil on success, error value on a rejected command).
func applyRaw(f *KVFSM, data []byte) interface{} {
	return f.Apply(&hraft.Log{Data: data})
}

// TestAdversarial2_Encode_RejectsInvalidUTF8_NoSilentCorruption is the load-bearing
// probe + the mutation target for the REAL bug.
//
// BUG (silent data loss): Command.Value/Key are Go strings (arbitrary bytes), and
// the type advertises a general KV store. encoding/json, however, does NOT error
// on invalid UTF-8 — it substitutes U+FFFD. So historically Encode() returned a
// nil error for a value like {0x01,0xff} and Apply stored {0x01,0xEF,0xBF,0xBD}:
// a committed write that SUCCEEDED everywhere yet LOST the original bytes on every
// replica. This test proves Encode now SURFACES the problem (honest error) instead
// of silently corrupting, and that NO mangled value reaches the store.
//
// MUTATION PROOF: revert Encode to `return json.Marshal(c)` and this test FAILS at
// the "expected Encode to reject" assertion (Encode returns nil err) and, via the
// fallback path, at the byte-fidelity assertion (stored != original). With the fix
// it PASSES. (Verified by the agent: without-fix `--- FAIL`, with-fix `ok`.)
func TestAdversarial2_Encode_RejectsInvalidUTF8_NoSilentCorruption(t *testing.T) {
	t.Parallel()

	// Lone continuation / invalid lead bytes that json.Marshal rewrites to U+FFFD.
	badValues := []string{
		string([]byte{0xff}),
		string([]byte{0x01, 0xff, 0x02, 0xfe}),
		string([]byte{0xed, 0xa0, 0x80}), // UTF-16 surrogate half, invalid in UTF-8
		string([]byte{0xc0, 0x80}),       // overlong NUL
	}

	for i, bad := range badValues {
		t.Run(fmt.Sprintf("value-%d", i), func(t *testing.T) {
			cmd := Command{Op: CommandSet, Key: "k", Value: bad}
			b, err := cmd.Encode()
			if err == nil {
				// The bug is live: Encode silently produced JSON. Prove sink-side
				// that the round-trip is LOSSY so the failure is unambiguous and
				// the mutation (no-fix) path fails here on byte fidelity.
				fsm := NewKVFSM()
				if resp := applyRaw(fsm, b); resp != nil {
					t.Fatalf("apply of silently-encoded bad value errored: %v", resp)
				}
				got, ok := fsm.Get("k")
				if !ok {
					t.Fatalf("key missing after apply")
				}
				if got == bad {
					t.Fatalf("unexpected: invalid-UTF-8 value survived json round-trip intact")
				}
				t.Fatalf("SILENT DATA LOSS: Encode accepted invalid-UTF-8 value and the "+
					"committed round-trip CORRUPTED it: in=% x out=% x (Encode must reject)",
					[]byte(bad), []byte(got))
			}
			// Fixed behavior: honest, surfaced error — no corrupted bytes anywhere.
			if utf8.ValidString(bad) {
				t.Fatalf("test bug: value % x is actually valid UTF-8", []byte(bad))
			}
		})
	}

	// Key on the invalid path must be rejected too (keys also flow through JSON).
	if _, err := (Command{Op: CommandSet, Key: string([]byte{0xff}), Value: "v"}).Encode(); err == nil {
		t.Fatalf("Encode accepted invalid-UTF-8 KEY (would corrupt on commit)")
	}
}

// TestAdversarial2_Encode_ValidValuesRoundTripByteExact guards the OTHER side of
// the fix: every value domain a real caller uses (config text, unicode, control
// chars, embedded NUL, the full 0x00..0x7f ASCII range, large blobs) must still
// encode AND round-trip through the FSM BYTE-IDENTICALLY. This pins that the
// UTF-8 guard rejects ONLY genuinely-invalid bytes and never a valid value.
func TestAdversarial2_Encode_ValidValuesRoundTripByteExact(t *testing.T) {
	t.Parallel()

	// Every ASCII byte including NUL and DEL (all valid UTF-8).
	allASCII := make([]byte, 0, 128)
	for c := 0; c < 128; c++ {
		allASCII = append(allASCII, byte(c))
	}

	values := []string{
		"",
		"v",
		"10.0.0.7",
		"日本語-🚀",
		"a\nb\tc\r\x00d", // control chars + embedded NUL, all valid UTF-8
		string(allASCII),
		string(bytes.Repeat([]byte("λ"), 4096)), // large multi-byte blob
	}
	for i, v := range values {
		cmd := Command{Op: CommandSet, Key: fmt.Sprintf("key-%d", i), Value: v}
		b, err := cmd.Encode()
		if err != nil {
			t.Fatalf("Encode rejected a VALID value %q: %v", v, err)
		}
		fsm := NewKVFSM()
		if resp := applyRaw(fsm, b); resp != nil {
			t.Fatalf("apply: %v", resp)
		}
		got, ok := fsm.Get(fmt.Sprintf("key-%d", i))
		if !ok || got != v {
			t.Fatalf("ROUND-TRIP NOT BYTE-EXACT: in=% x out=% x ok=%v", []byte(v), []byte(got), ok)
		}
	}
}

// TestAdversarial2_DeterministicApply_IndependentFSMsConverge proves the property
// that makes Raft log-matching safe at the application layer: feeding the SAME
// ordered sequence of committed log bytes to two INDEPENDENT FSMs yields
// BYTE-IDENTICAL state. A non-deterministic Apply (e.g. order-dependent default,
// map-mutation that depended on iteration order, or a value transform) would make
// two replicas diverge from the same log → split-brain. We assert full equality of
// the resulting stores AND the applied counters.
func TestAdversarial2_DeterministicApply_IndependentFSMsConverge(t *testing.T) {
	t.Parallel()

	type entry struct {
		op   CommandType
		k, v string
	}
	// A log that exercises set, overwrite, delete, delete-of-absent, re-set.
	log := []entry{
		{CommandSet, "a", "1"},
		{CommandSet, "b", "2"},
		{CommandSet, "a", "1b"}, // overwrite
		{CommandDelete, "b", ""},
		{CommandDelete, "z", ""}, // delete absent (no-op)
		{CommandSet, "c", "日本語"},
		{CommandSet, "b", "back"}, // re-create after delete
		{CommandSet, "a", ""},     // overwrite with empty
	}

	build := func() *KVFSM {
		f := NewKVFSM()
		for _, e := range log {
			data, err := Command{Op: e.op, Key: e.k, Value: e.v}.Encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if resp := applyRaw(f, data); resp != nil {
				t.Fatalf("apply %v: %v", e, resp)
			}
		}
		return f
	}

	f1 := build()
	f2 := build()

	// Sink-side: identical applied counts and identical full key/value snapshots.
	if f1.AppliedCount() != f2.AppliedCount() {
		t.Fatalf("DETERMINISM VIOLATED: applied counts differ %d vs %d", f1.AppliedCount(), f2.AppliedCount())
	}
	s1 := snapshotState(t, f1)
	s2 := snapshotState(t, f2)
	if len(s1) != len(s2) {
		t.Fatalf("DETERMINISM VIOLATED: state sizes differ %d vs %d", len(s1), len(s2))
	}
	for k, v := range s1 {
		if v2, ok := s2[k]; !ok || v2 != v {
			t.Fatalf("DETERMINISM VIOLATED: key %q = %q vs %q ok=%v", k, v, v2, ok)
		}
	}
	// Expected final state, computed independently.
	want := map[string]string{"a": "", "b": "back", "c": "日本語"}
	if len(s1) != len(want) {
		t.Fatalf("unexpected final size %d want %d (%v)", len(s1), len(want), s1)
	}
	for k, v := range want {
		if s1[k] != v {
			t.Fatalf("final state wrong: %q=%q want %q", k, s1[k], v)
		}
	}
}

// TestAdversarial2_SnapshotRestore_ByteExactForValueDomain proves snapshot →
// restore is LOSSLESS for the full value domain: take a Snapshot of a populated
// FSM, Persist it, Restore into a FRESH FSM, and assert the restored store equals
// the original EXACTLY (same keys, same value bytes, same size). A lossy snapshot
// (e.g. dropping empty values, or transforming bytes) would surface as a mismatch
// here — that would corrupt a late-joiner brought up via InstallSnapshot.
func TestAdversarial2_SnapshotRestore_ByteExactForValueDomain(t *testing.T) {
	t.Parallel()

	orig := NewKVFSM()
	domain := map[string]string{
		"empty":    "",
		"text":     "hello",
		"unicode":  "日本語-🚀",
		"control":  "a\nb\tc\x00d",
		"ip":       "10.0.0.7",
		"big":      string(bytes.Repeat([]byte("x"), 8192)),
		"emptyKey": "", // ensure empty-value keys survive (omitempty is per-Command, not per-snapshot)
	}
	for k, v := range domain {
		data, err := Command{Op: CommandSet, Key: k, Value: v}.Encode()
		if err != nil {
			t.Fatalf("encode %q: %v", k, err)
		}
		if resp := applyRaw(orig, data); resp != nil {
			t.Fatalf("apply %q: %v", k, resp)
		}
	}

	// Persist the snapshot to an in-memory sink.
	snap, err := orig.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	sink := newMemSink()
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	snap.Release()
	if !sink.closed {
		t.Fatalf("snapshot sink was not Closed by Persist")
	}

	// Restore into a fresh FSM from the persisted bytes.
	restored := NewKVFSM()
	if err := restored.Restore(io.NopCloser(bytes.NewReader(sink.buf.Bytes()))); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// SINK-SIDE byte-exact equality, both directions.
	if got := restored.Len(); got != len(domain) {
		t.Fatalf("LOSSY SNAPSHOT: restored has %d keys, want %d", got, len(domain))
	}
	for k, v := range domain {
		got, ok := restored.Get(k)
		if !ok {
			t.Fatalf("LOSSY SNAPSHOT: key %q LOST after restore", k)
		}
		if got != v {
			t.Fatalf("LOSSY SNAPSHOT: key %q value changed: in=% x out=% x", k, []byte(v), []byte(got))
		}
	}
}

// TestAdversarial2_Apply_CorruptEntry_NoMutationNoCount proves apply atomicity on
// a decode failure: a committed log entry whose bytes are NOT valid JSON must be
// rejected (error response) and must NOT advance the applied counter NOR mutate
// the store. A counter that ticked or a partial mutation would desync replicas
// (one node that happened to apply more) and break the AppliedCount sink signal.
func TestAdversarial2_Apply_CorruptEntry_NoMutationNoCount(t *testing.T) {
	t.Parallel()

	f := NewKVFSM()
	// Seed one good entry so we have a known baseline.
	good, _ := Command{Op: CommandSet, Key: "seed", Value: "ok"}.Encode()
	if resp := applyRaw(f, good); resp != nil {
		t.Fatalf("seed apply: %v", resp)
	}
	baseCount := f.AppliedCount()
	baseLen := f.Len()

	// Garbage that is not valid JSON at all.
	corrupt := [][]byte{
		[]byte("{not json"),
		{0x00, 0x01, 0x02},
		[]byte(`{"op":`),
	}
	for _, c := range corrupt {
		resp := applyRaw(f, c)
		if resp == nil {
			t.Fatalf("CORRUPT ENTRY ACCEPTED: apply(% x) returned nil (expected decode error)", c)
		}
		if _, isErr := resp.(error); !isErr {
			t.Fatalf("apply of corrupt entry returned non-error response %T", resp)
		}
	}

	// Apply decodes BEFORE touching the applied counter or the store, returning
	// early on a decode error. So a corrupt entry must NOT advance AppliedCount —
	// it stays at the baseline. (This is the deterministic, replica-safe contract:
	// every replica decodes the same committed bytes and fails identically, but a
	// rejected entry leaves the FSM's observable state untouched.) We pin both the
	// counter AND the store here.
	if got := f.AppliedCount(); got != baseCount {
		t.Fatalf("CORRUPT ENTRY ADVANCED AppliedCount: got %d want %d (baseline)", got, baseCount)
	}
	if got := f.Len(); got != baseLen {
		t.Fatalf("CORRUPT ENTRY MUTATED STORE: len=%d want %d", got, baseLen)
	}
	if v, ok := f.Get("seed"); !ok || v != "ok" {
		t.Fatalf("corrupt entry disturbed existing key: seed=%q ok=%v", v, ok)
	}
}

// --- helpers ---

// snapshotState extracts the full key/value map from an FSM via its Snapshot,
// giving a sink-side view for equality comparison.
func snapshotState(t *testing.T, f *KVFSM) map[string]string {
	t.Helper()
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer snap.Release()
	sink := newMemSink()
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	var state map[string]string
	if err := json.Unmarshal(sink.buf.Bytes(), &state); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if state == nil {
		state = map[string]string{}
	}
	return state
}

// memSink is an in-memory hraft.SnapshotSink for exercising Persist without disk.
type memSink struct {
	buf      bytes.Buffer
	closed   bool
	canceled bool
}

func newMemSink() *memSink { return &memSink{} }

func (m *memSink) Write(p []byte) (int, error) { return m.buf.Write(p) }
func (m *memSink) Close() error                { m.closed = true; return nil }
func (m *memSink) ID() string                  { return "mem-snapshot" }
func (m *memSink) Cancel() error               { m.canceled = true; return nil }
