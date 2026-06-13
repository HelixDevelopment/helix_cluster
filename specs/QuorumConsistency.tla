------------------------ MODULE QuorumConsistency ------------------------
(***************************************************************************)
(* TLA+ model of DYNAMO-STYLE QUORUM CONSISTENCY: the R + W > N           *)
(* overlapping-quorum rule that gives read-your-writes / strong           *)
(* single-object consistency in a leaderless replicated store. Part of    *)
(* the Helix formal-methods stream (cf. VectorClock, ORSet, Consensus,    *)
(* Swim, TwoPhaseCommit, Saga, RaftMembership). This verifies the         *)
(* replication-consistency primitive behind the cluster's replicated      *)
(* state -- pkg/crdt (replicated values), pkg/antientropy (replica        *)
(* repair), pkg/mvcc (versioned values), and quorum reads.                *)
(*                                                                         *)
(* MODEL.                                                                  *)
(*                                                                         *)
(*   * N = Cardinality(Replicas) replicas. Each replica r holds a single  *)
(*     versioned object store[r] = a version number in 0..MaxVersion.     *)
(*     (val is 1:1 with version here -- a fresh write always picks the    *)
(*     next-higher version, so "version" fully identifies "the value      *)
(*     written"; modelling a separate opaque val adds states without      *)
(*     adding any reachable behaviour to check.)                          *)
(*                                                                         *)
(*   * A WRITE is a two-stage operation:                                   *)
(*       BeginWrite(Q)  -- nondeterministically pick a write-quorum Q, a   *)
(*         set of replicas with |Q| = W, and mint the next global version *)
(*         ver. The write is now IN FLIGHT against quorum Q at version ver.*)
(*       StepWrite      -- apply the in-flight write to ONE not-yet-       *)
(*         updated replica in its quorum (replicas are updated one at a    *)
(*         time, modelling partial/interleaved replication; a replica      *)
(*         only moves FORWARD: store[r] := max(store[r], ver)).           *)
(*       CompleteWrite  -- once EVERY replica in the quorum Q holds a      *)
(*         version >= ver, the write COMPLETES: it is recorded in          *)
(*         `completed` with its version and the quorum it durably wrote.   *)
(*     A write that has completed has durably reached W replicas. Because  *)
(*     replication is one-replica-at-a-time and reads may interleave, a    *)
(*     read can observe a write that is still IN FLIGHT (some but not all  *)
(*     of its quorum updated) -- that is the realistic hazard.            *)
(*                                                                         *)
(*   * A READ is a single atomic observation: BeginRead(Q) picks a         *)
(*     read-quorum Q with |Q| = R and returns the MAXIMUM version held by  *)
(*     any replica in Q (the standard "newest wins" quorum read). We       *)
(*     record, for that read, the set of writes that had ALREADY COMPLETED *)
(*     at the instant the read was taken (its "happened-before" frontier)  *)
(*     together with the version the read returned. The safety invariant   *)
(*     is then checked against that recorded evidence.                     *)
(*                                                                         *)
(* THE QUORUM-OVERLAP THEOREM (what we model-check).                       *)
(*                                                                         *)
(*   If  R + W > N  then any read-quorum (size R) and any write-quorum     *)
(*   (size W) INTERSECT in at least one replica. A completed write has     *)
(*   durably placed its version on all W replicas of its write-quorum, and *)
(*   replicas only move forward, so that intersecting replica still holds  *)
(*   a version >= the completed write's version. The read takes the MAX    *)
(*   over its quorum, hence the read returns a version >= that completed   *)
(*   write's version. => ReadSeesLatestComplete.                          *)
(*                                                                         *)
(*   When R + W <= N the quorums need NOT intersect, so a read can pick a  *)
(*   quorum entirely disjoint from a completed write's quorum and miss it  *)
(*   -> a STALE READ. The cfg `QuorumConsistencyStale.cfg` sets R=W=1 on   *)
(*   N=3 (R+W=2 <= 3) and TLC finds exactly this counterexample, proving   *)
(*   the invariant has TEETH and is not vacuously true.                   *)
(*                                                                         *)
(* PROPERTIES (see QuorumConsistency.cfg):                                 *)
(*                                                                         *)
(*   TypeOK                  -- state stays well-typed.                    *)
(*   ReadSeesLatestComplete  -- THE safety goal. Every recorded read       *)
(*       returned a version >= the version of EVERY write that had         *)
(*       completed before that read began. Under R+W>N this holds by       *)
(*       quorum overlap; under R+W<=N it is violated (stale read).         *)
(*   MonotonicVersions       -- (action property) no replica's stored      *)
(*       version ever decreases; the global high-water version only grows. *)
(*   ReadReturnsAnExistingVersion -- a sanity/typing law: a read returns   *)
(*       0 or a version that was actually minted.                          *)
(*                                                                         *)
(*   ReadSawFreshWrite (non-vacuity witness, asserted reachable) -- a      *)
(*       state where some read returned the version of a write that        *)
(*       completed before it, AND that write's quorum was NOT a subset of  *)
(*       the read's quorum (the value was carried across via the single    *)
(*       forced overlap replica, not by reading the whole write quorum).   *)
(*       Proving this reachable shows the overlap mechanism is actually    *)
(*       exercised, not trivially satisfied.                               *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Replicas,    \* finite set of replica ids, e.g. {n1, n2, n3}; N = Cardinality
    R,           \* read-quorum size  (1 <= R <= N)
    W,           \* write-quorum size (1 <= W <= N)
    MaxWrites    \* GLOBAL bound on number of writes minted (bounds the state space)

N == Cardinality(Replicas)

ASSUME /\ R \in 1..N
       /\ W \in 1..N
       /\ MaxWrites \in Nat

----------------------------------------------------------------------------
\* Versions: 0 = the initial value every replica starts with; live writes
\* mint 1, 2, ... up to MaxWrites. MaxVersion is therefore MaxWrites.
MaxVersion == MaxWrites
Versions   == 0..MaxVersion

\* Quorums of a given size.
ReadQuorums  == { Q \in SUBSET Replicas : Cardinality(Q) = R }
WriteQuorums == { Q \in SUBSET Replicas : Cardinality(Q) = W }

\* Read ids: a fixed finite pool. We allow as many reads as writes (a read is
\* cheap state-wise and a single read suffices to expose staleness, but a pool
\* the size of MaxWrites lets several distinct reads coexist as evidence).
ReadIds == 1..MaxWrites

Max(a, b) == IF a >= b THEN a ELSE b

VARIABLES
    store,        \* store[r]  : Versions          -- version currently held by replica r
    nextVer,      \* nextVer   : Nat               -- next version to mint (1-based high-water)
    inflight,     \* inflight  : record or NULL    -- the single in-flight write (if any)
    completed,    \* completed : SUBSET [ver, quorum]  -- writes that durably reached their full W-quorum
    reads,        \* reads[k]  : record or NULL    -- recorded read k: its returned version + completed-frontier
    readCnt       \* readCnt   : Nat               -- how many reads taken so far (<= MaxWrites)

vars == <<store, nextVer, inflight, completed, reads, readCnt>>

\* "no in-flight write" sentinel.
NoWrite == [ver |-> 0, quorum |-> {}, active |-> FALSE]

\* "read slot not yet used" sentinel.
NoRead == [ver |-> 0, frontier |-> {}, quorum |-> {}, used |-> FALSE]

----------------------------------------------------------------------------
\* Helpers

\* The maximum version held among a set of replicas Q (the value a quorum read
\* over Q returns). For a non-empty Q this is a real "newest wins" read.
MaxOver(Q) == CHOOSE v \in {store[r] : r \in Q} :
                  \A r \in Q : store[r] <= v

\* The frontier of a read: the set of writes COMPLETED at the instant the read
\* is taken. Each is a [ver, quorum] record drawn from `completed`.
CompletedNow == completed

----------------------------------------------------------------------------
Init ==
    /\ store     = [r \in Replicas |-> 0]
    /\ nextVer   = 1
    /\ inflight  = NoWrite
    /\ completed = {}
    /\ reads     = [k \in ReadIds |-> NoRead]
    /\ readCnt   = 0

----------------------------------------------------------------------------
\* Actions

\* Begin a new write: only when no write is in flight (one writer at a time keeps
\* the model tiny while still allowing reads to interleave with replication) and
\* the global write budget is not exhausted. Pick a write-quorum Q of size W and
\* mint the next version. Nothing is applied to any replica yet.
BeginWrite(Q) ==
    /\ inflight.active = FALSE
    /\ nextVer <= MaxWrites
    /\ Q \in WriteQuorums
    /\ inflight' = [ver |-> nextVer, quorum |-> Q, active |-> TRUE]
    /\ nextVer'  = nextVer + 1
    /\ UNCHANGED <<store, completed, reads, readCnt>>

\* Apply the in-flight write to ONE replica in its quorum that has not yet caught
\* up to it. Replicas move FORWARD only (max), modelling partial replication: at
\* this point some quorum members hold the new version and some do not, so a
\* concurrent read may see a torn/partial write.
StepWrite(r) ==
    /\ inflight.active = TRUE
    /\ r \in inflight.quorum
    /\ store[r] < inflight.ver
    /\ store' = [store EXCEPT ![r] = Max(@, inflight.ver)]
    /\ UNCHANGED <<nextVer, inflight, completed, reads, readCnt>>

\* Complete the in-flight write once EVERY replica in its quorum holds a version
\* >= its version (its W replicas are durably updated). Record it in `completed`
\* and clear the in-flight slot, freeing the writer for the next write.
CompleteWrite ==
    /\ inflight.active = TRUE
    /\ \A r \in inflight.quorum : store[r] >= inflight.ver
    /\ completed' = completed \cup {[ver |-> inflight.ver, quorum |-> inflight.quorum]}
    /\ inflight'  = NoWrite
    /\ UNCHANGED <<store, nextVer, reads, readCnt>>

\* Take a quorum read over read-quorum Q (size R). The read returns the MAX
\* version in Q and records, as its happened-before frontier, the set of writes
\* that had completed BEFORE this read was taken. This recorded evidence is what
\* the safety invariant is checked against.
BeginRead(Q) ==
    /\ readCnt < MaxWrites
    /\ Q \in ReadQuorums
    /\ LET k == readCnt + 1 IN
         /\ reads' = [reads EXCEPT ![k] =
                        [ ver      |-> MaxOver(Q),
                          frontier |-> CompletedNow,
                          quorum   |-> Q,
                          used     |-> TRUE ]]
         /\ readCnt' = k
    /\ UNCHANGED <<store, nextVer, inflight, completed>>

Next ==
    \/ \E Q \in WriteQuorums : BeginWrite(Q)
    \/ \E r \in Replicas     : StepWrite(r)
    \/ CompleteWrite
    \/ \E Q \in ReadQuorums  : BeginRead(Q)

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
\* Symmetry: replica ids are interchangeable. Permuting them maps reachable
\* states to reachable states (quorums are defined purely by size, the store is
\* per-replica, and both invariant and witness quantify over replicas/quorums
\* symmetrically), so TLC may quotient the state graph by replica permutations.
Symmetry == Permutations(Replicas)

----------------------------------------------------------------------------
\* Invariants

\* (1) Type correctness.
TypeOK ==
    /\ store     \in [Replicas -> Versions]
    /\ nextVer   \in 1..(MaxWrites + 1)
    /\ inflight.ver \in Versions
    /\ inflight.quorum \in SUBSET Replicas
    /\ inflight.active \in BOOLEAN
    /\ completed \in SUBSET [ver : Versions, quorum : SUBSET Replicas]
    /\ reads \in [ReadIds -> [ ver : Versions,
                               frontier : SUBSET [ver : Versions, quorum : SUBSET Replicas],
                               quorum : SUBSET Replicas,
                               used : BOOLEAN ]]
    /\ readCnt \in 0..MaxWrites

\* (2) ReadSeesLatestComplete -- THE safety goal.
\*
\* Every recorded read returned a version that is >= the version of EVERY write
\* that had ALREADY COMPLETED at the instant that read was taken (the read's
\* recorded frontier). Equivalently: a quorum read never returns a value staler
\* than any write that finished before the read began.
\*
\* Under R + W > N this holds: the read-quorum intersects every write-quorum, the
\* intersecting replica durably holds the completed write's version (replicas
\* only move forward), and the read takes the MAX over its quorum.
\*
\* Under R + W <= N it can FAIL: a read can pick a quorum disjoint from a
\* completed write's quorum and miss it (stale read). That failing trace is the
\* teeth-proving mutation (QuorumConsistencyStale.cfg).
ReadSeesLatestComplete ==
    \A k \in ReadIds :
        reads[k].used =>
            \A wr \in reads[k].frontier :
                reads[k].ver >= wr.ver

\* (3) ReadReturnsAnExistingVersion -- a read returns either the initial 0 or a
\* version that was actually minted (1 .. nextVer-1). A read can never conjure a
\* version out of thin air. Independent of the quorum logic; guards against a
\* modelling slip that would make (2) vacuous by allowing arbitrary versions.
ReadReturnsAnExistingVersion ==
    \A k \in ReadIds :
        reads[k].used => reads[k].ver \in 0..(nextVer - 1)

----------------------------------------------------------------------------
\* Action property

\* MonotonicVersions -- no replica's stored version ever decreases on any step,
\* and the mint high-water `nextVer` never decreases. Checked via PROPERTY.
\* StepWrite uses max; no action lowers a store value or nextVer.
MonotonicVersions ==
    [][ /\ \A r \in Replicas : store[r] <= store'[r]
        /\ nextVer <= nextVer' ]_vars

----------------------------------------------------------------------------
\* Non-vacuity witness (asserted reachable; negate as an invariant on a witness
\* run, or read off the coverage). A read that returned the version of a write
\* which had completed before it, where that write's quorum is NOT a subset of
\* the read's quorum -- so the value could only have been seen via the FORCED
\* overlap replica (R+W>N), not by trivially reading the whole write quorum.
\* This proves ReadSeesLatestComplete is exercised non-trivially.
ReadSawFreshWrite ==
    \E k \in ReadIds :
        /\ reads[k].used
        /\ \E wr \in reads[k].frontier :
              /\ wr.ver > 0
              /\ reads[k].ver = wr.ver
              /\ ~ (wr.quorum \subseteq reads[k].quorum)

============================================================================
