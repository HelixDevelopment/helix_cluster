// Command dst-sim is the deterministic-simulation CI GATE (HXC-915).
//
// It executes a large number (>= 1000 by default) of SEEDED deterministic
// simulations of a representative distributed scenario — a single-register
// linearizable store replicated through a primary actor — and runs a
// linearizability/safety check (composing pkg/porcupine's WGL checker and
// pkg/dst's deterministic harness) on EACH seed. The moment any seed surfaces a
// safety/linearizability violation, the runner prints the offending seed and a
// witness operation and exits NON-ZERO. It exits zero only if every seed passes.
//
// WHY this is the load-bearing artifact (CLAUDE-1): a green CI job that merely
// ran tests does not prove the system is correct under adversarial scheduling.
// This binary turns "does the replicated register stay linearizable under
// dropped/reordered/delayed messages and disk faults across 1000 seeds?" into a
// single deterministic PASS/FAIL. Because the entire execution of each seed is a
// pure function of that seed (pkg/dst guarantees trace = f(seed)), the SAME seed
// set ALWAYS reproduces the SAME pass/fail outcome — the gate is deterministic
// and a failure is captured forever as "seed N fails".
//
// Usage:
//
//	dst-sim [-seeds N] [-start S] [-steps M] [-v]
//
// It is wired into .github/workflows/disabled/dst-sim.yml (active workflows in
// this repo live disabled; the binary is the enforcer).
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var (
		seeds   = flag.Int("seeds", 1000, "number of seeds to run (seeds start..start+seeds-1)")
		start   = flag.Int64("start", 0, "first seed (inclusive)")
		steps   = flag.Int("steps", 4000, "max scheduler steps per simulation")
		verbose = flag.Bool("v", false, "print per-seed PASS lines (noisy)")
	)
	flag.Parse()

	cfg := ScenarioConfig{MaxSteps: *steps}
	if code := Gate(os.Stdout, *start, *seeds, cfg, *verbose); code != 0 {
		os.Exit(code)
	}
}

// Gate runs RunSeed over the seed range [start, start+seeds) and writes a human
// report to out. It returns 0 iff every seed is linearizable; otherwise it
// returns 1 after printing the FIRST offending seed (seeds are processed in
// ascending order, so "first" is deterministic). Gate is the testable core of
// the binary: tests drive it directly to prove both the all-pass exit-0 outcome
// and the load-bearing non-zero exit on an injected violation.
func Gate(out interface {
	Write([]byte) (int, error)
}, start int64, seeds int, cfg ScenarioConfig, verbose bool) int {
	fmt.Fprintf(out, "dst-sim: running %d seeded simulations (seeds %d..%d), %d steps each\n",
		seeds, start, start+int64(seeds)-1, cfg.MaxSteps)

	for i := 0; i < seeds; i++ {
		seed := start + int64(i)
		res := RunSeed(seed, cfg)
		if !res.OK {
			fmt.Fprintf(out, "FAIL: seed %d surfaced a linearizability violation\n", seed)
			fmt.Fprintf(out, "  reason:   %s\n", res.Reason)
			fmt.Fprintf(out, "  witness:  input=%v output=%v call=%d return=%d\n",
				res.Offending.Input, res.Offending.Output, res.Offending.Call, res.Offending.Return)
			fmt.Fprintf(out, "  replay:   dst-sim -start %d -seeds 1 -steps %d\n", seed, cfg.MaxSteps)
			return 1
		}
		if verbose {
			fmt.Fprintf(out, "PASS: seed %d (%d ops linearizable)\n", seed, res.NumOps)
		}
	}

	fmt.Fprintf(out, "OK: all %d seeds linearizable (exit 0)\n", seeds)
	return 0
}
