package classads

import (
	"testing"
)

func TestParseLiteral(t *testing.T) {
	tests := []struct {
		input string
		want  interface{}
	}{
		{"42", float64(42)},
		{"3.14", 3.14},
		{`"hello"`, "hello"},
		{"true", true},
		{"false", false},
	}
	for _, tc := range tests {
		p := NewParser(tc.input)
		expr, err := p.Parse()
		if err != nil {
			t.Fatalf("parse %q: %v", tc.input, err)
		}
		lit, ok := expr.(Literal)
		if !ok {
			t.Fatalf("expected Literal, got %T", expr)
		}
		if lit.Value != tc.want {
			t.Errorf("parse %q = %v, want %v", tc.input, lit.Value, tc.want)
		}
	}
}

func TestParseIdentifier(t *testing.T) {
	p := NewParser("Memory")
	expr, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	id, ok := expr.(Identifier)
	if !ok {
		t.Fatalf("expected Identifier, got %T", expr)
	}
	if id.Name != "Memory" {
		t.Errorf("name = %q, want Memory", id.Name)
	}
}

func TestParseBinaryOp(t *testing.T) {
	p := NewParser("1 + 2")
	expr, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	bin, ok := expr.(BinaryOp)
	if !ok {
		t.Fatalf("expected BinaryOp, got %T", expr)
	}
	if bin.Op != "+" {
		t.Errorf("op = %q, want +", bin.Op)
	}
}

func TestParseComparison(t *testing.T) {
	tests := []string{
		"x < 10",
		"x <= 10",
		"x > 10",
		"x >= 10",
		"x == 10",
		"x != 10",
	}
	for _, tc := range tests {
		_, err := NewParser(tc).Parse()
		if err != nil {
			t.Errorf("parse %q: %v", tc, err)
		}
	}
}

func TestParseLogical(t *testing.T) {
	_, err := NewParser("a && b || c && d").Parse()
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseFunctionCall(t *testing.T) {
	p := NewParser(`regexp("foo.*", bar)`)
	expr, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := expr.(FunctionCall)
	if !ok {
		t.Fatalf("expected FunctionCall, got %T", expr)
	}
	if fc.Name != "regexp" {
		t.Errorf("name = %q, want regexp", fc.Name)
	}
	if len(fc.Args) != 2 {
		t.Errorf("args = %d, want 2", len(fc.Args))
	}
}

func TestParseUnary(t *testing.T) {
	p := NewParser("!true")
	expr, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	u, ok := expr.(UnaryOp)
	if !ok {
		t.Fatalf("expected UnaryOp, got %T", expr)
	}
	if u.Op != "!" {
		t.Errorf("op = %q, want !", u.Op)
	}
}

func TestParseParen(t *testing.T) {
	_, err := NewParser("(1 + 2) * 3").Parse()
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseError(t *testing.T) {
	_, err := NewParser("1 +").Parse()
	if err == nil {
		t.Error("expected error for incomplete expression")
	}
}
