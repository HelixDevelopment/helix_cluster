package classads

// Adversarial round 2 — what the FIRST-PASS depth-cap fix's regression net MISSED.
//
// The first fix added two recursion guards (parser.go: maxParseDepth in parseOr
// + parseUnary; eval.go: maxEvalDepth in evalExprD) and the round-1 test file
// pins them with (a) a deep-PARENS subprocess probe (exercises the parseOr cap)
// and (b) a directly-built deep UnaryOp AST (exercises the evalExprD cap). Those
// two probes leave three real holes in the regression net, each verified here
// against the CURRENT (correct) code:
//
//   H1. The parser's OTHER recursion source — the '!'/'-' chain through
//       parseUnary — is never exercised at DoS depth by round 1. A pure
//       "!!!!…true" / "----…1" string never reaches a '(' so it never touches
//       the parseOr cap; only the parseUnary cap stands between it and a fatal
//       stack overflow. Mutation-verified: deleting ONLY the parseUnary guard
//       (leaving parseOr's) lets a multi-million-deep unary chain abort the
//       process with `fatal error: stack overflow` (exit status 2). Round 1
//       would stay green through that regression; this file would not.
//
//   H2. A FLAT (width, not paren-depth) operator chain — "1+1+1+…+1" — is
//       parsed ITERATIVELY (the for-loops in parseAdditive et al. never recurse)
//       so the parse caps never fire, yet it yields a left-leaning AST whose
//       eval depth equals the term count. The load-bearing guard there is the
//       evalExprD cap reached via a PARSER-PRODUCED AST (round 1 only built a
//       deep AST by hand). This pins that a hostile flat expression is rejected
//       by eval, never crashes, and that ordinary chains still compute.
//
//   H3. Recursion reached THROUGH a function-call argument (regexp("x", (((…))))
//       descends parseArgs -> parseOr) must hit the same cap. Round 1 never
//       routes depth through parseArgs.
//
// Plus correctness pins for the values that real matchmaking (pkg/scheduler
// ClassAdMatch.Filter, which is fail-CLOSED on error) feeds through this engine:
// IEEE-754 NaN/Inf from node labels (coerceAttr uses strconv.ParseFloat, which
// parses "NaN"/"Inf") must not flip a constraint open or panic.
//
// All probes whose failure mode is an UNRECOVERABLE fatal stack overflow run in
// a re-exec'd CHILD process (recover() cannot catch that crash). Every test here
// PASSES on the code left behind; each is a mutation detector for a specific
// guard.

import (
	"os"
	"os/exec"
	"math"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// H1. PARSER UNARY-CHAIN RECURSION (parseUnary cap) — distinct from parseOr.
// ---------------------------------------------------------------------------

const adv2ChildEnv = "HELIX_CLASSADS_ADV2_CHILD"

// TestAdv2UnaryBombDoesNotCrashProcess pins that a pathologically deep unary
// chain ("!"×N then a bool) is rejected with an error and never aborts the
// process. This exercises the parseUnary depth guard SPECIFICALLY: a pure unary
// chain contains no '(' so parseOr's guard is never reached — only parseUnary's
// guard (and, as backstop, evalExprD's) prevents the fatal stack overflow.
//
// Mutation pairing: with the parseUnary guard removed, a multi-million-deep
// chain overflows the stack (verified exit status 2); with it present, even a
// 2M-deep chain is rejected at the depth limit with a clean error and the child
// exits 0. The child uses a depth far above the cap so the test is robust
// whichever guard (parse or eval) fires first.
func TestAdv2UnaryBombDoesNotCrashProcess(t *testing.T) {
	if os.Getenv(adv2ChildEnv) == "bang" {
		expr := strings.Repeat("!", 2000000) + "true"
		_, err := Eval(expr, nil)
		_ = err // reaching here at all means no stack overflow
		os.Exit(0)
	}
	if os.Getenv(adv2ChildEnv) == "minus" {
		expr := strings.Repeat("-", 2000000) + "1"
		_, err := Eval(expr, nil)
		_ = err
		os.Exit(0)
	}
	for _, mode := range []string{"bang", "minus"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAdv2UnaryBombDoesNotCrashProcess$")
		cmd.Env = append(os.Environ(), adv2ChildEnv+"="+mode)
		out, err := cmd.CombinedOutput()
		crashed := strings.Contains(string(out), "stack overflow") ||
			strings.Contains(string(out), "goroutine stack exceeds")
		if crashed || err != nil {
			t.Fatalf("DoS: deep unary %q chain crashed the parser instead of "+
				"returning an error.\n  child exit error: %v\n  stack-overflow in child: %v\n"+
				"  This guards the parseUnary depth cap, which the round-1 paren probe "+
				"does NOT exercise.", mode, err, crashed)
		}
	}
}

// TestAdv2UnaryChainCorrectBelowCap pins that ordinary (legitimate) unary chains
// still evaluate correctly, so the cap rejects only pathological depth. This is
// the converse mutation pair: a guard that rejected ALL unary nesting would fail
// here.
func TestAdv2UnaryChainCorrectBelowCap(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"!true", false}, {"!!true", true}, {"!!!true", false}, {"!!!!true", true},
	}
	for _, c := range cases {
		got, err := Eval(c.expr, nil)
		if err != nil {
			t.Errorf("Eval(%q): unexpected error %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
	// Unary minus chain folds correctly too.
	got, err := Eval("--5", nil) // -(-5) = 5
	if err != nil {
		t.Fatalf("Eval(--5): %v", err)
	}
	if got != float64(5) {
		t.Errorf("Eval(--5) = %v, want 5", got)
	}
}

// ---------------------------------------------------------------------------
// H2. FLAT-WIDTH CHAIN — parsed iteratively, deep AST caught by the EVAL cap.
// ---------------------------------------------------------------------------

// TestAdv2FlatChainBoundedByEvalCap pins the load-bearing guard for a hostile
// FLAT expression: "1+1+…+1" parses without tripping any parse cap (the binary
// loops are iterative), but builds a left-leaning AST whose eval recursion depth
// equals the term count. A huge flat chain must be rejected by the evalExprD cap
// with a clean error — never a crash, never a silent wrong value.
//
// Runs in a child process: if the eval cap were absent, a multi-million-term
// flat chain would overflow the stack during eval (a fatal error).
func TestAdv2FlatChainBoundedByEvalCap(t *testing.T) {
	if os.Getenv(adv2ChildEnv) == "flat" {
		var b strings.Builder
		b.WriteByte('1')
		for i := 0; i < 2000000; i++ {
			b.WriteString("+1")
		}
		_, err := Eval(b.String(), nil)
		_ = err // reaching here means eval did not overflow the stack
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdv2FlatChainBoundedByEvalCap$")
	cmd.Env = append(os.Environ(), adv2ChildEnv+"=flat")
	out, err := cmd.CombinedOutput()
	crashed := strings.Contains(string(out), "stack overflow") ||
		strings.Contains(string(out), "goroutine stack exceeds")
	if crashed || err != nil {
		t.Fatalf("DoS: flat 2M-term additive chain crashed eval instead of "+
			"returning an error.\n  child exit error: %v\n  stack-overflow in child: %v\n"+
			"  This guards the evalExprD cap reached via a PARSER-PRODUCED deep AST, "+
			"which the round-1 hand-built-AST probe does NOT exercise.", err, crashed)
	}
}

// TestAdv2ModerateFlatChainComputes pins that an ordinary flat chain (well below
// the eval cap) computes the correct value, so the cap rejects only pathological
// width. Mutation pair for the above.
func TestAdv2ModerateFlatChainComputes(t *testing.T) {
	const n = 1000
	var b strings.Builder
	b.WriteByte('1')
	for i := 0; i < n-1; i++ {
		b.WriteString("+1")
	}
	got, err := Eval(b.String(), nil)
	if err != nil {
		t.Fatalf("Eval(flat chain of %d ones): %v", n, err)
	}
	if got != float64(n) {
		t.Errorf("Eval(1+1+…) [%d terms] = %v, want %d", n, got, n)
	}
}

// ---------------------------------------------------------------------------
// H3. RECURSION THROUGH A FUNCTION-CALL ARGUMENT (parseArgs -> parseOr).
// ---------------------------------------------------------------------------

// TestAdv2FunctionArgNestingBounded pins that deep nesting reached THROUGH a
// function-call argument (regexp("x", (((…))))) hits the parseOr depth cap and
// returns a clean error rather than recursing without bound. parseArgs is a
// recursion source the round-1 probes never route depth through. This depth
// (well above the cap) is rejected at parse time before any deep stack builds,
// so it is safe to run in-process under a timeout guard.
func TestAdv2FunctionArgNestingBounded(t *testing.T) {
	done := make(chan struct{})
	var err error
	go func() {
		inner := strings.Repeat("(", 200000) + "1" + strings.Repeat(")", 200000)
		_, err = Eval(`regexp("x", `+inner+`)`, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("function-arg nesting probe hung: parseArgs->parseOr recursion is not bounded")
	}
	if err == nil {
		t.Fatal("deep nesting via a function argument returned nil error, want a depth-limit error")
	}
	if !strings.Contains(err.Error(), "depth limit") {
		t.Errorf("function-arg nesting error = %q, want it to mention the depth limit", err.Error())
	}
	// A legitimate shallow function call still works.
	got, err := Eval(`regexp("a+", "aaa")`, nil)
	if err != nil {
		t.Fatalf("Eval(regexp valid): %v", err)
	}
	if got != true {
		t.Errorf("Eval(regexp(\"a+\",\"aaa\")) = %v, want true", got)
	}
}

// ---------------------------------------------------------------------------
// IEEE-754 NaN / Inf CONSTRAINT CONTRACT — fail-CLOSED matchmaking.
// ---------------------------------------------------------------------------

// TestAdv2NaNConstraintFailsClosed pins the safety-critical contract that an
// attribute value of NaN (which real matchmaking can produce: a node label of
// "NaN" is parsed by strconv.ParseFloat in pkg/scheduler's coerceAttr) makes
// every relational constraint evaluate to FALSE — i.e. a node advertising a NaN
// capacity is NOT admitted. A regression that made any NaN comparison return
// true would silently place a job on a node that does not actually satisfy the
// requirement (a fail-OPEN matchmaking defect, the worst class here). This pins
// IEEE-754 semantics flowing correctly through toFloat/evalBinary.
func TestAdv2NaNConstraintFailsClosed(t *testing.T) {
	attrs := map[string]interface{}{"Mem": math.NaN()}
	// Every ordering comparison against NaN is false; a requirement is rejected.
	for _, e := range []string{"Mem >= 100", "Mem > 100", "Mem < 100", "Mem <= 100"} {
		got, err := Eval(e, attrs)
		if err != nil {
			t.Errorf("Eval(%q) with Mem=NaN: unexpected error %v", e, err)
			continue
		}
		if got != false {
			t.Errorf("Eval(%q) with Mem=NaN = %v, want false (NaN must NOT satisfy a "+
				"capacity requirement — fail-closed matchmaking)", e, got)
		}
	}
	// NaN != NaN is true and NaN == NaN is false (IEEE-754), and importantly an
	// equality requirement against NaN does not admit the node.
	if got, _ := Eval("Mem == Mem", attrs); got != false {
		t.Errorf("Eval(Mem == Mem) with Mem=NaN = %v, want false", got)
	}
	if got, _ := Eval("Mem != Mem", attrs); got != true {
		t.Errorf("Eval(Mem != Mem) with Mem=NaN = %v, want true", got)
	}
}

// TestAdv2InfConstraintSemantics pins +Inf/-Inf ordering: +Inf satisfies a lower
// bound and never an upper bound; -Inf is the converse. A node advertising an
// infinite capacity (e.g. a "Inf" label, or a 1e400 overflow) thus passes a
// ">=" requirement — documented, deterministic, no panic.
func TestAdv2InfConstraintSemantics(t *testing.T) {
	pos := map[string]interface{}{"X": math.Inf(1)}
	if got, err := Eval("X >= 100", pos); err != nil || got != true {
		t.Errorf("Eval(X >= 100) with X=+Inf = (%v,%v), want (true,nil)", got, err)
	}
	if got, err := Eval("X <= 100", pos); err != nil || got != false {
		t.Errorf("Eval(X <= 100) with X=+Inf = (%v,%v), want (false,nil)", got, err)
	}
	neg := map[string]interface{}{"X": math.Inf(-1)}
	if got, err := Eval("X <= 100", neg); err != nil || got != true {
		t.Errorf("Eval(X <= 100) with X=-Inf = (%v,%v), want (true,nil)", got, err)
	}
}

// TestAdv2HostileAttrValuesNeverPanic pins that non-coercible Go attribute values
// injected directly into the attrs map (nil, []byte, a struct) produce a clean
// error or false, never a panic — the attrs map is filled from external node
// data. In the matchmaking caller an error is fail-closed; what matters is the
// absence of a panic that would crash the scheduler.
func TestAdv2HostileAttrValuesNeverPanic(t *testing.T) {
	type weird struct{ N int }
	hostile := []interface{}{nil, []byte("x"), weird{N: 1}, map[string]int{"a": 1}, []int{1, 2}}
	exprs := []string{"X == 1", "X < 1", "X && true", "X || false", "!X", "X + 1", `X == "y"`}
	for _, hv := range hostile {
		attrs := map[string]interface{}{"X": hv}
		for _, e := range exprs {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("Eval(%q) with X=%T PANICKED: %v", e, hv, r)
					}
				}()
				_, _ = Eval(e, attrs) // value/err both acceptable; panic is not
			}()
		}
	}
}
