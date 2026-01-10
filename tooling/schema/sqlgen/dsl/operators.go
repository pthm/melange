package dsl

import (
	"strings"
)

// Comparison operators

// Eq represents an equality comparison (=).
type Eq struct {
	Left  Expr
	Right Expr
}

// SQL renders the equality comparison.
func (e Eq) SQL() string {
	return e.Left.SQL() + " = " + e.Right.SQL()
}

// Ne represents a not-equal comparison (<>).
type Ne struct {
	Left  Expr
	Right Expr
}

// SQL renders the not-equal comparison.
func (n Ne) SQL() string {
	return n.Left.SQL() + " <> " + n.Right.SQL()
}

// Lt represents a less-than comparison (<).
type Lt struct {
	Left  Expr
	Right Expr
}

// SQL renders the less-than comparison.
func (l Lt) SQL() string {
	return l.Left.SQL() + " < " + l.Right.SQL()
}

// Gt represents a greater-than comparison (>).
type Gt struct {
	Left  Expr
	Right Expr
}

// SQL renders the greater-than comparison.
func (g Gt) SQL() string {
	return g.Left.SQL() + " > " + g.Right.SQL()
}

// Lte represents a less-than-or-equal comparison (<=).
type Lte struct {
	Left  Expr
	Right Expr
}

// SQL renders the less-than-or-equal comparison.
func (l Lte) SQL() string {
	return l.Left.SQL() + " <= " + l.Right.SQL()
}

// Gte represents a greater-than-or-equal comparison (>=).
type Gte struct {
	Left  Expr
	Right Expr
}

// SQL renders the greater-than-or-equal comparison.
func (g Gte) SQL() string {
	return g.Left.SQL() + " >= " + g.Right.SQL()
}

// In represents an IN clause for string values.
type In struct {
	Expr   Expr
	Values []string
}

// SQL renders the IN clause.
func (i In) SQL() string {
	if len(i.Values) == 0 {
		return "FALSE"
	}
	quoted := make([]string, len(i.Values))
	for j, v := range i.Values {
		quoted[j] = Lit(v).SQL()
	}
	return i.Expr.SQL() + " IN (" + strings.Join(quoted, ", ") + ")"
}

// InExpr represents an IN clause with expression values.
type InExpr struct {
	Expr   Expr
	Values []Expr
}

// SQL renders the IN clause.
func (i InExpr) SQL() string {
	if len(i.Values) == 0 {
		return "FALSE"
	}
	vals := make([]string, len(i.Values))
	for j, v := range i.Values {
		vals[j] = v.SQL()
	}
	return i.Expr.SQL() + " IN (" + strings.Join(vals, ", ") + ")"
}

// Logical operators

// AndExpr represents a logical AND of multiple expressions.
type AndExpr struct {
	Exprs []Expr
}

// SQL renders the AND expression.
func (a AndExpr) SQL() string {
	if len(a.Exprs) == 0 {
		return "TRUE"
	}
	if len(a.Exprs) == 1 {
		return a.Exprs[0].SQL()
	}
	parts := make([]string, len(a.Exprs))
	for i, e := range a.Exprs {
		parts[i] = e.SQL()
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

// And creates an AND expression from multiple expressions.
func And(exprs ...Expr) AndExpr {
	// Filter out nil expressions
	filtered := make([]Expr, 0, len(exprs))
	for _, e := range exprs {
		if e != nil {
			filtered = append(filtered, e)
		}
	}
	return AndExpr{Exprs: filtered}
}

// OrExpr represents a logical OR of multiple expressions.
type OrExpr struct {
	Exprs []Expr
}

// SQL renders the OR expression.
func (o OrExpr) SQL() string {
	if len(o.Exprs) == 0 {
		return "FALSE"
	}
	if len(o.Exprs) == 1 {
		return o.Exprs[0].SQL()
	}
	parts := make([]string, len(o.Exprs))
	for i, e := range o.Exprs {
		parts[i] = e.SQL()
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// Or creates an OR expression from multiple expressions.
func Or(exprs ...Expr) OrExpr {
	// Filter out nil expressions
	filtered := make([]Expr, 0, len(exprs))
	for _, e := range exprs {
		if e != nil {
			filtered = append(filtered, e)
		}
	}
	return OrExpr{Exprs: filtered}
}

// NotExpr represents a logical NOT of an expression.
type NotExpr struct {
	Expr Expr
}

// SQL renders the NOT expression.
func (n NotExpr) SQL() string {
	return "NOT (" + n.Expr.SQL() + ")"
}

// Not creates a NOT expression.
func Not(expr Expr) NotExpr {
	return NotExpr{Expr: expr}
}

// Exists represents an EXISTS subquery.
type Exists struct {
	Query interface{ SQL() string }
}

// SQL renders the EXISTS expression.
func (e Exists) SQL() string {
	return "EXISTS (\n" + e.Query.SQL() + "\n)"
}

// NotExists represents a NOT EXISTS subquery.
type NotExists struct {
	Query interface{ SQL() string }
}

// SQL renders the NOT EXISTS expression.
func (n NotExists) SQL() string {
	return "NOT EXISTS (\n" + n.Query.SQL() + "\n)"
}

// IsNull represents IS NULL check.
type IsNull struct {
	Expr Expr
}

// SQL renders the IS NULL expression.
func (i IsNull) SQL() string {
	return i.Expr.SQL() + " IS NULL"
}

// IsNotNull represents IS NOT NULL check.
type IsNotNull struct {
	Expr Expr
}

// SQL renders the IS NOT NULL expression.
func (i IsNotNull) SQL() string {
	return i.Expr.SQL() + " IS NOT NULL"
}
