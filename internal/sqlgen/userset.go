package sqlgen

// Userset operations encapsulate the complex string manipulation for userset handling.
// A userset ID has the format "object_id#relation" (e.g., "group:1#member").
// These types extract components from userset identifiers.

// UsersetObjectID extracts the object ID from a userset identifier.
// "group:1#member" -> "group:1"
// Uses: split_part(source, '#', 1)
type UsersetObjectID struct {
	Source Expr
}

// SQL renders the split_part expression.
func (u UsersetObjectID) SQL() string {
	return "split_part(" + u.Source.SQL() + ", '#', 1)"
}

// UsersetRelation extracts the relation from a userset identifier.
// "group:1#member" -> "member"
// Uses: split_part(source, '#', 2)
type UsersetRelation struct {
	Source Expr
}

// SQL renders the split_part expression.
func (u UsersetRelation) SQL() string {
	return "split_part(" + u.Source.SQL() + ", '#', 2)"
}

// HasUserset checks if an expression contains a userset marker (#).
// Returns true if the expression contains '#'.
// Uses: position('#' in source) > 0
type HasUserset struct {
	Source Expr
}

// SQL renders the position check expression.
func (h HasUserset) SQL() string {
	return "position('#' in " + h.Source.SQL() + ") > 0"
}

// NoUserset checks if an expression does NOT contain a userset marker (#).
// Returns true if the expression does not contain '#'.
// Uses: position('#' in source) = 0
type NoUserset struct {
	Source Expr
}

// SQL renders the position check expression.
func (n NoUserset) SQL() string {
	return "position('#' in " + n.Source.SQL() + ") = 0"
}

// SubstringUsersetRelation extracts the relation using substring/position.
// This variant is used when the input might already contain a userset marker
// and we need to extract just the relation part.
// Uses: substring(source from position('#' in source) + 1)
type SubstringUsersetRelation struct {
	Source Expr
}

// SQL renders the substring expression.
func (s SubstringUsersetRelation) SQL() string {
	return "substring(" + s.Source.SQL() + " from position('#' in " + s.Source.SQL() + ") + 1)"
}

// IsWildcard checks if an expression equals the wildcard value "*".
type IsWildcard struct {
	Source Expr
}

// SQL renders the wildcard check.
func (w IsWildcard) SQL() string {
	return w.Source.SQL() + " = '*'"
}

// SubjectIDMatch creates a condition for matching subject IDs.
// If allowWildcard is true, matches either exact ID or wildcard.
// If allowWildcard is false, matches exact ID and excludes wildcards.
func SubjectIDMatch(column, subjectID Expr, allowWildcard bool) Expr {
	if allowWildcard {
		return Or(
			Eq{Left: column, Right: subjectID},
			IsWildcard{Source: column},
		)
	}
	return And(
		Eq{Left: column, Right: subjectID},
		NotExpr{Expr: IsWildcard{Source: column}},
	)
}
