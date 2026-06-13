-------------------------- MODULE LeaderLease --------------------------
(***************************************************************************)
(* TLA+ model of LEADER-LEASE / leaseholder-local reads, the CockroachDB   *)
(* pattern implemented by pkg/multiraft/lease.go (LeaseTracker).            *)
(*                                                                         *)
(* WHY a lease at all (from lease.go): serving a linearizable read through  *)
(* Raft needs a round trip. Instead the current leader is granted a        *)
(* time-bounded *lease*; while the lease is valid the leaseholder answers   *)
(* reads directly from its local committed store with NO new proposal,      *)
(* because the lease guarantees no other node served a conflicting write in *)
(* that interval. When the lease expires (clock >= expiry) OR is superseded *)
(* by a higher-epoch lease on another node, the fast path MUST stop --      *)
(* otherwise a STALE node could serve a value after another node took over. *)
(*                                                                         *)
(* This is the exact correctness risk the spec verifies: a STALE READ       *)
(* across lease handoff. The guard `valid(id, now)` in lease.go combines    *)
(* TWO load-bearing checks -- holder identity (modeled here as the CURRENT  *)
(* lease holder / epoch) and clock < expiry. Dropping either lets a stale   *)
(* read through; the mutation model (LeaderLeaseMut.tla) drops the guard    *)
(* and TLC produces the stale-read counterexample.                          *)
(*                                                                         *)
(* MODEL                                                                    *)
(*   * Nodes: a small finite set (2 in the cfg).                            *)
(*   * clock: a bounded monotone logical clock 0..MaxClock.                 *)
(*   * lease: <<leaseHolder, leaseEpoch, leaseExpiry>> -- at most one lease  *)
(*     exists at a time. A new lease ALWAYS takes a strictly-higher epoch    *)
(*     (a handoff). The leaseholder is the ONLY node that may commit a write *)
(*     (CockroachDB: writes flow through the leaseholder), and on acquiring  *)
(*     a lease a node learns the latest committed value (read-index at lease *)
(*     start). committed is a monotone counter of committed writes.         *)
(*   * Each node has a local snapshot `seen[n]` = the committed value it     *)
(*     would return from a LOCAL read. The acquiring holder sets            *)
(*     seen[n]:=committed; a committing holder advances its own snapshot.    *)
(*     An OLD holder's snapshot is FROZEN at the value it last saw -> the    *)
(*     stale value a wrong guard would serve.                               *)
(*                                                                         *)
(* A node may serve a LOCAL READ only via ServeLocalRead, whose guard is     *)
(* LeaseValid(n) = (n is the current lease holder) /\ (clock < expiry). The  *)
(* served value seen[n] is recorded in lastRead, AND committedAtRead records *)
(* the authoritative committed count at the instant of service. NoStaleRead  *)
(* then asserts lastRead = committedAtRead: the read returned EXACTLY the    *)
(* latest committed write -- never an older value.                          *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Nodes,        \* finite set of node ids (model values)
          MaxClock,     \* highest logical clock tick (bounds state space)
          MaxEpoch,     \* highest lease epoch / handoff count (bounds space)
          MaxWrites,    \* highest committed-write count (bounds space)
          LeaseLen,     \* lease length added to clock at acquire time
          Nil           \* sentinel: "no leaseholder" / "no read yet"

ASSUME Nil \notin Nodes
ASSUME LeaseLen \in Nat /\ LeaseLen > 0

VARIABLES
    clock,           \* current logical time, monotone in 0..MaxClock
    leaseHolder,     \* node currently holding the lease, or Nil
    leaseEpoch,      \* epoch of current lease (strictly increases per handoff)
    leaseExpiry,     \* logical time at which the current lease stops being valid
    committed,       \* count of committed writes (authoritative latest value)
    seen,            \* seen[n]: committed value node n returns from a local read
    lastRead,        \* value returned by the most recent served LOCAL read / Nil
    committedAtRead  \* committed count at the instant lastRead was served / Nil

vars == <<clock, leaseHolder, leaseEpoch, leaseExpiry,
          committed, seen, lastRead, committedAtRead>>

Epochs == 0..MaxEpoch
Writes == 0..MaxWrites
Times  == 0..MaxClock

(***************************************************************************)
(* LeaseValid(n): may node n serve a LOCAL fast-path read right now? Mirrors *)
(* lease.go `lease.valid(id, now)`: n must be the CURRENT lease holder AND   *)
(* the lease must not have expired (clock < leaseExpiry). BOTH conjuncts are *)
(* load-bearing -- the mutation drops the expiry/currency guard.            *)
(***************************************************************************)
LeaseValid(n) ==
    /\ leaseHolder = n
    /\ clock < leaseExpiry

TypeOK ==
    /\ clock          \in Times
    /\ leaseHolder    \in Nodes \cup {Nil}
    /\ leaseEpoch     \in Epochs
    /\ leaseExpiry    \in 0..(MaxClock + LeaseLen)
    /\ committed      \in Writes
    /\ seen           \in [Nodes -> Writes]
    /\ lastRead       \in Writes \cup {Nil}
    /\ committedAtRead \in Writes \cup {Nil}

Init ==
    /\ clock           = 0
    /\ leaseHolder     = Nil
    /\ leaseEpoch      = 0
    /\ leaseExpiry     = 0
    /\ committed       = 0
    /\ seen            = [n \in Nodes |-> 0]
    /\ lastRead        = Nil
    /\ committedAtRead = Nil

(***************************************************************************)
(* AcquireLease(n): node n becomes the leaseholder with a strictly-higher   *)
(* epoch, expiring LeaseLen ticks from now. Models BOTH the initial grant   *)
(* and a HANDOFF to a new node. On acquiring, n learns the latest committed *)
(* value (read-index at lease start): seen[n] := committed. Other nodes'    *)
(* snapshots are NOT updated -- an old holder keeps its (now stale) snapshot *)
(* The strictly-higher epoch SUPERSEDES the old lease immediately, so the   *)
(* old holder's LeaseValid becomes false even before its old wall-clock     *)
(* expiry (n is no longer leaseHolder).                                     *)
(***************************************************************************)
AcquireLease(n) ==
    /\ leaseEpoch < MaxEpoch
    /\ leaseHolder' = n
    /\ leaseEpoch'  = leaseEpoch + 1
    /\ leaseExpiry' = clock + LeaseLen
    /\ seen'        = [seen EXCEPT ![n] = committed]
    /\ UNCHANGED <<clock, committed, lastRead, committedAtRead>>

(***************************************************************************)
(* CommitWrite: the CURRENT, VALID leaseholder commits a new write. Writes  *)
(* flow only through the leaseholder (CockroachDB invariant), so committing *)
(* requires a live valid lease. committed increments; the committing holder *)
(* advances with it (up to date by construction). This action is what makes *)
(* a previous holder's frozen snapshot go STALE.                           *)
(***************************************************************************)
CommitWrite ==
    /\ committed < MaxWrites
    /\ leaseHolder # Nil
    /\ LeaseValid(leaseHolder)
    /\ committed' = committed + 1
    /\ seen'      = [seen EXCEPT ![leaseHolder] = committed + 1]
    /\ UNCHANGED <<clock, leaseHolder, leaseEpoch, leaseExpiry,
                   lastRead, committedAtRead>>

(***************************************************************************)
(* ServeLocalRead(n): node n serves a LEASEHOLDER-LOCAL read -- the fast     *)
(* path with NO quorum. The GUARD is the whole point: n may serve ONLY if    *)
(* LeaseValid(n). It records the value returned (seen[n]) AND the committed  *)
(* count that was authoritative at this instant (committedAtRead), so the    *)
(* staleness check is exact.                                                *)
(***************************************************************************)
ServeLocalRead(n) ==
    /\ LeaseValid(n)
    /\ lastRead'        = seen[n]
    /\ committedAtRead' = committed
    /\ UNCHANGED <<clock, leaseHolder, leaseEpoch, leaseExpiry,
                   committed, seen>>

(***************************************************************************)
(* Tick: advance the logical clock by one. This is what eventually expires  *)
(* a lease (clock reaches leaseExpiry) and so closes the fast-path window.   *)
(***************************************************************************)
Tick ==
    /\ clock < MaxClock
    /\ clock' = clock + 1
    /\ UNCHANGED <<leaseHolder, leaseEpoch, leaseExpiry,
                   committed, seen, lastRead, committedAtRead>>

Next ==
    \/ \E n \in Nodes : AcquireLease(n)
    \/ CommitWrite
    \/ \E n \in Nodes : ServeLocalRead(n)
    \/ Tick

Spec == Init /\ [][Next]_vars

\* Symmetry set for TLC: node ids are interchangeable (see .cfg justification).
\* Permutations(Nodes) is provided by the TLC standard module.
Symm == Permutations(Nodes)

(***************************************************************************)
(* SAFETY 1 -- NoStaleRead (linearizability of leaseholder reads).          *)
(*                                                                         *)
(* A local read served by a leaseholder NEVER returns a value older than    *)
(* the latest committed write that had completed when the read was served.  *)
(* We record at service time BOTH the value returned (lastRead = seen[n])   *)
(* and the authoritative committed count then (committedAtRead). The read    *)
(* is non-stale iff it returned exactly the latest committed value:          *)
(*                                                                         *)
(*     lastRead = committedAtRead.                                          *)
(*                                                                         *)
(* This is MEANINGFUL, not vacuous: lease HANDOFF (AcquireLease bumps the    *)
(* epoch and freezes the previous holder's snapshot) followed by CommitWrite *)
(* under the new holder drives an old holder's seen[] strictly below         *)
(* committed. Only the LeaseValid guard in ServeLocalRead prevents that old  *)
(* holder from serving its stale snapshot. The mutation (LeaderLeaseMut)     *)
(* drops that guard and TLC finds lastRead < committedAtRead -- the stale    *)
(* read. We also keep the structural fact that the freshly-fresh leaseholder *)
(* is up to date (LeaseholderFresh) as the load-bearing lemma.              *)
(***************************************************************************)
LeaseholderFresh ==
    \A n \in Nodes : LeaseValid(n) => seen[n] = committed

NoStaleRead ==
    /\ LeaseholderFresh
    /\ (lastRead # Nil) => (lastRead = committedAtRead)

(***************************************************************************)
(* SAFETY 2 -- SingleActiveLease. At most one node has a valid (serveable)  *)
(* lease at any logical instant; no two leaseholders serve reads at once.   *)
(* There is a single lease record, so this holds structurally, but we       *)
(* assert it over the SERVEABILITY predicate so a wrong handoff that left    *)
(* two nodes serveable would be caught.                                     *)
(***************************************************************************)
SingleActiveLease ==
    \A a, b \in Nodes :
        (LeaseValid(a) /\ LeaseValid(b)) => a = b

-------------------------------------------------------------------------
(* Non-vacuity witnesses: negate to make TLC print a reachable state, used  *)
(* to PROVE the interesting states are actually reached (see README).       *)

\* A valid local read actually happened (lastRead set under a valid lease).
ReachServedRead == lastRead = Nil

\* A lease HANDOFF actually happened (epoch advanced past the first lease).
ReachHandoff == leaseEpoch < 2

\* A write committed AFTER a handoff: some node's frozen snapshot is strictly
\* behind committed -- the precise stale-read setup the guard must defend.
ReachStaleSetup == \A n \in Nodes : seen[n] >= committed
=============================================================================
