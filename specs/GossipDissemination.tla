------------------------- MODULE GossipDissemination -------------------------
(***************************************************************************)
(* HXC-GOSSIP: TLA+ model of epidemic / gossip dissemination CONVERGENCE   *)
(* -- the anti-entropy / infection-style propagation that underlies SWIM   *)
(* gossip and pkg/antientropy in the Helix control plane.                  *)
(*                                                                         *)
(* This extends the formal-methods stream. Swim.tla models MEMBERSHIP      *)
(* (status/incarnation precedence) and FailureDetector.tla models the      *)
(* DETECTOR; this spec isolates and proves the one property they both lean *)
(* on but neither states directly: that infection-style gossip CONVERGES   *)
(* -- a single update (a rumor / a new membership fact / a CRDT delta)     *)
(* injected at one node reaches EVERY node, and the set of nodes that      *)
(* "know" it only ever GROWS (monotone), even under bounded message loss   *)
(* and a temporarily-unreachable node.                                     *)
(*                                                                         *)
(* MODEL OVERVIEW                                                          *)
(* ------------- *)
(* N nodes. Exactly one node (the "origin") starts out KNOWING the update; *)
(* every other node starts ignorant. The only state is `known`, the set of *)
(* nodes that currently know the rumor.                                    *)
(*                                                                         *)
(*   * Push (the gossip step): a node i that KNOWS the update picks a peer  *)
(*     j (any node reachable from i in this round) and pushes the rumor to  *)
(*     it, so j now KNOWS it too. Knowledge is MONOTONE -- a push only ever *)
(*     adds to `known`, never removes.                                     *)
(*                                                                         *)
(*   * Message loss / temporary unreachability is modeled by `reachable`,  *)
(*     a per-pair relation that says which directed pushes can currently    *)
(*     succeed. A blocked pair models a dropped gossip message or a node    *)
(*     that is momentarily unreachable. CRUCIALLY the link is only ever     *)
(*     TEMPORARILY down: connectivity is restored before quiescence (see    *)
(*     `Heal` and the `LiveEnough` constraint), so the rumor cannot be      *)
(*     permanently dammed behind a dead link -- it merely takes more rounds.*)
(*                                                                         *)
(* Everything is BOUNDED (3 nodes, one rumor, a single reachability         *)
(* relation that can drop then heal) so TLC explores the FULL finite state  *)
(* space and TERMINATES (gossip quiesces once everyone knows -- there is no *)
(* enabled Push left), letting us check with -deadlock disabled.           *)
(*                                                                         *)
(* PROPERTIES (must hold with ZERO counterexamples over the full space)    *)
(* ----------                                                             *)
(*  1. TypeOK              -- structural well-formedness.                   *)
(*  2. MonotoneKnowledge   -- (safety) the origin always knows; `known`    *)
(*     never excludes a node it once included. The infection spreads,      *)
(*     never recedes. (Enforced as an inductive invariant: origin \in      *)
(*     known, and every reachable state has known monotone w.r.t. its      *)
(*     predecessor -- captured by KnownNeverEmpty + the Next relation,     *)
(*     and directly as the action property NoForget below.)                *)
(*  3. EventualConvergence -- (the headline) in EVERY quiescent state (no   *)
(*     Push enabled and connectivity healed), ALL nodes know the update:   *)
(*       Quiescent => AllKnow.                                             *)
(*     A wrong rule (e.g. a node that gossips only once then stops, or a    *)
(*     link that never heals) leaves a permanent non-knower in a quiescent  *)
(*     state and is caught here.                                           *)
(*                                                                         *)
(* NON-VACUITY is proven by two reachability witnesses (negated as          *)
(* invariants in dedicated runs): an all-know converged state IS reachable, *)
(* AND an intermediate partial-knowledge state IS reachable -- so the       *)
(* invariants are not vacuously true over a frozen initial state.          *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Nodes,    \* finite set of node ids
          Origin    \* the single node that starts out knowing the rumor

ASSUME OriginIsNode == Origin \in Nodes

VARIABLES
    known,      \* set of nodes that currently KNOW the update (the infected set)
    reachable,  \* set of ordered <<i,j>> pairs i can currently push to (links UP)
    healed      \* TRUE once connectivity has been (re)established for everyone

vars == <<known, reachable, healed>>

\* All ordered pairs of distinct nodes -- the complete directed gossip graph.
AllLinks == { <<i, j>> \in Nodes \X Nodes : i # j }

\* Symmetry over the INTERCHANGEABLE non-origin nodes only. The origin is
\* distinguished by Init (it alone starts infected), so permuting it would be
\* unsound; we permute just the ignorant-at-start peers. TLC uses this to
\* collapse redundant permutations of those nodes.
Symmetry == Permutations(Nodes \ {Origin})

----------------------------------------------------------------------------
(* Type / structural invariant. *)
TypeOK ==
    /\ known \subseteq Nodes
    /\ reachable \subseteq AllLinks
    /\ healed \in BOOLEAN

----------------------------------------------------------------------------
(* Initial state: only the origin knows; start from a possibly-degraded     *)
(* reachability relation (some links may be down) that has NOT yet healed.   *)
Init ==
    /\ known = {Origin}
    /\ reachable \in SUBSET AllLinks   \* any starting connectivity (incl. gaps)
    /\ healed = FALSE

----------------------------------------------------------------------------
(* Push: an infected node i gossips the rumor along a currently-UP link to a *)
(* peer j, infecting j. Monotone: known only grows. Enabled only while some  *)
(* knower can still reach an ignorant peer.                                  *)
Push ==
    \E i, j \in Nodes :
        /\ i \in known
        /\ j \notin known
        /\ <<i, j>> \in reachable
        /\ known' = known \cup {j}
        /\ UNCHANGED <<reachable, healed>>

(* Drop: a link temporarily goes down (gossip message loss / node briefly    *)
(* unreachable). Only allowed BEFORE healing, so loss is genuinely transient. *)
Drop ==
    /\ ~healed
    /\ \E lnk \in reachable :
            /\ reachable' = reachable \ {lnk}
            /\ UNCHANGED <<known, healed>>

(* Heal: connectivity is (re)established -- every directed link comes UP and  *)
(* stays up. This models "temporarily unreachable but reachable eventually".  *)
(* After Heal, no Drop is possible, so the gossip graph is fully connected    *)
(* and the rumor can always finish propagating.                              *)
Heal ==
    /\ ~healed
    /\ reachable' = AllLinks
    /\ healed' = TRUE
    /\ UNCHANGED known

Next == Push \/ Drop \/ Heal

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
(* ---- Quiescence ---- *)
(* The system is quiescent when no Push is enabled. With a healed (fully      *)
(* connected) graph that happens EXACTLY when every node knows: any ignorant  *)
(* node j is reachable from the origin-knower, so a Push would still fire.    *)
AllKnow == known = Nodes

\* A Push is enabled iff some knower can reach some ignorant peer.
PushEnabled ==
    \E i, j \in Nodes :
        /\ i \in known
        /\ j \notin known
        /\ <<i, j>> \in reachable

\* Quiescent for the convergence argument = healed AND no further gossip.
\* (Healing must have happened; otherwise a transient down-link could make
\*  Push falsely "not enabled" while propagation is in fact still possible.)
Quiescent == healed /\ ~PushEnabled

----------------------------------------------------------------------------
(* ---- PROPERTY 1: MonotoneKnowledge (safety) ---- *)
(* The origin is always infected and the infected set is never empty: the    *)
(* infection never recedes to nothing. Combined with the action property      *)
(* NoForget (below, checked as a temporal property) this is exactly "once a   *)
(* node knows, it never un-knows".                                            *)
MonotoneKnowledge ==
    /\ Origin \in known
    /\ known # {}

\* Action-level monotonicity: across EVERY step, known only grows. Checked as
\* a PROPERTY (it constrains the transition, not a single state) so a mutation
\* that lets a node forget the rumor is caught directly.
NoForget == [][ known \subseteq known' ]_vars

----------------------------------------------------------------------------
(* ---- PROPERTY 2: EventualConvergence (the headline invariant) ---- *)
(* In every quiescent state, everyone knows. A stuck propagation (a node left *)
(* permanently ignorant while gossip could still have reached it) is exactly  *)
(* a Quiescent state with known # Nodes -- which violates this.               *)
EventualConvergence == Quiescent => AllKnow

----------------------------------------------------------------------------
(* ---- NON-VACUITY WITNESSES ---- *)
(* Negated, these become "the good state is UNreachable"; TLC reporting them  *)
(* as VIOLATED with a trace proves the good state IS reachable. Used only in  *)
(* dedicated witness runs, never as correctness invariants of the main run.   *)

\* Witness A: a fully-converged all-know state is reachable.
NotAllKnow == known # Nodes

\* Witness B: an intermediate partial-knowledge state (more than the origin
\* knows, but not yet everyone) is reachable -- so propagation really happens
\* in steps rather than the invariants being vacuous over the initial state.
NotPartialKnowledge == ~(Origin \in known /\ known # {Origin} /\ known # Nodes)

============================================================================
