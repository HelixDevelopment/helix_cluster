-------------------------- MODULE RendezvousHashing --------------------------
(***************************************************************************)
(* TLA+ model of RENDEZVOUS (Highest-Random-Weight, HRW) consistent        *)
(* hashing, and its defining MINIMAL-DISRUPTION property. Part of the Helix *)
(* formal-methods stream (cf. Swim, GossipDissemination, RaftMembership,    *)
(* QuorumConsistency, PNCounter). This spec verifies the key->node          *)
(* assignment law behind the cluster's sharding / placement: keys are       *)
(* assigned to nodes CONSISTENTLY, and a single membership change reshuffles *)
(* the MINIMAL necessary set of keys.                                       *)
(*                                                                         *)
(* HRW assignment (the real algorithm, e.g. shard placement / replica       *)
(* selection): for each key k, every node n has a deterministic weight      *)
(* w(k,n) = hash(k,n). The key is OWNED by the LIVE node with the HIGHEST    *)
(* weight for that key:                                                      *)
(*                                                                         *)
(*     Owner(k, liveSet) = argmax_{n in liveSet} w(k,n).                     *)
(*                                                                         *)
(* HRW's correctness does NOT depend on the specific hash, only that, for   *)
(* each key, w(k,.) induces a DETERMINISTIC TOTAL ORDER over the nodes. We  *)
(* therefore model the weight function as a CONSTANT: a per-key total        *)
(* ordering Rank, where Rank[k] is a sequence of all nodes from MOST to      *)
(* LEAST preferred for key k (a fixed permutation per key). This makes the   *)
(* model finite and fully deterministic while preserving every property HRW *)
(* relies on. "Highest weight among the live nodes" becomes "earliest in    *)
(* Rank[k] among the live nodes".                                          *)
(*                                                                         *)
(* The model evolves the LIVE SET (a node is ADDED to / REMOVED from the     *)
(* membership) and recomputes the resulting key->owner assignment. The       *)
(* defining HRW invariants checked over EVERY single membership change:      *)
(*                                                                         *)
(*   MinimalDisruption -- across any single Add/Remove, the set of keys      *)
(*       whose owner CHANGED equals exactly the minimal necessary set:       *)
(*         * ADD n  : a key moves IFF n outranks the key's current owner     *)
(*                    (only keys that prefer the new node move, and they     *)
(*                    move TO n). No other key is disturbed.                 *)
(*         * REMOVE n: only the keys n OWNED reassign (to their next-highest *)
(*                    live node); keys owned by other nodes are UNAFFECTED.  *)
(*                                                                         *)
(*   UniqueDeterministicOwner -- every key with a non-empty live set has     *)
(*       EXACTLY ONE owner = its highest-ranked live node (deterministic; no *)
(*       key unassigned or doubly-assigned). + TypeOK.                       *)
(*                                                                         *)
(* TEETH (see RendezvousHashingMut.cfg): flip the assignment rule to a naive *)
(* modulo-N / hash-mod-count scheme (assign k to the live node at position   *)
(* hash(k) mod |live| in a fixed node ordering). That scheme reshuffles many *)
(* keys on a membership change, so a key that does NOT prefer the new node   *)
(* nonetheless moves -> MinimalDisruption VIOLATED. That counterexample      *)
(* proves the invariant has teeth and is the required mutation evidence.     *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, Sequences, TLC

CONSTANTS
    Keys,        \* finite set of keys to place (e.g. {k1, k2, k3})
    Nodes,       \* finite set of candidate nodes (e.g. {n1, n2, n3})
    Rank,        \* Rank[k] : a Sequence that is a permutation of Nodes,
                 \* MOST-preferred first. This is the deterministic per-key
                 \* total order induced by the (modeled-as-constant) weight
                 \* function w(k,.). "highest weight live node" == earliest
                 \* live node in Rank[k]. A fixed concrete permutation per key.
    UseModulo,   \* assignment-rule selector. FALSE (correct): HRW max-weight.
                 \* TRUE (mutation): naive hash-mod-|live| -> not minimal.
    n1, n2, n3,  \* the three concrete candidate-node model values (see .cfg).
    k1, k2, k3   \* the three concrete key model values (see .cfg).

\* Concrete per-key total order (deterministic weight function as a CONSTANT),
\* overridden into CONSTANT Rank via `Rank <- RankDef` in the .cfg. Each row is
\* a permutation of {n1,n2,n3}, MOST-preferred first:
\*   k1: n1 > n2 > n3 ;  k2: n2 > n3 > n1 ;  k3: n3 > n1 > n2.
\* Distinct rows => different keys prefer different nodes, so a single add/remove
\* disturbs different per-key subsets (non-degenerate minimal-disruption check).
RankDef ==
    ( k1 :> <<n1, n2, n3>> @@
      k2 :> <<n2, n3, n1>> @@
      k3 :> <<n3, n1, n2>> )

\* A fixed total ordering of ALL nodes, used ONLY by the modulo mutation to
\* (a) give each node a stable index and (b) define a deterministic node at
\* position p among the live nodes. Derived deterministically from Rank[k0]
\* for an arbitrary fixed key so it needs no extra constant; any fixed total
\* order works since the mutation only needs SOME deterministic order.
NodeOrder == Rank[CHOOSE k \in Keys : TRUE]

ASSUME
    \* Rank[k] must be a permutation of Nodes for every key: a genuine total
    \* order over all candidate nodes (every node appears exactly once).
    /\ \A k \in Keys :
          /\ DOMAIN Rank[k] = 1..Cardinality(Nodes)
          /\ \A n \in Nodes : \E i \in DOMAIN Rank[k] : Rank[k][i] = n
          /\ \A i, j \in DOMAIN Rank[k] : (Rank[k][i] = Rank[k][j]) => (i = j)
    /\ UseModulo \in BOOLEAN

----------------------------------------------------------------------------
VARIABLES
    live,        \* the current LIVE/member set of nodes (subset of Nodes).
    \* GHOST history of the previous step, so MinimalDisruption can compare
    \* the owner map BEFORE vs AFTER a single membership change. None of these
    \* feed back into the assignment; they only record what just happened.
    prevLive,    \* live set in the PREVIOUS state (= live at Init / no-op).
    lastOp,      \* "init" | "add" | "remove" -- which membership op just fired.
    lastNode     \* the node added/removed by the last op ("none" at Init).

vars == <<live, prevLive, lastOp, lastNode>>

NoNode == "none"   \* sentinel for "no node" (a string, distinct from any node id)

----------------------------------------------------------------------------
\* HRW core: rank position of node n for key k (1 = most preferred). Total,
\* since Rank[k] is a permutation of Nodes.
RankPos(k, n) == CHOOSE i \in DOMAIN Rank[k] : Rank[k][i] = n

\* a outranks b for key k iff a is earlier (higher weight) in Rank[k].
Outranks(k, a, b) == RankPos(k, a) < RankPos(k, b)

\* The HRW owner of key k among a non-empty node set S: the highest-weight
\* (earliest-ranked) node in S. Deterministic and unique because Rank[k] is a
\* strict total order. This is argmax_{n in S} w(k,n).
HRWOwner(k, S) == CHOOSE n \in S : \A m \in S \ {n} : Outranks(k, n, m)

\* Index of node n in the global NodeOrder (1-based), for the modulo mutation.
NodeIdx(n) == CHOOSE i \in DOMAIN NodeOrder : NodeOrder[i] = n

\* The live nodes listed in NodeOrder order (a deterministic sequence). Used
\* only by the modulo mutation to pick "the node at position p".
LiveSeq(S) ==
    LET ordered == [i \in 1..Cardinality(Nodes) |-> NodeOrder[i]]
    IN  SelectSeq(ordered, LAMBDA n : n \in S)

\* A deterministic per-key hash in 0..(|Nodes|-1), derived from the key's most
\* preferred node's index. Concrete and fixed -- HRW correctness is independent
\* of the hash, and the mutation only needs SOME deterministic per-key value.
KeyHash(k) == NodeIdx(Rank[k][1]) - 1

\* Naive modulo-N assignment (the MUTATION rule): assign k to the live node at
\* position (KeyHash(k) mod |live|) in the live-node ordering. This is the
\* classic "hash mod node-count" scheme that REshuffles many keys whenever the
\* node count changes -- it does NOT respect per-key preference, so it breaks
\* minimal disruption.
ModuloOwner(k, S) ==
    LET seq == LiveSeq(S)
        cnt == Len(seq)
    IN  seq[(KeyHash(k) % cnt) + 1]

\* The owner map under the SELECTED rule. UseModulo=FALSE -> HRW (correct);
\* UseModulo=TRUE -> modulo-N (mutation). Defined per key for a given live set.
OwnerOf(k, S) == IF UseModulo THEN ModuloOwner(k, S) ELSE HRWOwner(k, S)

\* Current owner map (only meaningful for keys when live # {}).
Owner(k) == OwnerOf(k, live)

----------------------------------------------------------------------------
Init ==
    \* Start from the FULL membership: all candidate nodes live. Membership
    \* then shrinks/grows. (Any non-empty start works; full membership makes
    \* both an add-after-remove and a remove reachable in few steps.)
    /\ live = Nodes
    /\ prevLive = Nodes
    /\ lastOp = "init"
    /\ lastNode = NoNode

----------------------------------------------------------------------------
\* Actions: a single membership change. Each records the pre-state into the
\* ghost history so the single-step MinimalDisruption action property can
\* compare owner-before vs owner-after.

\* ADD node n to the live set (n currently not live). Guarded so |live| stays
\* in 1..|Nodes| and the step genuinely changes membership.
AddNode(n) ==
    /\ n \notin live
    /\ prevLive' = live
    /\ live' = live \cup {n}
    /\ lastOp' = "add"
    /\ lastNode' = n

\* REMOVE node n from the live set (n currently live). Guarded to keep at least
\* one live node, so every key always has an owner (no empty live set).
RemoveNode(n) ==
    /\ n \in live
    /\ Cardinality(live) > 1
    /\ prevLive' = live
    /\ live' = live \ {n}
    /\ lastOp' = "remove"
    /\ lastNode' = n

Next ==
    \/ \E n \in Nodes : AddNode(n)
    \/ \E n \in Nodes : RemoveNode(n)

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
\* Symmetry: NOT declared. Although nodes look interchangeable, Rank is a
\* CONSTANT that distinguishes them by identity (each node sits at a fixed
\* position in each key's ranking), so permuting node ids does NOT map
\* reachable states to reachable states under a fixed Rank. Permuting keys is
\* likewise unsound because Rank treats keys distinctly. We therefore rely on
\* the tiny bound (3 keys, 3 nodes) for an exhaustive finite run rather than
\* symmetry reduction.

----------------------------------------------------------------------------
\* Invariants

\* (1) Type / structural correctness. live is a non-empty subset of Nodes (an
\* empty live set is never reached: Init is full, Remove keeps >=1). The ghost
\* history is well-formed.
TypeOK ==
    /\ live \subseteq Nodes
    /\ live # {}
    /\ prevLive \subseteq Nodes
    /\ lastOp \in {"init", "add", "remove"}
    /\ lastNode \in (Nodes \cup {NoNode})

\* (2) UniqueDeterministicOwner -- RULE-AGNOSTIC single-valued assignment:
\* every key has EXACTLY ONE owner and that owner is LIVE. Owner(k) is a total
\* function of (k, live), so it is by construction single-valued and
\* deterministic; this invariant additionally pins it to a LIVE node (no key
\* unassigned, none assigned to a dead node, none doubly-assigned). This holds
\* under BOTH assignment rules (HRW and the modulo mutation), so it does NOT
\* mask the MinimalDisruption counterexample on the mutation run.
UniqueDeterministicOwner ==
    \A k \in Keys : Owner(k) \in live

\* (2b) HRWMaxWeightOwner -- the HRW-SPECIFIC owner law: every key's owner is
\* precisely the highest-weight (earliest-ranked) live node -- it outranks every
\* OTHER live node for that key. This is what makes the assignment "rendezvous /
\* max-weight" rather than arbitrary. Checked on the CORRECT run only; the
\* modulo mutation breaks it (modulo picks by position, not by weight), which is
\* expected and is NOT the property we use to demonstrate the disruption teeth.
HRWMaxWeightOwner ==
    \A k \in Keys :
        \A m \in live \ {Owner(k)} : Outranks(k, Owner(k), m)

----------------------------------------------------------------------------
\* (3) MinimalDisruption -- the DEFINING HRW property, as a single-step ACTION
\* property (relates the owner map before a membership change to the owner map
\* after). We compare Owner under prevLive (pre-state owner) to Owner under
\* live (post-state owner) for the SAME key, and constrain EXACTLY which keys
\* may change owner.
\*
\* Let oldOwner(k) = OwnerOf(k, prevLive), newOwner(k) = OwnerOf(k, live).
\*
\*   * On an ADD of node x: a key k may change owner ONLY IF the added node x
\*     outranks k's previous owner (k prefers x). Equivalently:
\*       - if x does NOT outrank oldOwner(k): owner is UNCHANGED, and
\*       - if owner DID change, the new owner is exactly x AND x outranks the
\*         old owner. So no key that doesn't prefer x is disturbed, and any key
\*         that moves goes straight to x.
\*
\*   * On a REMOVE of node x: a key k may change owner ONLY IF k was OWNED by x.
\*     Equivalently:
\*       - if oldOwner(k) # x: owner is UNCHANGED, and
\*       - if oldOwner(k) = x: it reassigns to the next-highest live node
\*         (some live node, necessarily # x).
\*     Keys owned by other nodes are untouched.
\*
\* This is asserted across EVERY add/remove transition. The "init" no-op step
\* is exempt (prevLive = live, nothing changed).

\* Owner of key k computed against an arbitrary node set S, guarded for S={}.
OwnerIn(k, S) == OwnerOf(k, S)

\* Per-key disruption clause for an ADD of node x (pre-set P, post-set = P\cup{x}).
AddMinimal(k, x, P) ==
    LET oldO == OwnerIn(k, P)
        newO == OwnerIn(k, P \cup {x})
    IN  \* keys not preferring x are undisturbed; keys that move go to x.
        IF Outranks(k, x, oldO)
          THEN newO = x                 \* k prefers x => k now owned by x
          ELSE newO = oldO              \* k does not prefer x => unchanged

\* Per-key disruption clause for a REMOVE of node x (pre-set P, post = P\{x}).
RemoveMinimal(k, x, P) ==
    LET oldO == OwnerIn(k, P)
        newO == OwnerIn(k, P \ {x})
    IN  IF oldO = x
          THEN newO # x                 \* k was owned by x => reassigned away
          ELSE newO = oldO              \* k owned by someone else => unchanged

\* The action property: on every real membership step, every key obeys the
\* minimal-disruption clause for the op that fired.
MinimalDisruption ==
    [][ \/ lastOp' = "init"
        \/ /\ lastOp' = "add"
           /\ \A k \in Keys : AddMinimal(k, lastNode', prevLive')
        \/ /\ lastOp' = "remove"
           /\ \A k \in Keys : RemoveMinimal(k, lastNode', prevLive')
      ]_vars

----------------------------------------------------------------------------
\* Reachability witnesses (NON-VACUITY). Exposed as state predicates; a
\* dedicated run checks each NEGATION as an invariant, and TLC reporting it
\* VIOLATED (with a trace) proves the state IS reachable -- so the main
\* invariants are not vacuously satisfied.

\* Witness A -- an ADD that moves EXACTLY the keys preferring the new node:
\* a reachable post-add state in which some key's owner is the just-added node
\* (a key really moved to it) AND some other key was NOT disturbed (its owner
\* is not the added node). I.e. the add caused a real, partial reshuffle.
AddMovedSomeNotAllState ==
    /\ lastOp = "add"
    /\ \E k \in Keys : Owner(k) = lastNode          \* a key moved to the new node
    /\ \E k \in Keys : Owner(k) # lastNode          \* another key untouched

\* Witness B -- a REMOVE that reassigns ONLY the removed node's keys: a
\* reachable post-remove state in which the removed node owns nothing now (its
\* keys went elsewhere) while at least one key kept a non-removed owner. The
\* meaningful case is when the removed node HAD owned some key before.
RemoveReassignedOnlyItsKeysState ==
    /\ lastOp = "remove"
    /\ \E k \in Keys : OwnerOf(k, prevLive) = lastNode   \* it had owned a key
    /\ \A k \in Keys : Owner(k) # lastNode               \* now owns nothing

\* Negations, checked as INVARIANTS in the witness runs: TLC reporting these
\* VIOLATED (with a reachability trace) proves the corresponding witness state
\* IS reachable, hence the main invariants are non-vacuous.
NotAddMovedSomeNotAll        == ~AddMovedSomeNotAllState
NotRemoveReassignedOnlyKeys  == ~RemoveReassignedOnlyItsKeysState

=============================================================================
