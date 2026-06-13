------------------------- MODULE RaftReadIndex -------------------------
(***************************************************************************)
(* TLA+ model of RAFT ReadIndex linearizable reads -- the lease-FREE       *)
(* mechanism a Raft leader uses to serve a strongly-consistent (read-      *)
(* index) read WITHOUT a fast-path lease, by confirming through a fresh    *)
(* heartbeat round that it is STILL the leader before answering.           *)
(*                                                                         *)
(* WHY ReadIndex (the real mechanism this verifies): to serve a            *)
(* linearizable read a leader cannot simply read its local state machine,  *)
(* because it may already have been deposed by a newer leader that         *)
(* committed newer writes -- reading locally would then MISS those writes  *)
(* (a stale read). The ReadIndex protocol is:                              *)
(*                                                                         *)
(*   (1) record readIndex := the leader's current commitIndex;             *)
(*   (2) confirm leadership by exchanging heartbeats with a QUORUM in the   *)
(*       leader's CURRENT term -- if a quorum acks, no newer leader can     *)
(*       have been elected (a new election needs a quorum at a higher term, *)
(*       and the two quorums must intersect), so the leader is still the    *)
(*       real leader and readIndex is at least as large as any write        *)
(*       committed before the read began;                                  *)
(*   (3) wait until the local state machine has APPLIED up to readIndex,    *)
(*       then serve the read at readIndex.                                  *)
(*                                                                         *)
(* THE HAZARD this spec pins down: a STALE leader -- one whose term has     *)
(* been superseded by a NEWER leader that has since committed a newer write *)
(* -- must NOT serve a read at its own (stale) commitIndex WITHOUT the      *)
(* heartbeat-quorum confirmation. The heartbeat round of step (2) is the    *)
(* whole point: a stale leader can no longer collect a quorum of acks IN    *)
(* ITS OWN TERM (the quorum has moved to the new leader's higher term), so  *)
(* the confirmation FAILS and the stale read is prevented. Skipping step (2)*)
(* (the mutation, ConfirmHeartbeat = FALSE) lets the stale leader serve at  *)
(* its stale commitIndex and TLC produces the STALE-READ counterexample.   *)
(*                                                                         *)
(* ABSTRACTION (deliberately NOT full Raft log replication): we model the   *)
(* read-consistency safety property, not log entries. State:               *)
(*   * Servers: a tiny finite set (3 in the cfg) -- enough for two distinct *)
(*     quorums across two terms, which is what creates the stale-leader     *)
(*     hazard.                                                              *)
(*   * currentTerm[s]   : the term server s believes itself in.            *)
(*   * commitIndex[s]   : highest write index s knows is committed (its     *)
(*     local view; a deposed leader's view is FROZEN behind the global).    *)
(*   * applied[s]       : highest write index s has applied to its state    *)
(*     machine (applied <= commitIndex always).                            *)
(*   * leader           : the server id that is the CURRENT real leader, or *)
(*     Nil. A leader is elected at a strictly-higher term with quorum       *)
(*     support; electing a new leader does NOT reset the old leader's       *)
(*     currentTerm/commitIndex -- it stays STALE, modelling the deposed     *)
(*     leader that has not yet heard about the new term.                    *)
(*   * leaderTerm       : the term of the current real leader.             *)
(*   * committed        : GLOBAL count of committed writes (the ground      *)
(*     truth / latest linearization point). Only the real leader commits.   *)
(*   * lastReadValue    : value (commitIndex) returned by the most recent    *)
(*     served ReadIndex read, or Nil.                                       *)
(*   * lastReadCommitted: the GLOBAL committed count at the instant that     *)
(*     read was served -- the value a linearizable read MUST have returned. *)
(*                                                                         *)
(* A read is served only via ServeRead(s), whose guard requires that s is   *)
(* the real leader AND (when the heartbeat gate is enabled) that s has just *)
(* confirmed leadership via a heartbeat quorum in its current term          *)
(* (hbConfirmed[s] /\ currentTerm[s] = leaderTerm). The served value is     *)
(* applied[s]; lastReadCommitted records committed at that instant, so the  *)
(* staleness test (lastReadValue = lastReadCommitted) is exact.            *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Servers,          \* finite set of server ids (model values)
          MaxTerm,          \* highest term / election count (bounds space)
          MaxWrites,        \* highest committed-write count (bounds space)
          ConfirmHeartbeat, \* TRUE: ReadIndex confirms via heartbeat quorum
                            \* FALSE (mutation): skip step (2) -> stale reads
          Nil               \* sentinel: "no leader" / "no read yet"

ASSUME Nil \notin Servers
ASSUME ConfirmHeartbeat \in BOOLEAN
ASSUME MaxTerm \in Nat /\ MaxTerm > 0

Terms   == 0..MaxTerm
Indices == 0..MaxWrites

\* A quorum is any strict majority of servers. Any two quorums intersect --
\* the structural fact that makes ReadIndex's heartbeat confirmation sound.
Quorum == {Q \in SUBSET Servers : Cardinality(Q) * 2 > Cardinality(Servers)}

VARIABLES
    currentTerm,        \* currentTerm[s]: term server s believes it is in
    commitIndex,        \* commitIndex[s]: s's local view of committed index
    applied,            \* applied[s]: highest index s applied to its SM
    leader,             \* current REAL leader id, or Nil
    leaderTerm,         \* term of the current real leader
    committed,          \* GLOBAL committed-write count (ground truth)
    believesLeader,     \* believesLeader[s]: s THINKS it is the leader. A
                        \* deposed leader keeps this TRUE (its local belief)
                        \* until it learns of the newer term -- this is the
                        \* server that would serve a STALE read if ungated.
    hbConfirmed,        \* hbConfirmed[s]: s holds a FRESH heartbeat-quorum
                        \* confirmation in its current term (step (2) done)
    lastReadValue,      \* value returned by most recent ReadIndex read / Nil
    lastReadCommitted   \* global committed count when that read was served/Nil

vars == <<currentTerm, commitIndex, applied, leader, leaderTerm,
          committed, believesLeader, hbConfirmed,
          lastReadValue, lastReadCommitted>>

TypeOK ==
    /\ currentTerm       \in [Servers -> Terms]
    /\ commitIndex       \in [Servers -> Indices]
    /\ applied           \in [Servers -> Indices]
    /\ leader            \in Servers \cup {Nil}
    /\ leaderTerm        \in Terms
    /\ committed         \in Indices
    /\ believesLeader    \in [Servers -> BOOLEAN]
    /\ hbConfirmed       \in [Servers -> BOOLEAN]
    /\ lastReadValue     \in Indices \cup {Nil}
    /\ lastReadCommitted \in Indices \cup {Nil}

Init ==
    /\ currentTerm       = [s \in Servers |-> 0]
    /\ commitIndex       = [s \in Servers |-> 0]
    /\ applied           = [s \in Servers |-> 0]
    /\ leader            = Nil
    /\ leaderTerm        = 0
    /\ committed         = 0
    /\ believesLeader    = [s \in Servers |-> FALSE]
    /\ hbConfirmed       = [s \in Servers |-> FALSE]
    /\ lastReadValue     = Nil
    /\ lastReadCommitted = Nil

(***************************************************************************)
(* ElectLeader(s): server s wins an election at a strictly-higher term     *)
(* with the support of a QUORUM that INCLUDES s. This is the deposing step. *)
(* Crucially, a server only LEARNS of the new term if it is in the electing *)
(* quorum: those servers adopt currentTerm := leaderTerm+1 and DROP any      *)
(* stale "I am leader" belief. A server OUTSIDE the quorum -- including a    *)
(* PREVIOUS leader that is not in Q -- keeps its now-stale currentTerm,      *)
(* commitIndex AND believesLeader (it has NOT heard of the new term: the     *)
(* deposed-but-doesn't-know-it leader). That server is the stale believer    *)
(* the ReadIndex heartbeat gate must stop. The new leader learns the latest  *)
(* committed index (read-index at election: a winning leader's log is at     *)
(* least as up to date as its supporting quorum). The new leader's own       *)
(* belief is set TRUE; all fresh heartbeat confirmations are INVALIDATED --  *)
(* a fresh confirmation must be obtained in the new term before serving.     *)
(***************************************************************************)
ElectLeader(s) ==
    /\ leaderTerm < MaxTerm
    /\ \E Q \in Quorum :
           /\ s \in Q
           \* the electing quorum adopts the new (higher) term
           /\ currentTerm' = [t \in Servers |->
                              IF t \in Q THEN leaderTerm + 1 ELSE currentTerm[t]]
           \* members of Q drop any stale leader-belief; the NEW leader s
           \* believes; servers OUTSIDE Q keep their (possibly stale) belief
           /\ believesLeader' = [t \in Servers |->
                              IF t = s     THEN TRUE
                              ELSE IF t \in Q THEN FALSE
                              ELSE believesLeader[t]]
    /\ leader'       = s
    /\ leaderTerm'   = leaderTerm + 1
    \* new leader learns the latest committed index (its commit view catches up)
    /\ commitIndex'  = [commitIndex EXCEPT ![s] = committed]
    \* term changed -> any stale heartbeat confirmation is dropped everywhere
    /\ hbConfirmed'  = [t \in Servers |-> FALSE]
    /\ UNCHANGED <<applied, committed, lastReadValue, lastReadCommitted>>

(***************************************************************************)
(* CommitWrite: the CURRENT real leader, in its own term, commits a new     *)
(* write. Writes flow only through the real leader (Raft invariant). The    *)
(* global committed count advances and the leader's local commitIndex moves *)
(* with it. A previously-deposed (stale) leader does NOT advance -- this is  *)
(* exactly what makes its commitIndex/applied fall behind `committed`, the  *)
(* configuration in which a guard-less read would be stale.                *)
(***************************************************************************)
CommitWrite ==
    /\ committed < MaxWrites
    /\ leader # Nil
    /\ currentTerm[leader] = leaderTerm
    /\ committed'    = committed + 1
    /\ commitIndex'  = [commitIndex EXCEPT ![leader] = committed + 1]
    /\ UNCHANGED <<currentTerm, applied, leader, leaderTerm,
                   believesLeader, hbConfirmed,
                   lastReadValue, lastReadCommitted>>

(***************************************************************************)
(* ConfirmLeadership(s): step (2) of ReadIndex. The real leader s exchanges *)
(* heartbeats with a QUORUM in its CURRENT term and they ack. This succeeds  *)
(* ONLY if s is still the real leader in the current leaderTerm -- a stale   *)
(* leader (currentTerm[s] # leaderTerm, or leader # s) cannot get a quorum   *)
(* to ack in its old term, so it can never set hbConfirmed. Recording the    *)
(* confirmation lets ServeRead proceed. (We require a quorum to ack in the   *)
(* leader's term; since the real leader was elected with a quorum at         *)
(* leaderTerm, such a quorum exists for it and only it.)                    *)
(***************************************************************************)
ConfirmLeadership(s) ==
    /\ leader = s
    /\ currentTerm[s] = leaderTerm
    /\ \E Q \in Quorum :
           \* every member of the acking quorum is in the leader's term
           \A t \in Q : currentTerm[t] = leaderTerm
    /\ hbConfirmed' = [hbConfirmed EXCEPT ![s] = TRUE]
    /\ UNCHANGED <<currentTerm, commitIndex, applied, leader, leaderTerm,
                   committed, believesLeader,
                   lastReadValue, lastReadCommitted>>

(***************************************************************************)
(* Apply(s): server s applies committed entries to its state machine, up to *)
(* its own commitIndex. ReadIndex step (3): the read waits until applied    *)
(* has reached readIndex (here, the leader's commitIndex). applied never    *)
(* exceeds the server's local commitIndex.                                  *)
(***************************************************************************)
Apply(s) ==
    /\ applied[s] < commitIndex[s]
    /\ applied' = [applied EXCEPT ![s] = commitIndex[s]]
    /\ UNCHANGED <<currentTerm, commitIndex, leader, leaderTerm,
                   committed, believesLeader, hbConfirmed,
                   lastReadValue, lastReadCommitted>>

(***************************************************************************)
(* ServeRead(s): server s serves a linearizable ReadIndex read at its       *)
(* applied index. s serves because it BELIEVES it is the leader             *)
(* (believesLeader[s]) -- which a DEPOSED leader still does until it hears   *)
(* of the newer term. The GUARD is the whole correctness argument:          *)
(*   - s believes it is the leader, AND                                     *)
(*   - (READ GATE, when ConfirmHeartbeat) s must hold a FRESH heartbeat-     *)
(*     quorum confirmation in the CURRENT term: hbConfirmed[s] AND           *)
(*     currentTerm[s] = leaderTerm. This is ReadIndex step (2). A STALE      *)
(*     believer can NEVER satisfy this: hbConfirmed was cleared at the term  *)
(*     change and ConfirmLeadership cannot re-set it (the believer is not    *)
(*     the real `leader` and its term is below leaderTerm), so the gate      *)
(*     blocks it -- exactly how ReadIndex prevents the stale read.           *)
(*   - applied[s] must have caught up to s's commitIndex (step (3)).         *)
(* It records the served value (applied[s]) and the GLOBAL committed count   *)
(* at this instant, so the staleness check is exact.                        *)
(*                                                                         *)
(* The MUTATION sets ConfirmHeartbeat = FALSE: the heartbeat gate is         *)
(* dropped (ReadGate = TRUE), so a STALE believer -- a former leader deposed *)
(* by a newer leader that has since committed a newer write, with its commit *)
(* view FROZEN behind the global `committed` -- serves at its stale applied  *)
(* index, returning a value strictly below `committed` -> lastReadValue <    *)
(* lastReadCommitted, a counterexample to LinearizableRead.                 *)
(***************************************************************************)
ReadGate(s) ==
    IF ConfirmHeartbeat
    THEN hbConfirmed[s] /\ currentTerm[s] = leaderTerm
    ELSE TRUE

ServeRead(s) ==
    /\ believesLeader[s]
    /\ ReadGate(s)
    /\ applied[s] = commitIndex[s]
    /\ lastReadValue'     = applied[s]
    /\ lastReadCommitted' = committed
    /\ UNCHANGED <<currentTerm, commitIndex, applied, leader, leaderTerm,
                   committed, believesLeader, hbConfirmed>>

Next ==
    \/ \E s \in Servers : ElectLeader(s)
    \/ CommitWrite
    \/ \E s \in Servers : ConfirmLeadership(s)
    \/ \E s \in Servers : Apply(s)
    \/ \E s \in Servers : ServeRead(s)

Spec == Init /\ [][Next]_vars

\* Symmetry set for TLC: server ids are interchangeable model values (the
\* spec never orders them; all invariants are symmetric under permutation).
Symm == Permutations(Servers)

(***************************************************************************)
(* SAFETY 1 -- LinearizableRead (the read-consistency property).            *)
(*                                                                         *)
(* A read served via ReadIndex returns a value reflecting ALL writes        *)
(* committed before the read began: the value returned equals the global    *)
(* committed count that was authoritative at the instant of service. A      *)
(* leader that confirmed leadership via a heartbeat quorum can NEVER have    *)
(* missed a write committed by a newer leader -- because a newer leader      *)
(* needs a quorum at a higher term, which would have prevented the          *)
(* heartbeat quorum from acking in the old term.                            *)
(*                                                                         *)
(*     lastReadValue # Nil => lastReadValue = lastReadCommitted             *)
(*                                                                         *)
(* MEANINGFUL / has TEETH: a STALE leader serving WITHOUT the heartbeat      *)
(* confirmation (the mutation, ConfirmHeartbeat = FALSE) returns its frozen  *)
(* applied index, which is strictly below `committed` after the new leader   *)
(* committed a write -> lastReadValue < lastReadCommitted, a counterexample. *)
(***************************************************************************)
LinearizableRead ==
    (lastReadValue # Nil) => (lastReadValue = lastReadCommitted)

(***************************************************************************)
(* SAFETY 2 -- CommitMonotonic / leader-completeness-lite.                  *)
(*                                                                         *)
(* (a) The GLOBAL committed count is never below the real leader's local    *)
(*     commit view nor below any server's local commit view: no server ever *)
(*     believes more is committed than actually is. (commitIndex[s] is a    *)
(*     view of the single global commit point.)                             *)
(* (b) applied never runs ahead of the local commit view.                   *)
(* (c) The current real leader's term is the maximum term any server holds   *)
(*     (leader-completeness-lite: leadership lives at the highest term).     *)
(***************************************************************************)
CommitMonotonic ==
    /\ \A s \in Servers : commitIndex[s] <= committed
    /\ \A s \in Servers : applied[s] <= commitIndex[s]
    /\ (leader # Nil) =>
           (\A s \in Servers : currentTerm[s] <= leaderTerm)

-------------------------------------------------------------------------
(* Non-vacuity witnesses: negate each to make TLC print a reachable state,  *)
(* proving the interesting configurations are actually reached (see README).*)

\* A ReadIndex read was actually served AFTER a heartbeat-quorum confirmation
\* (the real protocol path). Negation reachable => the served-read state is
\* reached on the main (ConfirmHeartbeat = TRUE) model.
ReachServedRead == lastReadValue = Nil

\* A second election happened (leadership moved to a higher term): the
\* stale-leader setup. Negation reachable => term >= 2 is reached.
ReachReelection == leaderTerm < 2

\* A write committed under a NEW leader while some OTHER server's commit view
\* is strictly behind -- the exact stale-read setup the heartbeat gate guards.
ReachStaleSetup == \A s \in Servers : commitIndex[s] >= committed
=============================================================================
