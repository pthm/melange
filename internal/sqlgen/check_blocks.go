package sqlgen

import "fmt"

// =============================================================================
// Check Blocks Layer
// =============================================================================
//
// This file implements the Blocks layer for check function generation.
// The Blocks layer builds DSL expressions for permission checking.
//
// Unlike list functions which use UNION'd query blocks, check functions use
// DSL expressions in PL/pgSQL IF statements. The "blocks" here are the
// individual check expressions that get composed in the render layer.
//
// Architecture: Plan → Blocks → Render
// - Plan: compute flags and normalized inputs
// - Blocks: build typed DSL expressions (this file)
// - Render: produce SQL/PLpgSQL strings

// CheckBlocks contains all the DSL blocks needed to generate a check function.
// Each block is an Expr that can be rendered to SQL.
type CheckBlocks struct {
	// DirectCheck is the EXISTS check for direct tuple lookup.
	// Returns true if there's a direct or implied tuple grant.
	DirectCheck Expr

	// UsersetCheck is the EXISTS check for userset membership.
	// Returns true if access is granted through a userset pattern.
	UsersetCheck Expr

	// ExclusionCheck is the EXISTS check for exclusion (denial).
	// Returns true if access should be denied due to exclusion.
	ExclusionCheck Expr

	// UsersetSubjectSelfCheck validates when the subject IS a userset.
	// Used for self-referential userset validation.
	UsersetSubjectSelfCheck SelectStmt

	// UsersetSubjectComputedCheck validates computed userset subject matching.
	// Used when the subject is a userset that requires closure validation.
	UsersetSubjectComputedCheck SelectStmt

	// ParentRelationBlocks contains checks for TTU (tuple-to-userset) patterns.
	// Each check traverses through a parent relation.
	ParentRelationBlocks []ParentRelationBlock

	// ImpliedFunctionCalls contains checks for complex implied relations.
	// These require calling specialized check functions.
	ImpliedFunctionCalls []ImpliedFunctionCheck

	// IntersectionGroups contains AND groups for intersection patterns.
	// Each group is a set of checks that must all pass.
	IntersectionGroups []IntersectionGroupCheck

	// HasStandaloneAccess indicates if there are access paths outside intersections.
	HasStandaloneAccess bool
}

// ParentRelationBlock represents a TTU check through a parent relation.
type ParentRelationBlock struct {
	LinkingRelation     string   // The linking relation (e.g., "parent")
	ParentRelation      string   // The relation to check on the parent
	AllowedLinkingTypes []string // Types allowed for the linking relation
	Query               SelectStmt
}

// ImpliedFunctionCheck represents a check that calls another check function.
type ImpliedFunctionCheck struct {
	Relation     string // The implied relation to check
	FunctionName string // The function to call
	Check        Expr   // The check expression
}

// IntersectionGroupCheck represents an AND group where all checks must pass.
type IntersectionGroupCheck struct {
	Parts []IntersectionPartCheck
}

// IntersectionPartCheck represents one part of an intersection check.
type IntersectionPartCheck struct {
	Relation         string // The relation being checked
	ExcludedRelation string // Optional: excluded relation (for "but not" in intersection)
	IsParent         bool   // True if this is a TTU part
	ParentRelation   string // For TTU: the parent relation
	LinkingRelation  string // For TTU: the linking relation
	Check            Expr   // The check expression
}

// BuildCheckBlocks builds all DSL blocks for a check function.
func BuildCheckBlocks(plan CheckPlan) (CheckBlocks, error) {
	var blocks CheckBlocks

	// Build direct check
	if plan.HasDirect || plan.HasImplied {
		directCheck, err := buildTypedDirectCheck(plan)
		if err != nil {
			return CheckBlocks{}, err
		}
		blocks.DirectCheck = directCheck
	}

	// Build userset check
	if plan.HasUserset {
		usersetCheck, err := buildTypedUsersetCheck(plan)
		if err != nil {
			return CheckBlocks{}, err
		}
		blocks.UsersetCheck = usersetCheck
	}

	// Build exclusion check
	if plan.HasExclusion {
		exclusionCheck, err := buildTypedExclusionCheck(plan)
		if err != nil {
			return CheckBlocks{}, err
		}
		blocks.ExclusionCheck = exclusionCheck
	}

	// Build userset subject checks
	selfCheck, computedCheck, err := buildTypedUsersetSubjectChecks(plan)
	if err != nil {
		return CheckBlocks{}, err
	}
	blocks.UsersetSubjectSelfCheck = selfCheck
	blocks.UsersetSubjectComputedCheck = computedCheck

	// Build parent relation checks for TTU
	if plan.HasParentRelations {
		parentChecks, err := buildTypedParentRelationBlocks(plan)
		if err != nil {
			return CheckBlocks{}, err
		}
		blocks.ParentRelationBlocks = parentChecks
	}

	// Build implied function calls for complex closure
	if plan.HasImpliedFunctionCall {
		impliedCalls, err := buildTypedImpliedFunctionCalls(plan)
		if err != nil {
			return CheckBlocks{}, err
		}
		blocks.ImpliedFunctionCalls = impliedCalls
	}

	// Build intersection groups
	if plan.HasIntersection {
		intersectionGroups, err := buildTypedIntersectionGroups(plan)
		if err != nil {
			return CheckBlocks{}, err
		}
		blocks.IntersectionGroups = intersectionGroups
	}

	blocks.HasStandaloneAccess = plan.HasStandaloneAccess

	return blocks, nil
}

// =============================================================================
// Block Building Functions
// =============================================================================

// buildTypedDirectCheck builds the direct tuple lookup check as a DSL expression.
func buildTypedDirectCheck(plan CheckPlan) (Expr, error) {
	q := Tuples("").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Column: "subject_type"}, Values: plan.AllowedSubjectTypes},
			Eq{Left: Col{Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Column: "subject_id"}, SubjectID, plan.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return Exists{Query: q}, nil
}

// buildTypedUsersetCheck builds the userset membership check as a DSL expression.
func buildTypedUsersetCheck(plan CheckPlan) (Expr, error) {
	// Get userset patterns from analysis
	patterns := plan.Analysis.UsersetPatterns
	if len(patterns) == 0 {
		return nil, nil
	}

	var checks []Expr
	// Build visited expression that appends the current key: p_visited || ARRAY['type:' || p_object_id || ':relation']
	visitedWithKey := Param(fmt.Sprintf("p_visited || ARRAY['%s:' || p_object_id || ':%s']", plan.ObjectType, plan.Relation))

	for _, pattern := range patterns {
		if pattern.IsComplex {
			// Complex pattern: use check_permission_internal to verify membership.
			// This handles cases where the userset closure contains relations with
			// exclusions, usersets, TTU, or intersections.
			q := Tuples("grant_tuple").
				ObjectType(plan.ObjectType).
				Relations(plan.Relation).
				Where(
					Eq{Left: Col{Table: "grant_tuple", Column: "object_id"}, Right: ObjectID},
					Eq{Left: Col{Table: "grant_tuple", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
					HasUserset{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
					Eq{
						Left:  UsersetRelation{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
						Right: Lit(pattern.SubjectRelation),
					},
					// Use check_permission_internal to recursively verify membership
					CheckPermission{
						Subject:  SubjectParams(),
						Relation: pattern.SubjectRelation,
						Object: ObjectRef{
							Type: Lit(pattern.SubjectType),
							ID:   UsersetObjectID{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
						},
						Visited:     visitedWithKey, // Include visited key for cycle detection
						ExpectAllow: true,
					},
				).
				Select("1").
				Limit(1)
			checks = append(checks, Exists{Query: q})
		} else {
			// Simple pattern: use tuple JOIN for membership lookup.
			// Use the plan's relation for searching grant tuples, not pattern.SourceRelation.
			// For check functions, we search tuples like (object:id, relation, subject_type:id#subject_relation).
			q := Tuples("grant_tuple").
				ObjectType(plan.ObjectType).
				Relations(plan.Relation).
				Where(
					Eq{Left: Col{Table: "grant_tuple", Column: "object_id"}, Right: ObjectID},
					Eq{Left: Col{Table: "grant_tuple", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
					HasUserset{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
					Eq{
						Left:  UsersetRelation{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
						Right: Lit(pattern.SubjectRelation),
					},
				).
				JoinTuples("membership",
					Eq{Left: Col{Table: "membership", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
					Eq{
						Left:  Col{Table: "membership", Column: "object_id"},
						Right: UsersetObjectID{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
					},
					In{Expr: Col{Table: "membership", Column: "relation"}, Values: pattern.SatisfyingRelations},
					Eq{Left: Col{Table: "membership", Column: "subject_type"}, Right: SubjectType},
					SubjectIDMatch(Col{Table: "membership", Column: "subject_id"}, SubjectID, plan.AllowWildcard),
				).
				Select("1").
				Limit(1)
			checks = append(checks, Exists{Query: q})
		}
	}

	if len(checks) == 1 {
		return checks[0], nil
	}
	return Or(checks...), nil
}

// buildTypedExclusionCheck builds the exclusion check as a DSL expression.
// Returns an expression that evaluates to TRUE when the subject is excluded.
func buildTypedExclusionCheck(plan CheckPlan) (Expr, error) {
	if !plan.Exclusions.HasExclusions() {
		return nil, nil
	}

	var exclusionChecks []Expr

	// Simple exclusions: EXISTS (excluded tuple)
	for _, rel := range plan.Exclusions.SimpleExcludedRelations {
		q := Tuples("excl").
			ObjectType(plan.ObjectType).
			Relations(rel).
			Where(
				Eq{Left: Col{Table: "excl", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "excl", Column: "subject_type"}, Right: SubjectType},
				Or(
					Eq{Left: Col{Table: "excl", Column: "subject_id"}, Right: SubjectID},
					IsWildcard{Source: Col{Table: "excl", Column: "subject_id"}},
				),
			).
			Select("1").
			Limit(1)
		exclusionChecks = append(exclusionChecks, Exists{Query: q})
	}

	// Complex exclusions: check_permission_internal(...) = 1 with p_visited for cycle detection
	for _, rel := range plan.Exclusions.ComplexExcludedRelations {
		exclusionChecks = append(exclusionChecks, CheckPermission{
			Subject:     SubjectParams(),
			Relation:    rel,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			Visited:     Visited, // Use p_visited parameter for cycle detection
			ExpectAllow: true,
		})
	}

	// TTU exclusions: EXISTS (link tuple where check_permission on linked object returns 1)
	for _, rel := range plan.Exclusions.ExcludedParentRelations {
		linkQuery := Tuples("link").
			ObjectType(plan.ObjectType).
			Relations(rel.LinkingRelation).
			Where(
				Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
				CheckPermission{
					Subject:  SubjectParams(),
					Relation: rel.Relation,
					Object: ObjectRef{
						Type: Col{Table: "link", Column: "subject_type"},
						ID:   Col{Table: "link", Column: "subject_id"},
					},
					Visited:     Visited,
					ExpectAllow: true,
				},
			).
			Select("1").
			Limit(1)
		if len(rel.AllowedLinkingTypes) > 0 {
			linkQuery.WhereSubjectTypeIn(rel.AllowedLinkingTypes...)
		}
		exclusionChecks = append(exclusionChecks, Exists{Query: linkQuery})
	}

	// Intersection exclusions: (part1 AND part2 AND ...) - all parts must match
	for _, group := range plan.Exclusions.ExcludedIntersection {
		var parts []Expr
		for _, part := range group.Parts {
			switch {
			case part.ParentRelation != nil:
				// TTU part: EXISTS (link tuple where check_permission returns 1)
				linkQuery := Tuples("link").
					ObjectType(plan.ObjectType).
					Relations(part.ParentRelation.LinkingRelation).
					Where(
						Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
						CheckPermission{
							Subject:  SubjectParams(),
							Relation: part.ParentRelation.Relation,
							Object: ObjectRef{
								Type: Col{Table: "link", Column: "subject_type"},
								ID:   Col{Table: "link", Column: "subject_id"},
							},
							Visited:     Visited,
							ExpectAllow: true,
						},
					).
					Select("1").
					Limit(1)
				if len(part.ParentRelation.AllowedLinkingTypes) > 0 {
					linkQuery.WhereSubjectTypeIn(part.ParentRelation.AllowedLinkingTypes...)
				}
				parts = append(parts, Exists{Query: linkQuery})
			case part.ExcludedRelation != "":
				// Nested exclusion: (relation AND NOT excluded_relation)
				mainCheck := CheckPermission{
					Subject:     SubjectParams(),
					Relation:    part.Relation,
					Object:      LiteralObject(plan.ObjectType, ObjectID),
					Visited:     Visited,
					ExpectAllow: true,
				}
				excludeCheck := CheckPermission{
					Subject:     SubjectParams(),
					Relation:    part.ExcludedRelation,
					Object:      LiteralObject(plan.ObjectType, ObjectID),
					Visited:     Visited,
					ExpectAllow: false,
				}
				parts = append(parts, And(mainCheck, excludeCheck))
			default:
				// Direct check part
				parts = append(parts, CheckPermission{
					Subject:     SubjectParams(),
					Relation:    part.Relation,
					Object:      LiteralObject(plan.ObjectType, ObjectID),
					Visited:     Visited,
					ExpectAllow: true,
				})
			}
		}
		if len(parts) > 0 {
			exclusionChecks = append(exclusionChecks, And(parts...))
		}
	}

	if len(exclusionChecks) == 0 {
		return nil, nil
	}
	if len(exclusionChecks) == 1 {
		return exclusionChecks[0], nil
	}
	return Or(exclusionChecks...), nil
}

// buildTypedUsersetSubjectChecks builds the userset subject validation checks.
func buildTypedUsersetSubjectChecks(plan CheckPlan) (selfCheck, computedCheck SelectStmt, err error) {
	// Self check: validates when subject IS a userset with matching closure
	selfCheck = SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{
				Left:  Col{Table: "c", Column: "satisfying_relation"},
				Right: SubstringUsersetRelation{Source: SubjectID},
			},
		),
		Limit: 1,
	}

	// Computed check: validates userset subject via tuple join
	computedCheck = SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{
			{
				Type:      "INNER",
				TableExpr: ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
				On: And(
					Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
					Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Col{Table: "t", Column: "relation"}},
				),
			},
			{
				Type:      "INNER",
				TableExpr: UsersetTable(plan.Inline.UsersetRows, plan.Inline.UsersetValues, "m"),
				On: And(
					Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "m", Column: "relation"}, Right: Col{Table: "c", Column: "satisfying_relation"}},
					Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: Col{Table: "t", Column: "subject_type"}},
				),
			},
			{
				Type:      "INNER",
				TableExpr: ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "subj_c"),
				On: And(
					Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Col{Table: "t", Column: "subject_type"}},
					Eq{
						Left:  Col{Table: "subj_c", Column: "relation"},
						Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
					},
					Eq{
						Left:  Col{Table: "subj_c", Column: "satisfying_relation"},
						Right: SubstringUsersetRelation{Source: SubjectID},
					},
				),
			},
		},
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{
				Left:  UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
				Right: UsersetObjectID{Source: SubjectID},
			},
		),
		Limit: 1,
	}

	return selfCheck, computedCheck, nil
}

// buildTypedParentRelationBlocks builds TTU checks for parent relations.
func buildTypedParentRelationBlocks(plan CheckPlan) ([]ParentRelationBlock, error) {
	checks := make([]ParentRelationBlock, 0, len(plan.Analysis.ParentRelations))

	for _, parent := range plan.Analysis.ParentRelations {
		// Build the linking tuple query
		q := Tuples("link").
			ObjectType(plan.ObjectType).
			Relations(parent.LinkingRelation).
			Where(
				Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			).
			Select("link.subject_type", "link.subject_id")

		if len(parent.AllowedLinkingTypes) > 0 {
			q.WhereSubjectTypeIn(parent.AllowedLinkingTypes...)
		}

		checks = append(checks, ParentRelationBlock{
			LinkingRelation:     parent.LinkingRelation,
			ParentRelation:      parent.Relation,
			AllowedLinkingTypes: parent.AllowedLinkingTypes,
			Query:               q.Build(),
		})
	}

	return checks, nil
}

// buildTypedImpliedFunctionCalls builds function calls for complex implied relations.
func buildTypedImpliedFunctionCalls(plan CheckPlan) ([]ImpliedFunctionCheck, error) {
	calls := make([]ImpliedFunctionCheck, 0, len(plan.Analysis.ComplexClosureRelations))

	for _, rel := range plan.Analysis.ComplexClosureRelations {
		funcName := functionName(plan.ObjectType, rel)
		if plan.NoWildcard {
			funcName = functionNameNoWildcard(plan.ObjectType, rel)
		}

		check := CheckPermissionCall{
			FunctionName: funcName,
			Subject:      SubjectParams(),
			Relation:     rel,
			Object:       LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow:  true,
		}

		calls = append(calls, ImpliedFunctionCheck{
			Relation:     rel,
			FunctionName: funcName,
			Check:        check,
		})
	}

	return calls, nil
}

// buildTypedIntersectionGroups builds intersection group checks.
func buildTypedIntersectionGroups(plan CheckPlan) ([]IntersectionGroupCheck, error) {
	groups := make([]IntersectionGroupCheck, 0, len(plan.Analysis.IntersectionGroups))

	// Build visited expression that appends the current key for recursive patterns
	visitedWithKey := Param(fmt.Sprintf("p_visited || ARRAY['%s:' || p_object_id || ':%s']", plan.ObjectType, plan.Relation))

	for _, group := range plan.Analysis.IntersectionGroups {
		parts := make([]IntersectionPartCheck, 0, len(group.Parts))

		for _, part := range group.Parts {
			partCheck := IntersectionPartCheck{
				Relation:         part.Relation,
				ExcludedRelation: part.ExcludedRelation,
			}

			switch {
			case part.ParentRelation != nil:
				// TTU part
				partCheck.IsParent = true
				partCheck.ParentRelation = part.ParentRelation.Relation
				partCheck.LinkingRelation = part.ParentRelation.LinkingRelation

				// Build EXISTS check for parent relation
				q := Tuples("link").
					ObjectType(plan.ObjectType).
					Relations(part.ParentRelation.LinkingRelation).
					Where(
						Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
						CheckPermission{
							Subject:  SubjectParams(),
							Relation: part.ParentRelation.Relation,
							Object: ObjectRef{
								Type: Col{Table: "link", Column: "subject_type"},
								ID:   Col{Table: "link", Column: "subject_id"},
							},
							Visited:     visitedWithKey, // Include visited key for cycle detection
							ExpectAllow: true,
						},
					).
					Select("1").
					Limit(1)

				if len(part.ParentRelation.AllowedLinkingTypes) > 0 {
					q.WhereSubjectTypeIn(part.ParentRelation.AllowedLinkingTypes...)
				}

				partCheck.Check = Exists{Query: q}
			case part.IsThis:
				// "This" pattern - direct tuple lookup for the current relation.
				// Do NOT use check_permission_internal as it would cause infinite recursion.
				// Handle wildcards based on the part's HasWildcard flag.
				thisHasWildcard := part.HasWildcard && plan.AllowWildcard
				var subjectCheck Expr
				if thisHasWildcard {
					// Allow wildcard: match subject_id = p_subject_id OR subject_id = '*'
					subjectCheck = Or(
						Eq{Left: Col{Table: "t", Column: "subject_id"}, Right: SubjectID},
						IsWildcard{Source: Col{Table: "t", Column: "subject_id"}},
					)
				} else {
					// No wildcard: match subject_id = p_subject_id and NOT wildcard
					subjectCheck = And(
						Eq{Left: Col{Table: "t", Column: "subject_id"}, Right: SubjectID},
						NotExpr{Expr: IsWildcard{Source: Col{Table: "t", Column: "subject_id"}}},
					)
				}
				q := Tuples("t").
					ObjectType(plan.ObjectType).
					Relations(part.Relation).
					Where(
						Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
						Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
						subjectCheck,
					).
					Select("1").
					Limit(1)
				partCheck.Check = Exists{Query: q}
			default:
				// Regular computed userset part
				partCheck.Check = CheckPermission{
					Subject:     SubjectParams(),
					Relation:    part.Relation,
					Object:      LiteralObject(plan.ObjectType, ObjectID),
					Visited:     visitedWithKey, // Include visited key for cycle detection
					ExpectAllow: true,
				}
			}

			// Add exclusion check if needed
			if part.ExcludedRelation != "" {
				exclusionCheck := CheckPermission{
					Subject:     SubjectParams(),
					Relation:    part.ExcludedRelation,
					Object:      LiteralObject(plan.ObjectType, ObjectID),
					Visited:     visitedWithKey, // Include visited key for cycle detection
					ExpectAllow: false,
				}
				partCheck.Check = And(partCheck.Check, exclusionCheck)
			}

			parts = append(parts, partCheck)
		}

		groups = append(groups, IntersectionGroupCheck{Parts: parts})
	}

	return groups, nil
}

// =============================================================================
// Helper Functions
// =============================================================================

// HasDirectOrUsersetCheck returns true if the blocks have direct or userset checks.
func (b CheckBlocks) HasDirectOrUsersetCheck() bool {
	return b.DirectCheck != nil || b.UsersetCheck != nil
}

// CombinedAccessCheck returns the OR of direct and userset checks.
func (b CheckBlocks) CombinedAccessCheck() Expr {
	var checks []Expr
	if b.DirectCheck != nil {
		checks = append(checks, b.DirectCheck)
	}
	if b.UsersetCheck != nil {
		checks = append(checks, b.UsersetCheck)
	}
	if len(checks) == 0 {
		return nil
	}
	if len(checks) == 1 {
		return checks[0]
	}
	return Or(checks...)
}
