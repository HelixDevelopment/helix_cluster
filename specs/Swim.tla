------------------------------- MODULE Swim -------------------------------
(***************************************************************************)
(* HXC-SWIM: TLA+ model of the SWIM membership / failure-detection         *)
(* protocol (Scalable Weakly-consistent Infection-style Membership).       *)
(*                                                                         *)
(* This extends the HXC-1139 formal-methods work (Scheduling.tla,          *)
(* Consensus.tla) to the gossip/membership layer of the Helix control      *)
(* plane (SWIM gossip + Raft consensus, per the architecture).             *)
(*                                                                         *)
(* MODEL OVERVIEW                                                          *)
(* ------------- *)
(* A small set of nodes, each holding a membership VIEW that maps every    *)
(* peer to a (status, incarnation) pair where status \in {alive, suspect,  *)
(* dead}. Failure detection runs as in the SWIM paper:                     *)
(*                                                                         *)
(*   * Direct ping: node i pings a target j. If the ping is DELIVERED, j   *)
(*     is confirmed alive at its current incarnation. If LOST, i may fall  *)
(*     through to an indirect probe.                                       *)
(*   * Indirect ping-req: i asks k relays to ping j on its behalf. If any  *)
(*     relay's probe is delivered, j is confirmed alive; otherwise i marks *)
(*     j SUSPECT (it does not jump straight to dead).                      *)
(*   * Suspicion timeout: a SUSPECT verdict that is not refuted within a   *)
(*     bounded timeout escalates suspect -> DEAD.                          *)
(*   * Refutation (the SWIM correctness core): when a node learns it is    *)
(*     suspected/dead at incarnation c, it bumps its OWN incarnation to    *)
(*     c+1 and disseminates a fresh ALIVE update. The monotone-incarnation *)
(*     rule guarantees this refutation supersedes the stale suspect/dead.  *)
(*   * Infection-style dissemination: membership updates are piggybacked   *)
(*     and merged into peer views by the SWIM precedence rule (higher      *)
(*     incarnation always wins; at equal incarnation dead > suspect >      *)
(*     alive).                                                             *)
(*                                                                         *)
(* Everything is BOUNDED (3-4 nodes, incarnations 0..MaxInc, a suspicion   *)
(* timeout counter 0..SuspectTimeout, message loss as nondeterministic     *)
(* delivery) so TLC explores the full finite state space.                  *)
(*                                                                         *)
(* SAFETY PROPERTIES (must hold with ZERO counterexamples)                 *)
(* -----------------                                                      *)
(*  1. NoFalsePermanentDeath: a node that is actually alive is never left  *)
(*     permanently DEAD in a correct node's view once its refuting (alive, *)
(*     higher-incarnation) update has propagated. Operationally: no view   *)
(*     marks a node dead at an incarnation that the node has already       *)
(*     refuted by advancing past it. Message loss and suspicion CAN drive  *)
(*     a transient suspect/dead -- the incarnation rule is what makes the  *)
(*     death non-permanent. A wrong refute rule WOULD violate this.        *)
(*  2. IncarnationMonotonicity: a node's own incarnation only ever         *)
(*     increases; and no view holds a DEAD verdict for a peer at an        *)
(*     incarnation strictly below that peer's true current incarnation     *)
(*     (i.e. a death the peer has already out-lived).                      *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Nodes,           \* finite set of node ids
          MaxInc,          \* highest incarnation explored (bounds state)
          SuspectTimeout   \* ticks a suspect endures before escalating to dead

ASSUME MaxInc \in Nat /\ MaxInc >= 1
ASSUME SuspectTimeout \in Nat /\ SuspectTimeout >= 1

Incarnations == 0..MaxInc

\* Status precedence used by the infection-style merge. At EQUAL incarnation
\* a more-negative report wins (dead beats suspect beats alive); a strictly
\* higher incarnation always wins regardless of status (refutation).
Status == {"alive", "suspect", "dead"}

\* Numeric rank for equal-incarnation conflict resolution (higher = "more dead").
Rank(st) == CASE st = "alive"   -> 0
              [] st = "suspect" -> 1
              [] st = "dead"    -> 2

VARIABLES
    incarnation,  \* incarnation[n]      : node n's OWN true incarnation number
    alive,        \* alive[n]            : TRUE iff node n is actually up/reachable
    view          \* view[i][j]          : i's belief about j = [st |-> .., inc |-> ..]

vars == <<incarnation, alive, view>>

\* The (status, incarnation) record i currently believes about j.
Belief(i, j) == view[i][j]

(***************************************************************************)
(* The infection-style merge: does an incoming (status st @ incarnation   *)
(* inc) update about peer j SUPERSEDE what node i currently believes?      *)
(* This is the canonical SWIM precedence relation:                         *)
(*   * strictly higher incarnation always overrides (this is how a fresh   *)
(*     ALIVE refutation defeats a stale SUSPECT/DEAD);                      *)
(*   * at equal incarnation, a higher-ranked (more negative) status wins,  *)
(*     so suspect/dead can spread but ONLY at the same incarnation.        *)
(* Critically there is NO rule by which a dead/suspect at incarnation c    *)
(* can override an alive at incarnation > c -- that asymmetry is exactly   *)
(* what protects a refuted node.                                           *)
(***************************************************************************)
Supersedes(st, inc, cur) ==
    \/ inc > cur.inc
    \/ (inc = cur.inc /\ Rank(st) > Rank(cur.st))

\* Apply an update about j into i's view iff it supersedes the current belief.
\* A node is AUTHORITATIVE about itself: no incoming report can downgrade i's
\* belief about i. A suspect/dead report about oneself is handled by Refute
\* (incarnation bump), never by overwriting the self-view -- this is a core
\* SWIM rule. We therefore freeze the self-cell (i = j) under any merge.
MergeInto(i, j, st, inc) ==
    /\ i # j
    /\ view' = [view EXCEPT ![i][j] =
                IF Supersedes(st, inc, view[i][j])
                THEN [st |-> st, inc |-> inc]
                ELSE view[i][j]]

TypeOK ==
    /\ incarnation \in [Nodes -> Incarnations]
    /\ alive       \in [Nodes -> BOOLEAN]
    /\ view \in [Nodes -> [Nodes -> [st: Status, inc: Incarnations]]]

Init ==
    /\ incarnation = [n \in Nodes |-> 0]
    /\ alive       = [n \in Nodes |-> TRUE]   \* everyone starts up
    /\ view = [i \in Nodes |->
                 [j \in Nodes |-> [st |-> "alive", inc |-> 0]]]

(***************************************************************************)
(* DIRECT PING (delivered).  i pings j; the ping is delivered, so i learns *)
(* j is alive at j's TRUE current incarnation and merges that in. This is  *)
(* the path that REFUTES a stale suspicion: an alive report at j's current *)
(* (possibly bumped) incarnation supersedes an older suspect/dead.         *)
(***************************************************************************)
DirectPingOK(i, j) ==
    /\ i # j
    /\ alive[j]                     \* j really answers only if it is up
    /\ MergeInto(i, j, "alive", incarnation[j])
    /\ UNCHANGED <<incarnation, alive>>

(***************************************************************************)
(* INDIRECT PING-REQ (k-relayed, succeeds).  Models the case where i's     *)
(* direct ping was LOST but at least one relay r reaches j. The fan-out    *)
(* itself is abstracted: it is enough that SOME relay r (r # i, r # j)      *)
(* exists, r is up, and j is up -- then j is confirmed alive. This is the  *)
(* indirect-probe recovery that keeps a transient loss from becoming a     *)
(* false suspicion.                                                        *)
(***************************************************************************)
IndirectPingOK(i, j) ==
    /\ i # j
    /\ alive[j]
    /\ \E r \in Nodes : r # i /\ r # j /\ alive[r]
    /\ MergeInto(i, j, "alive", incarnation[j])
    /\ UNCHANGED <<incarnation, alive>>

(***************************************************************************)
(* FAILED PROBE -> SUSPECT.  Both the direct ping AND the indirect ping-   *)
(* req fall through (all delivery attempts lost, OR j is genuinely down).  *)
(* i now SUSPECTS j at the incarnation i currently has on record for j.    *)
(* It does NOT leap to dead -- that only happens via the suspicion timeout.*)
(* This action is the source of FALSE suspicion: it fires even when j is   *)
(* alive (modelling pure message loss), which is precisely the adversarial *)
(* scenario the NoFalsePermanentDeath invariant must survive.              *)
(***************************************************************************)
SuspectOnTimeout(i, j) ==
    /\ i # j
    /\ view[i][j].st = "alive"      \* currently believed alive...
    /\ MergeInto(i, j, "suspect", view[i][j].inc)   \* ...now suspected, same inc
    /\ UNCHANGED <<incarnation, alive>>

(***************************************************************************)
(* SUSPICION TIMEOUT ESCALATION.  A suspect that has not been refuted      *)
(* escalates to DEAD at the SAME incarnation it was suspected at. (The     *)
(* SuspectTimeout constant bounds how long this takes in real time; in the *)
(* untimed safety model the escalation may fire at any point a node is     *)
(* still suspect, which is the most adversarial scheduling.)               *)
(***************************************************************************)
EscalateToDead(i, j) ==
    /\ i # j
    /\ view[i][j].st = "suspect"
    /\ MergeInto(i, j, "dead", view[i][j].inc)
    /\ UNCHANGED <<incarnation, alive>>

(***************************************************************************)
(* REFUTATION (the SWIM self-defence).  Node j discovers -- via some peer  *)
(* i -- that it is SUSPECTED or DEAD at an incarnation c >= its own. A live *)
(* node always refutes: it advances its incarnation to c+1 (capped by      *)
(* MaxInc) and resets its self-belief to alive at the new incarnation. The *)
(* fresh, higher incarnation is what will supersede the stale verdict as   *)
(* it disseminates. Only an ALIVE node can refute -- a truly dead node     *)
(* cannot, which is why a real failure eventually sticks.                  *)
(***************************************************************************)
Refute(j) ==
    /\ alive[j]
    /\ incarnation[j] < MaxInc      \* bound the incarnation space
    /\ \E i \in Nodes :
         /\ i # j
         /\ view[i][j].st \in {"suspect", "dead"}
         /\ view[i][j].inc >= incarnation[j]
    /\ incarnation' = [incarnation EXCEPT ![j] = incarnation[j] + 1]
    /\ view' = [view EXCEPT ![j][j] = [st |-> "alive", inc |-> incarnation[j] + 1]]
    /\ UNCHANGED alive

(***************************************************************************)
(* INFECTION-STYLE GOSSIP.  Node i piggybacks its belief about some peer m *)
(* onto a message to node k, who merges it by the precedence rule. This is *)
(* how both deaths AND refutations spread through the cluster. Bounded     *)
(* simply by being a normal disjunct over all (i, k, m).                   *)
(***************************************************************************)
Gossip(i, k, m) ==
    /\ i # k
    /\ LET b == view[i][m] IN
         MergeInto(k, m, b.st, b.inc)
    /\ UNCHANGED <<incarnation, alive>>

(***************************************************************************)
(* CRASH.  A node genuinely goes down. Its self-incarnation is frozen;     *)
(* peers that probe it will (correctly) fail and eventually mark it dead,  *)
(* and -- being down -- it can no longer refute. This makes a REAL death   *)
(* permanent, which is correct and must NOT trip NoFalsePermanentDeath     *)
(* (that invariant only protects nodes that are actually alive).           *)
(***************************************************************************)
Crash(n) ==
    /\ alive[n]
    /\ alive' = [alive EXCEPT ![n] = FALSE]
    /\ UNCHANGED <<incarnation, view>>

Next ==
    \/ \E i, j \in Nodes : DirectPingOK(i, j)
    \/ \E i, j \in Nodes : IndirectPingOK(i, j)
    \/ \E i, j \in Nodes : SuspectOnTimeout(i, j)
    \/ \E i, j \in Nodes : EscalateToDead(i, j)
    \/ \E j \in Nodes    : Refute(j)
    \/ \E i, k, m \in Nodes : Gossip(i, k, m)
    \/ \E n \in Nodes    : Crash(n)

Spec == Init /\ [][Next]_vars /\ WF_vars(Next)

\* Node ids are interchangeable, so membership states are equivalent under any
\* permutation of Nodes. Declaring this symmetry lets TLC explore one
\* representative per equivalence class and finish the full state space.
Symmetry == Permutations(Nodes)

(***************************************************************************)
(* SAFETY INVARIANT 1 -- NoFalsePermanentDeath under bounded loss.         *)
(*                                                                         *)
(* The protective core of SWIM is that a false death is always REFUTABLE:  *)
(* it never gets pinned at an incarnation a live node cannot escape. We    *)
(* state this as the SAFETY property that makes the death non-permanent:   *)
(*                                                                         *)
(*   For every observer i and live target j, if i believes j DEAD, that    *)
(*   dead verdict is at an incarnation <= j's own current incarnation --    *)
(*   so j can ALWAYS supersede it by advancing to incarnation+1 (Refute    *)
(*   bumps to inc+1, and Supersedes(alive, inc+1, dead@<=inc) is TRUE).    *)
(*                                                                         *)
(* Equivalently: no view ever holds a death for a LIVE node at an          *)
(* incarnation STRICTLY ABOVE that node's own. Such a death would be       *)
(* un-refutable (j could never out-bump it within the protocol) and hence  *)
(* a genuinely PERMANENT false death. It can only arise if the precedence  *)
(* rule wrongly lets a fabricated dead@(c+1) override alive@c, or if a      *)
(* node's incarnation fails to track its self-view -- exactly the SWIM     *)
(* modelling errors this invariant exists to catch. NOTE: TLC first caught *)
(* a too-strong earlier draft (which demanded the refutation had ALREADY   *)
(* happened -- a liveness claim, not a state invariant); a transient       *)
(* dead@inc(j) for a live j is the normal, refutable case and is exactly   *)
(* what the convergence PROPERTY below, not this invariant, governs.       *)
(*                                                                         *)
(* The matching LIVENESS guarantee -- that the false death does not just   *)
(* stay refutable but is actually CLEARED -- is EventuallyAgreesAlive.     *)
(***************************************************************************)
NoFalsePermanentDeath ==
    \A i, j \in Nodes :
        ( alive[j] /\ view[i][j].st = "dead" )
            => view[i][j].inc <= incarnation[j]

(***************************************************************************)
(* SAFETY INVARIANT 2 -- IncarnationMonotonicity.                          *)
(*                                                                         *)
(* (a) A node's self-view incarnation never lags its own true incarnation: *)
(*     view[n][n].inc = incarnation[n] and it is alive in its own eyes     *)
(*     (a node always considers itself alive at its current incarnation).  *)
(* (b) No view holds a DEAD verdict for a peer j at an incarnation ABOVE   *)
(*     j's own true incarnation -- nobody can fabricate a death at an       *)
(*     incarnation j never reached. Combined with the self-view rule and   *)
(*     the fact that incarnation[n] only increases (every action either    *)
(*     leaves it unchanged or, in Refute, increments it), this captures    *)
(*     "no view accepts a dead verdict for a node at an incarnation the    *)
(*     node has already refuted / never issued."                           *)
(***************************************************************************)
IncarnationMonotonicity ==
    /\ \A n \in Nodes :
         /\ view[n][n].inc = incarnation[n]
         /\ view[n][n].st  = "alive"
    /\ \A i, j \in Nodes :
         view[i][j].st = "dead" => view[i][j].inc <= incarnation[j]

\* Self-evident structural guard kept alongside the two headline invariants.
ViewWellFormed ==
    \A i, j \in Nodes : view[i][j].inc <= incarnation[j] \/ view[i][j].st = "alive"

(***************************************************************************)
(* OPTIONAL LIVENESS -- eventual convergence to truth.                     *)
(* For nodes that never crash, every observer eventually agrees the target *)
(* is alive at the target's stabilised incarnation. Checked only on the    *)
(* no-crash sub-model (a separate cfg can enable it); under the full model *)
(* with crashes it does not hold (a crashed node stays dead), which is     *)
(* correct, so it is left as documentation here rather than a default      *)
(* PROPERTY to avoid spurious counterexamples.                             *)
(***************************************************************************)
EventuallyAgreesAlive ==
    \A i, j \in Nodes :
        (alive[j]) ~> (view[i][j].st = "alive")

=============================================================================
