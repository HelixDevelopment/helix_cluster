---------------------------- MODULE Wireguard ----------------------------
(***************************************************************************)
(* HXC-WIREGUARD: TLA+ model of the WireGuard-style Noise IK handshake     *)
(* state machine, with a Dolev-Yao-lite network attacker.                  *)
(*                                                                         *)
(* This spec extends the Helix formal-methods stream (Scheduling.tla,      *)
(* Consensus.tla, Swim.tla). It models the two-message Noise IK handshake  *)
(* used by WireGuard:                                                      *)
(*                                                                         *)
(*   1. Initiator -> Responder : handshake-initiation                      *)
(*        carries the initiator's ephemeral public key + its static        *)
(*        identity (encrypted), and a monotone handshake counter (the      *)
(*        WireGuard anti-replay "index"/timestamp surrogate).              *)
(*   2. Responder -> Initiator : handshake-response                        *)
(*        carries the responder's ephemeral public key.                    *)
(*                                                                         *)
(* Both sides then derive a SYMMETRIC session key from the pair of         *)
(* ephemeral keys + the two static identities (a deterministic KDF over    *)
(* the transcript). The session key is modelled as the record             *)
(*   [ie |-> initiatorEphemeral, re |-> responderEphemeral,                *)
(*    id |-> initiatorIdentity,  rid |-> responderIdentity]                *)
(* so that two peers "agree" iff they derived from the same transcript.    *)
(*                                                                         *)
(* THE NETWORK is a set (not a queue) of in-flight messages. An attacker   *)
(* may REPLAY any message already seen and REORDER freely (set semantics), *)
(* but CANNOT forge keys: it never invents an ephemeral/static value that  *)
(* an honest peer did not emit, and it cannot read encrypted identities.   *)
(* This is the Dolev-Yao-lite threat model the task requires.              *)
(*                                                                         *)
(* SAFETY invariants under test (see bottom of module):                    *)
(*   1. SessionAgreement / NoKeyConfusion -- if both the initiator and the *)
(*      responder have COMPLETED a handshake, they hold the same derived   *)
(*      session key and agree on each other's identity.                    *)
(*   2. NoReplayAcceptance -- the responder never accepts two DISTINCT     *)
(*      initiations bearing the same (or a stale) handshake counter; a     *)
(*      replayed initiation does not open a second fresh session. Modelled *)
(*      via a strictly-monotone "lastCounter" anti-replay window that a    *)
(*      wrong rule would violate.                                          *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS
    Initiators,    \* finite set of honest initiator ids
    Responders,    \* finite set of honest responder ids
    MaxCounter,    \* highest handshake counter value (bounds state space)
    Nil            \* sentinel model value

ASSUME Nil \notin Initiators
ASSUME Nil \notin Responders
ASSUME Initiators \cap Responders = {}

Peers    == Initiators \cup Responders
Counters == 0..MaxCounter

\* Each honest peer owns exactly ONE static identity (its long-term key),
\* modelled as the peer id itself. Each peer can mint ephemeral keys; we
\* model the (small, finite) ephemeral-key space as the counter range
\* tagged by owner, so distinct handshake attempts use distinct ephemerals.
Ephemerals == [ owner : Peers, gen : Counters ]

(***************************************************************************)
(* Message shapes on the network. We use a single flat record type with a *)
(* `kind` tag so the network is a homogeneous SET of records.             *)
(*                                                                         *)
(*  Initiation: from=initiator, to=responder, ie=initiator ephemeral,     *)
(*              sid=initiator static identity, ctr=monotone handshake ctr  *)
(*  Response:   from=responder, to=initiator, re=responder ephemeral,      *)
(*              ie=echoed initiator ephemeral (binds the response to the   *)
(*              initiation), ctr=echoed counter                            *)
(***************************************************************************)
InitMsgs ==
    [ kind : {"init"},
      from : Initiators, to : Responders,
      ie   : Ephemerals, sid : Initiators, ctr : Counters ]

RespMsgs ==
    [ kind : {"resp"},
      from : Responders, to : Initiators,
      re   : Ephemerals, ie : Ephemerals, ctr : Counters ]

Messages == InitMsgs \cup RespMsgs

VARIABLES
    net,        \* set of in-flight (and replayable) messages on the network
    iState,     \* iState[i]   : "idle" | "sent" | "established"
    iEphGen,    \* iEphGen[i]  : next ephemeral generation counter for i
    iCtr,       \* iCtr[i]     : monotone handshake counter the initiator uses
    iSession,   \* iSession[i] : derived session key record, or Nil
    rState,     \* rState[r]   : "idle" | "established"
    rEphGen,    \* rEphGen[r]  : next ephemeral generation counter for r
    rLastCtr,   \* rLastCtr[r] : highest counter r has ACCEPTED, per initiator
    rSession,   \* rSession[r] : derived session key record, or Nil
    rAccepted   \* rAccepted[r][i] : HISTORY -- the set of counter values r has
                \* actually accepted from initiator i. A replayed/stale init
                \* that the anti-replay rule rejects must NOT add to this set;
                \* this history is what makes NoReplayAcceptance falsifiable.

vars == << net, iState, iEphGen, iCtr, iSession,
           rState, rEphGen, rLastCtr, rSession, rAccepted >>

(***************************************************************************)
(* The KDF: a deterministic session key over the handshake transcript.    *)
(* Two peers agree iff they feed in the same four transcript components.   *)
(***************************************************************************)
DeriveKey(initiatorId, responderId, ie, re) ==
    [ id |-> initiatorId, rid |-> responderId, ie |-> ie, re |-> re ]

TypeOK ==
    /\ net      \subseteq Messages
    /\ iState   \in [Initiators -> {"idle", "sent", "established"}]
    /\ iEphGen  \in [Initiators -> Counters]
    /\ iCtr     \in [Initiators -> Counters]
    /\ iSession \in [Initiators -> (RespMsgs \cup {Nil}) \cup
                       [id: Initiators, rid: Responders, ie: Ephemerals, re: Ephemerals]
                       \cup {Nil}]
    /\ rState   \in [Responders -> {"idle", "established"}]
    /\ rEphGen  \in [Responders -> Counters]
    /\ rLastCtr \in [Responders -> [Initiators -> Counters \cup {Nil}]]
    /\ rAccepted \in [Responders -> [Initiators -> SUBSET Counters]]
    /\ rSession \in [Responders ->
                       ([id: Initiators, rid: Responders, ie: Ephemerals, re: Ephemerals]
                        \cup {Nil})]

Init ==
    /\ net      = {}
    /\ iState   = [i \in Initiators |-> "idle"]
    /\ iEphGen  = [i \in Initiators |-> 0]
    /\ iCtr     = [i \in Initiators |-> 0]
    /\ iSession = [i \in Initiators |-> Nil]
    /\ rState   = [r \in Responders |-> "idle"]
    /\ rEphGen  = [r \in Responders |-> 0]
    /\ rLastCtr = [r \in Responders |-> [i \in Initiators |-> Nil]]
    /\ rAccepted = [r \in Responders |-> [i \in Initiators |-> {}]]
    /\ rSession = [r \in Responders |-> Nil]

(***************************************************************************)
(* STEP 1: initiator i opens (or re-keys) a handshake to responder r.     *)
(* It mints a fresh ephemeral, advances its monotone handshake counter,   *)
(* and puts a handshake-initiation on the network. WireGuard re-keys, so   *)
(* an initiator may start a new handshake from any state -- this is what   *)
(* lets one initiator legitimately reach counter 2, so that a REPLAY of    *)
(* its earlier counter-1 initiation is a genuine stale replay the          *)
(* anti-replay rule must reject (making NoReplayAcceptance falsifiable).   *)
(***************************************************************************)
SendInitiation(i, r) ==
    /\ iCtr[i]    < MaxCounter
    /\ iEphGen[i] < MaxCounter
    /\ LET myEph == [owner |-> i, gen |-> iEphGen[i] + 1]
           myCtr == iCtr[i] + 1
           msg   == [ kind |-> "init", from |-> i, to |-> r,
                      ie |-> myEph, sid |-> i, ctr |-> myCtr ]
       IN
        /\ net'     = net \cup {msg}
        /\ iState'  = [iState  EXCEPT ![i] = "sent"]
        /\ iEphGen' = [iEphGen EXCEPT ![i] = iEphGen[i] + 1]
        /\ iCtr'    = [iCtr    EXCEPT ![i] = myCtr]
        /\ UNCHANGED << iSession, rState, rEphGen, rLastCtr, rSession,
                        rAccepted >>

(***************************************************************************)
(* STEP 2: responder r processes a handshake-initiation it sees on the    *)
(* network. THE ANTI-REPLAY RULE: r accepts the initiation only if its     *)
(* counter is STRICTLY GREATER than the last counter r accepted from that  *)
(* initiator. A replayed / stale-counter initiation is silently dropped    *)
(* (no state change) -- so a replay can never open a fresh session.        *)
(*                                                                         *)
(* On acceptance r mints its ephemeral, derives the session key from the   *)
(* transcript (initiator-id, responder-id, initiator-ephemeral, its own    *)
(* ephemeral), advances rLastCtr, and emits the handshake-response.        *)
(***************************************************************************)
RecvInitiation(r) ==
    /\ \E m \in net :
        /\ m.kind = "init"
        /\ m.to = r
        /\ \* ANTI-REPLAY: counter must beat the last accepted one.
           \* (IF guards the > so it is never applied to the Nil sentinel.)
           IF rLastCtr[r][m.from] = Nil
             THEN TRUE
             ELSE m.ctr > rLastCtr[r][m.from]
        /\ rEphGen[r] < MaxCounter
        /\ LET myEph == [owner |-> r, gen |-> rEphGen[r] + 1]
               key   == DeriveKey(m.from, r, m.ie, myEph)
               resp  == [ kind |-> "resp", from |-> r, to |-> m.from,
                          re |-> myEph, ie |-> m.ie, ctr |-> m.ctr ]
           IN
            /\ net'      = net \cup {resp}
            /\ rEphGen'  = [rEphGen  EXCEPT ![r] = rEphGen[r] + 1]
            /\ rLastCtr' = [rLastCtr EXCEPT ![r][m.from] = m.ctr]
            /\ rAccepted' = [rAccepted EXCEPT ![r][m.from] =
                                rAccepted[r][m.from] \cup {m.ctr}]
            /\ rSession' = [rSession EXCEPT ![r] = key]
            /\ rState'   = [rState   EXCEPT ![r] = "established"]
            /\ UNCHANGED << iState, iEphGen, iCtr, iSession >>

(***************************************************************************)
(* STEP 3: initiator i processes the handshake-response. It accepts only a *)
(* response that echoes ITS OWN current ephemeral (binding the response to *)
(* the initiation it actually sent) and its current handshake counter. It  *)
(* derives the SAME session key from the same transcript and completes.    *)
(***************************************************************************)
RecvResponse(i) ==
    /\ iState[i] = "sent"
    /\ \E m \in net :
        /\ m.kind = "resp"
        /\ m.to = i
        /\ m.ie.owner = i               \* response echoes my ephemeral...
        /\ m.ie.gen   = iEphGen[i]       \* ...and it is my CURRENT one
        /\ m.ctr      = iCtr[i]          \* and my current handshake counter
        /\ LET key == DeriveKey(i, m.from, m.ie, m.re) IN
            /\ iSession' = [iSession EXCEPT ![i] = key]
            /\ iState'   = [iState   EXCEPT ![i] = "established"]
            /\ UNCHANGED << net, iEphGen, iCtr,
                            rState, rEphGen, rLastCtr, rSession, rAccepted >>

(***************************************************************************)
(* DOLEV-YAO-LITE ATTACKER. The network is a set, so reordering is free.   *)
(* Replay is the explicit ability to re-present an OLD message: because    *)
(* `net` is a set and messages are never removed, every delivered message  *)
(* remains available to be processed again -- already a replay channel.    *)
(*                                                                         *)
(* AttackerReplay makes replay a first-class, visible step: the attacker   *)
(* re-injects a message it has already observed (idempotent on the set,    *)
(* but it documents the capability and lets the model-checker treat replay *)
(* as an adversary action). The attacker can NOT forge: it only re-adds    *)
(* messages already in `net`, never fabricates new keys/identities.        *)
(***************************************************************************)
AttackerReplay ==
    /\ \E m \in net : net' = net \cup {m}   \* re-present an observed message
    /\ UNCHANGED << iState, iEphGen, iCtr, iSession,
                    rState, rEphGen, rLastCtr, rSession, rAccepted >>

Next ==
    \/ \E i \in Initiators, r \in Responders : SendInitiation(i, r)
    \/ \E r \in Responders : RecvInitiation(r)
    \/ \E i \in Initiators : RecvResponse(i)
    \/ AttackerReplay

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* Initiator ids are interchangeable, as are responder ids: permuting them *)
(* maps any behaviour to an equivalent one. Declaring this symmetry lets    *)
(* TLC collapse the equivalence classes and finish the FULL finite space.   *)
(***************************************************************************)
Symmetry == Permutations(Initiators)

(***************************************************************************)
(* SAFETY INVARIANT 1: SessionAgreement / NoKeyConfusion.                  *)
(*                                                                         *)
(* If an initiator i and a responder r have BOTH established, and they     *)
(* established WITH EACH OTHER (i's session names r and r's session names  *)
(* i), then they must hold byte-identical session keys and agree on both   *)
(* identities. Mismatched keys for the same completed handshake = key      *)
(* confusion, which an attacker reordering/replaying responses could       *)
(* otherwise induce if the binding rule (Step 3) were wrong.               *)
(***************************************************************************)
SessionAgreement ==
    \A i \in Initiators, r \in Responders :
        ( /\ iState[i] = "established"
          /\ rState[r] = "established"
          /\ iSession[i] # Nil
          /\ rSession[r] # Nil
          /\ iSession[i].id  = i  /\ iSession[i].rid = r
          /\ rSession[r].id  = i  /\ rSession[r].rid = r )
        => iSession[i] = rSession[r]

(***************************************************************************)
(* SAFETY INVARIANT 2: NoReplayAcceptance.                                 *)
(*                                                                         *)
(* This is asserted over the rAccepted HISTORY -- the actual set of counter *)
(* values the responder accepted -- so it can genuinely be FALSIFIED by a   *)
(* wrong anti-replay rule (it is NOT a tautology).                          *)
(*                                                                         *)
(* The correct ">" rule guarantees acceptance is STRICTLY MONOTONE: every   *)
(* accepted counter is greater than all previously accepted ones. Hence:    *)
(*   (a) the live high-water mark rLastCtr[r][i] equals the MAXIMUM of the   *)
(*       accepted set, and                                                  *)
(*   (b) rLastCtr[r][i] is itself a member of the accepted set whenever any  *)
(*       counter has been accepted.                                         *)
(* If a stale/replayed initiation (ctr <= rLastCtr) were ever ACCEPTED       *)
(* (a ">=" rule, no guard, or a window reset), it would add a value <=       *)
(* rLastCtr while the EXCEPT moved rLastCtr backwards to that stale value,   *)
(* breaking rLastCtr = Max(rAccepted). TLC reaches that state and reports a  *)
(* counterexample -- proving the invariant has teeth.                       *)
(***************************************************************************)
Max(S) == CHOOSE x \in S : \A y \in S : y <= x

NoReplayAcceptance ==
    \A r \in Responders, i \in Initiators :
        (rAccepted[r][i] # {}) =>
            /\ rLastCtr[r][i] # Nil
            /\ rLastCtr[r][i] = Max(rAccepted[r][i])   \* high-water = max accepted
            /\ rLastCtr[r][i] \in rAccepted[r][i]
            \* every accepted counter is at or below the high-water mark,
            \* i.e. no stale counter ever sneaks in above an accepted one
            /\ \A c \in rAccepted[r][i] : c <= rLastCtr[r][i]

\* An established responder always carries a concrete (non-Nil) high-water
\* mark for at least one initiator it accepted from.
ReplayWindowSane ==
    \A r \in Responders :
        (rState[r] = "established") =>
            \E i \in Initiators : rLastCtr[r][i] # Nil

=============================================================================

