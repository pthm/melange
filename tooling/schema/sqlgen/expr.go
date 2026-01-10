package sqlgen

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

// Position represents the SQL POSITION function.
// Example: Position{Substring: "'#'", In: Col{Column: "subject_id"}}
// Renders: position('#' in subject_id)
type Position struct {
	Substring Expr // What to search for
	In        Expr // Where to search
}

// SQL renders the POSITION function.
func (p Position) SQL() string {
	return fmt.Sprintf("position(%s in %s)", p.Substring.SQL(), p.In.SQL())
}

// Substring represents the SQL SUBSTRING function.
// Supports two forms:
// - Substring{Expr, From, nil} -> substring(expr from start)
// - Substring{Expr, From, For} -> substring(expr from start for length)
type Substring struct {
	Source Expr // The source string
	From   Expr // Start position (1-based)
	For    Expr // Optional length (nil means to end)
}

// SQL renders the SUBSTRING function.
func (s Substring) SQL() string {
	if s.For != nil {
		return fmt.Sprintf("substring(%s from %s for %s)", s.Source.SQL(), s.From.SQL(), s.For.SQL())
	}
	return fmt.Sprintf("substring(%s from %s)", s.Source.SQL(), s.From.SQL())
}

// SplitPart represents the SQL split_part function.
// Example: SplitPart{String: Col{Column: "subject_id"}, Delimiter: Lit("#"), Part: Int(1)}
// Renders: split_part(subject_id, '#', 1)
type SplitPart struct {
	String    Expr // The string to split
	Delimiter Expr // The delimiter
	Part      Expr // Which part (1-based)
}

// SQL renders the split_part function.
func (s SplitPart) SQL() string {
	return fmt.Sprintf("split_part(%s, %s, %s)", s.String.SQL(), s.Delimiter.SQL(), s.Part.SQL())
}

// =============================================================================
// Arithmetic Expressions
// =============================================================================

// Add represents addition (+).
type Add struct {
	Left  Expr
	Right Expr
}

// SQL renders the addition.
func (a Add) SQL() string {
	return fmt.Sprintf("%s + %s", a.Left.SQL(), a.Right.SQL())
}

// Sub represents subtraction (-).
type Sub struct {
	Left  Expr
	Right Expr
}

// SQL renders the subtraction.
func (s Sub) SQL() string {
	return fmt.Sprintf("%s - %s", s.Left.SQL(), s.Right.SQL())
}

// =============================================================================
// Userset String Helpers
// =============================================================================

// NormalizedUsersetSubject creates a normalized userset subject reference.
// Takes the object_id from subjectID and combines with the given relation.
// Example: NormalizedUsersetSubject(Col{Column: "subject_id"}, Raw("v_filter_relation"))
// SQL: split_part(subject_id, '#', 1) || '#' || v_filter_relation
func NormalizedUsersetSubject(subjectID Expr, relation Expr) Expr {
	return Concat{Parts: []Expr{
		UsersetObjectID{Source: subjectID},
		Lit("#"),
		relation,
	}}
}
