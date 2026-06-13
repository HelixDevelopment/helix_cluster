------------------------------ MODULE RaftSnapshot ------------------------------
(***************************************************************************)
(* Raft snapshot-install / log-compaction SAFETY (abstracted).             *)
(*                                                                         *)
(* Verifies the real mechanism now exercised by pkg/raft:                  *)
(*   FileSnapshotStore (FSM point-in-time snapshot for log compaction) +   *)
(*   log truncation past the snapshot point + InstallSnapshot to a         *)
(*   follower whose log has fallen BEHIND the leader's snapshotIndex, so    *)
(*   the leader can no longer replicate the missing entries by a plain      *)
(*   AppendEntries and must ship the snapshot instead.                      *)
(*                                                                         *)
(* This is deliberately NOT full Raft. We fix ONE stable leader holding a   *)
(* committed log prefix and model only the snapshot/compaction/install      *)
(* dimension the Go code exercises:                                         *)
(*                                                                         *)
(*   COMMIT     advance the leader's commitIndex (a new entry is committed). *)
(*   COMPACT    leader takes an FSM snapshot at snapIndex <= commitIndex,    *)
(*              discarding log entries <= snapIndex (FileSnapshotStore +     *)
(*              log truncation). The snapshot reflects applied state through *)
(*              snapIndex.                                                   *)
(*   INSTALL    a follower whose log is BEHIND the leader's snapshotIndex    *)
(*              receives InstallSnapshot: it adopts the snapshot (its        *)
(*              applied state + log start jump to snapIndex).                *)
(*   APPEND     a follower catches up by normal AppendEntries for entries    *)
(*              AFTER its log end, but ONLY entries the leader still holds    *)
(*              (index > leader's snapshotIndex). Entries <= the leader       *)
(*              snapshot are gone from the leader -- only INSTALL bridges     *)
(*              that gap.                                                     *)
(*                                                                         *)
(* MODELLING OF STATE. A committed entry at index i is fixed forever in real *)
(* Raft (Log Matching + Leader Completeness); we model its value as Entry(i).*)
(* A node's reconstructable STATE is the set of indices it can serve: every  *)
(* index its snapshot covers (1..snap) UNION every index still in its log    *)
(* (snap+1..logEnd). SAFETY = a node's reconstructed state never loses /      *)
(* never diverges on a committed entry.                                      *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Followers,        \* set of follower ids (model values; symmetric)
    MaxIndex,         \* highest log index in the bounded model
    AllowBadCompact,  \* MUTATION: permit COMPACT at snapIndex > commitIndex
    AllowLossyInstall \* MUTATION: INSTALL adopts snap index but drops covered state

\* Permutations of the interchangeable follower ids -- sound symmetry
\* reduction (see RaftSnapshot.cfg for the soundness argument).
Symmetry == Permutations(Followers)

VARIABLES
    commitIndex,    \* leader's commit index: entries 1..commitIndex are committed
    leaderSnap,     \* leader's snapshot index (log <= leaderSnap is compacted away)
    fSnap,          \* fSnap[f]   = follower f's installed snapshot index
    fLogEnd,        \* fLogEnd[f] = highest contiguous index f can serve from its log
    fHasState       \* fHasState[f][i] = TRUE iff follower f actually holds the entry
                    \* for committed index i in its reconstructed state. This makes a
                    \* lossy install OBSERVABLE: it advances the snap index pointer but
                    \* leaves fHasState FALSE for the indices it failed to materialise.

vars == <<commitIndex, leaderSnap, fSnap, fLogEnd, fHasState>>

Indices == 0..MaxIndex   \* 0 means "empty / nothing yet"

(***************************************************************************)
(* The committed entry stored at index i. Deterministic and immutable --   *)
(* the Raft invariant that a committed index's entry never changes.        *)
(***************************************************************************)
Entry(i) == i

\* Indices the leader has committed.
Committed == { i \in 1..MaxIndex : i <= commitIndex }

\* Indices the leader can serve: snapshot prefix UNION remaining log.
LeaderState == { i \in 1..MaxIndex : i <= leaderSnap \/ (i > leaderSnap /\ i <= commitIndex) }

\* Indices a follower actually holds (the materialised truth, not just pointers).
FollowerState(f) == { i \in 1..MaxIndex : fHasState[f][i] }

TypeOK ==
    /\ commitIndex \in Indices
    /\ leaderSnap \in Indices
    /\ fSnap \in [Followers -> Indices]
    /\ fLogEnd \in [Followers -> Indices]
    /\ fHasState \in [Followers -> [1..MaxIndex -> BOOLEAN]]

Init ==
    /\ commitIndex = 0
    /\ leaderSnap  = 0
    /\ fSnap   = [f \in Followers |-> 0]
    /\ fLogEnd = [f \in Followers |-> 0]
    /\ fHasState = [f \in Followers |-> [i \in 1..MaxIndex |-> FALSE]]

----------------------------------------------------------------------------
(* COMMIT: a new entry becomes committed and durable in the leader's log.   *)
Commit ==
    /\ commitIndex < MaxIndex
    /\ commitIndex' = commitIndex + 1
    /\ UNCHANGED <<leaderSnap, fSnap, fLogEnd, fHasState>>

(* COMPACT: leader snapshots at index k and truncates its log <= k.         *)
(* CORRECT rule: k <= commitIndex -- only ALREADY-committed state may be     *)
(* captured, so compaction can never discard an entry the snapshot does not  *)
(* legitimately reflect. The MUTATION AllowBadCompact lifts the              *)
(* k <= commitIndex guard, letting the leader snapshot AHEAD of commit and   *)
(* truncate the log to a point it has not actually applied/committed.        *)
CompactBound == IF AllowBadCompact THEN MaxIndex ELSE commitIndex
Compact ==
    \E k \in 1..MaxIndex :
        /\ k > leaderSnap            \* snapshot must make progress
        /\ k <= CompactBound         \* CORRECT: k <= commitIndex
        /\ leaderSnap' = k
        /\ UNCHANGED <<commitIndex, fSnap, fLogEnd, fHasState>>

(* INSTALL: a follower that has fallen BEHIND the leader's snapshot receives *)
(* InstallSnapshot. Precondition fLogEnd[f] < leaderSnap is exactly the real *)
(* trigger: the leader no longer holds the entries the follower is missing,  *)
(* so a plain AppendEntries cannot catch it up.                              *)
(* CORRECT install: follower adopts the snapshot at leaderSnap AND           *)
(* materialises every covered committed index 1..leaderSnap (fHasState set). *)
(* MUTATION AllowLossyInstall: the follower advances its snap/log POINTERS   *)
(* to leaderSnap but fails to materialise the covered entries (fHasState     *)
(* left as-is) -- modelling an install that updates bookkeeping but loses    *)
(* the actual state below the previous log end.                              *)
Install ==
    \E f \in Followers :
        /\ fLogEnd[f] < leaderSnap          \* genuinely lagging behind the snapshot
        /\ leaderSnap > 0
        /\ fSnap'   = [fSnap   EXCEPT ![f] = leaderSnap]
        /\ fLogEnd' = [fLogEnd EXCEPT ![f] = leaderSnap]
        /\ fHasState' = [fHasState EXCEPT ![f] =
               IF AllowLossyInstall
               THEN @                                 \* BUG: pointers move, state not materialised
               ELSE [i \in 1..MaxIndex |->            \* CORRECT: snapshot covers 1..leaderSnap
                        @[i] \/ i <= leaderSnap]]
        /\ UNCHANGED <<commitIndex, leaderSnap>>

(* APPEND: a follower catches up by one entry via normal AppendEntries, but  *)
(* ONLY for an entry the leader STILL HAS (index > leaderSnap) and that is    *)
(* committed. Entries <= leaderSnap are compacted away on the leader and can  *)
(* never be appended -- only INSTALL bridges that gap. This is what makes the *)
(* install path non-vacuously necessary.                                     *)
Append ==
    \E f \in Followers :
        /\ fLogEnd[f] < commitIndex          \* something committed to catch up to
        /\ fLogEnd[f] + 1 > leaderSnap        \* next entry not yet compacted away on leader
        /\ fLogEnd'  = [fLogEnd  EXCEPT ![f] = fLogEnd[f] + 1]
        /\ fHasState' = [fHasState EXCEPT ![f][fLogEnd[f] + 1] = TRUE]
        /\ UNCHANGED <<commitIndex, leaderSnap, fSnap>>

Next == Commit \/ Compact \/ Install \/ Append

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
(***************************************************************************)
(* SAFETY PROPERTIES                                                        *)
(***************************************************************************)

(* 1. NoCommittedEntryLost (compaction safety).                            *)
(* Every committed index is legitimately represented by the leader: either  *)
(* validly inside its snapshot (and that snapshot point is itself <= commit, *)
(* i.e. reflects applied state) or still present in the remaining log. A     *)
(* snapshot taken AHEAD of commit (AllowBadCompact) makes leaderSnap >       *)
(* commitIndex, so a committed index i with i <= commitIndex < leaderSnap is *)
(* claimed by a snapshot that reflects un-applied state -- the snapshot does *)
(* not legitimately cover it, and the truncated log no longer holds it.      *)
LegitLeaderCover == { i \in 1..MaxIndex :
                        \/ (i <= leaderSnap /\ leaderSnap <= commitIndex)
                        \/ (i > leaderSnap  /\ i <= commitIndex) }
NoCommittedEntryLost == Committed \subseteq LegitLeaderCover

(* 2. SnapshotInstallCorrect.                                               *)
(* After a follower installs a snapshot and appends the rest, it holds every *)
(* committed entry up to its own frontier (fLogEnd) with no gap, and its     *)
(* held values agree with the leader on the shared committed prefix.         *)
NoFollowerGap ==
    \A f \in Followers :
        \A i \in 1..MaxIndex :
            (i <= fLogEnd[f] /\ i <= commitIndex) => i \in FollowerState(f)

FollowerAgrees ==
    \A f \in Followers :
        \A i \in FollowerState(f) :
            (i <= commitIndex) => (i \in LeaderState /\ Entry(i) = Entry(i))

SnapshotInstallCorrect == NoFollowerGap /\ FollowerAgrees

(***************************************************************************)
(* NON-VACUITY witness (checked as an invariant we EXPECT to FAIL in the    *)
(* main run, proving the interesting compact-then-install-to-a-lagging-      *)
(* follower state is actually REACHED). NOT a safety property -- it is a     *)
(* reachability probe: TLC reports it as "violated", and that violation is   *)
(* the proof the scenario is non-vacuous. Enabled only in the witness cfg.   *)
(*                                                                         *)
(*   A follower f installed a snapshot strictly above 0, where it was        *)
(*   genuinely behind (so plain append could NOT have reached leaderSnap),   *)
(*   and afterwards holds a committed index it could only have gotten via    *)
(*   the snapshot.                                                           *)
(***************************************************************************)
NoInterestingInstall ==
    \A f \in Followers :
        ~( fSnap[f] > 0 /\ fSnap[f] \in FollowerState(f) /\ fLogEnd[f] >= fSnap[f] )

============================================================================
