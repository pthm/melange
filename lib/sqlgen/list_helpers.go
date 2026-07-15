package sqlgen

import (
	"strings"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
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
func buildExclusionInput(a RelationAnalysis, databaseSchema string, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
		DatabaseSchema:           databaseSchema,
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
		result[i] = ExcludedParentRelation{
			Relation:            rel.Relation,
			LinkingRelation:     rel.LinkingRelation,
			AllowedLinkingTypes: rel.AllowedLinkingTypes,
		}
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
func buildSimpleComplexExclusionInput(a RelationAnalysis, databaseSchema string, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
		DatabaseSchema:           databaseSchema,
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
func buildUsersetWildcardTailQuery(a RelationAnalysis, databaseSchema string) SQLer {
	if a.Features.HasWildcard {
		return SelectStmt{
			ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
			FromExpr:    TableAs("", "base_results", "br"),
			Joins: []JoinClause{
				{Type: "CROSS", Table: "has_wildcard", Alias: "hw"},
			},
			Where: wildcardSubjectsTailWhere(databaseSchema, a.Relation, a.ObjectType, a.Features.HasExclusion),
		}
	}
	return SelectStmt{
		ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
		FromExpr:    TableAs("", "base_results", "br"),
	}
}

// wildcardSubjectsTailWhere builds the WHERE for the list_subjects
// wildcard-completion tail (shared by the recursive and self-referential-userset
// renderers). When the base set has no wildcard, keep every row. Otherwise keep
// each subject the relation actually grants:
//   - the '*' row is verified via the FULL check (check_permission_internal):
//     '*”s access flows through wildcard ([user:*]) grants, so the no-wildcard
//     check would wrongly drop it — but it must still be subtracted when an
//     exclusion denies it, whether the exclusion is on this relation or reached
//     transitively through a TTU. Verifying '*' unconditionally is correct for
//     non-exclusion relations too (the check passes), so no gate is needed.
//   - concrete subjects use the no-wildcard check, because wildcard-only access
//     is already represented by the '*' row.
//
// The NOT(has_wildcard) short-circuit keeps every base row when there is no '*'.
// That is safe only when the relation has no exclusion: for an exclusion-bearing
// relation the base-set builders can over-report banned subjects (e.g. a
// self-ref userset where a direct or intermediate-group member is banned), so
// re-verification must always run. Gate the short-circuit on !hasExclusion so
// concrete subjects are always validated by check_permission_nw_internal and
// '*' by check_permission_internal.
func wildcardSubjectsTailWhere(schema, relation, objectType string, hasExclusion bool) Expr {
	subject := Col{Table: "br", Column: "subject_id"}
	branches := []Expr{
		And(
			Eq{Left: subject, Right: Lit("*")},
			WildcardPermissionCheckCall(schema, relation, objectType, subject, ObjectID),
		),
		And(
			Ne{Left: subject, Right: Lit("*")},
			NoWildcardPermissionCheckCall(schema, relation, objectType, subject, ObjectID),
		),
	}
	if !hasExclusion {
		branches = append([]Expr{NotExpr{Expr: Col{Table: "hw", Column: "has_wildcard"}}}, branches...)
	}
	return Or(branches...)
}

// buildDispatcherBody builds the common routing logic for list dispatcher functions.
func buildDispatcherBody(cases []ListDispatcherCase, callArgs string) []Stmt {
	var stmts []Stmt
	if len(cases) > 0 {
		stmts = append(stmts, Comment{Text: "Route to specialized functions, nested by object type then relation"})
		// Group by object type (preserving order) and emit an outer IF per type
		// wrapping inner per-relation IFs — mirrors the check dispatcher, so a
		// non-matching object type skips its whole relation block in one compare.
		var order []string
		byType := make(map[string][]ListDispatcherCase)
		for _, c := range cases {
			if _, seen := byType[c.ObjectType]; !seen {
				order = append(order, c.ObjectType)
			}
			byType[c.ObjectType] = append(byType[c.ObjectType], c)
		}
		for _, ot := range order {
			inner := make([]Stmt, 0, len(byType[ot]))
			for _, c := range byType[ot] {
				inner = append(inner, If{
					Cond: Eq{Left: Param("p_relation"), Right: Lit(c.Relation)},
					Then: []Stmt{
						ReturnQuery{Query: "SELECT * FROM " + sqldsl.PrefixIdent(c.FunctionName, c.DatabaseSchema) + "(" + callArgs + ")"},
						Return{},
					},
				})
			}
			stmts = append(stmts, If{
				Cond: Eq{Left: ObjectType, Right: Lit(ot)},
				Then: inner,
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
func generateListObjectsDispatcher(analyses []RelationAnalysis, databaseSchema string) (string, error) {
	cases := collectListDispatcherCases(analyses, listObjectsFunctionName, databaseSchema)

	fn := PlpgsqlFunction{
		Schema:  databaseSchema,
		Name:    "list_accessible_objects",
		Args:    ListObjectsDispatcherArgs(),
		Returns: "TABLE (object_id TEXT, next_cursor TEXT) ROWS 100",
		Header: []string{
			"Generated dispatcher for list_accessible_objects",
			"Routes to specialized functions for all type/relation pairs",
		},
		Body: buildDispatcherBody(cases, "p_subject_type, p_subject_id, p_limit, p_after"),
		// Routes only to schema-qualified list_{type}_{rel}_obj calls, no
		// unqualified melange_tuples.
		NoSearchPath: true,
	}
	return fn.SQL(), nil
}

// generateListSubjectsDispatcher generates the list_accessible_subjects dispatcher function.
func generateListSubjectsDispatcher(analyses []RelationAnalysis, databaseSchema string) (string, error) {
	cases := collectListDispatcherCases(analyses, listSubjectsFunctionName, databaseSchema)

	fn := PlpgsqlFunction{
		Schema:  databaseSchema,
		Name:    "list_accessible_subjects",
		Args:    ListSubjectsDispatcherArgs(),
		Returns: "TABLE (subject_id TEXT, next_cursor TEXT) ROWS 100",
		Header: []string{
			"Generated dispatcher for list_accessible_subjects",
			"Routes to specialized functions for all type/relation pairs",
		},
		Body: buildDispatcherBody(cases, "p_object_id, p_subject_type, p_limit, p_after"),
		// Routes only to schema-qualified list_{type}_{rel}_sub calls, no
		// unqualified melange_tuples.
		NoSearchPath: true,
	}
	return fn.SQL(), nil
}

// collectListDispatcherCases gathers eligible analyses into dispatcher cases.
func collectListDispatcherCases(analyses []RelationAnalysis, nameFunc func(string, string) string, databaseSchema string) []ListDispatcherCase {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			DatabaseSchema: databaseSchema,
			ObjectType:     a.ObjectType,
			Relation:       a.Relation,
			FunctionName:   nameFunc(a.ObjectType, a.Relation),
		})
	}
	return cases
}
