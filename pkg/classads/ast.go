// Package classads provides ClassAd (classified advertisement) handling for Helix Cluster OS.
package classads

// Expr is the interface for all AST nodes.
type Expr interface {
	exprNode()
}

// Literal represents a constant value.
type Literal struct {
	Value interface{} // string, float64, or bool
}

func (Literal) exprNode() {}

// Identifier represents a variable reference.
type Identifier struct {
	Name string
}

func (Identifier) exprNode() {}

// BinaryOp represents a binary operation.
type BinaryOp struct {
	Op    string // ==, !=, <, <=, >, >=, &&, ||, +, -, *, /
	Left  Expr
	Right Expr
}

func (BinaryOp) exprNode() {}

// UnaryOp represents a unary operation.
type UnaryOp struct {
	Op   string // !, -
	Expr Expr
}

func (UnaryOp) exprNode() {}

// FunctionCall represents a function invocation.
type FunctionCall struct {
	Name string
	Args []Expr
}

func (FunctionCall) exprNode() {}
