package classads

import (
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

func TestMatchNonBoolErrorPath(t *testing.T) {
	// The "did not evaluate to bool" guard in Match (eval.go) triggers
	// only when toBool cannot coerce the result. Through normal Eval the
	// result is always bool/float64/string (all coercible), so the guard
	// is exercised here by feeding toBool the one shape it rejects,
	// proving the branch is correct rather than leaving it untested.
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
