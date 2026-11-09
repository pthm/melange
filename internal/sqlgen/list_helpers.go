package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
)

// composedClosureRelations identifies relations whose grants must be enumerated
// by their own list functions rather than by looking up tuples on the relation.
func composedClosureRelations(a RelationAnalysis) []string {
	return slices.Concat(a.IntersectionClosureRelations, a.ExclusionClosureRelations)
}

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
// that are already handled by composed closure blocks.
func filterComplexClosureRelations(a RelationAnalysis) []string {
	composed := composedClosureRelations(a)
	if len(composed) == 0 {
		return a.ComplexClosureRelations
	}
	excluded := make(map[string]bool, len(composed))
	for _, rel := range composed {
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
// This configures exclusion predicates for SQL query generation. g routes
// the exclusion checks (see CheckPermission) and may be nil.
func buildExclusionInput(a RelationAnalysis, g *relationGraph, databaseSchema string, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
		Graph:                    g,
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
func buildUsersetWildcardTailQuery(a RelationAnalysis, g *relationGraph, databaseSchema string) SQLer {
	if a.Features.HasWildcard {
		return SelectStmt{
			ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
			FromExpr:    TableAs("", "subjects", "br"),
			Joins: []JoinClause{
				{Type: "CROSS", Table: "has_wildcard", Alias: "hw"},
			},
			Where: wildcardSubjectsTailWhere(databaseSchema, g, a.Relation, a.ObjectType, a.Features.HasExclusion),
		}
	}
	return SelectStmt{
		ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
		FromExpr:    TableAs("", "subjects", "br"),
	}
}

// wildcardSubjectsTailWhere builds the WHERE for the list_subjects
// wildcard-completion tail (shared by the recursive and self-referential-userset
// renderers). When the base set has no wildcard, keep every row. Otherwise keep
// each subject the relation actually grants:
//   - the '*' row is verified via the FULL check (check_<t>_<r>):
//     '*”s access flows through wildcard ([user:*]) grants, so the no-wildcard
//     check would wrongly drop it — but it must still be subtracted when an
//     exclusion denies it, whether the exclusion is on this relation or reached
//     transitively through a TTU. Verifying '*' unconditionally is correct for
//     non-exclusion relations too (the check passes), so no gate is needed.
//   - concrete subjects use the no-wildcard check, because wildcard-only access
//     is already represented by the '*' row. With an exclusion, when '*' is
//     denied, the '*' row is dropped and no longer represents them, so they
//     also pass through the full check.
//
// The NOT(has_wildcard) short-circuit keeps every base row when there is no '*'.
// That is safe only when the relation has no exclusion: for an exclusion-bearing
// relation the base-set builders can over-report banned subjects (e.g. a
// self-ref userset where a direct or intermediate-group member is banned), so
// re-verification must always run. Gate the short-circuit on !hasExclusion so
// concrete subjects are always validated by the no-wildcard check and '*' by
// the full one.
func wildcardSubjectsTailWhere(schema string, g *relationGraph, relation, objectType string, hasExclusion bool) Expr {
	subject := Col{Table: "br", Column: "subject_id"}
	full := subjectCheck(schema, g, relation, objectType, subject)
	noWildcard := full
	noWildcard.NoWildcard = true
	var concrete Expr = noWildcard
	if hasExclusion {
		concrete = Or(concrete, And(wildcardDenied(schema, g, relation, objectType), full))
	}
	branches := []Expr{
		And(Eq{Left: subject, Right: Lit("*")}, full),
		And(Ne{Left: subject, Right: Lit("*")}, concrete),
	}
	if !hasExclusion {
		branches = append([]Expr{NotExpr{Expr: Col{Table: "hw", Column: "has_wildcard"}}}, branches...)
	}
	return Or(branches...)
}

// wildcardDenied is true when the full check denies '*' on the object. The
// subquery is uncorrelated, so PostgreSQL evaluates it once.
func wildcardDenied(schema string, g *relationGraph, relation, objectType string) Expr {
	check := subjectCheck(schema, g, relation, objectType, Lit("*"))
	return NotExpr{Expr: Paren{Expr: Raw("SELECT " + check.SQL())}}
}

// subjectCheck is the full check (wildcard grants included) that the
// p_subject_type subject subjectID holds relation on the object p_object_id,
// with an empty visited array: a fresh check per candidate subject.
func subjectCheck(schema string, g *relationGraph, relation, objectType string, subjectID Expr) CheckPermission {
	return CheckPermission{
		Schema:      schema,
		Subject:     SubjectRef{Type: SubjectType, ID: subjectID},
		Relation:    relation,
		Object:      LiteralObject(objectType, ObjectID),
		ExpectAllow: true,
		Graph:       g,
	}
}

// buildDispatcherBody builds the common routing logic for list dispatcher
// functions: an arm per case returning query(case), and a raise for each
// relation in unexposed.
func buildDispatcherBody(cases []ListDispatcherCase, unexposed []relationKey, query func(ListDispatcherCase) string) []Stmt {
	var stmts []Stmt
	if len(cases) > 0 || len(unexposed) > 0 {
		stmts = append(stmts, Comment{Text: "Route to specialized functions, nested by object type then relation"})
		// Group by object type (preserving order) and emit an outer IF per type
		// wrapping inner per-relation IFs — mirrors the check dispatcher, so a
		// non-matching object type skips its whole relation block in one compare.
		var order []string
		byType := make(map[string][]ListDispatcherCase)
		raiseByType := make(map[string][]string)
		for _, c := range cases {
			if _, seen := byType[c.ObjectType]; !seen {
				order = append(order, c.ObjectType)
			}
			byType[c.ObjectType] = append(byType[c.ObjectType], c)
		}
		for _, k := range unexposed {
			if _, seen := byType[k.ObjectType]; !seen {
				byType[k.ObjectType] = nil
				order = append(order, k.ObjectType)
			}
			raiseByType[k.ObjectType] = append(raiseByType[k.ObjectType], k.Relation)
		}
		for _, ot := range order {
			inner := make([]Stmt, 0, len(byType[ot]))
			for _, c := range byType[ot] {
				inner = append(inner, If{
					Cond: Eq{Left: Param("p_relation"), Right: Lit(c.Relation)},
					Then: []Stmt{
						ReturnQuery{Query: query(c)},
						Return{},
					},
				})
			}
			if rels := raiseByType[ot]; len(rels) > 0 {
				inner = append(inner, If{
					Cond: In{Expr: Param("p_relation"), Values: rels},
					Then: []Stmt{raiseNotExposed},
				})
			}
			// Known type, unknown relation → return empty immediately, mirroring
			// dispatchIfChain's per-type sentinel: a matched object type never
			// falls through to walk the remaining per-type IF blocks.
			inner = append(inner, Return{})
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

// selectAllFrom returns the arm query of a dispatcher that returns its
// case's function called with callArgs.
func selectAllFrom(callArgs string) func(ListDispatcherCase) string {
	return func(c ListDispatcherCase) string {
		return "SELECT * FROM " + sqldsl.PrefixIdent(c.FunctionName, c.DatabaseSchema) + "(" + callArgs + ")"
	}
}

// generateListObjectsDispatcher generates the list_accessible_objects dispatcher function.
func generateListObjectsDispatcher(analyses []RelationAnalysis, databaseSchema string, exposure *exposureClosure) (string, error) {
	cases := collectListDispatcherCases(analyses, listObjectsFunctionName, databaseSchema, func(objectType, relation string) bool {
		return exposure.public(familyListObjects, objectType, relation)
	})

	fn := PlpgsqlFunction{
		Schema:  databaseSchema,
		Name:    "list_accessible_objects",
		Args:    ListObjectsDispatcherArgs(),
		Returns: "TABLE (object_id TEXT, next_cursor TEXT) ROWS 100",
		Header: []string{
			"Generated dispatcher for list_accessible_objects",
			"Routes to specialized functions for all type/relation pairs",
		},
		Body: buildDispatcherBody(cases, exposure.unexposed(familyListObjects), selectAllFrom("p_subject_type, p_subject_id, p_limit, p_after")),
		// Routes only to schema-qualified list_{type}_{rel}_obj calls, no
		// unqualified melange_tuples.
		NoSearchPath: true,
	}
	return fn.SQL(), nil
}

// accessibleObjectsDispatcherName is the accessible function that takes the
// relation and object type as arguments.
const accessibleObjectsDispatcherName = "accessible_objects"

// generateAccessibleObjectsDispatcher generates accessible_objects: the
// objects list_accessible_objects returns, without pages, as a set to filter
// queries by. Unlike accessible_{type}_{relation} it is not inlined (see
// list_objects_accessible.go).
//
// It wraps list_accessible_objects while the accessible and list_objects
// families are both enabled or both disabled: list_accessible_objects then
// answers and raises for exactly the relations accessible_objects exposes,
// so the wrapper needs no guard of its own. When only one of the two is
// enabled their exposed relations differ, and no internal list-objects
// dispatcher exists to wrap, so it routes to the list functions itself, over
// its own exposed relations, and raises for the others.
func generateAccessibleObjectsDispatcher(analyses []RelationAnalysis, databaseSchema string, exposure *exposureClosure) string {
	if exposure != nil && exposure.accessible != exposure.listObjects {
		cases := collectListDispatcherCases(analyses, listObjectsFunctionName, databaseSchema, func(objectType, relation string) bool {
			return exposure.public(familyAccessible, objectType, relation)
		})
		fn := PlpgsqlFunction{
			Schema: databaseSchema,
			Name:   accessibleObjectsDispatcherName,
			Args: []FuncArg{
				{Name: "p_subject_type", Type: "TEXT"},
				{Name: "p_subject_id", Type: "TEXT"},
				{Name: "p_relation", Type: "TEXT"},
				{Name: "p_object_type", Type: "TEXT"},
			},
			Returns: "TABLE(object_id TEXT) ROWS 100",
			Header: []string{
				"Objects of p_object_type on which the subject holds p_relation, for filtering queries:",
				"WHERE id IN (SELECT * FROM " + sqldsl.PrefixIdent(accessibleObjectsDispatcherName, databaseSchema) + "(subject_type, subject_id, relation, object_type)).",
				"Routes to the exposed relations' list functions and raises for the relations not exposed.",
			},
			Body: buildDispatcherBody(cases, exposure.unexposed(familyAccessible), func(c ListDispatcherCase) string {
				return "SELECT o.object_id FROM " + sqldsl.PrefixIdent(c.FunctionName, c.DatabaseSchema) + "(p_subject_type, p_subject_id) AS o"
			}),
			NoSearchPath: true,
		}
		return fn.SQL()
	}
	return fmt.Sprintf(`-- Objects of p_object_type on which the subject holds p_relation, for filtering queries:
-- WHERE id IN (SELECT * FROM %s(subject_type, subject_id, relation, object_type)).
-- Plans in constant time; for large sets accessible_{type}_{relation}, which the
-- planner inlines, is faster.
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT) AS $$
    SELECT o.object_id FROM %s(p_subject_type, p_subject_id, p_relation, p_object_type) AS o
$$ LANGUAGE sql STABLE PARALLEL RESTRICTED;`,
		sqldsl.PrefixIdent(accessibleObjectsDispatcherName, databaseSchema),
		sqldsl.PrefixIdent(accessibleObjectsDispatcherName, databaseSchema),
		sqldsl.PrefixIdent("list_accessible_objects", databaseSchema))
}

// listSubjectsInternalDispatcherName routes every list_subjects function the
// exposure installs. Generated list_subjects functions call it instead of
// list_accessible_subjects, which answers only for exposed relations.
const listSubjectsInternalDispatcherName = "list_accessible_subjects_internal"

// generateListSubjectsDispatcher generates list_accessible_subjects_internal
// and the public list_accessible_subjects dispatcher.
func generateListSubjectsDispatcher(analyses []RelationAnalysis, databaseSchema string, exposure *exposureClosure) (string, error) {
	query := selectAllFrom("p_object_id, p_subject_type, p_limit, p_after")
	internal := PlpgsqlFunction{
		Schema:  databaseSchema,
		Name:    listSubjectsInternalDispatcherName,
		Args:    ListSubjectsDispatcherArgs(),
		Returns: "TABLE (subject_id TEXT, next_cursor TEXT) ROWS 100",
		Header: []string{
			"Generated internal dispatcher for list_accessible_subjects",
			"Routes to every generated list_subjects function; generated functions call it",
		},
		Body:         buildDispatcherBody(collectListDispatcherCases(analyses, listSubjectsFunctionName, databaseSchema, exposure.installsListSubjects), nil, query),
		NoSearchPath: true,
	}
	fn := PlpgsqlFunction{
		Schema:  databaseSchema,
		Name:    "list_accessible_subjects",
		Args:    ListSubjectsDispatcherArgs(),
		Returns: "TABLE (subject_id TEXT, next_cursor TEXT) ROWS 100",
		Header: []string{
			"Generated dispatcher for list_accessible_subjects",
			"Routes to specialized functions for all type/relation pairs",
		},
		Body: buildDispatcherBody(collectListDispatcherCases(analyses, listSubjectsFunctionName, databaseSchema, func(objectType, relation string) bool {
			return exposure.public(familyListSubjects, objectType, relation)
		}), exposure.unexposed(familyListSubjects), query),
		// Routes only to schema-qualified list_{type}_{rel}_sub calls, no
		// unqualified melange_tuples.
		NoSearchPath: true,
	}
	return internal.SQL() + "\n\n" + fn.SQL(), nil
}

// collectListDispatcherCases gathers the ListAllowed analyses include
// accepts into dispatcher cases.
func collectListDispatcherCases(analyses []RelationAnalysis, nameFunc func(string, string) string, databaseSchema string, include func(objectType, relation string) bool) []ListDispatcherCase {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed || !include(a.ObjectType, a.Relation) {
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

// closureCoverage chooses which of a relation's closure relations its list
// function obtains by composing their own (exact) list function, and which
// closure relations those compositions cover, so per-arm blocks derived from
// a covered relation can be skipped. From the composable candidates it drops
// a relation R when another candidate S has R in its satisfying closure and S
// excludes nothing, so S's results include R's (as for ImpliedCallees; when
// two imply each other the lexically greater is dropped). covered holds each
// composed relation and, for exclusion-free ones, its satisfying closure. It
// never contains the listed relation itself.
func closureCoverage(plan ListPlan, candidates []string, composable func(string) bool) (compose []string, covered map[string]bool) {
	if plan.AnalysisLookup == nil {
		return nil, nil
	}
	var usable []string
	for _, r := range candidates {
		if r != "" && r != plan.Relation && !slices.Contains(usable, r) && composable(r) {
			usable = append(usable, r)
		}
	}
	analysis := func(r string) *RelationAnalysis { return plan.AnalysisLookup[plan.ObjectType+"."+r] }
	covers := func(s, r string) bool {
		sa := analysis(s)
		return sa != nil && !sa.Features.HasExclusion && slices.Contains(sa.SatisfyingRelations, r)
	}
	covered = make(map[string]bool)
	for _, r := range usable {
		if slices.ContainsFunc(usable, func(s string) bool {
			return s != r && covers(s, r) && (!covers(r, s) || s < r)
		}) {
			continue
		}
		compose = append(compose, r)
		covered[r] = true
		if ra := analysis(r); ra != nil && !ra.Features.HasExclusion {
			for _, x := range ra.SatisfyingRelations {
				covered[x] = true
			}
		}
	}
	delete(covered, plan.Relation)
	return compose, covered
}
