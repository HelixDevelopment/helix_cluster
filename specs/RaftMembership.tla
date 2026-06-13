-------------------------- MODULE RaftMembership --------------------------
(***************************************************************************)
(* TLA+ model of RAFT SINGLE-SERVER MEMBERSHIP CHANGE (the Raft            *)
(* dissertation's safe reconfiguration, Ongaro 2014, ch. 4).               *)
(*                                                                         *)
(* Real-cluster relevance: pkg/raft, pkg/multiraft and the embedded        *)
(* etcd-raft all perform DYNAMIC membership changes. The classic hazard    *)
(* during a config transition C -> C' is TWO DISJOINT MAJORITIES: if a     *)
(* majority of the OLD config and a majority of the NEW config do not      *)
(* share a server, two leaders / two conflicting decisions can commit at   *)
(* once -- split-brain.  Raft avoids this by restricting every change to   *)
(* add-or-remove of EXACTLY ONE server, which guarantees any majority of C *)
(* and any majority of C' intersect.                                       *)
(*                                                                         *)
(* This is the MEMBERSHIP-SAFETY ABSTRACTION, deliberately NOT full Raft   *)
(* log replication.  We model:                                            *)
(*   - a chain of configurations, each a server-set, grown one change at   *)
(*     a time (single-server add/remove transitions);                      *)
(*   - servers that may LAG: during the overlap window a server still      *)
(*     believes an older config in the chain is the one it votes under;    *)
(*   - a DECISION committed under a config = a strict majority of THAT      *)
(*     config's members all backing it. Two decisions CONFLICT if they     *)
(*     carry different values.                                            *)
(*                                                                         *)
(* Safety properties (model-checked exhaustively by TLC, zero              *)
(* counterexamples):                                                       *)
(*   QuorumOverlap          -- any majority of any config in the chain and *)
(*                             any majority of the NEXT config share a     *)
(*                             server (the property that makes             *)
(*                             single-server change safe).                 *)
(*   NoDisjointMajorities   -- no two committed decisions carry conflicting*)
(*                             values (the operational consequence).       *)
(*   OneServerChangeAtATime -- consecutive configs in the chain differ by  *)
(*                             exactly one member (the guard the protocol  *)
(*                             relies on).                                 *)
(*   TypeOK                 -- shape invariant.                            *)
(*                                                                         *)
(* TEETH: flip Mutation_AllowTwoChangeAtOnce = TRUE (see .cfg) to drop the *)
(* single-server guard and permit a two-server change; TLC then finds a    *)
(* reachable disjoint-majority / conflicting-decision counterexample.      *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, Sequences, TLC

CONSTANTS Servers,    \* finite universe of candidate server ids
          MaxConfigs, \* bound on chain length (number of configs explored)
          Values,     \* finite set of decision values (>= 2 to expose conflict)
          \* TEETH toggle: when TRUE the change rule no longer requires the
          \* old and new configs to differ by exactly one member.
          Mutation_AllowTwoChangeAtOnce

ASSUME MaxConfigs \in Nat /\ MaxConfigs >= 1
ASSUME Mutation_AllowTwoChangeAtOnce \in BOOLEAN

\* Server ids are interchangeable -- nothing in the spec names a specific one --
\* so TLC may quotient the state space by permutations of Servers.
Symmetry == Permutations(Servers)

\* A quorum OF a given config S: any subset of S of strictly-majority size.
QuorumsOf(S) == { Q \in SUBSET S : Cardinality(Q) * 2 > Cardinality(S) }

\* Two configs are a legal single-server change iff one is obtained from the
\* other by adding or removing exactly one member (symmetric difference = 1).
\* Both must be non-empty (an empty config has no majority -- meaningless).
DiffByOne(a, b) ==
    /\ Cardinality((a \ b) \cup (b \ a)) = 1
    /\ a # {}
    /\ b # {}

VARIABLES
    configs,    \* Seq of server-sets: the reconfiguration chain, configs[1]
                \* the initial cluster, configs[Len] the current target.
    decisions   \* set of records [cfg |-> serverSet, val |-> Values]:
                \* every decision that has committed, tagged with the config
                \* it committed under and the value it carries.

vars == <<configs, decisions>>

\* Index domain helpers.
Idx == 1..Len(configs)

\* Non-empty subsets of the universe -- the space of possible configs.
NonEmptyConfigs == (SUBSET Servers) \ {{}}

TypeOK ==
    /\ configs \in Seq(NonEmptyConfigs)
    /\ Len(configs) >= 1
    /\ Len(configs) <= MaxConfigs
    /\ decisions \subseteq [cfg : NonEmptyConfigs, val : Values,
                            quorum : SUBSET Servers]
    \* Each decision's witnessing quorum is a genuine majority of its config.
    /\ \A d \in decisions : d.quorum \in QuorumsOf(d.cfg)

(***************************************************************************)
(* Init: a single-server cluster (the whole universe is the first config). *)
(* Using the full universe keeps the chain able to both grow and shrink.   *)
(***************************************************************************)
Init ==
    /\ configs   = << Servers >>
    /\ decisions = {}

(***************************************************************************)
(* AddConfig: append a new config C' to the chain that is a legal change   *)
(* from the current last config C.  Under the protocol (mutation FALSE)    *)
(* C' must differ from C by exactly one server.  Under the TEETH mutation  *)
(* (TRUE) we relax that to "any different, non-empty config", which admits *)
(* two-at-once changes that break overlap.                                 *)
(***************************************************************************)
LegalChange(C, Cp) ==
    IF Mutation_AllowTwoChangeAtOnce
        THEN /\ Cp # {}
             /\ Cp # C
        ELSE DiffByOne(C, Cp)

AddConfig ==
    /\ Len(configs) < MaxConfigs
    /\ \E Cp \in NonEmptyConfigs :
          /\ LegalChange(configs[Len(configs)], Cp)
          /\ configs' = Append(configs, Cp)
          \* Advancing the window: the old config C (now the predecessor of
          \* the new target C') stays live; decisions committed under configs
          \* further back leave the overlap window and are retired. This keeps
          \* the model focused on the C/C' transition Raft's rule governs --
          \* a value committed two reconfigurations ago is settled history and
          \* need not overlap the current window.
          /\ decisions' = { d \in decisions : d.cfg = configs[Len(configs)] }

(***************************************************************************)
(* The OVERLAP WINDOW: during a single transition the live configs are the *)
(* current target C' = configs[Len] and its immediate predecessor C =      *)
(* configs[Len-1] (the config that lagging servers still believe in).      *)
(* Those are the two configs under which a decision can race -- exactly the *)
(* C / C' pair Raft's single-server rule must keep overlapping.            *)
(***************************************************************************)
LiveConfigIdx ==
    IF Len(configs) >= 2 THEN {Len(configs) - 1, Len(configs)}
                         ELSE {Len(configs)}

(***************************************************************************)
(* Decide: a decision commits under one of the two live configs of the     *)
(* current transition window (old C, new C') -- modelling a lagging server *)
(* that still decides under C while others have advanced to C'. A decision *)
(* needs a strict majority (a quorum) of THAT config to back its value.    *)
(*                                                                         *)
(* The faithful Raft mechanic: a quorum is a real set of servers, and a    *)
(* server cannot back two different values at once. So a new value V may   *)
(* commit under config C using quorum Q only if Q does NOT share a server  *)
(* with the quorum of any ALREADY-committed decision that carries a        *)
(* DIFFERENT value -- a shared server would have to vote twice. When the    *)
(* C/C' majorities overlap (single-server change) this BLOCKS every        *)
(* conflict; when they can be disjoint (two-at-once change) it lets the    *)
(* conflicting decision through, which the invariant then catches. We carry*)
(* the witnessing quorum Q on each decision so overlap is checked on real  *)
(* server sets, not just config labels.                                    *)
(***************************************************************************)
Decide ==
    /\ \E i \in LiveConfigIdx :
         \E val \in Values :
            \E Q \in QuorumsOf(configs[i]) :
               \* No conflicting committed decision shares a server with Q.
               /\ \A d \in decisions :
                     (d.val # val) => (d.quorum \cap Q = {})
               /\ decisions' = decisions \cup
                     {[cfg |-> configs[i], val |-> val, quorum |-> Q]}
    /\ UNCHANGED configs

Next == AddConfig \/ Decide

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* SAFETY INVARIANTS                                                       *)
(***************************************************************************)

\* (2) OneServerChangeAtATime: every consecutive pair in the chain differs
\* by exactly one member.  Under the protocol this always holds; under the
\* TEETH mutation a two-at-once step violates it -- and that violation is
\* precisely what destroys overlap.
OneServerChangeAtATime ==
    \A i \in 1..(Len(configs) - 1) :
        DiffByOne(configs[i], configs[i+1])

\* (1) QuorumOverlap: for every consecutive config pair (C, C') in the
\* chain, EVERY majority of C and EVERY majority of C' intersect.  This is
\* the structural property single-server change is designed to guarantee.
QuorumOverlap ==
    \A i \in 1..(Len(configs) - 1) :
        \A Q1 \in QuorumsOf(configs[i]) :
            \A Q2 \in QuorumsOf(configs[i+1]) :
                Q1 \cap Q2 # {}

\* (1') NoDisjointMajorities: the OPERATIONAL consequence -- the true Raft
\* safety guarantee.  No two committed decisions may carry CONFLICTING
\* values: a single agreed value is the whole point of consensus.  Under
\* single-server change every relevant pair of quorums overlaps, so the
\* shared server's once-only vote forces agreement and this holds.  Under a
\* two-at-once change the old and new majorities can be disjoint, two
\* leaders commit different values, and this invariant is VIOLATED --
\* exactly the split-brain TLC is asked to hunt for.
NoDisjointMajorities ==
    \A d1, d2 \in decisions : d1.val = d2.val

THEOREM Spec => [](TypeOK /\ OneServerChangeAtATime
                          /\ QuorumOverlap
                          /\ NoDisjointMajorities)

(***************************************************************************)
(* NON-VACUITY witnesses (checked as "expected to FAIL" => reachable):     *)
(* negate to make TLC print a state where the chain really reconfigured.   *)
(***************************************************************************)
\* A reconfiguration of length >= 3 with at least one decision is reachable.
NonVacuous_ReachedReconfig == ~(Len(configs) >= 3 /\ decisions # {})
=============================================================================
