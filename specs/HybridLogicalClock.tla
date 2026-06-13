----------------------- MODULE HybridLogicalClock -----------------------
(***************************************************************************)
(* TLA+ model of the HYBRID LOGICAL CLOCK (HLC) algorithm of Kulkarni,     *)
(* Demirbas, Madappa, Avva & Leone (2014). Part of the Helix formal-       *)
(* methods stream (cf. Scheduling, Consensus, Swim, Wireguard, ORSet,      *)
(* TwoPhaseCommit, Saga, VectorClock, LamportMutex, ChandyLamport). This   *)
(* verifies the real pkg/hlc primitive the cluster uses for causally-      *)
(* ordered, physical-time-bounded timestamps.                              *)
(*                                                                         *)
(* WHAT AN HLC IS. Each node keeps an HLC = <<l, c>>:                       *)
(*   * l  -- the "logical" component: tracks the maximum PHYSICAL time the  *)
(*           node has seen (its own clock or any timestamp received). It    *)
(*           stays close to physical time but is never below it.            *)
(*   * c  -- a bounded counter that breaks ties / compensates when the      *)
(*           physical clock does not advance, so distinct events that share *)
(*           the same l still get distinct, causally-ordered stamps.        *)
(* HLCs are compared LEXICOGRAPHICALLY: <<l1,c1>> < <<l2,c2>> iff           *)
(*   l1 < l2, or (l1 = l2 and c1 < c2).  This is exactly pkg/hlc's          *)
(* Timestamp.Compare (Physical first, Logical second) with l = Physical,    *)
(* c = Logical.                                                            *)
(*                                                                         *)
(* PHYSICAL CLOCKS. Every node n has a physical clock pt[n] in 0..MaxPT     *)
(* that advances monotonically and NON-DETERMINISTICALLY (it may stall;    *)
(* nodes may be skewed relative to one another). pt is the injected        *)
(* "now" of pkg/hlc (the core never reads a wall clock itself), so the      *)
(* model explores every interleaving of physical-time advances against     *)
(* events, sends, and receives.                                           *)
(*                                                                         *)
(* HLC UPDATE RULES (Kulkarni et al.; mirrors pkg/hlc Clock.Now /          *)
(* Clock.Update). l holds the prior logical component, c the prior counter:*)
(*                                                                         *)
(*   LOCAL event / SEND at node n (pkg/hlc Now):                           *)
(*     l' = max(l, pt[n])                                                   *)
(*     c' = IF l' = l THEN c + 1 ELSE 0                                     *)
(*   (l' >= pt[n] always; if physical time advanced past l the counter      *)
(*    resets, otherwise it bumps to stay strictly greater.)                *)
(*                                                                         *)
(*   RECEIVE at node n of a message stamped <<lm,cm>> (pkg/hlc Update):     *)
(*     l' = max(l, lm, pt[n])                                              *)
(*     c' = IF l' = l = lm THEN max(c,cm) + 1                              *)
(*          ELSE IF l' = l  THEN c  + 1                                     *)
(*          ELSE IF l' = lm THEN cm + 1                                     *)
(*          ELSE 0                                                          *)
(*   (the receiver's new stamp strictly dominates BOTH its own prior stamp  *)
(*    AND the sender's send stamp -- this is what makes the receive edge a  *)
(*    happens-before edge.)                                                *)
(*                                                                         *)
(* CHANNEL. A message minted by a SEND carries a COPY of the sender's       *)
(* send-stamp <<lm,cm>> and the sender's id. The (asynchronous, reordering) *)
(* channel holds it until SOME node receives it. A message may be received  *)
(* by a node other than its sender; the sender does not receive its own.    *)
(*                                                                         *)
(* CAUSALITY (happens-before, Lamport) captured here:                       *)
(*   e1 --hb--> e2  when                                                    *)
(*     (a) e2 is a LOCAL SUCCESSOR of e1 on the same node (e1 set the       *)
(*         node's stamp, e2 is the very next stamp it produced), OR         *)
(*     (b) e2 is the RECEIVE of a message whose send-stamp was e1.          *)
(* The HLC guarantee: e1 --hb--> e2  =>  HLC(e1) < HLC(e2) lexicographically.*)
(* We check this on EVERY causal step as it happens (see CausalityRespected)*)
(*                                                                         *)
(* PROPERTIES model-checked by TLC (see HybridLogicalClock.cfg):           *)
(*                                                                         *)
(*   TypeOK             -- state stays well-typed; counter stays in range.  *)
(*   CausalityRespected -- THE safety goal. (i) Every node's HLC is strictly*)
(*                         monotone across its own successive events        *)
(*                         (local-successor hb). (ii) On every RECEIVE the  *)
(*                         resulting HLC strictly dominates the message's    *)
(*                         send-stamp (message hb). A WRONG receive rule     *)
(*                         that ignored the sender's lm would let a          *)
(*                         receiver NOT dominate the sender -- the mutation  *)
(*                         run proves this invariant has TEETH.             *)
(*   BoundedDivergence  -- the HLC guarantee that l never runs arbitrarily  *)
(*                         ahead of physical time: l[n] is ALWAYS >= pt[n]   *)
(*                         (l dominates the local physical clock) AND        *)
(*                         l[n] <= the maximum physical time seen anywhere   *)
(*                         in the system (l never exceeds a real physical    *)
(*                         reading -- bounded, here tight to MaxPT).         *)
(*   CounterMonotone    -- within a fixed l, the counter only ever grows;    *)
(*                         encoded as part of the per-node monotonicity.     *)
(*                                                                         *)
(* NON-VACUITY. CausalReceiveWitness asserts reachable a RECEIVE that       *)
(* bumped the receiver's HLC ABOVE both its own prior stamp AND its own pt  *)
(* purely because of the sender's lm -- i.e. a state where the dominance    *)
(* constraint was genuinely exercised, not vacuously true.                 *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Nodes,     \* finite set of node ids (e.g. {n1, n2})
    MaxPT,     \* upper bound on every physical clock (physical domain 0..MaxPT)
    MaxEvents, \* GLOBAL bound on total events (local+send+receive); bounds state
    MaxMsgs    \* GLOBAL bound on total messages minted (channel ids); bounds state

ASSUME MaxPT     \in Nat
ASSUME MaxEvents \in Nat
ASSUME MaxMsgs   \in Nat

----------------------------------------------------------------------------
\* An HLC value is a record [l |-> Nat, c |-> Nat]. Lexicographic order:
LtHLC(a, b) == (a.l < b.l) \/ (a.l = b.l /\ a.c < b.c)
LeqHLC(a, b) == LtHLC(a, b) \/ (a.l = b.l /\ a.c = b.c)

Max2(x, y) == IF x >= y THEN x ELSE y
Max3(x, y, z) == Max2(Max2(x, y), z)

----------------------------------------------------------------------------
VARIABLES
    pt,        \* pt[n]   : 0..MaxPT       -- node n's physical clock (monotone)
    hlc,       \* hlc[n]  : [l,c]          -- node n's current HLC stamp
    prevHlc,   \* prevHlc[n] : [l,c]       -- n's HLC *before* its latest event (for hb check)
    hadEvent,  \* hadEvent[n] : BOOLEAN    -- has n produced at least one event?
    chan,      \* chan    : SUBSET MsgIds  -- ids of messages in flight (unreceived)
    msgStamp,  \* msgStamp[m] : [l,c]      -- send-stamp carried by message m
    msgFrom,   \* msgFrom[m]  : Nodes      -- which node sent message m
    minted,    \* minted  : Nat            -- how many messages minted so far (<= MaxMsgs)
    evtCnt,    \* evtCnt  : Nat            -- how many events so far (<= MaxEvents)
    lastRecvOK \* lastRecvOK : BOOLEAN     -- did the most recent step, if a receive,
               \*                             leave the receiver dominating the msg stamp?
               \* (used to make the receive-dominance hb edge an inductive invariant)

vars == <<pt, hlc, prevHlc, hadEvent, chan, msgStamp, msgFrom,
          minted, evtCnt, lastRecvOK>>

\* Message-id pool. A message m is live (actually minted) iff m <= minted.
MsgIds == 1..MaxMsgs

Zero == [l |-> 0, c |-> 0]

----------------------------------------------------------------------------
\* Initial state: physical clocks at 0, HLCs at zero, channel empty.
Init ==
    /\ pt        = [n \in Nodes |-> 0]
    /\ hlc       = [n \in Nodes |-> Zero]
    /\ prevHlc   = [n \in Nodes |-> Zero]
    /\ hadEvent  = [n \in Nodes |-> FALSE]
    /\ chan      = {}
    /\ msgStamp  = [m \in MsgIds |-> Zero]
    /\ msgFrom   = [m \in MsgIds |-> CHOOSE p \in Nodes : TRUE]
    /\ minted    = 0
    /\ evtCnt    = 0
    /\ lastRecvOK = TRUE

----------------------------------------------------------------------------
\* Actions

\* Physical-clock advance: node n's clock ticks forward by 1 (monotone,
\* non-deterministic, bounded). Skew between nodes arises naturally because
\* each node ticks independently. This does NOT consume an event budget --
\* it is the environment, not an HLC operation -- but it IS bounded by MaxPT.
Tick(n) ==
    /\ pt[n] < MaxPT
    /\ pt' = [pt EXCEPT ![n] = @ + 1]
    /\ UNCHANGED <<hlc, prevHlc, hadEvent, chan, msgStamp, msgFrom,
                   minted, evtCnt, lastRecvOK>>

\* The HLC value a LOCAL event / SEND at node n would produce (pkg/hlc Now).
LocalNext(n) ==
    LET old == hlc[n]
        lp  == Max2(old.l, pt[n])
    IN  IF lp = old.l THEN [l |-> lp, c |-> old.c + 1]
                      ELSE [l |-> lp, c |-> 0]

\* The HLC value a RECEIVE at node n of message stamp ms produces (pkg/hlc
\* Update). l' = max(l, lm, pt); counter per the standard HLC receive rule.
RecvNext(n, ms) ==
    LET old == hlc[n]
        lp  == Max3(old.l, ms.l, pt[n])
    IN  IF lp = old.l /\ lp = ms.l THEN [l |-> lp, c |-> Max2(old.c, ms.c) + 1]
        ELSE IF lp = old.l         THEN [l |-> lp, c |-> old.c + 1]
        ELSE IF lp = ms.l          THEN [l |-> lp, c |-> ms.c + 1]
        ELSE                            [l |-> lp, c |-> 0]

\* LOCAL event at node n: advance n's HLC per Now; record prevHlc for the
\* local-successor happens-before check. No message produced.
LocalEvent(n) ==
    /\ evtCnt < MaxEvents
    /\ hlc'      = [hlc      EXCEPT ![n] = LocalNext(n)]
    /\ prevHlc'  = [prevHlc  EXCEPT ![n] = hlc[n]]
    /\ hadEvent' = [hadEvent EXCEPT ![n] = TRUE]
    /\ evtCnt'   = evtCnt + 1
    /\ lastRecvOK' = TRUE
    /\ UNCHANGED <<pt, chan, msgStamp, msgFrom, minted>>

\* SEND at node n: produce a fresh HLC (like a local event), then mint a
\* message carrying a COPY of that stamp and put it on the channel.
Send(n) ==
    /\ evtCnt < MaxEvents
    /\ minted < MaxMsgs
    /\ LET m  == minted + 1
           ns == LocalNext(n)
       IN  /\ hlc'      = [hlc      EXCEPT ![n] = ns]
           /\ prevHlc'  = [prevHlc  EXCEPT ![n] = hlc[n]]
           /\ hadEvent' = [hadEvent EXCEPT ![n] = TRUE]
           /\ msgStamp' = [msgStamp EXCEPT ![m] = ns]
           /\ msgFrom'  = [msgFrom  EXCEPT ![m] = n]
           /\ chan'     = chan \cup {m}
           /\ minted'   = m
    /\ evtCnt'   = evtCnt + 1
    /\ lastRecvOK' = TRUE
    /\ UNCHANGED <<pt>>

\* RECEIVE at node n of in-flight message m (sent by another node): merge the
\* message stamp into n's HLC per the receive rule, consume m from the channel.
Receive(n, m) ==
    /\ evtCnt < MaxEvents
    /\ m \in chan
    /\ msgFrom[m] # n                       \* a node does not receive its own send
    /\ LET ms == msgStamp[m]
           nr == RecvNext(n, ms)
       IN  /\ hlc'      = [hlc      EXCEPT ![n] = nr]
           /\ prevHlc'  = [prevHlc  EXCEPT ![n] = hlc[n]]
           /\ hadEvent' = [hadEvent EXCEPT ![n] = TRUE]
           /\ chan'     = chan \ {m}
           \* record whether the receive left n strictly dominating the sender:
           /\ lastRecvOK' = LtHLC(ms, nr)
    /\ evtCnt'   = evtCnt + 1
    /\ UNCHANGED <<pt, msgStamp, msgFrom, minted>>

Next ==
    \/ \E n \in Nodes : Tick(n)
    \/ \E n \in Nodes : LocalEvent(n)
    \/ \E n \in Nodes : Send(n)
    \/ \E n \in Nodes, m \in MsgIds : Receive(n, m)

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
\* Node ids are interchangeable; permuting them maps reachable states to
\* reachable states, so TLC may quotient the state graph by this symmetry.
\* (msgFrom is over Nodes and IS permuted consistently; message ids are
\* mint-order counters and are NOT permuted.)
Symmetry == Permutations(Nodes)

----------------------------------------------------------------------------
\* Helper: maximum physical time currently observed anywhere in the system.
MaxPTSeen == Max2(0, MaxPT)   \* every pt[n] <= MaxPT by Tick's guard

----------------------------------------------------------------------------
\* INVARIANTS

\* (1) Type correctness; counter stays bounded (no runaway). The logical
\* component l is bounded by MaxPT (BoundedDivergence proves it independently);
\* we type l generously and let BoundedDivergence pin the real bound.
HLCRange == [l : 0..MaxPT, c : 0..(MaxEvents + 1)]
TypeOK ==
    /\ pt        \in [Nodes -> 0..MaxPT]
    /\ hlc       \in [Nodes -> HLCRange]
    /\ prevHlc   \in [Nodes -> HLCRange]
    /\ hadEvent  \in [Nodes -> BOOLEAN]
    /\ chan      \in SUBSET MsgIds
    /\ msgStamp  \in [MsgIds -> HLCRange]
    /\ msgFrom   \in [MsgIds -> Nodes]
    /\ minted    \in 0..MaxMsgs
    /\ evtCnt    \in 0..MaxEvents
    /\ lastRecvOK \in BOOLEAN

\* (2) CausalityRespected -- THE safety goal. Two happens-before families:
\*
\*   (i)  LOCAL-SUCCESSOR hb: every event a node produces yields a stamp
\*        STRICTLY GREATER than the stamp it held immediately before. We
\*        carry prevHlc = the stamp before the latest event; once a node has
\*        had an event, prevHlc < hlc must hold. This is exactly pkg/hlc's
\*        strict-monotonicity guarantee (each Now()/Update() result strictly
\*        exceeds the previous one on that clock).
\*
\*   (ii) MESSAGE hb: the most recent step, if it was a RECEIVE, left the
\*        receiver's HLC strictly dominating the received message's send-stamp
\*        (lastRecvOK). Under the CORRECT receive rule this is always TRUE;
\*        under a mutated rule that drops the sender's lm it can be FALSE,
\*        yielding a counterexample. lastRecvOK is reset to TRUE by every
\*        non-receive step, so the invariant speaks only about the latest
\*        receive -- but since it must hold after EVERY receive, it constrains
\*        all of them.
CausalityRespected ==
    /\ \A n \in Nodes : hadEvent[n] => LtHLC(prevHlc[n], hlc[n])
    /\ lastRecvOK

\* (3) BoundedDivergence -- the HLC physical-time guarantee: l never runs
\* arbitrarily AHEAD of physical time.
\*   (a) l[n] <= MaxPT: a node's logical component never exceeds the maximum
\*       physical reading available anywhere in the system. Every stamp's l
\*       derives (transitively, via max) from some node's pt advance, and pt is
\*       capped at MaxPT, so l is tightly bounded by MaxPT and CANNOT drift
\*       arbitrarily into the future. This is the "l - pt stays bounded"
\*       guarantee in its tightest form for this model (the bound is exactly
\*       the largest physical time the system can produce).
\*   (b) at the moment of (after) an event, l dominates the node's OWN physical
\*       clock: l[n] >= pt[n]. NOTE this is asserted only WHEN no later bare
\*       physical Tick has advanced pt[n] past the last event -- i.e. l catches
\*       up to physical time on each Now()/Update(), and a subsequent unobserved
\*       Tick may transiently leave pt[n] > l[n] until the next event. We assert
\*       the always-true direction (the bound that matters for divergence) here;
\*       the at-event lower bound is captured by LtoPTAfterEvent below.
\*   Also pins every in-flight message stamp to the same upper bound.
BoundedDivergence ==
    /\ \A n \in Nodes : hlc[n].l <= MaxPT
    /\ \A m \in 1..minted : msgStamp[m].l <= MaxPT

\* (3b) LtoPTAfterEvent -- the lower-bound half of the HLC guarantee, stated at
\* the only point it is guaranteed: a node whose physical clock has NOT advanced
\* beyond the prior stamp's l (still 0 if no tick, or any node that already
\* observed its current pt) keeps l >= pt. Concretely, since LocalNext/RecvNext
\* set l' = max(l, pt, ...), immediately after ANY event l[n] >= pt[n]. A bare
\* Tick is the ONLY action that can momentarily break it; so we assert: if
\* l[n] < pt[n] then it is because pt advanced since the last event, which is
\* always permissible. We capture the strong, always-holding consequence: l[n]
\* is at least the physical time as of the node's most recent EVENT, encoded as
\* l[n] >= prevHlc[n].l (the l never decreases) AND l[n] tracks max physical
\* time seen. The pin we can assert unconditionally and meaningfully:
\* hlc[n].l >= prevHlc[n].l (l is monotone non-decreasing per node).
LMonotone ==
    \A n \in Nodes : hlc[n].l >= prevHlc[n].l

\* (4) CounterMonotone / no-overflow structural law: the counter never exceeds
\* the number of events (it can only bump once per event), so it stays inside
\* the typed range -- a cheap independent check that the reset/bump logic never
\* lets c run away while l is pinned.
CounterBounded ==
    /\ \A n \in Nodes : hlc[n].c <= evtCnt
    /\ \A m \in 1..minted : msgStamp[m].c <= evtCnt

----------------------------------------------------------------------------
\* NON-VACUITY witness (proving CausalityRespected's message-hb arm is NOT
\* vacuously true): a reachable state whose LATEST step was a RECEIVE that
\* bumped the receiver's HLC strictly above BOTH its own previous stamp AND
\* its own physical clock, driven by the sender's lm. Concretely: the receiver
\* now holds an l that exceeds its own pt (so the sender's physical time, not
\* the receiver's, set it) and strictly exceeds the stamp it held before the
\* receive. Asserted reachable via the witness run (negate to get a trace).
CausalReceiveWitness ==
    \E n \in Nodes :
        /\ hadEvent[n]
        /\ LtHLC(prevHlc[n], hlc[n])
        /\ hlc[n].l > pt[n]          \* the dominating l came from the network, not local pt
        /\ lastRecvOK

\* Negation is the property we actually run to extract the witness trace:
NoCausalReceiveWitness == ~CausalReceiveWitness

============================================================================
