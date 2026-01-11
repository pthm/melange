// Package sqldsl provides a domain-specific SQL DSL for generating Melange authorization queries.
// It models authorization concepts directly rather than generic SQL syntax.
package sqldsl

import (
	"fmt"
	"strings"
)

// Expr is the interface that all SQL expression types implement.
type Expr interface {
	SQL() string
}

// Param represents a function parameter (e.g., p_subject_type, p_object_id).
type Param string

// SQL renders the parameter.
func (p Param) SQL() string {
	return string(p)
}

// Common parameter constants.
var (
	SubjectType = Param("p_subject_type")
	SubjectID   = Param("p_subject_id")
	ObjectType  = Param("p_object_type")
	ObjectID    = Param("p_object_id")
	Visited     = Param("p_visited")
)

// ParamRef creates a Param from a variable name.
// Use this for local PL/pgSQL variables (e.g., "v_filter_type", "v_filter_relation").
func ParamRef(name string) Param {
	return Param(name)
}

// LitText creates a Lit from a string value.
// This is an explicit alias for Lit, useful when the intent is to create a text literal.
// Example: LitText("document") renders as 'document'
func LitText(v string) Lit {
	return Lit(v)
}

// Col represents a table column reference (e.g., t.object_id).
type Col struct {
	Table  string
	Column string
}

// SQL renders the column reference.
func (c Col) SQL() string {
	if c.Table == "" {
		return c.Column
	}
	return c.Table + "." + c.Column
}

// Lit represents a literal string value (auto-quoted with single quotes).
type Lit string

// SQL renders the literal with single quotes.
func (l Lit) SQL() string {
	// Escape single quotes by doubling them
	escaped := strings.ReplaceAll(string(l), "'", "''")
	return "'" + escaped + "'"
}

// Raw is an escape hatch for arbitrary SQL expressions.
type Raw string

// SQL renders the raw SQL as-is.
func (r Raw) SQL() string {
	return string(r)
}

// Int represents an integer literal.
type Int int

// SQL renders the integer.
func (i Int) SQL() string {
	return fmt.Sprintf("%d", i)
}

// Bool represents a boolean literal.
type Bool bool

// SQL renders the boolean.
func (b Bool) SQL() string {
	if b {
		return "TRUE"
	}
	return "FALSE"
}

// Null represents SQL NULL.
type Null struct{}

// SQL renders NULL.
func (Null) SQL() string {
	return "NULL"
}

// EmptyArray represents an empty text array (ARRAY[]::TEXT[]).
type EmptyArray struct{}

// SQL renders the empty array.
func (EmptyArray) SQL() string {
	return "ARRAY[]::TEXT[]"
}

// Func represents a SQL function call.
type Func struct {
	Name string
	Args []Expr
}

// SQL renders the function call.
func (f Func) SQL() string {
	args := make([]string, len(f.Args))
	for i, arg := range f.Args {
		args[i] = arg.SQL()
	}
	return f.Name + "(" + strings.Join(args, ", ") + ")"
}

// Alias wraps an expression with an alias (expr AS alias).
type Alias struct {
	Expr Expr
	Name string
}

// SQL renders the aliased expression.
func (a Alias) SQL() string {
	return a.Expr.SQL() + " AS " + a.Name
}

// Paren wraps an expression in parentheses.
type Paren struct {
	Expr Expr
}

// SQL renders the parenthesized expression.
func (p Paren) SQL() string {
	return "(" + p.Expr.SQL() + ")"
}

// =============================================================================
// String Functions
// =============================================================================

// Concat represents SQL string concatenation (||).
type Concat struct {
	Parts []Expr
}

// SQL renders the concatenation.
func (c Concat) SQL() string {
	if len(c.Parts) == 0 {
		return "''"
	}
	parts := make([]string, len(c.Parts))
	for i, p := range c.Parts {
		parts[i] = p.SQL()
	}
	return strings.Join(parts, " || ")
}

// Position represents SQL position(needle in haystack).
// Returns the position of the first occurrence of needle in haystack.
type Position struct {
	Needle   Expr
	Haystack Expr
}

// SQL renders the position expression.
func (p Position) SQL() string {
	return "position(" + p.Needle.SQL() + " in " + p.Haystack.SQL() + ")"
}

// Substring represents SQL substring(source from start [for length]).
// If For is nil, renders substring(source from start).
// If For is provided, renders substring(source from start for length).
type Substring struct {
	Source Expr
	From   Expr
	For    Expr // optional
}

// SQL renders the substring expression.
func (s Substring) SQL() string {
	if s.For == nil {
		return "substring(" + s.Source.SQL() + " from " + s.From.SQL() + ")"
	}
	return "substring(" + s.Source.SQL() + " from " + s.From.SQL() + " for " + s.For.SQL() + ")"
}

// UsersetNormalized extracts the object ID from a userset and combines with a new relation.
// Renders: substring(source from 1 for position('#' in source) - 1) || '#' || relation
// Example: "group:1#admin" with relation "member" -> "group:1#member"
// This is used to normalize userset subjects to a specific relation.
type UsersetNormalized struct {
	Source   Expr
	Relation Expr
}

// SQL renders the userset normalization expression.
func (u UsersetNormalized) SQL() string {
	// substring(source from 1 for position('#' in source) - 1) || '#' || relation
	posExpr := "position('#' in " + u.Source.SQL() + ")"
	objectID := "substring(" + u.Source.SQL() + " from 1 for " + posExpr + " - 1)"
	return objectID + " || '#' || " + u.Relation.SQL()
}

// =============================================================================
// Userset String Helpers
// =============================================================================

// NormalizedUsersetSubject creates a normalized userset subject reference.
// Takes the object_id from subjectID and combines with the given relation.
// Example: NormalizedUsersetSubject(Col{Column: "subject_id"}, Raw("v_filter_relation"))
// SQL: split_part(subject_id, '#', 1) || '#' || v_filter_relation
func NormalizedUsersetSubject(subjectID, relation Expr) Expr {
	return Concat{Parts: []Expr{
		UsersetObjectID{Source: subjectID},
		Lit("#"),
		relation,
	}}
}

// =============================================================================
// Column Expression Helpers
// =============================================================================

// SelectAs creates an aliased column expression (expr AS alias).
// Shorthand for Alias{Expr: expr, Name: alias}.
func SelectAs(expr Expr, alias string) Alias {
	return Alias{Expr: expr, Name: alias}
}
