package sqlgen

// ExcludedParentRelation represents a TTU exclusion pattern like "but not viewer from parent".
// The exclusion is satisfied when a tuple exists linking the object to a parent object
// that grants the excluded relation.
type ExcludedParentRelation struct {
	Relation            string   // The relation to check on the parent (e.g., "viewer")
	LinkingRelation     string   // The relation linking to the parent (e.g., "parent")
	AllowedLinkingTypes []string // Object types the linking relation can point to
}

// ExcludedIntersectionPart represents one part of an intersection exclusion.
// For "but not (editor and owner)", there would be two parts: one for editor, one for owner.
type ExcludedIntersectionPart struct {
	Relation         string                  // The relation to check
	ExcludedRelation string                  // Optional nested exclusion (e.g., "editor but not owner")
	ParentRelation   *ExcludedParentRelation // Optional TTU pattern in the intersection
}

// ExcludedIntersectionGroup represents a complete intersection exclusion like "but not (A and B)".
// Access is denied if ALL parts in the group are satisfied.
type ExcludedIntersectionGroup struct {
	Parts []ExcludedIntersectionPart
}

// ExclusionConfig holds all exclusion rules for a query.
// It classifies exclusions by complexity and generates appropriate SQL predicates.
type ExclusionConfig struct {
	ObjectType string // The object type being checked

	ObjectIDExpr    Expr // Expression for the object ID (typically a column or parameter)
	SubjectTypeExpr Expr // Expression for the subject type
	SubjectIDExpr   Expr // Expression for the subject ID

	// SimpleExcludedRelations can use direct tuple lookups (NOT EXISTS).
	SimpleExcludedRelations []string

	// ComplexExcludedRelations need check_permission_internal calls.
	ComplexExcludedRelations []string

	// ExcludedParentRelations represent TTU exclusions (e.g., "but not viewer from parent").
	ExcludedParentRelations []ExcludedParentRelation

	// ExcludedIntersection represents intersection exclusions (e.g., "but not (A and B)").
	ExcludedIntersection []ExcludedIntersectionGroup
}

// HasExclusions returns true if any exclusion rules are configured.
func (c ExclusionConfig) HasExclusions() bool {
	return len(c.SimpleExcludedRelations) > 0 ||
		len(c.ComplexExcludedRelations) > 0 ||
		len(c.ExcludedParentRelations) > 0 ||
		len(c.ExcludedIntersection) > 0
}

// CanUseCTEOptimization returns true if exclusions can use CTE-based optimization.
//
// Eligible when:
//   - Has simple exclusions (direct tuple lookups)
//   - No complex exclusions (require check_permission calls)
//   - No TTU exclusions (require parent traversal)
//   - No intersection exclusions (require AND logic)
//
// The optimization materializes excluded subjects once in a CTE, then uses a single
// LEFT JOIN...WHERE IS NULL instead of repeated NOT EXISTS predicates.
func (c ExclusionConfig) CanUseCTEOptimization() bool {
	return len(c.SimpleExcludedRelations) > 0 &&
		len(c.ComplexExcludedRelations) == 0 &&
		len(c.ExcludedParentRelations) == 0 &&
		len(c.ExcludedIntersection) == 0
}

func (c ExclusionConfig) subjectRef() SubjectRef {
	return SubjectRef{Type: c.SubjectTypeExpr, ID: c.SubjectIDExpr}
}

func (c ExclusionConfig) objectRef() ObjectRef {
	return ObjectRef{Type: Lit(c.ObjectType), ID: c.ObjectIDExpr}
}

func (c ExclusionConfig) checkPermission(relation string, obj ObjectRef, expectAllow bool) CheckPermission {
	return CheckPermission{
		Subject:     c.subjectRef(),
		Relation:    relation,
		Object:      obj,
		ExpectAllow: expectAllow,
	}
}

func (c ExclusionConfig) ttuLinkQuery(rel ExcludedParentRelation) *TupleQuery {
	linkedObject := ObjectRef{
		Type: Col{Table: "link", Column: "subject_type"},
		ID:   Col{Table: "link", Column: "subject_id"},
	}
	q := Tuples("link").
		ObjectType(c.ObjectType).
		Relations(rel.LinkingRelation).
		Select("1").
		Where(
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: c.ObjectIDExpr},
			c.checkPermission(rel.Relation, linkedObject, true),
		)
	if len(rel.AllowedLinkingTypes) > 0 {
		q.WhereSubjectTypeIn(rel.AllowedLinkingTypes...)
	}
	return q
}

// BuildPredicates converts exclusion rules into SQL predicates.
//
// Simple exclusions become NOT EXISTS subqueries checking for direct tuples.
// Complex exclusions become check_permission_internal(...) = 0 calls.
// TTU exclusions check for linking tuples where the parent grants the excluded relation.
// Intersection exclusions become NOT (part1 AND part2 AND ...) expressions.
//
// All predicates are returned as a slice that should be ANDed into the WHERE clause.
func (c ExclusionConfig) BuildPredicates() []Expr {
	if !c.HasExclusions() {
		return nil
	}

	var predicates []Expr

	for _, rel := range c.SimpleExcludedRelations {
		predicates = append(predicates, simpleExclusionQuery(
			c.ObjectType, rel, c.ObjectIDExpr, c.SubjectTypeExpr, c.SubjectIDExpr,
		))
	}

	for _, rel := range c.ComplexExcludedRelations {
		predicates = append(predicates, c.checkPermission(rel, c.objectRef(), false))
	}

	for _, rel := range c.ExcludedParentRelations {
		predicates = append(predicates, NotExists{Query: c.ttuLinkQuery(rel)})
	}

	for _, group := range c.ExcludedIntersection {
		if pred := c.buildIntersectionPredicate(group); pred != nil {
			predicates = append(predicates, pred)
		}
	}

	return predicates
}

func (c ExclusionConfig) buildIntersectionPredicate(group ExcludedIntersectionGroup) Expr {
	parts := make([]Expr, 0, len(group.Parts))
	for _, part := range group.Parts {
		parts = append(parts, c.buildIntersectionPart(part))
	}
	if len(parts) == 0 {
		return nil
	}
	return Not(And(parts...))
}

func (c ExclusionConfig) buildIntersectionPart(part ExcludedIntersectionPart) Expr {
	if part.ParentRelation != nil {
		return Exists{Query: c.ttuLinkQuery(*part.ParentRelation)}
	}

	partExpr := c.checkPermission(part.Relation, c.objectRef(), true)
	if part.ExcludedRelation == "" {
		return partExpr
	}

	return And(
		partExpr,
		c.checkPermission(part.ExcludedRelation, c.objectRef(), false),
	)
}

func simpleExclusionQuery(objectType, relation string, objectID, subjectType, subjectID Expr) NotExists {
	excl := Tuples("excl").
		ObjectType(objectType).
		Relations(relation).
		Select("1").
		Where(
			Eq{Left: Col{Table: "excl", Column: "object_id"}, Right: objectID},
			Eq{Left: Col{Table: "excl", Column: "subject_type"}, Right: subjectType},
			Or(
				Eq{Left: Col{Table: "excl", Column: "subject_id"}, Right: subjectID},
				IsWildcard{Source: Col{Table: "excl", Column: "subject_id"}},
			),
		)
	return NotExists{Query: excl}
}

// SimpleExclusion creates a NOT EXISTS exclusion for a simple "but not" rule.
// This checks for the absence of a tuple granting the excluded relation to the subject.
// Wildcards are handled: if a wildcard tuple exists for the excluded relation, access is denied.
func SimpleExclusion(objectType, relation string, objectID, subjectType, subjectID Expr) Expr {
	return simpleExclusionQuery(objectType, relation, objectID, subjectType, subjectID)
}

// BuildExclusionCTE builds a CTE that materializes all excluded subjects.
// Used for CTE-based exclusion optimization to precompute exclusions once
// instead of checking via NOT EXISTS for each result row.
//
// Returns a SELECT statement producing a single column: subject_id
//
// Example for "but not restricted" where restricted: [user]:
//
//	SELECT subject_id FROM melange_tuples
//	WHERE object_type = 'document'
//	  AND relation IN ('restricted')
//	  AND object_id = p_object_id
//
// Result is used in anti-join pattern:
//
//	LEFT JOIN excluded_subjects ON excluded_subjects.subject_id = candidate.subject_id
//	                              OR excluded_subjects.subject_id = '*'
//	WHERE excluded_subjects.subject_id IS NULL
func (c ExclusionConfig) BuildExclusionCTE() string {
	if len(c.SimpleExcludedRelations) == 0 {
		return "SELECT NULL::TEXT AS subject_id WHERE FALSE"
	}

	q := Tuples("").
		ObjectType(c.ObjectType).
		Relations(c.SimpleExcludedRelations...).
		Where(
			Eq{Left: Col{Column: "object_id"}, Right: c.ObjectIDExpr},
		).
		Select("subject_id")

	return q.Build().SQL()
}
