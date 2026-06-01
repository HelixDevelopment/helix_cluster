package classads

import (
	"fmt"
	"regexp"
	"strconv"
)

// Eval evaluates an expression against a set of attributes.
func Eval(expr string, attrs map[string]interface{}) (interface{}, error) {
	p := NewParser(expr)
	ast, err := p.Parse()
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}
	return evalExpr(ast, attrs)
}

// Match evaluates a requirements expression against attributes and returns true if they match.
func Match(requirements string, attrs map[string]interface{}) (bool, error) {
	result, err := Eval(requirements, attrs)
	if err != nil {
		return false, err
	}
	b, ok := toBool(result)
	if !ok {
		// unreachable via public string-input Match API: the parser only
		// produces Literals with bool/float64/string values, all coercible.
		// Reachable only via evalExpr called directly with a synthetic Literal
		// whose Value is a non-coercible type (e.g. nil, []byte).
		return false, fmt.Errorf("match expression did not evaluate to bool, got %T", result)
	}
	return b, nil
}

func evalExpr(e Expr, attrs map[string]interface{}) (interface{}, error) {
	switch n := e.(type) {
	case Literal:
		return n.Value, nil
	case Identifier:
		v, ok := attrs[n.Name]
		if !ok {
			return nil, fmt.Errorf("undefined attribute: %s", n.Name)
		}
		return v, nil
	case UnaryOp:
		v, err := evalExpr(n.Expr, attrs)
		if err != nil {
			return nil, err
		}
		switch n.Op {
		case "!":
			b, ok := toBool(v)
			if !ok {
				// unreachable via public Eval API: every value producible by
				// evalExpr (bool, float64, string) is accepted by toBool.
				// Reachable only via evalExpr called directly with a synthetic
				// Literal whose Value is a non-coercible type (e.g. nil).
				return nil, fmt.Errorf("! requires bool, got %T", v)
			}
			return !b, nil
		case "-":
			f, ok := toFloat(v)
			if !ok {
				return nil, fmt.Errorf("- requires number, got %T", v)
			}
			return -f, nil
		default:
			return nil, fmt.Errorf("unknown unary op: %s", n.Op)
		}
	case BinaryOp:
		left, err := evalExpr(n.Left, attrs)
		if err != nil {
			return nil, err
		}
		// Short-circuit for && and ||
		if n.Op == "&&" {
			lb, ok := toBool(left)
			if !ok {
				return nil, fmt.Errorf("&& requires bool, got %T", left)
			}
			if !lb {
				return false, nil
			}
			right, err := evalExpr(n.Right, attrs)
			if err != nil {
				return nil, err
			}
			rb, ok := toBool(right)
			if !ok {
				return nil, fmt.Errorf("&& requires bool, got %T", right)
			}
			return rb, nil
		}
		if n.Op == "||" {
			lb, ok := toBool(left)
			if !ok {
				return nil, fmt.Errorf("|| requires bool, got %T", left)
			}
			if lb {
				return true, nil
			}
			right, err := evalExpr(n.Right, attrs)
			if err != nil {
				return nil, err
			}
			rb, ok := toBool(right)
			if !ok {
				return nil, fmt.Errorf("|| requires bool, got %T", right)
			}
			return rb, nil
		}
		right, err := evalExpr(n.Right, attrs)
		if err != nil {
			return nil, err
		}
		return evalBinary(n.Op, left, right)
	case FunctionCall:
		return evalFunction(n, attrs)
	default:
		return nil, fmt.Errorf("unknown expression type: %T", e)
	}
}

func evalBinary(op string, left, right interface{}) (interface{}, error) {
	switch op {
	case "+", "-", "*", "/":
		lf, ok1 := toFloat(left)
		rf, ok2 := toFloat(right)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%s requires numbers, got %T and %T", op, left, right)
		}
		switch op {
		case "+":
			return lf + rf, nil
		case "-":
			return lf - rf, nil
		case "*":
			return lf * rf, nil
		case "/":
			if rf == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return lf / rf, nil
		}
	case "==", "!=":
		eq := valuesEqual(left, right)
		if op == "==" {
			return eq, nil
		}
		return !eq, nil
	case "<", "<=", ">", ">=":
		lf, ok1 := toFloat(left)
		rf, ok2 := toFloat(right)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%s requires numbers, got %T and %T", op, left, right)
		}
		switch op {
		case "<":
			return lf < rf, nil
		case "<=":
			return lf <= rf, nil
		case ">":
			return lf > rf, nil
		case ">=":
			return lf >= rf, nil
		}
	}
	return nil, fmt.Errorf("unknown binary op: %s", op)
}

func evalFunction(fc FunctionCall, attrs map[string]interface{}) (interface{}, error) {
	args := make([]interface{}, len(fc.Args))
	for i, a := range fc.Args {
		v, err := evalExpr(a, attrs)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	switch fc.Name {
	case "regexp":
		if len(args) != 2 {
			return nil, fmt.Errorf("regexp requires 2 args, got %d", len(args))
		}
		pattern, ok1 := args[0].(string)
		target, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("regexp requires string args")
		}
		matched, err := regexp.MatchString(pattern, target)
		if err != nil {
			return nil, fmt.Errorf("invalid regexp: %w", err)
		}
		return matched, nil
	default:
		return nil, fmt.Errorf("unknown function: %s", fc.Name)
	}
}

func toBool(v interface{}) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case float64:
		return b != 0, true
	case string:
		return b != "", true
	default:
		return false, false
	}
}

func toFloat(v interface{}) (float64, bool) {
	switch f := v.(type) {
	case float64:
		return f, true
	case int:
		return float64(f), true
	case int64:
		return float64(f), true
	case string:
		ff, err := strconv.ParseFloat(f, 64)
		if err == nil {
			return ff, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func valuesEqual(a, b interface{}) bool {
	switch av := a.(type) {
	case float64:
		bv, ok := toFloat(b)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
}
