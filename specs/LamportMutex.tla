-------------------------- MODULE LamportMutex --------------------------
(***************************************************************************)
(* TLA+ model of LAMPORT's 1978 logical-clock DISTRIBUTED MUTUAL          *)
(* EXCLUSION algorithm (the classic timestamp-ordered request-queue       *)
(* mutex). Part of the Helix formal-methods stream (cf. VectorClock,      *)
(* Consensus, Swim, Wireguard, ORSet, TwoPhaseCommit, Saga, LeaderLease,  *)
(* RaftMembership). This verifies the Lamport-clock + distributed-lock    *)
(* primitives the cluster relies on (pkg/lock, pkg/hlc Lamport ordering). *)
(*                                                                         *)
(* ALGORITHM (Lamport, "Time, Clocks, and the Ordering of Events in a     *)
(* Distributed System", CACM 1978):                                       *)
(*                                                                         *)
(*   Each process i keeps                                                  *)
(*     * a Lamport logical clock  clock[i] : Nat                          *)
(*     * a request queue          queue[i] : set of <<ts, proc>> entries  *)
(*       ordered by (ts, proc) -- the timestamp-ordered request queue.    *)
(*                                                                         *)
(*   Messages travel over per-(sender,receiver) **FIFO** channels. FIFO    *)
(*   ordering is ESSENTIAL to Lamport's algorithm and is modelled here     *)
(*   explicitly as a SEQUENCE per ordered pair: a receiver consumes only   *)
(*   the HEAD of a channel, so messages from j to i arrive in send order.  *)
(*   (An earlier draft used an unordered message set; TLC found a genuine  *)
(*   two-in-CS counterexample, demonstrating that Lamport mutual exclusion *)
(*   REQUIRES FIFO channels -- without them a process can hear a process's *)
(*   later ACK before that process's earlier REQUEST and enter wrongly.)   *)
(*                                                                         *)
(*   Three message types:                                                  *)
(*     REQUEST(ts, j)  -- j wants the critical section, timestamped ts     *)
(*     ACK(ts, j)      -- j acknowledges, carrying j's current clock ts    *)
(*     RELEASE(ts, j)  -- j has left the CS; drop j's request everywhere   *)
(*                                                                         *)
(*   To ENTER the critical section, process i must:                       *)
(*     (R1) have its OWN request <<myTS, i>> at the HEAD of queue[i]       *)
(*          (lowest (ts, proc), ties broken by process id), AND            *)
(*     (R2) have received a message with timestamp GREATER than myTS from  *)
(*          EVERY other process j. Under FIFO channels this guarantees i    *)
(*          has already received any REQUEST j sent before that message,   *)
(*          so no request from j earlier than myTS can still be in flight  *)
(*          toward i -- queue[i]'s head is then the global minimum.        *)
(*   Both conditions together are Lamport's mutual-exclusion rule.        *)
(*                                                                         *)
(*   On EXIT, i removes its own request and broadcasts RELEASE; each       *)
(*   receiver dequeues i's request from its own queue.                    *)
(*                                                                         *)
(* CLOCK RULE. Every message receipt sets clock := max(clock, msg.ts) + 1; *)
(* every local "event" (issuing a request, sending) bumps clock by 1.     *)
(*                                                                         *)
(* MODELLING CHOICES for a SMALL, EXHAUSTIVELY-CHECKABLE state space:     *)
(*   * Channels: chan[j][i] is a SEQUENCE of message records in flight     *)
(*     from j to i (FIFO). Receivers consume Head(chan[j][i]).            *)
(*   * "Heard a greater timestamp from every other process" (R2) is        *)
(*     tracked compactly via heardTS[i][j] = the largest timestamp i has   *)
(*     ever seen in a message FROM j. An ACK carries the acker's bumped    *)
(*     clock, strictly greater than the request's ts; under FIFO the ACK   *)
(*     cannot overtake an earlier REQUEST from the same acker.            *)
(*   * The Lamport clock and the number of CS visits are BOUNDED          *)
(*     (MaxClock, MaxVisits) so TLC terminates with a finite graph.       *)
(*                                                                         *)
(* PROPERTIES model-checked by TLC (see LamportMutex.cfg):                *)
(*   TypeOK            -- state stays well-typed.                          *)
(*   MutualExclusion   -- THE core safety goal: at most ONE process is in  *)
(*                        the critical section in any reachable state.     *)
(*   CSHasRequest      -- a process in the CS holds a live request entry    *)
(*                        in its own queue (queue/state bookkeeping is      *)
(*                        consistent with CS occupancy).                    *)
(*   ClockMonotone     -- (action property) no clock ever decreases.       *)
(*                                                                         *)
(* The model is NON-VACUOUS: CSReachable (each process actually ENTERS    *)
(* the CS) is shown reachable, so MutualExclusion is not vacuously true.   *)
(* A mutation that DROPS condition (R2) -- enter as soon as your request   *)
(* is at the queue head -- produces a genuine two-in-CS counterexample,    *)
(* proving the invariant has teeth.                                        *)
(***************************************************************************)
EXTENDS Naturals, Sequences, FiniteSets, TLC

CONSTANTS
    Processes,   \* finite set of process ids (e.g. {p1, p2})
    MaxClock,    \* bound on every Lamport clock (bounds the state space)
    MaxVisits    \* bound on total CS entries across all processes

ASSUME MaxClock \in Nat
ASSUME MaxVisits \in Nat

----------------------------------------------------------------------------
\* Process-control states.
\* "idle"  : not interested in the CS.
\* "wait"  : has issued a REQUEST, waiting to satisfy (R1) /\ (R2).
\* "crit"  : in the critical section.
PCStates == {"idle", "wait", "crit"}

VARIABLES
    clock,    \* clock[i]   : Nat                 -- i's Lamport logical clock
    pc,       \* pc[i]      : PCStates            -- i's control state
    reqTS,    \* reqTS[i]   : Nat                 -- ts of i's current request (0 = none)
    queue,    \* queue[i]   : SUBSET (Nat \X Processes) -- requests i knows of
    heardTS,  \* heardTS[i] : [Processes -> Nat]  -- max ts i has heard FROM each proc
    chan,     \* chan[j][i] : Seq(MsgRecord)      -- FIFO channel j -> i
    visits    \* visits     : Nat                 -- total CS entries so far (<= MaxVisits)

vars == <<clock, pc, reqTS, queue, heardTS, chan, visits>>

----------------------------------------------------------------------------
\* Lamport total order on request entries <<ts, proc>>: lower ts first, ties
\* broken by process id. We need a total order on Processes; CHOOSE a fixed
\* injection into the naturals so ties are broken deterministically and
\* SYMMETRY remains sound (the order is over a *fixed* but arbitrary id rank).
PidRank == CHOOSE f \in [Processes -> 1..Cardinality(Processes)] :
               \A a, b \in Processes : (a # b) => (f[a] # f[b])

\* Strict precedence on entries e1, e2 \in (Nat \X Processes).
EntryLess(e1, e2) ==
    \/ e1[1] < e2[1]
    \/ (e1[1] = e2[1] /\ PidRank[e1[2]] < PidRank[e2[2]])

\* i's OWN request entry (only meaningful when reqTS[i] > 0).
MyEntry(i) == <<reqTS[i], i>>

\* i's own request is the minimum (head) of its queue.
IsHead(i) ==
    /\ reqTS[i] > 0
    /\ MyEntry(i) \in queue[i]
    /\ \A e \in queue[i] : (e = MyEntry(i)) \/ EntryLess(MyEntry(i), e)

\* (R2): i has heard a STRICTLY GREATER timestamp than its own request from
\* every OTHER process. heardTS[i][j] is the largest ts i has seen from j.
HeardLaterFromAll(i) ==
    \A j \in Processes \ {i} : heardTS[i][j] > reqTS[i]

\* Lamport entry condition = (R1) /\ (R2).
CanEnter(i) == IsHead(i) /\ HeardLaterFromAll(i)

\* Bump a clock, capped at MaxClock so the state space stays finite. (The cap
\* never falsifies safety: it only removes far-future behaviours.)
Bump(c) == IF c < MaxClock THEN c + 1 ELSE MaxClock

\* Receive-side clock rule: max(local, incoming) then +1 (capped).
MergeClock(c, ts) == Bump(IF c >= ts THEN c ELSE ts)

----------------------------------------------------------------------------
\* Message constructors (records placed on the FIFO channels).
ReqMsg(ts, from) == [type |-> "req", ts |-> ts, from |-> from]
AckMsg(ts, from) == [type |-> "ack", ts |-> ts, from |-> from]
RelMsg(ts, from) == [type |-> "rel", ts |-> ts, from |-> from]

Others(i) == Processes \ {i}

\* Broadcast: append message `m` to every FIFO channel from `frm` to each
\* OTHER process, leaving all other channels unchanged.
BroadcastFrom(c, frm, m) ==
    [ a \in Processes |->
        [ b \in Processes |->
            IF a = frm /\ b # frm THEN Append(c[a][b], m) ELSE c[a][b] ] ]

----------------------------------------------------------------------------
Init ==
    /\ clock   = [i \in Processes |-> 0]
    /\ pc      = [i \in Processes |-> "idle"]
    /\ reqTS   = [i \in Processes |-> 0]
    /\ queue   = [i \in Processes |-> {}]
    /\ heardTS = [i \in Processes |-> [j \in Processes |-> 0]]
    /\ chan    = [j \in Processes |-> [i \in Processes |-> << >>]]
    /\ visits  = 0

----------------------------------------------------------------------------
\* ACTIONS

\* (1) REQUEST: idle process i issues a timestamped request. It bumps its
\* clock, stamps ts = new clock, enqueues its own entry locally, and FIFO-sends
\* a REQUEST to every other process. Bounded by the clock and visit budgets so
\* fresh requests stop once the state space is fully explored.
Request(i) ==
    /\ pc[i] = "idle"
    /\ clock[i] < MaxClock          \* a request needs a fresh (bumpable) tick
    /\ visits < MaxVisits           \* don't start requests we can't budget a visit for
    /\ LET ts == Bump(clock[i]) IN
         /\ clock' = [clock EXCEPT ![i] = ts]
         /\ pc'    = [pc    EXCEPT ![i] = "wait"]
         /\ reqTS' = [reqTS EXCEPT ![i] = ts]
         /\ queue' = [queue EXCEPT ![i] = @ \cup {<<ts, i>>}]
         /\ chan'  = BroadcastFrom(chan, i, ReqMsg(ts, i))
    /\ UNCHANGED <<heardTS, visits>>

\* (2) RECEIVE REQUEST: process i consumes the HEAD REQUEST(ts, j) of channel
\* j -> i. It merges the clock, records j's request in its queue, records that
\* it has heard ts from j, and FIFO-replies ACK carrying its freshly-bumped
\* clock (> ts). Under FIFO, this ACK cannot reach j before any earlier REQUEST
\* j-relevant message -- the property Lamport's algorithm depends on.
RecvRequest(i, j) ==
    /\ Len(chan[j][i]) > 0
    /\ Head(chan[j][i]).type = "req"
    /\ LET m == Head(chan[j][i])
           c == MergeClock(clock[i], m.ts)
       IN
         /\ clock'   = [clock   EXCEPT ![i] = c]
         /\ queue'   = [queue   EXCEPT ![i] = @ \cup {<<m.ts, j>>}]
         /\ heardTS' = [heardTS EXCEPT ![i][j] = IF @ >= m.ts THEN @ ELSE m.ts]
         /\ chan'    = [chan EXCEPT ![j][i] = Tail(@), ![i][j] = Append(@, AckMsg(c, i))]
    /\ UNCHANGED <<pc, reqTS, visits>>

\* (3) RECEIVE ACK: process i consumes the HEAD ACK(ts, j) of channel j -> i.
\* Merge clock and record having heard ts from j. ts > the request i sent, so
\* collecting all acks drives HeardLaterFromAll(i) true -> (R2).
RecvAck(i, j) ==
    /\ Len(chan[j][i]) > 0
    /\ Head(chan[j][i]).type = "ack"
    /\ LET m == Head(chan[j][i]) IN
         /\ clock'   = [clock   EXCEPT ![i] = MergeClock(clock[i], m.ts)]
         /\ heardTS' = [heardTS EXCEPT ![i][j] = IF @ >= m.ts THEN @ ELSE m.ts]
         /\ chan'    = [chan EXCEPT ![j][i] = Tail(@)]
    /\ UNCHANGED <<pc, reqTS, queue, visits>>

\* (4) RECEIVE RELEASE: process i consumes the HEAD RELEASE(ts, j) of channel
\* j -> i. Merge clock, record heard ts from j (RELEASE also carries j's clock),
\* and drop j's request entry from i's queue.
RecvRelease(i, j) ==
    /\ Len(chan[j][i]) > 0
    /\ Head(chan[j][i]).type = "rel"
    /\ LET m == Head(chan[j][i]) IN
         /\ clock'   = [clock   EXCEPT ![i] = MergeClock(clock[i], m.ts)]
         /\ heardTS' = [heardTS EXCEPT ![i][j] = IF @ >= m.ts THEN @ ELSE m.ts]
         /\ queue'   = [queue   EXCEPT ![i] = { e \in @ : e[2] # j }]
         /\ chan'    = [chan EXCEPT ![j][i] = Tail(@)]
    /\ UNCHANGED <<pc, reqTS, visits>>

\* (5) ENTER the critical section. Both Lamport conditions hold.
Enter(i) ==
    /\ pc[i] = "wait"
    /\ CanEnter(i)
    /\ visits < MaxVisits          \* CS-entry budget (bounds the state space)
    /\ pc'     = [pc EXCEPT ![i] = "crit"]
    /\ visits' = visits + 1
    /\ UNCHANGED <<clock, reqTS, queue, heardTS, chan>>

\* (6) EXIT the critical section: drop own request locally, broadcast RELEASE
\* carrying i's bumped clock.
Exit(i) ==
    /\ pc[i] = "crit"
    /\ LET myE == MyEntry(i)
           ts  == Bump(clock[i])
       IN
         /\ clock' = [clock EXCEPT ![i] = ts]
         /\ pc'    = [pc    EXCEPT ![i] = "idle"]
         /\ reqTS' = [reqTS EXCEPT ![i] = 0]
         /\ queue' = [queue EXCEPT ![i] = @ \ {myE}]
         /\ chan'  = BroadcastFrom(chan, i, RelMsg(ts, i))
    /\ UNCHANGED <<heardTS, visits>>

Next ==
    \/ \E i \in Processes : Request(i)
    \/ \E i \in Processes : Enter(i)
    \/ \E i \in Processes : Exit(i)
    \/ \E i, j \in Processes : RecvRequest(i, j)
    \/ \E i, j \in Processes : RecvAck(i, j)
    \/ \E i, j \in Processes : RecvRelease(i, j)

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
\* ClockMonotone -- no Lamport clock ever decreases on any step. ACTION
\* property, checked via PROPERTY. Every action either bumps or merge-bumps.
ClockMonotone ==
    [][ \A i \in Processes : clock[i] <= clock'[i] ]_vars

\* Process ids are interchangeable, so TLC may quotient the state graph by
\* permutations of Processes. Sound: every action treats ids uniformly and
\* MutualExclusion is invariant under permuting which process holds the CS.
Symmetry == Permutations(Processes)

----------------------------------------------------------------------------
\* INVARIANTS

\* (1) Type correctness.
MsgRecords ==
    [ type : {"req","ack","rel"}, ts : 0..MaxClock, from : Processes ]

TypeOK ==
    /\ clock   \in [Processes -> 0..MaxClock]
    /\ pc      \in [Processes -> PCStates]
    /\ reqTS   \in [Processes -> 0..MaxClock]
    /\ queue   \in [Processes -> SUBSET ((0..MaxClock) \X Processes)]
    /\ heardTS \in [Processes -> [Processes -> 0..MaxClock]]
    /\ chan    \in [Processes -> [Processes -> Seq(MsgRecords)]]
    /\ visits  \in 0..MaxVisits

\* (2) MutualExclusion -- THE core property: at most one process in the CS.
InCS == { i \in Processes : pc[i] = "crit" }
MutualExclusion == Cardinality(InCS) <= 1

\* (3) CSHasRequest -- a process IN the CS still holds its own live request
\* entry in its own queue. (A non-vacuous structural check that CS occupancy is
\* backed by a real outstanding request: it would fail if a process entered the
\* CS without a request, or had its own entry dropped while holding the lock.)
CSHasRequest ==
    \A i \in Processes :
      (pc[i] = "crit") => (reqTS[i] > 0 /\ MyEntry(i) \in queue[i])

----------------------------------------------------------------------------
\* NON-VACUITY witnesses (proved reachable by the reachability probe run):
\* each process can actually ENTER the CS, so MutualExclusion is not vacuous.
CSReachable == \E i \in Processes : pc[i] = "crit"

============================================================================
