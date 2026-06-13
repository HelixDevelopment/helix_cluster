-------------------------------- MODULE Stonith --------------------------------
(***************************************************************************)
(* TLA+ model of STONITH (Shoot-The-Other-Node-In-The-Head) node fencing   *)
(* SAFETY -- the split-brain protection implemented by pkg/stonith         *)
(* (stonith.go MultiLevelFencer, ipmi.go / cloud.go / sbd.go agents).      *)
(*                                                                         *)
(* WHY fence at all (from stonith.go): when a node is SUSPECTED faulty the  *)
(* cluster wants another node to take over its resources (a virtual IP, a   *)
(* storage volume, a primary role). But a suspicion can be WRONG -- the     *)
(* "failed" node may actually still be alive (a network partition, a GC     *)
(* pause, a slow link). If a survivor takes over the resource while the     *)
(* suspected node is still alive and still acting on it, BOTH nodes are     *)
(* active on the same resource at once: SPLIT-BRAIN / dual-active, which     *)
(* corrupts shared storage or duplicates a service IP.                     *)
(*                                                                         *)
(* STONITH's rule -- the load-bearing invariant of stonith.go -- is         *)
(* "confirm, don't assume": a survivor may take over the resource ONLY      *)
(* after the suspected previous owner has been *confirmed fenced* (powered  *)
(* off via IPMI/cloud, or cut off via SBD poison). Fencing forcibly stops   *)
(* the old owner from acting on the resource BEFORE the new owner activates.*)
(* MultiLevelFencer.Fence returns a Confirmation only once the fenced state *)
(* is verified; takeover is gated on that confirmation. Dropping the gate   *)
(* (taking over on mere suspicion) is precisely the split-brain hazard.     *)
(*                                                                         *)
(* This spec verifies that the gate is SUFFICIENT: with takeover gated on a *)
(* confirmed fence, two nodes are NEVER simultaneously active on the shared *)
(* resource -- even when the suspected node was a FALSE positive (actually  *)
(* still alive). The mutation spec (Stonith.cfg's sibling run) drops the    *)
(* fence gate and TLC produces the dual-active counterexample.             *)
(*                                                                         *)
(* MODEL                                                                    *)
(*   * Nodes: a small finite set of node ids (2 in the cfg -- the minimum   *)
(*     that makes an owner + a taker-over, hence split-brain, reachable).   *)
(*   * One SHARED RESOURCE that at most one node may be ACTIVE on (own).    *)
(*   * liveness[n]: whether node n is actually still ALIVE (TRUE) or has    *)
(*     genuinely crashed (FALSE). A node can crash on its own.              *)
(*   * status[n] \in {Up, Suspected, Fenced}: the CLUSTER's belief +        *)
(*     fence state for n. Up = trusted. Suspected = some node reported n    *)
(*     as failed (the suspicion may be FALSE: liveness[n] may still be TRUE).*)
(*     Fenced = STONITH has CONFIRMED n is shot (powered off / cut off);    *)
(*     a Fenced node can no longer act on the resource.                     *)
(*   * active[n]: whether node n is currently ACTIVE on (owns) the resource.*)
(*                                                                         *)
(* The dangerous case is modelled head-on: Suspect may fire on a node whose *)
(* liveness is still TRUE (a FALSE suspicion). Such a node, until fenced,    *)
(* may keep ResourceActOnly-acting and could activate; only the Fence step  *)
(* (which forces active[n] := FALSE and status[n] := Fenced) makes it safe  *)
(* for a survivor to take over.                                            *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Nodes,        \* finite set of node ids (model values)
          MaxFences     \* bound on number of fence actions (bounds state space)

\* Node status / fence-state values (model values, declared in the .cfg).
CONSTANTS Up,           \* node is trusted, no suspicion outstanding
          Suspected,    \* some node reported this node as failed (maybe falsely)
          Fenced        \* STONITH confirmed: node is shot, can no longer act

ASSUME MaxFences \in Nat

Status == {Up, Suspected, Fenced}

VARIABLES
    liveness,   \* liveness[n] \in BOOLEAN : is n ACTUALLY still alive?
    status,     \* status[n]   \in Status  : cluster belief + fence state for n
    active,     \* active[n]   \in BOOLEAN : does n currently own/act on the resource?
    fenceCount, \* number of fence actions taken (bounds the state space)
    lastTookOK, \* witness: was the most recent Takeover's victim confirmed Fenced
                \* at the instant of takeover? TRUE before any takeover. This
                \* records the takeover discipline directly so the invariant is
                \* exact and the mutation has a clean property to break.
    tookOver    \* witness: has a Takeover action EVER fired? FALSE in Init, set
                \* TRUE by Takeover. Lets us prove (non-vacuity) that a genuine
                \* fence-then-takeover transition is actually reachable.

vars == <<liveness, status, active, fenceCount, lastTookOK, tookOver>>

TypeOK ==
    /\ liveness   \in [Nodes -> BOOLEAN]
    /\ status     \in [Nodes -> Status]
    /\ active     \in [Nodes -> BOOLEAN]
    /\ fenceCount \in 0..MaxFences
    /\ lastTookOK \in BOOLEAN
    /\ tookOver   \in BOOLEAN

(***************************************************************************)
(* Init: exactly ONE node starts ACTIVE on the resource (the initial       *)
(* owner), all nodes alive and Up. Choosing a single initial owner keeps    *)
(* the start state a valid single-active configuration; the spec then       *)
(* explores suspicion / fence / takeover from there.                       *)
(***************************************************************************)
Init ==
    /\ liveness   = [n \in Nodes |-> TRUE]
    /\ status     = [n \in Nodes |-> Up]
    /\ \E owner \in Nodes : active = [n \in Nodes |-> n = owner]
    /\ fenceCount = 0
    /\ lastTookOK = TRUE
    /\ tookOver   = FALSE

(***************************************************************************)
(* Crash(n): node n genuinely dies. Its liveness becomes FALSE. A crash     *)
(* does NOT by itself relinquish the resource cleanly -- a crashed node may  *)
(* still be recorded active until it is fenced (that is exactly why fencing  *)
(* must force active:=FALSE rather than trust the node to release).         *)
(***************************************************************************)
Crash(n) ==
    /\ liveness[n] = TRUE
    /\ liveness' = [liveness EXCEPT ![n] = FALSE]
    /\ UNCHANGED <<status, active, fenceCount, lastTookOK, tookOver>>

(***************************************************************************)
(* Suspect(n): the cluster suspects node n has failed. CRUCIALLY this may    *)
(* fire whether or not n is actually dead -- a FALSE suspicion (liveness[n]  *)
(* still TRUE) is the dangerous case the whole spec is about. We only move    *)
(* an Up node to Suspected (a Fenced node stays fenced; re-suspecting is a    *)
(* no-op we exclude to keep the model tight).                               *)
(***************************************************************************)
Suspect(n) ==
    /\ status[n] = Up
    /\ status' = [status EXCEPT ![n] = Suspected]
    /\ UNCHANGED <<liveness, active, fenceCount, lastTookOK, tookOver>>

(***************************************************************************)
(* Fence(n): STONITH shoots node n. This is the real pkg/stonith            *)
(* MultiLevelFencer.Fence returning a confirmed Confirmation: the fence is   *)
(* CONFIRMED to have taken effect. Two effects, both load-bearing:           *)
(*   1. status[n] := Fenced  -- the cluster now KNOWS n is shot.             *)
(*   2. active[n] := FALSE   -- n is forcibly stopped from acting on the     *)
(*      resource (powered off / cut off). This is what makes a subsequent    *)
(*      takeover safe: the old owner cannot be dual-active because fencing    *)
(*      removed it from the resource BEFORE any takeover.                    *)
(* A node is fenced only out of Suspected (you suspect, THEN you shoot).     *)
(* Confirmed fencing also pins liveness[n] := FALSE: a shot node is dead     *)
(* (powered off), so it can no longer be (falsely) alive and acting.         *)
(***************************************************************************)
Fence(n) ==
    /\ fenceCount < MaxFences
    /\ status[n] = Suspected
    /\ status'   = [status EXCEPT ![n] = Fenced]
    /\ active'   = [active EXCEPT ![n] = FALSE]
    /\ liveness' = [liveness EXCEPT ![n] = FALSE]
    /\ fenceCount' = fenceCount + 1
    /\ UNCHANGED <<lastTookOK, tookOver>>

(***************************************************************************)
(* Takeover(taker, victim): node `taker` activates on (takes over) the      *)
(* resource previously owned by `victim`. THE STONITH GATE: this is allowed  *)
(* ONLY because `victim` is confirmed Fenced. Because Fence already forced    *)
(* active[victim] := FALSE, activating the taker cannot create two active     *)
(* nodes. `taker` must itself be a usable node (alive and not fenced).       *)
(*                                                                         *)
(* This gate -- status[victim] = Fenced -- is the entire safety mechanism.   *)
(* The mutation spec drops it (allowing takeover on status[victim] =          *)
(* Suspected, i.e. on mere suspicion), and TLC then reaches a state where a   *)
(* FALSE-suspected-but-alive victim is still active while the taker also      *)
(* activates: NoDualActive fails.                                           *)
(***************************************************************************)
Takeover(taker, victim) ==
    /\ taker # victim
    /\ status[victim] = Fenced        \* THE GATE: victim confirmed shot first
    /\ active[victim] = FALSE         \* (implied by Fence, asserted for clarity)
    /\ active[taker]  = FALSE         \* taker is not already active
    /\ liveness[taker] = TRUE         \* taker is actually alive
    /\ status[taker]  # Fenced        \* taker is a usable node
    /\ active' = [active EXCEPT ![taker] = TRUE]
    \* Record whether the victim was confirmed Fenced at the moment of takeover.
    \* In the correct spec the gate above forces this TRUE; the mutation drops
    \* the gate, so a Suspected (un-fenced) victim makes this FALSE.
    /\ lastTookOK' = (status[victim] = Fenced)
    /\ tookOver'   = TRUE
    /\ UNCHANGED <<liveness, status, fenceCount>>

Next ==
    \/ \E n \in Nodes : Crash(n)
    \/ \E n \in Nodes : Suspect(n)
    \/ \E n \in Nodes : Fence(n)
    \/ \E taker, victim \in Nodes : Takeover(taker, victim)

Spec == Init /\ [][Next]_vars

\* Symmetry set for TLC: node ids are interchangeable model values -- the spec
\* never orders them and both safety invariants are symmetric under any
\* permutation of Nodes (see the .cfg justification).
Symm == Permutations(Nodes)

(***************************************************************************)
(* SAFETY 1 -- NoDualActive (THE fencing safety property).                  *)
(*                                                                         *)
(* At no reachable state are TWO distinct nodes simultaneously ACTIVE on the *)
(* shared resource. A takeover is safe ONLY because the previous owner was   *)
(* fenced first: Fence forces the old owner inactive (active:=FALSE) BEFORE  *)
(* any Takeover can activate a survivor, so the count of active nodes never  *)
(* exceeds one.                                                             *)
(*                                                                         *)
(* This invariant is MEANINGFUL, not vacuous: a FALSE-suspected-but-alive    *)
(* node (Suspect fired with liveness still TRUE) remains active until fenced;*)
(* without the Fence gate a Takeover would activate the survivor alongside   *)
(* it -> two active nodes. The mutation removes the gate and TLC reaches      *)
(* exactly that dual-active state. (Reachability of the false-suspicion and  *)
(* of a real fence-then-takeover sequence is proven via the negated          *)
(* witnesses below.)                                                        *)
(***************************************************************************)
NoDualActive ==
    \A a, b \in Nodes : (active[a] /\ active[b]) => a = b

(***************************************************************************)
(* SAFETY 2 -- TakeoverImpliesFenced.                                       *)
(*                                                                         *)
(* A node becomes ACTIVE via takeover ONLY if the previous owner (the        *)
(* victim) was in the FENCED state at the instant of takeover. We record      *)
(* that fact directly: Takeover sets lastTookOK := (status[victim] = Fenced),*)
(* so lastTookOK is TRUE exactly when every takeover so far respected the     *)
(* fence gate. The invariant asserts it always holds (and, via TypeOK, that   *)
(* the whole state is well-typed):                                          *)
(*                                                                         *)
(*     lastTookOK = TRUE.                                                    *)
(*                                                                         *)
(* This is the takeover discipline of pkg/stonith stated as a checkable       *)
(* invariant: MultiLevelFencer.Fence must return a confirmed Confirmation     *)
(* (victim Fenced) before a survivor activates. It is NOT vacuous -- a real   *)
(* fence-then-takeover sequence is reachable (witness ReachTakeoverAfterFence)*)
(* and exercises a takeover whose victim IS Fenced (keeping lastTookOK TRUE). *)
(* The mutation spec drops the gate, lets a survivor take over a merely       *)
(* Suspected victim, and TLC reaches lastTookOK = FALSE -- the violation.     *)
(***************************************************************************)
TakeoverImpliesFenced ==
    /\ TypeOK
    /\ lastTookOK = TRUE

-------------------------------------------------------------------------
(* Non-vacuity witnesses: each is an invariant whose NEGATION is reachable,  *)
(* so checking it makes TLC print a reachable witness state (see README).    *)

\* (a) A confirmed FENCE actually happened: some node reaches the Fenced state.
\* Negated: assert no node is Fenced; TLC's counterexample is a reached fenced
\* state -- proving the fence path is exercised, not dead code.
ReachFence == \A n \in Nodes : status[n] # Fenced

\* (b) A FALSE SUSPICION is reachable: a node is Suspected while STILL ALIVE
\* (liveness TRUE) -- the dangerous split-brain setup. Negated: assert no node
\* is both Suspected and alive; TLC reaches one.
ReachFalseSuspicion ==
    \A n \in Nodes : ~(status[n] = Suspected /\ liveness[n] = TRUE)

\* (c) A GENUINE fence-then-takeover transition actually fired: the Takeover
\* action ran (tookOver set TRUE) AND it ran legitimately (lastTookOK TRUE,
\* i.e. the victim was confirmed Fenced) AND a survivor is now active beside the
\* Fenced victim. This proves the safe takeover path is real and reachable in
\* the correct spec (not just that fencing alone happens). Negated: assert this
\* completed-takeover signature never occurs; TLC reaches it as a witness.
ReachTakeoverAfterFence ==
    ~ ( tookOver
        /\ lastTookOK
        /\ \E t, v \in Nodes : t # v /\ active[t] /\ status[v] = Fenced )
================================================================================
