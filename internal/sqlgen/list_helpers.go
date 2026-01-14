package sqlgen

import (
	"strings"
)

// listUsersetPatternInput contains data for building userset pattern blocks.
// Used by both list_objects and list_subjects block builders.
type listUsersetPatternInput struct {
	SubjectType         string
	SubjectRelation     string
	SatisfyingRelations []string
	SourceRelations     []string
	SourceRelation      string
	IsClosurePattern    bool
	HasWildcard         bool
	IsComplex           bool
	IsSelfReferential   bool
}

// filterComplexClosureRelations returns complex closure relations excluding those
// that are already handled by intersection closure blocks.
func filterComplexClosureRelations(a RelationAnalysis) []string {
	if len(a.IntersectionClosureRelations) == 0 {
		return a.ComplexClosureRelations
	}
	excluded := make(map[string]bool, len(a.IntersectionClosureRelations))
	for _, rel := range a.IntersectionClosureRelations {
		excluded[rel] = true
	}
	var result []string
	for _, rel := range a.ComplexClosureRelations {
		if !excluded[rel] {
			result = append(result, rel)
		}
	}
	return result
}

// buildAllowedSubjectTypesList returns the list of allowed subject types for a relation.
// Falls back to DirectSubjectTypes if AllowedSubjectTypes is not populated.
func buildAllowedSubjectTypesList(a RelationAnalysis) []string {
	subjectTypes := a.AllowedSubjectTypes
	if len(subjectTypes) == 0 {
		subjectTypes = a.DirectSubjectTypes
	}
	if len(subjectTypes) == 0 {
		return []string{""}
	}
	return subjectTypes
}

// buildAllSatisfyingRelationsList returns all relations that satisfy the given relation.
// Falls back to just the relation itself if SatisfyingRelations is not populated.
func buildAllSatisfyingRelationsList(a RelationAnalysis) []string {
	relations := a.SatisfyingRelations
	if len(relations) == 0 {
		relations = []string{a.Relation}
	}
	return relations
}

// buildListUsersetPatternInputs builds template data for userset pattern expansion.
// Combines both direct UsersetPatterns and ClosureUsersetPatterns.
func buildListUsersetPatternInputs(a RelationAnalysis) []listUsersetPatternInput {
	if len(a.UsersetPatterns) == 0 && len(a.ClosureUsersetPatterns) == 0 {
		return nil
	}

	directRelations := buildTupleLookupRelations(a)
	patterns := make([]listUsersetPatternInput, 0, len(a.UsersetPatterns)+len(a.ClosureUsersetPatterns))

	addPattern := func(p UsersetPattern, isClosurePattern bool, sourceRelations []string) {
		satisfying := p.SatisfyingRelations
		if len(satisfying) == 0 {
			satisfying = []string{p.SubjectRelation}
		}
		patterns = append(patterns, listUsersetPatternInput{
			SubjectType:         p.SubjectType,
			SubjectRelation:     p.SubjectRelation,
			SatisfyingRelations: satisfying,
			SourceRelations:     sourceRelations,
			SourceRelation:      p.SourceRelation,
			IsClosurePattern:    isClosurePattern,
			HasWildcard:         p.HasWildcard,
			IsComplex:           p.IsComplex,
			IsSelfReferential:   p.SubjectType == a.ObjectType && p.SubjectRelation == a.Relation,
		})
	}

	for _, p := range a.UsersetPatterns {
		addPattern(p, false, directRelations)
	}
	for _, p := range a.ClosureUsersetPatterns {
		addPattern(p, true, []string{p.SourceRelation})
	}

	return patterns
}

// buildExclusionInput creates an ExclusionConfig from a RelationAnalysis.
// This configures exclusion predicates for SQL query generation.
func buildExclusionInput(a RelationAnalysis, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
		ObjectType:               a.ObjectType,
		ObjectIDExpr:             objectIDExpr,
		SubjectTypeExpr:          subjectTypeExpr,
		SubjectIDExpr:            subjectIDExpr,
		SimpleExcludedRelations:  a.SimpleExcludedRelations,
		ComplexExcludedRelations: a.ComplexExcludedRelations,
		ExcludedParentRelations:  convertParentRelations(a.ExcludedParentRelations),
		ExcludedIntersection:     convertIntersectionGroups(a.ExcludedIntersectionGroups),
	}
}

// convertParentRelations converts ParentRelationInfo slice to ExcludedParentRelation slice.
func convertParentRelations(relations []ParentRelationInfo) []ExcludedParentRelation {
	if len(relations) == 0 {
		return nil
	}
	result := make([]ExcludedParentRelation, len(relations))
	for i, rel := range relations {
		result[i] = ExcludedParentRelation(rel)
	}
	return result
}

// convertIntersectionGroups converts IntersectionGroupInfo slice to ExcludedIntersectionGroup slice.
func convertIntersectionGroups(groups []IntersectionGroupInfo) []ExcludedIntersectionGroup {
	if len(groups) == 0 {
		return nil
	}
	result := make([]ExcludedIntersectionGroup, len(groups))
	for i, group := range groups {
		parts := make([]ExcludedIntersectionPart, len(group.Parts))
		for j, part := range group.Parts {
			if part.ParentRelation != nil {
				parts[j] = ExcludedIntersectionPart{
					ParentRelation: &ExcludedParentRelation{
						Relation:            part.ParentRelation.Relation,
						LinkingRelation:     part.ParentRelation.LinkingRelation,
						AllowedLinkingTypes: part.ParentRelation.AllowedLinkingTypes,
					},
				}
			} else {
				parts[j] = ExcludedIntersectionPart{
					Relation:         part.Relation,
					ExcludedRelation: part.ExcludedRelation,
				}
			}
		}
		result[i] = ExcludedIntersectionGroup{Parts: parts}
	}
	return result
}

// buildSimpleComplexExclusionInput creates an ExclusionConfig with only simple and complex
// exclusions (no TTU or intersection exclusions).
func buildSimpleComplexExclusionInput(a RelationAnalysis, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
		ObjectType:               a.ObjectType,
		ObjectIDExpr:             objectIDExpr,
		SubjectTypeExpr:          subjectTypeExpr,
		SubjectIDExpr:            subjectIDExpr,
		SimpleExcludedRelations:  a.SimpleExcludedRelations,
		ComplexExcludedRelations: a.ComplexExcludedRelations,
	}
}

// trimTrailingSemicolon removes a trailing semicolon from a SQL string.
func trimTrailingSemicolon(input string) string {
	trimmed := strings.TrimSpace(input)
	return strings.TrimSuffix(trimmed, ";")
}

// buildWhereFromPredicates returns an And expression if predicates are non-empty, otherwise nil.
func buildWhereFromPredicates(predicates []Expr) Expr {
	if len(predicates) == 0 {
		return nil
	}
	return And(predicates...)
}

// buildUsersetWildcardTailQuery builds the wildcard handling tail as a typed query for list_subjects functions.
func buildUsersetWildcardTailQuery(a RelationAnalysis) SQLer {
	if a.Features.HasWildcard {
		// Build the wildcard handling query with permission check
		return SelectStmt{
			ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
			FromExpr:    TableAs("base_results", "br"),
			Joins: []JoinClause{
				{Type: "CROSS", Table: "has_wildcard", Alias: "hw"},
			},
			Where: Or(
				NotExpr{Expr: Col{Table: "hw", Column: "has_wildcard"}},
				Eq{Left: Col{Table: "br", Column: "subject_id"}, Right: Lit("*")},
				And(
					Ne{Left: Col{Table: "br", Column: "subject_id"}, Right: Lit("*")},
					NoWildcardPermissionCheckCall(a.Relation, a.ObjectType, Col{Table: "br", Column: "subject_id"}, ObjectID),
				),
			),
		}
	}
	return SelectStmt{
		ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
		FromExpr:    TableAs("base_results", "br"),
	}
}

// buildDispatcherBody builds the common routing logic for list dispatcher functions.
func buildDispatcherBody(cases []ListDispatcherCase, callArgs string) []Stmt {
	var stmts []Stmt
	if len(cases) > 0 {
		stmts = append(stmts, Comment{Text: "Route to specialized functions for all type/relation pairs"})
		for _, c := range cases {
			stmts = append(stmts, If{
				Cond: And(
					Eq{Left: ObjectType, Right: Lit(c.ObjectType)},
					Eq{Left: Param("p_relation"), Right: Lit(c.Relation)},
				),
				Then: []Stmt{
					ReturnQuery{Query: "SELECT * FROM " + c.FunctionName + "(" + callArgs + ")"},
					Return{},
				},
			})
		}
	}
	stmts = append(stmts,
		Comment{Text: "Unknown type/relation pair - return empty result (relation not defined in model)"},
		Comment{Text: "This matches check_permission behavior for unknown relations (returns 0/denied)"},
		Return{},
	)
	return stmts
}

// generateListObjectsDispatcher generates the list_accessible_objects dispatcher function.
func generateListObjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	cases := collectListDispatcherCases(analyses, listObjectsFunctionName)

	fn := PlpgsqlFunction{
		Name:    "list_accessible_objects",
		Args:    ListObjectsDispatcherArgs(),
		Returns: "TABLE (object_id TEXT, next_cursor TEXT) ROWS 100",
		Header: []string{
			"Generated dispatcher for list_accessible_objects",
			"Routes to specialized functions for all type/relation pairs",
		},
		Body: buildDispatcherBody(cases, "p_subject_type, p_subject_id, p_limit, p_after"),
	}
	return fn.SQL(), nil
}

// generateListSubjectsDispatcher generates the list_accessible_subjects dispatcher function.
func generateListSubjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	cases := collectListDispatcherCases(analyses, listSubjectsFunctionName)

	fn := PlpgsqlFunction{
		Name:    "list_accessible_subjects",
		Args:    ListSubjectsDispatcherArgs(),
		Returns: "TABLE (subject_id TEXT, next_cursor TEXT) ROWS 100",
		Header: []string{
			"Generated dispatcher for list_accessible_subjects",
			"Routes to specialized functions for all type/relation pairs",
		},
		Body: buildDispatcherBody(cases, "p_object_id, p_subject_type, p_limit, p_after"),
	}
	return fn.SQL(), nil
}

// collectListDispatcherCases gathers eligible analyses into dispatcher cases.
func collectListDispatcherCases(analyses []RelationAnalysis, nameFunc func(string, string) string) []ListDispatcherCase {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: nameFunc(a.ObjectType, a.Relation),
		})
	}
	return cases
}
