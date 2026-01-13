package sqldsl

import (
	"strings"
)

// Comparison operators

// Eq represents an equality comparison (=).
type Eq struct {
	Left  Expr
	Right Expr
}

func (e Eq) SQL() string { return e.Left.SQL() + " = " + e.Right.SQL() }

// Ne represents a not-equal comparison (<>).
type Ne struct {
	Left  Expr
	Right Expr
}

func (n Ne) SQL() string { return n.Left.SQL() + " <> " + n.Right.SQL() }

// Lt represents a less-than comparison (<).
type Lt struct {
	Left  Expr
	Right Expr
}

func (l Lt) SQL() string { return l.Left.SQL() + " < " + l.Right.SQL() }

// Gt represents a greater-than comparison (>).
type Gt struct {
	Left  Expr
	Right Expr
}

func (g Gt) SQL() string { return g.Left.SQL() + " > " + g.Right.SQL() }

// Lte represents a less-than-or-equal comparison (<=).
type Lte struct {
	Left  Expr
	Right Expr
}

func (l Lte) SQL() string { return l.Left.SQL() + " <= " + l.Right.SQL() }

// Gte represents a greater-than-or-equal comparison (>=).
type Gte struct {
	Left  Expr
	Right Expr
}

func (g Gte) SQL() string { return g.Left.SQL() + " >= " + g.Right.SQL() }

// Arithmetic operators

// Add represents addition (+).
type Add struct {
	Left  Expr
	Right Expr
}

func (a Add) SQL() string { return a.Left.SQL() + " + " + a.Right.SQL() }

// Sub represents subtraction (-).
type Sub struct {
	Left  Expr
	Right Expr
}

func (s Sub) SQL() string { return s.Left.SQL() + " - " + s.Right.SQL() }

// quoteValues renders a slice of strings as quoted SQL literals.
func quoteValues(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = Lit(v).SQL()
	}
	return strings.Join(quoted, ", ")
}

// In represents an IN clause for string values.
type In struct {
	Expr   Expr
	Values []string
}

func (i In) SQL() string {
	if len(i.Values) == 0 {
		return "FALSE"
	}
	return i.Expr.SQL() + " IN (" + quoteValues(i.Values) + ")"
}

// NotIn represents a NOT IN clause for string values.
type NotIn struct {
	Expr   Expr
	Values []string
}

func (n NotIn) SQL() string {
	if len(n.Values) == 0 {
		return "TRUE"
	}
	return n.Expr.SQL() + " NOT IN (" + quoteValues(n.Values) + ")"
}

// Logical operators

// filterNilExprs removes nil expressions from the slice.
func filterNilExprs(exprs []Expr) []Expr {
	filtered := make([]Expr, 0, len(exprs))
	for _, e := range exprs {
		if e != nil {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// joinExprs renders expressions joined by a separator, wrapped in parentheses if more than one.
func joinExprs(exprs []Expr, sep, emptyVal string) string {
	switch len(exprs) {
	case 0:
		return emptyVal
	case 1:
		return exprs[0].SQL()
	default:
		parts := make([]string, len(exprs))
		for i, e := range exprs {
			parts[i] = e.SQL()
		}
		return "(" + strings.Join(parts, sep) + ")"
	}
}

// AndExpr represents a logical AND of multiple expressions.
type AndExpr struct {
	Exprs []Expr
}

func (a AndExpr) SQL() string { return joinExprs(a.Exprs, " AND ", "TRUE") }

// And creates an AND expression from multiple expressions.
func And(exprs ...Expr) AndExpr {
	return AndExpr{Exprs: filterNilExprs(exprs)}
}

// OrExpr represents a logical OR of multiple expressions.
type OrExpr struct {
	Exprs []Expr
}

func (o OrExpr) SQL() string { return joinExprs(o.Exprs, " OR ", "FALSE") }

// Or creates an OR expression from multiple expressions.
func Or(exprs ...Expr) OrExpr {
	return OrExpr{Exprs: filterNilExprs(exprs)}
}

// NotExpr represents a logical NOT of an expression.
type NotExpr struct {
	Expr Expr
}

func (n NotExpr) SQL() string { return "NOT (" + n.Expr.SQL() + ")" }

// Not creates a NOT expression.
func Not(expr Expr) NotExpr { return NotExpr{Expr: expr} }

// Exists represents an EXISTS subquery.
type Exists struct {
	Query interface{ SQL() string }
}

func (e Exists) SQL() string { return "EXISTS (\n" + e.Query.SQL() + "\n)" }

// NotExists represents a NOT EXISTS subquery.
type NotExists struct {
	Query interface{ SQL() string }
}

func (n NotExists) SQL() string { return "NOT EXISTS (\n" + n.Query.SQL() + "\n)" }

// ExistsExpr creates an Exists expression from a SelectStmt.
func ExistsExpr(stmt SelectStmt) Exists { return Exists{Query: stmt} }

// IsNull represents IS NULL check.
type IsNull struct {
	Expr Expr
}

func (i IsNull) SQL() string { return i.Expr.SQL() + " IS NULL" }

// IsNotNull represents IS NOT NULL check.
type IsNotNull struct {
	Expr Expr
}

func (i IsNotNull) SQL() string { return i.Expr.SQL() + " IS NOT NULL" }

// CaseWhen represents a single WHEN clause in a CASE expression.
type CaseWhen struct {
	Cond   Expr
	Result Expr
}

// CaseExpr represents a CASE expression with multiple WHEN clauses.
type CaseExpr struct {
	Whens []CaseWhen
	Else  Expr // optional default value
}

func (c CaseExpr) SQL() string {
	if len(c.Whens) == 0 {
		if c.Else != nil {
			return c.Else.SQL()
		}
		return "NULL"
	}

	var sb strings.Builder
	sb.WriteString("CASE")
	for _, w := range c.Whens {
		sb.WriteString("\n        WHEN ")
		sb.WriteString(w.Cond.SQL())
		sb.WriteString(" THEN ")
		sb.WriteString(w.Result.SQL())
	}
	if c.Else != nil {
		sb.WriteString("\n        ELSE ")
		sb.WriteString(c.Else.SQL())
	}
	sb.WriteString("\n    END")
	return sb.String()
}
