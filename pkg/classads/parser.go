package classads

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// maxParseDepth bounds recursive-descent nesting. The grammar recurses through
// parseOr (each parenthesized sub-expression) and parseUnary (each chained !/-),
// neither of which had a depth limit — so a deeply nested hostile expression
// (e.g. ~500k nested parens, a ~1 MB string) drove the goroutine stack past Go's
// 1 GB limit and aborted the process with an UNRECOVERABLE `fatal error: stack
// overflow` — a remotely-triggerable DoS via the public Eval/Parse API. The cap
// is far above any legitimate expression (hundreds of levels) yet well below the
// depth that overflows the stack, so pathological input is rejected with a clean
// error instead of crashing. It also transitively bounds AST depth, keeping the
// recursive evalExpr safe for anything Parse can produce.
const maxParseDepth = 10000

// Parser parses ClassAd expression strings into an AST.
type Parser struct {
	input string
	pos   int
	depth int // current recursion depth, bounded by maxParseDepth
}

// NewParser creates a new parser for the given input.
func NewParser(input string) *Parser {
	return &Parser{input: input}
}

// Parse parses the input into an Expr AST.
func (p *Parser) Parse() (Expr, error) {
	p.skipWhitespace()
	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	p.skipWhitespace()
	if p.pos < len(p.input) {
		return nil, fmt.Errorf("unexpected token at position %d: %q", p.pos, p.peek())
	}
	return expr, nil
}

func (p *Parser) peek() byte {
	if p.pos >= len(p.input) {
		return 0
	}
	return p.input[p.pos]
}

func (p *Parser) advance() byte {
	ch := p.peek()
	p.pos++
	return ch
}

func (p *Parser) skipWhitespace() {
	for p.pos < len(p.input) && unicode.IsSpace(rune(p.peek())) {
		p.pos++
	}
}

func (p *Parser) consume(s string) bool {
	p.skipWhitespace()
	if strings.HasPrefix(p.input[p.pos:], s) {
		p.pos += len(s)
		return true
	}
	return false
}

func (p *Parser) parseOr() (Expr, error) {
	if p.depth >= maxParseDepth {
		return nil, fmt.Errorf("classads: expression nesting exceeds depth limit %d (possible denial-of-service input)", maxParseDepth)
	}
	p.depth++
	defer func() { p.depth-- }()

	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.consume("||") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = BinaryOp{Op: "||", Left: left, Right: right}
	}
	return left, nil
}

func (p *Parser) parseAnd() (Expr, error) {
	left, err := p.parseEquality()
	if err != nil {
		return nil, err
	}
	for p.consume("&&") {
		right, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		left = BinaryOp{Op: "&&", Left: left, Right: right}
	}
	return left, nil
}

func (p *Parser) parseEquality() (Expr, error) {
	left, err := p.parseRelational()
	if err != nil {
		return nil, err
	}
	for {
		if p.consume("==") {
			right, err := p.parseRelational()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: "==", Left: left, Right: right}
		} else if p.consume("!=") {
			right, err := p.parseRelational()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: "!=", Left: left, Right: right}
		} else {
			break
		}
	}
	return left, nil
}

func (p *Parser) parseRelational() (Expr, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		if p.consume("<=") {
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: "<=", Left: left, Right: right}
		} else if p.consume(">=") {
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: ">=", Left: left, Right: right}
		} else if p.consume("<") {
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: "<", Left: left, Right: right}
		} else if p.consume(">") {
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: ">", Left: left, Right: right}
		} else {
			break
		}
	}
	return left, nil
}

func (p *Parser) parseAdditive() (Expr, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		if p.consume("+") {
			right, err := p.parseMultiplicative()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: "+", Left: left, Right: right}
		} else if p.consume("-") {
			right, err := p.parseMultiplicative()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: "-", Left: left, Right: right}
		} else {
			break
		}
	}
	return left, nil
}

func (p *Parser) parseMultiplicative() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		if p.consume("*") {
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: "*", Left: left, Right: right}
		} else if p.consume("/") {
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = BinaryOp{Op: "/", Left: left, Right: right}
		} else {
			break
		}
	}
	return left, nil
}

func (p *Parser) parseUnary() (Expr, error) {
	if p.depth >= maxParseDepth {
		return nil, fmt.Errorf("classads: expression nesting exceeds depth limit %d (possible denial-of-service input)", maxParseDepth)
	}
	p.depth++
	defer func() { p.depth-- }()

	p.skipWhitespace()
	if p.consume("!") {
		expr, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return UnaryOp{Op: "!", Expr: expr}, nil
	}
	if p.consume("-") {
		expr, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return UnaryOp{Op: "-", Expr: expr}, nil
	}
	return p.parsePrimary()
}

func (p *Parser) parsePrimary() (Expr, error) {
	p.skipWhitespace()
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("unexpected end of input")
	}

	ch := p.peek()

	// String literal
	if ch == '"' {
		return p.parseString()
	}

	// Number literal
	if ch == '.' || (ch >= '0' && ch <= '9') {
		return p.parseNumber()
	}

	// Boolean literals
	if strings.HasPrefix(p.input[p.pos:], "true") && (p.pos+4 >= len(p.input) || !isIdentChar(p.input[p.pos+4])) {
		p.pos += 4
		return Literal{Value: true}, nil
	}
	if strings.HasPrefix(p.input[p.pos:], "false") && (p.pos+5 >= len(p.input) || !isIdentChar(p.input[p.pos+5])) {
		p.pos += 5
		return Literal{Value: false}, nil
	}

	// Parenthesized expression
	if p.consume("(") {
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.consume(")") {
			return nil, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		return expr, nil
	}

	// Identifier or function call
	if isIdentStart(ch) {
		name := p.parseIdentifier()
		p.skipWhitespace()
		if p.consume("(") {
			args, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			if !p.consume(")") {
				return nil, fmt.Errorf("expected ')' after function arguments at position %d", p.pos)
			}
			return FunctionCall{Name: name, Args: args}, nil
		}
		return Identifier{Name: name}, nil
	}

	return nil, fmt.Errorf("unexpected character %q at position %d", ch, p.pos)
}

func (p *Parser) parseString() (Expr, error) {
	p.advance() // consume opening quote
	start := p.pos
	for p.pos < len(p.input) && p.peek() != '"' {
		if p.peek() == '\\' && p.pos+1 < len(p.input) {
			p.pos += 2
		} else {
			p.pos++
		}
	}
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("unterminated string literal")
	}
	value := p.input[start:p.pos]
	p.advance() // consume closing quote
	return Literal{Value: value}, nil
}

func (p *Parser) parseNumber() (Expr, error) {
	start := p.pos
	hasDot := false
	for p.pos < len(p.input) && ((p.peek() >= '0' && p.peek() <= '9') || p.peek() == '.') {
		if p.peek() == '.' {
			if hasDot {
				break
			}
			hasDot = true
		}
		p.pos++
	}
	s := p.input[start:p.pos]
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q: %w", s, err)
	}
	return Literal{Value: v}, nil
}

func (p *Parser) parseIdentifier() string {
	start := p.pos
	for p.pos < len(p.input) && isIdentChar(p.input[p.pos]) {
		p.pos++
	}
	return p.input[start:p.pos]
}

func (p *Parser) parseArgs() ([]Expr, error) {
	var args []Expr
	p.skipWhitespace()
	if p.peek() == ')' {
		return args, nil
	}
	for {
		arg, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		p.skipWhitespace()
		if !p.consume(",") {
			break
		}
	}
	return args, nil
}

func isIdentStart(ch byte) bool {
	return unicode.IsLetter(rune(ch)) || ch == '_'
}

func isIdentChar(ch byte) bool {
	return unicode.IsLetter(rune(ch)) || unicode.IsDigit(rune(ch)) || ch == '_'
}
