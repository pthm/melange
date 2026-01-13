package sqlgen

// ExcludedParentRelation represents a TTU exclusion pattern.
type ExcludedParentRelation struct {
	Relation            string
	LinkingRelation     string
	AllowedLinkingTypes []string
}

// ExcludedIntersectionPart represents one part of an intersection exclusion.
type ExcludedIntersectionPart struct {
	Relation         string
	ExcludedRelation string
	ParentRelation   *ExcludedParentRelation
}

// ExcludedIntersectionGroup represents a complete intersection exclusion.
type ExcludedIntersectionGroup struct {
	Parts []ExcludedIntersectionPart
}

// ExclusionConfig holds all exclusion rules for a query.
type ExclusionConfig struct {
	ObjectType string

	ObjectIDExpr    Expr
	SubjectTypeExpr Expr
	SubjectIDExpr   Expr

	SimpleExcludedRelations  []string
	ComplexExcludedRelations []string
	ExcludedParentRelations  []ExcludedParentRelation
	ExcludedIntersection     []ExcludedIntersectionGroup
}

// HasExclusions returns true if any exclusion rules are configured.
func (c ExclusionConfig) HasExclusions() bool {
	return len(c.SimpleExcludedRelations) > 0 ||
		len(c.ComplexExcludedRelations) > 0 ||
		len(c.ExcludedParentRelations) > 0 ||
		len(c.ExcludedIntersection) > 0
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

// BuildPredicates returns the exclusion predicates as Expr slices.
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
func SimpleExclusion(objectType, relation string, objectID, subjectType, subjectID Expr) Expr {
	return simpleExclusionQuery(objectType, relation, objectID, subjectType, subjectID)
}
