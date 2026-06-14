package checkpointmerge_test

// Adversarial / mutation-verified contract suite for pkg/checkpoint_merge.
//
// Focus mirrors the bug classes found tonight in the sibling deserialize/merge
// packages (offlinesync decompression-bomb + equal-version no-tiebreak
// divergence; antientropy equal-version split-brain):
//
//   - MERGE DETERMINISM + COMPLETENESS (centerpiece): conflicting/overlapping
//     keys must converge regardless of apply order, with no state loss or
//     duplication, and equal-version conflicts must be broken by a deterministic
//     TOTAL ORDER (the missing-tiebreak class).
//   - HOSTILE APPLY / DoS (centerpiece): malformed / truncated / random /
//     crafted-length checkpoint bytes must yield an error, never a panic and
//     never an unbounded allocation.
//   - MIGRATE FIDELITY: every field carried correctly across the export->merge
//     pipeline; an unsupported version is REJECTED, not silently accepted as a
//     zero state.
//   - IDEMPOTENCE and EDGES (empty / nil / single key).
//
// FINDING (see report): no real bug. These tests pin the correct contract;
// each was confirmed to FAIL under a canonical injected mutation.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"time"

	checkpointmerge "github.com/HelixDevelopment/helix_cluster/pkg/checkpoint_merge"
	"github.com/HelixDevelopment/helix_cluster/pkg/session"
)

// ---- helpers ---------------------------------------------------------------

func advWindow(id, name, layout string, version uint64, panes map[string]*session.CRDTPane) *session.CRDTWindow {
	if panes == nil {
		panes = map[string]*session.CRDTPane{}
	}
	return &session.CRDTWindow{
		ID:      id,
		Name:    name,
		Layout:  layout,
		Panes:   panes,
		Version: version,
	}
}

func advPane(id, cmd, dir string, version uint64) *session.CRDTPane {
	return &session.CRDTPane{ID: id, Command: cmd, WorkingDir: dir, Version: version}
}

func mustCP(t *testing.T, s *session.CRDTSessionState) *session.Checkpoint {
	t.Helper()
	cp, err := session.NewCRDTCheckpoint(s)
	if err != nil {
		t.Fatalf("NewCRDTCheckpoint: %v", err)
	}
	return cp
}

// ---- MERGE DETERMINISM + COMPLETENESS (centerpiece) ------------------------

// TestAdv_EqualVersion_TotalOrder_WindowScalars pins the deterministic
// tiebreak when two replicas collide on the SAME window ID at the SAME Version
// with DIFFERENT scalar content. The winner MUST be the lexicographically
// greater windowContentKey (Name+"\x00"+Layout), independent of apply order.
//
// This is the exact class that diverged in antientropy/offlinesync (equal
// version, no tiebreak -> order-dependent winner). Here we assert a fixed
// winner in BOTH directions.
func TestAdv_EqualVersion_TotalOrder_WindowScalars(t *testing.T) {
	// key("zeta","z") > key("alpha","a") lexicographically -> "zeta" must win.
	A := session.NewCRDTSessionState()
	A.UpdateWindow(advWindow("w1", "zeta", "z-layout", 7, nil))
	B := session.NewCRDTSessionState()
	B.UpdateWindow(advWindow("w1", "alpha", "a-layout", 7, nil))

	cpA, cpB := mustCP(t, A), mustCP(t, B)

	mBA, err := checkpointmerge.Apply(B, cpA)
	if err != nil {
		t.Fatalf("Apply(B,cpA): %v", err)
	}
	mAB, err := checkpointmerge.Apply(A, cpB)
	if err != nil {
		t.Fatalf("Apply(A,cpB): %v", err)
	}

	// Same fixed winner in BOTH directions (order independence).
	for name, m := range map[string]*session.CRDTSessionState{"Apply(B,cpA)": mBA, "Apply(A,cpB)": mAB} {
		w, ok := m.GetWindow("w1")
		if !ok {
			t.Fatalf("%s: window w1 missing", name)
		}
		if w.Name != "zeta" || w.Layout != "z-layout" {
			t.Errorf("%s: equal-version tiebreak winner = (%q,%q), want (zeta,z-layout)", name, w.Name, w.Layout)
		}
	}
	if mBA.Fingerprint() != mAB.Fingerprint() {
		t.Errorf("equal-version window scalar merge is ORDER-DEPENDENT (split-brain):\n  BA=%s\n  AB=%s",
			mBA.Fingerprint(), mAB.Fingerprint())
	}
}

// TestAdv_EqualVersion_TotalOrder_PaneContent pins the same deterministic
// total order one level down: two replicas with the same window+pane ID at the
// same pane Version but different pane content must resolve to a fixed winner
// (greater paneContentKey) regardless of order.
func TestAdv_EqualVersion_TotalOrder_PaneContent(t *testing.T) {
	A := session.NewCRDTSessionState()
	A.UpdateWindow(advWindow("w1", "w", "l", 1, map[string]*session.CRDTPane{
		"p1": advPane("p1", "zsh", "/home", 4), // key "zsh\x00/home\x000"
	}))
	B := session.NewCRDTSessionState()
	B.UpdateWindow(advWindow("w1", "w", "l", 1, map[string]*session.CRDTPane{
		"p1": advPane("p1", "bash", "/tmp", 4), // key "bash\x00/tmp\x000"
	}))

	cpA, cpB := mustCP(t, A), mustCP(t, B)
	mBA, err := checkpointmerge.Apply(B, cpA)
	if err != nil {
		t.Fatalf("Apply(B,cpA): %v", err)
	}
	mAB, err := checkpointmerge.Apply(A, cpB)
	if err != nil {
		t.Fatalf("Apply(A,cpB): %v", err)
	}

	// "zsh..." > "bash..." -> zsh wins deterministically both ways.
	for name, m := range map[string]*session.CRDTSessionState{"BA": mBA, "AB": mAB} {
		p, ok := m.GetPane("w1", "p1")
		if !ok {
			t.Fatalf("%s: pane p1 missing", name)
		}
		if p.Command != "zsh" || p.WorkingDir != "/home" {
			t.Errorf("%s: equal-version pane tiebreak = (%q,%q), want (zsh,/home)", name, p.Command, p.WorkingDir)
		}
	}
	if mBA.Fingerprint() != mAB.Fingerprint() {
		t.Errorf("equal-version pane merge is ORDER-DEPENDENT:\n  BA=%s\n  AB=%s", mBA.Fingerprint(), mAB.Fingerprint())
	}
}

// TestAdv_Merge_Completeness_NoLossNoDup verifies that a merge of two replicas
// with overlapping AND disjoint windows/panes preserves the UNION: every key
// from either side survives exactly once (no loss, no duplication), and the
// result is order-independent.
func TestAdv_Merge_Completeness_NoLossNoDup(t *testing.T) {
	A := session.NewCRDTSessionState()
	A.UpdateWindow(advWindow("shared", "a-name", "a-lay", 5, map[string]*session.CRDTPane{
		"sp":    advPane("sp", "cmdA", "/a", 5), // shared pane, A higher? equal ver below
		"onlyA": advPane("onlyA", "ca", "/a", 2),
	}))
	A.UpdateWindow(advWindow("wA", "only-a-win", "la", 9, nil))

	B := session.NewCRDTSessionState()
	B.UpdateWindow(advWindow("shared", "b-name", "b-lay", 5, map[string]*session.CRDTPane{
		"sp":    advPane("sp", "cmdB", "/b", 5),
		"onlyB": advPane("onlyB", "cb", "/b", 2),
	}))
	B.UpdateWindow(advWindow("wB", "only-b-win", "lb", 1, nil))

	cpA, cpB := mustCP(t, A), mustCP(t, B)
	mBA, err := checkpointmerge.Apply(B, cpA)
	if err != nil {
		t.Fatalf("Apply(B,cpA): %v", err)
	}
	mAB, err := checkpointmerge.Apply(A, cpB)
	if err != nil {
		t.Fatalf("Apply(A,cpB): %v", err)
	}

	if mBA.Fingerprint() != mAB.Fingerprint() {
		t.Fatalf("union merge order-dependent:\n  BA=%s\n  AB=%s", mBA.Fingerprint(), mAB.Fingerprint())
	}

	// All three windows present.
	for _, wid := range []string{"shared", "wA", "wB"} {
		if _, ok := mBA.GetWindow(wid); !ok {
			t.Errorf("completeness: window %q lost in merge", wid)
		}
	}
	// shared window must contain the union of panes: sp, onlyA, onlyB (no dup).
	w, _ := mBA.GetWindow("shared")
	if len(w.Panes) != 3 {
		t.Errorf("pane union: got %d panes, want 3 (sp,onlyA,onlyB)", len(w.Panes))
	}
	for _, pid := range []string{"sp", "onlyA", "onlyB"} {
		if _, ok := w.Panes[pid]; !ok {
			t.Errorf("completeness: pane %q lost", pid)
		}
	}
}

// TestAdv_Merge_Associative drives three replicas through Apply in two
// different groupings/orders (all colliding at equal version) and asserts the
// fingerprints match — associativity at the orchestration level.
func TestAdv_Merge_Associative(t *testing.T) {
	mk := func(name, layout string, v uint64) *session.CRDTSessionState {
		s := session.NewCRDTSessionState()
		s.UpdateWindow(advWindow("w1", name, layout, v, nil))
		return s
	}
	A, B, C := mk("alpha", "la", 7), mk("mike", "lm", 7), mk("zeta", "lz", 7)
	cpA, cpB, cpC := mustCP(t, A), mustCP(t, B), mustCP(t, C)

	// (A <- B) <- C
	ab, err := checkpointmerge.Apply(A, cpB)
	if err != nil {
		t.Fatal(err)
	}
	left, err := checkpointmerge.Apply(ab, cpC)
	if err != nil {
		t.Fatal(err)
	}

	// (C <- B) <- A  — different grouping and order.
	cb, err := checkpointmerge.Apply(C, cpB)
	if err != nil {
		t.Fatal(err)
	}
	right, err := checkpointmerge.Apply(cb, cpA)
	if err != nil {
		t.Fatal(err)
	}

	if left.Fingerprint() != right.Fingerprint() {
		t.Errorf("associativity/order divergence:\n  left =%s\n  right=%s", left.Fingerprint(), right.Fingerprint())
	}
	// Deterministic winner across all three at equal version: "zeta" (greatest key).
	gw, _ := left.GetWindow("w1")
	if gw.Name != "zeta" {
		t.Errorf("3-way equal-version winner = %q, want zeta", gw.Name)
	}
}

// ---- HOSTILE APPLY / DoS (centerpiece) -------------------------------------

// TestAdv_HostileApply_NoPanic_NoOverAlloc feeds Apply a battery of
// malformed/truncated/random/crafted-length checkpoint payloads (each behind a
// VALID checksum so the gob decoder is actually reached) and asserts:
//   - never a panic,
//   - always a sentinel error (the bytes are not a real CRDT state),
//   - bounded allocation / bounded time (no decompression/length bomb).
//
// The checksum is recomputed for each payload to model an attacker who fully
// controls the checkpoint bytes — the checksum gate alone is NOT what protects
// us here; the gob decoder's own length-sanity is. This test fails loudly if a
// future change lets a crafted length prefix pre-allocate.
func TestAdv_HostileApply_NoPanic_NoOverAlloc(t *testing.T) {
	local := session.NewCRDTSessionState()
	local.UpdateWindow(advWindow("w1", "local", "tiled", 3, map[string]*session.CRDTPane{
		"p1": advPane("p1", "bash", "/tmp", 3),
	}))

	// Build a legit payload to derive structurally-plausible-but-corrupt variants.
	legit := mustCP(t, func() *session.CRDTSessionState {
		s := session.NewCRDTSessionState()
		s.UpdateWindow(advWindow("ws", "src", "even-h", 7, nil))
		return s
	}())

	craft := func(data []byte) *session.Checkpoint {
		return &session.Checkpoint{
			Magic:    [4]byte{'H', 'X', 'C', 'K'},
			Version:  session.CRDTCheckpointVersion,
			Checksum: legitChecksum(data), // valid checksum: forces the gob path
			Data:     data,
		}
	}

	payloads := map[string][]byte{
		"empty":            {},
		"single-zero":      {0x00},
		"truncated-gob":    {0x0e, 0x01, 0x02, 0xff, 0xfe},
		"all-0xff-8":       bytes.Repeat([]byte{0xff}, 8),
		"all-0xff-64":      bytes.Repeat([]byte{0xff}, 64),
		"legit-first-flip": flipFirst(legit.Data),
		"legit-truncated":  legit.Data[:len(legit.Data)/2],
		"legit-head-only":  legit.Data[:min(4, len(legit.Data))],
		// Crafted huge map/slice length prefixes (classic gob length bomb shapes).
		"huge-count-f8":  {0xf8, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"huge-count-mid": append(append([]byte{0x2a, 0x7f, 0x03}, 0xf8, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff), 0x00),
		"random-256":     randomBytes(256, 1),
		"random-4k":      randomBytes(4096, 2),
	}

	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	for name, data := range payloads {
		name, data := name, data
		t.Run(name, func(t *testing.T) {
			cp := craft(data)
			done := make(chan struct{})
			var perr error
			go func() {
				defer func() {
					if r := recover(); r != nil {
						perr = errorf("PANIC: %v", r)
					}
					close(done)
				}()
				_, perr = checkpointmerge.Apply(local, cp)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("hostile payload %q: Apply HUNG (>10s) — DoS", name)
			}
			if perr != nil && isPanic(perr) {
				t.Fatalf("hostile payload %q: Apply PANICKED: %v", name, perr)
			}
			// All these payloads are NOT a real CRDT state -> Apply must error.
			if perr == nil {
				t.Errorf("hostile payload %q: Apply unexpectedly succeeded (expected sentinel error)", name)
			}
		})
	}

	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	// Bounded allocation: a handful of small payloads must not balloon memory.
	// 256 MiB is a generous ceiling that a real length-bomb would blow past.
	const ceiling = int64(256) * 1024 * 1024
	if delta := int64(m1.TotalAlloc - m0.TotalAlloc); delta > ceiling {
		t.Errorf("hostile apply over-allocated: %d bytes total (> %d ceiling) — possible length-bomb", delta, ceiling)
	}
}

// TestAdv_CorruptChecksum_Sentinel: a real checkpoint with a flipped payload
// byte (checksum left intact) must fail with the CHECKSUM sentinel — proving
// the integrity gate rejects tampering before decode.
func TestAdv_CorruptChecksum_Sentinel(t *testing.T) {
	s := session.NewCRDTSessionState()
	s.UpdateWindow(advWindow("w1", "x", "y", 1, nil))
	cp := mustCP(t, s)
	if len(cp.Data) == 0 {
		t.Fatal("empty checkpoint data")
	}
	cp.Data[0] ^= 0xFF // flip payload but keep the recorded checksum
	_, err := checkpointmerge.Apply(session.NewCRDTSessionState(), cp)
	if !errors.Is(err, session.ErrCheckpointChecksum) {
		t.Errorf("flipped payload: want ErrCheckpointChecksum, got %v", err)
	}
}

// TestAdv_BadMagic_Sentinel: a wrong magic must be rejected before anything
// else, with the magic sentinel.
func TestAdv_BadMagic_Sentinel(t *testing.T) {
	s := session.NewCRDTSessionState()
	s.UpdateWindow(advWindow("w1", "x", "y", 1, nil))
	cp := mustCP(t, s)
	cp.Magic = [4]byte{'X', 'X', 'X', 'X'}
	_, err := checkpointmerge.Apply(session.NewCRDTSessionState(), cp)
	if !errors.Is(err, session.ErrCheckpointMagic) {
		t.Errorf("bad magic: want ErrCheckpointMagic, got %v", err)
	}
}

// ---- MIGRATE FIDELITY ------------------------------------------------------

// TestAdv_Migrate_FieldFidelity verifies that every CRDT field (window scalars
// + per-pane scalars) survives the full export->merge pipeline when the source
// strictly dominates (higher versions), so the merged result must equal the
// source content for that window.
func TestAdv_Migrate_FieldFidelity(t *testing.T) {
	source := session.NewCRDTSessionState()
	source.UpdateWindow(advWindow("w1", "src-name", "src-layout", 100, map[string]*session.CRDTPane{
		"p1": {ID: "p1", Command: "vim", WorkingDir: "/work", Status: session.PaneFrozen, Version: 100},
	}))

	local := session.NewCRDTSessionState()
	local.UpdateWindow(advWindow("w1", "old", "old-lay", 1, map[string]*session.CRDTPane{
		"p1": advPane("p1", "old-cmd", "/old", 1),
	}))

	merged, err := checkpointmerge.Migrate(source, local)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	w, ok := merged.GetWindow("w1")
	if !ok {
		t.Fatal("w1 missing after migrate")
	}
	if w.Name != "src-name" || w.Layout != "src-layout" || w.Version != 100 {
		t.Errorf("window fidelity: got (%q,%q,v%d), want (src-name,src-layout,v100)", w.Name, w.Layout, w.Version)
	}
	p, ok := merged.GetPane("w1", "p1")
	if !ok {
		t.Fatal("pane p1 missing after migrate")
	}
	if p.Command != "vim" || p.WorkingDir != "/work" || p.Status != session.PaneFrozen || p.Version != 100 {
		t.Errorf("pane fidelity: got (cmd=%q,dir=%q,st=%d,v%d), want (vim,/work,%d,100)",
			p.Command, p.WorkingDir, int(p.Status), p.Version, int(session.PaneFrozen))
	}
}

// TestAdv_Migrate_WrongVersionRejected: a version-1 SESSION checkpoint must be
// REJECTED by Apply (which only accepts CRDT version 2), never silently
// accepted as a zero state that wipes local. This is the "unknown version
// silently accepted" Migrate-corruption class.
func TestAdv_Migrate_WrongVersionRejected(t *testing.T) {
	v1 := mustSessionCheckpoint(t)
	local := session.NewCRDTSessionState()
	local.UpdateWindow(advWindow("keep", "keep-name", "keep-lay", 9, nil))
	before := local.Fingerprint()

	_, err := checkpointmerge.Apply(local, v1)
	if err == nil {
		t.Fatal("v1 session checkpoint was silently accepted by Apply — state-corruption risk")
	}
	if !errors.Is(err, session.ErrCheckpointVersion) {
		t.Errorf("want ErrCheckpointVersion, got %v", err)
	}
	// And the local state the caller passed must be untouched (Apply clones).
	if local.Fingerprint() != before {
		t.Errorf("Apply mutated caller local state on rejected checkpoint:\n  before=%s\n  after =%s", before, local.Fingerprint())
	}
}

// ---- IDEMPOTENCE -----------------------------------------------------------

// TestAdv_Idempotent_ReapplySameCheckpoint applies the same checkpoint twice
// and asserts the second apply leaves the observable fingerprint unchanged,
// with mixed window/pane content.
func TestAdv_Idempotent_ReapplySameCheckpoint(t *testing.T) {
	s := session.NewCRDTSessionState()
	s.UpdateWindow(advWindow("w1", "name1", "lay1", 4, map[string]*session.CRDTPane{
		"p1": advPane("p1", "bash", "/a", 4),
		"p2": advPane("p2", "zsh", "/b", 2),
	}))
	s.UpdateWindow(advWindow("w2", "name2", "lay2", 6, nil))
	cp := mustCP(t, s)

	first, err := checkpointmerge.Apply(s, cp)
	if err != nil {
		t.Fatal(err)
	}
	second, err := checkpointmerge.Apply(first, cp)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Errorf("non-idempotent re-apply:\n  first =%s\n  second=%s", first.Fingerprint(), second.Fingerprint())
	}
}

// ---- EDGES -----------------------------------------------------------------

func TestAdv_Edge_EmptyCheckpointPreservesLocal(t *testing.T) {
	cp := mustCP(t, session.NewCRDTSessionState()) // no windows
	local := session.NewCRDTSessionState()
	local.UpdateWindow(advWindow("w1", "keep", "lay", 3, nil))
	before := local.Fingerprint()
	merged, err := checkpointmerge.Apply(local, cp)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Fingerprint() != before {
		t.Errorf("empty checkpoint changed state:\n  before=%s\n  after =%s", before, merged.Fingerprint())
	}
}

func TestAdv_Edge_NilInputs(t *testing.T) {
	cp := mustCP(t, session.NewCRDTSessionState())
	if _, err := checkpointmerge.Apply(nil, cp); err == nil {
		t.Error("Apply(nil, cp): expected error")
	}
	if _, err := checkpointmerge.Apply(session.NewCRDTSessionState(), nil); err == nil {
		t.Error("Apply(s, nil): expected error")
	}
	if _, err := checkpointmerge.Migrate(nil, session.NewCRDTSessionState()); err == nil {
		t.Error("Migrate(nil, local): expected error")
	}
}

func TestAdv_Edge_SingleKey_BothDirectionsConverge(t *testing.T) {
	A := session.NewCRDTSessionState()
	A.UpdateWindow(advWindow("only", "a", "la", 5, nil))
	B := session.NewCRDTSessionState()
	B.UpdateWindow(advWindow("only", "b", "lb", 8, nil)) // B strictly newer
	cpA, cpB := mustCP(t, A), mustCP(t, B)

	mBA, err := checkpointmerge.Apply(B, cpA)
	if err != nil {
		t.Fatal(err)
	}
	mAB, err := checkpointmerge.Apply(A, cpB)
	if err != nil {
		t.Fatal(err)
	}
	if mBA.Fingerprint() != mAB.Fingerprint() {
		t.Errorf("single-key convergence failed:\n  BA=%s\n  AB=%s", mBA.Fingerprint(), mAB.Fingerprint())
	}
	// B is strictly newer (v8 > v5) -> B's scalars win in both.
	w, _ := mBA.GetWindow("only")
	if w.Name != "b" || w.Version != 8 {
		t.Errorf("LWW winner = (%q,v%d), want (b,v8)", w.Name, w.Version)
	}
}

// ---- small local utilities (no external deps) ------------------------------

// legitChecksum reproduces session.checksumOf(CRDTCheckpointVersion, data) =
// sha256(big-endian uint32 version || data). Recomputing it here models an
// attacker who fully controls the checkpoint bytes: the checksum gate is NOT
// what protects Apply from a hostile payload — the gob decoder's own
// length-sanity is. By supplying a VALID checksum we force the hostile bytes
// all the way to gob.Decode.
func legitChecksum(data []byte) [32]byte {
	h := sha256.New()
	v := session.CRDTCheckpointVersion
	h.Write([]byte{
		byte((v >> 24) & 0xff),
		byte((v >> 16) & 0xff),
		byte((v >> 8) & 0xff),
		byte(v & 0xff),
	})
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func flipFirst(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	out := append([]byte{}, b...)
	out[0] ^= 0xFF
	return out
}

func randomBytes(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	r.Read(b)
	return b
}

// mustSessionCheckpoint builds a version-1 (session) checkpoint to feed Apply,
// which must reject it as the wrong version.
func mustSessionCheckpoint(t *testing.T) *session.Checkpoint {
	t.Helper()
	cp, err := session.NewCheckpoint(&session.Session{ID: "s1", Name: "n"})
	if err != nil {
		t.Fatalf("NewCheckpoint: %v", err)
	}
	return cp
}

// panicErr distinguishes a recovered panic from a normal error inside the
// hostile-apply goroutine.
type panicErr struct{ msg string }

func (e panicErr) Error() string { return e.msg }

func errorf(format string, a ...any) error { return panicErr{msg: fmt.Sprintf(format, a...)} }
func isPanic(err error) bool {
	var pe panicErr
	return errors.As(err, &pe)
}
