package classads

// Adversarial / correctness audit for the ClassAd parser+evaluator.
//
// This file pins the parser+evaluator contract against hostile and malformed
// external expression strings, and contains an executable regression probe for
// a REAL denial-of-service defect: the recursive-descent parser and the
// evalExpr evaluator have NO recursion-depth guard, so a deeply nested
// expression (e.g. ~500k nested parentheses, a ~1 MB string) drives the
// goroutine stack past Go's 1 GB limit and aborts the process with an
// UNRECOVERABLE `fatal error: stack overflow`.
//
// Because that crash is a runtime fatal error (recover() cannot catch it), the
// DoS probe runs the offending parse in a re-exec'd CHILD process so a crash
// does not take down the whole test binary. The probe ASSERTS the desired
// contract (bounded input must return an error, never crash); it FAILS today
// (documenting the bug) and will PASS once a depth guard is added to
// parseOr/parseUnary/parsePrimary and to evalExpr. See the FINDING comment at
// the bottom of this file for the minimal fix.

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 1. PARSE/EVAL ROBUSTNESS — malformed/hostile input must error, never panic.
// ---------------------------------------------------------------------------

// TestAdvMalformedNeverPanics feeds a battery of malformed, truncated, and
// hostile (but NOT deeply nested) expressions. Every one must return an error
// and must not panic. Deep-nesting DoS is covered separately by the subprocess
// probe below, because that failure mode is an unrecoverable fatal error.
func TestAdvMalformedNeverPanics(t *testing.T) {
	inputs := []string{
		"",                  // empty
		"   ",               // whitespace only
		"(",                 // lone open paren
		")",                 // lone close paren
		"(1+2",              // unbalanced open
		"1+2)",              // trailing close
		"((((1))))",         // balanced but harmless (must succeed, asserted elsewhere)
		"1 +",               // dangling binary operator
		"+ 1",               // leading binary operator
		"* 3",               // leading high-prec operator
		"1 2",               // two primaries, no operator
		"&&",                // operator only
		"1 &&",              // dangling &&
		"!",                 // dangling unary
		"-",                 // dangling unary minus
		"1.2.3",             // malformed number (multiple dots)
		".",                 // lone dot
		"@#$",               // invalid tokens
		"\x00\x01\x02",      // control bytes
		`"unterminated`,     // unterminated string
		`regexp("x"`,        // unterminated call
		`regexp("x",)`,      // trailing comma in args
		"foo(",              // unterminated user call
		"true false",        // two literals
		"1 == == 2",         // doubled operator
		"((1)",              // unbalanced nested
		"1)))",              // extra closes
		strings.Repeat("a b ", 100), // many primaries no operators
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Eval(%q) PANICKED: %v", in, r)
				}
			}()
			// We only require: no panic, and a clean (value,err) return.
			// Most of these are errors; "((((1))))" is a valid success.
			_, _ = Eval(in, nil)
		}()
	}
}

// TestAdvMalformedAreErrors pins that the clearly-invalid inputs specifically
// return a non-nil error (not a silent value). This catches a mutation that
// makes the parser swallow trailing garbage.
func TestAdvMalformedAreErrors(t *testing.T) {
	bad := []string{"", "   ", "(", ")", "(1+2", "1+2)", "1 +", "1 2", "1.2.3", "@#$", `"oops`}
	for _, in := range bad {
		if _, err := Eval(in, nil); err == nil {
			t.Errorf("Eval(%q) = nil error, want a parse error", in)
		}
	}
}

// TestAdvBalancedNestingSucceeds confirms small, legitimate nesting works so
// the depth-guard fix (when added) is tuned to reject only pathological depth,
// not ordinary expressions.
func TestAdvBalancedNestingSucceeds(t *testing.T) {
	v, err := Eval("((((1 + 2))))", nil)
	if err != nil {
		t.Fatalf("balanced nesting errored: %v", err)
	}
	if v != float64(3) {
		t.Errorf("((((1 + 2)))) = %v, want 3", v)
	}
	// A few hundred levels is still "normal" and must keep working.
	n := 500
	expr := strings.Repeat("(", n) + "7" + strings.Repeat(")", n)
	v, err = Eval(expr, nil)
	if err != nil {
		t.Fatalf("500-deep balanced parens errored: %v", err)
	}
	if v != float64(7) {
		t.Errorf("500-deep parens = %v, want 7", v)
	}
}

// ---------------------------------------------------------------------------
// 2. DoS REGRESSION PROBE — deep nesting must NOT crash the process.
//    Runs in a child process because the failure is a fatal stack overflow.
// ---------------------------------------------------------------------------

const (
	dosEnvKey   = "HELIX_CLASSADS_DOS_CHILD"
	dosDepth    = 500000 // ~1 MB input; reliably overflows the 1 GB stack today
	dosChildEnv = "1"
)

// runDeepParseInChild is the child-side body: parse a pathologically deep
// expression. A guarded parser returns an error (we exit 0). An UNGUARDED
// parser overflows the stack and the runtime aborts the child with a fatal
// error (non-zero exit) BEFORE this code can exit cleanly.
func runDeepParseInChild() {
	expr := strings.Repeat("(", dosDepth) + "1" + strings.Repeat(")", dosDepth)
	_, err := Eval(expr, nil)
	// Reaching here at all means no stack overflow. Whether err is nil or a
	// depth-limit error, the contract "do not crash" is satisfied.
	_ = err
	os.Exit(0)
}

// TestAdvDeepNestingDoesNotCrashProcess is the load-bearing DoS regression.
//
// CONTRACT: a bounded (~1 MB) hostile expression must be rejected with an error
// or evaluated, but MUST NOT abort the process. Today the recursive-descent
// parser has no depth guard, so the child dies with
// "fatal error: stack overflow" and this test FAILS — that failure IS the proof
// of the bug. Once a depth guard is added to the parser, the child returns an
// error and exits 0, flipping this test to PASS.
//
// §1.1 mutation pairing: this test is itself the mutation detector for the fix.
// If a future change removes the depth guard, the child crashes again and this
// test fails — exactly the regression we want pinned.
func TestAdvDeepNestingDoesNotCrashProcess(t *testing.T) {
	if os.Getenv(dosEnvKey) == dosChildEnv {
		runDeepParseInChild()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdvDeepNestingDoesNotCrashProcess$", "-test.v")
	cmd.Env = append(os.Environ(), dosEnvKey+"="+dosChildEnv)
	out, err := cmd.CombinedOutput()

	crashed := strings.Contains(string(out), "stack overflow") ||
		strings.Contains(string(out), "goroutine stack exceeds")

	if crashed || err != nil {
		t.Fatalf("DoS: deeply nested expression (%d parens) crashed the parser "+
			"instead of returning an error.\n"+
			"  child exit error: %v\n"+
			"  stack-overflow detected in child output: %v\n"+
			"  FIX: add a recursion-depth guard to parseOr/parseUnary/parsePrimary "+
			"(and to evalExpr) returning an error past a fixed limit (e.g. 1000).",
			dosDepth, err, crashed)
	}
}

// TestAdvDeepEvalDoesNotCrashProcess pins the SAME contract for the evaluator,
// which recurses in evalExpr with no depth guard. A pathologically deep AST
// (built directly, independent of the parser) overflows the stack today. Runs
// in a child process for the same fatal-error reason.
func TestAdvDeepEvalDoesNotCrashProcess(t *testing.T) {
	const evalEnvKey = "HELIX_CLASSADS_EVAL_DOS_CHILD"
	const evalDepth = 4000000 // evalExpr frames are shallow; needs a deep AST
	if os.Getenv(evalEnvKey) == dosChildEnv {
		var e Expr = Literal{Value: float64(1)}
		for i := 0; i < evalDepth; i++ {
			e = UnaryOp{Op: "-", Expr: e}
		}
		_, _ = evalExpr(e, nil)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdvDeepEvalDoesNotCrashProcess$", "-test.v")
	cmd.Env = append(os.Environ(), evalEnvKey+"="+dosChildEnv)
	out, err := cmd.CombinedOutput()
	crashed := strings.Contains(string(out), "stack overflow") ||
		strings.Contains(string(out), "goroutine stack exceeds")
	if crashed || err != nil {
		t.Fatalf("DoS: deeply nested AST (%d UnaryOp) crashed evalExpr instead of "+
			"returning.\n  child exit error: %v\n  stack-overflow detected: %v\n"+
			"  FIX: thread a depth counter through evalExpr and return an error "+
			"past a fixed limit.", evalDepth, err, crashed)
	}
}

// ---------------------------------------------------------------------------
// 3. EVAL CORRECTNESS — operator precedence & associativity (hand-verified).
// ---------------------------------------------------------------------------

// TestAdvPrecedenceArithmetic hand-verifies the prompt's canonical probe:
// 2 + 3 * 4 must be 14 (not 20), proving '*' binds tighter than '+'.
func TestAdvPrecedenceArithmetic(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"2 + 3 * 4", 14},          // * before +
		{"2 * 3 + 4", 10},          // * before +
		{"10 - 2 - 3", 5},          // left-assoc: (10-2)-3, not 10-(2-3)=11
		{"100 / 10 / 2", 5},        // left-assoc: (100/10)/2, not 100/(10/2)=20
		{"2 + 3 * 4 - 1", 13},      // mixed
		{"(2 + 3) * 4", 20},        // parens override
		{"2 * (3 + 4)", 14},        // parens override
		{"1 - 2 + 3", 2},           // left-assoc +/- same level: (1-2)+3
		{"8 / 4 * 2", 4},           // left-assoc * / same level: (8/4)*2, not 8/(4*2)=1
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
}

// TestAdvComparisonBindsLooserThanArithmetic pins that arithmetic binds tighter
// than comparison: "2 + 3 == 5" is "(2+3) == 5" -> true, not "2 + (3==5)".
func TestAdvComparisonBindsLooserThanArithmetic(t *testing.T) {
	got, err := Eval("2 + 3 == 5", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("Eval(\"2 + 3 == 5\") = %v, want true", got)
	}
	got, err = Eval("2 * 3 > 5", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("Eval(\"2 * 3 > 5\") = %v, want true", got)
	}
}

// TestAdvBooleanPrecedence pins comparison < && < || and that && binds tighter
// than ||, evaluated end-to-end (not just at parse time).
func TestAdvBooleanPrecedence(t *testing.T) {
	// true || false && false  ==  true || (false && false)  == true.
	// If || bound tighter it would be (true||false)&&false == false.
	got, err := Eval("true || false && false", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("Eval(\"true || false && false\") = %v, want true (&& binds tighter)", got)
	}
	// 1 < 2 && 3 < 4  -> comparisons bind tighter than && -> true.
	got, err = Eval("1 < 2 && 3 < 4", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("Eval(\"1 < 2 && 3 < 4\") = %v, want true", got)
	}
}

// TestAdvUnaryMinusVsBinary pins that unary minus binds tighter than binary
// arithmetic: "-2 * 3" is "(-2) * 3" = -6.
func TestAdvUnaryMinusVsBinary(t *testing.T) {
	got, err := Eval("-2 * 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(-6) {
		t.Errorf("Eval(\"-2 * 3\") = %v, want -6", got)
	}
	got, err = Eval("-(2 + 3)", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(-5) {
		t.Errorf("Eval(\"-(2 + 3)\") = %v, want -5", got)
	}
}

// ---------------------------------------------------------------------------
// 4. THREE-VALUED / ERROR LOGIC — undefined refs & type mismatch.
// ---------------------------------------------------------------------------

// TestAdvUndefinedAttributeErrors pins the package's contract: a reference to
// an undefined attribute yields an ERROR (this package models "undefined" as a
// Go error, not as a distinct UNDEFINED value).
func TestAdvUndefinedAttributeErrors(t *testing.T) {
	_, err := Eval("NoSuchAttr", nil)
	if err == nil {
		t.Fatal("Eval(undefined attr) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "undefined attribute") {
		t.Errorf("error = %q, want it to mention 'undefined attribute'", err.Error())
	}
}

// TestAdvUndefinedPropagatesThroughArithmetic pins that an undefined sub-expr
// propagates (errors) rather than silently yielding a wrong value.
func TestAdvUndefinedPropagation(t *testing.T) {
	exprs := []string{"Missing + 1", "1 + Missing", "Missing > 5", "Missing && true", "!Missing"}
	for _, e := range exprs {
		if _, err := Eval(e, nil); err == nil {
			t.Errorf("Eval(%q) = nil error, want undefined-attribute propagation", e)
		}
	}
}

// TestAdvShortCircuitSuppressesUndefined pins the documented short-circuit
// behavior: the right operand (including an undefined reference) is NOT
// evaluated when the left operand already determines the result.
func TestAdvShortCircuitSuppressesUndefined(t *testing.T) {
	// false && Missing -> false, Missing never evaluated.
	v, err := Eval("false && Missing", nil)
	if err != nil {
		t.Fatalf("false && Missing errored: %v", err)
	}
	if v != false {
		t.Errorf("false && Missing = %v, want false", v)
	}
	// true || Missing -> true, Missing never evaluated.
	v, err = Eval("true || Missing", nil)
	if err != nil {
		t.Fatalf("true || Missing errored: %v", err)
	}
	if v != true {
		t.Errorf("true || Missing = %v, want true", v)
	}
	// CONVERSE: when the left operand does NOT short-circuit, the undefined
	// right operand MUST surface. This is the mutation pair that proves the
	// short-circuit is conditional, not unconditional suppression.
	if _, err := Eval("true && Missing", nil); err == nil {
		t.Error("true && Missing = nil error, want undefined to surface (no short-circuit)")
	}
	if _, err := Eval("false || Missing", nil); err == nil {
		t.Error("false || Missing = nil error, want undefined to surface (no short-circuit)")
	}
}

// TestAdvTypeMismatchErrors pins that type-incompatible operations ERROR rather
// than panic or coerce to a wrong value.
func TestAdvTypeMismatchErrors(t *testing.T) {
	attrs := map[string]interface{}{"S": "abc"}
	// Relational against a non-numeric string: must error.
	if _, err := Eval("S < 5", attrs); err == nil {
		t.Error(`Eval("S < 5") with S="abc" = nil error, want "requires numbers"`)
	}
	// Arithmetic on a non-numeric string: must error.
	if _, err := Eval("S + 1", attrs); err == nil {
		t.Error(`Eval("S + 1") with S="abc" = nil error, want "requires numbers"`)
	}
}

// TestAdvDivisionByZeroErrors pins that division by zero ERRORS (never panics,
// never returns +Inf).
func TestAdvDivisionByZeroErrors(t *testing.T) {
	for _, e := range []string{"1 / 0", "5 / (3 - 3)", "0 / 0"} {
		v, err := Eval(e, nil)
		if err == nil {
			t.Errorf("Eval(%q) = %v, want a division-by-zero error", e, v)
		}
	}
}

// TestAdvStringEqualityCoercion pins string vs string equality and string vs
// number inequality semantics in valuesEqual.
func TestAdvEqualityTypeSemantics(t *testing.T) {
	cases := []struct {
		expr  string
		attrs map[string]interface{}
		want  bool
	}{
		{`"x86_64" == "x86_64"`, nil, true},
		{`"x86" == "arm"`, nil, false},
		{`1 == 1`, nil, true},
		{`true == true`, nil, false}, // wait: must be true
	}
	_ = cases // placeholder guard; real cases below to avoid mistakes
	check := func(expr string, attrs map[string]interface{}, want bool) {
		got, err := Eval(expr, attrs)
		if err != nil {
			t.Errorf("Eval(%q): %v", expr, err)
			return
		}
		if got != want {
			t.Errorf("Eval(%q) = %v, want %v", expr, got, want)
		}
	}
	check(`"x86_64" == "x86_64"`, nil, true)
	check(`"x86" == "arm"`, nil, false)
	check(`1 == 1`, nil, true)
	check(`1 == 2`, nil, false)
	check(`true == true`, nil, true)
	check(`true == false`, nil, false)
	// Cross-type equality: string "1" vs number 1. valuesEqual compares a
	// float64 only to another coercible float; "1" is a string so this is
	// the default Sprintf fallback path. Pin whatever the contract yields so a
	// change is caught.
	got, err := Eval(`1 == "1"`, nil)
	if err != nil {
		t.Fatalf(`Eval("1 == \"1\""): %v`, err)
	}
	// float64 branch: toFloat("1") succeeds -> 1==1 -> true.
	if got != true {
		t.Errorf(`Eval("1 == \"1\"") = %v, want true (string "1" coerces to 1.0)`, got)
	}
	// String "abc" vs number: toFloat fails -> not equal.
	got, err = Eval(`1 == "abc"`, nil)
	if err != nil {
		t.Fatalf(`Eval("1 == \"abc\""): %v`, err)
	}
	if got != false {
		t.Errorf(`Eval("1 == \"abc\"") = %v, want false`, got)
	}
}

// ---------------------------------------------------------------------------
// 5. CYCLES & EDGES.
// ---------------------------------------------------------------------------

// TestAdvCyclicAttributesTerminate pins that mutually-referential attributes do
// NOT cause an infinite loop. This evaluator stores attribute values as opaque
// interface{} and does NOT re-parse string values, so A="B", B="A" simply
// returns the stored strings — no recursion, no loop. This documents that the
// "cycle" attack surface is closed by design.
func TestAdvCyclicAttributesTerminate(t *testing.T) {
	done := make(chan struct{})
	go func() {
		attrs := map[string]interface{}{"A": "B", "B": "A"}
		v, err := Eval("A", attrs)
		if err != nil || v != "B" {
			t.Errorf("Eval(A) with cyclic attrs = (%v, %v), want (\"B\", nil)", v, err)
		}
		// Even an expression combining both must terminate.
		_, _ = Eval(`A == "B" && B == "A"`, attrs)
		close(done)
	}()
	select {
	case <-done:
	default:
		// Immediate; if it ever hangs the test framework timeout catches it.
		<-done
	}
}

// TestAdvEmptyAndMissing pins edge behavior for an empty attribute set and a
// missing attribute.
func TestAdvEmptyAndMissing(t *testing.T) {
	// Literal with no attrs: fine.
	if v, err := Eval("42", map[string]interface{}{}); err != nil || v != float64(42) {
		t.Errorf("Eval(42) with empty attrs = (%v,%v), want (42,nil)", v, err)
	}
	// Missing attr with empty (non-nil) map: error.
	if _, err := Eval("Ghost", map[string]interface{}{}); err == nil {
		t.Error("Eval(Ghost) with empty attrs = nil error, want undefined error")
	}
}

// ---------------------------------------------------------------------------
// FINDING (read with the file header):
//
//   REAL BUG — Unbounded-recursion DoS (stack overflow) in the parser and the
//   evaluator. Both descend without any depth guard:
//     * parser.go: parseOr -> ... -> parsePrimary -> (on '(') parseOr, and
//       parseUnary -> parseUnary on '!'/'-' chains.
//     * eval.go:   evalExpr recurses on every BinaryOp/UnaryOp child.
//   A ~500k-deep nested expression (~1 MB string of '(') drives the goroutine
//   stack past Go's 1 GB limit and aborts the PROCESS with an unrecoverable
//   `fatal error: stack overflow` (verified: exit status 2, single shot, in a
//   clean child process). Eval() is invoked on untrusted external requirement
//   strings, so this is a remotely triggerable denial of service.
//
//   MINIMAL FIX (coordinator to apply in production, not in this test file):
//     1. Add a depth field + small helper to Parser, e.g.
//          const maxParseDepth = 1000
//          func (p *Parser) enter() error { p.depth++; if p.depth > maxParseDepth { return fmt.Errorf("expression nesting too deep") }; return nil }
//          func (p *Parser) leave() { p.depth-- }
//        and call enter()/leave() at the top of parseOr (or, more cheaply, at
//        the two recursion sources: the '(' branch of parsePrimary and the
//        '!'/'-' branches of parseUnary), returning the error up the chain.
//     2. Add an analogous depth counter to evalExpr (thread `depth int` or a
//        small *struct field) returning an error past a fixed limit.
//   With the guard, TestAdvDeepNestingDoesNotCrashProcess and
//   TestAdvDeepEvalDoesNotCrashProcess flip from FAIL (child crash) to PASS
//   (child returns an error and exits 0), while TestAdvBalancedNestingSucceeds
//   guarantees ordinary nesting still parses.
// ---------------------------------------------------------------------------

var _ = strconv.Itoa // keep strconv imported if a future probe needs it
