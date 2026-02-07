package sqlgen

import "fmt"

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

func renderBulkDispatcherWithCases(cases []DispatcherCase) string {
	fn := PlpgsqlFunction{
		Name:    "check_permission_bulk",
		Args:    bulkDispatcherArgs(),
		Returns: "TABLE(idx INTEGER, allowed INTEGER)",
		Body:    []Stmt{ReturnQuery{Query: buildBulkDispatcherBody(cases)}},
		Header: []string{
			"Generated bulk dispatcher for check_permission_bulk",
			fmt.Sprintf("Routes %d (object_type, relation) pairs to specialized check functions", len(cases)),
		},
	}
	return fn.SQL() + "\n"
}

// buildBulkDispatcherBody constructs the CTE + UNION ALL query body for the bulk dispatcher.
func buildBulkDispatcherBody(cases []DispatcherCase) string {
	rSubjectType := Col{Table: "r", Column: "subject_type"}
	rSubjectID := Col{Table: "r", Column: "subject_id"}
	rObjectType := Col{Table: "r", Column: "object_type"}
	rObjectID := Col{Table: "r", Column: "object_id"}
	rRelation := Col{Table: "r", Column: "relation"}
	rIdx := Cast{Expr: Col{Table: "r", Column: "idx"}, Type: "INTEGER"}
	requestsTable := TableRef{Name: "requests", Alias: "r"}

	// One UNION ALL branch per (object_type, relation) pair, plus a fallback for unknown pairs.
	branches := make([]SQLer, 0, len(cases)+1)
	notInPairs := make([][]string, 0, len(cases))

	for _, c := range cases {
		whereClause := And(
			Eq{Left: rObjectType, Right: Lit(c.ObjectType)},
			Eq{Left: rRelation, Right: Lit(c.Relation)},
		)

		var allowedExpr Expr
		if c.Inlineable {
			// Inline check for simple direct-assignment relations (avoids function call overhead).
			allowedExpr = CaseExpr{
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
		} else {
			allowedExpr = Func{Name: c.CheckFunctionName, Args: []Expr{rSubjectType, rSubjectID, rObjectID, EmptyArray{}}}
		}

		branch := SelectStmt{
			ColumnExprs: []Expr{rIdx, allowedExpr},
			FromExpr:    requestsTable,
			Where:       whereClause,
		}

		branches = append(branches, branch)
		notInPairs = append(notInPairs, []string{c.ObjectType, c.Relation})
	}

	// Fallback branch: return 0 for unknown (object_type, relation) pairs.
	branches = append(branches, SelectStmt{
		ColumnExprs: []Expr{rIdx, Int(0)},
		FromExpr:    requestsTable,
		Where: TupleNotIn{
			Exprs: []Expr{rObjectType, rRelation},
			Pairs: notInPairs,
		},
	})

	return WithCTE{
		CTEs: []CTEDef{{
			Name:         "requests",
			Materialized: true,
			Query: Raw("SELECT t.* FROM UNNEST(p_subject_types, p_subject_ids, p_relations, p_object_types, p_object_ids)\n" +
				"    WITH ORDINALITY AS t(subject_type, subject_id, relation, object_type, object_id, idx)"),
		}},
		Query: UnionAll{Queries: branches},
	}.SQL()
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

