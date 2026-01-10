package dsl

// Exclusion builders for handling "but not" patterns in authorization rules.

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

	// References to use in exclusion predicates
	ObjectIDExpr    Expr
	SubjectTypeExpr Expr
	SubjectIDExpr   Expr

	// Exclusion types
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

// BuildPredicates returns the exclusion predicates as Expr slices.
func (c ExclusionConfig) BuildPredicates() []Expr {
	if !c.HasExclusions() {
		return nil
	}

	var predicates []Expr

	// Simple exclusions: NOT EXISTS (tuple with excluded relation)
	for _, rel := range c.SimpleExcludedRelations {
		excl := Tuples("excl").
			ObjectType(c.ObjectType).
			Relations(rel).
			Select("1").
			Where(
				Eq{Col{Table: "excl", Column: "object_id"}, c.ObjectIDExpr},
				Eq{Col{Table: "excl", Column: "subject_type"}, c.SubjectTypeExpr},
				Or(
					Eq{Col{Table: "excl", Column: "subject_id"}, c.SubjectIDExpr},
					IsWildcard{Col{Table: "excl", Column: "subject_id"}},
				),
			)
		predicates = append(predicates, NotExists{excl})
	}

	// Complex exclusions: check_permission_internal(...) = 0
	for _, rel := range c.ComplexExcludedRelations {
		predicates = append(predicates, CheckPermission{
			Subject: SubjectRef{
				Type: c.SubjectTypeExpr,
				ID:   c.SubjectIDExpr,
			},
			Relation: rel,
			Object: ObjectRef{
				Type: Lit(c.ObjectType),
				ID:   c.ObjectIDExpr,
			},
			ExpectAllow: false,
		})
	}

	// TTU exclusions: NOT EXISTS (link tuple where check_permission on linked object returns 1)
	for _, rel := range c.ExcludedParentRelations {
		linkQuery := Tuples("link").
			ObjectType(c.ObjectType).
			Relations(rel.LinkingRelation).
			Select("1").
			Where(
				Eq{Col{Table: "link", Column: "object_id"}, c.ObjectIDExpr},
				CheckPermission{
					Subject: SubjectRef{
						Type: c.SubjectTypeExpr,
						ID:   c.SubjectIDExpr,
					},
					Relation: rel.Relation,
					Object: ObjectRef{
						Type: Col{Table: "link", Column: "subject_type"},
						ID:   Col{Table: "link", Column: "subject_id"},
					},
					ExpectAllow: true,
				},
			)
		if len(rel.AllowedLinkingTypes) > 0 {
			linkQuery.WhereSubjectTypeIn(rel.AllowedLinkingTypes...)
		}
		predicates = append(predicates, NotExists{linkQuery})
	}

	// Intersection exclusions: NOT (all parts match)
	for _, group := range c.ExcludedIntersection {
		var parts []Expr
		for _, part := range group.Parts {
			if part.ParentRelation != nil {
				// TTU part: EXISTS (link tuple where check_permission returns 1)
				linkQuery := Tuples("link").
					ObjectType(c.ObjectType).
					Relations(part.ParentRelation.LinkingRelation).
					Select("1").
					Where(
						Eq{Col{Table: "link", Column: "object_id"}, c.ObjectIDExpr},
						CheckPermission{
							Subject: SubjectRef{
								Type: c.SubjectTypeExpr,
								ID:   c.SubjectIDExpr,
							},
							Relation: part.ParentRelation.Relation,
							Object: ObjectRef{
								Type: Col{Table: "link", Column: "subject_type"},
								ID:   Col{Table: "link", Column: "subject_id"},
							},
							ExpectAllow: true,
						},
					)
				if len(part.ParentRelation.AllowedLinkingTypes) > 0 {
					linkQuery.WhereSubjectTypeIn(part.ParentRelation.AllowedLinkingTypes...)
				}
				parts = append(parts, Exists{linkQuery})
			} else {
				// Direct check part
				partExpr := CheckPermission{
					Subject: SubjectRef{
						Type: c.SubjectTypeExpr,
						ID:   c.SubjectIDExpr,
					},
					Relation: part.Relation,
					Object: ObjectRef{
						Type: Lit(c.ObjectType),
						ID:   c.ObjectIDExpr,
					},
					ExpectAllow: true,
				}
				if part.ExcludedRelation != "" {
					// Add nested exclusion
					parts = append(parts, And(
						partExpr,
						CheckPermission{
							Subject: SubjectRef{
								Type: c.SubjectTypeExpr,
								ID:   c.SubjectIDExpr,
							},
							Relation: part.ExcludedRelation,
							Object: ObjectRef{
								Type: Lit(c.ObjectType),
								ID:   c.ObjectIDExpr,
							},
							ExpectAllow: false,
						},
					))
				} else {
					parts = append(parts, partExpr)
				}
			}
		}
		if len(parts) > 0 {
			predicates = append(predicates, Not(And(parts...)))
		}
	}

	return predicates
}

// SimpleExclusion creates a NOT EXISTS exclusion for a simple "but not" rule.
func SimpleExclusion(objectType, relation string, objectID, subjectType, subjectID Expr) Expr {
	excl := Tuples("excl").
		ObjectType(objectType).
		Relations(relation).
		Select("1").
		Where(
			Eq{Col{Table: "excl", Column: "object_id"}, objectID},
			Eq{Col{Table: "excl", Column: "subject_type"}, subjectType},
			Or(
				Eq{Col{Table: "excl", Column: "subject_id"}, subjectID},
				IsWildcard{Col{Table: "excl", Column: "subject_id"}},
			),
		)
	return NotExists{excl}
}
