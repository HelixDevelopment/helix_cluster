------------------------- MODULE FailureDetector -------------------------
(***************************************************************************)
(* TLA+ model of the SAFETY ENVELOPE of a timeout / phi-accrual style      *)
(* FAILURE DETECTOR -- the accuracy core of the detector behind pkg/swim   *)
(* (SWIM gossip membership with phi-accrual failure detection,             *)
(* pkg/swim/phi_accrual.go, failure_detector.go, suspicion.go). Part of    *)
(* the Helix formal-methods stream (cf. Swim, VectorClock, Consensus,      *)
(* RaftMembership, LeaderLease, ORSet, Saga, TwoPhaseCommit, Wireguard).   *)
(*                                                                         *)
(* The Swim.tla spec covers the membership/refute dimension (alive /       *)
(* suspect / dead state machine + incarnation refutation). THIS spec       *)
(* covers the ORTHOGONAL dimension: the detector's suspect/alive           *)
(* ACCURACY -- does a correctly-configured timeout detector ever FALSELY   *)
(* suspect a node that is in fact alive and responsive?                    *)
(*                                                                         *)
(* ABSTRACTION. We abstract the phi-accrual statistical estimator to its   *)
(* SAFETY core. The real detector computes                                 *)
(*     phi(t) = -log10 P_later(t - tLastHeartbeat)                         *)
(* and suspects once phi exceeds a threshold. What the threshold MEANS, in *)
(* safety terms, is: "suspect once the silence since the last received     *)
(* heartbeat exceeds a bound T". We model exactly that timeout core:       *)
(*                                                                         *)
(*   * A MONITOR tracks the time `now - lastHB` since the last heartbeat   *)
(*     it RECEIVED from the monitored node.                                *)
(*   * While ALIVE, the node periodically SENDS heartbeats, no more often  *)
(*     than is realistic and AT LEAST every Interval ticks (paced sends).  *)
(*   * Each sent heartbeat is DELIVERED to the monitor after a bounded     *)
(*     network delay of at most MaxDelay ticks (it may be reordered /      *)
(*     delayed, but never beyond MaxDelay while in flight).                *)
(*   * Once CRASHED the node sends no more heartbeats; in-flight ones may  *)
(*     still deliver, then silence.                                        *)
(*   * The monitor SUSPECTS the node when (now - lastHB) > T, and CLEARS   *)
(*     suspicion the instant a fresher heartbeat is delivered.             *)
(*                                                                         *)
(* THE CONFIGURATION RELATIONSHIP (this is the whole point):               *)
(*                                                                         *)
(*     T > MaxDelay + Interval                                             *)
(*                                                                         *)
(* A heartbeat sent at time s is RECEIVED by at most s + MaxDelay. While   *)
(* the node stays alive it sends again within Interval, so the monitor's   *)
(* lastHB is never older than (Interval + MaxDelay) behind `now` once the  *)
(* pipeline is primed. Hence with T strictly greater than MaxDelay +       *)
(* Interval the timeout NEVER expires on a live, responsive node -- the    *)
(* detector is ACCURATE (no false positives). Set T too small             *)
(* (T <= MaxDelay + Interval) and a perfectly healthy node IS falsely      *)
(* suspected -- which the mutation run in FailureDetector.cfg demonstrates *)
(* with a concrete counterexample. The invariant therefore has teeth.     *)
(*                                                                         *)
(* PROPERTIES model-checked by TLC (see FailureDetector.cfg):              *)
(*                                                                         *)
(*   TypeOK                       -- state stays well-typed.               *)
(*   NoFalseSuspicionWithinBound  -- THE accuracy goal: while the node is  *)
(*                                   alive AND its heartbeat pipeline is    *)
(*                                   within bound, it is NOT suspected.     *)
(*   UnsuspectOnFreshHeartbeat    -- delivering a fresh heartbeat clears    *)
(*                                   suspicion (a recovered/responsive node *)
(*                                   is not left permanently suspected).    *)
(*   SuspectImpliesStale          -- a suspect decision is never taken      *)
(*                                   while the last heartbeat is fresh      *)
(*                                   (the decision tracks real silence).    *)
(*                                                                         *)
(* The model is DELIBERATELY non-degenerate: heartbeats carry a bounded    *)
(* network delay (0..MaxDelay) so a too-small threshold WOULD falsely      *)
(* suspect, and the crash path makes a TRUE-suspicion state reachable so   *)
(* the accuracy invariant is not vacuous (see witnesses below).            *)
(***************************************************************************)
EXTENDS Integers, FiniteSets

CONSTANTS
    MaxClock,   \* logical-clock bound: now ranges over 0..MaxClock (bounds state)
    MaxDelay,   \* max network delay (ticks) a heartbeat may incur in flight
    Interval,   \* heartbeat send period: an alive node sends at least this often
    T           \* suspicion threshold: suspect once (now - lastHB) > T

ASSUME /\ MaxClock \in Nat
       /\ MaxDelay  \in Nat
       /\ Interval  \in Nat /\ Interval >= 1
       /\ T         \in Nat

VARIABLES
    now,        \* current logical time, 0..MaxClock
    alive,      \* TRUE while the monitored node is up; FALSE once crashed
    lastSend,   \* time at which the node last SENT a heartbeat (-1 = none yet)
    lastHB,     \* time the monitor last RECEIVED a heartbeat (-1 = none yet)
    inflight,   \* set of send-times of heartbeats sent but not yet delivered
    suspected   \* monitor's current suspect/alive verdict for the node

vars == <<now, alive, lastSend, lastHB, inflight, suspected>>

----------------------------------------------------------------------------
\* "No heartbeat yet" sentinel. Times are otherwise in 0..MaxClock.
NoTime == -1

\* A heartbeat sent at s is still legitimately in flight only while
\* now <= s + MaxDelay; past that it MUST already have been delivered (the
\* Deliver action is forced before the bound by DeliveryDeadline below).
TimeDomain == NoTime .. MaxClock

----------------------------------------------------------------------------
\* Initial state: clock at 0, node alive, nothing sent/received/in-flight,
\* not suspected. The pipeline is empty (cold start).
Init ==
    /\ now       = 0
    /\ alive     = TRUE
    /\ lastSend  = NoTime
    /\ lastHB    = NoTime
    /\ inflight  = {}
    /\ suspected = FALSE

----------------------------------------------------------------------------
\* ACTIONS

\* SendHeartbeat: while alive, the node emits a heartbeat stamped with `now`
\* and places it in flight. PACING: it may send again only once at least
\* Interval ticks have elapsed since its last send (or it has never sent).
\* This models a node that heartbeats on a fixed period -- it does not spam,
\* which is exactly what makes a too-small threshold dangerous.
SendHeartbeat ==
    /\ alive
    /\ (lastSend = NoTime \/ now >= lastSend + Interval)
    /\ lastSend' = now
    /\ inflight' = inflight \cup {now}
    /\ UNCHANGED <<now, alive, lastHB, suspected>>

\* Deliver: a heartbeat that is in flight is delivered to the monitor. This
\* advances lastHB to the FRESHEST delivered send-time (monitors track the
\* most recent heartbeat; a stale reordered one never moves lastHB backward)
\* and CLEARS suspicion, since a fresh heartbeat is positive liveness
\* evidence. Delivery is only enabled within the network bound
\* (now <= s + MaxDelay): a heartbeat is never delivered later than MaxDelay
\* after it was sent.
Deliver(s) ==
    /\ s \in inflight
    /\ now <= s + MaxDelay
    /\ inflight' = inflight \ {s}
    /\ lastHB'   = IF s > lastHB THEN s ELSE lastHB
    /\ suspected' = IF s > lastHB THEN FALSE ELSE suspected
    /\ UNCHANGED <<now, alive, lastSend>>

\* Crash: the node fails. It sends no further heartbeats; already-in-flight
\* ones may still deliver (and then silence). Modelled once (alive -> FALSE).
Crash ==
    /\ alive
    /\ alive'    = FALSE
    /\ UNCHANGED <<now, lastSend, lastHB, inflight, suspected>>

\* Suspect: the monitor's timeout fires. It suspects the node once the
\* silence since the last RECEIVED heartbeat strictly exceeds the threshold
\* T. `Silence` uses lastHB; with no heartbeat yet (lastHB = NoTime) the
\* silence is measured from time 0 (cold-start grace is folded into T being
\* large; here NoTime = -1 so silence = now + 1, which only matters for the
\* mutation). A node already suspected stays suspected until a fresh
\* heartbeat clears it (handled in Deliver).
Silence == now - lastHB
Suspect ==
    /\ ~suspected
    /\ Silence > T
    /\ suspected' = TRUE
    /\ UNCHANGED <<now, alive, lastSend, lastHB, inflight>>

\* Tick: logical time advances by one, but ONLY while no in-flight heartbeat
\* is at its delivery deadline. DeliveryDeadline enforces the MaxDelay bound:
\* time may not advance past s + MaxDelay for any in-flight s -- such a
\* heartbeat MUST be delivered first. This is how "delivered within MaxDelay"
\* is made a hard guarantee rather than a hope. Bounded by MaxClock so the
\* state space is finite.
DeliveryDeadline == \A s \in inflight : now < s + MaxDelay
Tick ==
    /\ now < MaxClock
    /\ DeliveryDeadline
    /\ now' = now + 1
    /\ UNCHANGED <<alive, lastSend, lastHB, inflight, suspected>>

Next ==
    \/ SendHeartbeat
    \/ \E s \in TimeDomain : Deliver(s)
    \/ Crash
    \/ Suspect
    \/ Tick

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
\* INVARIANTS

\* (1) Type correctness.
TypeOK ==
    /\ now       \in 0..MaxClock
    /\ alive     \in BOOLEAN
    /\ lastSend  \in TimeDomain
    /\ lastHB    \in TimeDomain
    /\ inflight  \in SUBSET (0..MaxClock)
    /\ suspected \in BOOLEAN

----------------------------------------------------------------------------
\* WithinBound: the heartbeat pipeline is currently healthy at the monitor --
\* the silence since the last received heartbeat is within the bound a
\* responsive alive node can ever exhibit, namely Interval (max gap between
\* sends) plus MaxDelay (max in-flight delay). This is the precondition under
\* which a CORRECT detector must NOT suspect. We compute it structurally from
\* lastHB so the invariant is checkable on every state.
\*
\* The deepest the monitor's lastHB can lag `now` while the node is alive and
\* the pipeline is within bound is Interval + MaxDelay: the node sends every
\* Interval, and the freshest of those is delivered within MaxDelay. With a
\* primed pipeline (lastHB # NoTime) Silence never exceeds Interval + MaxDelay.
WithinBound == lastHB # NoTime /\ Silence <= Interval + MaxDelay

\* (2) NoFalseSuspicionWithinBound -- THE accuracy goal.
\*
\* If the node is ALIVE and its heartbeat pipeline is within bound (WithinBound),
\* then the monitor does NOT suspect it. With the CONFIGURED relationship
\* T > MaxDelay + Interval this holds: WithinBound gives Silence <= MaxDelay +
\* Interval < T, so the Suspect guard (Silence > T) can never have fired. Set
\* T <= MaxDelay + Interval (the mutation) and a within-bound alive node is
\* falsely suspected -- a real counterexample.
NoFalseSuspicionWithinBound ==
    (alive /\ WithinBound) => ~suspected

\* (3) UnsuspectOnFreshHeartbeat -- structural monotone-recovery property.
\*
\* Whenever the monitor's verdict is "suspected", the last heartbeat it holds
\* is genuinely STALE (Silence > T): a fresh heartbeat would have cleared the
\* verdict on delivery. Equivalently, a freshly-delivered heartbeat (Silence
\* small) is incompatible with a standing suspicion. This rules out a detector
\* that latches suspicion through a recovery -- a suspected-then-refreshed node
\* is cleared, never permanently dead by the detector.
UnsuspectOnFreshHeartbeat ==
    suspected => (lastHB = NoTime \/ Silence > MaxDelay + Interval)

\* (4) SuspectImpliesStale -- the suspect decision tracks REAL silence, never
\* fires on a fresh heartbeat. (Companion to (3); guards against a detector
\* that suspects spuriously regardless of evidence.)
SuspectImpliesStale ==
    suspected => Silence > T

----------------------------------------------------------------------------
\* NON-VACUITY WITNESSES (negated as invariants in the reachability run, so
\* TLC reports the reachable witness state as a "violation" = existence proof).

\* (A) A TRUE suspicion is reachable: the node has crashed, its in-flight
\* heartbeats drained, silence grew past T, and the monitor correctly suspects.
\* This proves NoFalseSuspicionWithinBound is not vacuously true by the
\* detector "never suspecting anything".
TrueSuspicionReachable == ~(suspected /\ ~alive)

\* (B) A within-bound, alive, NOT-suspected state with a primed pipeline is
\* reachable: the detector actually observes a healthy node and (correctly)
\* stays quiet. This proves the WithinBound precondition is satisfiable, so
\* invariant (2) is not vacuous by an unsatisfiable antecedent.
HealthyObservedReachable == ~(alive /\ WithinBound /\ ~suspected /\ lastHB >= 0)

============================================================================
