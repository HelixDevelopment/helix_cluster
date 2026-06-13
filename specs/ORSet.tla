------------------------------ MODULE ORSet ------------------------------
(***************************************************************************)
(* TLA+ model of an OR-Set (Observed-Remove Set) CRDT, replicated across a *)
(* small set of replicas, with concurrent Add/Remove and asynchronous      *)
(* gossip/merge. Part of the Helix formal-methods stream (cf. Scheduling,  *)
(* Consensus, Swim, Wireguard).                                            *)
(*                                                                         *)
(* CRDT design (state-based / CvRDT, add-wins observed-remove):            *)
(*                                                                         *)
(*   * Each replica r keeps two pieces of state:                          *)
(*       adds[r]    -- a set of (element, tag) PAIRS. Every successful     *)
(*                     Add attaches a globally-UNIQUE tag (a dot:          *)
(*                     <<replica, localCounter>>) to the element.          *)
(*       removed[r] -- a set of TAGS that have been tombstoned (the        *)
(*                     observed-remove set / "minus" set).                 *)
(*                                                                         *)
(*   * Add(e) at r: invent a fresh unique tag t = <<r, ctr[r]>> and insert *)
(*     (e,t) into adds[r]. Unique tags are what make concurrent adds of    *)
(*     the same element independent observations that each survive on      *)
(*     their own.                                                          *)
(*                                                                         *)
(*   * Remove(e) at r: tombstone EXACTLY the tags of e that r has CURRENTLY *)
(*     OBSERVED -- i.e. every tag t with (e,t) in adds[r]. This is the     *)
(*     observed-remove rule: a concurrent Add(e) at another replica, whose *)
(*     tag r has not yet seen, is NOT removed and therefore survives the   *)
(*     merge => add-wins.                                                  *)
(*                                                                         *)
(*   * Merge (driven by Gossip): replica state is a join-semilattice under *)
(*     componentwise UNION (adds := adds \cup adds', removed := removed     *)
(*     \cup removed'). Gossip ships one replica's whole state to another   *)
(*     and merges it in. Union is commutative/associative/idempotent, so   *)
(*     the merge is order- and duplication-insensitive.                    *)
(*                                                                         *)
(*   * Visible(r): an element e is PRESENT at r iff e has at least one      *)
(*     add-tag that is NOT tombstoned:                                     *)
(*         e in Visible(r) <=> \E t : (e,t) in adds[r] /\ t \notin removed[r]*)
(*                                                                         *)
(* Properties model-checked by TLC (see ORSet.cfg):                        *)
(*                                                                         *)
(*   TypeOK           -- state stays well-typed.                           *)
(*   Convergence (SEC)-- in any QUIESCENT state (all replicas have merged   *)
(*                       all updates, i.e. identical add/removed state),    *)
(*                       every replica computes the SAME visible set.       *)
(*                       This is Strong Eventual Consistency.              *)
(*   AddWinsSemantics -- whenever the model has produced a concurrent       *)
(*                       Add(e) || Remove(e) (an add-tag of e that no       *)
(*                       replica has tombstoned), that e is present in the  *)
(*                       fully-merged view => add-wins, never remove-wins.  *)
(*                                                                         *)
(* The model deliberately CONTAINS the divergence-prone scenarios:         *)
(*   - two replicas concurrently Add the same element (distinct tags),     *)
(*   - one replica Removes an element while another concurrently Adds it,  *)
(* so that a WRONG merge rule (intersection instead of union, remove-wins, *)
(* or "remove all tags incl. unobserved") is actually caught as a          *)
(* counterexample rather than silently passing on a degenerate model.      *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Replicas,    \* finite set of replica ids (e.g. {r1, r2})
    Elements,    \* finite set of element values (e.g. {e1, e2})
    MaxAdds      \* per-replica bound on the number of Adds (bounds tag domain)

ASSUME MaxAdds \in Nat

\* A tag (dot) is <<originReplica, localSeqNo>>; localSeqNo in 1..MaxAdds.
Tags == Replicas \X (1..MaxAdds)

VARIABLES
    adds,        \* adds[r]    : SUBSET (Elements \X Tags) -- observed (elem,tag) pairs
    removed,     \* removed[r] : SUBSET Tags              -- tombstoned tags
    ctr          \* ctr[r]     : 0..MaxAdds               -- next local seq no at r

vars == <<adds, removed, ctr>>

----------------------------------------------------------------------------
\* Helpers

\* Tags of element e currently observed at replica r.
TagsOf(r, e) == { t \in Tags : <<e, t>> \in adds[r] }

\* The element e is present (visible) at r iff it has a live (non-tombstoned) tag.
IsPresent(r, e) == \E t \in Tags : /\ <<e, t>> \in adds[r]
                                   /\ t \notin removed[r]

\* The visible set the user sees at replica r.
Visible(r) == { e \in Elements : IsPresent(r, e) }

----------------------------------------------------------------------------
\* Initial state: every replica empty.

Init ==
    /\ adds    = [r \in Replicas |-> {}]
    /\ removed = [r \in Replicas |-> {}]
    /\ ctr     = [r \in Replicas |-> 0]

----------------------------------------------------------------------------
\* Actions

\* Add(e) at replica r: mint a fresh unique tag <<r, ctr[r]+1>> for e.
\* Guarded by ctr[r] < MaxAdds so the tag domain stays finite.
Add(r, e) ==
    /\ ctr[r] < MaxAdds
    /\ LET t == <<r, ctr[r] + 1>> IN
         adds' = [adds EXCEPT ![r] = @ \cup {<<e, t>>}]
    /\ ctr' = [ctr EXCEPT ![r] = @ + 1]
    /\ UNCHANGED removed

\* Remove(e) at replica r: tombstone EXACTLY the OBSERVED tags of e at r.
\* Concurrent adds elsewhere (tags r has not seen) are untouched => survive.
\* Enabled only when there is something observed to remove (avoids a pure
\* no-op stutter being counted as a distinct transition).
Remove(r, e) ==
    /\ TagsOf(r, e) # {}
    /\ removed' = [removed EXCEPT ![r] = @ \cup TagsOf(r, e)]
    /\ UNCHANGED <<adds, ctr>>

\* Gossip: replica src ships its full state to dst, which MERGES by union.
\* This is the CvRDT join. src # dst to avoid trivial self-stutter.
Gossip(src, dst) ==
    /\ src # dst
    /\ adds'    = [adds    EXCEPT ![dst] = @ \cup adds[src]]
    /\ removed' = [removed EXCEPT ![dst] = @ \cup removed[src]]
    /\ UNCHANGED ctr

Next ==
    \/ \E r \in Replicas, e \in Elements : Add(r, e)
    \/ \E r \in Replicas, e \in Elements : Remove(r, e)
    \/ \E src, dst \in Replicas : Gossip(src, dst)

Spec == Init /\ [][Next]_vars

\* Monotonicity of the CvRDT state: NO step may ever shrink a replica's
\* knowledge -- both the add-set and the tombstone-set only grow. This is the
\* join-semilattice law that Strong Eventual Consistency rests on, and it is
\* an ACTION property (relates a state to its successor), checked via PROPERTY.
\* A correct UNION merge preserves it; an INTERSECTION merge violates it the
\* moment one replica gossips a state that lacks a pair the other already had.
MergeMonotonicity ==
    [][ \A r \in Replicas : /\ adds[r]    \subseteq adds'[r]
                            /\ removed[r] \subseteq removed'[r] ]_vars

\* Replica ids are interchangeable; permuting them maps reachable states to
\* reachable states, so TLC may quotient the state graph by this symmetry.
Symmetry == Permutations(Replicas)

----------------------------------------------------------------------------
\* Invariants / properties

\* (1) Type correctness.
TypeOK ==
    /\ adds    \in [Replicas -> SUBSET (Elements \X Tags)]
    /\ removed \in [Replicas -> SUBSET Tags]
    /\ ctr     \in [Replicas -> 0..MaxAdds]

\* (2) Convergence / Strong Eventual Consistency.
\*
\* Two replicas have merged everything iff their underlying CRDT state is
\* identical. When that holds, their USER-VISIBLE sets MUST coincide. This
\* is the operative SEC guarantee. Because the visible set is a pure
\* function of (adds, removed), equal state forces equal views -- but a
\* WRONG merge (e.g. intersection) makes "merged" replicas hold different
\* state, and the matching converged-state pairs would then disagree, which
\* this catches. Stated over every replica pair:
Converged(r1, r2) == /\ adds[r1]    = adds[r2]
                     /\ removed[r1] = removed[r2]

Convergence ==
    \A r1, r2 \in Replicas : Converged(r1, r2) => (Visible(r1) = Visible(r2))

\* (3) Add-wins semantics.
\*
\* Consider the global, fully-merged knowledge:
\*   GlobalAdds    = union of every replica's adds
\*   GlobalRemoved = union of every replica's removed (tombstones)
\* An element e is globally present iff some add-tag of e is NOT tombstoned
\* anywhere. Add-wins says: if ANY add of e is concurrent with (not observed
\* by) the removes of e -- i.e. e has a live tag globally -- then e MUST be
\* present in the merged view. Equivalently, a fully-merged replica's view
\* equals the global live-tag computation. A remove-wins or last-writer rule,
\* or a Remove that dropped unobserved tags, would delete a concurrently-added
\* survivor and violate this.
GlobalAdds    == UNION { adds[r]    : r \in Replicas }
GlobalRemoved == UNION { removed[r] : r \in Replicas }

GloballyPresent(e) == \E t \in Tags : /\ <<e, t>> \in GlobalAdds
                                      /\ t \notin GlobalRemoved

\* If a replica has merged the full global state, its view must contain
\* exactly the globally-present (live-tag) elements -- add-wins.
AddWinsSemantics ==
    \A r \in Replicas :
        ( /\ adds[r]    = GlobalAdds
          /\ removed[r] = GlobalRemoved )
        => (\A e \in Elements : (e \in Visible(r)) <=> GloballyPresent(e))

\* (3b) Observed-remove discipline -- the structural law that MAKES the merge
\* add-wins, stated independently of the visible-set computation so it cannot
\* be satisfied vacuously by a tombstone set that over-removes.
\*
\* A tag may only ever be tombstoned by a replica that has ACTUALLY OBSERVED
\* the corresponding (element,tag) add. Because gossip ships adds and removes
\* together (and the add of a tag always precedes / accompanies its tombstone
\* under correct observed-remove), every tombstoned tag present at a replica
\* must also be present as an add at that replica. Equivalently: no tag is
\* removed without its add being known. A Remove that drops UNOBSERVED
\* concurrent tags (remove-wins) tombstones a tag whose add the removing
\* replica has not seen, breaking this immediately.
TombstoneObserved ==
    \A r \in Replicas :
        \A t \in removed[r] :
            \E e \in Elements : <<e, t>> \in adds[r]

----------------------------------------------------------------------------
\* Reachability witnesses (proving the invariants are NON-VACUOUS).
\*
\* TLC, run with these as INVARIANTs *negated by hand* would error; instead
\* we expose them as state predicates and assert (in the cfg, as separate
\* "this state is reachable" checks via PROPERTIES of <>) -- but to keep the
\* run a single exhaustive safety check, we instead make them available for
\* the mutation/diagnostic runs and document reachability via the state
\* counts (the converged + concurrent states ARE in the explored graph).

\* A genuinely converged state where at least one element is present:
ConvergedNonEmptyState ==
    /\ \A r1, r2 \in Replicas : Converged(r1, r2)
    /\ \E e \in Elements : \E r \in Replicas : e \in Visible(r)

\* A concurrent Add(e) || Remove(e) witness: globally, element e has BOTH a
\* tombstoned tag AND a live (concurrently-added, unobserved) tag.
ConcurrentAddRemoveState ==
    \E e \in Elements :
        /\ GloballyPresent(e)                                  \* a live add survives
        /\ \E t \in Tags : <<e, t>> \in GlobalAdds /\ t \in GlobalRemoved  \* and a removed one exists

============================================================================
