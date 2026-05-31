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
	// Assert the parsed BinaryOp.Op per operator so a swapped-operator
	// mutation (e.g. parsing "<" as ">") fails. Asserting only err==nil
	// would let such a mutation survive.
	tests := []struct {
		input  string
		wantOp string
	}{
		{"x < 10", "<"},
		{"x <= 10", "<="},
		{"x > 10", ">"},
		{"x >= 10", ">="},
		{"x == 10", "=="},
		{"x != 10", "!="},
	}
	for _, tc := range tests {
		expr, err := NewParser(tc.input).Parse()
		if err != nil {
			t.Errorf("parse %q: %v", tc.input, err)
			continue
		}
		bin, ok := expr.(BinaryOp)
		if !ok {
			t.Errorf("parse %q: expected BinaryOp, got %T", tc.input, expr)
			continue
		}
		if bin.Op != tc.wantOp {
			t.Errorf("parse %q: Op = %q, want %q", tc.input, bin.Op, tc.wantOp)
		}
		// Confirm operands are not swapped: left is the identifier, right the literal.
		if id, ok := bin.Left.(Identifier); !ok || id.Name != "x" {
			t.Errorf("parse %q: Left = %#v, want Identifier{x}", tc.input, bin.Left)
		}
		if lit, ok := bin.Right.(Literal); !ok || lit.Value != float64(10) {
			t.Errorf("parse %q: Right = %#v, want Literal{10}", tc.input, bin.Right)
		}
	}
}

func TestParseArithmeticOps(t *testing.T) {
	// Pin BinaryOp.Op for each arithmetic operator so a swap mutation fails.
	tests := []struct {
		input  string
		wantOp string
	}{
		{"1 + 2", "+"},
		{"1 - 2", "-"},
		{"1 * 2", "*"},
		{"1 / 2", "/"},
	}
	for _, tc := range tests {
		expr, err := NewParser(tc.input).Parse()
		if err != nil {
			t.Errorf("parse %q: %v", tc.input, err)
			continue
		}
		bin, ok := expr.(BinaryOp)
		if !ok {
			t.Errorf("parse %q: expected BinaryOp, got %T", tc.input, expr)
			continue
		}
		if bin.Op != tc.wantOp {
			t.Errorf("parse %q: Op = %q, want %q", tc.input, bin.Op, tc.wantOp)
		}
	}
}

func TestParseLogical(t *testing.T) {
	// "a && b || c && d" must parse as ((a && b) || (c && d)): || is the
	// root, both children are &&. This proves && binds tighter than ||.
	// Asserting only err==nil would not catch an &&/|| precedence swap.
	expr, err := NewParser("a && b || c && d").Parse()
	if err != nil {
		t.Fatal(err)
	}
	root, ok := expr.(BinaryOp)
	if !ok {
		t.Fatalf("expected BinaryOp at root, got %T", expr)
	}
	if root.Op != "||" {
		t.Fatalf("root Op = %q, want || (&& must bind tighter than ||)", root.Op)
	}
	left, ok := root.Left.(BinaryOp)
	if !ok || left.Op != "&&" {
		t.Errorf("root.Left = %#v, want BinaryOp{Op:\"&&\"}", root.Left)
	}
	right, ok := root.Right.(BinaryOp)
	if !ok || right.Op != "&&" {
		t.Errorf("root.Right = %#v, want BinaryOp{Op:\"&&\"}", root.Right)
	}
	// And the && children hold the expected identifiers.
	if id, ok := left.Left.(Identifier); !ok || id.Name != "a" {
		t.Errorf("left.Left = %#v, want Identifier{a}", left.Left)
	}
	if id, ok := left.Right.(Identifier); !ok || id.Name != "b" {
		t.Errorf("left.Right = %#v, want Identifier{b}", left.Right)
	}
	if id, ok := right.Left.(Identifier); !ok || id.Name != "c" {
		t.Errorf("right.Left = %#v, want Identifier{c}", right.Left)
	}
	if id, ok := right.Right.(Identifier); !ok || id.Name != "d" {
		t.Errorf("right.Right = %#v, want Identifier{d}", right.Right)
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
	// "(1 + 2) * 3" must regroup so that the root is the multiplication
	// whose left child is the parenthesized addition. A parser that
	// ignored parens would build "1 + (2 * 3)" with + at the root.
	expr, err := NewParser("(1 + 2) * 3").Parse()
	if err != nil {
		t.Fatal(err)
	}
	root, ok := expr.(BinaryOp)
	if !ok {
		t.Fatalf("expected BinaryOp at root, got %T", expr)
	}
	if root.Op != "*" {
		t.Fatalf("root Op = %q, want * (parens must regroup the addition)", root.Op)
	}
	left, ok := root.Left.(BinaryOp)
	if !ok || left.Op != "+" {
		t.Errorf("root.Left = %#v, want BinaryOp{Op:\"+\"}", root.Left)
	}
}

func TestParseError(t *testing.T) {
	_, err := NewParser("1 +").Parse()
	if err == nil {
		t.Error("expected error for incomplete expression")
	}
}
