-------------------------- MODULE TwoPhaseCommit --------------------------
(***************************************************************************)
(* TLA+ model of the classic Two-Phase Commit (2PC) atomic-commit          *)
(* protocol -- the canonical Gray/Lamport teaching example, adapted to the *)
(* Helix formal-methods stream alongside Consensus / Swim / ORSet etc.     *)
(*                                                                         *)
(* PARTICIPANTS                                                            *)
(*   - One Transaction Manager (TM), the coordinator.                       *)
(*   - A set RMs of Resource Managers (cohorts / participants).            *)
(*                                                                         *)
(* PER-RM LIFECYCLE                                                        *)
(*   "working" --> "prepared" --> "committed"   (happy path)               *)
(*   "working" -----------------> "aborted"     (RM unilaterally aborts     *)
(*                                               before voting yes)         *)
(*   "prepared" ----------------> "aborted"     (TM globally aborts)        *)
(*                                                                         *)
(* PROTOCOL (phase 1: voting; phase 2: decision)                          *)
(*   Phase 1 -- An RM in "working" either votes YES by moving to           *)
(*     "prepared" (and emitting a "Prepared" message), or unilaterally     *)
(*     aborts (votes NO) by moving to "aborted".                           *)
(*   Phase 2 -- The TM collects votes from the shared message set:         *)
(*     * if it has seen a "Prepared" message from EVERY RM, it may decide  *)
(*       global commit and emit "Commit";                                  *)
(*     * at any time it may decide global abort and emit "Abort"           *)
(*       (modelling timeouts / a received NO vote -- an RM that aborted     *)
(*       never sends "Prepared", so commit can never be reached for it).   *)
(*   RMs then follow the TM decision: on "Commit" a prepared RM commits;   *)
(*   on "Abort" a working-or-prepared RM aborts.                           *)
(*                                                                         *)
(* COMMUNICATION is modelled (per Lamport's TwoPhase) as a single shared   *)
(* set `msgs` of messages that, once added, are never removed -- a stable  *)
(* broadcast medium the TM and RMs read from. This abstracts unreliable    *)
(* delivery as nondeterministic (re)reading.                              *)
(*                                                                         *)
(* SAFETY PROPERTIES VERIFIED BY TLC (exhaustively, full state space):     *)
(*   1. Consistency (Atomicity) -- never one RM "committed" while another  *)
(*      is "aborted": all-or-nothing.                                      *)
(*   2. Decisiveness/Validity -- an RM is "committed" only if the TM       *)
(*      actually decided global commit (a "Commit" message exists), which  *)
(*      the protocol issues only after ALL RMs prepared; symmetrically an  *)
(*      "aborted" RM implies global commit was never decided.             *)
(*   3. TypeOK -- the state invariant.                                     *)
(***************************************************************************)
EXTENDS FiniteSets, TLC

CONSTANTS RMs   \* the (finite, non-empty) set of Resource Manager ids

\* RM ids are interchangeable; collapse permutations to shrink the state space.
Symmetry == Permutations(RMs)

VARIABLES
    rmState,   \* rmState[rm] in {"working","prepared","committed","aborted"}
    tmState,   \* "init" | "committed" | "aborted" -- the TM's global decision
    msgs       \* the shared, append-only set of in-flight messages

vars == <<rmState, tmState, msgs>>

\* Every message either announces an RM's prepare vote or the TM's decision.
Messages ==
       [type : {"Prepared"}, rm : RMs]
    \cup [type : {"Commit", "Abort"}]

TypeOK ==
    /\ rmState \in [RMs -> {"working", "prepared", "committed", "aborted"}]
    /\ tmState \in {"init", "committed", "aborted"}
    /\ msgs \subseteq Messages

Init ==
    /\ rmState = [rm \in RMs |-> "working"]
    /\ tmState = "init"
    /\ msgs    = {}

(***************************************************************************)
(* PHASE 1 -- RM voting.                                                   *)
(***************************************************************************)

\* RM votes YES: it prepares and broadcasts a "Prepared" message. Only an
\* RM still "working" may prepare (a decided RM is frozen).
RMPrepare(rm) ==
    /\ rmState[rm] = "working"
    /\ rmState' = [rmState EXCEPT ![rm] = "prepared"]
    /\ msgs'    = msgs \cup {[type |-> "Prepared", rm |-> rm]}
    /\ UNCHANGED tmState

\* RM votes NO: a "working" RM may unilaterally abort (e.g. local failure)
\* before ever preparing. It never sends "Prepared", so the TM can never
\* see an all-prepared set including this RM -- global commit is precluded.
RMChooseToAbort(rm) ==
    /\ rmState[rm] = "working"
    /\ rmState' = [rmState EXCEPT ![rm] = "aborted"]
    /\ UNCHANGED <<tmState, msgs>>

(***************************************************************************)
(* PHASE 2 -- TM decision.                                                 *)
(***************************************************************************)

\* TM decides global COMMIT -- permitted ONLY when it has received a
\* "Prepared" message from EVERY RM (the all-prepared gate). This is the
\* load-bearing precondition: weakening it breaks atomicity (the mutation
\* run TwoPhaseCommitMut demonstrates exactly this).
TMCommit ==
    /\ tmState = "init"
    /\ \A rm \in RMs : [type |-> "Prepared", rm |-> rm] \in msgs
    /\ tmState' = "committed"
    /\ msgs'    = msgs \cup {[type |-> "Commit"]}
    /\ UNCHANGED rmState

\* TM decides global ABORT -- always permitted while undecided (models a
\* timeout or a received NO vote). Safe because aborting is unconditional.
TMAbort ==
    /\ tmState = "init"
    /\ tmState' = "aborted"
    /\ msgs'    = msgs \cup {[type |-> "Abort"]}
    /\ UNCHANGED rmState

(***************************************************************************)
(* RMs follow the TM decision (reading the shared message set).            *)
(***************************************************************************)

\* A prepared RM commits upon seeing the TM's "Commit". A "working" RM can
\* never commit -- it must have voted YES (prepared) first.
RMRcvCommit(rm) ==
    /\ rmState[rm] = "prepared"
    /\ [type |-> "Commit"] \in msgs
    /\ rmState' = [rmState EXCEPT ![rm] = "committed"]
    /\ UNCHANGED <<tmState, msgs>>

\* A working-or-prepared RM aborts upon seeing the TM's "Abort".
RMRcvAbort(rm) ==
    /\ rmState[rm] \in {"working", "prepared"}
    /\ [type |-> "Abort"] \in msgs
    /\ rmState' = [rmState EXCEPT ![rm] = "aborted"]
    /\ UNCHANGED <<tmState, msgs>>

Next ==
    \/ \E rm \in RMs :
           \/ RMPrepare(rm)
           \/ RMChooseToAbort(rm)
           \/ RMRcvCommit(rm)
           \/ RMRcvAbort(rm)
    \/ TMCommit
    \/ TMAbort

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* SAFETY INVARIANTS.                                                       *)
(***************************************************************************)

\* (1) Consistency / Atomicity: all-or-nothing. It is NEVER the case that one
\* RM has committed while another has aborted. This is the heart of 2PC.
Consistency ==
    \A r1, r2 \in RMs :
        ~ (rmState[r1] = "committed" /\ rmState[r2] = "aborted")

\* (2) Decisiveness / Validity (two complementary directions):
\*   (a) an RM is "committed" only if the TM decided global commit, which
\*       in turn could only happen with ALL RMs prepared; and
\*   (b) an RM is "aborted" only if the TM did NOT decide global commit.
\* Together these chain a local commit back to a genuine global commit
\* decision, and forbid a local abort whenever the global outcome was
\* commit -- the validity side of atomicity.
Decisiveness ==
    /\ (\E rm \in RMs : rmState[rm] = "committed") => (tmState = "committed")
    /\ (\E rm \in RMs : rmState[rm] = "aborted")   => (tmState # "committed")

\* A committed RM implies the "Commit" message was actually broadcast, and
\* "Commit" is only ever emitted by TMCommit under the all-prepared gate.
CommitImpliesGlobalCommit ==
    (\E rm \in RMs : rmState[rm] = "committed")
        => ([type |-> "Commit"] \in msgs)

(***************************************************************************)
(* NON-VACUITY witnesses (negated as invariants so a TLC "violation" is a   *)
(* REACHABILITY PROOF that the state exists -- run separately, never in the *)
(* main safety cfg). See README for how these are exercised.               *)
(*   - GlobalCommitReachable: some run reaches all-RMs-committed.           *)
(*   - GlobalAbortReachable:  some run reaches all-RMs-aborted.             *)
(***************************************************************************)
AllCommitted == \A rm \in RMs : rmState[rm] = "committed"
AllAborted   == \A rm \in RMs : rmState[rm] = "aborted"

GlobalCommitReachable == ~AllCommitted   \* TLC violation => commit reachable
GlobalAbortReachable  == ~AllAborted     \* TLC violation => abort  reachable

=============================================================================
