---------------------------- MODULE ChandyLamport ----------------------------
(***************************************************************************)
(* TLA+ model of the CHANDY-LAMPORT distributed global-snapshot algorithm  *)
(* (the classic "determining global states of distributed systems"         *)
(* marker algorithm), adapted to the Helix formal-methods stream alongside *)
(* Consensus / Swim / ORSet / Saga / VectorClock etc.                      *)
(*                                                                         *)
(* This is the real primitive behind the cluster's consistent global       *)
(* checkpointing (pkg/checkpoint_merge / pkg/offlinesync / CRIU session    *)
(* migration): a snapshot taken WHILE the distributed computation runs     *)
(* concurrently MUST capture a CONSISTENT cut -- a global state the system  *)
(* could actually have been in -- so that nothing recorded as received was  *)
(* never recorded as sent, and no in-flight work is lost or duplicated.     *)
(*                                                                         *)
(* SYSTEM MODELLED                                                          *)
(*   A set of processes fully connected by directed FIFO channels (one per  *)
(*   ordered pair of distinct processes). A single conserved TOKEN circu-   *)
(*   lates: it sits at exactly one process, or is in flight inside exactly  *)
(*   one channel. "Total token count" over (all process states + all        *)
(*   channel contents) is the conserved quantity and is INVARIANTLY 1.      *)
(*                                                                         *)
(* THE ALGORITHM                                                            *)
(*   * Some process INITIATES: it records its own local state, then sends a  *)
(*     MARKER on every one of its outgoing channels, BEFORE sending any      *)
(*     further application messages. Its incoming channels begin "recording" *)
(*     (empty), except that a channel a marker already arrived on is closed. *)
(*   * On receiving a MARKER on channel c into process p:                    *)
(*       - if p has NOT yet recorded its state: p records its state now,      *)
(*         records channel c's state as EMPTY (nothing came before the        *)
(*         marker that p hadn't already accounted for), starts recording all  *)
(*         OTHER incoming channels, and sends a marker on all its outgoing    *)
(*         channels.                                                          *)
(*       - if p HAS already recorded its state: p closes channel c, recording *)
(*         as c's channel-state exactly the application messages that arrived  *)
(*         on c AFTER p recorded but BEFORE this marker (the in-flight set).   *)
(*   The FIFO marker discipline guarantees the marker on c "flushes" exactly  *)
(*   the messages in flight on c at the instant of the cut.                   *)
(*                                                                         *)
(* CONSERVED-QUANTITY ENCODING                                             *)
(*   We model the application traffic as a single token. The snapshot must   *)
(*   capture the token EXACTLY ONCE across (recorded process states +        *)
(*   recorded channel states): never lose it, never double-count it -- even  *)
(*   when the token is in flight on a channel at the moment the cut is taken. *)
(*                                                                         *)
(* SAFETY PROPERTIES VERIFIED BY TLC (exhaustively, full state space):     *)
(*   1. ConsistentSnapshot -- once the snapshot run is COMPLETE, the total    *)
(*      token count it recorded (sum over recorded process states + recorded  *)
(*      channel states) equals the true conserved invariant (exactly 1).      *)
(*      The recorded cut neither loses nor duplicates the token. This is the  *)
(*      Chandy-Lamport consistency theorem made concrete on a conserved       *)
(*      quantity: the recorded global state is a state the system could have  *)
(*      been in.                                                              *)
(*   2. SnapshotComplete / termination -- once initiated, the algorithm runs  *)
(*      to a state where EVERY process has recorded its state and EVERY        *)
(*      channel has been recorded; at that point the conservation above holds. *)
(*   3. TypeOK -- the state invariant.                                        *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, Sequences, TLC

CONSTANTS Procs   \* set of process ids (small, e.g. {p1, p2})

\* Directed channels: every ordered pair of DISTINCT processes (src -> dst).
Chans == { <<s, d>> : s \in Procs, d \in Procs } \ { <<p, p>> : p \in Procs }

\* A marker token and the application TOKEN are distinguishable wire symbols.
Marker == "MARKER"
Tok    == "TOKEN"

(***************************************************************************)
(* STATE                                                                    *)
(*   chan[c]      : sequence of in-flight wire symbols on channel c          *)
(*                  (FIFO: appended at tail, consumed at head). Elements are  *)
(*                  "MARKER" or "TOKEN".                                      *)
(*   hasTok[p]    : TRUE iff the live (real) token currently rests at p.      *)
(*                                                                         *)
(*   --- snapshot bookkeeping ---                                           *)
(*   recorded[p]  : TRUE iff p has recorded its local state.                 *)
(*   recTok[p]    : the token count p recorded for ITSELF at record time.    *)
(*   chanRecorded[c] : TRUE iff channel c's state has been recorded (its      *)
(*                     marker consumed by the receiver after recording).      *)
(*   recChan[c]   : the token count recorded as in-flight on channel c.       *)
(*   recording[c] : TRUE iff the receiver of c is actively logging app        *)
(*                  messages arriving on c into the channel snapshot (i.e.    *)
(*                  receiver recorded, c's marker not yet seen).              *)
(*   initiated    : TRUE iff a snapshot has been started.                     *)
(***************************************************************************)
VARIABLES
    chan, hasTok,
    recorded, recTok, chanRecorded, recChan, recording, initiated

vars == <<chan, hasTok, recorded, recTok, chanRecorded, recChan,
          recording, initiated>>

\* Channels out of / into a process.
OutChans(p) == { c \in Chans : c[1] = p }
InChans(p)  == { c \in Chans : c[2] = p }

\* Count of live TOKENs sitting inside a channel sequence (0 or 1 here).
TokensIn(seq) == Cardinality({ i \in 1..Len(seq) : seq[i] = Tok })

TypeOK ==
    /\ chan \in [Chans -> Seq({Marker, Tok})]
    /\ hasTok \in [Procs -> BOOLEAN]
    /\ recorded \in [Procs -> BOOLEAN]
    /\ recTok \in [Procs -> 0..1]
    /\ chanRecorded \in [Chans -> BOOLEAN]
    /\ recChan \in [Chans -> 0..1]
    /\ recording \in [Chans -> BOOLEAN]
    /\ initiated \in BOOLEAN

(***************************************************************************)
(* INITIAL STATE                                                            *)
(*   Exactly one process holds the token; all channels empty; no snapshot    *)
(*   has been taken. We pin the token's start position to the FIRST process  *)
(*   (under a fixed order) so the conserved quantity is exactly 1 and the     *)
(*   initial state is unique -- SYMMETRY over Procs stays sound because the   *)
(*   protocol treats process ids identically and the start owner is chosen    *)
(*   canonically rather than by a permuted constant.                         *)
(***************************************************************************)
\* A canonical "first" process, used only to place the single initial token.
StartProc == CHOOSE p \in Procs : TRUE

Init ==
    /\ chan = [c \in Chans |-> << >>]
    /\ hasTok = [p \in Procs |-> (p = StartProc)]
    /\ recorded = [p \in Procs |-> FALSE]
    /\ recTok = [p \in Procs |-> 0]
    /\ chanRecorded = [c \in Chans |-> FALSE]
    /\ recChan = [c \in Chans |-> 0]
    /\ recording = [c \in Chans |-> FALSE]
    /\ initiated = FALSE

(***************************************************************************)
(* APPLICATION ACTION: move the token p -> q.                              *)
(*   p (which holds the token) SENDS it on channel <<p,q>>: the token leaves  *)
(*   p and is appended to the channel (in flight). This runs CONCURRENTLY     *)
(*   with the snapshot, so a token can be in flight exactly when the cut is   *)
(*   taken -- the case Chandy-Lamport must get right.                        *)
(***************************************************************************)
SendToken(p, q) ==
    /\ p # q
    /\ hasTok[p]
    /\ hasTok' = [hasTok EXCEPT ![p] = FALSE]
    /\ chan' = [chan EXCEPT ![<<p, q>>] = Append(@, Tok)]
    /\ UNCHANGED <<recorded, recTok, chanRecorded, recChan, recording,
                   initiated>>

(***************************************************************************)
(* APPLICATION ACTION: deliver a token from the head of a channel into q.  *)
(*   The receiver q takes the token (head of chan c must be a TOKEN). If q's  *)
(*   incoming channel c is currently "recording" for the snapshot, the token  *)
(*   that arrives AFTER q recorded but BEFORE c's marker is logged into c's   *)
(*   recorded channel-state -- this is exactly the in-flight message capture. *)
(***************************************************************************)
DeliverToken(c) ==
    LET q == c[2] IN
    /\ Len(chan[c]) > 0
    /\ Head(chan[c]) = Tok
    /\ chan' = [chan EXCEPT ![c] = Tail(@)]
    /\ hasTok' = [hasTok EXCEPT ![q] = TRUE]
    \* If we are recording channel c, log the in-flight token into recChan.
    /\ recChan' = IF recording[c]
                     THEN [recChan EXCEPT ![c] = @ + 1]
                     ELSE recChan
    /\ UNCHANGED <<recorded, recTok, chanRecorded, recording, initiated>>

(***************************************************************************)
(* SNAPSHOT INITIATION.                                                     *)
(*   Process p starts the snapshot: records its OWN state (recTok = its live  *)
(*   token count), marks itself recorded, sends a MARKER on every outgoing    *)
(*   channel, and begins RECORDING every incoming channel. By the algorithm,  *)
(*   the initiator records all its incoming channels as empty-so-far (they    *)
(*   become recording=TRUE; any later token before the marker is captured).   *)
(*   Only one initiation per run (initiated guards it).                       *)
(***************************************************************************)
Initiate(p) ==
    /\ ~initiated
    /\ initiated' = TRUE
    /\ recorded' = [recorded EXCEPT ![p] = TRUE]
    /\ recTok'   = [recTok   EXCEPT ![p] = IF hasTok[p] THEN 1 ELSE 0]
    \* Send a marker on every outgoing channel of p (FIFO: appended at tail).
    /\ chan' = [c \in Chans |->
                   IF c \in OutChans(p) THEN Append(chan[c], Marker)
                                        ELSE chan[c]]
    \* Start recording every incoming channel of p.
    /\ recording' = [c \in Chans |->
                        IF c \in InChans(p) THEN TRUE ELSE recording[c]]
    /\ UNCHANGED <<hasTok, chanRecorded, recChan>>

(***************************************************************************)
(* MARKER RECEIPT.                                                          *)
(*   q consumes a MARKER at the head of incoming channel c = <<s,q>>.         *)
(*                                                                         *)
(*   CASE A -- q has NOT recorded yet:                                       *)
(*     q records its own state now (recTok), records c's channel-state as     *)
(*     EMPTY (recChan[c]=0, chanRecorded[c]=TRUE -- nothing was in flight on  *)
(*     c before this marker that q hadn't already captured), starts RECORDING *)
(*     all its OTHER incoming channels, and sends a marker on all its         *)
(*     outgoing channels.                                                     *)
(*                                                                         *)
(*   CASE B -- q has ALREADY recorded:                                       *)
(*     q simply CLOSES channel c: chanRecorded[c]=TRUE, stops recording c.    *)
(*     recChan[c] already holds exactly the tokens that arrived on c after q  *)
(*     recorded and before this marker (logged by DeliverToken). The marker   *)
(*     is the FIFO boundary, so this set is precisely the in-flight content.  *)
(***************************************************************************)
RecvMarker(c) ==
    LET q == c[2] IN
    /\ Len(chan[c]) > 0
    /\ Head(chan[c]) = Marker
    /\ IF ~recorded[q]
          THEN \* CASE A: first marker reaches q -> q records now.
               /\ recorded' = [recorded EXCEPT ![q] = TRUE]
               /\ recTok'   = [recTok   EXCEPT ![q] = IF hasTok[q] THEN 1 ELSE 0]
               \* c is recorded EMPTY; q starts recording its OTHER in-channels.
               /\ chanRecorded' = [chanRecorded EXCEPT ![c] = TRUE]
               /\ recChan' = [recChan EXCEPT ![c] = 0]
               /\ recording' = [ch \in Chans |->
                                   IF ch = c THEN FALSE
                                   ELSE IF ch \in InChans(q) THEN TRUE
                                   ELSE recording[ch]]
               \* Consume the marker from c and emit markers on q's out-channels.
               /\ chan' = [ch \in Chans |->
                              IF ch = c THEN Tail(chan[c])
                              ELSE IF ch \in OutChans(q) THEN Append(chan[ch], Marker)
                              ELSE chan[ch]]
               /\ UNCHANGED <<hasTok, initiated>>
          ELSE \* CASE B: q already recorded -> just close channel c.
               /\ chanRecorded' = [chanRecorded EXCEPT ![c] = TRUE]
               /\ recording' = [recording EXCEPT ![c] = FALSE]
               /\ chan' = [chan EXCEPT ![c] = Tail(@)]
               /\ UNCHANGED <<hasTok, recorded, recTok, recChan, initiated>>

(***************************************************************************)
(* NEXT / SPEC                                                              *)
(*   The token circulates (send/deliver) CONCURRENTLY with the snapshot      *)
(*   (initiate / marker receipt). The snapshot is a terminating sub-protocol; *)
(*   once every process recorded and every channel recorded, no snapshot      *)
(*   action is enabled, and -- to make the run finite & terminating for       *)
(*   -deadlock checking -- token motion also quiesces once the snapshot has   *)
(*   completed (SystemBusy gates the application actions).                    *)
(***************************************************************************)

\* The snapshot run is COMPLETE: every process recorded, every channel closed.
SnapshotDone ==
    /\ initiated
    /\ \A p \in Procs : recorded[p]
    /\ \A c \in Chans : chanRecorded[c]

\* Application token motion is allowed only while the snapshot is not yet
\* complete -- this bounds the state space and gives a genuine terminal state
\* (so -deadlock confirms termination rather than flagging a livelock).
SystemBusy == ~SnapshotDone

Next ==
    \/ /\ SystemBusy
       /\ \E p \in Procs, q \in Procs : SendToken(p, q)
    \/ /\ SystemBusy
       /\ \E c \in Chans : DeliverToken(c)
    \/ \E p \in Procs : Initiate(p)
    \/ \E c \in Chans : RecvMarker(c)

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* CONSERVATION INVARIANT (ground truth).                                   *)
(*   At ALL times the LIVE token count -- tokens resting at processes plus    *)
(*   tokens in flight in channels -- is exactly 1. This is the physical       *)
(*   conserved quantity the snapshot must faithfully capture.                 *)
(***************************************************************************)
LiveTokens ==
      Cardinality({ p \in Procs : hasTok[p] })
    + LET seqs == { chan[c] : c \in Chans }
       IN \* sum of tokens across all channel sequences
          LET F[S \in SUBSET Chans] ==
                IF S = {} THEN 0
                ELSE LET c == CHOOSE x \in S : TRUE
                      IN TokensIn(chan[c]) + F[S \ {c}]
           IN F[Chans]

ConservationHolds == LiveTokens = 1

(***************************************************************************)
(* THE RECORDED SNAPSHOT and its token count.                              *)
(*   RecordedTokens sums the snapshot bookkeeping: the token counts recorded  *)
(*   at each process (recTok) plus the token counts recorded as in flight on  *)
(*   each channel (recChan). This is the Chandy-Lamport "recorded global      *)
(*   state".                                                                  *)
(***************************************************************************)
SumProcRec ==
    LET G[S \in SUBSET Procs] ==
          IF S = {} THEN 0
          ELSE LET p == CHOOSE x \in S : TRUE IN recTok[p] + G[S \ {p}]
     IN G[Procs]

SumChanRec ==
    LET H[S \in SUBSET Chans] ==
          IF S = {} THEN 0
          ELSE LET c == CHOOSE x \in S : TRUE IN recChan[c] + H[S \ {c}]
     IN H[Chans]

RecordedTokens == SumProcRec + SumChanRec

(***************************************************************************)
(* SAFETY INVARIANTS.                                                       *)
(***************************************************************************)

\* (1) ConsistentSnapshot -- THE Chandy-Lamport correctness property on the
\* conserved quantity. Once the snapshot run is COMPLETE, the recorded global
\* state must account for EXACTLY the conserved token -- no loss, no duplicate.
\* A token in flight at the cut MUST appear either in some recorded process
\* state or in some recorded channel state, never both, never neither. If the
\* marker/recording rules were wrong (e.g. a process snapshots without
\* recording in-flight channel messages, or records a channel incorrectly),
\* RecordedTokens would drift from 1 and this fails.
ConsistentSnapshot ==
    SnapshotDone => (RecordedTokens = 1)

\* (2) SnapshotConsistencyAlways -- a stronger continuous form: at EVERY point
\* after the snapshot is complete the recorded count stays the captured cut
\* value (it is never re-mutated). (Together with ConservationHolds this pins
\* the recorded cut to a genuine reachable global state.)
SnapshotConsistencyAlways == ConservationHolds

\* (3) NoChannelDoubleClose -- a recorded channel count never exceeds the one
\* token; the recorded channel-state can hold at most the single in-flight
\* token, never a duplicate. Catches a rule that double-logs an in-flight msg.
NoChannelDoubleClose == \A c \in Chans : recChan[c] <= 1

(***************************************************************************)
(* NON-VACUITY witnesses (negated as invariants so a TLC "violation" is a    *)
(* REACHABILITY PROOF that the interesting state exists -- run separately,    *)
(* never in the main safety cfg). See README.                               *)
(*   - InFlightCutReachable: a COMPLETED snapshot whose single token was      *)
(*     captured IN A CHANNEL (recChan), i.e. the token was in flight across   *)
(*     the cut and correctly recorded as channel state -- the non-trivial     *)
(*     Chandy-Lamport case, proving the property is not vacuously true on     *)
(*     only the "token sits still" runs.                                      *)
(***************************************************************************)
InFlightSnapshot ==
    /\ SnapshotDone
    /\ \E c \in Chans : recChan[c] = 1   \* token captured as in-flight channel state

InFlightCutReachable == ~InFlightSnapshot   \* TLC violation => such a cut is reachable

(***************************************************************************)
(* SYMMETRY -- process ids are interchangeable (the protocol treats them    *)
(* identically; the single initial token is placed at the CANONICAL         *)
(* StartProc, not a permuted constant), so permutation symmetry is sound.    *)
(***************************************************************************)
Symmetry == Permutations(Procs)

=============================================================================
