package sqldsl

import "strings"

// ArrayLiteral represents a SQL array literal: ARRAY[values].
type ArrayLiteral struct {
	Values []Expr
}

// SQL renders the array literal.
func (a ArrayLiteral) SQL() string {
	if len(a.Values) == 0 {
		return "ARRAY[]::TEXT[]"
	}
	parts := make([]string, len(a.Values))
	for i, v := range a.Values {
		parts[i] = v.SQL()
	}
	return "ARRAY[" + strings.Join(parts, ", ") + "]"
}

// ArrayAppend represents array concatenation: arr || ARRAY[values].
// Used for building the visited array in recursive check functions.
//
// Example:
//
//	ArrayAppend{
//	    Array:  Param("p_visited"),
//	    Values: []Expr{Concat{Parts: []Expr{Lit("doc:"), ObjectID, Lit(":viewer")}}},
//	}
//
// Renders: p_visited || ARRAY['doc:' || p_object_id || ':viewer']
type ArrayAppend struct {
	Array  Expr   // The base array expression
	Values []Expr // Values to append (rendered as ARRAY[...])
}

// SQL renders the array concatenation.
func (a ArrayAppend) SQL() string {
	valParts := make([]string, len(a.Values))
	for i, v := range a.Values {
		valParts[i] = v.SQL()
	}
	return a.Array.SQL() + " || ARRAY[" + strings.Join(valParts, ", ") + "]"
}

// VisitedKey creates the standard visited key expression for cycle detection.
// The key format is: 'objectType:' || objectID || ':relation'
//
// Example:
//
//	VisitedKey("document", "viewer", ObjectID)
//
// Renders the expression: 'document:' || p_object_id || ':viewer'
func VisitedKey(objectType, relation string, objectID Expr) Expr {
	return Concat{Parts: []Expr{
		Lit(objectType + ":"),
		objectID,
		Lit(":" + relation),
	}}
}

// VisitedWithKey creates the p_visited || ARRAY[key] expression.
// This is the standard pattern for passing updated visited arrays to recursive calls.
//
// Example:
//
//	VisitedWithKey("document", "viewer", ObjectID)
//
// Renders: p_visited || ARRAY['document:' || p_object_id || ':viewer']
func VisitedWithKey(objectType, relation string, objectID Expr) ArrayAppend {
	return ArrayAppend{
		Array:  Visited,
		Values: []Expr{VisitedKey(objectType, relation, objectID)},
	}
}

// VisitedKeyVar creates the v_key variable assignment expression.
// Used at the start of recursive functions: v_key := 'objectType:' || p_object_id || ':relation'
//
// Returns the right-hand side expression (without assignment).
func VisitedKeyVar(objectType, relation string, objectID Expr) Expr {
	return VisitedKey(objectType, relation, objectID)
}

// ArrayContains represents the ANY() check: value = ANY(array).
type ArrayContains struct {
	Value Expr
	Array Expr
}

// SQL renders the ANY check.
func (a ArrayContains) SQL() string {
	return a.Value.SQL() + " = ANY(" + a.Array.SQL() + ")"
}

// ArrayLength represents array_length(arr, dim).
type ArrayLength struct {
	Array     Expr
	Dimension int // Usually 1
}

// SQL renders the array_length call.
func (a ArrayLength) SQL() string {
	dim := a.Dimension
	if dim == 0 {
		dim = 1
	}
	return "array_length(" + a.Array.SQL() + ", " + Int(dim).SQL() + ")"
}
