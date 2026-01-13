package sqlgen

// =============================================================================
// Self-Referential Userset Render Functions (List Subjects)
// =============================================================================
func RenderListSubjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) (string, error) {
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(
		trimTrailingSemicolon(renderSelfRefUsersetFilterQuery(blocks)),
	)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(
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
		cteBody = appendUnionAll(cteBody, formatQueryBlockSQL(recursiveBlock.Comments, recursiveBlock.Query.SQL()))
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
			FromExpr: TableAs("userset_expansion", "ue"),
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

	hasWildcardQuery := SelectStmt{
		ColumnExprs: []Expr{
			Alias{
				Expr: Raw("EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*')"),
				Name: "has_wildcard",
			},
		},
	}

	cteQuery := MultiCTE(true, []CTEDef{
		{Name: "userset_objects", Columns: []string{"userset_object_id", "depth"}, Query: Raw(usersetObjectsCTE)},
		{Name: "base_results", Query: Raw(baseResultsSQL)},
		{Name: "has_wildcard", Query: hasWildcardQuery},
	}, buildUsersetWildcardTailQuery(plan.Analysis))

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
	return baseSQL + "\n            UNION ALL\n" + recursiveSQL
}
