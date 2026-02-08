package sqlgen

import (
	"fmt"
	"sort"
)

func generateCheckFunction(a RelationAnalysis, inline InlineSQLData, noWildcard bool) (string, error) {
	plan := BuildCheckPlan(a, inline, noWildcard)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		return "", fmt.Errorf("building check blocks for %s.%s: %w", a.ObjectType, a.Relation, err)
	}
	return RenderCheckFunction(plan, blocks)
}

func generateDispatcher(analyses []RelationAnalysis, noWildcard bool) (string, error) {
	fnName := "check_permission"
	if noWildcard {
		fnName = "check_permission_no_wildcard"
	}

	cases := buildDispatcherCases(analyses, noWildcard)
	if len(cases) == 0 {
		return renderEmptyDispatcher(fnName), nil
	}
	return renderDispatcherWithCases(fnName, cases), nil
}

func buildDispatcherCases(analyses []RelationAnalysis, noWildcard bool) []DispatcherCase {
	var cases []DispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.CheckAllowed {
			continue
		}
		inlineable := isInlineable(a.Features)
		dc := DispatcherCase{
			ObjectType:        a.ObjectType,
			Relation:          a.Relation,
			CheckFunctionName: functionNameForDispatcher(a, noWildcard),
			Inlineable:        inlineable,
		}
		if inlineable {
			dc.DirectSubjectTypes = a.DirectSubjectTypes
			dc.SatisfyingRelations = a.SatisfyingRelations
		}
		cases = append(cases, dc)
	}
	return cases
}

func isInlineable(f RelationFeatures) bool {
	return f.HasDirect && !f.HasImplied && !f.HasWildcard &&
		!f.HasUserset && !f.HasRecursive && !f.HasExclusion && !f.HasIntersection
}

func renderDispatcherWithCases(fnName string, cases []DispatcherCase) string {
	caseExpr := buildDispatcherCaseExpr(cases)

	internalFn := PlpgsqlFunction{
		Name:    fnName + "_internal",
		Args:    dispatcherInternalArgs(),
		Returns: "INTEGER",
		Body: []Stmt{
			Comment{Text: "Depth limit check: prevent excessively deep permission resolution chains"},
			Comment{Text: "This catches both recursive TTU patterns and long userset chains"},
			If{
				Cond: Gte{Left: ArrayLength{Array: Visited}, Right: Int(25)},
				Then: []Stmt{Raise{Message: "resolution too complex", ErrCode: "M2002"}},
			},
			ReturnValue{Value: Raw("(SELECT " + caseExpr.SQL() + ")")},
		},
		Header: []string{
			"Generated internal dispatcher for " + fnName + "_internal",
			"Routes to specialized functions with p_visited for cycle detection in TTU patterns",
			"Enforces depth limit of 25 to prevent stack overflow from deep permission chains",
			"Phase 5: All relations use specialized functions - no generic fallback",
		},
	}

	publicFn := SqlFunction{
		Name:    fnName,
		Args:    dispatcherPublicArgs(),
		Returns: "INTEGER",
		Body:    Raw("SELECT " + fnName + "_internal(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[])"),
		Header: []string{
			"Generated dispatcher for " + fnName,
			"Routes to specialized functions for all known type/relation pairs",
		},
	}

	return internalFn.SQL() + "\n\n" + publicFn.SQL() + "\n"
}

func renderEmptyDispatcher(fnName string) string {
	internalFn := SqlFunction{
		Name:    fnName + "_internal",
		Args:    dispatcherInternalArgs(),
		Returns: "INTEGER",
		Body:    Raw("SELECT 0"),
		Header: []string{
			"Generated dispatcher for " + fnName + " (no relations defined)",
			"Returns 0 (deny) for all requests",
		},
	}

	publicFn := SqlFunction{
		Name:    fnName,
		Args:    dispatcherPublicArgs(),
		Returns: "INTEGER",
		Body:    Raw("SELECT 0"),
	}

	return internalFn.SQL() + "\n\n" + publicFn.SQL() + "\n"
}

func generateBulkDispatcher(analyses []RelationAnalysis) string {
	cases := buildDispatcherCases(analyses, false)
	if len(cases) == 0 {
		return renderEmptyBulkDispatcher()
	}
	return renderBulkDispatcherWithCases(cases)
}

func renderEmptyBulkDispatcher() string {
	fn := PlpgsqlFunction{
		Name:    "check_permission_bulk",
		Args:    bulkDispatcherArgs(),
		Returns: "TABLE(idx INTEGER, allowed INTEGER)",
		Body:    []Stmt{ReturnQuery{Query: "SELECT NULL::INTEGER, NULL::INTEGER WHERE false"}},
		Header: []string{
			"Generated bulk dispatcher for check_permission_bulk (no relations defined)",
			"Returns no rows (caller treats missing results as deny)",
		},
	}
	return fn.SQL() + "\n"
}

// bulkUnnestExpr is the shared UNNEST expression used to expand bulk request arrays
// into rows with an ordinality index.
const bulkUnnestExpr = "UNNEST(p_subject_types, p_subject_ids, p_relations, p_object_types, p_object_ids)\n" +
	"    WITH ORDINALITY AS t(subject_type, subject_id, relation, object_type, object_id, idx)"

// typeGroup groups dispatcher cases by object type for the bulk dispatcher.
type typeGroup struct {
	ObjectType string
	Cases      []DispatcherCase
}

// groupCasesByObjectType groups dispatcher cases by ObjectType, returning sorted groups.
func groupCasesByObjectType(cases []DispatcherCase) []typeGroup {
	byType := make(map[string][]DispatcherCase)
	for _, c := range cases {
		byType[c.ObjectType] = append(byType[c.ObjectType], c)
	}
	groups := make([]typeGroup, 0, len(byType))
	for ot, grouped := range byType {
		groups = append(groups, typeGroup{ObjectType: ot, Cases: grouped})
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].ObjectType < groups[j].ObjectType
	})
	return groups
}

func renderBulkDispatcherWithCases(cases []DispatcherCase) string {
	groups := groupCasesByObjectType(cases)

	// Build one IF block per object type + a final fallback RETURN QUERY.
	body := make([]Stmt, 0, len(groups)+1)
	for _, g := range groups {
		body = append(body, buildBulkTypeGroupIf(g))
	}
	body = append(body, buildBulkUnknownTypeFallback(cases))

	fn := PlpgsqlFunction{
		Name:    "check_permission_bulk",
		Args:    bulkDispatcherArgs(),
		Returns: "TABLE(idx INTEGER, allowed INTEGER)",
		Body:    body,
		Header: []string{
			"Generated bulk dispatcher for check_permission_bulk",
			fmt.Sprintf("Routes %d (object_type, relation) pairs across %d object types", len(cases), len(groups)),
			"Uses separate IF blocks to execute only branches for object types present in the batch",
		},
	}
	return fn.SQL() + "\n"
}

// buildBulkTypeGroupIf builds an IF block for a single object type group.
// The condition checks if the object type is present in p_object_types,
// then executes a RETURN QUERY with a CTE filtered to that type.
func buildBulkTypeGroupIf(g typeGroup) If {
	rSubjectType := Col{Table: "r", Column: "subject_type"}
	rSubjectID := Col{Table: "r", Column: "subject_id"}
	rObjectID := Col{Table: "r", Column: "object_id"}
	rRelation := Col{Table: "r", Column: "relation"}
	rIdx := Cast{Expr: Col{Table: "r", Column: "idx"}, Type: "INTEGER"}
	requestsTable := TableRef{Name: "requests", Alias: "r"}

	// Build UNION ALL branches for each relation of this object type.
	branches := make([]SQLer, 0, len(g.Cases)+1)
	knownRelations := make([]string, 0, len(g.Cases))

	for _, c := range g.Cases {
		allowedExpr := buildInlineCheckExpr(c, rSubjectType, rSubjectID, rObjectID)
		branch := SelectStmt{
			ColumnExprs: []Expr{rIdx, allowedExpr},
			FromExpr:    requestsTable,
			Where:       Eq{Left: rRelation, Right: Lit(c.Relation)},
		}
		branches = append(branches, branch)
		knownRelations = append(knownRelations, c.Relation)
	}

	// Per-group fallback: unknown relations for this known type return 0.
	branches = append(branches, SelectStmt{
		ColumnExprs: []Expr{rIdx, Int(0)},
		FromExpr:    requestsTable,
		Where:       NotIn{Expr: rRelation, Values: knownRelations},
	})

	// The CTE filters requests to only this object type.
	query := WithCTE{
		CTEs: []CTEDef{{
			Name:         "requests",
			Materialized: true,
			Query: Raw("SELECT t.* FROM " + bulkUnnestExpr + "\n" +
				"    WHERE t.object_type = " + Lit(g.ObjectType).SQL()),
		}},
		Query: UnionAll{Queries: branches},
	}

	return If{
		Cond: ArrayContains{Value: Lit(g.ObjectType), Array: Param("p_object_types")},
		Then: []Stmt{ReturnQuery{Query: query.SQL()}},
	}
}

// buildInlineCheckExpr builds the allowed expression for a single dispatcher case.
// For inlineable relations, it generates a CASE/EXISTS expression.
// For non-inlineable relations, it generates a function call.
func buildInlineCheckExpr(c DispatcherCase, rSubjectType, rSubjectID, rObjectID Expr) Expr {
	if !c.Inlineable {
		return Func{Name: c.CheckFunctionName, Args: []Expr{rSubjectType, rSubjectID, rObjectID, EmptyArray{}}}
	}
	return CaseExpr{
		Whens: []CaseWhen{
			{
				// Self-referential userset: subject is objectType:objectId#satisfyingRelation
				Cond: And(
					Eq{Left: rSubjectType, Right: Lit(c.ObjectType)},
					HasUserset{Source: rSubjectID},
					Eq{Left: UsersetObjectID{Source: rSubjectID}, Right: rObjectID},
					In{Expr: SubstringUsersetRelation{Source: rSubjectID}, Values: c.SatisfyingRelations},
				),
				Result: Int(1),
			},
			{
				// Direct tuple check with subject type restriction
				Cond: Exists{Query: SelectStmt{
					ColumnExprs: []Expr{Int(1)},
					FromExpr:    TableRef{Name: "melange_tuples", Alias: "t"},
					Where: And(
						Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: rSubjectType},
						Eq{Left: Col{Table: "t", Column: "subject_id"}, Right: rSubjectID},
						Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(c.Relation)},
						Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(c.ObjectType)},
						Eq{Left: Col{Table: "t", Column: "object_id"}, Right: rObjectID},
						In{Expr: rSubjectType, Values: c.DirectSubjectTypes},
					),
				}},
				Result: Int(1),
			},
		},
		Else: Int(0),
	}
}

// buildBulkUnknownTypeFallback builds the final RETURN QUERY statement that handles
// (object_type, relation) pairs not covered by any IF block.
func buildBulkUnknownTypeFallback(cases []DispatcherCase) ReturnQuery {
	notInPairs := make([][]string, 0, len(cases))
	for _, c := range cases {
		notInPairs = append(notInPairs, []string{c.ObjectType, c.Relation})
	}

	rObjectType := Col{Table: "t", Column: "object_type"}
	rRelation := Col{Table: "t", Column: "relation"}
	rIdx := Cast{Expr: Col{Table: "t", Column: "idx"}, Type: "INTEGER"}

	query := SelectStmt{
		ColumnExprs: []Expr{rIdx, Int(0)},
		From:        bulkUnnestExpr,
		Where: TupleNotIn{
			Exprs: []Expr{rObjectType, rRelation},
			Pairs: notInPairs,
		},
	}
	return ReturnQuery{Query: query.SQL()}
}

func functionNameForDispatcher(a RelationAnalysis, noWildcard bool) string {
	if noWildcard {
		return functionNameNoWildcard(a.ObjectType, a.Relation)
	}
	return functionName(a.ObjectType, a.Relation)
}

func buildDispatcherCaseExpr(cases []DispatcherCase) CaseExpr {
	whens := make([]CaseWhen, 0, len(cases))
	for _, c := range cases {
		cond := AndExpr{Exprs: []Expr{
			Eq{Left: ObjectType, Right: Lit(c.ObjectType)},
			Eq{Left: Raw("p_relation"), Right: Lit(c.Relation)},
		}}
		result := Func{
			Name: c.CheckFunctionName,
			Args: []Expr{SubjectType, SubjectID, ObjectID, Visited},
		}
		whens = append(whens, CaseWhen{Cond: cond, Result: result})
	}
	return CaseExpr{Whens: whens, Else: Int(0)}
}

func bulkDispatcherArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_types", Type: "TEXT[]"},
		{Name: "p_subject_ids", Type: "TEXT[]"},
		{Name: "p_relations", Type: "TEXT[]"},
		{Name: "p_object_types", Type: "TEXT[]"},
		{Name: "p_object_ids", Type: "TEXT[]"},
	}
}

func dispatcherPublicArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_subject_id", Type: "TEXT"},
		{Name: "p_relation", Type: "TEXT"},
		{Name: "p_object_type", Type: "TEXT"},
		{Name: "p_object_id", Type: "TEXT"},
	}
}

func dispatcherInternalArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_subject_id", Type: "TEXT"},
		{Name: "p_relation", Type: "TEXT"},
		{Name: "p_object_type", Type: "TEXT"},
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
	}
}
