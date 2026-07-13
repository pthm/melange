package sqlgen

// =============================================================================
// Self-Referential Userset Render Functions (List Subjects)
// =============================================================================
func RenderListSubjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) (string, error) {
	usersetFilterPaginatedQuery := plan.wrapPaginationWildcardFirst(
		trimTrailingSemicolon(renderSelfRefUsersetFilterQuery(blocks)),
	)
	regularPaginatedQuery := plan.wrapPaginationWildcardFirst(
		trimTrailingSemicolon(renderSelfRefUsersetRegularQuery(plan, blocks)),
	)

	// Build the THEN branch (userset filter path)
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery)

	// Build the ELSE branch (regular subject type path)
	elseBranch := []Stmt{
		Comment{Text: "Regular subject type: find individual subjects via recursive userset expansion"},
		ReturnQuery{Query: regularPaginatedQuery},
	}

	// Build main IF statement: check if subject_type is a userset filter
	mainIf := If{
		Cond: Gt{
			Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
			Right: Int(0),
		},
		Then: thenBranch,
		Else: elseBranch,
	}

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header:  ListSubjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()+" (self-referential userset)"),
		Decls: []Decl{
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		},
		Body: []Stmt{
			Comment{Text: "Check if p_subject_type is a userset filter (contains '#')"},
			mainIf,
		},
	}

	return fn.SQL(), nil
}

func renderSelfRefUsersetFilterQuery(blocks SelfRefUsersetSubjectsBlockSet) string {
	baseBlocks := renderTypedQueryBlocks(blocks.UsersetFilterBlocks)
	cteBody := RenderUnionBlocks(baseBlocks)

	if blocks.UsersetFilterRecursiveBlock != nil {
		recursiveBlock := renderTypedQueryBlock(*blocks.UsersetFilterRecursiveBlock)
		cteBody = appendUnion(cteBody, formatQueryBlockSQL(recursiveBlock.Comments, recursiveBlock.Query.SQL()))
	}

	resultBlocks := []QueryBlock{{
		Comments: []string{"-- Userset filter: return normalized userset references"},
		Query: SelectStmt{
			Distinct: true,
			ColumnExprs: []Expr{
				Alias{
					Expr: Concat{Parts: []Expr{Col{Table: "ue", Column: "userset_object_id"}, Lit("#"), Param("v_filter_relation")}},
					Name: "subject_id",
				},
			},
			FromExpr: TableAs("", "userset_expansion", "ue"),
		},
	}}

	if blocks.UsersetFilterSelfBlock != nil {
		resultBlocks = append(resultBlocks, renderTypedQueryBlock(*blocks.UsersetFilterSelfBlock))
	}

	cteQuery := RecursiveCTE(
		"userset_expansion",
		[]string{"userset_object_id", "depth"},
		Raw(cteBody),
		Raw(RenderUnionBlocks(resultBlocks)),
	)

	return cteQuery.SQL()
}

func renderSelfRefUsersetRegularQuery(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) string {
	usersetObjectsCTE := buildUsersetObjectsCTE(blocks)
	baseResultsSQL := RenderUnionBlocks(renderTypedQueryBlocks(blocks.RegularBlocks))

	ctes := []CTEDef{
		{Name: "userset_objects", Columns: []string{"userset_object_id", "depth"}, Query: Raw(usersetObjectsCTE)},
		// base_results is referenced by the has_wildcard EXISTS (when emitted) and
		// the outer tail SELECT, so materialize to compute it once.
		{Name: "base_results", Query: Raw(baseResultsSQL), Materialized: plan.MaterializeCTEs()},
	}

	// The has_wildcard CTE is read only by buildUsersetWildcardTailQuery's CROSS
	// JOIN, which it emits only when Features.HasWildcard — gate the CTE on the
	// same flag so def and ref stay lockstep (an unreferenced CTE is dead codegen).
	if plan.Analysis.Features.HasWildcard {
		ctes = append(ctes, CTEDef{Name: "has_wildcard", Query: SelectStmt{
			ColumnExprs: []Expr{
				Alias{
					Expr: Raw("EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*')"),
					Name: "has_wildcard",
				},
			},
		}})
	}

	cteQuery := MultiCTE(true, ctes, buildUsersetWildcardTailQuery(plan.Analysis, plan.DatabaseSchema))

	return cteQuery.SQL()
}

func buildUsersetObjectsCTE(blocks SelfRefUsersetSubjectsBlockSet) string {
	if blocks.UsersetObjectsBaseBlock == nil {
		return ""
	}
	baseBlock := renderTypedQueryBlock(*blocks.UsersetObjectsBaseBlock)
	baseSQL := formatQueryBlockSQL(baseBlock.Comments, baseBlock.Query.SQL())

	if blocks.UsersetObjectsRecursiveBlock == nil {
		return baseSQL
	}
	recursiveBlock := renderTypedQueryBlock(*blocks.UsersetObjectsRecursiveBlock)
	recursiveSQL := formatQueryBlockSQL(recursiveBlock.Comments, recursiveBlock.Query.SQL())
	return baseSQL + "\n            UNION\n" + recursiveSQL
}
