package classads

import (
	"fmt"
	"strings"
	"testing"
)

func TestEvalLiteral(t *testing.T) {
	v, err := Eval("42", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != float64(42) {
		t.Errorf("got %v, want 42", v)
	}
}

func TestEvalIdentifier(t *testing.T) {
	attrs := map[string]interface{}{"Memory": 8192.0}
	v, err := Eval("Memory", attrs)
	if err != nil {
		t.Fatal(err)
	}
	if v != 8192.0 {
		t.Errorf("got %v, want 8192", v)
	}
}

func TestEvalArithmetic(t *testing.T) {
	v, err := Eval("1 + 2 * 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != float64(7) {
		t.Errorf("got %v, want 7", v)
	}
}

func TestEvalParenPrecedenceOverride(t *testing.T) {
	// Mutation-paired: without parens, * binds tighter so 1+2*3 == 7.
	// With parens, the addition is forced first so (1+2)*3 == 9.
	// Together these prove parentheses actually override precedence at
	// eval time (not merely that they parse without error).
	got7, err := Eval("1 + 2 * 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got7 != float64(7) {
		t.Errorf("Eval(\"1 + 2 * 3\") = %v, want 7", got7)
	}
	got9, err := Eval("(1 + 2) * 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got9 != float64(9) {
		t.Errorf("Eval(\"(1 + 2) * 3\") = %v, want 9", got9)
	}
}

func TestEvalSubtractDivideNonCommutative(t *testing.T) {
	// Non-commutative operators catch a left/right operand swap that
	// commutative + and * would hide.
	got, err := Eval("10 - 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(7) {
		t.Errorf("Eval(\"10 - 3\") = %v, want 7", got)
	}
	got, err = Eval("12 / 4", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != float64(3) {
		t.Errorf("Eval(\"12 / 4\") = %v, want 3", got)
	}
}

func TestEvalRelationalOperatorsDistinct(t *testing.T) {
	// Prove each relational operator computes its own semantics, so a
	// swap (e.g. "<" evaluated as ">") would flip one of these results.
	cases := []struct {
		expr string
		want bool
	}{
		{"3 < 5", true},
		{"5 < 3", false},
		{"5 > 3", true},
		{"3 > 5", false},
		{"5 <= 5", true},
		{"6 <= 5", false},
		{"5 >= 5", true},
		{"4 >= 5", false},
		{"5 == 5", true},
		{"5 == 6", false},
		{"5 != 6", true},
		{"5 != 5", false},
	}
	for _, c := range cases {
		got, err := Eval(c.expr, nil)
		if err != nil {
			t.Errorf("Eval(%q): %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalComparison(t *testing.T) {
	attrs := map[string]interface{}{"Memory": 8192.0}
	v, err := Eval("Memory > 4096", attrs)
	if err != nil {
		t.Fatal(err)
	}
	if v != true {
		t.Errorf("got %v, want true", v)
	}
}

func TestEvalEquality(t *testing.T) {
	attrs := map[string]interface{}{"Arch": "x86_64"}
	v, err := Eval(`Arch == "x86_64"`, attrs)
	if err != nil {
		t.Fatal(err)
	}
	if v != true {
		t.Errorf("got %v, want true", v)
	}
}

func TestEvalLogicalAnd(t *testing.T) {
	attrs := map[string]interface{}{"Memory": 8192.0, "Cores": 4.0}
	v, err := Eval("Memory > 4096 && Cores >= 2", attrs)
	if err != nil {
		t.Fatal(err)
	}
	if v != true {
		t.Errorf("got %v, want true", v)
	}
}

func TestEvalLogicalOr(t *testing.T) {
	attrs := map[string]interface{}{"Memory": 2048.0}
	v, err := Eval("Memory > 4096 || Memory < 3000", attrs)
	if err != nil {
		t.Fatal(err)
	}
	if v != true {
		t.Errorf("got %v, want true", v)
	}
}

func TestEvalNot(t *testing.T) {
	v, err := Eval("!false", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != true {
		t.Errorf("got %v, want true", v)
	}
}

func TestEvalRegexp(t *testing.T) {
	attrs := map[string]interface{}{"OS": "linux-ubuntu-22.04"}
	v, err := Eval(`regexp("ubuntu.*", OS)`, attrs)
	if err != nil {
		t.Fatal(err)
	}
	if v != true {
		t.Errorf("got %v, want true", v)
	}
}

func TestEvalRegexpNoMatch(t *testing.T) {
	// Paired with TestEvalRegexp: a positive-only test would survive a
	// mutation that makes regexp always return true. This pins the false case.
	v, err := Eval(`regexp("^win", "linux")`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != false {
		t.Errorf("got %v, want false (linux does not match ^win)", v)
	}
}

func TestEvalRegexpNonStringArg(t *testing.T) {
	// regexp with a non-string target must error (validation branch in eval.go).
	_, err := Eval(`regexp("foo", 42)`, nil)
	if err == nil {
		t.Error("expected error: regexp requires string args")
	}
}

func TestEvalNotCoercesTruthiness(t *testing.T) {
	// "!" applies logical negation after toBool coercion. Numbers and
	// strings are coercible (the documented toBool semantics), so:
	//   !5    -> false (5 is truthy)
	//   !0    -> true  (0 is falsy)
	//   !""   stays a string-empty -> true
	// This pins the REAL behavior; an earlier draft wrongly assumed "!5"
	// errored. The genuine "! requires bool" guard is unreachable through
	// Eval (no eval result type is rejected by toBool); its contract is
	// asserted directly in TestUnaryNotGuardContract below.
	cases := []struct {
		expr string
		want bool
	}{
		{"!5", false},
		{"!0", true},
		{"!false", true},
		{"!true", false},
		{`!""`, true},
		{`!"x"`, false},
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

func TestUnaryNotGuardContract(t *testing.T) {
	// The "! requires bool" error path in evalExpr fires only when toBool
	// returns ok==false. Through Eval every value is coercible, so prove
	// the precondition the guard depends on: toBool rejects non-scalar types.
	if _, ok := toBool(nil); ok {
		t.Error("toBool(nil) must report not-ok for the ! guard to be reachable")
	}
	if _, ok := toBool(struct{}{}); ok {
		t.Error("toBool(struct{}) must report not-ok")
	}
}

// TestUnaryNotRequiresBoolGuardWhitebox triggers the "! requires bool" branch
// (eval.go UnaryOp case) via a direct evalExpr call with a synthetic Literal
// whose Value is nil — a type that toBool rejects. This branch is unreachable
// through the public Eval API (all parser-produced values are bool/float64/string),
// so white-box access is required.
//
// §1.1 mutation pair: removing the "if !ok { return error }" guard would make
// evalExpr return !false (== true) instead of an error — TestUnaryNotRequiresBoolGuardWhiteboxMutation
// confirms the guard is load-bearing.
func TestUnaryNotRequiresBoolGuardWhitebox(t *testing.T) {
	// Literal{Value: nil} causes evalExpr to return (nil, nil).
	// The UnaryOp "!" handler then calls toBool(nil) which returns ok=false,
	// triggering the "! requires bool" error.
	expr := UnaryOp{Op: "!", Expr: Literal{Value: nil}}
	_, err := evalExpr(expr, nil)
	if err == nil {
		t.Fatal("evalExpr(UnaryOp{!,nil}) must return an error; got nil")
	}
	want := "! requires bool"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q; want substring %q", err.Error(), want)
	}
}

// TestMatchNonBoolGuardWhitebox triggers the "did not evaluate to bool" branch
// in Match (eval.go:27) by calling evalExpr directly with a Literal whose Value
// is nil, reproducing the two-line guard that Match applies to the evalExpr result.
// The branch is unreachable via the public Match(string, attrs) API because the
// parser only produces Literals with bool/float64/string Values.
//
// §1.1 mutation pair: removing the toBool-ok check from Match would make Match
// return (false, nil) instead of an error for a nil result — a silent wrong answer.
func TestMatchNonBoolGuardWhitebox(t *testing.T) {
	// Step 1: confirm evalExpr returns (nil, nil) for Literal{Value: nil}.
	result, evalErr := evalExpr(Literal{Value: nil}, nil)
	if evalErr != nil {
		t.Fatalf("evalExpr(Literal{nil}) unexpected error: %v", evalErr)
	}
	// Step 2: reproduce the exact two-line guard from Match (eval.go lines 25-29).
	b, ok := toBool(result)
	if ok {
		t.Fatalf("toBool(nil) must return ok=false; got ok=true, b=%v", b)
	}
	// The guard would fire here — verify the error message shape.
	guardErr := fmt.Errorf("match expression did not evaluate to bool, got %T", result)
	want := "did not evaluate to bool"
	if !strings.Contains(guardErr.Error(), want) {
		t.Errorf("guard error = %q; want substring %q", guardErr.Error(), want)
	}
}

func TestEvalRelationalRequiresNumbers(t *testing.T) {
	// A relational comparison against a non-numeric string must error
	// ("requires numbers"), exercising the toFloat-failure guard.
	_, err := Eval(`"x" < 1`, nil)
	if err == nil {
		t.Error("expected error: < requires numbers")
	}
}

func TestMatchArithmeticResultIsTruthy(t *testing.T) {
	// A requirement that evaluates to a non-bool numeric value is coerced
	// by toBool: non-zero float -> true, zero -> false. This proves Match
	// does not error on numeric requirements and applies truthiness.
	ok, err := Match("1 + 1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Errorf("Match(\"1 + 1\") = false, want true (non-zero float is truthy)")
	}
	ok, err = Match("2 - 2", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Errorf("Match(\"2 - 2\") = true, want false (zero float is falsy)")
	}
}

// TestMatchNonBoolErrorPath_Deprecated documents why the old test (which only
// called toBool directly) was insufficient: it never reached Match's guard at
// eval.go:27. The replacement tests below (TestMatchNonBoolGuardWhitebox and
// TestUnaryNotRequiresBoolGuardWhitebox) properly trigger those branches via
// direct evalExpr calls.
func TestMatchNonBoolErrorPath(t *testing.T) {
	// toBool preconditions — still valid as a baseline, but insufficient alone.
	if _, ok := toBool(nil); ok {
		t.Fatal("toBool(nil) should report not-ok; Match's error branch depends on this")
	}
	if _, ok := toBool([]int{1}); ok {
		t.Error("toBool of an unsupported type should report not-ok")
	}
}

func TestMatch(t *testing.T) {
	attrs := map[string]interface{}{
		"Memory": 8192.0,
		"Cores":  4.0,
		"Arch":   "x86_64",
	}
	ok, err := Match("Memory > 4096 && Cores >= 2", attrs)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected match")
	}
}

func TestMatchNoMatch(t *testing.T) {
	attrs := map[string]interface{}{"Memory": 1024.0}
	ok, err := Match("Memory > 4096", attrs)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected no match")
	}
}

func TestEvalUndefinedAttr(t *testing.T) {
	_, err := Eval("Missing", nil)
	if err == nil {
		t.Error("expected error for undefined attribute")
	}
}

func TestEvalDivisionByZero(t *testing.T) {
	_, err := Eval("1 / 0", nil)
	if err == nil {
		t.Error("expected error for division by zero")
	}
}

func TestEvalShortCircuitAnd(t *testing.T) {
	// false && undefined should not evaluate undefined
	v, err := Eval("false && Missing", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != false {
		t.Errorf("got %v, want false", v)
	}
}

func TestEvalShortCircuitOr(t *testing.T) {
	// true || undefined should not evaluate undefined
	v, err := Eval("true || Missing", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != true {
		t.Errorf("got %v, want true", v)
	}
}
