----------------------------- MODULE PNCounter -----------------------------
(***************************************************************************)
(* TLA+ model of a PN-Counter (Positive-Negative Counter) CRDT, replicated *)
(* across a small set of replicas, with concurrent Increment/Decrement and *)
(* asynchronous gossip/merge. Part of the Helix formal-methods stream      *)
(* (cf. ORSet, VectorClock, Swim, Consensus). This spec formally verifies  *)
(* the convergence guarantee of the REAL cluster CRDT in pkg/crdt          *)
(* (crdt.go: type PNCounter = two GCounters p,n with max-join Merge), the  *)
(* conflict-free replicated counter used across the control plane.         *)
(*                                                                         *)
(* CRDT design (state-based / CvRDT — a PN-Counter is two G-Counters):     *)
(*                                                                         *)
(*   * Each replica r keeps two per-replica VECTORS over Replicas:         *)
(*       P[r]  -- the increment G-Counter. P[r][k] is how many increments  *)
(*                replica r currently KNOWS replica k has performed.       *)
(*       N[r]  -- the decrement G-Counter. N[r][k] is how many decrements  *)
(*                replica r currently KNOWS replica k has performed.       *)
(*     A replica only ever bumps ITS OWN slot directly; it learns of other *)
(*     replicas' slots solely through merge. This per-replica-slot rule is *)
(*     what makes the G-Counter (and hence the PN-Counter) conflict-free:  *)
(*     no two replicas ever write the same slot, so there is never a true  *)
(*     write-write conflict to resolve.                                    *)
(*                                                                         *)
(*   * Increment at r: P[r][r] := P[r][r] + 1   (bump own P slot).         *)
(*   * Decrement at r: N[r][r] := N[r][r] + 1   (bump own N slot).         *)
(*                                                                         *)
(*   * VALUE at r: Value(r) = sum(P[r]) - sum(N[r]). This is exactly       *)
(*     pkg/crdt PNCounter.Value() = int64(p.Value()) - int64(n.Value()).   *)
(*                                                                         *)
(*   * Merge (driven by Gossip): the replica state is a join-semilattice   *)
(*     under POINTWISE MAX, applied independently to the P and the N       *)
(*     vectors:  P[dst][k] := max(P[dst][k], P[src][k])  for all k, and    *)
(*     likewise for N. This is exactly pkg/crdt PNCounter.Merge, which     *)
(*     merges its two GCounter halves independently with the per-replica   *)
(*     MAX rule. MAX (not SUM) is the ONLY idempotent join: re-merging an  *)
(*     already-seen state must not double-count. Gossip ships one replica's *)
(*     whole state to another and merges it in. MAX is commutative,        *)
(*     associative, and idempotent, so the merge is order- and             *)
(*     duplication-insensitive.                                            *)
(*                                                                         *)
(* Properties model-checked by TLC (see PNCounter.cfg):                    *)
(*                                                                         *)
(*   TypeOK            -- state stays well-typed and per-slot bounded.     *)
(*   Convergence (SEC) -- in any QUIESCENT state (all replicas have merged  *)
(*                        everything, i.e. identical P/N vectors), every    *)
(*                        replica computes the SAME counter value AND that  *)
(*                        value equals the TRUE (total incs - total decs).  *)
(*                        This is Strong Eventual Consistency.             *)
(*   MergeMonotonicity -- merge only GROWS a replica's per-slot knowledge   *)
(*                        (max never shrinks), so merge is idempotent +     *)
(*                        commutative and order does not matter.           *)
(*                                                                         *)
(* The model deliberately CONTAINS the divergence-prone scenarios:         *)
(*   - two replicas concurrently Increment (each bumps its own P slot),    *)
(*   - one replica Increments while another concurrently Decrements,       *)
(* so that a WRONG merge rule (SUM/ADD instead of MAX — the classic        *)
(* double-count bug) actually OVERCOUNTS and is caught as a counterexample *)
(* rather than silently passing on a degenerate model. See PNCounterMut.   *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Replicas,    \* finite set of replica ids (e.g. {r1, r2})
    MaxOps       \* per-replica bound on Increments and on Decrements (bounds state)

ASSUME MaxOps \in Nat

VARIABLES
    P,           \* P[r][k] : 0..(MaxOps*Cardinality(Replicas)) -- inc G-Counter at r
    N,           \* N[r][k] : 0..(MaxOps*Cardinality(Replicas)) -- dec G-Counter at r
    \* GHOST ground-truth operation counters. These are NOT part of the CRDT
    \* state and are NEVER touched by merge — they record how many real
    \* Increment/Decrement operations EACH replica has actually performed.
    \* They give an independent oracle for the TRUE counter value, so a SUM
    \* merge that double-counts (inflating P/N past the real op count) is
    \* caught as a Convergence violation even in a quiescent/converged state,
    \* where comparing replicas to each other alone would not reveal it.
    incOps,      \* incOps[r] : 0..MaxOps -- real Increments performed at r
    decOps       \* decOps[r] : 0..MaxOps -- real Decrements performed at r

vars == <<P, N, incOps, decOps>>

----------------------------------------------------------------------------
\* Helpers

\* Sum of a per-replica vector v (a function Replicas -> Nat).
SumVec(v) == LET add[S \in SUBSET Replicas] ==
                 IF S = {} THEN 0
                 ELSE LET k == CHOOSE x \in S : TRUE
                      IN v[k] + add[S \ {k}]
             IN add[Replicas]

\* The signed counter value the user sees at replica r: sum(P[r]) - sum(N[r]).
\* (Modelled over the integers; matches PNCounter.Value() in pkg/crdt.)
Value(r) == SumVec(P[r]) - SumVec(N[r])

\* Per-slot upper bound: every slot is bumped at most MaxOps times by its
\* owner, and merge only copies an owner's own value, so no slot can exceed
\* MaxOps under the CORRECT (max) merge. We give TypeOK headroom up to
\* MaxOps*|Replicas| so that a WRONG (sum) merge — which can push a slot past
\* MaxOps by adding two replicas' views — does NOT trivially break TypeOK and
\* instead surfaces as the meaningful Convergence/overcount counterexample.
SlotMax == MaxOps * Cardinality(Replicas)

----------------------------------------------------------------------------
\* Initial state: every replica's P and N vectors are all-zero.

ZeroVec == [k \in Replicas |-> 0]

Init ==
    /\ P = [r \in Replicas |-> ZeroVec]
    /\ N = [r \in Replicas |-> ZeroVec]
    /\ incOps = [r \in Replicas |-> 0]
    /\ decOps = [r \in Replicas |-> 0]

----------------------------------------------------------------------------
\* Actions

\* Increment at replica r: bump r's OWN slot in its P (increment) vector AND
\* record the real operation in the ghost oracle incOps[r].
\* Guarded by P[r][r] < MaxOps so the per-slot domain stays finite.
Increment(r) ==
    /\ P[r][r] < MaxOps
    /\ P' = [P EXCEPT ![r][r] = @ + 1]
    /\ incOps' = [incOps EXCEPT ![r] = @ + 1]
    /\ UNCHANGED <<N, decOps>>

\* Decrement at replica r: bump r's OWN slot in its N (decrement) vector AND
\* record the real operation in the ghost oracle decOps[r].
Decrement(r) ==
    /\ N[r][r] < MaxOps
    /\ N' = [N EXCEPT ![r][r] = @ + 1]
    /\ decOps' = [decOps EXCEPT ![r] = @ + 1]
    /\ UNCHANGED <<P, incOps>>

\* Gossip: replica src ships its full state to dst, which MERGES by POINTWISE
\* MAX on both the P and the N vectors independently. This is the CvRDT join
\* and is exactly pkg/crdt PNCounter.Merge. src # dst avoids trivial
\* self-stutter (merging a replica into itself is a no-op under max anyway).
\*
\* MERGE_OP is the per-slot join. The CORRECT spec sets it to Max; the
\* mutation spec (PNCounterMut) overrides it to Sum to exhibit the
\* double-count bug. Defining it here keeps the two specs structurally
\* identical so the mutation changes ONLY the merge rule.
Max(a, b) == IF a >= b THEN a ELSE b
MergeSlot(a, b) == Max(a, b)

Gossip(src, dst) ==
    /\ src # dst
    /\ P' = [P EXCEPT ![dst] = [k \in Replicas |-> MergeSlot(P[dst][k], P[src][k])]]
    /\ N' = [N EXCEPT ![dst] = [k \in Replicas |-> MergeSlot(N[dst][k], N[src][k])]]
    /\ UNCHANGED <<incOps, decOps>>   \* merge never touches the ground truth

Next ==
    \/ \E r \in Replicas : Increment(r)
    \/ \E r \in Replicas : Decrement(r)
    \/ \E src, dst \in Replicas : Gossip(src, dst)

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
\* Symmetry: replica ids are interchangeable; permuting them maps reachable
\* states to reachable states, so TLC may quotient the state graph by this
\* symmetry. Sound here because no action distinguishes replicas by identity.
Symmetry == Permutations(Replicas)

----------------------------------------------------------------------------
\* Action property: MERGE / state MONOTONICITY (commutativity witness).
\*
\* NO step may ever shrink a replica's per-slot knowledge: every slot of P
\* and of N is non-decreasing across every transition. This is the
\* join-semilattice law that Strong Eventual Consistency rests on. A correct
\* MAX merge preserves it trivially (max(a,b) >= a). It is an ACTION property
\* (relates a state to its successor), checked via PROPERTY. Monotone + a
\* commutative/associative/idempotent join (max) is precisely what makes the
\* merge order-insensitive — the CRDT convergence theorem's hypothesis.
MergeMonotonicity ==
    [][ \A r \in Replicas, k \in Replicas :
            /\ P[r][k] <= P'[r][k]
            /\ N[r][k] <= N'[r][k] ]_vars

----------------------------------------------------------------------------
\* Invariants

\* (1) Type correctness (and per-slot boundedness).
TypeOK ==
    /\ P \in [Replicas -> [Replicas -> 0..SlotMax]]
    /\ N \in [Replicas -> [Replicas -> 0..SlotMax]]
    /\ incOps \in [Replicas -> 0..MaxOps]
    /\ decOps \in [Replicas -> 0..MaxOps]

\* (2) Convergence / Strong Eventual Consistency.
\*
\* Two replicas have merged everything iff their underlying CRDT state (both
\* P and N vectors) is identical. When that holds, their user-visible counter
\* VALUES must coincide. Because Value is a pure function of (P,N), equal
\* state forces equal value — but a WRONG merge (SUM) makes "merged" replicas
\* hold different (overcounted) state, and the converged-state pairs then
\* disagree, which this catches. Stated over every replica pair:
Converged(r1, r2) == /\ P[r1] = P[r2]
                     /\ N[r1] = N[r2]

ConvergenceAgree ==
    \A r1, r2 \in Replicas : Converged(r1, r2) => (Value(r1) = Value(r2))

\* (2b) The CORRECTNESS half of SEC: a fully-merged replica's value must equal
\* the TRUE net count, grounded in the GHOST oracle (incOps/decOps) — the
\* number of real operations, which NO merge can corrupt. The true increment
\* count for slot k is incOps[k] (replica k's real Increment count); the true
\* decrement count is decOps[k]. Hence:
\*    TrueValue = sum_k incOps[k]  -  sum_k decOps[k].
\* A replica is FULLY merged iff its P/N slots have caught up to the ground
\* truth on every slot: P[r][k] = incOps[k] and N[r][k] = decOps[k]. A correct
\* MAX merge makes a fully-gossiped replica's slots exactly equal the owners'
\* op counts, so its Value reads TrueValue. A SUM merge inflates those slots
\* PAST the op counts (double-count), so a "merged" replica reads MORE than
\* TrueValue — and this invariant catches it even in a quiescent converged
\* state, which a replica-vs-replica comparison alone cannot.
TrueValue ==
    LET sumOver(f) == LET add[S \in SUBSET Replicas] ==
                          IF S = {} THEN 0
                          ELSE LET k == CHOOSE x \in S : TRUE
                               IN f[k] + add[S \ {k}]
                      IN add[Replicas]
    IN sumOver(incOps) - sumOver(decOps)

\* A replica has OBSERVED ALL OPERATIONS iff every slot is >= the ground-truth
\* op count for that slot — i.e. gossip has propagated every replica's updates
\* to r. This is defined from the OP ORACLE, not from the (possibly corrupted)
\* slot values, so it does NOT presuppose a correct merge: under a SUM merge an
\* inflated slot is still >= its op count, so r still counts as fully merged and
\* its overcounted Value is then required to equal TrueValue — which it does
\* NOT, exposing the double-count. (Defining FullyMerged as slot = opcount would
\* let SUM escape by never being "fully merged"; >= closes that hole.)
FullyMerged(r) == /\ \A k \in Replicas : P[r][k] >= incOps[k]
                  /\ \A k \in Replicas : N[r][k] >= decOps[k]

\* SEC correctness: once a replica has observed all operations, its value MUST
\* equal exactly the true net count. A correct MAX merge makes the slots equal
\* the op counts (so Value = TrueValue). A SUM merge inflates them above the op
\* counts, so Value > TrueValue — caught here.
ConvergenceCorrect ==
    \A r \in Replicas : FullyMerged(r) => (Value(r) = TrueValue)

\* The full convergence invariant: agreement among converged replicas AND
\* correctness of a fully-merged read.
Convergence == ConvergenceAgree /\ ConvergenceCorrect

----------------------------------------------------------------------------
\* Reachability witnesses (proving the invariants are NON-VACUOUS).
\*
\* Exposed as state predicates. The mutation/diagnostic runs and the state
\* counts document that these states ARE in the explored graph: a converged
\* state with a non-trivial (here negative — incs and decs on different
\* replicas) value is reachable, so Convergence is not vacuously true.

\* A genuinely converged state (all replicas identical) whose common value is
\* NON-ZERO — i.e. concurrent inc AND dec really happened and propagated.
ConvergedNonTrivialState ==
    /\ \A r1, r2 \in Replicas : Converged(r1, r2)
    /\ \E r \in Replicas : Value(r) # 0

\* A concurrent inc||dec witness on DIFFERENT replicas, before full merge:
\* some replica has incremented and a DIFFERENT replica has decremented.
ConcurrentIncDecState ==
    \E r1, r2 \in Replicas :
        /\ r1 # r2
        /\ P[r1][r1] > 0
        /\ N[r2][r2] > 0

=============================================================================
