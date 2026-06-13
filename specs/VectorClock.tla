-------------------------- MODULE VectorClock --------------------------
(***************************************************************************)
(* TLA+ model of VECTOR-CLOCK causal-order message delivery over an        *)
(* asynchronous, REORDERING broadcast network. Part of the Helix           *)
(* formal-methods stream (cf. Scheduling, Consensus, Swim, Wireguard,      *)
(* ORSet, TwoPhaseCommit, Saga). This verifies the causal-delivery         *)
(* protocol underpinning the cluster's vector clocks (pkg/crdt) and        *)
(* hybrid logical clocks (pkg/hlc).                                        *)
(*                                                                         *)
(* PROTOCOL (Birman-Schiper-Stephenson causal broadcast):                  *)
(*                                                                         *)
(*   * Each process i keeps a vector clock vc[i] : Processes -> Nat.       *)
(*     vc[i][k] = number of broadcasts FROM k that i has already           *)
(*     DELIVERED (and, for k=i, that i has SENT).                          *)
(*                                                                         *)
(*   * Broadcast at i: increment vc[i][i] by 1, stamp the outgoing         *)
(*     message m with a COPY of the just-incremented vc[i], and place one  *)
(*     copy of m in every OTHER process's incoming network buffer. The     *)
(*     stamp m.vc is the sender's knowledge AT SEND TIME, including the    *)
(*     message itself (m.vc[i] = old vc[i][i] + 1).                        *)
(*                                                                         *)
(*   * The network REORDERS: a process may pick ANY buffered message to    *)
(*     attempt delivery, in any order, independent of send order. This is  *)
(*     what makes causal delivery non-trivial -- out-of-order arrival is   *)
(*     the norm, not the exception.                                        *)
(*                                                                         *)
(*   * VC DELIVERY GUARD: process i may DELIVER a message m sent by j      *)
(*     (j # i) iff                                                         *)
(*         m.vc[j] = vc[i][j] + 1                  (next from j: no gap)    *)
(*       /\ \A k \in Processes \ {j} : m.vc[k] <= vc[i][k]                 *)
(*                                       (all of j's causal deps already   *)
(*                                        delivered at i)                  *)
(*     When the guard holds, i DELIVERS m and MERGES: vc[i] := pointwise   *)
(*     max(vc[i], m.vc). Under the guard this max simply advances          *)
(*     vc[i][j] by one and leaves the rest unchanged, but max is the       *)
(*     general (and idempotent) merge rule. A message whose guard is NOT   *)
(*     satisfied stays BUFFERED until its causal predecessors arrive --    *)
(*     the buffer is the causal reordering mechanism.                      *)
(*                                                                         *)
(* HAPPENS-BEFORE. Each broadcast gets a globally-unique id and a unique   *)
(* vector stamp (its sender's own component reaches a value no other       *)
(* message carries). For two delivered/sent messages m1, m2:              *)
(*     m1 --hb--> m2   <=>   m1.vc < m2.vc   (pointwise <=, strict)        *)
(* i.e. m2 causally depends on m1. This is the standard VC characterisation*)
(* of Lamport's happens-before for messages produced by this protocol.    *)
(*                                                                         *)
(* PROPERTIES model-checked by TLC (see VectorClock.cfg):                  *)
(*                                                                         *)
(*   TypeOK            -- state stays well-typed.                          *)
(*   CausalDelivery    -- THE safety goal: if m1 --hb--> m2 then NO process *)
(*                        delivers m2 before m1. Stated over the per-      *)
(*                        process delivery ORDER. Because the network      *)
(*                        reorders, m2 can physically arrive at i before   *)
(*                        m1; a WRONG (or absent) guard would deliver it   *)
(*                        early and violate this -- the mutation run       *)
(*                        proves the invariant has teeth.                  *)
(*   NoDuplicateDelivery -- each message delivered at most once per process.*)
(*   MonotoneVC        -- (action property) a process's vector clock never *)
(*                        decreases in any component on any step.          *)
(*                                                                         *)
(* The model DELIBERATELY contains the reordering hazard: with 2 senders   *)
(* and a causal chain (i broadcasts m1; j delivers m1 then broadcasts m2   *)
(* which depends on m1), a third (or the original) process can receive m2  *)
(* into its buffer BEFORE m1. Correct causal delivery must hold m2 until   *)
(* m1 is delivered. A degenerate (FIFO-only, single-sender) model would    *)
(* make CausalDelivery vacuous; this one does not.                         *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Processes,   \* finite set of process ids (e.g. {p1, p2, p3})
    MaxSends     \* GLOBAL bound on the total number of broadcasts (bounds state)

ASSUME MaxSends \in Nat

----------------------------------------------------------------------------
\* A vector clock is a function Processes -> Nat. The zero clock:
ZeroVC == [k \in Processes |-> 0]

\* Pointwise max merge of two vector clocks.
MaxVC(a, b) == [k \in Processes |-> IF a[k] >= b[k] THEN a[k] ELSE b[k]]

\* Pointwise <= and strict happens-before (<) on vector clocks.
LeqVC(a, b) == \A k \in Processes : a[k] <= b[k]
HB(a, b)    == LeqVC(a, b) /\ (\E k \in Processes : a[k] < b[k])

VARIABLES
    vc,        \* vc[i]     : Processes -> Nat      -- process i's vector clock
    buffer,    \* buffer[i] : SUBSET MsgIds         -- msgs i has received, not yet delivered
    delivered, \* delivered[i] : SUBSET MsgIds      -- msgs i has delivered
    delPos,    \* delPos[i] : [MsgIds -> Nat]       -- delivery sequence number at i (0 = not delivered)
    delCnt,    \* delCnt[i] : Nat                   -- how many msgs i has delivered (next pos)
    sender,    \* sender[m] : Processes             -- who broadcast message m
    stamp,     \* stamp[m]  : Processes -> Nat      -- the vector clock carried by message m
    sentCnt    \* sentCnt   : Nat                   -- total broadcasts so far (<= MaxSends)

vars == <<vc, buffer, delivered, delPos, delCnt, sender, stamp, sentCnt>>

\* Message ids: a fixed finite pool 1..MaxSends. A message m is "live" (has
\* actually been broadcast) iff m <= sentCnt; sender[m]/stamp[m] are only
\* meaningful for live ids.
MsgIds == 1..MaxSends

----------------------------------------------------------------------------
\* Helpers

\* The set of ids actually broadcast so far.
SentIds == 1..sentCnt

\* Can process i deliver message m (sent by j = sender[m]) right now?
\* THE VECTOR-CLOCK DELIVERY GUARD.
Deliverable(i, m) ==
    LET j == sender[m] IN
      /\ j # i
      /\ stamp[m][j] = vc[i][j] + 1
      /\ \A k \in Processes \ {j} : stamp[m][k] <= vc[i][k]

----------------------------------------------------------------------------
\* Initial state: all clocks zero, nothing sent, nothing buffered/delivered.

Init ==
    /\ vc        = [i \in Processes |-> ZeroVC]
    /\ buffer    = [i \in Processes |-> {}]
    /\ delivered = [i \in Processes |-> {}]
    /\ delPos    = [i \in Processes |-> [m \in MsgIds |-> 0]]
    /\ delCnt    = [i \in Processes |-> 0]
    /\ sender    = [m \in MsgIds |-> CHOOSE p \in Processes : TRUE]
    /\ stamp     = [m \in MsgIds |-> ZeroVC]
    /\ sentCnt   = 0

----------------------------------------------------------------------------
\* Actions

\* Broadcast at process i. Mint the next id (sentCnt+1), bump vc[i][i], stamp
\* the message with i's just-incremented clock, and deposit a copy into every
\* OTHER process's incoming buffer. Guarded by the global send bound.
Broadcast(i) ==
    /\ sentCnt < MaxSends
    /\ LET m   == sentCnt + 1
           nvc == [vc[i] EXCEPT ![i] = @ + 1]
       IN  /\ vc'     = [vc EXCEPT ![i] = nvc]
           /\ stamp'  = [stamp  EXCEPT ![m] = nvc]
           /\ sender' = [sender EXCEPT ![m] = i]
           /\ buffer' = [p \in Processes |->
                            IF p = i THEN buffer[p] ELSE buffer[p] \cup {m}]
           /\ sentCnt' = m
    /\ UNCHANGED <<delivered, delPos, delCnt>>

\* Deliver a buffered, causally-ready message m at process i.
\* Records delivery order (delPos/delCnt), removes m from the buffer, and
\* MERGES the stamp into vc[i] by pointwise max.
Deliver(i, m) ==
    /\ m \in buffer[i]
    /\ Deliverable(i, m)
    /\ vc'        = [vc EXCEPT ![i] = MaxVC(@, stamp[m])]
    /\ buffer'    = [buffer    EXCEPT ![i] = @ \ {m}]
    /\ delivered' = [delivered EXCEPT ![i] = @ \cup {m}]
    /\ delCnt'    = [delCnt    EXCEPT ![i] = @ + 1]
    /\ delPos'    = [delPos    EXCEPT ![i] = [@ EXCEPT ![m] = delCnt[i] + 1]]
    /\ UNCHANGED <<sender, stamp, sentCnt>>

Next ==
    \/ \E i \in Processes : Broadcast(i)
    \/ \E i \in Processes, m \in MsgIds : Deliver(i, m)

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
\* MonotoneVC -- a process's vector clock never decreases on any step.
\* ACTION property (relates a state to its successor), checked via PROPERTY.
\* Both Broadcast (self-increment) and Deliver (pointwise max) preserve it.
MonotoneVC ==
    [][ \A i \in Processes : \A k \in Processes : vc[i][k] <= vc'[i][k] ]_vars

\* Process ids are interchangeable; permuting them maps reachable states to
\* reachable states, so TLC may quotient the state graph by this symmetry.
Symmetry == Permutations(Processes)

----------------------------------------------------------------------------
\* Invariants

\* (1) Type correctness.
TypeOK ==
    /\ vc        \in [Processes -> [Processes -> 0..MaxSends]]
    /\ buffer    \in [Processes -> SUBSET MsgIds]
    /\ delivered \in [Processes -> SUBSET MsgIds]
    /\ delPos    \in [Processes -> [MsgIds -> 0..MaxSends]]
    /\ delCnt    \in [Processes -> 0..MaxSends]
    /\ sender    \in [MsgIds -> Processes]
    /\ stamp     \in [MsgIds -> [Processes -> 0..MaxSends]]
    /\ sentCnt   \in 0..MaxSends

\* (2) CausalDelivery -- THE safety goal.
\*
\* For any two LIVE messages m1, m2 with m1 --hb--> m2 (m2 causally depends on
\* m1), NO process delivers m2 strictly before m1. We read the per-process
\* delivery ORDER off delPos (a positive position = delivered at that rank).
\* A process never DELIVERS its own broadcasts (it ORIGINATES them); at send
\* time its clock already reflects the message, so an originated message is a
\* causal predecessor that is "acquired" without a delivery. Hence the order
\* relation is over ACQUISITION: i has acquired m iff it delivered m OR it is
\* the sender of m. The causal predecessor m1 must be acquired by i before i
\* delivers the dependent m2.
Acquired(i, m) == (delPos[i][m] > 0) \/ (sender[m] = i)

\* "i delivered m2 but had NOT yet acquired m1, OR delivered m1 strictly later"
\* is the causal-order violation. Because the network reorders, m2 can
\* physically arrive at i before m1; a correct VC guard buffers it until m1's
\* causal slot is filled (delivered, or m1 is i's own send), so this holds.
\* When BOTH are delivered at i, m1's delivery rank must precede m2's.
CausalDelivery ==
    \A i \in Processes :
      \A m1, m2 \in SentIds :
        ( m1 # m2 /\ HB(stamp[m1], stamp[m2]) /\ delPos[i][m2] > 0 )
          => /\ Acquired(i, m1)
             /\ (delPos[i][m1] > 0 => delPos[i][m1] < delPos[i][m2])

\* (3) NoDuplicateDelivery -- a delivered message is never re-buffered or
\* re-counted. delivered[i] and buffer[i] are disjoint, every delivered id has
\* a positive unique position, and delivery counts agree with positions.
\* (Uniqueness of positions: no two delivered ids share a rank at i.)
NoDuplicateDelivery ==
    \A i \in Processes :
      /\ (buffer[i] \cap delivered[i]) = {}
      /\ \A m \in delivered[i] : delPos[i][m] > 0
      /\ \A m \in MsgIds : (delPos[i][m] > 0) <=> (m \in delivered[i])
      /\ \A a, b \in delivered[i] :
            (a # b) => (delPos[i][a] # delPos[i][b])
      /\ delCnt[i] = Cardinality(delivered[i])

\* (4) GuardConsistency -- a structural law independent of the order bookkeeping:
\* every message a process has delivered must have had its stamp absorbed into
\* the process's clock (vc[i][k] >= stamp[m][k] for the merge to have happened),
\* and in particular the sender component advanced past the message. This cannot
\* be satisfied vacuously by a delivery that skipped the merge/guard.
GuardConsistency ==
    \A i \in Processes :
      \A m \in delivered[i] :
        \A k \in Processes : vc[i][k] >= stamp[m][k]

----------------------------------------------------------------------------
\* Reachability witness (proving CausalDelivery is NON-VACUOUS): a state in
\* which some process has delivered a causally-DEPENDENT message m2 whose
\* predecessor m1 was, at some earlier moment, sitting in that process's
\* buffer AHEAD of being deliverable -- i.e. a genuine reorder occurred and
\* the buffer had to hold m2. We expose the structural witness: two live
\* messages in a happens-before relation that have BOTH been delivered at the
\* same process (so the ordering constraint was actually exercised), where the
\* dependent one's stamp shows a cross-process dependency (out-of-order
\* potential). Asserted reachable via the mutation/reachability runs.
CausalChainDeliveredState ==
    \E i \in Processes :
      \E m1, m2 \in SentIds :
        /\ m1 # m2
        /\ HB(stamp[m1], stamp[m2])
        /\ sender[m1] # sender[m2]          \* genuinely cross-process dependency
        /\ delPos[i][m1] > 0
        /\ delPos[i][m2] > 0
        /\ i # sender[m2]

============================================================================
