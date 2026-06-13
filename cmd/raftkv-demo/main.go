package main

import (
	"fmt"
	"os"
	"sort"
	"time"
)

// main runs a scripted demonstration of the replicated KV service, printing what
// happens at each step so the run doubles as human-readable evidence:
//
//  1. bring up a 3-node Raft cluster and report the elected leader
//  2. Put several keys via the leader (replicated through the Raft log)
//  3. Get each key from ALL three nodes' FSMs (proves replication)
//  4. attempt a Put directly on a follower and show it is rejected (ErrNotLeader)
//  5. KILL the leader, show a NEW leader is elected
//  6. Get the pre-kill keys from the survivors (still present — committed)
//  7. Put a fresh key via the new leader and show it replicates to survivors
//
// Every Get reads a replica's own replicated FSM, and every Put goes through real
// Raft consensus — so the printed output reflects genuine end-user behaviour, not
// a local map (CLAUDE-1).
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "demo failed:", err)
		os.Exit(1)
	}
}

func run() error {
	const (
		electTimeout  = 5 * time.Second
		replTimeout   = 5 * time.Second
		failoverElect = 5 * time.Second
	)
	ids := []string{"node-A", "node-B", "node-C"}

	fmt.Println("=== raftkv-demo: a real 3-node replicated key/value store on embedded Raft ===")
	fmt.Printf("[1] bringing up %d-node Raft cluster: %v\n", len(ids), ids)

	svc, err := NewKVService(electTimeout, ids...)
	if err != nil {
		return err
	}
	defer func() { _ = svc.Close() }()

	leader := svc.LeaderID()
	fmt.Printf("    leader elected: %s\n", leader)

	// [2] Put several keys via the leader (leader-routed, replicated).
	puts := []struct{ k, v string }{
		{"service/db/host", "10.0.0.7"},
		{"service/db/port", "5432"},
		{"feature/raftkv", "enabled"},
	}
	fmt.Printf("[2] Put %d keys via the leader (each replicated through the Raft log):\n", len(puts))
	for _, p := range puts {
		if err := svc.Put(p.k, p.v); err != nil {
			return err
		}
		fmt.Printf("    PUT  %-20s = %s   (committed by majority)\n", p.k, p.v)
	}

	// [3] Get each key from ALL nodes' FSMs — proves replication.
	fmt.Println("[3] Get each key from ALL 3 nodes' replicated FSMs (replication proof):")
	for _, p := range puts {
		if err := svc.WaitReplicated(p.k, p.v, ids, replTimeout); err != nil {
			return err
		}
		fmt.Printf("    %-20s ->", p.k)
		for _, id := range ids {
			v, ok, err := svc.Get(id, p.k)
			if err != nil {
				return err
			}
			fmt.Printf("  %s=%q(ok=%v)", id, v, ok)
		}
		fmt.Println()
	}

	// [4] Attempt a Put directly on a follower — must be rejected.
	follower := firstFollower(svc, leader)
	fmt.Printf("[4] Put directly on FOLLOWER %s (no leader routing) — expect rejection:\n", follower)
	err = svc.PutVia(follower, "should/not/exist", "nope")
	if err != nil {
		fmt.Printf("    rejected as expected: %v\n", err)
	} else {
		return fmt.Errorf("expected follower Put to be rejected, but it succeeded")
	}

	// [5] Kill the leader; a new one must be elected.
	fmt.Printf("[5] KILL leader %s — survivors must elect a NEW leader:\n", leader)
	killed, err := svc.KillLeader()
	if err != nil {
		return err
	}
	newLeader, err := svc.WaitNewLeader(killed, failoverElect)
	if err != nil {
		return err
	}
	fmt.Printf("    killed %s; new leader elected: %s\n", killed, newLeader)

	survivors := svc.SurvivorIDs(killed)
	sort.Strings(survivors)

	// [6] Pre-kill keys still readable on survivors (committed before the crash).
	fmt.Printf("[6] pre-kill keys still readable on survivors %v (durability across failover):\n", survivors)
	for _, p := range puts {
		if err := svc.WaitReplicated(p.k, p.v, survivors, replTimeout); err != nil {
			return err
		}
		fmt.Printf("    %-20s ->", p.k)
		for _, id := range survivors {
			v, _, err := svc.Get(id, p.k)
			if err != nil {
				return err
			}
			fmt.Printf("  %s=%q", id, v)
		}
		fmt.Println()
	}

	// [7] Put a new key via the NEW leader; it must replicate to survivors.
	fmt.Printf("[7] Put a fresh key via the new leader %s and confirm replication:\n", newLeader)
	if err := svc.Put("post/failover/key", "written-after-failover"); err != nil {
		return err
	}
	if err := svc.WaitReplicated("post/failover/key", "written-after-failover", survivors, replTimeout); err != nil {
		return err
	}
	fmt.Printf("    PUT  %-20s = %s\n", "post/failover/key", "written-after-failover")
	fmt.Printf("    %-20s ->", "post/failover/key")
	for _, id := range survivors {
		v, ok, err := svc.Get(id, "post/failover/key")
		if err != nil {
			return err
		}
		fmt.Printf("  %s=%q(ok=%v)", id, v, ok)
	}
	fmt.Println()

	fmt.Println("=== demo complete: leader election + log replication + failover all proven ===")
	return nil
}

// firstFollower returns the id of some node that is not the given leader.
func firstFollower(svc *KVService, leader string) string {
	for _, id := range svc.NodeIDs() {
		if id != leader {
			return id
		}
	}
	return leader // single-node degenerate case; not reached in the 3-node demo
}
