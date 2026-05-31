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
