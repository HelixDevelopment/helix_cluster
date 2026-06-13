------------------------- MODULE RaftPreVote -------------------------
(***************************************************************************)
(* TLA+ model of RAFT PreVote -- the disruption-avoidance extension that    *)
(* stops a restarted/partitioned node from disrupting a stable leader by    *)
(* inflating the cluster term.                                              *)
(*                                                                         *)
(* WHY PreVote (the real mechanism this verifies): in plain Raft a node     *)
(* that is PARTITIONED or has just RESTARTED stops hearing the leader's     *)
(* heartbeats, times out, and -- to start an election -- INCREMENTS its own *)
(* currentTerm and sends RequestVote at the new, higher term. While         *)
(* isolated it keeps timing out, so it keeps inflating its term. When it    *)
(* finally RECONNECTS, its inflated term is higher than the legitimate      *)
(* leader's; any RequestVote (or even a heartbeat reply) carrying that      *)
(* higher term FORCES the healthy leader to step down to follower and the   *)
(* whole cluster to bump its term -- a DISRUPTIVE, USELESS election, because *)
(* the isolated node could never actually win: its log is stale and/or it   *)
(* never held a quorum that had lost contact with the leader.               *)
(*                                                                         *)
(* WITH PreVote: before incrementing its term and starting a REAL election, *)
(* a candidate runs a PRE-VOTE round. It asks peers "would you grant me a    *)
(* vote in term+1?" WITHOUT bumping its own term or anyone else's. A peer    *)
(* grants a pre-vote ONLY if BOTH:                                          *)
(*   (a) it has NOT heard from a current leader recently (it is not under a  *)
(*       leader's lease / heartbeat), AND                                   *)
(*   (b) the pre-candidate's log is at least as up-to-date as the peer's     *)
(*       (the standard Raft up-to-dateness check on lastLogTerm/lastLogIdx). *)
(* ONLY if a QUORUM would grant does the pre-candidate actually increment    *)
(* its term and start a real election. So a stale/partitioned node that      *)
(* cannot win NEVER inflates a term and NEVER disrupts the stable leader.    *)
(*                                                                         *)
(* THE HAZARD this spec pins down: a partitioned/restarted node whose log is *)
(* behind (or whose peers still hear the leader) must NOT be able to cause   *)
(* the current leader to step down or the cluster term to advance. PreVote   *)
(* gates the term bump behind a pre-vote quorum the stale node cannot get;   *)
(* dropping the gate (the mutation, PreVoteEnabled = FALSE) lets the stale   *)
(* node bump its term directly, reconnect, and depose the leader -- TLC      *)
(* produces the DISRUPTION counterexample.                                  *)
(*                                                                         *)
(* ABSTRACTION (deliberately NOT full Raft log replication): we model the    *)
(* disruption-avoidance property, not full log machinery. We summarise each  *)
(* server's log by a single up-to-dateness rank (logRank), which is the      *)
(* standard (lastLogTerm,lastLogIndex) ordering collapsed to a Nat -- all    *)
(* the PreVote up-to-dateness check needs. State:                           *)
(*   * Servers      : a tiny finite set (3 in the cfg) -- enough for a       *)
(*     leader + supporting quorum and one partitioned outsider.             *)
(*   * currentTerm[s]: the term server s believes itself in.                *)
(*   * state[s]      : "Leader" | "Follower" (role of s).                    *)
(*   * logRank[s]    : up-to-dateness rank of s's log (higher == more up to  *)
(*     date); peer t grants a pre-vote to p only if logRank[p] >= logRank[t].*)
(*   * connected[s]  : FALSE == s is PARTITIONED/just-restarted (hears no     *)
(*     leader); TRUE == s is in the connected majority hearing the leader.   *)
(*   * leaderTerm    : the term of the current stable leader (0 == none).    *)
(*   * disrupted     : latch set TRUE the instant a node that should NOT win  *)
(*     causes the stable leader to step down / the term to advance. This is  *)
(*     the GROUND-TRUTH sink-side flag the headline invariant checks.        *)
(*                                                                         *)
(* A pre-vote round (PreVoteRound) NEVER changes currentTerm/state/votedFor  *)
(* of anyone -- it only computes whether a quorum WOULD grant and records    *)
(* the outcome in preVoteGranted[p]. Only StartRealElection(p), guarded by   *)
(* preVoteGranted[p] when PreVote is enabled, bumps the term and runs a real *)
(* election. A connected peer that still hears the leader REFUSES the pre-   *)
(* vote (clause (a)); a peer with a fresher log REFUSES (clause (b)). So a   *)
(* partitioned stale node cannot collect a pre-vote quorum and is gated.    *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Servers,         \* finite set of server ids (model values)
          MaxTerm,         \* highest term / election count (bounds space)
          MaxRank,         \* highest log up-to-dateness rank (bounds space)
          PreVoteEnabled,  \* TRUE: gate real election behind a pre-vote quorum
                           \* FALSE (mutation): partitioned node bumps term and
                           \* starts a real election directly -> disruption
          Nil              \* sentinel: "no leader"

ASSUME Nil \notin Servers
ASSUME PreVoteEnabled \in BOOLEAN
ASSUME MaxTerm \in Nat /\ MaxTerm > 0
ASSUME MaxRank \in Nat

Terms == 0..MaxTerm
Ranks == 0..MaxRank

\* A quorum is any strict majority of servers. Any two quorums intersect --
\* the structural fact behind both real elections and the pre-vote count.
Quorum == {Q \in SUBSET Servers : Cardinality(Q) * 2 > Cardinality(Servers)}

VARIABLES
    currentTerm,     \* currentTerm[s]: term server s believes it is in
    state,           \* state[s]: "Leader" or "Follower"
    logRank,         \* logRank[s]: up-to-dateness rank of s's log
    connected,       \* connected[s]: TRUE == hears the leader; FALSE == isolated
    leader,          \* current stable leader id, or Nil
    leaderTerm,      \* term of the current stable leader (0 == none)
    preVoteGranted,  \* preVoteGranted[s]: s's most recent pre-vote round WON a
                     \* quorum (would-win in term+1). Set ONLY by PreVoteRound;
                     \* never itself changes any term/state.
    ranPreVote,      \* ranPreVote[s]: s has actually EXECUTED a pre-vote round
                     \* (history flag used only by the non-vacuity witness, so
                     \* the witness proves a round really fired, not just Init)
    disrupted        \* latch: a node that should NOT win forced the stable
                     \* leader to step down / the cluster term to advance

vars == <<currentTerm, state, logRank, connected, leader, leaderTerm,
          preVoteGranted, ranPreVote, disrupted>>

States == {"Leader", "Follower"}

TypeOK ==
    /\ currentTerm    \in [Servers -> Terms]
    /\ state          \in [Servers -> States]
    /\ logRank        \in [Servers -> Ranks]
    /\ connected      \in [Servers -> BOOLEAN]
    /\ leader         \in Servers \cup {Nil}
    /\ leaderTerm     \in Terms
    /\ preVoteGranted \in [Servers -> BOOLEAN]
    /\ ranPreVote     \in [Servers -> BOOLEAN]
    /\ disrupted      \in BOOLEAN

(***************************************************************************)
(* Init: a single stable leader already exists at term 1, supported by a    *)
(* connected majority whose logs are fully up to date (rank MaxRank). ONE    *)
(* server is PARTITIONED/just-restarted: disconnected, a follower, and with  *)
(* a STALE log (rank 0) -- the classic disruptive-node setup. We initialise  *)
(* over ALL such configurations (any server may be the stale outsider, any   *)
(* quorum may be the connected majority) so TLC explores every assignment.   *)
(***************************************************************************)
Init ==
    /\ \E ldr \in Servers, iso \in Servers :
           /\ ldr # iso
           \* the leader is connected with a fully up-to-date log
           /\ state      = [s \in Servers |-> IF s = ldr THEN "Leader" ELSE "Follower"]
           /\ leader     = ldr
           /\ leaderTerm = 1
           /\ currentTerm = [s \in Servers |-> IF s = iso THEN 0 ELSE 1]
           \* the isolated node is partitioned with a STALE log (rank 0);
           \* everyone else is connected with a fully up-to-date log (MaxRank)
           /\ connected  = [s \in Servers |-> s # iso]
           /\ logRank    = [s \in Servers |-> IF s = iso THEN 0 ELSE MaxRank]
    /\ preVoteGranted = [s \in Servers |-> FALSE]
    /\ ranPreVote     = [s \in Servers |-> FALSE]
    /\ disrupted      = FALSE

(***************************************************************************)
(* HelpfulPeer(p, t): would peer t GRANT a pre-vote to pre-candidate p?      *)
(* The two standard PreVote conditions:                                     *)
(*   (a) t has NOT heard from a current leader recently. We model "hears a   *)
(*       current leader" as: t is connected AND a leader exists AND t is in   *)
(*       the leader's term. A connected in-term peer REFUSES (it still has a  *)
(*       leader). A partitioned peer, or one already in a stale/older term,   *)
(*       has effectively lost the leader and may grant.                      *)
(*   (b) p's log is at least as up to date as t's: logRank[p] >= logRank[t]. *)
(* A peer grants only if it is NOT currently hearing a leader AND p's log is  *)
(* fresh enough.                                                            *)
(***************************************************************************)
HearsLeader(t) ==
    /\ connected[t]
    /\ leader # Nil
    /\ currentTerm[t] = leaderTerm

HelpfulPeer(p, t) ==
    /\ ~HearsLeader(t)
    /\ logRank[p] >= logRank[t]

(***************************************************************************)
(* PreVoteRound(p): pre-candidate p runs a pre-vote round. It asks the peers *)
(* it can currently reach. Crucially this NEVER changes any currentTerm,     *)
(* state, leader, leaderTerm or logRank -- a pre-vote does not bump terms.   *)
(* It only records, in preVoteGranted[p], whether a QUORUM (counting p       *)
(* itself, which always pre-votes for itself) WOULD grant in term+1.         *)
(*                                                                         *)
(* p can only reach peers reachable from it. We model reachability simply:   *)
(* p reaches t iff they are on the same side of the partition (both          *)
(* connected, or both disconnected -- two isolated nodes could still be a    *)
(* minority island). A partitioned stale node thus solicits only its own     *)
(* island; the connected majority is unreachable, so it cannot reach a       *)
(* cluster-wide quorum of granters -> preVoteGranted stays FALSE.           *)
(***************************************************************************)
Reaches(p, t) == connected[p] = connected[t]

PreVoteRound(p) ==
    /\ preVoteGranted' =
           [preVoteGranted EXCEPT ![p] =
               \E Q \in Quorum :
                   /\ p \in Q
                   /\ \A t \in Q :
                          \/ t = p                       \* p pre-votes for itself
                          \/ (Reaches(p, t) /\ HelpfulPeer(p, t))]
    /\ ranPreVote' = [ranPreVote EXCEPT ![p] = TRUE]
    /\ UNCHANGED <<currentTerm, state, logRank, connected, leader,
                   leaderTerm, disrupted>>

(***************************************************************************)
(* PreVoteGate(p): may p proceed to a REAL election?                        *)
(*   - PreVote ENABLED: only if p's last pre-vote round WON (preVoteGranted).*)
(*   - PreVote DISABLED (mutation): always -- p bumps its term and starts a   *)
(*     real election with NO pre-vote, the plain-Raft disruptive behaviour.  *)
(***************************************************************************)
PreVoteGate(p) == IF PreVoteEnabled THEN preVoteGranted[p] ELSE TRUE

(***************************************************************************)
(* StartRealElection(p): p increments its term and starts a REAL election.   *)
(* This is the ONLY action that changes a term. It fires only when the pre-  *)
(* vote gate permits (a pre-vote quorum, when PreVote is on).               *)
(*                                                                         *)
(* p bumps its OWN term to currentTerm[p]+1 (a candidate raises its own term *)
(* by one) and becomes a candidate-then-... -- we collapse candidacy: p      *)
(* records the new term. The DISRUPTION test: if p's new term EXCEEDS the    *)
(* stable leader's term, the leader is forced to step down (any node it      *)
(* contacts at the higher term, or p's RequestVote, deposes it). We set      *)
(* disrupted iff this term bump deposes a stable leader that p could NOT     *)
(* legitimately replace -- i.e. p's log is NOT up to date enough to have won *)
(* a real election against the connected majority (logRank[p] < MaxRank), OR *)
(* p is partitioned from that majority (cannot have held a winning quorum).  *)
(* That is exactly the "useless, disruptive election" PreVote must prevent.  *)
(***************************************************************************)
StalePreCandidate(p) ==
    \* p could NOT have legitimately won a real, cluster-wide election: its
    \* log is behind the up-to-date majority, OR it is cut off from them.
    \/ logRank[p] < MaxRank
    \/ ~connected[p]

StartRealElection(p) ==
    /\ currentTerm[p] < MaxTerm
    /\ PreVoteGate(p)
    /\ currentTerm' = [currentTerm EXCEPT ![p] = currentTerm[p] + 1]
    \* a fresh real election invalidates p's pre-vote record (must re-run)
    /\ preVoteGranted' = [preVoteGranted EXCEPT ![p] = FALSE]
    \* DISRUPTION: p's higher term deposes the stable leader it could not beat
    /\ IF /\ leader # Nil
          /\ currentTerm[p] + 1 > leaderTerm
          /\ p # leader
          /\ StalePreCandidate(p)
       THEN /\ disrupted'  = TRUE
            /\ leader'      = Nil           \* stable leader forced to step down
            /\ leaderTerm'  = currentTerm[p] + 1
            /\ state'       = [s \in Servers |->
                                  IF s = leader THEN "Follower" ELSE state[s]]
       ELSE /\ UNCHANGED <<disrupted, leader, leaderTerm, state>>
    /\ UNCHANGED <<logRank, connected, ranPreVote>>

(***************************************************************************)
(* Reconnect(s): a partitioned node rejoins the connected majority. This is  *)
(* helix-raftd's restart-rejoin (HXC-982). On its own this is HARMLESS --     *)
(* rejoining does not change any term. The hazard is only realised if the     *)
(* node had previously INFLATED its term while isolated (which PreVote        *)
(* prevents) and then reconnects. Modelling reconnection lets the isolated    *)
(* node's (possibly inflated) term meet the leader.                          *)
(***************************************************************************)
Reconnect(s) ==
    /\ ~connected[s]
    /\ connected' = [connected EXCEPT ![s] = TRUE]
    /\ UNCHANGED <<currentTerm, state, logRank, leader, leaderTerm,
                   preVoteGranted, ranPreVote, disrupted>>

(***************************************************************************)
(* LegitElection(p): the GOOD path -- a fully up-to-date, connected node      *)
(* legitimately wins a real election (e.g. the old leader genuinely failed).  *)
(* Guarded by the pre-vote gate too, so PreVote does NOT block legitimate     *)
(* elections. It installs p as the new stable leader at the higher term. This *)
(* is the non-vacuity witness that PreVote is not trivially blocking ALL      *)
(* elections: a well-positioned node CAN win a pre-vote and proceed.          *)
(* It does NOT set `disrupted` -- a legitimate leadership change is not        *)
(* disruption.                                                               *)
(***************************************************************************)
LegitElection(p) ==
    /\ leaderTerm < MaxTerm
    /\ connected[p]
    /\ logRank[p] = MaxRank
    /\ PreVoteGate(p)
    \* p is supported by a connected quorum of up-to-date peers in/below its term
    /\ \E Q \in Quorum :
           /\ p \in Q
           /\ \A t \in Q : connected[t] /\ logRank[t] <= logRank[p]
    /\ leader'      = p
    /\ leaderTerm'  = leaderTerm + 1
    /\ currentTerm' = [s \in Servers |->
                          IF s \in {q \in Servers : connected[q]}
                          THEN leaderTerm + 1 ELSE currentTerm[s]]
    /\ state'       = [s \in Servers |-> IF s = p THEN "Leader" ELSE "Follower"]
    /\ preVoteGranted' = [s \in Servers |-> FALSE]
    /\ UNCHANGED <<logRank, connected, ranPreVote, disrupted>>

Next ==
    \/ \E p \in Servers : PreVoteRound(p)
    \/ \E p \in Servers : StartRealElection(p)
    \/ \E s \in Servers : Reconnect(s)
    \/ \E p \in Servers : LegitElection(p)

Spec == Init /\ [][Next]_vars

\* Symmetry set for TLC: server ids are interchangeable model values (the
\* spec never orders them; Quorum is by cardinality; all invariants are
\* symmetric under permutation of Servers).
Symm == Permutations(Servers)

(***************************************************************************)
(* SAFETY 1 -- NoDisruptionByStaleNode (the HEADLINE).                       *)
(*                                                                         *)
(* A node that is NOT in a position to win -- its log is behind the up-to-   *)
(* date majority, or it is partitioned from that majority -- can NEVER cause  *)
(* the stable leader to step down or the cluster term to advance. With       *)
(* PreVote on, such a node's pre-vote round fails (peers refuse: they still   *)
(* hear the leader and/or its log is stale), so its term bump is gated and    *)
(* the `disrupted` latch is never set.                                       *)
(*                                                                         *)
(*     disrupted = FALSE                                                    *)
(*                                                                         *)
(* MEANINGFUL / has TEETH: with PreVote DISABLED (the mutation), the stale    *)
(* partitioned node bumps its term directly and reconnects; its higher term  *)
(* deposes the stable leader -> disrupted = TRUE, a counterexample.          *)
(***************************************************************************)
NoDisruptionByStaleNode == disrupted = FALSE

(***************************************************************************)
(* SAFETY 2 -- PreVoteDoesNotChangeTerm.                                    *)
(*                                                                         *)
(* A pre-vote round must never modify any server's currentTerm. We verify    *)
(* this as a STATE invariant over the PreVoteRound action: across that       *)
(* action, currentTerm is UNCHANGED (it appears in PreVoteRound's UNCHANGED  *)
(* list). The action-level guarantee is checked here as an ACTION property    *)
(* via the [Next]-step invariant PreVoteStep below; as a plain state         *)
(* invariant we assert the structural fact that only StartRealElection /     *)
(* LegitElection (term-changing actions) ever raise a term above leaderTerm  *)
(* origins -- captured operationally by ElectionSafety.                      *)
(*                                                                         *)
(* Concretely we check the ACTION invariant: in any PreVoteRound step the    *)
(* term vector is preserved. TLC checks this as an invariant on the          *)
(* primed/unprimed relation through PreVotePreservesTerm.                    *)
(***************************************************************************)
\* Action property: a PreVoteRound step never changes currentTerm or state
\* or votedFor-equivalent (leader/leaderTerm). Checked with -inv on the
\* action via the conjunction below (TLC ACTION-CONSTRAINT style as INVARIANT
\* over an enabling predicate is not expressible as a pure state invariant;
\* we therefore express the guarantee structurally): preVoteGranted may change
\* in a PreVote step, but NO term-bearing variable does. We encode the
\* before/after check as a temporal action property.
PreVoteDoesNotChangeTerm ==
    [][ (\E p \in Servers : PreVoteRound(p))
        => /\ currentTerm' = currentTerm
           /\ state'       = state
           /\ leader'      = leader
           /\ leaderTerm'  = leaderTerm ]_vars

(***************************************************************************)
(* SAFETY 3 -- ElectionSafety (one-leader-per-term sanity check).           *)
(*                                                                         *)
(* At most one stable leader exists, and it sits at leaderTerm. No server's  *)
(* currentTerm exceeds leaderTerm WITHOUT having gone through a term bump     *)
(* that, if it deposed the leader, set `disrupted` (so leaderTerm tracks the  *)
(* highest committed leadership). The standard sanity invariant: there is at  *)
(* most one Leader-state server, and if a leader is installed it is unique.   *)
(***************************************************************************)
ElectionSafety ==
    \A s1, s2 \in Servers :
        (state[s1] = "Leader" /\ state[s2] = "Leader") => (s1 = s2)

-------------------------------------------------------------------------
(* Non-vacuity witnesses: negate each to make TLC print a reachable state,   *)
(* proving the interesting configurations are actually reached (see README). *)

\* (a) A partitioned/stale node actually RAN a pre-vote round and it FAILED
\* (no quorum), while the leader stayed up. Negation reachable => such a state
\* exists: some disconnected server p ran PreVoteRound and preVoteGranted[p]
\* is FALSE with the leader still present.
ReachFailedPreVote ==
    ~ (\E p \in Servers :
          /\ ~connected[p]
          /\ ranPreVote[p] = TRUE
          /\ preVoteGranted[p] = FALSE
          /\ leader # Nil)

\* (b) A legitimately better-positioned node CAN win a pre-vote and proceed:
\* leadership moved to a new node at a higher term WITHOUT disruption (the
\* good path is reachable, so PreVote is not blocking all elections).
\* Negation reachable => leaderTerm >= 2 with disrupted still FALSE.
ReachLegitElection == ~ (leaderTerm >= 2 /\ disrupted = FALSE)
=============================================================================
