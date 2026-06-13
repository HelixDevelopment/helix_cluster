------------------------------- MODULE Saga -------------------------------
(***************************************************************************)
(* TLA+ model of the SAGA pattern -- a distributed long-running           *)
(* transaction executed as a sequence of local steps, each with a         *)
(* compensating (undo) action, adapted to the Helix formal-methods stream *)
(* alongside Consensus / Swim / ORSet / TwoPhaseCommit etc.               *)
(*                                                                         *)
(* MODEL                                                                   *)
(*   A saga of N sequential steps 1..N. Step i has a forward action T_i    *)
(*   and a compensating action C_i. Execution runs T_1, T_2, ... in order. *)
(*   If some T_k FAILS, the saga rolls back by running the compensations   *)
(*   C_(k-1), ..., C_1 in REVERSE order of completion -- undoing exactly   *)
(*   the forward steps that had already committed.                         *)
(*                                                                         *)
(* PER-STEP LIFECYCLE                                                      *)
(*   "notStarted" --T_i success--> "done" ----C_i--> "compensated"         *)
(*   "notStarted" --T_i failure--> "failed"  (triggers rollback)           *)
(*                                                                         *)
(* GLOBAL PHASE                                                            *)
(*   "forward"   -- executing forward steps in order.                      *)
(*   "rollback"  -- a step failed; compensating done steps in reverse.     *)
(*   "committed" -- all steps done (saga succeeded).                       *)
(*   "aborted"   -- rollback complete; every done step compensated.        *)
(*                                                                         *)
(* The forward gate "all earlier steps are done" makes execution strictly  *)
(* sequential. The rollback gate "no later-completed step is still done"   *)
(* enforces REVERSE compensation order: a step may only be compensated     *)
(* once every higher-indexed step it depends on has already been undone.   *)
(*                                                                         *)
(* SAFETY PROPERTIES VERIFIED BY TLC (exhaustively, full state space):     *)
(*   1. AtomicityViaCompensation -- in every terminal state, EITHER all    *)
(*      steps are "done" (committed) OR every step that was "done" has been *)
(*      "compensated" (fully rolled back). No partial effect survives a     *)
(*      failed saga.                                                        *)
(*   2. CompensationOrder -- compensations run in reverse order of          *)
(*      completion: a step is "compensated" only after every later          *)
(*      (higher-indexed) step is no longer "done" (i.e. already            *)
(*      compensated, or never ran).                                        *)
(*   3. TypeOK -- the state invariant.                                     *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS N   \* number of saga steps (a small Nat, e.g. 3)

Steps == 1 .. N

\* Per-step state and the global saga phase.
VARIABLES
    stepState,   \* stepState[i] in {"notStarted","done","failed","compensated"}
    phase        \* "forward" | "rollback" | "committed" | "aborted"

vars == <<stepState, phase>>

StepStates == {"notStarted", "done", "failed", "compensated"}

TypeOK ==
    /\ stepState \in [Steps -> StepStates]
    /\ phase \in {"forward", "rollback", "committed", "aborted"}

Init ==
    /\ stepState = [i \in Steps |-> "notStarted"]
    /\ phase     = "forward"

(***************************************************************************)
(* FORWARD EXECUTION.                                                       *)
(*                                                                         *)
(* Step i may run only when every earlier step is already "done" (strict   *)
(* sequential order) and i itself has not started. It NON-DETERMINISTICALLY *)
(* succeeds (-> "done") or fails (-> "failed").                            *)
(***************************************************************************)

\* All strictly-earlier steps have completed their forward action.
EarlierAllDone(i) == \A j \in Steps : j < i => stepState[j] = "done"

\* T_i succeeds: notStarted -> done. Permitted only in the forward phase
\* with all earlier steps done.
StepSucceed(i) ==
    /\ phase = "forward"
    /\ stepState[i] = "notStarted"
    /\ EarlierAllDone(i)
    /\ stepState' = [stepState EXCEPT ![i] = "done"]
    /\ UNCHANGED phase

\* T_i FAILS: notStarted -> failed. This is the non-deterministic failure
\* that triggers compensation. We move into the rollback phase. (If no done
\* step exists yet -- i.e. step 1 failed -- rollback completes immediately.)
StepFail(i) ==
    /\ phase = "forward"
    /\ stepState[i] = "notStarted"
    /\ EarlierAllDone(i)
    /\ stepState' = [stepState EXCEPT ![i] = "failed"]
    /\ phase'     = "rollback"

\* When all steps are "done", the saga has committed successfully.
CommitSaga ==
    /\ phase = "forward"
    /\ \A i \in Steps : stepState[i] = "done"
    /\ phase' = "committed"
    /\ UNCHANGED stepState

(***************************************************************************)
(* ROLLBACK (COMPENSATION).                                                *)
(*                                                                         *)
(* During rollback, a "done" step i is compensated -> "compensated". The   *)
(* REVERSE-ORDER gate: i may be compensated only when no HIGHER-indexed     *)
(* step is still "done" -- every later committed step must already have     *)
(* been undone first. This forces C_(k-1), ..., C_1 in descending order.    *)
(***************************************************************************)

\* No later (higher-indexed) step is still in the "done" state.
LaterAllUndone(i) == \A j \in Steps : j > i => stepState[j] # "done"

\* Compensate done step i (reverse order enforced by LaterAllUndone).
Compensate(i) ==
    /\ phase = "rollback"
    /\ stepState[i] = "done"
    /\ LaterAllUndone(i)
    /\ stepState' = [stepState EXCEPT ![i] = "compensated"]
    /\ UNCHANGED phase

\* Rollback is finished once no step remains "done" (all formerly-done steps
\* have been compensated). The saga is then fully aborted.
AbortSaga ==
    /\ phase = "rollback"
    /\ \A i \in Steps : stepState[i] # "done"
    /\ phase' = "aborted"
    /\ UNCHANGED stepState

Next ==
    \/ \E i \in Steps :
           \/ StepSucceed(i)
           \/ StepFail(i)
           \/ Compensate(i)
    \/ CommitSaga
    \/ AbortSaga

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* SAFETY INVARIANTS.                                                       *)
(***************************************************************************)

\* Terminal (absorbing) states: the saga has reached a final verdict.
Terminal == phase \in {"committed", "aborted"}

\* (1) AtomicityViaCompensation -- the saga correctness property.
\* In every terminal state, EITHER the saga committed with all steps "done",
\* OR it aborted with NO step left "done" (every forward effect that
\* committed has since been compensated). There is no terminal state with a
\* committed-but-uncompensated step surviving alongside the failure: no
\* partial effect outlives a failed saga.
AtomicityViaCompensation ==
    Terminal =>
        \/ (\A i \in Steps : stepState[i] = "done")          \* committed
        \/ (\A i \in Steps : stepState[i] # "done")          \* fully rolled back

\* (2) CompensationOrder -- compensations run in reverse order of completion.
\* Whenever a step i is "compensated", every later (higher-indexed) step must
\* already be undone -- none may still be "done". A wrong rule that
\* compensated an earlier step while a later one was still committed would
\* violate this. (notStarted/failed later steps are fine: they hold no
\* committed effect to undo.)
CompensationOrder ==
    \A i \in Steps :
        stepState[i] = "compensated" =>
            (\A j \in Steps : j > i => stepState[j] # "done")

(***************************************************************************)
(* NON-VACUITY witnesses (negated as invariants so a TLC "violation" is a   *)
(* REACHABILITY PROOF that the state exists -- run separately, never in the *)
(* main safety cfg). See README for how these are exercised.               *)
(*   - CommittedReachable:        some run reaches an all-"done" commit.     *)
(*   - FullyCompensatedReachable: some run reaches abort after a failure     *)
(*                                with at least one step compensated.        *)
(***************************************************************************)
SagaCommitted     == phase = "committed" /\ (\A i \in Steps : stepState[i] = "done")
SagaCompensated   == /\ phase = "aborted"
                     /\ \E i \in Steps : stepState[i] = "compensated"
                     /\ \E i \in Steps : stepState[i] = "failed"

CommittedReachable        == ~SagaCommitted    \* TLC violation => commit reachable
FullyCompensatedReachable == ~SagaCompensated  \* TLC violation => rollback reachable

=============================================================================
