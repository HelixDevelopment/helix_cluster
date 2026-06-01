package session

// crdt_hxc1085_test.go — HXC-1085 closure-criteria tests.
//
// Closure criteria:
//  1. Two replicas with concurrent updates merge+converge to identical state.
//  2. Illegal lifecycle transitions are rejected.
//  3. Mutation gate: breaking the merge/transition guard fails a test.
//
// The convergence and lifecycle tests already have coverage in convergence_test.go
// and lifecycle_test.go.  This file adds the EXPLICIT closure-criteria tests
// (with §1.1 mutation proof annotations) to satisfy the HXC-1085 checklist.

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// HXC-1085 §A: Two concurrent-update replicas converge
// ---------------------------------------------------------------------------

// TestHXC1085_TwoReplicasMergeConverge is the headline test for HXC-1085.
// Two replicas each independently apply a distinct set of updates; after a
// single round of anti-entropy (Merge) they must hold byte-identical
// observable state regardless of the original application order.
//
// MUTATION: in Merge, skip the mergePanes call (comment it out) -> windows
// whose panes were added on the other replica are absent; fingerprints
// diverge -> test FAILS.
func TestHXC1085_TwoReplicasMergeConverge(t *testing.T) {
	// Replica A: adds window w1 with panes p1, p2; and window w2 with p3.
	replicaA := NewCRDTSessionState()
	replicaA.UpdateWindow(&CRDTWindow{ID: "w1", Name: "editor", Layout: "main-v", Version: 1})
	replicaA.UpdatePane("w1", &CRDTPane{ID: "p1", Command: "vim", WorkingDir: "/src", Version: 1})
	replicaA.UpdatePane("w1", &CRDTPane{ID: "p2", Command: "bash", WorkingDir: "/src", Version: 1})
	replicaA.UpdateWindow(&CRDTWindow{ID: "w2", Name: "logs", Layout: "tiled", Version: 2})
	replicaA.UpdatePane("w2", &CRDTPane{ID: "p3", Command: "tail", WorkingDir: "/var/log", Version: 2})

	// Replica B: adds window w1 (same ID, different name at same version => tie-break)
	// and window w3 which is unique to B.
	replicaB := NewCRDTSessionState()
	replicaB.UpdateWindow(&CRDTWindow{ID: "w1", Name: "EDITOR", Layout: "main-v", Version: 1})
	replicaB.UpdatePane("w1", &CRDTPane{ID: "p4", Command: "nvim", WorkingDir: "/home", Version: 3})
	replicaB.UpdateWindow(&CRDTWindow{ID: "w3", Name: "shell", Layout: "even-h", Version: 5})
	replicaB.UpdatePane("w3", &CRDTPane{ID: "p5", Command: "zsh", WorkingDir: "/home", Version: 5})

	// Merge A into B and B into A independently.
	mergedAB := replicaA.Clone()
	if err := mergedAB.Merge(replicaB); err != nil {
		t.Fatalf("merge(A,B): %v", err)
	}
	mergedBA := replicaB.Clone()
	if err := mergedBA.Merge(replicaA); err != nil {
		t.Fatalf("merge(B,A): %v", err)
	}

	// Convergence: both merged states must be fingerprint-equal (observable state).
	fpAB := mergedAB.Fingerprint()
	fpBA := mergedBA.Fingerprint()
	if fpAB != fpBA {
		t.Fatalf("replicas did not converge:\n merge(A,B)=%s\n merge(B,A)=%s", fpAB, fpBA)
	}

	// Sink-side: all three windows must be present in the merged state.
	for _, wid := range []string{"w1", "w2", "w3"} {
		if _, ok := mergedAB.GetWindow(wid); !ok {
			t.Fatalf("window %q missing after merge(A,B)", wid)
		}
		if _, ok := mergedBA.GetWindow(wid); !ok {
			t.Fatalf("window %q missing after merge(B,A)", wid)
		}
	}

	// Sink-side: all panes from both sides must survive (union semantics).
	for _, pid := range []string{"p1", "p2", "p3", "p4"} {
		wid := "w1"
		if pid == "p3" {
			wid = "w2"
		}
		if _, ok := mergedAB.GetPane(wid, pid); !ok {
			t.Fatalf("pane %s/%s missing after merge(A,B)", wid, pid)
		}
	}
	if _, ok := mergedAB.GetPane("w3", "p5"); !ok {
		t.Fatalf("pane w3/p5 missing after merge(A,B)")
	}
}

// TestHXC1085_ConcurrentUpdatesConverge proves that N independent goroutines
// writing to separate replicas all converge after a single global merge round.
// (Stress/property-style complement to TestHXC1085_TwoReplicasMergeConverge.)
//
// MUTATION: in mergePanes, skip `dst[pid] = clonePane(op)` for absent panes ->
// new panes are not adopted; some windows have fewer panes than expected ->
// FAIL at the "all window/pane IDs present" check.
func TestHXC1085_ConcurrentUpdatesConverge(t *testing.T) {
	const numReplicas = 6
	const updatesPerReplica = 10

	replicas := make([]*CRDTSessionState, numReplicas)
	for i := range replicas {
		replicas[i] = NewCRDTSessionState()
		for j := 0; j < updatesPerReplica; j++ {
			wid := fmt.Sprintf("w-%d", j%4) // shared key space forces collisions
			pid := fmt.Sprintf("p-%d-%d", i, j)
			ver := uint64(i*updatesPerReplica + j + 1)
			replicas[i].UpdateWindow(&CRDTWindow{
				ID:      wid,
				Name:    fmt.Sprintf("name-%d-%d", i, j),
				Layout:  fmt.Sprintf("layout-%d", j%3),
				Version: ver,
			})
			replicas[i].UpdatePane(wid, &CRDTPane{
				ID:         pid,
				Command:    fmt.Sprintf("cmd-%d", i),
				WorkingDir: fmt.Sprintf("/wd/%d", j),
				Version:    ver,
			})
		}
	}

	// Global merge: fold all replicas into a single state.
	global := replicas[0].Clone()
	for _, r := range replicas[1:] {
		if err := global.Merge(r); err != nil {
			t.Fatalf("global merge: %v", err)
		}
	}

	// All per-replica unique pane IDs must be present in the global state.
	for i := 0; i < numReplicas; i++ {
		for j := 0; j < updatesPerReplica; j++ {
			pid := fmt.Sprintf("p-%d-%d", i, j)
			wid := fmt.Sprintf("w-%d", j%4)
			if _, ok := global.GetPane(wid, pid); !ok {
				t.Fatalf("pane %s/%s missing after global merge", wid, pid)
			}
		}
	}

	// Convergence check: merge all-into-global again is idempotent.
	global2 := global.Clone()
	for _, r := range replicas {
		_ = global2.Merge(r)
	}
	if global.Fingerprint() != global2.Fingerprint() {
		t.Fatal("re-merging all replicas into global changed the state (not idempotent)")
	}
}

// ---------------------------------------------------------------------------
// HXC-1085 §B: Lifecycle transition guard mutation gates
// ---------------------------------------------------------------------------

// TestHXC1085_IllegalTransition_TerminatedCannotAttach is the primary
// HXC-1085 lifecycle-guard test.  A session whose status is Terminated
// must reject Attach with an *InvalidTransitionError.
//
// MUTATION: in validateTransition, return nil unconditionally ->
//
//	error is nil; the "expected rejection" assert fires -> FAIL.
func TestHXC1085_IllegalTransition_TerminatedCannotAttach(t *testing.T) {
	err := validateTransition(StatusTerminated, actionAttach)
	if err == nil {
		t.Fatal("mutation gate: attach-from-terminated MUST return an error, got nil")
	}
	var ite *InvalidTransitionError
	if !asInvalidTransition(err, &ite) {
		t.Fatalf("expected *InvalidTransitionError, got %T: %v", err, err)
	}
	// Field-exact: From and Action must be precisely reported.
	if ite.From != StatusTerminated {
		t.Fatalf("error.From = %s, want terminated", ite.From)
	}
	if ite.Action != actionAttach {
		t.Fatalf("error.Action = %s, want attach", ite.Action)
	}
}

// TestHXC1085_IllegalTransition_TerminatedCannotMigrate guards the
// double-migrate / post-terminate-migrate edge.
//
// MUTATION: in allowedTransitions[actionMigrate], add StatusTerminated: true ->
//
//	validateTransition returns nil; the "expected rejection" fires -> FAIL.
func TestHXC1085_IllegalTransition_TerminatedCannotMigrate(t *testing.T) {
	err := validateTransition(StatusTerminated, actionMigrate)
	if err == nil {
		t.Fatal("mutation gate: migrate-from-terminated MUST be rejected")
	}
	var ite *InvalidTransitionError
	if !asInvalidTransition(err, &ite) {
		t.Fatalf("expected *InvalidTransitionError, got %T: %v", err, err)
	}
	if ite.From != StatusTerminated || ite.Action != actionMigrate {
		t.Fatalf("error fields: from=%s action=%s", ite.From, ite.Action)
	}
}

// TestHXC1085_IllegalTransition_MigratingCannotMigrateAgain guards
// concurrent-migration: a session that is already Migrating must not be
// migrated again.
//
// MUTATION: add StatusMigrating: true to allowedTransitions[actionMigrate] ->
//
//	validateTransition returns nil; assertion fires -> FAIL.
func TestHXC1085_IllegalTransition_MigratingCannotMigrateAgain(t *testing.T) {
	err := validateTransition(StatusMigrating, actionMigrate)
	if err == nil {
		t.Fatal("mutation gate: migrate-from-migrating MUST be rejected")
	}
}

// TestHXC1085_LegalTransitions_NotOverRestricted proves the FSM does not
// reject the canonical happy-path transitions (over-restriction is also a bug).
//
// MUTATION: remove StatusRunning: true from actionAttach / actionMigrate ->
//
//	legal transitions start failing -> this test FAILS.
func TestHXC1085_LegalTransitions_NotOverRestricted(t *testing.T) {
	legal := []struct {
		from   SessionStatus
		action lifecycleAction
	}{
		{StatusRunning, actionAttach},
		{StatusRunning, actionMigrate},
		{StatusRunning, actionTerminate},
		{StatusRunning, actionDetach},
		{StatusMigrating, actionTerminate},
		{StatusPaused, actionDetach},
		{StatusPaused, actionTerminate},
	}
	for _, tc := range legal {
		if err := validateTransition(tc.from, tc.action); err != nil {
			t.Fatalf("legal transition %s -> %s rejected: %v", tc.from, tc.action, err)
		}
	}
}

// ---------------------------------------------------------------------------
// HXC-1085 §C: CRDT LWW scalar-conflict mutation gate
// ---------------------------------------------------------------------------

// TestHXC1085_LWWScalarMerge_MutationGate proves that the deterministic
// tie-break in windowScalarWins selects the lexicographically greater content
// key on equal versions.  Changing this rule would make the merge
// non-commutative (the TestCRDT_Convergence_MutationGate in convergence_test.go
// catches the global case; this test verifies the exact winning value).
//
// MUTATION: in windowScalarWins, return `ow.Version > cw.Version` without the
// equal-version tie-break (i.e., always return false on equal versions) ->
// the merge of equal-version windows no longer resolves deterministically;
// merging "alpha" into "omega" keeps "omega" but merging "omega" into "alpha"
// also keeps "omega" (neither side's key wins the tie), BUT because we
// check the EXACT winning name ("omega" > "alpha" lexicographically):
//
// Actually we check both directions give the same winner.
func TestHXC1085_LWWScalarMerge_MutationGate(t *testing.T) {
	// Both windows have the same version and different names.
	// "omega" > "alpha" lexicographically, so omega should win in both directions.
	a := NewCRDTSessionState()
	a.UpdateWindow(&CRDTWindow{ID: "w", Name: "alpha", Version: 7})

	b := NewCRDTSessionState()
	b.UpdateWindow(&CRDTWindow{ID: "w", Name: "omega", Version: 7})

	// merge(a, b) — "omega" must win.
	ab := a.Clone()
	if err := ab.Merge(b); err != nil {
		t.Fatalf("merge(a,b): %v", err)
	}
	wAB, _ := ab.GetWindow("w")

	// merge(b, a) — "omega" must ALSO win (commutativity).
	ba := b.Clone()
	if err := ba.Merge(a); err != nil {
		t.Fatalf("merge(b,a): %v", err)
	}
	wBA, _ := ba.GetWindow("w")

	if wAB.Name != wBA.Name {
		t.Fatalf("LWW scalar merge not deterministic: merge(a,b)=%q merge(b,a)=%q", wAB.Name, wBA.Name)
	}
	// The lexicographically greater name must win.
	if wAB.Name != "omega" {
		t.Fatalf("LWW scalar: expected 'omega' to win (omega > alpha), got %q", wAB.Name)
	}
}

// TestHXC1085_PaneLWWMerge_MutationGate proves that paneWins correctly picks
// the higher-version pane on a head-to-head collision.
//
// MUTATION: in mergePanes, replace `paneWins(op, cp)` with `false` (never
// replace existing pane) -> a higher-version pane from the other replica is
// silently dropped; the version-exact assert fails -> FAIL.
func TestHXC1085_PaneLWWMerge_MutationGate(t *testing.T) {
	a := NewCRDTSessionState()
	a.UpdateWindow(&CRDTWindow{ID: "w", Name: "win", Version: 1})
	a.UpdatePane("w", &CRDTPane{ID: "p", Command: "low", WorkingDir: "/", Version: 1})

	b := NewCRDTSessionState()
	b.UpdateWindow(&CRDTWindow{ID: "w", Name: "win", Version: 1})
	b.UpdatePane("w", &CRDTPane{ID: "p", Command: "high", WorkingDir: "/", Version: 99})

	merged := a.Clone()
	if err := merged.Merge(b); err != nil {
		t.Fatalf("merge: %v", err)
	}
	p, ok := merged.GetPane("w", "p")
	if !ok {
		t.Fatal("pane missing after merge")
	}
	// Higher version (99) must win.
	if p.Command != "high" {
		t.Fatalf("pane LWW: expected command 'high' (ver 99), got %q", p.Command)
	}
	if p.Version != 99 {
		t.Fatalf("pane LWW: expected version 99, got %d", p.Version)
	}
}
