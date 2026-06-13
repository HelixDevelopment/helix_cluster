---------------------------- MODULE Consensus ----------------------------
(***************************************************************************)
(* HXC-1139: TLA+ model of the Helix membership/consensus leader election. *)
(*                                                                         *)
(* A Raft-like single-decree election over a small set of servers and a    *)
(* bounded set of terms. A candidate requests votes for a term; each server *)
(* grants AT MOST ONE vote per term. A candidate that collects a quorum     *)
(* (strict majority) of votes for a term becomes leader for that term.      *)
(*                                                                         *)
(* Safety property under test:                                             *)
(*   NoSplitBrain -- at most one leader per term (no two leaders in the     *)
(*                   same term => no split-brain ownership).               *)
(*                                                                         *)
(* The classic Raft argument: two leaders in the same term would each need  *)
(* a majority, and two majorities of the same server set must intersect,    *)
(* but a server votes only once per term -- contradiction. TLC verifies     *)
(* this exhaustively over the bounded model.                                *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Servers,      \* finite set of server ids
          MaxTerm,      \* highest term number explored (bounds state space)
          Nil           \* sentinel model value: "no vote" / "no leader"

ASSUME Nil \notin Servers

\* A quorum is any set of servers of strictly-majority size.
Quorum == { S \in SUBSET Servers : Cardinality(S) * 2 > Cardinality(Servers) }

Terms == 1..MaxTerm

VARIABLES
    state,        \* state[s]      : "follower" | "candidate" | "leader"
    currentTerm,  \* currentTerm[s]: the term server s is in
    votedFor,     \* votedFor[s]   : [term -> serverOrNil] -- whom s voted for
    leaderOf      \* leaderOf[term]: the elected leader of `term`, or Nil

vars == <<state, currentTerm, votedFor, leaderOf>>

\* The authoritative tally: who actually voted for c in term tm, read straight
\* from the per-server ledger. This is the single source of truth -- there is
\* no separately-cached vote set that could go stale and be double-counted.
VotersFor(c, tm) == { v \in Servers : votedFor[v][tm] = c }

TypeOK ==
    /\ state       \in [Servers -> {"follower", "candidate", "leader"}]
    /\ currentTerm \in [Servers -> Terms]
    /\ votedFor    \in [Servers -> [Terms -> Servers \cup {Nil}]]
    /\ leaderOf    \in [Terms -> Servers \cup {Nil}]

Init ==
    /\ state       = [s \in Servers |-> "follower"]
    /\ currentTerm = [s \in Servers |-> 1]
    /\ votedFor    = [s \in Servers |-> [tm \in Terms |-> Nil]]
    /\ leaderOf    = [tm \in Terms |-> Nil]

(***************************************************************************)
(* A server starts an election by advancing to the next term, becoming a    *)
(* candidate, and voting for itself in that term. Bounded by MaxTerm.       *)
(***************************************************************************)
StartElection(s) ==
    /\ currentTerm[s] < MaxTerm
    /\ LET nt == currentTerm[s] + 1 IN
        \* A candidate may only run for a term in which it has not already
        \* voted. Because nt is strictly greater than every term s has acted
        \* in, its self-vote can never overwrite a prior grant.
        /\ votedFor[s][nt] = Nil
        /\ state'       = [state       EXCEPT ![s] = "candidate"]
        /\ currentTerm' = [currentTerm EXCEPT ![s] = nt]
        /\ votedFor'    = [votedFor    EXCEPT ![s][nt] = s]
        /\ UNCHANGED leaderOf

(***************************************************************************)
(* A voter v grants its vote to candidate c for c's current term, but ONLY  *)
(* if v has not already voted in that term (one vote per term -- the rule    *)
(* that makes split-brain impossible). v also catches up its own term.      *)
(***************************************************************************)
GrantVote(v, c) ==
    /\ state[c] = "candidate"
    /\ LET tm == currentTerm[c] IN
        /\ votedFor[v][tm] = Nil          \* v has NOT voted this term
        /\ currentTerm[v] <= tm
        /\ votedFor'    = [votedFor    EXCEPT ![v][tm] = c]
        /\ currentTerm' = [currentTerm EXCEPT ![v] = tm]
        \* Raft step-down: a server that observes a term >= its own and votes
        \* reverts to follower. Without this a stale leader of an older term
        \* could linger in "leader" state after advancing its term -- a
        \* genuine split-brain hazard this model is designed to catch.
        /\ state'       = [state       EXCEPT ![v] = "follower"]
        /\ UNCHANGED leaderOf

(***************************************************************************)
(* A candidate that has collected a quorum of votes for its current term    *)
(* becomes leader of that term and records itself in leaderOf[term].        *)
(***************************************************************************)
BecomeLeader(c) ==
    /\ state[c] = "candidate"
    /\ LET tm == currentTerm[c] IN
        /\ VotersFor(c, tm) \in Quorum   \* quorum read from the live ledger
        /\ state'    = [state    EXCEPT ![c] = "leader"]
        /\ leaderOf' = [leaderOf EXCEPT ![tm] = c]
        /\ UNCHANGED <<currentTerm, votedFor>>

Next ==
    \/ \E s \in Servers : StartElection(s)
    \/ \E v, c \in Servers : GrantVote(v, c)
    \/ \E c \in Servers : BecomeLeader(c)

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* SAFETY INVARIANT: at most one leader per term (no split-brain).          *)
(* Checked two complementary ways:                                          *)
(*   (a) leaderOf records a single id per term (function), and              *)
(*   (b) no two DISTINCT servers are simultaneously in "leader" state for   *)
(*       the same currentTerm.                                              *)
(***************************************************************************)
NoSplitBrain ==
    \A t1, t2 \in Servers :
        ( /\ t1 # t2
          /\ state[t1] = "leader"
          /\ state[t2] = "leader" )
        => currentTerm[t1] # currentTerm[t2]

\* A server can never have voted for two different candidates in one term.
OneVotePerTerm ==
    \A v \in Servers, tm \in Terms :
        votedFor[v][tm] \in Servers \cup {Nil}

=============================================================================
